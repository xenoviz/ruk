package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testAddress = "127.0.0.1:47213"

var testToken = strings.Repeat("ab", 32)

type fakeSource struct {
	mu          sync.Mutex
	actions     []Action
	snapshotErr error
	actErr      error
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	delay       time.Duration
}

func (source *fakeSource) Snapshot(context.Context) (Snapshot, error) {
	if source.snapshotErr != nil {
		return Snapshot{}, source.snapshotErr
	}
	lifecycle := "assigned"
	return Snapshot{GeneratedAt: "2026-09-26T10:00:00Z", Host: "dev", Version: "test", Repositories: []Repository{{
		Name: "app", Root: "/repo/app", CommonDir: "/repo/app/.git",
		Workspaces: []Workspace{{Path: "/repo/ws", Branch: "agent/x", Lifecycle: &lifecycle, Ports: map[string]int64{}, Processes: []Process{}}},
	}}}, nil
}

func (source *fakeSource) Act(_ context.Context, action Action) (ActionResult, error) {
	current := source.inFlight.Add(1)
	defer source.inFlight.Add(-1)
	for {
		seen := source.maxInFlight.Load()
		if current <= seen || source.maxInFlight.CompareAndSwap(seen, current) {
			break
		}
	}
	time.Sleep(source.delay)
	source.mu.Lock()
	source.actions = append(source.actions, action)
	source.mu.Unlock()
	if source.actErr != nil {
		return ActionResult{}, source.actErr
	}
	return ActionResult{Result: json.RawMessage(`{"status":"renewed"}`)}, nil
}

func newTestHandler(t *testing.T, source Source) *Handler {
	t.Helper()
	handler, err := NewHandler(source, testToken, testAddress)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func request(method, target, body string, mutate func(*http.Request)) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Host = testAddress
	req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	if method == http.MethodPost {
		req.Header.Set("Origin", "http://"+testAddress)
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	return req
}

func serve(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func withoutCookie(req *http.Request) { req.Header.Del("Cookie") }

func TestNewHandlerRejectsUnsafeConfiguration(t *testing.T) {
	cases := map[string]struct {
		source  Source
		token   string
		address string
	}{
		"missing source": {nil, testToken, testAddress},
		"short token":    {&fakeSource{}, "abc", testAddress},
		"public address": {&fakeSource{}, testToken, "0.0.0.0:47213"},
		"lan address":    {&fakeSource{}, testToken, "192.168.1.5:47213"},
		"host name":      {&fakeSource{}, testToken, "localhost:47213"},
		"no port":        {&fakeSource{}, testToken, "127.0.0.1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHandler(tc.source, tc.token, tc.address); err == nil {
				t.Fatal("NewHandler() error = nil, want rejection")
			}
		})
	}
}

func TestTokenExchangeSetsSessionCookieAndRedirects(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{})
	response := serve(handler, request(http.MethodGet, "/?token="+testToken, "", withoutCookie))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatalf("status = %d location = %q, want 303 to /", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != testToken || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies = %+v, want one HttpOnly SameSite=Strict session cookie", cookies)
	}
}

func TestInvalidTokenAndMissingCookieAreRejected(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{})
	for _, target := range []string{"/?token=" + strings.Repeat("cd", 32), "/", "/app.js", "/api/snapshot"} {
		response := serve(handler, request(http.MethodGet, target, "", withoutCookie))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want 401", target, response.Code)
		}
	}
	wrong := func(req *http.Request) {
		withoutCookie(req)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: strings.Repeat("cd", 32)})
	}
	if response := serve(handler, request(http.MethodGet, "/api/snapshot", "", wrong)); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong cookie status = %d, want 401", response.Code)
	}
}

func TestUnknownHostIsRejectedBeforeAuthentication(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{})
	for _, host := range []string{"evil.example:47213", "127.0.0.1:1", "localhost", ""} {
		response := serve(handler, request(http.MethodGet, "/?token="+testToken, "", func(req *http.Request) { req.Host = host }))
		if response.Code != http.StatusForbidden {
			t.Errorf("host %q status = %d, want 403", host, response.Code)
		}
	}
	response := serve(handler, request(http.MethodGet, "/api/snapshot", "", func(req *http.Request) { req.Host = "localhost:47213" }))
	if response.Code != http.StatusOK {
		t.Fatalf("localhost alias status = %d, want 200", response.Code)
	}
}

func TestEveryResponseCarriesSecurityHeaders(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{})
	for _, req := range []*http.Request{
		request(http.MethodGet, "/", "", nil),
		request(http.MethodGet, "/api/snapshot", "", withoutCookie),
		request(http.MethodGet, "/", "", func(r *http.Request) { r.Host = "evil.example" }),
	} {
		response := serve(handler, req)
		for name, want := range securityHeaders {
			if got := response.Header().Get(name); got != want {
				t.Errorf("%s %s: header %s = %q, want %q", req.Method, req.URL.Path, name, got, want)
			}
		}
	}
}

