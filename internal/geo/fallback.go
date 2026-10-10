package geo

import (
	"errors"
	"fmt"
	"math"
)

// EngineHaversine names estimates computed in a straight line.
const EngineHaversine = "haversine-fallback"

// ErrInvalidSpeed means the fallback speed is not a positive finite number.
var ErrInvalidSpeed = errors.New("invalid speed")

// Estimate is a travel time and distance between two points.
type Estimate struct {
	DurationS int
	DistanceM int
	Engine    string
}

// Fallback estimates the trip from -> to as the straight-line distance at speedMPS.
func Fallback(from, to Point, speedMPS float64) (Estimate, error) {
	if math.IsNaN(speedMPS) || math.IsInf(speedMPS, 0) || speedMPS <= 0 {
		return Estimate{}, fmt.Errorf("speed %v: %w", speedMPS, ErrInvalidSpeed)
	}
	if err := from.Validate(); err != nil {
		return Estimate{}, fmt.Errorf("from: %w", err)
	}
	if err := to.Validate(); err != nil {
		return Estimate{}, fmt.Errorf("to: %w", err)
	}
	d := Distance(from, to)
	return Estimate{
		DurationS: int(math.Round(d / speedMPS)),
		DistanceM: int(math.Round(d)),
		Engine:    EngineHaversine,
	}, nil
}
