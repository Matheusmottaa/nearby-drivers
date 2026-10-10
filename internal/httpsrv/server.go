// Package httpsrv is the process HTTP surface shared by the four binaries:
// liveness, readiness and shutdown on context cancel.
package httpsrv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Gate is the readiness flag read by GET /readyz.
type Gate struct {
	ready   atomic.Bool
	handler http.Handler
}

// NewGate serves /healthz and /readyz. Readiness starts closed.
func NewGate() *Gate {
	g := &Gate{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.healthz)
	mux.HandleFunc("GET /readyz", g.readyz)
	g.handler = mux
	return g
}

// SetReady opens or closes readiness. Liveness does not change.
func (g *Gate) SetReady(ready bool) {
	g.ready.Store(ready)
}

// Handler is the HTTP handler of the gate.
func (g *Gate) Handler() http.Handler {
	return g.handler
}

func (g *Gate) healthz(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "ok\n")
}

func (g *Gate) readyz(w http.ResponseWriter, _ *http.Request) {
	if !g.ready.Load() {
		writeText(w, http.StatusServiceUnavailable, "not ready\n")
		return
	}
	writeText(w, http.StatusOK, "ok\n")
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// NewLogger returns a JSON logger with the service attribute set.
func NewLogger(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
}

// Run serves handler on addr until ctx is canceled, then shuts the server down.
func Run(ctx context.Context, log *slog.Logger, addr string, handler http.Handler) error {
	if log == nil {
		log = slog.Default()
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	errc := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown http: %w", err)
		}
		return nil
	case err := <-errc:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}
}

// ReadyStatus probes GET /readyz on addr. It returns 0 when the status is 200.
func ReadyStatus(addr string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+loopback(addr)+"/readyz", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func loopback(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}
