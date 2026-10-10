package main

import "os"

type config struct {
	HTTPAddr     string
	RedisURL     string
	KafkaBrokers string
	DriverURL    string
	RoutingURL   string
}

func loadConfig() config {
	return config{
		HTTPAddr:     envOr("HTTP_ADDR", ":8080"),
		RedisURL:     envOr("REDIS_URL", "redis://localhost:6379"),
		KafkaBrokers: envOr("KAFKA_BROKERS", "localhost:9092"),
		DriverURL:    envOr("DRIVER_URL", "http://localhost:8080"),
		RoutingURL:   envOr("ROUTING_URL", "http://localhost:8081"),
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
