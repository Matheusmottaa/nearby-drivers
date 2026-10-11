package matching

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func TestNearbyRetriesThenDecodes(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/drivers/nearby" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-Request-Id") != "req-1" {
			t.Errorf("trace = %q", r.Header.Get("X-Request-Id"))
		}
		q := r.URL.Query()
		if q.Get("k") != "10" || q.Get("class") != "standard" || q.Get("radius_m") != "3000" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"drivers": []map[string]any{{"id": "d1", "lat": -23.56, "lng": -46.65}},
			"index":   "kdtree",
		})
	}))
	t.Cleanup(srv.Close)

	c := NewNearbyClient(srv.URL, ClientOptions{Timeout: defaultTimeout, Retries: 2, Sleep: func(context.Context, time.Duration) error { return nil }})
	got, err := c.Nearby(t.Context(), "req-1", geo.Point{Lat: -23.5613, Lng: -46.6565}, 3000, 10, "standard")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if len(got) != 1 || got[0].ID != "d1" {
		t.Fatalf("drivers = %+v", got)
	}
}

func TestETABatchBodyAndBreaker(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/routes/eta:batch" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Destination geoPoint   `json:"destination"`
			Origins     []geoPoint `json:"origins"`
			MaxRadiusM  float64    `json:"max_radius_m"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Origins) != 1 || body.MaxRadiusM != 3000 || body.Destination.Lat != -23.5613 {
			t.Fatalf("body = %+v", body)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := NewETAClient(srv.URL, ClientOptions{Timeout: 50 * time.Millisecond, Retries: 0, Sleep: func(context.Context, time.Duration) error { return nil }})
	origins := []geo.Point{{Lat: -23.56, Lng: -46.65}}
	dest := geo.Point{Lat: -23.5613, Lng: -46.6565}
	for range breakerFailures {
		if _, err := c.Batch(t.Context(), "t", dest, origins, 3000); err == nil {
			t.Fatal("expected status error")
		}
	}
	if calls != breakerFailures {
		t.Fatalf("calls = %d, want %d", calls, breakerFailures)
	}
	_, err := c.Batch(t.Context(), "t", dest, origins, 3000)
	if err != ErrBreakerOpen {
		t.Fatalf("Batch() error = %v, want open breaker", err)
	}
	if calls != breakerFailures {
		t.Fatalf("open breaker still called routing, calls = %d", calls)
	}
}

func TestCallTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := NewNearbyClient(srv.URL, ClientOptions{Timeout: 30 * time.Millisecond, Retries: 0})
	start := time.Now()
	_, err := c.Nearby(t.Context(), "t", geo.Point{Lat: 1, Lng: 2}, 10, 1, "standard")
	if err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout took %s", time.Since(start))
	}
}
