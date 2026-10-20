package graph

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func TestETA(t *testing.T) {
	g := fixtureGraph(t)
	pickup := nodePoint(t, g, 0)
	node := func(id int) geo.Point { return nodePoint(t, g, id) }

	tests := []struct {
		name        string
		origins     []geo.Point
		radius      float64
		want        []Estimate
		wantVisited int
	}{
		{
			name:    "corridor",
			origins: []geo.Point{node(1)},
			radius:  15000,
			want:    []Estimate{{OriginIndex: 0, DurationS: 142, DistanceM: 820, Reachable: true}},
		},
		{
			name:    "alley is unreachable",
			origins: []geo.Point{node(2), node(3)},
			radius:  15000,
			want:    []Estimate{{OriginIndex: 0}, {OriginIndex: 1}},
		},
		{
			name:    "time beats meters",
			origins: []geo.Point{node(4)},
			radius:  15000,
			want:    []Estimate{{OriginIndex: 0, DurationS: 20, DistanceM: 1000, Reachable: true}},
		},
		{
			name:    "pickup itself",
			origins: []geo.Point{pickup},
			radius:  0,
			want:    []Estimate{{OriginIndex: 0, Reachable: true}},
		},
		{
			name:    "small radius cuts the corridor",
			origins: []geo.Point{node(1)},
			radius:  819,
			want:    []Estimate{{OriginIndex: 0}},
		},
		{
			name:    "radius prunes the fast path and keeps the short one",
			origins: []geo.Point{node(4), node(6)},
			radius:  499,
			want: []Estimate{
				{OriginIndex: 0, DurationS: 160, DistanceM: 200, Reachable: true},
				{OriginIndex: 1},
			},
		},
		{
			name:    "repeated origins",
			origins: []geo.Point{node(1), node(4), node(1)},
			radius:  15000,
			want: []Estimate{
				{OriginIndex: 0, DurationS: 142, DistanceM: 820, Reachable: true},
				{OriginIndex: 1, DurationS: 20, DistanceM: 1000, Reachable: true},
				{OriginIndex: 2, DurationS: 142, DistanceM: 820, Reachable: true},
			},
		},
		{
			name:        "stops once origins are settled",
			origins:     []geo.Point{node(6)},
			radius:      15000,
			want:        []Estimate{{OriginIndex: 0, DurationS: 10, DistanceM: 500, Reachable: true}},
			wantVisited: 2,
		},
		{
			name:    "every node",
			origins: []geo.Point{node(0), node(1), node(2), node(3), node(4), node(5), node(6)},
			radius:  math.Inf(1),
			want: []Estimate{
				{OriginIndex: 0, Reachable: true},
				{OriginIndex: 1, DurationS: 142, DistanceM: 820, Reachable: true},
				{OriginIndex: 2},
				{OriginIndex: 3},
				{OriginIndex: 4, DurationS: 20, DistanceM: 1000, Reachable: true},
				{OriginIndex: 5, DurationS: 80, DistanceM: 100, Reachable: true},
				{OriginIndex: 6, DurationS: 10, DistanceM: 500, Reachable: true},
			},
			wantVisited: 17,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewPool(1, g.NodeCount())
			got, err := g.ETA(t.Context(), pool, pickup, tt.origins, tt.radius)
			if err != nil {
				t.Fatalf("ETA() = %v", err)
			}
			if !slices.Equal(got.Estimates, tt.want) {
				t.Fatalf("estimates = %+v, want %+v", got.Estimates, tt.want)
			}
			if got.NodesVisited <= 0 {
				t.Errorf("nodes visited = %d, want > 0", got.NodesVisited)
			}
			if tt.wantVisited > 0 && got.NodesVisited != tt.wantVisited {
				t.Errorf("nodes visited = %d, want %d", got.NodesVisited, tt.wantVisited)
			}
			if pool.InUse() != 0 {
				t.Errorf("InUse() = %d after search, want 0", pool.InUse())
			}
		})
	}
}

