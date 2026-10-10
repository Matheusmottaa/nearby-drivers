package httpsrv

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
		})
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
