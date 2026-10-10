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
)

func main() {
	log := httpsrv.NewLogger("matching-service")
	cfg := loadConfig()
	if code, probe := httpsrv.ProbeCode(os.Args, cfg.HTTPAddr); probe {
		if code == 2 {
			log.Error("arguments", "args", os.Args[1:])
		}
		os.Exit(code)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// /readyz exige o broker. Redis fora do ar não segura o processo.
	gate := httpsrv.NewGate()
	go boot.Supervise(ctx, log, gate, true, time.Second, func(ctx context.Context) (func(), error) {
		return open(ctx, log, cfg)
	})

	log.Info("starting",
		"http_addr", cfg.HTTPAddr,
		"redis_url", cfg.RedisURL,
		"kafka_brokers", cfg.KafkaBrokers,
		"driver_url", cfg.DriverURL,
		"routing_url", cfg.RoutingURL,
	)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func open(ctx context.Context, log *slog.Logger, cfg config) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cl, err := depend.OpenKafka(ctx, cfg.KafkaBrokers)
	if err != nil {
		return nil, err
	}
	rdb, err := depend.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		log.Error("redis unavailable", "err", err)
		return cl.Close, nil
	}
	return func() {
		_ = rdb.Close()
		cl.Close()
	}, nil
}
