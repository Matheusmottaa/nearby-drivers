package httpsrv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

func TestGateHealthAndReady(t *testing.T) {
	g := NewGate()

	tests := []struct {
		name   string
		method string
		path   string
		ready  bool
		want   int
		body   string
	}{
		{name: "liveness", method: http.MethodGet, path: "/healthz", want: http.StatusOK, body: "ok\n"},
		{name: "ready closed", method: http.MethodGet, path: "/readyz", want: http.StatusServiceUnavailable, body: "not ready\n"},
		{name: "ready open", method: http.MethodGet, path: "/readyz", ready: true, want: http.StatusOK, body: "ok\n"},
		{name: "liveness stays up when not ready", method: http.MethodGet, path: "/healthz", want: http.StatusOK, body: "ok\n"},
		{name: "post healthz", method: http.MethodPost, path: "/healthz", want: http.StatusMethodNotAllowed},
		{name: "unknown route", method: http.MethodGet, path: "/rides", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g.SetReady(tt.ready)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			g.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.body != "" && rec.Body.String() != tt.body {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.body)
			}
			if tt.want == http.StatusMethodNotAllowed || tt.want == http.StatusNotFound {
				if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
					t.Fatalf("Content-Type = %q, want application/problem+json", got)
				}
			}
		})
	}
}

func TestNotFoundCarriesTraceID(t *testing.T) {
	g := NewGate()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("X-Request-Id", "req-abc123")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	var got problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TraceID != "req-abc123" || got.Status != http.StatusNotFound {
		t.Fatalf("problem = %#v", got)
	}
}

func TestReadyStatus(t *testing.T) {
	g := NewGate()
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)

	if got := ReadyStatus(srv.Listener.Addr().String()); got != 1 {
		t.Fatalf("ReadyStatus() closed = %d, want 1", got)
	}
	g.SetReady(true)
	if got := ReadyStatus(srv.Listener.Addr().String()); got != 0 {
		t.Fatalf("ReadyStatus() open = %d, want 0", got)
	}
}

func TestReadyStatusUnreachable(t *testing.T) {
	if got := ReadyStatus("127.0.0.1:1"); got != 1 {
		t.Fatalf("ReadyStatus() down = %d, want 1", got)
	}
}

func TestLiveStatus(t *testing.T) {
	g := NewGate()
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)

	if got := LiveStatus(srv.Listener.Addr().String()); got != 0 {
		t.Fatalf("LiveStatus() = %d, want 0", got)
	}
	if got := LiveStatus("127.0.0.1:1"); got != 1 {
		t.Fatalf("LiveStatus() down = %d, want 1", got)
	}
}

func TestProbeCode(t *testing.T) {
	g := NewGate()
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()

	if _, ok := ProbeCode([]string{"bin"}, addr); ok {
		t.Fatal("ProbeCode() ok = true, want false")
	}
	if code, ok := ProbeCode([]string{"bin", "-live"}, addr); !ok || code != 0 {
		t.Fatalf("ProbeCode(-live) = %d, %v", code, ok)
	}
	if code, ok := ProbeCode([]string{"bin", "-healthcheck"}, addr); !ok || code != 1 {
		t.Fatalf("ProbeCode(-healthcheck) closed = %d, %v", code, ok)
	}
	g.SetReady(true)
	if code, ok := ProbeCode([]string{"bin", "-healthcheck"}, addr); !ok || code != 0 {
		t.Fatalf("ProbeCode(-healthcheck) open = %d, %v", code, ok)
	}
	if code, ok := ProbeCode([]string{"bin", "-nope"}, addr); !ok || code != 2 {
		t.Fatalf("ProbeCode(unknown) = %d, %v", code, ok)
	}
	if code, ok := ProbeCode([]string{"bin", "-live", "extra"}, addr); !ok || code != 2 {
		t.Fatalf("ProbeCode(extra) = %d, %v", code, ok)
	}
}

func TestLoopback(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{addr: ":8080", want: "127.0.0.1:8080"},
		{addr: "0.0.0.0:8080", want: "127.0.0.1:8080"},
		{addr: "10.0.0.8:8080", want: "10.0.0.8:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := loopback(tt.addr); got != tt.want {
				t.Fatalf("loopback(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}
