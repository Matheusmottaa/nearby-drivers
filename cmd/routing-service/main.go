package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/graph"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
)

func main() {
	log := httpsrv.NewLogger("routing-service")
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

	gate := httpsrv.NewGate()
	go watchGraph(ctx, log, cfg.GraphPath, gate)

	log.Info("starting", "http_addr", cfg.HTTPAddr, "graph_path", cfg.GraphPath)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func watchGraph(ctx context.Context, log *slog.Logger, path string, gate *httpsrv.Gate) {
	const retry = time.Second
	for {
		f, err := graph.Load(ctx, path)
		if err == nil {
			gate.SetReady(true)
			log.Info("graph loaded", "graph_version", f.Version, "nodes", len(f.Nodes), "edges", len(f.Edges))
			return
		}
		if ctx.Err() != nil {
			return
		}
		gate.SetReady(false)
		log.Error("load graph", "path", path, "err", err)

		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