func TestServesEmbeddedAssets(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{})
	for path, contentType := range map[string]string{"/": "text/html", "/app.js": "javascript", "/app.css": "text/css", "/fonts/dm-sans.woff2": "font/woff2", "/fonts/space-grotesk.woff2": "font/woff2"} {
		response := serve(handler, request(http.MethodGet, path, "", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), contentType) || response.Body.Len() == 0 {
			t.Errorf("GET %s = %d %q (%d bytes)", path, response.Code, response.Header().Get("Content-Type"), response.Body.Len())
		}
	}
	for _, path := range []string{"/assets/index.html", "/fonts/DM-Sans-OFL.txt", "/fonts/../app.js"} {
		if response := serve(handler, request(http.MethodGet, path, "", nil)); response.Code != http.StatusNotFound {
			t.Errorf("unlisted asset %s status = %d, want 404", path, response.Code)
		}
	}
	if response := serve(handler, request(http.MethodPost, "/", "{}", nil)); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST / status = %d, want 405", response.Code)
	}
}

func TestIndexLoadsOnlySameOriginFiles(t *testing.T) {
	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, forbidden := range []string{"http://", "https://", "<style", "style=", "onclick", "<script>"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("index.html contains %q, which the content security policy blocks or forbids", forbidden)
		}
	}
}

func TestStylesheetUsesOnlyTheTwoEmbeddedTypefaces(t *testing.T) {
	css, err := assets.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	faces := strings.Count(string(css), "@font-face")
	if faces != 2 || strings.Contains(string(css), "monospace") || strings.Contains(string(css), "http") {
		t.Fatalf("app.css declares %d font faces (want 2), or references a monospace or remote font", faces)
	}
	for _, license := range []string{"assets/fonts/DM-Sans-OFL.txt", "assets/fonts/Space-Grotesk-OFL.txt"} {
		text, err := assets.ReadFile(license)
		if err != nil || !strings.Contains(string(text), "SIL Open Font License") {
			t.Errorf("%s must ship with the embedded font (%v)", license, err)
		}
	}
}

