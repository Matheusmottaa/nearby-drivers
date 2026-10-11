package ride

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"
)

var errCacheDown = errors.New("redis unavailable")

// Memory is an in-memory Store. Tests use it as the ride schema.
// A published row can be rewound so the publisher sends the same event_id again.
type Memory struct {
	mu   sync.Mutex
	now  func() time.Time
	ride map[string]Ride
	box  []memEvent
	keys map[string]memKey
}

type memEvent struct {
	OutboxEvent
	RideID      string
	CreatedAt   time.Time
	Published   bool
	PublishedAt time.Time
}

type memKey struct {
	hash   []byte
	rideID string
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		now:  time.Now,
		ride: make(map[string]Ride),
		keys: make(map[string]memKey),
	}
}

func (m *Memory) InsertNew(_ context.Context, key string, hash []byte, ride Ride, ev OutboxEvent) (Ride, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.keys[key]; ok {
		if !bytes.Equal(prev.hash, hash) {
			return Ride{}, false, ErrIdempotencyConflict
		}
		return m.ride[prev.rideID], false, nil
	}
	now := m.now()
	ride.RequestedAt = now
	ride.UpdatedAt = now
	m.ride[ride.ID] = ride
	m.box = append(m.box, memEvent{OutboxEvent: ev, RideID: ride.ID, CreatedAt: now})
	copied := make([]byte, len(hash))
	copy(copied, hash)
	m.keys[key] = memKey{hash: copied, rideID: ride.ID}
	return ride, true, nil
}

func (m *Memory) Get(_ context.Context, id string) (Ride, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ride, ok := m.ride[id]
	if !ok {
		return Ride{}, ErrNotFound
	}
	ride.ExcludedDriverIDs = copyIDs(ride.ExcludedDriverIDs)
	return ride, nil
}

func (m *Memory) Commit(_ context.Context, fromVersion int, next Ride, events []OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.ride[next.ID]
	if !ok {
		return ErrNotFound
	}
	if cur.Version != fromVersion {
		return ErrVersionConflict
	}
	if next.Status == StatusAssigned {
		for id, other := range m.ride {
			if id != next.ID && other.Status == StatusAssigned && other.AssignedDriverID == next.AssignedDriverID {
				return ErrDriverTaken
			}
		}
	}
	now := m.now()
	next.RequestedAt = cur.RequestedAt
	next.UpdatedAt = now
	if next.TraceID == "" {
		next.TraceID = cur.TraceID
	}
	next.ExcludedDriverIDs = copyIDs(next.ExcludedDriverIDs)
	m.ride[next.ID] = next
	for _, ev := range events {
		m.box = append(m.box, memEvent{OutboxEvent: ev, RideID: next.ID, CreatedAt: now})
	}
	return nil
}

func (m *Memory) PublishPending(_ context.Context, limit int, publish func(PendingPublish) error) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var became []string
	n := 0
	for i := range m.box {
		if n >= limit {
			break
		}
		row := &m.box[i]
		if row.Published {
			continue
		}
		if err := publish(PendingPublish{
			Subject: row.Subject, RideID: row.RideID, EventID: row.EventID,
			Payload: row.Payload, CreatedAt: row.CreatedAt,
		}); err != nil {
			return became, err
		}
		row.Published = true
		row.PublishedAt = m.now()
		n++
		if row.Subject != "rides.requested" {
			continue
		}
		ride := m.ride[row.RideID]
		next, ok := planPublish(ride)
		if !ok {
			continue
		}
		next.UpdatedAt = row.PublishedAt
		m.ride[row.RideID] = next
		became = append(became, ride.ID)
	}
	return became, nil
}

func (m *Memory) OutboxStats(_ context.Context) (int, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pending int
	var oldest time.Time
	for _, row := range m.box {
		if row.Published {
			continue
		}
		pending++
		if oldest.IsZero() || row.CreatedAt.Before(oldest) {
			oldest = row.CreatedAt
		}
	}
	if pending == 0 {
		return 0, 0, nil
	}
	return pending, m.now().Sub(oldest), nil
}

// rewindPublished clears the publish mark so the next pass resends the payload.
func (m *Memory) rewindPublished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.box {
		m.box[i].Published = false
		m.box[i].PublishedAt = time.Time{}
	}
}

// MemoryCache is the Redis stand-in used by tests.
type MemoryCache struct {
	mu   sync.Mutex
	idem map[string]memKey
	res  map[string]string
	Down bool
}

// NewMemoryCache returns an empty cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		idem: make(map[string]memKey),
		res:  make(map[string]string),
	}
}

func (c *MemoryCache) Lookup(_ context.Context, key string) ([]byte, string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Down {
		return nil, "", false, errCacheDown
	}
	hit, ok := c.idem[key]
	if !ok {
		return nil, "", false, nil
	}
	return append([]byte(nil), hit.hash...), hit.rideID, true, nil
}

func (c *MemoryCache) Remember(_ context.Context, key string, hash []byte, rideID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Down {
		return errCacheDown
	}
	copied := make([]byte, len(hash))
	copy(copied, hash)
	c.idem[key] = memKey{hash: copied, rideID: rideID}
	return nil
}

func (c *MemoryCache) TryReserve(_ context.Context, driverID, rideID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Down {
		return false, errCacheDown
	}
	if cur, ok := c.res[driverID]; ok {
		return cur == rideID, nil
	}
	c.res[driverID] = rideID
	return true, nil
}

func (c *MemoryCache) Release(_ context.Context, driverID, rideID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Down {
		return errCacheDown
	}
	if c.res[driverID] == rideID {
		delete(c.res, driverID)
	}
	return nil
}
