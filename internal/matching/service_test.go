package matching

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func TestMatchRanksByETAAndDropsExcludedBeforeETA(t *testing.T) {
	near := &fakeNearby{drivers: []NearbyDriver{
		{ID: "excluded", Lat: 1, Lng: 1},
		{ID: "slow", Lat: 2, Lng: 2},
		{ID: "fast", Lat: 3, Lng: 3},
		{ID: "dead", Lat: 4, Lng: 4},
	}}
	eta := &fakeETA{estimates: []Estimate{
		{OriginIndex: 0, DurationS: 400, DistanceM: 4000, Reachable: true},
		{OriginIndex: 1, DurationS: 90, DistanceM: 800, Reachable: true},
		{OriginIndex: 2, DurationS: 10, DistanceM: 100, Reachable: false},
	}}
	pub := &fakePub{}
	dedup := newMemDedup()
	svc := New(slog.New(slog.DiscardHandler), near, eta, dedup, pub, Options{})

	req := sampleRequest()
	req.ExcludeDriverIDs = []string{"excluded"}
	req.Attempt = 4
	if err := svc.Match(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if near.k != defaultK {
		t.Fatalf("nearby k = %d, want %d", near.k, defaultK)
	}
	if len(eta.origins) != 3 || eta.origins[0].Lat != 2 {
		t.Fatalf("eta origins = %+v, excluded driver must be gone before the call", eta.origins)
	}
	got := pub.matched(t)
	if got.Attempt != 4 || got.TraceID != req.TraceID || got.Engine != event.EngineDijkstra {
		t.Fatalf("matched = %+v", got)
	}
	if len(got.Ranking) != 2 || got.Ranking[0].DriverID != "fast" || got.Ranking[1].DriverID != "slow" {
		t.Fatalf("ranking = %+v", got.Ranking)
	}
	if got.Ranking[0].ETASeconds != 90 || got.Ranking[0].Location.Lat != 3 {
		t.Fatalf("winner = %+v", got.Ranking[0])
	}
}

func TestMatchTieBreaksByDistanceThenID(t *testing.T) {
	near := &fakeNearby{drivers: []NearbyDriver{
		{ID: "c", Lat: 1, Lng: 1},
		{ID: "a", Lat: 2, Lng: 2},
		{ID: "b", Lat: 3, Lng: 3},
	}}
	eta := &fakeETA{estimates: []Estimate{
		{OriginIndex: 0, DurationS: 10, DistanceM: 5, Reachable: true},
		{OriginIndex: 1, DurationS: 10, DistanceM: 5, Reachable: true},
		{OriginIndex: 2, DurationS: 10, DistanceM: 3, Reachable: true},
	}}
	pub := &fakePub{}
	svc := New(slog.New(slog.DiscardHandler), near, eta, newMemDedup(), pub, Options{})
	if err := svc.Match(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	got := pub.matched(t)
	ids := []string{got.Ranking[0].DriverID, got.Ranking[1].DriverID, got.Ranking[2].DriverID}
	if ids[0] != "b" || ids[1] != "a" || ids[2] != "c" {
		t.Fatalf("ranking = %v, want b, a, c", ids)
	}
}

func TestMatchFallsBackToHaversine(t *testing.T) {
	pickup := geo.Point{Lat: -23.5613, Lng: -46.6565}
	drivers := []NearbyDriver{
		{ID: "far", Lat: -23.57, Lng: -46.66},
		{ID: "near", Lat: -23.562, Lng: -46.657},
	}
	near := &fakeNearby{drivers: drivers}
	eta := &fakeETA{err: errors.New("routing down")}
	pub := &fakePub{}
	svc := New(slog.New(slog.DiscardHandler), near, eta, newMemDedup(), pub, Options{})
	if err := svc.Match(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if len(pub.failed) != 0 {
		t.Fatal("routing failure published match-failed")
	}
	got := pub.matched(t)
	if got.Engine != event.EngineHaversine || len(got.Ranking) != 2 {
		t.Fatalf("matched = %+v", got)
	}
	wantFar, err := geo.Fallback(geo.Point{Lat: drivers[0].Lat, Lng: drivers[0].Lng}, pickup, defaultSpeed)
	if err != nil {
		t.Fatal(err)
	}
	wantNear, err := geo.Fallback(geo.Point{Lat: drivers[1].Lat, Lng: drivers[1].Lng}, pickup, defaultSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ranking[0].DriverID != "near" || got.Ranking[0].ETASeconds != wantNear.DurationS || got.Ranking[0].DistanceM != wantNear.DistanceM {
		t.Fatalf("first = %+v, want near eta %d m %d", got.Ranking[0], wantNear.DurationS, wantNear.DistanceM)
	}
	if got.Ranking[1].DriverID != "far" || got.Ranking[1].ETASeconds != wantFar.DurationS {
		t.Fatalf("second = %+v, want far eta %d", got.Ranking[1], wantFar.DurationS)
	}
}

func TestMatchNoCandidate(t *testing.T) {
	pub := &fakePub{}
	eta := &fakeETA{}
	svc := New(slog.New(slog.DiscardHandler), &fakeNearby{}, eta, newMemDedup(), pub, Options{})
	req := sampleRequest()
	if err := svc.Match(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if eta.calls != 0 {
		t.Fatal("empty nearby still called eta")
	}
	got := pub.failedOnce(t)
	if got.Reason != event.ReasonNoCandidate || got.Attempt != req.Attempt || got.TraceID != req.TraceID {
		t.Fatalf("failed = %+v", got)
	}

	near := &fakeNearby{drivers: []NearbyDriver{{ID: "gone", Lat: 1, Lng: 1}}}
	pub = &fakePub{}
	svc = New(slog.New(slog.DiscardHandler), near, eta, newMemDedup(), pub, Options{})
	req.ExcludeDriverIDs = []string{"gone"}
	if err := svc.Match(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if eta.calls != 0 {
		t.Fatal("exclusion still called eta")
	}
	if pub.failedOnce(t).Reason != event.ReasonNoCandidate {
		t.Fatal(pub.failed)
	}
}

func TestMatchNoneReachable(t *testing.T) {
	near := &fakeNearby{drivers: []NearbyDriver{{ID: "a", Lat: 1, Lng: 1}, {ID: "b", Lat: 2, Lng: 2}}}
	eta := &fakeETA{estimates: []Estimate{
		{OriginIndex: 0, Reachable: false},
		{OriginIndex: 1, Reachable: false},
	}}
	pub := &fakePub{}
	svc := New(slog.New(slog.DiscardHandler), near, eta, newMemDedup(), pub, Options{})
	if err := svc.Match(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if len(pub.matchedRaw) != 0 {
		t.Fatal("unreachable drivers published a ranking")
	}
	if pub.failedOnce(t).Reason != event.ReasonNoneReachable {
		t.Fatalf("failed = %+v", pub.failed)
	}
}

func TestMatchDuplicateEventPublishesOnce(t *testing.T) {
	near := &fakeNearby{drivers: []NearbyDriver{{ID: "a", Lat: 1, Lng: 1}}}
	eta := &fakeETA{estimates: []Estimate{{OriginIndex: 0, DurationS: 1, DistanceM: 1, Reachable: true}}}
	pub := &fakePub{}
	svc := New(slog.New(slog.DiscardHandler), near, eta, newMemDedup(), pub, Options{})
	req := sampleRequest()
	if err := svc.Match(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err := svc.Match(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(pub.matchedRaw) != 1 || near.calls != 1 {
		t.Fatalf("publishes = %d nearby = %d", len(pub.matchedRaw), near.calls)
	}
}

func TestMatchPublishFailureDoesNotMarkSeen(t *testing.T) {
	near := &fakeNearby{drivers: []NearbyDriver{{ID: "a", Lat: 1, Lng: 1}}}
	eta := &fakeETA{estimates: []Estimate{{OriginIndex: 0, DurationS: 1, DistanceM: 1, Reachable: true}}}
	pub := &fakePub{err: errors.New("broker down")}
	dedup := newMemDedup()
	svc := New(slog.New(slog.DiscardHandler), near, eta, dedup, pub, Options{})
	req := sampleRequest()
	if err := svc.Match(t.Context(), req); err == nil {
		t.Fatal("publish error was ignored")
	}
	if dedup.marks != 0 {
		t.Fatal("failed publish was marked seen")
	}
	pub.err = nil
	if err := svc.Match(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(pub.matchedRaw) != 1 || dedup.marks != 1 {
		t.Fatalf("publishes = %d marks = %d", len(pub.matchedRaw), dedup.marks)
	}
}

func TestMatchContinuesWhenRedisIsDown(t *testing.T) {
	near := &fakeNearby{drivers: []NearbyDriver{{ID: "a", Lat: 1, Lng: 1}}}
	eta := &fakeETA{estimates: []Estimate{{OriginIndex: 0, DurationS: 1, DistanceM: 1, Reachable: true}}}
	pub := &fakePub{}
	svc := New(slog.New(slog.DiscardHandler), near, eta, errDedup{}, pub, Options{})
	if err := svc.Match(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if len(pub.matchedRaw) != 1 {
		t.Fatal("redis outage dropped the ranking")
	}
}

func TestMatchCapsK(t *testing.T) {
	near := &fakeNearby{}
	svc := New(slog.New(slog.DiscardHandler), near, &fakeETA{}, newMemDedup(), &fakePub{}, Options{K: 80})
	if err := svc.Match(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	if near.k != maxK {
		t.Fatalf("nearby k = %d, want %d", near.k, maxK)
	}
}

func TestHandleAcksUndecodableRecord(t *testing.T) {
	svc := New(slog.New(slog.DiscardHandler), &fakeNearby{}, &fakeETA{}, newMemDedup(), &fakePub{}, Options{})
	if err := svc.Handle(t.Context(), event.TopicRidesRequested, []byte("{")); err != nil {
		t.Fatal(err)
	}
}

func sampleRequest() event.Requested {
	return event.Requested{
		EventID:      "018f3a2b-aaaa-7abc-8def-0123456789ab",
		TraceID:      "req-abc123",
		RideID:       "018f3a2b-2222-7abc-8def-0123456789ab",
		Attempt:      1,
		Pickup:       event.Point{Lat: -23.5613, Lng: -46.6565},
		VehicleClass: "standard",
	}
}

type fakeNearby struct {
	drivers []NearbyDriver
	err     error
	k       int
	calls   int
}

func (f *fakeNearby) Nearby(_ context.Context, _ string, _ geo.Point, _ float64, k int, _ string) ([]NearbyDriver, error) {
	f.calls++
	f.k = k
	return f.drivers, f.err
}

type fakeETA struct {
	estimates []Estimate
	origins   []geo.Point
	err       error
	calls     int
}

func (f *fakeETA) Batch(_ context.Context, _ string, _ geo.Point, origins []geo.Point, _ float64) ([]Estimate, error) {
	f.calls++
	f.origins = origins
	return f.estimates, f.err
}

type fakePub struct {
	err        error
	matchedRaw [][]byte
	failed     [][]byte
	keys       []string
}

func (f *fakePub) Publish(_ context.Context, topic, key string, value []byte) error {
	if f.err != nil {
		return f.err
	}
	f.keys = append(f.keys, key)
	switch topic {
	case event.TopicRidesMatched:
		f.matchedRaw = append(f.matchedRaw, value)
	case event.TopicRidesMatchFailed:
		f.failed = append(f.failed, value)
	default:
		return errors.New("unexpected topic")
	}
	return nil
}

func (f *fakePub) matched(t *testing.T) event.Matched {
	t.Helper()
	if len(f.matchedRaw) != 1 {
		t.Fatalf("matched publishes = %d", len(f.matchedRaw))
	}
	got, err := event.Decode[event.Matched](f.matchedRaw[0])
	if err != nil {
		t.Fatal(err)
	}
	if f.keys[0] == "" {
		t.Fatal("empty kafka key")
	}
	return got
}

func (f *fakePub) failedOnce(t *testing.T) event.MatchFailed {
	t.Helper()
	if len(f.failed) != 1 {
		t.Fatalf("match-failed publishes = %d", len(f.failed))
	}
	got, err := event.Decode[event.MatchFailed](f.failed[0])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

type memDedup struct {
	seen  map[string]bool
	marks int
}

func newMemDedup() *memDedup {
	return &memDedup{seen: map[string]bool{}}
}

func (d *memDedup) Seen(_ context.Context, id string) (bool, error) {
	return d.seen[id], nil
}

func (d *memDedup) Mark(_ context.Context, id string) error {
	d.seen[id] = true
	d.marks++
	return nil
}

type errDedup struct{}

func (errDedup) Seen(context.Context, string) (bool, error) {
	return false, errors.New("redis down")
}
func (errDedup) Mark(context.Context, string) error { return errors.New("redis down") }
