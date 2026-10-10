package event

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	observed := time.Date(2026, 9, 30, 18, 0, 0, 812000000, time.UTC)

	tests := []struct {
		name   string
		golden string
		value  any
		decode func([]byte) (any, error)
	}{
		{
			name: "rides.requested",
			golden: `{
				"event_id": "018f3a2b-aaaa-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
				"attempt": 1,
				"pickup": { "lat": -23.5613, "lng": -46.6565 },
				"vehicle_class": "standard",
				"exclude_driver_ids": []
			}`,
			value: Requested{
				EventID:          "018f3a2b-aaaa-7abc-8def-0123456789ab",
				TraceID:          "req-abc123",
				RideID:           "018f3a2b-2222-7abc-8def-0123456789ab",
				Attempt:          1,
				Pickup:           Point{Lat: -23.5613, Lng: -46.6565},
				VehicleClass:     "standard",
				ExcludeDriverIDs: []string{},
			},
			decode: decodeAs[Requested],
		},
		{
			name: "rides.matched",
			golden: `{
				"event_id": "018f3a2b-bbbb-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
				"attempt": 1,
				"engine": "dijkstra-one-to-many",
				"ranking": [
					{
						"driver_id": "018f9c00-3333-7abc-8def-0123456789ab",
						"eta_s": 142,
						"distance_m": 820,
						"location": { "lat": -23.5601, "lng": -46.6588 }
					}
				]
			}`,
			value: Matched{
				EventID: "018f3a2b-bbbb-7abc-8def-0123456789ab",
				TraceID: "req-abc123",
				RideID:  "018f3a2b-2222-7abc-8def-0123456789ab",
				Attempt: 1,
				Engine:  EngineDijkstra,
				Ranking: []Candidate{{
					DriverID:   "018f9c00-3333-7abc-8def-0123456789ab",
					ETASeconds: 142,
					DistanceM:  820,
					Location:   Point{Lat: -23.5601, Lng: -46.6588},
				}},
			},
			decode: decodeAs[Matched],
		},
		{
			name: "rides.match-failed",
			golden: `{
				"event_id": "018f3a2b-cccc-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
				"attempt": 1,
				"reason": "no_candidate"
			}`,
			value: MatchFailed{
				EventID: "018f3a2b-cccc-7abc-8def-0123456789ab",
				TraceID: "req-abc123",
				RideID:  "018f3a2b-2222-7abc-8def-0123456789ab",
				Attempt: 1,
				Reason:  ReasonNoCandidate,
			},
			decode: decodeAs[MatchFailed],
		},
		{
			name: "rides.match-failed none_reachable",
			golden: `{
				"event_id": "018f3a2b-cccc-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
				"attempt": 1,
				"reason": "none_reachable"
			}`,
			value: MatchFailed{
				EventID: "018f3a2b-cccc-7abc-8def-0123456789ab",
				TraceID: "req-abc123",
				RideID:  "018f3a2b-2222-7abc-8def-0123456789ab",
				Attempt: 1,
				Reason:  ReasonNoneReachable,
			},
			decode: decodeAs[MatchFailed],
		},
		{
			name: "rides.assigned",
			golden: `{
				"event_id": "018f3a2b-dddd-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
				"driver_id": "018f9c00-3333-7abc-8def-0123456789ab"
			}`,
			value: Assigned{
				EventID:  "018f3a2b-dddd-7abc-8def-0123456789ab",
				TraceID:  "req-abc123",
				RideID:   "018f3a2b-2222-7abc-8def-0123456789ab",
				DriverID: "018f9c00-3333-7abc-8def-0123456789ab",
			},
			decode: decodeAs[Assigned],
		},
		{
			name: "rides.released",
			golden: `{
				"event_id": "018f3a2b-dddd-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
				"driver_id": "018f9c00-3333-7abc-8def-0123456789ab"
			}`,
			value: Released{
				EventID:  "018f3a2b-dddd-7abc-8def-0123456789ab",
				TraceID:  "req-abc123",
				RideID:   "018f3a2b-2222-7abc-8def-0123456789ab",
				DriverID: "018f9c00-3333-7abc-8def-0123456789ab",
			},
			decode: decodeAs[Released],
		},
		{
			name: "drivers.presence",
			golden: `{
				"event_id": "018f3a2b-eeee-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"driver_id": "018f9c00-3333-7abc-8def-0123456789ab",
				"lost": { "ride_id": "018f3a2b-2222-7abc-8def-0123456789ab" }
			}`,
			value: Presence{
				EventID:  "018f3a2b-eeee-7abc-8def-0123456789ab",
				TraceID:  "req-abc123",
				DriverID: "018f9c00-3333-7abc-8def-0123456789ab",
				Lost:     Lost{RideID: "018f3a2b-2222-7abc-8def-0123456789ab"},
			},
			decode: decodeAs[Presence],
		},
		{
			name: "drivers.location",
			golden: `{
				"event_id": "018f3a2b-ffff-7abc-8def-0123456789ab",
				"trace_id": "req-abc123",
				"driver_id": "018f9c00-3333-7abc-8def-0123456789ab",
				"lat": -23.5601,
				"lng": -46.6588,
				"seq": 918342,
				"observed_at": "2026-09-30T18:00:00.812Z",
				"accuracy_m": 8.5
			}`,
			value: Location{
				EventID:    "018f3a2b-ffff-7abc-8def-0123456789ab",
				TraceID:    "req-abc123",
				DriverID:   "018f9c00-3333-7abc-8def-0123456789ab",
				Lat:        -23.5601,
				Lng:        -46.6588,
				Seq:        918342,
				ObservedAt: observed,
				AccuracyM:  8.5,
			},
			decode: decodeAs[Location],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.decode([]byte(tt.golden))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got, tt.value) {
				t.Fatalf("decode = %#v, want %#v", got, tt.value)
			}
			raw, err := Encode(got)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			sameShape(t, []byte(tt.golden), raw)
			again, err := tt.decode(raw)
			if err != nil {
				t.Fatalf("redecode: %v", err)
			}
			if !reflect.DeepEqual(again, got) {
				t.Fatalf("round trip = %#v, want %#v", again, got)
			}
		})
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	body := []byte(`{
		"event_id": "018f3a2b-aaaa-7abc-8def-0123456789ab",
		"trace_id": "req-abc123",
		"ride_id": "018f3a2b-2222-7abc-8def-0123456789ab",
		"attempt": 1,
		"pickup": { "lat": -23.5613, "lng": -46.6565, "alt": 1 },
		"vehicle_class": "standard",
		"exclude_driver_ids": []
	}`)
	if _, err := Decode[Requested](body); err == nil {
		t.Fatal("decode error = nil, want unknown field")
	}

	extra := []byte(`{"event_id":"e","trace_id":"t","ride_id":"r","driver_id":"d","note":"x"}`)
	if _, err := Decode[Assigned](extra); err == nil {
		t.Fatal("decode error = nil, want unknown field")
	}
}

