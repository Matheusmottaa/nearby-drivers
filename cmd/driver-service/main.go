package main

import (
	"context"
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

	// Postgres, Redis e Kafka sobem aqui. /readyz continua 503 até o índice
	// espacial da etapa 4.
	gate := httpsrv.NewGate()
	go boot.Supervise(ctx, log, gate, false, time.Second, func(ctx context.Context) (func(), error) {
		return open(ctx, cfg)
	})

	log.Info("starting", "http_addr", cfg.HTTPAddr, "redis_url", cfg.RedisURL, "kafka_brokers", cfg.KafkaBrokers)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func open(ctx context.Context, cfg config) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stores, err := depend.OpenStores(ctx, cfg.DatabaseURL, cfg.RedisURL, cfg.KafkaBrokers)
	if err != nil {
		return nil, err
	}
	if err := driver.ApplyDDL(ctx, stores.Postgres); err != nil {
		stores.Close()
		return nil, err
	}
	return stores.Close, nil
}
