package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
)

type config struct {
	HTTPAddr  string
	GraphPath string
	PoolSize  int
}

func loadConfig() (config, error) {
	cfg := config{
		HTTPAddr:  envOr("HTTP_ADDR", ":8080"),
		GraphPath: envOr("GRAPH_PATH", "/data/maps/graph.txt"),
		PoolSize:  runtime.GOMAXPROCS(0),
	}
	if v, ok := os.LookupEnv("ROUTING_POOL_SIZE"); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return config{}, fmt.Errorf("ROUTING_POOL_SIZE %q: must be a positive integer", v)
		}
		cfg.PoolSize = n
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