func TestEncodeNilSlicesAsEmptyArrays(t *testing.T) {
	raw, err := Encode(Requested{})
	if err != nil {
		t.Fatalf("encode requested: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode requested: %v", err)
	}
	ids, ok := req["exclude_driver_ids"].([]any)
	if !ok || len(ids) != 0 {
		t.Fatalf("exclude_driver_ids = %#v, want empty array", req["exclude_driver_ids"])
	}

	raw, err = Encode(Matched{})
	if err != nil {
		t.Fatalf("encode matched: %v", err)
	}
	var matched map[string]any
	if err := json.Unmarshal(raw, &matched); err != nil {
		t.Fatalf("decode matched: %v", err)
	}
	ranking, ok := matched["ranking"].([]any)
	if !ok || len(ranking) != 0 {
		t.Fatalf("ranking = %#v, want empty array", matched["ranking"])
	}
}

func TestKey(t *testing.T) {
	tests := []struct {
		topic  string
		ride   string
		driver string
		want   string
		err    bool
	}{
		{topic: TopicRidesRequested, ride: "ride-1", want: "ride-1"},
		{topic: TopicRidesMatched, ride: "ride-1", want: "ride-1"},
		{topic: TopicRidesMatchFailed, ride: "ride-1", want: "ride-1"},
		{topic: TopicRidesAssigned, ride: "ride-1", want: "ride-1"},
		{topic: TopicRidesReleased, ride: "ride-1", want: "ride-1"},
		{topic: TopicDriversPresence, driver: "drv-1", want: "drv-1"},
		{topic: TopicDriversLocation, driver: "drv-1", want: "drv-1"},
		{topic: TopicRidesRequested, driver: "drv-1", err: true},
		{topic: TopicDriversLocation, ride: "ride-1", err: true},
		{topic: "rides.other", ride: "ride-1", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.topic+"/"+tt.ride+"/"+tt.driver, func(t *testing.T) {
			got, err := Key(tt.topic, tt.ride, tt.driver)
			if tt.err {
				if err == nil {
					t.Fatal("error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Key() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Key() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeTrailingJSON(t *testing.T) {
	_, err := Decode[Assigned]([]byte(`{"event_id":"e","trace_id":"t","ride_id":"r","driver_id":"d"}{"x":1}`))
	if err == nil || err.Error() != "trailing json" {
		t.Fatalf("error = %v, want trailing json", err)
	}
}

func decodeAs[T any](data []byte) (any, error) {
	v, err := Decode[T](data)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func sameShape(t *testing.T, want, got []byte) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("golden: %v", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("encoded: %v", err)
	}
	if err := shapeEqual(w, g); err != nil {
		t.Fatalf("shape: %v\ngolden %s\nencoded %s", err, want, got)
	}
}

func shapeEqual(want, got any) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("got %T, want object", got)
		}
		if len(w) != len(g) {
			return fmt.Errorf("got keys %d, want %d", len(g), len(w))
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return fmt.Errorf("missing %s", k)
			}
			if err := shapeEqual(wv, gv); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		return nil
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Errorf("got %T, want array", got)
		}
		if len(w) != len(g) {
			return fmt.Errorf("len %d, want %d", len(g), len(w))
		}
		for i := range w {
			if err := shapeEqual(w[i], g[i]); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return nil
	default:
		if reflect.TypeOf(want) != reflect.TypeOf(got) {
			return fmt.Errorf("got %T, want %T", got, want)
		}
		return nil
	}
}
