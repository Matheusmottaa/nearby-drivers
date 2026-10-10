package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LastPositionTTL is the lifetime of driver:{id}:last.
const LastPositionTTL = 60 * time.Second

// Redis stores the last fix. Nearby does not call it.
type Redis struct {
	client *redis.Client
}

// NewRedis returns the last-position cache.
func NewRedis(client *redis.Client) *Redis {
	return &Redis{client: client}
}

func (r *Redis) Put(ctx context.Context, driverID string, pos Position) error {
	raw, err := json.Marshal(lastPosition{
		Lat:       pos.Lat,
		Lng:       pos.Lng,
		Seq:       pos.Seq,
		Timestamp: pos.At,
	})
	if err != nil {
		return fmt.Errorf("encode last position: %w", err)
	}
	if err := r.client.Set(ctx, lastKey(driverID), raw, LastPositionTTL).Err(); err != nil {
		return fmt.Errorf("set last position: %w", err)
	}
	return nil
}

func lastKey(id string) string {
	return "driver:" + id + ":last"
}

type lastPosition struct {
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
}
