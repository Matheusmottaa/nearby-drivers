package main

import (
	"runtime"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("GRAPH_PATH", "")
	t.Setenv("ROUTING_POOL_SIZE", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.GraphPath != "/data/maps/graph.txt" {
		t.Errorf("GraphPath = %q, want /data/maps/graph.txt", cfg.GraphPath)
	}
	if cfg.PoolSize != runtime.GOMAXPROCS(0) {
		t.Errorf("PoolSize = %d, want GOMAXPROCS %d", cfg.PoolSize, runtime.GOMAXPROCS(0))
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("GRAPH_PATH", "data/maps/graph.txt")
	t.Setenv("ROUTING_POOL_SIZE", "3")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.GraphPath != "data/maps/graph.txt" || cfg.PoolSize != 3 {
		t.Errorf("config = %+v", cfg)
	}
}

func TestLoadConfigRejectsPoolSize(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		t.Setenv("ROUTING_POOL_SIZE", v)
		if _, err := loadConfig(); err == nil {
			t.Errorf("ROUTING_POOL_SIZE=%q: want error", v)
		}
	}
}
