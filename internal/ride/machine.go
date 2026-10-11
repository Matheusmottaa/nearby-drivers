package ride

import (
	"fmt"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

// Plan is one transition of the aggregate. Ignore means the delivery is acked
// and the row stays as it is. Reject is a cancel the state machine refuses.
type Plan struct {
	Ignore        bool
	Reject        bool
	Next          Ride
	Events        []OutboxEvent
	AssignDriver  string
	ReleaseDriver string
	Exhausted     bool
	Rematch       bool
}

func (in Request) validate() error {
	if !validUUID(in.PassengerID) {
		return invalidInput("passenger_id must be a uuid")
	}
	if err := (geo.Point{Lat: in.PickupLat, Lng: in.PickupLng}).Validate(); err != nil {
		return err
	}
	if in.VehicleClass == "" {
		return invalidInput("vehicle_class is required")
	}
	return nil
}

func assignable(status string) bool {
	return status == StatusRequested || status == StatusMatching
}

func clearAssignment(r Ride) Ride {
	r.AssignedDriverID = ""
	r.AssignedETAS = 0
	r.AssignedDistanceM = 0
	r.ETAEngine = ""
	return r
}

func copyIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// planPublish is the REQUESTED to MATCHING arrow. The store applies it when
// the rides.requested row is marked published. A ride already past REQUESTED
// stays put, so a replay does not bump version again.
func planPublish(r Ride) (Ride, bool) {
	if r.Status != StatusRequested {
		return r, false
	}
	r.Status = StatusMatching
	r.Version++
	return r, true
}

func planMatch(r Ride, m event.Matched, blocked map[string]struct{}) (Plan, error) {
	if m.Attempt != r.Attempt || !assignable(r.Status) {
		return Plan{Ignore: true}, nil
	}
	if m.Engine != event.EngineDijkstra && m.Engine != event.EngineHaversine {
		return Plan{Ignore: true}, nil
	}
	for _, c := range m.Ranking {
		if _, skip := blocked[c.DriverID]; skip || !validUUID(c.DriverID) {
			continue
		}
		if c.ETASeconds < 0 || c.DistanceM < 0 {
			continue
		}
		next := r
		next.Status = StatusAssigned
		next.AssignedDriverID = c.DriverID
		next.AssignedETAS = c.ETASeconds
		next.AssignedDistanceM = c.DistanceM
		next.ETAEngine = m.Engine
		next.Version++
		ev, err := newAssigned(next)
		if err != nil {
			return Plan{}, err
		}
		return Plan{Next: next, Events: []OutboxEvent{ev}, AssignDriver: c.DriverID}, nil
	}
	next := clearAssignment(r)
	next.Status = StatusNoDriver
	next.Version++
	return Plan{Next: next, Exhausted: true}, nil
}

func planMatchFailed(r Ride, ev event.MatchFailed) Plan {
	if ev.Attempt != r.Attempt || !assignable(r.Status) {
		return Plan{Ignore: true}
	}
	next := clearAssignment(r)
	next.Status = StatusNoDriver
	next.Version++
	return Plan{Next: next}
}

func planLost(r Ride, driverID, lostRideID string, maxAttempts int) (Plan, error) {
	if r.Status != StatusAssigned || r.AssignedDriverID != driverID || r.ID != lostRideID {
		return Plan{Ignore: true}, nil
	}
	released, err := newReleased(r)
	if err != nil {
		return Plan{}, err
	}
	driver := r.AssignedDriverID
	next := clearAssignment(r)
	next.ExcludedDriverIDs = append(copyIDs(r.ExcludedDriverIDs), driver)
	next.Version++
	events := []OutboxEvent{released}
	if r.Attempt+1 <= maxAttempts {
		next.Attempt = r.Attempt + 1
		next.Status = StatusMatching
		requested, err := newRequested(next)
		if err != nil {
			return Plan{}, err
		}
		events = append(events, requested)
		return Plan{Next: next, Events: events, ReleaseDriver: driver, Rematch: true}, nil
	}
	next.Status = StatusNoDriver
	return Plan{Next: next, Events: events, ReleaseDriver: driver}, nil
}

func planCancel(r Ride, reason string) (Plan, error) {
	if r.Status == StatusCancelled {
		return Plan{Ignore: true, Next: r}, nil
	}
	if r.Status != StatusRequested && r.Status != StatusMatching && r.Status != StatusAssigned {
		return Plan{Reject: true}, nil
	}
	var events []OutboxEvent
	var release string
	if r.Status == StatusAssigned {
		ev, err := newReleased(r)
		if err != nil {
			return Plan{}, err
		}
		events = append(events, ev)
		release = r.AssignedDriverID
	}
	next := clearAssignment(r)
	next.Status = StatusCancelled
	next.CancelReason = reason
	next.Version++
	return Plan{Next: next, Events: events, ReleaseDriver: release}, nil
}

func newRequested(r Ride) (OutboxEvent, error) {
	id, err := newID()
	if err != nil {
		return OutboxEvent{}, err
	}
	raw, err := event.Encode(event.Requested{
		EventID:          id,
		TraceID:          r.TraceID,
		RideID:           r.ID,
		Attempt:          r.Attempt,
		Pickup:           event.Point{Lat: r.PickupLat, Lng: r.PickupLng},
		VehicleClass:     r.VehicleClass,
		ExcludeDriverIDs: copyIDs(r.ExcludedDriverIDs),
	})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{EventID: id, Subject: event.TopicRidesRequested, Payload: raw}, nil
}

func newAssigned(r Ride) (OutboxEvent, error) {
	id, err := newID()
	if err != nil {
		return OutboxEvent{}, err
	}
	raw, err := event.Encode(event.Assigned{
		EventID:  id,
		TraceID:  r.TraceID,
		RideID:   r.ID,
		DriverID: r.AssignedDriverID,
	})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{EventID: id, Subject: event.TopicRidesAssigned, Payload: raw}, nil
}

func newReleased(r Ride) (OutboxEvent, error) {
	id, err := newID()
	if err != nil {
		return OutboxEvent{}, err
	}
	raw, err := event.Encode(event.Released{
		EventID:  id,
		TraceID:  r.TraceID,
		RideID:   r.ID,
		DriverID: r.AssignedDriverID,
	})
	if err != nil {
		return OutboxEvent{}, err
	}
	return OutboxEvent{EventID: id, Subject: event.TopicRidesReleased, Payload: raw}, nil
}

func rideOf(in Request, id string, now timeNow) (Ride, error) {
	return Ride{
		ID:                id,
		PassengerID:       in.PassengerID,
		Status:            StatusRequested,
		PickupLat:         in.PickupLat,
		PickupLng:         in.PickupLng,
		VehicleClass:      in.VehicleClass,
		ExcludedDriverIDs: []string{},
		Attempt:           1,
		Version:           1,
		TraceID:           in.TraceID,
		RequestedAt:       now(),
		UpdatedAt:         now(),
	}, nil
}

type timeNow func() time.Time

func checkAttempt(max int) int {
	if max < 1 {
		return DefaultMaxAttempts
	}
	return max
}

func describe(from, to string) string {
	return fmt.Sprintf("%s->%s", from, to)
}
