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
	"github.com/Matheusmottaa/nearby-drivers/internal/driver"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

func main() {
	log := httpsrv.NewLogger("driver-service")
	cfg := loadConfig()
	if code, probe := httpsrv.ProbeCode(os.Args, cfg.HTTPAddr); probe {
		if code == 2 {
			log.Error("arguments", "args", os.Args[1:])
		}
		os.Exit(code)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Uma instância: o índice vive neste processo. /readyz abre depois do
	// replay de drivers.location e do primeiro snapshot.
	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)
	go boot.Supervise(ctx, log, gate, false, time.Second, func(ctx context.Context) (func(), error) {
		return open(ctx, log, cfg, gate, srv)
	})

	log.Info("starting", "http_addr", cfg.HTTPAddr, "redis_url", cfg.RedisURL, "kafka_brokers", cfg.KafkaBrokers)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func open(ctx context.Context, log *slog.Logger, cfg config, gate *httpsrv.Gate, srv *server) (func(), error) {
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	stores, err := depend.OpenStores(connCtx, cfg.DatabaseURL, cfg.RedisURL, cfg.KafkaBrokers)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := driver.ApplyDDL(connCtx, stores.Postgres); err != nil {
		cancel()
		stores.Close()
		return nil, err
	}
	cancel()

	svc := driver.New(log, driver.NewPostgres(stores.Postgres), driver.NewRedis(stores.Redis), driver.NewKafka(stores.Kafka, cfg.KafkaBrokers))
	bootCtx, bootCancel := context.WithTimeout(ctx, 30*time.Second)
	err = svc.Boot(bootCtx)
	bootCancel()
	if err != nil {
		stores.Close()
		return nil, err
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	go svc.Loop(loopCtx)
	srv.bind(svc)
	gate.SetReady(true)
	return func() {
		loopCancel()
		svc.Wait()
		stores.Close()
	}, nil
}
