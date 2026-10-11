package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/boot"
	"github.com/Matheusmottaa/nearby-drivers/internal/depend"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/ride"
)

func main() {
	log := httpsrv.NewLogger("ride-service")
	cfg := loadConfig()
	if code, probe := httpsrv.ProbeCode(os.Args, cfg.HTTPAddr); probe {
		if code == 2 {
			log.Error("arguments", "args", os.Args[1:])
		}
		os.Exit(code)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Uma instância. /readyz abre quando o banco responde e o DDL do schema ride foi aplicado.
	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)
	go boot.Supervise(ctx, log, gate, true, time.Second, func(ctx context.Context) (func(), error) {
		return open(ctx, log, cfg, srv)
	})

	log.Info("starting", "http_addr", cfg.HTTPAddr, "redis_url", cfg.RedisURL, "kafka_brokers", cfg.KafkaBrokers, "max_attempts", cfg.MaxAttempts)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func open(ctx context.Context, log *slog.Logger, cfg config, srv *server) (func(), error) {
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	stores, err := depend.OpenStores(connCtx, cfg.DatabaseURL, cfg.RedisURL, cfg.KafkaBrokers)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := ride.ApplyDDL(connCtx, stores.Postgres); err != nil {
		cancel()
		stores.Close()
		return nil, err
	}
	cancel()

	svc := ride.New(log, ride.NewPostgres(stores.Postgres), ride.NewRedis(stores.Redis), ride.NewKafka(stores.Kafka, cfg.KafkaBrokers, log), cfg.MaxAttempts)
	loopCtx, loopCancel := context.WithCancel(ctx)
	go svc.Loop(loopCtx)
	srv.bind(svc)
	return func() {
		loopCancel()
		svc.Wait()
		stores.Close()
	}, nil
}
