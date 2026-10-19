package ride

import (
	"errors"
	"slices"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

func TestApplyMatchedSavesRanking(t *testing.T) {
	body := []byte(`{"passenger_id":"` + passenger + `","pickup":{"lat":-23.5613,"lng":-46.6565},"vehicle_class":"standard"}`)
	req := Request{PassengerID: passenger, PickupLat: -23.5613, PickupLng: -46.6565, VehicleClass: "standard", TraceID: "req-rank"}
	const other = "018f9c00-5555-7abc-8def-0123456789ab"

	t.Run("keeps the order when the first driver is taken", func(t *testing.T) {
		svc, store, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-rank", body, req)
		if _, err := svc.cache.TryReserve(t.Context(), driver1, other); err != nil {
			t.Fatal(err)
		}
		msg := matched(ride, 1, driver1, driver2)
		msg.Ranking[1].ETASeconds = 200
		if err := svc.ApplyMatched(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, svc, ride.ID)
		if got.Status != StatusAssigned || got.AssignedDriverID != driver2 {
			t.Fatalf("assigned = %s %s, want %s %s", got.Status, got.AssignedDriverID, StatusAssigned, driver2)
		}
		saved, ok := store.ranking(ride.ID, 1)
		if !ok {
			t.Fatal("ranking not saved")
		}
		if ids := candidateIDs(saved); !slices.Equal(ids, []string{driver1, driver2}) {
			t.Fatalf("ranking = %v, want %v", ids, []string{driver1, driver2})
		}
		if saved.Ranking[1].ETASeconds != 200 || saved.Engine != event.EngineDijkstra || saved.EventID != msg.EventID {
			t.Fatalf("saved = %+v", saved)
		}
	})

	t.Run("exhausted ranking is saved", func(t *testing.T) {
		svc, store, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-rank-none", body, req)
		if _, err := svc.cache.TryReserve(t.Context(), driver1, other); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		if got := mustGet(t, svc, ride.ID); got.Status != StatusNoDriver {
			t.Fatalf("status = %s, want %s", got.Status, StatusNoDriver)
		}
		saved, ok := store.ranking(ride.ID, 1)
		if !ok || !slices.Equal(candidateIDs(saved), []string{driver1}) {
			t.Fatalf("ranking = %+v %v, want [%s]", saved, ok, driver1)
		}
	})

	t.Run("empty ranking is saved", func(t *testing.T) {
		svc, store, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-rank-empty", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1)); err != nil {
			t.Fatal(err)
		}
		saved, ok := store.ranking(ride.ID, 1)
		if !ok || len(saved.Ranking) != 0 {
			t.Fatalf("ranking = %+v %v, want empty", saved, ok)
		}
	})

	t.Run("ignored deliveries are not saved", func(t *testing.T) {
		svc, store, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-rank-old", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 0, driver1)); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.ranking(ride.ID, 0); ok {
			t.Fatal("ranking of an older attempt was saved")
		}
		unknown := matched(ride, 1, driver1)
		unknown.Engine = "teleport"
		if err := svc.ApplyMatched(t.Context(), unknown); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.ranking(ride.ID, 1); ok {
			t.Fatal("ranking with an unknown engine was saved")
		}
		if _, err := svc.Cancel(t.Context(), ride.ID, "passenger"); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.ranking(ride.ID, 1); ok {
			t.Fatal("ranking of a cancelled ride was saved")
		}
	})

	t.Run("store failure leaves the ride matching", func(t *testing.T) {
		svc, store, bus := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-rank-fail", body, req)
		errDB := errors.New("db down")
		store.failRankings(errDB)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1)); !errors.Is(err, errDB) {
			t.Fatalf("ApplyMatched() error = %v, want %v", err, errDB)
		}
		if got := mustGet(t, svc, ride.ID); got.Status != StatusMatching || got.AssignedDriverID != "" {
			t.Fatalf("after failure = %s %q, want %s with no driver", got.Status, got.AssignedDriverID, StatusMatching)
		}
		if ok, err := svc.cache.TryReserve(t.Context(), driver1, other); err != nil || !ok {
			t.Fatalf("driver1 reserved after a failed save: ok=%v err=%v", ok, err)
		}
		if bus.last(event.TopicRidesAssigned) != nil {
			t.Fatal("failed save published assigned")
		}

		store.failRankings(nil)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver2)); err != nil {
			t.Fatal(err)
		}
		if got := mustGet(t, svc, ride.ID); got.Status != StatusAssigned || got.AssignedDriverID != driver2 {
			t.Fatalf("redelivery = %s %s, want %s %s", got.Status, got.AssignedDriverID, StatusAssigned, driver2)
		}
	})

	t.Run("rematch keeps one ranking per attempt", func(t *testing.T) {
		svc, store, _ := newTestService(t, 3)
		ride := mustMatching(t, svc, "k-rank-lost", body, req)
		if err := svc.ApplyMatched(t.Context(), matched(ride, 1, driver1, driver2)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyLost(t.Context(), presence(driver1, ride.ID)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ApplyMatched(t.Context(), matched(ride, 2, driver2)); err != nil {
			t.Fatal(err)
		}
		first, ok1 := store.ranking(ride.ID, 1)
		second, ok2 := store.ranking(ride.ID, 2)
		if !ok1 || !ok2 {
			t.Fatalf("rankings saved: attempt 1 %v, attempt 2 %v", ok1, ok2)
		}
		if !slices.Equal(candidateIDs(first), []string{driver1, driver2}) || !slices.Equal(candidateIDs(second), []string{driver2}) {
			t.Fatalf("attempt 1 = %v, attempt 2 = %v", candidateIDs(first), candidateIDs(second))
		}
	})
}

func TestMemorySaveRankingReplacesAttempt(t *testing.T) {
	store := NewMemory()
	ride := Ride{ID: "018f3a2b-2222-7abc-8def-0123456789ab", PickupLat: -23.5, PickupLng: -46.6}
	first := matched(ride, 1, driver1, driver2)
	if err := store.SaveRanking(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	first.Ranking[0].DriverID = driver2
	if got, _ := store.ranking(ride.ID, 1); got.Ranking[0].DriverID != driver1 {
		t.Fatalf("stored ranking aliases the caller: first = %s, want %s", got.Ranking[0].DriverID, driver1)
	}
	later := matched(ride, 1, driver2)
	later.EventID = "evt-2"
	if err := store.SaveRanking(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	got, ok := store.ranking(ride.ID, 1)
	if !ok || got.EventID != "evt-2" || !slices.Equal(candidateIDs(got), []string{driver2}) {
		t.Fatalf("ranking = %+v %v, want evt-2 [%s]", got, ok, driver2)
	}
}

func candidateIDs(m event.Matched) []string {
	ids := make([]string, len(m.Ranking))
	for i, c := range m.Ranking {
		ids[i] = c.DriverID
	}
	return ids
}
