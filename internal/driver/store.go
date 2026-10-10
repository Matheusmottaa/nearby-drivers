package driver

import (
	"context"
	"time"
)

// AssignResult is the outcome of the conditional ON_TRIP update.
type AssignResult int

const (
	// AssignMissing means the driver id does not exist.
	AssignMissing AssignResult = iota
	// AssignOther means the driver is already reserved for a different ride.
	AssignOther
	// AssignSame means the driver is already ON_TRIP for this ride.
	AssignSame
	// AssignApplied means this call moved the driver to ON_TRIP.
	AssignApplied
)

// Driver is the durable registration. Position is not part of it.
type Driver struct {
	ID            string
	InternalID    uint32
	Name          string
	Phone         string
	VehicleClass  string
	Plate         string
	Status        string
	CurrentRideID string
	RegisteredAt  time.Time
}

// Registration is the body of POST /drivers.
type Registration struct {
	Name         string
	Phone        string
	VehicleClass string
	Plate        string
}

// Store is the driver schema. Implementations talk to Postgres.
type Store interface {
	Create(ctx context.Context, idempotencyKey string, in Registration) (Driver, bool, error)
	Get(ctx context.Context, id string) (Driver, error)
	UpdateStatus(ctx context.Context, id, status string) error
	Assign(ctx context.Context, driverID, rideID string) (AssignResult, error)
	Release(ctx context.Context, driverID, rideID string) (bool, error)
	List(ctx context.Context) ([]Driver, error)
}

// Position is the value stored at driver:{id}:last. Nearby does not read it.
type Position struct {
	Lat float64
	Lng float64
	Seq int64
	At  time.Time
}

// Cache stores the last fix. The only operation the service needs is Put:
// the spatial index never reads this key.
type Cache interface {
	Put(ctx context.Context, driverID string, pos Position) error
}