func TestSnapshotReturnsSourceJSON(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{})
	response := serve(handler, request(http.MethodGet, "/api/snapshot", "", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d content type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	var snapshot Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("snapshot is not one JSON value: %v", err)
	}
	if len(snapshot.Repositories) != 1 || snapshot.Repositories[0].Workspaces[0].Branch != "agent/x" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestSnapshotFailureIsReportedAsJSON(t *testing.T) {
	handler := newTestHandler(t, &fakeSource{snapshotErr: errors.New("state is invalid")})
	response := serve(handler, request(http.MethodGet, "/api/snapshot", "", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Message != "state is invalid" {
		t.Fatalf("body = %s (%v)", response.Body.String(), err)
	}
}

func TestActionForwardsValidatedRequest(t *testing.T) {
	source := &fakeSource{}
	handler := newTestHandler(t, source)
	response := serve(handler, request(http.MethodPost, "/api/actions", `{"kind":"release","repository":"/repo/app","assignmentId":"id-1","force":true}`, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if want := (Action{Kind: ActionRelease, Repository: "/repo/app", AssignmentID: "id-1", Force: true}); len(source.actions) != 1 || source.actions[0] != want {
		t.Fatalf("actions = %+v, want %+v", source.actions, want)
	}
	if !strings.Contains(response.Body.String(), `"result":{"status":"renewed"}`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestActionRequiresSameOriginJSON(t *testing.T) {
	source := &fakeSource{}
	handler := newTestHandler(t, source)
	body := `{"kind":"gc-preview","repository":"/repo/app"}`
	cases := map[string]struct {
		mutate func(*http.Request)
		status int
	}{
		"missing origin":   {func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden},
		"foreign origin":   {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		"other port":       {func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") }, http.StatusForbidden},
		"form post":        {func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, http.StatusUnsupportedMediaType},
		"text post":        {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		"no session":       {withoutCookie, http.StatusUnauthorized},
		"wrong method":     {func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
		"localhost origin": {func(r *http.Request) { r.Header.Set("Origin", "http://localhost:47213") }, http.StatusOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			response := serve(handler, request(http.MethodPost, "/api/actions", body, tc.mutate))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", response.Code, tc.status, response.Body.String())
			}
		})
	}
	if len(source.actions) != 1 {
		t.Fatalf("source ran %d actions, want only the localhost-origin request", len(source.actions))
	}
}

func TestActionRejectsMalformedRequests(t *testing.T) {
	source := &fakeSource{}
	handler := newTestHandler(t, source)
	cases := map[string]string{
		"not json":           `release`,
		"unknown field":      `{"kind":"renew","repository":"/r","assignmentId":"a","ttl":5}`,
		"two values":         `{"kind":"gc-preview","repository":"/r"}{"kind":"gc-preview","repository":"/r"}`,
		"unknown kind":       `{"kind":"shell","repository":"/r"}`,
		"missing repository": `{"kind":"gc-preview"}`,
		"renew without id":   `{"kind":"renew","repository":"/r"}`,
		"release with path":  `{"kind":"release","repository":"/r","assignmentId":"a","path":"/x"}`,
		"remove without":     `{"kind":"remove","repository":"/r"}`,
		"remove with id":     `{"kind":"remove","repository":"/r","path":"/x","assignmentId":"a"}`,
		"gc with path":       `{"kind":"gc-apply","repository":"/r","path":"/x"}`,
		"force on renew":     `{"kind":"renew","repository":"/r","assignmentId":"a","force":true}`,
		"force-expired prev": `{"kind":"gc-preview","repository":"/r","forceExpired":true}`,
		"ttl on release":     `{"kind":"release","repository":"/r","assignmentId":"a","ttlMinutes":5}`,
		"negative ttl":       `{"kind":"renew","repository":"/r","assignmentId":"a","ttlMinutes":-1}`,
		"too large":          `{"kind":"gc-preview","repository":"` + strings.Repeat("x", maxRequestBody) + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := serve(handler, request(http.MethodPost, "/api/actions", body, nil))
			var decoded errorResponse
			if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &decoded) != nil || decoded.Error.Code != "INVALID_ARGUMENT" {
				t.Fatalf("status = %d body = %s, want 400 INVALID_ARGUMENT", response.Code, response.Body.String())
			}
		})
	}
	if len(source.actions) != 0 {
		t.Fatalf("source ran %d actions for invalid requests", len(source.actions))
	}
}

func TestActionFailureKeepsCommandErrorRecord(t *testing.T) {
	source := &fakeSource{actErr: &ActionError{Code: "ASSIGNMENT_CONFLICT", Message: "Assignment belongs to another owner", Recovery: "ruk release a --force"}}
	handler := newTestHandler(t, source)
	response := serve(handler, request(http.MethodPost, "/api/actions", `{"kind":"release","repository":"/r","assignmentId":"a"}`, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || *body.Error != *source.actErr.(*ActionError) {
		t.Fatalf("body = %s (%v)", response.Body.String(), err)
	}
}

func TestActionsRunOneAtATimeWhileSnapshotsStayAvailable(t *testing.T) {
	source := &fakeSource{delay: 20 * time.Millisecond}
	handler := newTestHandler(t, source)
	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			response := serve(handler, request(http.MethodPost, "/api/actions", `{"kind":"gc-preview","repository":"/r"}`, nil))
			if response.Code != http.StatusOK {
				t.Errorf("action status = %d", response.Code)
			}
		}()
		go func() {
			defer wg.Done()
			if response := serve(handler, request(http.MethodGet, "/api/snapshot", "", nil)); response.Code != http.StatusOK {
				t.Errorf("snapshot status = %d", response.Code)
			}
		}()
	}
	wg.Wait()
	if got := source.maxInFlight.Load(); got != 1 {
		t.Fatalf("max concurrent actions = %d, want 1", got)
	}
	if len(source.actions) != 8 {
		t.Fatalf("actions = %d, want 8", len(source.actions))
	}
}

func TestServeStopsOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(&fakeSource{}, testToken, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handler) }()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("http://" + listener.Addr().String() + "/?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("live server status = %d, want 303", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not stop after cancellation")
	}
}

func TestServeStopsDespiteConnectionsThatNeverSendARequest(t *testing.T) {
	// Browsers open speculative connections that may never carry a request.
	// http.Server.Shutdown treats such connections as active for seconds, so
	// stopping must not wait for them or report an error.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(&fakeSource{}, testToken, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handler) }()
	idle, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	time.Sleep(50 * time.Millisecond) // let the server accept the connection
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() waited for a connection that never sent a request")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Serve() took %s to stop; an idle connection must not use the %s grace period", elapsed, shutdownGrace)
	}
}

func TestServeLetsARunningActionFinishBeforeStopping(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	source := &fakeSource{delay: 400 * time.Millisecond}
	handler, err := NewHandler(source, testToken, address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, handler) }()

	responded := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://"+address+"/api/actions", strings.NewReader(`{"kind":"gc-apply","repository":"/r"}`))
		req.Header.Set("Origin", "http://"+address)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			responded <- 0
			return
		}
		_ = response.Body.Close()
		responded <- response.StatusCode
	}()
	for source.inFlight.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if status := <-responded; status != http.StatusOK {
		t.Fatalf("running action status = %d, want it to finish with 200", status)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if len(source.actions) != 1 {
		t.Fatalf("actions completed = %d, want 1", len(source.actions))
	}
}

func TestNewTokenIsRandomHex(t *testing.T) {
	first, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewToken()
	if len(first) != 64 || first == second || strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("tokens %q and %q are not distinct 64-character hex strings", first, second)
	}
}

func TestBrowserCommandPerPlatform(t *testing.T) {
	url := "http://127.0.0.1:1/?token=abc"
	for goos, want := range map[string][]string{
		"darwin":  {"open", url},
		"windows": {"rundll32", "url.dll,FileProtocolHandler", url},
		"linux":   {"xdg-open", url},
	} {
		command, err := browserCommand(goos, url)
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if strings.Join(command.Args, " ") != strings.Join(want, " ") {
			t.Errorf("%s args = %q, want %q", goos, command.Args, want)
		}
	}
	if _, err := browserCommand("plan9", url); err == nil {
		t.Fatal("unsupported platform returned no error")
	}
}
