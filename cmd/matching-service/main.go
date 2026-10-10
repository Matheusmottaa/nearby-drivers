package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

func main() {
	log := httpsrv.NewLogger("matching-service")
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

	// O ready de broker entra na etapa 6. Este serviço não fica no nginx público.
	gate := httpsrv.NewGate()
	gate.SetReady(true)

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
