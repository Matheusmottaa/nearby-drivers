package driver

const (
	StatusPending   = "PENDING"
	StatusOffline   = "OFFLINE"
	StatusAvailable = "AVAILABLE"
	StatusOnTrip    = "ON_TRIP"
	StatusSuspended = "SUSPENDED"
)

// CanTransit reports whether PATCH /status may move a driver from `from` to `to`.
// ON_TRIP is owned by rides.assigned and rides.released. PENDING is only the
// status of a new registration. The same status is a no-op.
func CanTransit(from, to string) bool {
	if from == to {
		return from != StatusOnTrip
	}
	if from == StatusOnTrip || to == StatusOnTrip || to == StatusPending {
		return false
	}
	switch from {
	case StatusPending, StatusOffline, StatusAvailable, StatusSuspended:
	default:
		return false
	}
	switch to {
	case StatusOffline, StatusAvailable, StatusSuspended:
		return true
	default:
		return false
	}
}
