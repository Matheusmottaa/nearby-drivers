package geo

import (
	"errors"
	"math"
	"testing"
)

var (
	pickup   = Point{Lat: -23.5613, Lng: -46.6565}
	corridor = Point{Lat: -23.5601, Lng: -46.6588}
)

func TestDistance(t *testing.T) {
	tests := []struct {
		name  string
		a, b  Point
		wantM float64
		tolM  float64
	}{
		{name: "same point", a: pickup, b: pickup, wantM: 0, tolM: 1e-9},
		{name: "fixture pickup to corridor", a: pickup, b: corridor, wantM: 269, tolM: 1},
		{name: "symmetric", a: corridor, b: pickup, wantM: 269, tolM: 1},
		{name: "sao paulo to rio", a: Point{Lat: -23.5505, Lng: -46.6333}, b: Point{Lat: -22.9068, Lng: -43.1729}, wantM: 360_700, tolM: 500},
		{name: "antipodes", a: Point{Lat: 0, Lng: 0}, b: Point{Lat: 0, Lng: 180}, wantM: math.Pi * EarthRadiusM, tolM: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Distance(tt.a, tt.b)
			if math.Abs(got-tt.wantM) > tt.tolM {
				t.Errorf("Distance() = %.3f m, want %.3f ± %.3f m", got, tt.wantM, tt.tolM)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		p       Point
		wantErr bool
	}{
		{name: "fixture", p: pickup},
		{name: "north pole", p: Point{Lat: 90, Lng: 0}},
		{name: "south pole", p: Point{Lat: -90, Lng: 0}},
		{name: "east limit", p: Point{Lat: 0, Lng: 180}},
		{name: "west limit", p: Point{Lat: 0, Lng: -180}},
		{name: "lat 91", p: Point{Lat: 91, Lng: 0}, wantErr: true},
		{name: "lat -91", p: Point{Lat: -91, Lng: 0}, wantErr: true},
		{name: "lng 181", p: Point{Lat: 0, Lng: 181}, wantErr: true},
		{name: "lng -181", p: Point{Lat: 0, Lng: -181}, wantErr: true},
		{name: "lat NaN", p: Point{Lat: math.NaN(), Lng: 0}, wantErr: true},
		{name: "lng NaN", p: Point{Lat: 0, Lng: math.NaN()}, wantErr: true},
		{name: "lat Inf", p: Point{Lat: math.Inf(1), Lng: 0}, wantErr: true},
		{name: "lng -Inf", p: Point{Lat: 0, Lng: math.Inf(-1)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.p.Validate()
			if tt.wantErr && !errors.Is(err, ErrInvalidCoordinate) {
				t.Fatalf("Validate() = %v, want ErrInvalidCoordinate", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestFallback(t *testing.T) {
	tests := []struct {
		name     string
		from, to Point
		speed    float64
		want     Estimate
		wantErr  error
	}{
		{name: "fixture at 8.33 m/s", from: corridor, to: pickup, speed: 8.33, want: Estimate{DurationS: 32, DistanceM: 270, Engine: EngineHaversine}},
		{name: "same point", from: pickup, to: pickup, speed: 8.33, want: Estimate{Engine: EngineHaversine}},
		{name: "zero speed", from: corridor, to: pickup, speed: 0, wantErr: ErrInvalidSpeed},
		{name: "negative speed", from: corridor, to: pickup, speed: -1, wantErr: ErrInvalidSpeed},
		{name: "NaN speed", from: corridor, to: pickup, speed: math.NaN(), wantErr: ErrInvalidSpeed},
		{name: "invalid from", from: Point{Lat: 91}, to: pickup, speed: 8.33, wantErr: ErrInvalidCoordinate},
		{name: "invalid to", from: corridor, to: Point{Lng: 181}, speed: 8.33, wantErr: ErrInvalidCoordinate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Fallback(tt.from, tt.to, tt.speed)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Fallback() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Fallback() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
