package main

import (
	"os"
	"strconv"
)

type config struct {
	HTTPAddr     string
	RedisURL     string
	KafkaBrokers string
	DriverURL    string
	RoutingURL   string
	MatchK       int
	RadiusM      float64
	SpeedMPS     float64
}

func loadConfig() config {
	return config{
		HTTPAddr:     envOr("HTTP_ADDR", ":8080"),
		RedisURL:     envOr("REDIS_URL", "redis://localhost:6379"),
		KafkaBrokers: envOr("KAFKA_BROKERS", "localhost:9092"),
		DriverURL:    envOr("DRIVER_URL", "http://localhost:8080"),
		RoutingURL:   envOr("ROUTING_URL", "http://localhost:8081"),
		MatchK:       envInt("MATCH_K", 10),
		RadiusM:      envFloat("MATCH_RADIUS_M", 3000),
		SpeedMPS:     envFloat("HAVERSINE_SPEED_MPS", 8.33),
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
