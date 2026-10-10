package main

import "testing"

func TestLoadConfig(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("GRAPH_PATH", "")

	cfg := loadConfig()
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.GraphPath != "/data/maps/graph.txt" {
		t.Errorf("GraphPath = %q, want /data/maps/graph.txt", cfg.GraphPath)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("GRAPH_PATH", "data/maps/graph.txt")

	cfg := loadConfig()
	if cfg.HTTPAddr != ":9090" || cfg.GraphPath != "data/maps/graph.txt" {
		t.Errorf("config = %+v", cfg)
	}
}
