package matching

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const seenTTL = time.Hour

// Dedup remembers event ids that already produced a ranking.
type Dedup interface {
	Seen(ctx context.Context, eventID string) (bool, error)
	Mark(ctx context.Context, eventID string) error
}

// Redis is the evt:{event_id} key. A later outage is not fatal.
type Redis struct {
	client *redis.Client
}

// NewRedis returns the dedup cache.
func NewRedis(client *redis.Client) *Redis {
	return &Redis{client: client}
}

func seenKey(eventID string) string {
	return "evt:" + eventID
}

// Seen reports whether this event id was marked. The lookup is bounded so a
// stuck Redis does not hold the consumer.
func (r *Redis) Seen(ctx context.Context, eventID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	n, err := r.client.Exists(ctx, seenKey(eventID)).Result()
	if err != nil {
		return false, fmt.Errorf("seen %s: %w", eventID, err)
	}
	return n > 0, nil
}

// Mark stores the event id for one hour. SET NX keeps the first writer.
func (r *Redis) Mark(ctx context.Context, eventID string) error {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := r.client.SetNX(ctx, seenKey(eventID), "1", seenTTL).Err(); err != nil {
		return fmt.Errorf("mark %s: %w", eventID, err)
	}
	return nil
}

// NopDedup never remembers an event. It is the cache while Redis is down at boot.
type NopDedup struct{}

func (NopDedup) Seen(context.Context, string) (bool, error) { return false, nil }
func (NopDedup) Mark(context.Context, string) error         { return nil }
