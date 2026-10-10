// Package depend opens the process dependencies shared by the services.
package depend

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Stores are the connections a service holds until shutdown.
type Stores struct {
	Postgres *pgxpool.Pool
	Redis    *redis.Client
	Kafka    *kgo.Client
}

// Close releases whichever connections were opened.
func (s Stores) Close() {
	if s.Kafka != nil {
		s.Kafka.Close()
	}
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
	if s.Postgres != nil {
		s.Postgres.Close()
	}
}

// OpenPostgres parses url, pings, and returns a pool. The caller closes it.
func OpenPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// OpenRedis parses url and pings. The caller closes the client.
func OpenRedis(ctx context.Context, url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	opt.DialTimeout = 5 * time.Second
	opt.ContextTimeoutEnabled = true
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rdb, nil
}

// OpenKafka pings the brokers. It does not create topics.
func OpenKafka(ctx context.Context, brokers string) (*kgo.Client, error) {
	seeds := splitBrokers(brokers)
	if len(seeds) == 0 {
		return nil, fmt.Errorf("kafka brokers are empty")
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(seeds...),
		kgo.DialTimeout(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka client: %w", err)
	}
	if err := cl.Ping(ctx); err != nil {
		cl.Close()
		return nil, fmt.Errorf("ping kafka: %w", err)
	}
	return cl, nil
}

// OpenStores opens Postgres, Redis and Kafka. A failure closes what succeeded.
func OpenStores(ctx context.Context, pgURL, redisURL, brokers string) (Stores, error) {
	var s Stores
	pool, err := OpenPostgres(ctx, pgURL)
	if err != nil {
		return Stores{}, err
	}
	s.Postgres = pool

	rdb, err := OpenRedis(ctx, redisURL)
	if err != nil {
		s.Close()
		return Stores{}, err
	}
	s.Redis = rdb

	cl, err := OpenKafka(ctx, brokers)
	if err != nil {
		s.Close()
		return Stores{}, err
	}
	s.Kafka = cl
	return s, nil
}

func splitBrokers(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
