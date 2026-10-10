package driver

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Matheusmottaa/nearby-drivers/internal/depend"
	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

// TestReplayRestoresSlot needs the compose broker and the driver database:
//
//	KAFKA_BROKERS=localhost:9092 DRIVER_DATABASE_URL=postgres://driver:driver@localhost:5432/nearby?sslmode=disable
func TestReplayRestoresSlot(t *testing.T) {
	brokers := os.Getenv("KAFKA_BROKERS")
	dbURL := os.Getenv("DRIVER_DATABASE_URL")
	if brokers == "" || dbURL == "" {
		t.Skip("set KAFKA_BROKERS and DRIVER_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	pool, err := depend.OpenPostgres(ctx, dbURL)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := ApplyDDL(ctx, pool); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	cl, err := depend.OpenKafka(ctx, brokers)
	if err != nil {
		t.Fatalf("kafka: %v", err)
	}
	t.Cleanup(cl.Close)

	svc := New(slog.New(slog.DiscardHandler), NewPostgres(pool), noopCache{}, NewKafka(cl, brokers))
	key, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	plate := key[:8]
	created, ok, err := svc.Create(ctx, key, Registration{
		Name: "Replay", Phone: "11999999999", VehicleClass: "standard", Plate: plate,
	})
	if err != nil || !ok {
		t.Fatalf("create: %v created=%v", err, ok)
	}

	produce := func(seq int64, lat float64) {
		t.Helper()
		raw, err := event.Encode(event.Location{
			EventID: key, TraceID: "replay-test", DriverID: created.ID,
			Lat: lat, Lng: -46.65, Seq: seq, ObservedAt: time.Now().UTC(), AccuracyM: 5,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.ProduceSync(ctx, &kgo.Record{
			Topic: event.TopicDriversLocation,
			Key:   []byte(created.ID),
			Value: raw,
		}).FirstErr(); err != nil {
			t.Fatalf("produce seq %d: %v", seq, err)
		}
	}
	produce(1, -23.1)
	produce(4, -23.4)
	produce(2, -23.2)

	if err := svc.Boot(ctx); err != nil {
		t.Fatalf("boot: %v", err)
	}
	sl, found := svc.Slot(created.ID)
	if !found || !sl.HasFix || sl.Seq != 4 || sl.Lat != -23.4 {
		t.Fatalf("slot = %+v found=%v, want seq 4 at lat -23.4 from the replay", sl, found)
	}
	again, againOK, err := svc.Create(ctx, key, Registration{
		Name: "Other", Phone: "11888888888", VehicleClass: "comfort", Plate: plate + "x",
	})
	if err != nil || againOK || again.ID != created.ID || again.InternalID != created.InternalID {
		t.Fatalf("idempotent create = %+v created=%v err=%v", again, againOK, err)
	}
}
