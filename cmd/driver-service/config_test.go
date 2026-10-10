package main

import "testing"

func TestLoadConfig(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("KAFKA_BROKERS", "")

	cfg := loadConfig()
	if cfg.DatabaseURL != "postgres://driver:driver@localhost:5432/nearby?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.HTTPAddr != ":8080" || cfg.RedisURL != "redis://localhost:6379" || cfg.KafkaBrokers != "localhost:9092" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("DATABASE_URL", "postgres://driver:secret@db/nearby")
	t.Setenv("REDIS_URL", "redis://cache:6379")
	t.Setenv("KAFKA_BROKERS", "kafka:19092")

	cfg := loadConfig()
	if cfg.HTTPAddr != ":9090" || cfg.DatabaseURL != "postgres://driver:secret@db/nearby" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.RedisURL != "redis://cache:6379" || cfg.KafkaBrokers != "kafka:19092" {
		t.Errorf("config = %+v", cfg)
	}
}
