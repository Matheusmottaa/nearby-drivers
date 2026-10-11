package ride

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

const (
	passenger = "018f3a2b-1111-7abc-8def-0123456789ab"
	driver1   = "018f9c00-3333-7abc-8def-0123456789ab"
	driver2   = "018f9c00-4444-7abc-8def-0123456789ab"
)

func TestMachineTable(t *testing.T) {
	body := []byte(`{"passenger_id":"` + passenger + `","pickup":{"lat":-23.5613,"lng":-46.6565},"vehicle_class":"standard"}`)
	req := Request{PassengerID: passenger, PickupLat: -23.5613, PickupLng: -46.6565, VehicleClass: "standard", TraceID: "req-abc123"}

	t.Run("requested to matching", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustCreate(t, svc, "k1", body, req)
		if ride.Status != StatusRequested || ride.Version != 1 || ride.Attempt != 1 {
			t.Fatalf("created = %+v", ride)
		}
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusMatching || got.Version != 2 {
			t.Fatalf("after publish = %+v", got)
		}
		if len(bus.subjects(event.TopicRidesRequested)) != 1 {
			t.Fatalf("published = %v", bus.snapshot())
		}
		ev := decode[event.Requested](t, bus.snapshot()[0].value)
		if ev.Attempt != 1 || ev.TraceID != "req-abc123" || ev.RideID != ride.ID || len(ev.ExcludeDriverIDs) != 0 {
			t.Fatalf("requested = %+v", ev)
		}
	})

	t.Run("republish keeps event id", func(t *testing.T) {
		svc, store, bus := newTestService(t, 3)
		ride := mustCreate(t, svc, "k-replay", body, req)
		bus.setFail(true)
		if err := svc.PublishPending(t.Context()); err == nil {
			t.Fatal("publish succeeded while the bus was down")
		}
		if got := mustGet(t, svc, ride.ID); got.Status != StatusRequested || got.Version != 1 {
			t.Fatalf("failed publish changed the ride: %+v", got)
		}
		bus.setFail(false)
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		first := decode[event.Requested](t, bus.snapshot()[0].value)
		store.rewindPublished()
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		second := decode[event.Requested](t, bus.snapshot()[1].value)
		if first.EventID == "" || first.EventID != second.EventID {
			t.Fatalf("event ids = %s and %s", first.EventID, second.EventID)
		}
		if got := mustGet(t, svc, ride.ID); got.Status != StatusMatching || got.Version != 2 {
			t.Fatalf("replay bumped the ride: %+v", got)
		}
	})

	t.Run("cancel requested", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustCreate(t, svc, "k-cancel", body, req)
		got, err := svc.Cancel(t.Context(), ride.ID, "passenger")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusCancelled || got.Version != 2 || got.CancelReason != "passenger" {
			t.Fatalf("cancel = %+v", got)
		}
		if len(bus.snapshot()) != 0 {
			t.Fatal("cancel of a requested ride published")
		}
	})

	t.Run("cancel matching", func(t *testing.T) {
		svc, _, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-cancel-m", body, req)
		got, err := svc.Cancel(t.Context(), ride.ID, "")
		if err != nil || got.Status != StatusCancelled || got.Version != 3 {
			t.Fatalf("cancel = %+v %v", got, err)
		}
	})

	t.Run("matching to assigned", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-assign", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1, driver2)); err != nil {
			t.Fatal(err)
		}
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusAssigned || got.Version != 3 || got.AssignedDriverID != driver1 || got.AssignedETAS != 142 || got.ETAEngine != event.EngineDijkstra {
			t.Fatalf("assigned = %+v", got)
		}
		ev := decode[event.Assigned](t, bus.last(event.TopicRidesAssigned))
		if ev.DriverID != driver1 || ev.RideID != ride.ID || ev.TraceID != "req-abc123" {
			t.Fatalf("assigned event = %+v", ev)
		}
		if svc.Transitions(StatusMatching, StatusAssigned) != 1 {
			t.Fatalf("transitions = %d", svc.Transitions(StatusMatching, StatusAssigned))
		}
	})

	t.Run("requested can be assigned before publish", func(t *testing.T) {
		svc, _, _ := newTestService(t, 3)
		ride := mustCreate(t, svc, "k-early", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusAssigned || got.Version != 2 {
			t.Fatalf("early assign = %+v", got)
		}
	})

	t.Run("ranking exhausted", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-empty", body, req)
		if _, err := svc.cache.TryReserve(t.Context(), driver1, "018f9c00-5555-7abc-8def-0123456789ab"); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusNoDriver || got.Version != 3 || got.AssignedDriverID != "" {
			t.Fatalf("exhausted = %+v", got)
		}
		if svc.RankingExhausted() != 1 || svc.ReservationConflicts() != 1 {
			t.Fatalf("exhausted=%d conflicts=%d", svc.RankingExhausted(), svc.ReservationConflicts())
		}
		if bus.last(event.TopicRidesAssigned) != nil {
			t.Fatal("exhausted ranking published assigned")
		}
	})

	t.Run("reservation conflict takes the next driver", func(t *testing.T) {
		svc, _, _ := newTestService(t, 3)
		svc.cache = downCache{}
		first := mustMatching(t, svc, "k-a", body, req)
		second := mustMatching(t, svc, "k-b", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(first, 1, driver1, driver2)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), matched(second, 1, driver1, driver2)); err != nil {
			t.Fatal(err)
		}
		a := mustGet(t, svc, first.ID)
		b := mustGet(t, svc, second.ID)
		if a.Status != StatusAssigned || b.Status != StatusAssigned || a.AssignedDriverID != driver1 || b.AssignedDriverID != driver2 {
			t.Fatalf("a=%s %s b=%s %s", a.Status, a.AssignedDriverID, b.Status, b.AssignedDriverID)
		}
		if svc.ReservationConflicts() != 1 {
			t.Fatalf("conflicts = %d", svc.ReservationConflicts())
		}
	})

	t.Run("matched repeated and old attempt", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-dup", body, req)
		msg := matched(ride, 1, driver1)
		if err := svc.ApplyMatched(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), matched(ride, 0, driver2)); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusAssigned || got.Version != 3 || got.AssignedDriverID != driver1 {
			t.Fatalf("repeat = %+v", got)
		}
		if len(bus.subjects(event.TopicRidesAssigned)) != 1 {
			t.Fatalf("assigned events = %d", len(bus.subjects(event.TopicRidesAssigned)))
		}
	})

	t.Run("match failed and stale attempt", func(t *testing.T) {
		svc, _, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-fail", body, req)
		if err := svc.ApplyMatchFailed(t.Context(), event.MatchFailed{RideID: ride.ID, Attempt: 0, Reason: event.ReasonNoCandidate}); err != nil {
			t.Fatal(err)
		}
		if got := mustGet(t, svc, ride.ID); got.Status != StatusMatching || got.Version != 2 {
			t.Fatalf("stale fail = %+v", got)
		}
		if err := svc.ApplyMatchFailed(t.Context(), event.MatchFailed{RideID: ride.ID, Attempt: 1, Reason: event.ReasonNoneReachable}); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusNoDriver || got.Version != 3 {
			t.Fatalf("fail = %+v", got)
		}
		if _, err := svc.Cancel(t.Context(), ride.ID, ""); !errors.Is(err, ErrNotCancellable) {
			t.Fatalf("cancel terminal = %v", err)
		}
	})

	t.Run("lost rematch then second lost", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-lost", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyLost(t.Context(), presence(driver1, ride.ID)); err != nil {
			t.Fatal(err)
		}
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusMatching || got.Attempt != 2 || got.Version != 4 || len(got.ExcludedDriverIDs) != 1 || got.ExcludedDriverIDs[0] != driver1 {
			t.Fatalf("rematch = %+v", got)
		}
		if svc.Rematches() != 1 {
			t.Fatalf("rematches = %d", svc.Rematches())
		}
		rel := decode[event.Released](t, bus.last(event.TopicRidesReleased))
		if rel.DriverID != driver1 || rel.RideID != ride.ID {
			t.Fatalf("released = %+v", rel)
		}
		reqEv := decode[event.Requested](t, bus.last(event.TopicRidesRequested))
		if reqEv.Attempt != 2 || len(reqEv.ExcludeDriverIDs) != 1 || reqEv.ExcludeDriverIDs[0] != driver1 {
			t.Fatalf("rematch requested = %+v", reqEv)
		}
		version := got.Version
		if err := svc.ApplyLost(t.Context(), presence(driver1, ride.ID)); err != nil {
			t.Fatal(err)
		}
		again := mustGet(t, svc, ride.ID)
		if again.Version != version || again.Status != StatusMatching {
			t.Fatalf("second lost = %+v", again)
		}
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver2)); err != nil {
			t.Fatal(err)
		}
		if stale := mustGet(t, svc, ride.ID); stale.Status != StatusMatching || stale.Version != version {
			t.Fatalf("old attempt assigned = %+v", stale)
		}
	})

	t.Run("lost over the attempt limit", func(t *testing.T) {
		svc, _, bus := newTestService(t, 1)
		ride := mustMatching(t, svc, "k-limit", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyLost(t.Context(), presence(driver1, ride.ID)); err != nil {
			t.Fatal(err)
		}
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusNoDriver || got.Attempt != 1 || got.Version != 4 {
			t.Fatalf("limit = %+v", got)
		}
		if bus.last(event.TopicRidesReleased) == nil || bus.last(event.TopicRidesRequested) == nil {
			t.Fatal("limit lost must release and must not drop the original request")
		}
		if len(bus.subjects(event.TopicRidesRequested)) != 1 {
			t.Fatalf("requested events = %d, want the original only", len(bus.subjects(event.TopicRidesRequested)))
		}
		if svc.Rematches() != 0 {
			t.Fatalf("rematches = %d", svc.Rematches())
		}
	})

	t.Run("cancel assigned publishes released", func(t *testing.T) {
		svc, _, bus := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-cancel-a", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		got, err := svc.Cancel(t.Context(), ride.ID, "changed mind")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusCancelled || got.Version != 4 || got.AssignedDriverID != "" {
			t.Fatalf("cancel = %+v", got)
		}
		if err := svc.PublishPending(t.Context()); err != nil {
			t.Fatal(err)
		}
		rel := decode[event.Released](t, bus.last(event.TopicRidesReleased))
		if rel.DriverID != driver1 {
			t.Fatalf("released = %+v", rel)
		}
		again, err := svc.Cancel(t.Context(), ride.ID, "again")
		if err != nil {
			t.Fatal(err)
		}
		if again.Version != got.Version || again.Status != StatusCancelled || again.CancelReason != "changed mind" {
			t.Fatalf("repeat cancel = %+v", again)
		}
		if len(bus.subjects(event.TopicRidesReleased)) != 1 {
			t.Fatalf("released events = %d", len(bus.subjects(event.TopicRidesReleased)))
		}
	})

	t.Run("same idempotency key", func(t *testing.T) {
		svc, _, _ := newTestService(t, 3)
		svc.cache.(*MemoryCache).Down = true
		first, created, err := svc.Create(t.Context(), "same", body, req)
		if err != nil || !created {
			t.Fatalf("create = %+v %v %v", first, created, err)
		}
		again, created, err := svc.Create(t.Context(), "same", body, req)
		if err != nil || created || again.ID != first.ID {
			t.Fatalf("replay = %+v created=%v err=%v", again, created, err)
		}
		other := Request{PassengerID: passenger, PickupLat: 1, PickupLng: 2, VehicleClass: "comfort", TraceID: "x"}
		_, _, err = svc.Create(t.Context(), "same", []byte(`{"different":true}`), other)
		if !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("conflict = %v", err)
		}
	})
}