func TestETAEmptyOrigins(t *testing.T) {
	g := fixtureGraph(t)
	got, err := g.ETA(t.Context(), NewPool(1, g.NodeCount()), nodePoint(t, g, 0), nil, 15000)
	if err != nil {
		t.Fatalf("ETA() = %v", err)
	}
	if got.Estimates == nil || len(got.Estimates) != 0 || got.NodesVisited != 0 {
		t.Fatalf("ETA() = %+v, want empty estimates", got)
	}
}

func TestETARejects(t *testing.T) {
	g := fixtureGraph(t)
	pickup := nodePoint(t, g, 0)
	tests := []struct {
		name    string
		pool    *Pool
		dest    geo.Point
		origins []geo.Point
		radius  float64
		wantErr error
	}{
		{name: "invalid destination", dest: geo.Point{Lat: 91}, origins: []geo.Point{pickup}, radius: 100, wantErr: geo.ErrInvalidCoordinate},
		{name: "invalid origin", dest: pickup, origins: []geo.Point{pickup, {Lng: math.NaN()}}, radius: 100, wantErr: geo.ErrInvalidCoordinate},
		{name: "negative radius", dest: pickup, origins: []geo.Point{pickup}, radius: -1, wantErr: ErrInvalidRadius},
		{name: "NaN radius", dest: pickup, origins: []geo.Point{pickup}, radius: math.NaN(), wantErr: ErrInvalidRadius},
		{name: "pool too small", pool: NewPool(1, 3), dest: pickup, origins: []geo.Point{pickup}, radius: 100, wantErr: ErrPoolTooSmall},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := tt.pool
			if pool == nil {
				pool = NewPool(1, g.NodeCount())
			}
			_, err := g.ETA(t.Context(), pool, tt.dest, tt.origins, tt.radius)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ETA() = %v, want %v", err, tt.wantErr)
			}
			if pool.InUse() != 0 {
				t.Errorf("InUse() = %d, want 0", pool.InUse())
			}
		})
	}
}

func TestETACanceledReturnsBuffer(t *testing.T) {
	g := fixtureGraph(t)
	pool := NewPool(1, g.NodeCount())
	pickup := nodePoint(t, g, 0)
	origins := []geo.Point{nodePoint(t, g, 1)}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := g.ETA(ctx, pool, pickup, origins, 15000)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ETA(canceled) = %v, want context.Canceled", err)
	}
	if pool.InUse() != 0 {
		t.Fatalf("InUse() = %d after cancel, want 0", pool.InUse())
	}

	got, err := g.ETA(t.Context(), pool, pickup, origins, 15000)
	if err != nil {
		t.Fatalf("ETA() after cancel = %v", err)
	}
	if want := (Estimate{DurationS: 142, DistanceM: 820, Reachable: true}); got.Estimates[0] != want {
		t.Fatalf("estimate = %+v, want %+v", got.Estimates[0], want)
	}
}

func TestETAGenerationWrap(t *testing.T) {
	g := fixtureGraph(t)
	pool := NewPool(1, g.NodeCount())
	pickup := nodePoint(t, g, 0)
	corridor := []geo.Point{nodePoint(t, g, 1)}

	if _, err := g.ETA(t.Context(), pool, pickup, corridor, 15000); err != nil {
		t.Fatalf("ETA() = %v", err)
	}
	b, err := pool.acquire()
	if err != nil {
		t.Fatalf("acquire() = %v", err)
	}
	if b.gen != 1 {
		t.Fatalf("gen = %d after one search, want 1", b.gen)
	}
	b.gen = math.MaxUint32 - 1
	pool.release(b)

	want := []Estimate{{OriginIndex: 0}}
	for i := range 3 {
		got, err := g.ETA(t.Context(), pool, pickup, corridor, 0)
		if err != nil {
			t.Fatalf("search %d: ETA() = %v", i, err)
		}
		if !slices.Equal(got.Estimates, want) {
			t.Fatalf("search %d: estimates = %+v, want %+v", i, got.Estimates, want)
		}
	}
}
