// Package boot retries dependency setup until the process is signaled to stop.
package boot

import (
	"context"
	"log/slog"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

// Supervise calls open until it succeeds, then waits for ctx and runs cleanup.
// markReady opens the readiness gate only after that success. A service whose
// doc requires something this stage does not have yet passes markReady false
// and leaves GET /readyz at 503.
func Supervise(ctx context.Context, log *slog.Logger, gate *httpsrv.Gate, markReady bool, retry time.Duration, open func(context.Context) (func(), error)) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		cleanup, err := open(ctx)
		if err == nil {
			if markReady {
				gate.SetReady(true)
			}
			<-ctx.Done()
			if cleanup != nil {
				cleanup()
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		gate.SetReady(false)
		log.Error("open dependencies", "err", err)

		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
