package main

import "os"

type config struct {
	HTTPAddr     string
	DatabaseURL  string
	RedisURL     string
	KafkaBrokers string
}

func loadConfig() config {
	return config{
		HTTPAddr:     envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:  envOr("DATABASE_URL", "postgres://ride:ride@localhost:5432/nearby?sslmode=disable"),
		RedisURL:     envOr("REDIS_URL", "redis://localhost:6379"),
		KafkaBrokers: envOr("KAFKA_BROKERS", "localhost:9092"),
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
