package matching

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

func TestWriteMetricsRecordsEnginesAndFailures(t *testing.T) {
	svc := New(slog.New(slog.DiscardHandler), &fakeNearby{drivers: []NearbyDriver{
		{ID: "near", Lat: -23.562, Lng: -46.657},
	}}, &fakeETA{estimates: []Estimate{
		{OriginIndex: 0, DurationS: 10, DistanceM: 80, Reachable: true},
	}}, newMemDedup(), &fakePub{}, Options{})

	dijkstra := sampleRequest()
	dijkstra.EventID = "evt-dijkstra"
	if err := svc.Match(t.Context(), dijkstra); err != nil {
		t.Fatal(err)
	}

	fallbackReq := sampleRequest()
	fallbackReq.EventID = "evt-fallback"
	fallbackReq.RideID = "018f3a2b-2222-7abc-8def-0123456789ac"
	svc.eta = &fakeETA{err: errors.New("routing down")}
	if err := svc.Match(t.Context(), fallbackReq); err != nil {
		t.Fatal(err)
	}

	empty := sampleRequest()
	empty.EventID = "evt-empty"
	empty.RideID = "018f3a2b-2222-7abc-8def-0123456789ad"
	svc.nearby = &fakeNearby{}
	if err := svc.Match(t.Context(), empty); err != nil {
		t.Fatal(err)
	}

	none := sampleRequest()
	none.EventID = "evt-none"
	none.RideID = "018f3a2b-2222-7abc-8def-0123456789ae"
	svc.nearby = &fakeNearby{drivers: []NearbyDriver{{ID: "a", Lat: 1, Lng: 1}}}
	svc.eta = &fakeETA{estimates: []Estimate{{OriginIndex: 0, Reachable: false}}}
	if err := svc.Match(t.Context(), none); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	if err := WriteMetrics(&buf, svc.Counters(), true); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{
		`matching_rankings_total{engine="dijkstra-one-to-many"} 1`,
		`matching_rankings_total{engine="haversine-fallback"} 1`,
		`matching_fallback_total 1`,
		`matching_unreachable_total 1`,
		`matching_failed_total{reason="no_candidate"} 1`,
		`matching_failed_total{reason="none_reachable"} 1`,
		`matching_routing_breaker_open 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %s\n%s", want, body)
		}
	}
}

func TestProbeMetrics(t *testing.T) {
	probe := &Probe{}
	rec := httptest.NewRecorder()
	probe.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}

	rec = httptest.NewRecorder()
	probe.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `matching_rankings_total{engine="dijkstra-one-to-many"} 0`) {
		t.Fatalf("empty probe = %s", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("Content-Type = %q", got)
	}

	now := time.Unix(0, 0)
	eta := NewETAClient("http://routing", ClientOptions{Now: func() time.Time { return now }})
	for range breakerFailures {
		eta.breaker.Failure()
	}
	svc := New(slog.New(slog.DiscardHandler), &fakeNearby{drivers: []NearbyDriver{
		{ID: "near", Lat: -23.562, Lng: -46.657},
	}}, &fakeETA{err: errors.New("routing down")}, newMemDedup(), &fakePub{}, Options{})
	if err := svc.Match(t.Context(), sampleRequest()); err != nil {
		t.Fatal(err)
	}
	probe.Set(svc, eta)

	rec = httptest.NewRecorder()
	probe.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `matching_rankings_total{engine="`+event.EngineHaversine+`"} 1`) {
		t.Fatalf("probe = %s", body)
	}
	if !strings.Contains(body, "matching_routing_breaker_open 1") {
		t.Fatalf("breaker = %s", body)
	}
}

func TestMetricsRouteWinsOverCatchAll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.Handle("GET /metrics", &Probe{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "matching_fallback_total") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
