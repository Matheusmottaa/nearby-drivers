package driver

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func TestBootReplayBuildsNearby(t *testing.T) {
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	pickup := geo.Point{Lat: -23.5613, Lng: -46.6565}
	store := newMemStore()
	store.put(Driver{ID: "a", InternalID: 0, VehicleClass: "standard", Status: StatusAvailable})
	store.put(Driver{ID: "b", InternalID: 1, VehicleClass: "standard", Status: StatusAvailable})
	store.put(Driver{ID: "c", InternalID: 2, VehicleClass: "comfort", Status: StatusOnTrip, CurrentRideID: rideA})

	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-HeartbeatWindow - time.Second)
	bus := &scriptBus{locations: []event.Location{
		{DriverID: "a", Lat: north(pickup, 800).Lat, Lng: pickup.Lng, Seq: 1, ObservedAt: fresh},
		{DriverID: "a", Lat: north(pickup, 200).Lat, Lng: pickup.Lng, Seq: 3, ObservedAt: fresh},
		{DriverID: "a", Lat: north(pickup, 50).Lat, Lng: pickup.Lng, Seq: 2, ObservedAt: fresh},
		{DriverID: "b", Lat: north(pickup, 100).Lat, Lng: pickup.Lng, Seq: 1, ObservedAt: stale},
		{DriverID: "c", Lat: north(pickup, 40).Lat, Lng: pickup.Lng, Seq: 1, ObservedAt: fresh},
		{DriverID: "missing", Lat: pickup.Lat, Lng: pickup.Lng, Seq: 9, ObservedAt: fresh},
	}}
	svc := New(slog.New(slog.DiscardHandler), store, noopCache{}, bus)
	svc.now = func() time.Time { return now }

	if _, err := svc.Nearby(pickup, 3000, 10, ""); err != ErrIndexNotReady {
		t.Fatalf("before snapshot: %v", err)
	}
	if err := svc.Boot(t.Context()); err != nil {
		t.Fatalf("boot: %v", err)
	}

	sl, ok := svc.Slot("a")
	if !ok || sl.Seq != 3 || sl.Lat != north(pickup, 200).Lat {
		t.Fatalf("replay kept seq %d lat %v, want seq 3 at 200m", sl.Seq, sl.Lat)
	}
	if b, ok := svc.Slot("b"); !ok || !b.HasFix || b.Fresh(now) {
		t.Fatal("stale driver was not restored")
	}
	if c, ok := svc.Slot("c"); !ok || !c.Reserved || !c.HasFix {
		t.Fatal("reserved driver was not restored")
	}

	got, err := svc.Nearby(pickup, 3000, 10, "standard")
	if err != nil {
		t.Fatalf("nearby: %v", err)
	}
	if len(got.Drivers) != 1 || got.Drivers[0].ID != "a" {
		t.Fatalf("nearby = %+v, want only a", got.Drivers)
	}
	if d := got.Drivers[0].StraightLineM; d < 150 || d > 250 {
		t.Fatalf("straight_line_m = %v, want about 200", d)
	}

	empty, err := svc.Nearby(pickup, 10, 10, "")
	if err != nil || len(empty.Drivers) != 0 {
		t.Fatalf("empty nearby = %+v %v", empty.Drivers, err)
	}
}

func TestSweepPublishesLostOnce(t *testing.T) {
	now := time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)
	store := newMemStore()
	store.put(Driver{ID: "trip", InternalID: 0, VehicleClass: "standard", Status: StatusOnTrip, CurrentRideID: rideA})
	store.put(Driver{ID: "free", InternalID: 1, VehicleClass: "standard", Status: StatusAvailable})
	stale := now.Add(-HeartbeatWindow - time.Second)
	bus := &scriptBus{locations: []event.Location{
		{DriverID: "trip", Lat: 1, Lng: 2, Seq: 1, ObservedAt: stale},
		{DriverID: "free", Lat: 1, Lng: 2, Seq: 1, ObservedAt: stale},
	}}
	svc := New(slog.New(slog.DiscardHandler), store, noopCache{}, bus)
	svc.now = func() time.Time { return now }
	if err := svc.Boot(t.Context()); err != nil {
		t.Fatalf("boot: %v", err)
	}

	if n := svc.Sweep(t.Context()); n != 1 {
		t.Fatalf("sweep = %d, want 1", n)
	}
	if got := bus.presence(); got != 1 {
		t.Fatalf("lost publishes = %d, want 1", got)
	}
	if n := svc.Sweep(t.Context()); n != 0 || bus.presence() != 1 {
		t.Fatalf("second sweep published again: n=%d publishes=%d", n, bus.presence())
	}

	if err := svc.Assign(t.Context(), "trip", rideA); err != nil {
		t.Fatalf("assign same: %v", err)
	}
	if n := svc.Sweep(t.Context()); n != 0 {
		t.Fatalf("same reservation published lost again: %d", n)
	}
}