func TestConcurrentAssignment(t *testing.T) {
	body := []byte(`{"passenger_id":"` + passenger + `"}`)
	req := Request{PassengerID: passenger, PickupLat: -23.5, PickupLng: -46.6, VehicleClass: "standard", TraceID: "t"}
	svc, _, _ := newTestService(t, 3)
	a := mustMatching(t, svc, "conc-a", body, req)
	b := mustMatching(t, svc, "conc-b", body, req)
	ctx := t.Context()
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errc <- svc.ApplyMatched(ctx, matched(a, 1, driver1, driver2))
	}()
	go func() {
		defer wg.Done()
		errc <- svc.ApplyMatched(ctx, matched(b, 1, driver1, driver2))
	}()
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	left := mustGet(t, svc, a.ID)
	right := mustGet(t, svc, b.ID)
	if left.Status != StatusAssigned || right.Status != StatusAssigned || left.AssignedDriverID == right.AssignedDriverID {
		t.Fatalf("left=%s %s right=%s %s", left.Status, left.AssignedDriverID, right.Status, right.AssignedDriverID)
	}
}

type published struct {
	topic string
	key   string
	value []byte
}

type scriptBus struct {
	mu      sync.Mutex
	fail    bool
	records []published
}

func (b *scriptBus) setFail(fail bool) {
	b.mu.Lock()
	b.fail = fail
	b.mu.Unlock()
}

