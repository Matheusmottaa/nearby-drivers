package ride

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Redis stores the idempotency shortcut and the short reservation.
type Redis struct {
	client *redis.Client
}

// NewRedis returns the cache. A later outage is not fatal: callers fall back
// to idempotency_keys and the unique assignment index.
func NewRedis(client *redis.Client) *Redis {
	return &Redis{client: client}
}

type idemValue struct {
	Hash   string `json:"h"`
	RideID string `json:"r"`
}

func (r *Redis) Lookup(ctx context.Context, key string) ([]byte, string, bool, error) {
	raw, err := r.client.Get(ctx, idemKey(key)).Bytes()
	if err == redis.Nil {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("get idempotency: %w", err)
	}
	var v idemValue
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, "", false, nil
	}
	hash, err := hex.DecodeString(v.Hash)
	if err != nil {
		return nil, "", false, nil
	}
	return hash, v.RideID, true, nil
}

func (r *Redis) Remember(ctx context.Context, key string, hash []byte, rideID string) error {
	raw, err := json.Marshal(idemValue{Hash: hex.EncodeToString(hash), RideID: rideID})
	if err != nil {
		return fmt.Errorf("encode idempotency: %w", err)
	}
	if err := r.client.Set(ctx, idemKey(key), raw, idempotencyTTL).Err(); err != nil {
		return fmt.Errorf("set idempotency: %w", err)
	}
	return nil
}

func (r *Redis) TryReserve(ctx context.Context, driverID, rideID string) (bool, error) {
	ok, err := r.client.SetNX(ctx, reserveKey(driverID), rideID, reservationTTL).Result()
	if err != nil {
		return false, fmt.Errorf("reserve driver: %w", err)
	}
	if ok {
		return true, nil
	}
	cur, err := r.client.Get(ctx, reserveKey(driverID)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read reservation: %w", err)
	}
	return cur == rideID, nil
}

func (r *Redis) Release(ctx context.Context, driverID, rideID string) error {
	// Delete only the key this ride owns. A concurrent SETNX must survive.
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`
	if err := r.client.Eval(ctx, script, []string{reserveKey(driverID)}, rideID).Err(); err != nil {
		return fmt.Errorf("release driver: %w", err)
	}
	return nil
}

func idemKey(key string) string { return "idem:" + key }

func reserveKey(driverID string) string { return "driver:" + driverID + ":res" }
