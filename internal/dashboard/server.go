package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed assets
var assets embed.FS

const (
	cookieName     = "ruk_ui"
	maxRequestBody = 64 << 10
)

// shutdownGrace bounds how long a stopping server waits for running requests.
// It is a variable so tests can shorten it.
var shutdownGrace = 5 * time.Second

// securityHeaders apply to every response. The page loads only its own
// embedded files, so the policy allows nothing from other origins.
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Referrer-Policy":         "no-referrer",
	"Cache-Control":           "no-store",
}

// contentTypes is explicit so responses do not depend on the host's MIME
// registry, which on some systems maps .js to text/plain.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
}

// NewToken returns a random session token for one dashboard process.
func NewToken() (string, error) {
	var buffer [32]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate dashboard token: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}

// Handler serves the dashboard for one loopback address.
//
// A request must name the exact loopback host and port it was served on, which
// defeats DNS rebinding. The one-time URL token is exchanged for a same-site,
// HTTP-only cookie, and every other request must carry that cookie. Actions
// also require a matching Origin and a JSON body, so another site open in the
// same browser cannot trigger them. Actions run one at a time.
type Handler struct {
	source  Source
	token   string
	hosts   map[string]bool
	origins map[string]bool
	actions sync.Mutex
}

// NewHandler builds the handler for a server listening on address, which must
// be a loopback host and port such as 127.0.0.1:47213.
func NewHandler(source Source, token, address string) (*Handler, error) {
	if source == nil {
		return nil, errors.New("dashboard source is required")
	}
	if len(token) < 32 {
		return nil, errors.New("dashboard token is too short")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("dashboard address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("dashboard address %q is not a loopback address", address)
	}
	hosts := map[string]bool{net.JoinHostPort(host, port): true, net.JoinHostPort("localhost", port): true}
	origins := make(map[string]bool, len(hosts))
	for allowed := range hosts {
		origins["http://"+allowed] = true
	}
	return &Handler{source: source, token: token, hosts: hosts, origins: origins}, nil
}

// ServeHTTP implements http.Handler.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	for name, value := range securityHeaders {
		writer.Header().Set(name, value)
	}
	if !handler.hosts[request.Host] {
		http.Error(writer, "Unknown host", http.StatusForbidden)
		return
	}
	if request.URL.Path == "/" && request.Method == http.MethodGet && request.URL.Query().Has("token") {
		handler.exchangeToken(writer, request)
		return
	}
	if !handler.authorized(request) {
		http.Error(writer, "Open the address printed by ruk ui.", http.StatusUnauthorized)
		return
	}
	switch request.URL.Path {
	case "/":
		handler.asset(writer, request, "index.html")
	case "/app.css":
		handler.asset(writer, request, "app.css")
	case "/app.js":
		handler.asset(writer, request, "app.js")
	case "/fonts/dm-sans.woff2":
		handler.asset(writer, request, "fonts/dm-sans.woff2")
	case "/fonts/space-grotesk.woff2":
		handler.asset(writer, request, "fonts/space-grotesk.woff2")
	case "/api/snapshot":
		handler.snapshot(writer, request)
	case "/api/actions":
		handler.act(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (handler *Handler) exchangeToken(writer http.ResponseWriter, request *http.Request) {
	if !handler.matches(request.URL.Query().Get("token")) {
		http.Error(writer, "Invalid or expired dashboard address. Open the address printed by ruk ui.", http.StatusUnauthorized)
		return
	}
	http.SetCookie(writer, &http.Cookie{Name: cookieName, Value: handler.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}

func (handler *Handler) authorized(request *http.Request) bool {
	cookie, err := request.Cookie(cookieName)
	return err == nil && handler.matches(cookie.Value)
}

func (handler *Handler) matches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(handler.token)) == 1
}

func (handler *Handler) asset(writer http.ResponseWriter, request *http.Request, name string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	content, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", contentTypes[name[strings.LastIndex(name, "."):]])
	_, _ = writer.Write(content)
}

func (handler *Handler) snapshot(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	snapshot, err := handler.source.Snapshot(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (handler *Handler) act(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !handler.origins[request.Header.Get("Origin")] {
		writeJSON(writer, http.StatusForbidden, errorBody(&ActionError{Code: "FORBIDDEN", Message: "Actions must come from the dashboard page"}))
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(writer, http.StatusUnsupportedMediaType, errorBody(&ActionError{Code: "INVALID_ARGUMENT", Message: "Actions must be sent as JSON"}))
		return
	}
	action, err := decodeAction(http.MaxBytesReader(writer, request.Body, maxRequestBody))
	if err != nil {
		writeError(writer, err)
		return
	}
	handler.actions.Lock()
	defer handler.actions.Unlock()
	result, err := handler.source.Act(request.Context(), action)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func decodeAction(body io.Reader) (Action, error) {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var action Action
	if err := decoder.Decode(&action); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return Action{}, &ActionError{Code: "INVALID_ARGUMENT", Message: "Action request is too large"}
		}
		return Action{}, &ActionError{Code: "INVALID_ARGUMENT", Message: "Action request is not valid JSON"}
	}
	if decoder.More() {
		return Action{}, &ActionError{Code: "INVALID_ARGUMENT", Message: "Action request must contain one JSON object"}
	}
	return action, ValidateAction(action)
}

// ValidateAction rejects requests whose fields do not fit their kind, before
// any command runs.
func ValidateAction(action Action) error {
	invalid := func(message string) error { return &ActionError{Code: "INVALID_ARGUMENT", Message: message} }
	if strings.TrimSpace(action.Repository) == "" {
		return invalid("repository is required")
	}
	switch action.Kind {
	case ActionRenew, ActionRelease:
		if strings.TrimSpace(action.AssignmentID) == "" {
			return invalid(string(action.Kind) + " requires an assignment ID")
		}
		if action.Path != "" {
			return invalid(string(action.Kind) + " does not accept a path")
		}
	case ActionRemove:
		if strings.TrimSpace(action.Path) == "" {
			return invalid("remove requires a workspace path")
		}
		if action.AssignmentID != "" {
			return invalid("remove does not accept an assignment ID")
		}
	case ActionGCPreview, ActionGCApply, ActionDisk:
		if action.AssignmentID != "" || action.Path != "" {
			return invalid(string(action.Kind) + " applies to a whole repository")
		}
	default:
		return invalid(fmt.Sprintf("unknown action %q", action.Kind))
	}
	if action.Force && action.Kind != ActionRelease {
		return invalid("force applies only to release")
	}
	if action.ForceExpired && action.Kind != ActionGCApply {
		return invalid("forceExpired applies only to gc-apply")
	}
	if action.TTLMinutes < 0 {
		return invalid("ttlMinutes must not be negative")
	}
	if action.TTLMinutes != 0 && action.Kind != ActionRenew {
		return invalid("ttlMinutes applies only to renew")
	}
	return nil
}

type errorResponse struct {
	Error *ActionError `json:"error"`
}

func errorBody(err *ActionError) errorResponse { return errorResponse{Error: err} }

func writeError(writer http.ResponseWriter, err error) {
	var actionErr *ActionError
	if errors.As(err, &actionErr) {
		status := http.StatusConflict
		if actionErr.Code == "INVALID_ARGUMENT" {
			status = http.StatusBadRequest
		}
		writeJSON(writer, status, errorBody(actionErr))
		return
	}
	writeJSON(writer, http.StatusInternalServerError, errorBody(&ActionError{Code: "OPERATION_FAILED", Message: err.Error()}))
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "encode response", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	http.Error(writer, "Method not allowed", http.StatusMethodNotAllowed)
}

// Serve runs the dashboard on listener until ctx is cancelled. On
// cancellation it stops at once when no request is running; an action that is
// already running gets up to the grace period to finish instead of being cut
// off mid-transition. Request contexts do not derive from ctx for the same
// reason. Connections that never send a request, which browsers open
// speculatively, do not delay the stop. It returns nil after a requested stop.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	var active atomic.Int64
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			active.Add(1)
			defer active.Add(-1)
			handler.ServeHTTP(writer, request)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	select {
	case err := <-failed:
		return fmt.Errorf("serve dashboard: %w", err)
	case <-ctx.Done():
	}
	server.SetKeepAlivesEnabled(false)
	deadline := time.Now().Add(shutdownGrace)
	for active.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	closeErr := server.Close()
	if err := <-failed; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve dashboard: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("stop dashboard: %w", closeErr)
	}
	return nil
}
