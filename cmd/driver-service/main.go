package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

func main() {
	log := httpsrv.NewLogger("driver-service")
	cfg := loadConfig()
	if len(os.Args) > 1 {
		if len(os.Args) != 2 || os.Args[1] != "-healthcheck" {
			log.Error("arguments", "args", os.Args[1:])
			os.Exit(2)
		}
		os.Exit(httpsrv.ReadyStatus(cfg.HTTPAddr))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Índice, Postgres e Kafka entram nas etapas 1 e 4. Até lá o processo está
	// pronto quando está escutando.
	gate := httpsrv.NewGate()
	gate.SetReady(true)

	log.Info("starting", "http_addr", cfg.HTTPAddr, "redis_url", cfg.RedisURL, "kafka_brokers", cfg.KafkaBrokers)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
