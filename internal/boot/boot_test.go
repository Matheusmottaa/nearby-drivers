package boot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

func TestSuperviseOpensGateAfterSuccess(t *testing.T) {
	gate := httpsrv.NewGate()
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cleaned := make(chan struct{})

	go Supervise(ctx, slog.New(slog.DiscardHandler), gate, true, 0, func(context.Context) (func(), error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("down")
		}
		return func() { close(cleaned) }, nil
	})

	waitReady(t, gate, http.StatusOK)
	if calls.Load() < 2 {
		t.Fatalf("open calls = %d, want at least 2", calls.Load())
	}
	cancel()
	waitClosed(t, cleaned)
}

func TestSuperviseLeavesGateClosed(t *testing.T) {
	gate := httpsrv.NewGate()
	opened := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go Supervise(ctx, nil, gate, false, 0, func(context.Context) (func(), error) {
		close(opened)
		return func() {}, nil
	})

	<-opened
	waitReady(t, gate, http.StatusServiceUnavailable)
}

func TestSuperviseStopsWhenCanceled(t *testing.T) {
	gate := httpsrv.NewGate()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		Supervise(ctx, slog.New(slog.DiscardHandler), gate, true, time.Hour, func(ctx context.Context) (func(), error) {
			return nil, ctx.Err()
		})
		close(done)
	}()
	waitClosed(t, done)
	waitReady(t, gate, http.StatusServiceUnavailable)
}

func waitReady(t *testing.T, gate *httpsrv.Gate, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got int
	for {
		rec := httptest.NewRecorder()
		gate.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		got = rec.Code
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("readyz = %d, want %d", got, want)
		}
		runtime.Gosched()
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting")
	}
}
