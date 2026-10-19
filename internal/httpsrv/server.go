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

	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

// Gate is the readiness flag read by GET /readyz.
type Gate struct {
	ready atomic.Bool
	mux   *http.ServeMux
	hits  *httpStats
	extra func(io.Writer)
}

// NewGate serves /healthz and /readyz. Readiness starts closed.
func NewGate() *Gate {
	g := &Gate{mux: http.NewServeMux(), hits: newHTTPStats()}
	g.mux.HandleFunc("GET /metrics", g.metrics)
	g.mux.HandleFunc("/healthz", g.healthz)
	g.mux.HandleFunc("/readyz", g.readyz)
	g.mux.HandleFunc("/", notFound)
	return g
}

// Handle registers a service route next to the probes. It panics, like
// http.ServeMux, if pattern is already registered.
func (g *Gate) Handle(pattern string, handler http.Handler) {
	g.mux.Handle(pattern, handler)
}

// SetReady opens or closes readiness. Liveness does not change.
func (g *Gate) SetReady(ready bool) {
	g.ready.Store(ready)
}

// SetExtraMetrics appends process-specific counters to GET /metrics.
func (g *Gate) SetExtraMetrics(fn func(io.Writer)) {
	g.extra = fn
}

// Handler is the HTTP handler of the gate. It counts requests other than the probes.
func (g *Gate) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		g.mux.ServeHTTP(rec, r)
		route := strings.TrimPrefix(r.Pattern, r.Method+" ")
		switch route {
		case "", "/metrics", "/healthz", "/readyz":
			return
		}
		g.hits.add(r.Method, route, rec.code)
	})
}

func (g *Gate) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", problem.TraceID(r))
		return
	}
	writeText(w, http.StatusOK, "ok\n")
}

func (g *Gate) readyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", problem.TraceID(r))
		return
	}
	if !g.ready.Load() {
		writeText(w, http.StatusServiceUnavailable, "not ready\n")
		return
	}
	writeText(w, http.StatusOK, "ok\n")
}

func (g *Gate) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	g.hits.write(w)
	if g.extra != nil {
		g.extra(w)
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, http.StatusNotFound, "not found", "no such route", problem.TraceID(r))
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
	return probeStatus(addr, "/readyz")
}

// LiveStatus probes GET /healthz on addr. It returns 0 when the status is 200.
func LiveStatus(addr string) int {
	return probeStatus(addr, "/healthz")
}

// ProbeCode reports the process status for -healthcheck and -live.
// ok is false when args do not select a probe, which is a normal start.
func ProbeCode(args []string, addr string) (code int, ok bool) {
	if len(args) <= 1 {
		return 0, false
	}
	if len(args) != 2 {
		return 2, true
	}
	switch args[1] {
	case "-healthcheck":
		return ReadyStatus(addr), true
	case "-live":
		return LiveStatus(addr), true
	default:
		return 2, true
	}
}

func probeStatus(addr, path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+loopback(addr)+path, nil)
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
