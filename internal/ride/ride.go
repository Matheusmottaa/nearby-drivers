package ride

import "time"

const (
	// StatusRequested is the state written by POST /rides, before the outbox publish.
	StatusRequested = "REQUESTED"
	// StatusMatching is the state after rides.requested is published, or after a rematch.
	StatusMatching = "MATCHING"
	// StatusAssigned is the successful terminal state.
	StatusAssigned = "DRIVER_ASSIGNED"
	// StatusCancelled is a terminal state. Cancelling it again is a no-op.
	StatusCancelled = "CANCELLED"
	// StatusNoDriver is the terminal state when matching or the attempt limit fails.
	StatusNoDriver = "NO_DRIVER_AVAILABLE"

	// DefaultMaxAttempts is used when RIDE_MAX_ATTEMPTS is unset. A lost driver counts.
	DefaultMaxAttempts = 3

	// PollAfterMS is the hint returned by POST /rides.
	PollAfterMS = 300

	// MaxWait is the longest a GET /rides/{id}?wait= holds an open ride.
	MaxWait = 3 * time.Second

	idempotencyTTL = 24 * time.Hour
	reservationTTL = 30 * time.Second
	pollEvery      = 25 * time.Millisecond
)

// Ride is the aggregate. TraceID is copied from the first rides.requested payload.
type Ride struct {
	ID                string
	PassengerID       string
	Status            string
	PickupLat         float64
	PickupLng         float64
	VehicleClass      string
	AssignedDriverID  string
	AssignedETAS      int
	AssignedDistanceM int
	ETAEngine         string
	ExcludedDriverIDs []string
	Attempt           int
	CancelReason      string
	Version           int
	TraceID           string
	RequestedAt       time.Time
	UpdatedAt         time.Time
}

// Pending reports whether a long poll should keep waiting.
func (r Ride) Pending() bool {
	return r.Status == StatusRequested || r.Status == StatusMatching
}

// Request is the body of POST /rides.
type Request struct {
	PassengerID  string
	PickupLat    float64
	PickupLng    float64
	VehicleClass string
	TraceID      string
}

// OutboxEvent is one row written in the same transaction as the ride.
type OutboxEvent struct {
	EventID string
	Subject string
	Payload []byte
}

// PendingPublish is an unpublished outbox row.
type PendingPublish struct {
	Subject   string
	RideID    string
	EventID   string
	Payload   []byte
	CreatedAt time.Time
}
