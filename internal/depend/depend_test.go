package depend

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsBadInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	if _, err := OpenPostgres(ctx, "not a url"); err == nil {
		t.Fatal("OpenPostgres() error = nil")
	}
	if _, err := OpenRedis(ctx, "://bad"); err == nil {
		t.Fatal("OpenRedis() error = nil")
	}
	if _, err := OpenKafka(ctx, " , "); err == nil {
		t.Fatal("OpenKafka() error = nil")
	}
	if _, err := OpenStores(ctx, "not a url", "redis://127.0.0.1:6379", "127.0.0.1:9092"); err == nil {
		t.Fatal("OpenStores() error = nil")
	}
}

func TestPingFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	_, err := OpenPostgres(ctx, "postgres://ride:ride@127.0.0.1:1/nearby?sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "ping postgres") {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	_, err = OpenRedis(ctx, "redis://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "ping redis") {
		t.Fatalf("OpenRedis() error = %v", err)
	}
	_, err = OpenKafka(ctx, "127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "ping kafka") {
		t.Fatalf("OpenKafka() error = %v", err)
	}
}

func TestSplitBrokers(t *testing.T) {
	got := splitBrokers(" kafka:19092, localhost:9092 , ")
	if len(got) != 2 || got[0] != "kafka:19092" || got[1] != "localhost:9092" {
		t.Fatalf("splitBrokers() = %#v", got)
	}
}