func TestReleaseOtherRideKeepsReservation(t *testing.T) {
	store := newMemStore()
	store.put(Driver{ID: "a", InternalID: 0, VehicleClass: "standard", Status: StatusAvailable})
	bus := &scriptBus{}
	svc := New(slog.New(slog.DiscardHandler), store, noopCache{}, bus)
	if err := svc.Boot(t.Context()); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if err := svc.Assign(t.Context(), "a", rideA); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if err := svc.Release(t.Context(), "a", rideB); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	sl, _ := svc.Slot("a")
	if !sl.Reserved || sl.RideID != rideA {
		t.Fatalf("foreign release cleared the slot: %+v", sl)
	}
	d, err := store.Get(t.Context(), "a")
	if err != nil || d.Status != StatusOnTrip || d.CurrentRideID != rideA {
		t.Fatalf("store = %+v %v", d, err)
	}
	if err := svc.Release(t.Context(), "a", rideA); err != nil {
		t.Fatalf("release: %v", err)
	}
	sl, _ = svc.Slot("a")
	if sl.Reserved || sl.RideID != "" {
		t.Fatalf("release left the slot reserved: %+v", sl)
	}
}

const (
	rideA = "018f9c00-3333-7abc-8def-0123456789ab"
	rideB = "018f9c00-4444-7abc-8def-0123456789ab"
)

func north(p geo.Point, meters float64) geo.Point {
	const metersPerDegree = geo.EarthRadiusM * 3.141592653589793 / 180
	return geo.Point{Lat: p.Lat + meters/metersPerDegree, Lng: p.Lng}
}

type noopCache struct{}

func (noopCache) Put(context.Context, string, Position) error { return nil }

type scriptBus struct {
	mu        sync.Mutex
	locations []event.Location
	lost      int
}

func (b *scriptBus) Publish(_ context.Context, topic, _ string, _ []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if topic == event.TopicDriversPresence {
		b.lost++
	}
	return nil
}

func (b *scriptBus) presence() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lost
}

func (b *scriptBus) ReplayLocations(_ context.Context, apply func(event.Location) error) error {
	for _, loc := range b.locations {
		if err := apply(loc); err != nil {
			return err
		}
	}
	return nil
}

func (b *scriptBus) ConsumeReservations(ctx context.Context, _ func(context.Context, string, []byte) error) error {
	<-ctx.Done()
	return nil
}

type memStore struct {
	mu      sync.Mutex
	drivers map[string]Driver
}

func newMemStore() *memStore {
	return &memStore{drivers: make(map[string]Driver)}
}

func (m *memStore) put(d Driver) {
	m.drivers[d.ID] = d
}

func (m *memStore) Create(context.Context, string, Registration) (Driver, bool, error) {
	return Driver{}, false, nil
}

func (m *memStore) Get(_ context.Context, id string) (Driver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[id]
	if !ok {
		return Driver{}, ErrNotFound
	}
	return d, nil
}

func (m *memStore) UpdateStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[id]
	if !ok {
		return ErrNotFound
	}
	if d.Status == StatusOnTrip {
		return ErrIllegalStatus
	}
	d.Status = status
	m.drivers[id] = d
	return nil
}

func (m *memStore) Assign(_ context.Context, driverID, rideID string) (AssignResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[driverID]
	if !ok {
		return AssignMissing, nil
	}
	if d.Status == StatusOnTrip && d.CurrentRideID == rideID {
		return AssignSame, nil
	}
	if d.Status != StatusAvailable || d.CurrentRideID != "" {
		return AssignOther, nil
	}
	d.Status = StatusOnTrip
	d.CurrentRideID = rideID
	m.drivers[driverID] = d
	return AssignApplied, nil
}

func (m *memStore) Release(_ context.Context, driverID, rideID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[driverID]
	if !ok || d.CurrentRideID != rideID {
		return false, nil
	}
	d.Status = StatusAvailable
	d.CurrentRideID = ""
	m.drivers[driverID] = d
	return true, nil
}

func (m *memStore) List(context.Context) ([]Driver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Driver, 0, len(m.drivers))
	for _, d := range m.drivers {
		out = append(out, d)
	}
	return out, nil
}
