package ride

import (
	"context"
	"time"
)

// Store is the ride schema. InsertNew and Commit each run in one transaction
// with their outbox rows.
type Store interface {
	InsertNew(ctx context.Context, key string, hash []byte, ride Ride, ev OutboxEvent) (Ride, bool, error)
	Get(ctx context.Context, id string) (Ride, error)
	Commit(ctx context.Context, fromVersion int, next Ride, events []OutboxEvent) error
	PublishPending(ctx context.Context, limit int, publish func(PendingPublish) error) ([]string, error)
	OutboxStats(ctx context.Context) (pending int, lag time.Duration, err error)
}

// Cache is the Redis shortcut. Every method may fail when Redis is down;
// the caller then uses the database.
type Cache interface {
	Lookup(ctx context.Context, key string) (hash []byte, rideID string, ok bool, err error)
	Remember(ctx context.Context, key string, hash []byte, rideID string) error
	TryReserve(ctx context.Context, driverID, rideID string) (bool, error)
	Release(ctx context.Context, driverID, rideID string) error
}

// Bus publishes outbox rows and consumes the ride worker topics.
type Bus interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
	Consume(ctx context.Context, handle func(ctx context.Context, topic string, value []byte) error) error
}
