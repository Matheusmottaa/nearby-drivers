// Package event is the Kafka payload shared by the seven topics.
// rides.* is keyed by ride_id. drivers.* is keyed by driver_id.
// event_id stays the same when the outbox publishes the row again.
package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	TopicRidesRequested   = "rides.requested"
	TopicRidesMatched     = "rides.matched"
	TopicRidesMatchFailed = "rides.match-failed"
	TopicRidesAssigned    = "rides.assigned"
	TopicRidesReleased    = "rides.released"
	TopicDriversPresence  = "drivers.presence"
	TopicDriversLocation  = "drivers.location"

	EngineDijkstra  = "dijkstra-one-to-many"
	EngineHaversine = "haversine-fallback"

	ReasonNoCandidate   = "no_candidate"
	ReasonNoneReachable = "none_reachable"
)

// Point is a WGS84 coordinate in an event body.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Requested is the body of rides.requested.
type Requested struct {
	EventID          string   `json:"event_id"`
	TraceID          string   `json:"trace_id"`
	RideID           string   `json:"ride_id"`
	Attempt          int      `json:"attempt"`
	Pickup           Point    `json:"pickup"`
	VehicleClass     string   `json:"vehicle_class"`
	ExcludeDriverIDs []string `json:"exclude_driver_ids"`
}

// MarshalJSON encodes an absent exclusion list as an empty array.
func (e Requested) MarshalJSON() ([]byte, error) {
	if e.ExcludeDriverIDs == nil {
		e.ExcludeDriverIDs = []string{}
	}
	type alias Requested
	return json.Marshal(alias(e))
}

// Candidate is one driver in a rides.matched ranking.
type Candidate struct {
	DriverID   string `json:"driver_id"`
	ETASeconds int    `json:"eta_s"`
	DistanceM  int    `json:"distance_m"`
	Location   Point  `json:"location"`
}

// Matched is the body of rides.matched. The ranking is the full list.
type Matched struct {
	EventID string      `json:"event_id"`
	TraceID string      `json:"trace_id"`
	RideID  string      `json:"ride_id"`
	Attempt int         `json:"attempt"`
	Engine  string      `json:"engine"`
	Ranking []Candidate `json:"ranking"`
}

// MarshalJSON encodes an absent ranking as an empty array.
func (e Matched) MarshalJSON() ([]byte, error) {
	if e.Ranking == nil {
		e.Ranking = []Candidate{}
	}
	type alias Matched
	return json.Marshal(alias(e))
}

// MatchFailed is the body of rides.match-failed.
// Reason is no_candidate or none_reachable.
type MatchFailed struct {
	EventID string `json:"event_id"`
	TraceID string `json:"trace_id"`
	RideID  string `json:"ride_id"`
	Attempt int    `json:"attempt"`
	Reason  string `json:"reason"`
}

// Assigned is the body of rides.assigned.
type Assigned struct {
	EventID  string `json:"event_id"`
	TraceID  string `json:"trace_id"`
	RideID   string `json:"ride_id"`
	DriverID string `json:"driver_id"`
}

// Released is the body of rides.released.
type Released struct {
	EventID  string `json:"event_id"`
	TraceID  string `json:"trace_id"`
	RideID   string `json:"ride_id"`
	DriverID string `json:"driver_id"`
}

// Lost is the reservation whose driver stopped sending heartbeats.
type Lost struct {
	RideID string `json:"ride_id"`
}

// Presence is the body of drivers.presence. The lost object carries the
// ride_id of the current reservation.
type Presence struct {
	EventID  string `json:"event_id"`
	TraceID  string `json:"trace_id"`
	DriverID string `json:"driver_id"`
	Lost     Lost   `json:"lost"`
}

// Location is the body of drivers.location: the PUT /location body plus driver_id.
type Location struct {
	EventID    string    `json:"event_id"`
	TraceID    string    `json:"trace_id"`
	DriverID   string    `json:"driver_id"`
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	Seq        int64     `json:"seq"`
	ObservedAt time.Time `json:"observed_at"`
	AccuracyM  float64   `json:"accuracy_m"`
}

// Key is the Kafka record key for topic. rides.* uses rideID; drivers.* uses driverID.
func Key(topic, rideID, driverID string) (string, error) {
	switch topic {
	case TopicRidesRequested, TopicRidesMatched, TopicRidesMatchFailed, TopicRidesAssigned, TopicRidesReleased:
		if rideID == "" {
			return "", errors.New("ride_id is required")
		}
		return rideID, nil
	case TopicDriversPresence, TopicDriversLocation:
		if driverID == "" {
			return "", errors.New("driver_id is required")
		}
		return driverID, nil
	default:
		return "", fmt.Errorf("unknown topic %s", topic)
	}
}

// Encode serializes an event. Slice fields that are part of the contract encode
// as empty arrays when they are nil.
func Encode(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	return raw, nil
}

// Decode parses an event and rejects object keys that are not in the contract.
func Decode[T any](data []byte) (T, error) {
	var zero T
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&zero); err != nil {
		return zero, fmt.Errorf("decode event: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return zero, errors.New("trailing json")
	}
	return zero, nil
}
