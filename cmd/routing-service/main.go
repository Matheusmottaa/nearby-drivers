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
	cfg, cfgErr := loadConfig()
	if code, probe := httpsrv.ProbeCode(os.Args, cfg.HTTPAddr); probe {
		if code == 2 {
			log.Error("arguments", "args", os.Args[1:])
		}
		os.Exit(code)
	}
	if cfgErr != nil {
		log.Error("config", "err", cfgErr)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)
	go watchGraph(ctx, log, cfg.GraphPath, cfg.PoolSize, gate, srv)

	log.Info("starting", "http_addr", cfg.HTTPAddr, "graph_path", cfg.GraphPath, "pool_size", cfg.PoolSize)
	if err := httpsrv.Run(ctx, log, cfg.HTTPAddr, gate.Handler()); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

// watchGraph loads and builds the graph, publishes it with its search pool and
// only then opens readiness. A failed load or build retries every second.
func watchGraph(ctx context.Context, log *slog.Logger, path string, poolSize int, gate *httpsrv.Gate, srv *server) {
	const retry = time.Second
	start := time.Now()
	for {
		g, err := loadGraph(ctx, path)
		if err == nil {
			srv.publish(g, graph.NewPool(poolSize, g.NodeCount()))
			gate.SetReady(true)
			log.Info("graph loaded",
				"graph_version", g.Version,
				"nodes", g.NodeCount(),
				"edges", g.EdgeCount(),
				"pool_size", poolSize,
				"graph_load_ms", time.Since(start).Milliseconds(),
			)
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

func loadGraph(ctx context.Context, path string) (*graph.Graph, error) {
	f, err := graph.Load(ctx, path)
	if err != nil {
		return nil, err
	}
	return graph.Build(f)
}
