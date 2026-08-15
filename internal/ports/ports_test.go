package ports

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeNameUsesStableRukPrefix(t *testing.T) {
	tests := map[string]string{
		"debug-server":  "RUK_PORT_DEBUG_SERVER",
		" app ":         "RUK_PORT_APP",
		"a---b":         "RUK_PORT_A_B",
		"version 2":     "RUK_PORT_VERSION_2",
		"already_valid": "RUK_PORT_ALREADY_VALID",
	}
	for input, want := range tests {
		got, err := NormalizeName(input)
		if err != nil {
			t.Fatalf("NormalizeName(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "---", "   ", "é"} {
		if _, err := NormalizeName(input); err == nil {
			t.Errorf("NormalizeName(%q) succeeded, want unusable-name error", input)
		}
	}
}

func TestBuildEnvironmentRejectsDuplicateNormalizedNamesAndInvalidPorts(t *testing.T) {
	_, err := BuildEnvironment(map[string]int64{"debug-server": 3000, "debug server": 3001})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate normalized names error = %v", err)
	}
	for _, port := range []int64{0, 65_536, -1} {
		_, err := BuildEnvironment(map[string]int64{"app": port})
		if err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
			t.Errorf("port %d error = %v", port, err)
		}
	}
	_, err = BuildEnvironment(map[string]int64{"---": 3000})
	if err == nil || !strings.Contains(err.Error(), "letter or number") {
		t.Errorf("unusable name error = %v", err)
	}
}

func TestBuildEnvironmentIsDeterministic(t *testing.T) {
	ports := map[string]int64{"inspector": 4000, "app": 3000}
	want := map[string]string{"RUK_PORT_APP": "3000", "RUK_PORT_INSPECTOR": "4000"}
	for attempt := 0; attempt < 10; attempt++ {
		got, err := BuildEnvironment(ports)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("BuildEnvironment result = %#v, want %#v", got, want)
		}
	}
}

type fakeBinding struct {
	port   int
	closed bool
	err    error
}

func (binding *fakeBinding) Port() int { return binding.port }

func (binding *fakeBinding) Close() error {
	binding.closed = true
	return binding.err
}

func TestAvailabilityProbeUsesDualStackThenIPv4Fallback(t *testing.T) {
	var requests []BindRequest
	binding := &fakeBinding{port: 3010}
	probe := NewAvailabilityProbe(func(request BindRequest) (BoundListener, error) {
		requests = append(requests, request)
		if request.Network == "tcp6" {
			return nil, ErrIPv6Unavailable
		}
		return binding, nil
	})
	got, err := probe.Find(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3010 || !binding.closed {
		t.Fatalf("probe result = %d, closed = %t", got, binding.closed)
	}
	want := []BindRequest{
		{Network: "tcp6", Address: "[::]:0", IPv6Only: false},
		{Network: "tcp4", Address: "127.0.0.1:0", IPv6Only: false},
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("bind requests = %#v, want %#v", requests, want)
	}
}

func TestAvailabilityProbeClosesExcludedBindingAndRetries(t *testing.T) {
	bindings := []*fakeBinding{{port: 3011}, {port: 3012}}
	index := 0
	probe := AvailabilityProbe{
		Listen: func(request BindRequest) (BoundListener, error) {
			if request.Network != "tcp6" {
				t.Fatalf("unexpected network %q", request.Network)
			}
			binding := bindings[index]
			index++
			return binding, nil
		},
		MaxAttempts: 2,
	}
	got, err := probe.Find(map[int64]struct{}{3011: {}})
	if err != nil || got != 3012 {
		t.Fatalf("probe result = %d, error = %v", got, err)
	}
	if !bindings[0].closed || !bindings[1].closed {
		t.Fatal("probe did not close every binding")
	}
}

func TestAvailabilityProbeDoesNotFallbackForOtherErrors(t *testing.T) {
	wantErr := errors.New("permission denied")
	called := 0
	_, err := AvailablePort(nil, func(request BindRequest) (BoundListener, error) {
		called++
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) || called != 1 {
		t.Fatalf("error = %v, calls = %d", err, called)
	}
}
