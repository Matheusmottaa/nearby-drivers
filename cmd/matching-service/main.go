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
	"github.com/Matheusmottaa/nearby-drivers/internal/matching"
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
		"match_k", cfg.MatchK,
		"radius_m", cfg.RadiusM,
	)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func open(ctx context.Context, log *slog.Logger, cfg config) (func(), error) {
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cl, err := depend.OpenKafka(connCtx, cfg.KafkaBrokers)
	if err != nil {
		return nil, err
	}
	dedup := matching.Dedup(matching.NopDedup{})
	var closeRedis func()
	rdb, err := depend.OpenRedis(connCtx, cfg.RedisURL)
	if err != nil {
		log.Error("redis unavailable", "err", err)
	} else {
		dedup = matching.NewRedis(rdb)
		closeRedis = func() { _ = rdb.Close() }
	}

	bus := matching.NewKafka(cl, cfg.KafkaBrokers, log)
	opt := matching.DefaultClientOptions()
	svc := matching.New(log,
		matching.NewNearbyClient(cfg.DriverURL, opt),
		matching.NewETAClient(cfg.RoutingURL, opt),
		dedup,
		bus,
		matching.Options{K: cfg.MatchK, RadiusM: cfg.RadiusM, SpeedMPS: cfg.SpeedMPS},
	)
	svc.UseKafka(bus)
	loopCtx, loopCancel := context.WithCancel(ctx)
	svc.Loop(loopCtx)
	return func() {
		loopCancel()
		svc.Wait()
		if closeRedis != nil {
			closeRedis()
		}
		cl.Close()
	}, nil
}
