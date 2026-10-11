package main

import "testing"

func TestLoadConfig(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("KAFKA_BROKERS", "")

	cfg := loadConfig()
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL != "postgres://ride:ride@localhost:5432/nearby?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Errorf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.KafkaBrokers != "localhost:9092" {
		t.Errorf("KafkaBrokers = %q", cfg.KafkaBrokers)
	}
	if cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.MaxAttempts)
	}
}

func TestLoadConfigMaxAttempts(t *testing.T) {
	t.Setenv("RIDE_MAX_ATTEMPTS", "5")
	if cfg := loadConfig(); cfg.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", cfg.MaxAttempts)
	}
	t.Setenv("RIDE_MAX_ATTEMPTS", "0")
	if cfg := loadConfig(); cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want default 3", cfg.MaxAttempts)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("DATABASE_URL", "postgres://ride:secret@db/nearby")
	t.Setenv("REDIS_URL", "redis://cache:6379")
	t.Setenv("KAFKA_BROKERS", "kafka:19092")

	cfg := loadConfig()
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want :9090", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL != "postgres://ride:secret@db/nearby" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://cache:6379" {
		t.Errorf("RedisURL = %q", cfg.RedisURL)
	}
	if cfg.KafkaBrokers != "kafka:19092" {
		t.Errorf("KafkaBrokers = %q", cfg.KafkaBrokers)
	}
}
