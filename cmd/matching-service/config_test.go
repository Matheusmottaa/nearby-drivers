package main

import "testing"

func TestLoadConfig(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("KAFKA_BROKERS", "")
	t.Setenv("DRIVER_URL", "")
	t.Setenv("ROUTING_URL", "")

	cfg := loadConfig()
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DriverURL != "http://localhost:8080" {
		t.Errorf("DriverURL = %q", cfg.DriverURL)
	}
	if cfg.RoutingURL != "http://localhost:8081" {
		t.Errorf("RoutingURL = %q", cfg.RoutingURL)
	}
	if cfg.RedisURL != "redis://localhost:6379" || cfg.KafkaBrokers != "localhost:9092" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("REDIS_URL", "redis://cache:6379")
	t.Setenv("KAFKA_BROKERS", "kafka:19092")
	t.Setenv("DRIVER_URL", "http://nginx:8080")
	t.Setenv("ROUTING_URL", "http://nginx:8081")

	cfg := loadConfig()
	if cfg.DriverURL != "http://nginx:8080" || cfg.RoutingURL != "http://nginx:8081" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.HTTPAddr != ":9090" || cfg.KafkaBrokers != "kafka:19092" || cfg.RedisURL != "redis://cache:6379" {
		t.Errorf("config = %+v", cfg)
	}
}