func (b *scriptBus) Publish(_ context.Context, topic, key string, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errors.New("kafka down")
	}
	copied := append([]byte(nil), value...)
	b.records = append(b.records, published{topic: topic, key: key, value: copied})
	return nil
}

func (b *scriptBus) Consume(ctx context.Context, _ func(context.Context, string, []byte) error) error {
	<-ctx.Done()
	return nil
}

func (b *scriptBus) snapshot() []published {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]published, len(b.records))
	copy(out, b.records)
	return out
}

func (b *scriptBus) subjects(topic string) []published {
	var out []published
	for _, rec := range b.snapshot() {
		if rec.topic == topic {
			out = append(out, rec)
		}
	}
	return out
}

func (b *scriptBus) last(topic string) []byte {
	rows := b.subjects(topic)
	if len(rows) == 0 {
		return nil
	}
	return rows[len(rows)-1].value
}

func newTestService(t *testing.T, maxAttempts int) (*Service, *Memory, *scriptBus) {
	t.Helper()
	store := NewMemory()
	cache := NewMemoryCache()
	bus := &scriptBus{}
	svc := New(slog.New(slog.DiscardHandler), store, cache, bus, maxAttempts)
	return svc, store, bus
}

func mustCreate(t *testing.T, svc *Service, key string, body []byte, req Request) Ride {
	t.Helper()
	ride, created, err := svc.Create(t.Context(), key, body, req)
	if err != nil || !created {
		t.Fatalf("create %s: %v created=%v", key, err, created)
	}
	return ride
}

func mustMatching(t *testing.T, svc *Service, key string, body []byte, req Request) Ride {
	t.Helper()
	ride := mustCreate(t, svc, key, body, req)
	if err := svc.PublishPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	return mustGet(t, svc, ride.ID)
}

func mustGet(t *testing.T, svc *Service, id string) Ride {
	t.Helper()
	ride, err := svc.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ride
}

func matched(ride Ride, attempt int, drivers ...string) event.Matched {
	ranking := make([]event.Candidate, len(drivers))
	for i, id := range drivers {
		ranking[i] = event.Candidate{DriverID: id, ETASeconds: 142, DistanceM: 820, Location: event.Point{Lat: ride.PickupLat, Lng: ride.PickupLng}}
	}
	return event.Matched{
		EventID: "evt", TraceID: ride.TraceID, RideID: ride.ID, Attempt: attempt, Engine: event.EngineDijkstra, Ranking: ranking,
	}
}

func presence(driverID, rideID string) event.Presence {
	return event.Presence{EventID: "lost-evt", TraceID: "sweep", DriverID: driverID, Lost: event.Lost{RideID: rideID}}
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	got, err := event.Decode[T](raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}
