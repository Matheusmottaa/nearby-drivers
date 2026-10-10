package main

import "os"

type config struct {
	HTTPAddr  string
	GraphPath string
}

func loadConfig() config {
	return config{
		HTTPAddr:  envOr("HTTP_ADDR", ":8080"),
		GraphPath: envOr("GRAPH_PATH", "/data/maps/graph.txt"),
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
