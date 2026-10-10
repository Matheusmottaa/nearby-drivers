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

	log := slog.New(slog.DiscardHandler)
	gate := httpsrv.NewGate()
	srv := newServer(log)
	done := make(chan struct{})
	go func() {
		watchGraph(t.Context(), log, path, 1, gate, srv)
		close(done)
	}()
	<-done

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", rec.Code)
	}
	if srv.current.Load() == nil {
		t.Fatal("graph not published")
	}
}

func TestWatchGraphStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	log := slog.New(slog.DiscardHandler)
	gate := httpsrv.NewGate()
	srv := newServer(log)
	watchGraph(ctx, log, filepath.Join(t.TempDir(), "missing.txt"), 1, gate, srv)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", rec.Code)
	}
	if srv.current.Load() != nil {
		t.Fatal("graph published after cancel")
	}
}
