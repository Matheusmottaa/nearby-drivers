package ride

import "errors"

var (
	// ErrNotFound means the ride id is not in the store.
	ErrNotFound = errors.New("ride not found")
	// ErrIdempotencyKey means POST /rides arrived without Idempotency-Key.
	ErrIdempotencyKey = errors.New("Idempotency-Key is required")
	// ErrIdempotencyConflict means the same key was reused with a different body.
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different body")
	// ErrInvalid means a request field is missing or out of range.
	ErrInvalid = errors.New("invalid")
	// ErrVersionConflict means another writer committed a transition first.
	ErrVersionConflict = errors.New("version conflict")
	// ErrDriverTaken means the driver is already on a DRIVER_ASSIGNED ride.
	ErrDriverTaken = errors.New("driver already assigned")
	// ErrNotCancellable means the ride is in a terminal state other than CANCELLED.
	ErrNotCancellable = errors.New("ride cannot be cancelled")
)

type inputError struct {
	msg string
}

func (e inputError) Error() string { return e.msg }

func (e inputError) Unwrap() error { return ErrInvalid }

func invalidInput(msg string) error { return inputError{msg: msg} }
