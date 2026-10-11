package ride

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Matheusmottaa/nearby-drivers/internal/depend"
	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

// TestOutboxAndAssignment needs the compose stack:
//
//	RIDE_DATABASE_URL=postgres://ride:ride@localhost:5432/nearby?sslmode=disable
//	REDIS_URL=redis://localhost:6379
//	KAFKA_BROKERS=localhost:9092
func TestOutboxAndAssignment(t *testing.T) {
	dbURL := os.Getenv("RIDE_DATABASE_URL")
	redisURL := os.Getenv("REDIS_URL")
	brokers := os.Getenv("KAFKA_BROKERS")
	if dbURL == "" || redisURL == "" || brokers == "" {
		t.Skip("set RIDE_DATABASE_URL, REDIS_URL and KAFKA_BROKERS")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()

	pool, err := depend.OpenPostgres(ctx, dbURL)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := ApplyDDL(ctx, pool); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	rdb, err := depend.OpenRedis(ctx, redisURL)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	cl, err := depend.OpenKafka(ctx, brokers)
	if err != nil {
		t.Fatalf("kafka: %v", err)
	}
	t.Cleanup(cl.Close)

	log := slog.New(slog.DiscardHandler)
	store := NewPostgres(pool)
	svc := New(log, store, NewRedis(rdb), NewKafka(cl, brokers, log), 3)
	body := []byte(`{"passenger_id":"` + passenger + `","pickup":{"lat":-23.5613,"lng":-46.6565},"vehicle_class":"standard"}`)
	req := Request{PassengerID: passenger, PickupLat: -23.5613, PickupLng: -46.6565, VehicleClass: "standard", TraceID: "req-int"}

	ride, created, err := svc.Create(ctx, "int-"+mustID(t), body, req)
	if err != nil || !created {
		t.Fatalf("create: %v created=%v", err, created)
	}
	var outboxID string
	var published *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT event_id::text, published_at FROM outbox WHERE aggregate_id = $1::uuid`, ride.ID).
		Scan(&outboxID, &published); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if published != nil || outboxID == "" {
		t.Fatalf("outbox event %s published=%v", outboxID, published)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM rides WHERE id = $1::uuid`, ride.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusRequested {
		t.Fatalf("status = %s", status)
	}

	seen := make(chan event.Requested, 4)
	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()
	go consumeRequested(t, readCtx, brokers, ride.ID, seen)

	if err := svc.PublishPending(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	first := awaitRequested(t, seen)
	if first.EventID != outboxID {
		t.Fatalf("published event_id = %s, outbox = %s", first.EventID, outboxID)
	}
	got, err := svc.Get(ctx, ride.ID)
	if err != nil || got.Status != StatusMatching {
		t.Fatalf("after publish = %+v %v", got, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE outbox SET published_at = NULL WHERE aggregate_id = $1::uuid`, ride.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishPending(ctx); err != nil {
		t.Fatalf("republish: %v", err)
	}
	second := awaitRequested(t, seen)
	if second.EventID != first.EventID {
		t.Fatalf("republish event_id = %s, want %s", second.EventID, first.EventID)
	}
	got, err = svc.Get(ctx, ride.ID)
	if err != nil || got.Status != StatusMatching || got.Version != 2 {
		t.Fatalf("republish changed the ride: %+v %v", got, err)
	}

	otherBody := body
	other, _, err := svc.Create(ctx, "int-"+mustID(t), otherBody, req)
	if err != nil {
		t.Fatal(err)
	}
	down := New(log, store, downCache{}, NewKafka(cl, brokers, log), 3)
	var wg sync.WaitGroup
	errc := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errc <- down.ApplyMatched(ctx, matched(got, got.Attempt, driver1, driver2))
	}()
	go func() {
		defer wg.Done()
		errc <- down.ApplyMatched(ctx, matched(other, other.Attempt, driver1, driver2))
	}()
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	left, err := svc.Get(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	right, err := svc.Get(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if left.Status != StatusAssigned || right.Status != StatusAssigned || left.AssignedDriverID == right.AssignedDriverID {
		t.Fatalf("left=%s %s right=%s %s", left.Status, left.AssignedDriverID, right.Status, right.AssignedDriverID)
	}
	drivers := map[string]bool{left.AssignedDriverID: true, right.AssignedDriverID: true}
	if !drivers[driver1] || !drivers[driver2] {
		t.Fatalf("drivers = %v", drivers)
	}
}

func consumeRequested(t *testing.T, ctx context.Context, brokers, rideID string, out chan<- event.Requested) {
	t.Helper()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers),
		kgo.ConsumeTopics(event.TopicRidesRequested),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DialTimeout(5*time.Second),
	)
	if err != nil {
		t.Errorf("consumer: %v", err)
		return
	}
	defer cl.Close()
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		fetches.EachRecord(func(r *kgo.Record) {
			ev, err := event.Decode[event.Requested](r.Value)
			if err != nil || ev.RideID != rideID {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		})
	}
}

func awaitRequested(t *testing.T, seen <-chan event.Requested) event.Requested {
	t.Helper()
	select {
	case ev := <-seen:
		return ev
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for rides.requested")
	}
	return event.Requested{}
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
