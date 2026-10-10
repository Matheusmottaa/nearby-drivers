package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

func TestWatchGraphOpensReady(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.txt")
	if err := os.WriteFile(path, []byte("fixture-v1\n0 0\n"), 0o644); err != nil {
		t.Fatalf("write graph: %v", err)
	}

	gate := httpsrv.NewGate()
	done := make(chan struct{})
	go func() {
		watchGraph(t.Context(), slog.New(slog.DiscardHandler), path, gate)
		close(done)
	}()
	<-done

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", rec.Code)
	}
}

func TestWatchGraphStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	gate := httpsrv.NewGate()
	watchGraph(ctx, slog.New(slog.DiscardHandler), filepath.Join(t.TempDir(), "missing.txt"), gate)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", rec.Code)
	}
}
