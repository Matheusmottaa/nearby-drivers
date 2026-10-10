package driver

import "errors"

var (
	// ErrNotFound means the driver id is not in the registry.
	ErrNotFound = errors.New("driver not found")
	// ErrPlateTaken means another driver already uses the plate.
	ErrPlateTaken = errors.New("plate already registered")
	// ErrIllegalStatus means the status transition is not allowed.
	ErrIllegalStatus = errors.New("illegal status transition")
	// ErrIndexNotReady means the first KD-Tree snapshot has not been published.
	ErrIndexNotReady = errors.New("spatial index is not ready")
	// ErrInvalid means a request field is missing or out of range.
	ErrInvalid = errors.New("invalid")
	// ErrIdempotencyKey means POST /drivers arrived without Idempotency-Key.
	ErrIdempotencyKey = errors.New("Idempotency-Key is required")
)

type inputError struct {
	msg string
}

func (e inputError) Error() string { return e.msg }

func (e inputError) Unwrap() error { return ErrInvalid }

func invalidInput(msg string) error { return inputError{msg: msg} }
