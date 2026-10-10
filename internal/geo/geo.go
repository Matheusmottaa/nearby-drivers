// Package geo holds coordinates, their validation and the Haversine distance.
package geo

import (
	"errors"
	"fmt"
	"math"
)

// EarthRadiusM is the mean Earth radius in meters.
const EarthRadiusM = 6371008.8

// ErrInvalidCoordinate means a latitude or longitude is not finite or is out of range.
var ErrInvalidCoordinate = errors.New("invalid coordinate")

// Point is a WGS84 coordinate in degrees.
type Point struct {
	Lat float64
	Lng float64
}

// Validate rejects NaN, Inf, latitudes outside [-90, 90] and longitudes outside [-180, 180].
func (p Point) Validate() error {
	if math.IsNaN(p.Lat) || math.IsInf(p.Lat, 0) || p.Lat < -90 || p.Lat > 90 {
		return fmt.Errorf("lat %v: %w", p.Lat, ErrInvalidCoordinate)
	}
	if math.IsNaN(p.Lng) || math.IsInf(p.Lng, 0) || p.Lng < -180 || p.Lng > 180 {
		return fmt.Errorf("lng %v: %w", p.Lng, ErrInvalidCoordinate)
	}
	return nil
}

// Distance is the great-circle distance between a and b in meters.
func Distance(a, b Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dLat := lat2 - lat1
	dLng := (b.Lng - a.Lng) * math.Pi / 180

	sinLat := math.Sin(dLat / 2)
	sinLng := math.Sin(dLng / 2)
	h := sinLat*sinLat + math.Cos(lat1)*math.Cos(lat2)*sinLng*sinLng
	return 2 * EarthRadiusM * math.Asin(math.Sqrt(min(1, h)))
}
