package kdtree

import (
	"cmp"
	"errors"
	"math"
	"slices"
	"sync"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

var pickup = geo.Point{Lat: -23.5613, Lng: -46.6565}

// north returns a point m meters north of pickup.
func north(m float64) geo.Point {
	return geo.Point{Lat: pickup.Lat + m/metersPerDegree, Lng: pickup.Lng}
}

// south returns a point m meters south of pickup.
func south(m float64) geo.Point {
	return geo.Point{Lat: pickup.Lat - m/metersPerDegree, Lng: pickup.Lng}
}

func driver(id uint32, p geo.Point, class string) Entry {
	return Entry{ID: id, Point: p, Class: class, Available: true}
}

func ids(rs []Result) []uint32 {
	out := make([]uint32, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestNearby(t *testing.T) {
	entries := []Entry{
		driver(1, north(300), "standard"),
		driver(2, north(100), "standard"),
		driver(3, north(200), "comfort"),
		driver(4, north(5000), "standard"),
		driver(7, north(150), "standard"),
		driver(9, south(400), "standard"),
		driver(6, north(150), "standard"),
		{ID: 8, Point: north(50), Class: "standard"},
	}
	tests := []struct {
		name    string
		entries []Entry
		radius  float64
		k       int
		class   string
		want    []uint32
	}{
		{name: "k inside radius, sorted", entries: entries, radius: 1000, k: 10, want: []uint32{2, 6, 7, 3, 1, 9}},
		{name: "cut at k", entries: entries, radius: 1000, k: 2, want: []uint32{2, 6}},
		{name: "tie resolved by id", entries: entries, radius: 1000, k: 3, want: []uint32{2, 6, 7}},
		{name: "class filter", entries: entries, radius: 1000, k: 10, class: "comfort", want: []uint32{3}},
		{name: "wide radius", entries: entries, radius: 6000, k: 10, class: "standard", want: []uint32{2, 6, 7, 1, 9, 4}},
		{name: "small radius is empty", entries: entries, radius: 10, k: 10, want: []uint32{}},
		{name: "no drivers is empty", entries: nil, radius: 1000, k: 10, want: []uint32{}},
		{name: "zero radius on top of a driver", entries: []Entry{driver(5, pickup, "")}, radius: 0, k: 1, want: []uint32{5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := NewIndex()
			x.Publish(tt.entries, 0)
			got, err := x.Nearby(pickup, tt.radius, tt.k, tt.class)
			if err != nil {
				t.Fatalf("Nearby() = %v", err)
			}
			if !slices.Equal(ids(got), tt.want) {
				t.Fatalf("Nearby() = %v, want %v", ids(got), tt.want)
			}
			for i, r := range got {
				if want := geo.Distance(pickup, r.Point); r.StraightLineM != want {
					t.Errorf("result %d straight_line_m = %v, want %v", i, r.StraightLineM, want)
				}
			}
		})
	}
}

func TestNearbyErrors(t *testing.T) {
	ready := NewIndex()
	ready.Publish(nil, 0)
	tests := []struct {
		name    string
		x       *Index
		p       geo.Point
		radius  float64
		k       int
		wantErr error
	}{
		{name: "not published", x: NewIndex(), p: pickup, radius: 100, k: 1, wantErr: ErrNotReady},
		{name: "invalid coordinate", x: ready, p: geo.Point{Lat: 91}, radius: 100, k: 1, wantErr: geo.ErrInvalidCoordinate},
		{name: "NaN coordinate", x: ready, p: geo.Point{Lng: math.NaN()}, radius: 100, k: 1, wantErr: geo.ErrInvalidCoordinate},
		{name: "negative radius", x: ready, p: pickup, radius: -1, k: 1, wantErr: ErrInvalidQuery},
		{name: "NaN radius", x: ready, p: pickup, radius: math.NaN(), k: 1, wantErr: ErrInvalidQuery},
		{name: "zero k", x: ready, p: pickup, radius: 100, k: 0, wantErr: ErrInvalidQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.x.Nearby(tt.p, tt.radius, tt.k, "")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Nearby() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeltaCorrectsSnapshot(t *testing.T) {
	x := NewIndex()
	x.Publish([]Entry{
		driver(1, north(100), "standard"),
		driver(2, north(200), "standard"),
		driver(3, north(300), "standard"),
	}, 0)

	x.Apply(driver(1, north(5000), "standard"))
	x.Apply(Entry{ID: 2, Point: north(200), Class: "standard"})
	x.Apply(driver(4, north(50), "standard"))
	x.Apply(driver(5, north(60), "comfort"))

	got, err := x.Nearby(pickup, 1000, 10, "standard")
	if err != nil {
		t.Fatalf("Nearby() = %v", err)
	}
	if want := []uint32{4, 3}; !slices.Equal(ids(got), want) {
		t.Fatalf("Nearby() = %v, want %v", ids(got), want)
	}

	x.Apply(driver(1, north(10), "standard"))
	got, err = x.Nearby(pickup, 1000, 10, "standard")
	if err != nil {
		t.Fatalf("Nearby() = %v", err)
	}
	if want := []uint32{1, 4, 3}; !slices.Equal(ids(got), want) {
		t.Fatalf("Nearby() after second move = %v, want %v", ids(got), want)
	}
}

func TestPublishDropsIncorporatedDelta(t *testing.T) {
	x := NewIndex()
	x.Publish(nil, 0)
	v1 := x.Apply(driver(1, north(100), "standard"))
	v2 := x.Apply(driver(2, north(200), "standard"))
	if v2 <= v1 || x.Version() != v2 {
		t.Fatalf("versions = %d, %d, Version() = %d", v1, v2, x.Version())
	}

	x.Publish([]Entry{driver(1, north(100), "standard")}, v1)

	x.mu.RLock()
	_, has1 := x.delta[1]
	_, has2 := x.delta[2]
	x.mu.RUnlock()
	if has1 || !has2 {
		t.Fatalf("delta has 1 = %v, has 2 = %v, want false and true", has1, has2)
	}

	got, err := x.Nearby(pickup, 1000, 10, "")
	if err != nil {
		t.Fatalf("Nearby() = %v", err)
	}
	if want := []uint32{1, 2}; !slices.Equal(ids(got), want) {
		t.Fatalf("Nearby() = %v, want %v", ids(got), want)
	}
}

func TestPublishLastEntryWins(t *testing.T) {
	x := NewIndex()
	x.Publish([]Entry{
		driver(1, north(100), "standard"),
		{ID: 1, Point: north(100), Class: "standard"},
		driver(2, north(900), "standard"),
		driver(2, north(50), "standard"),
	}, 0)
	got, err := x.Nearby(pickup, 1000, 10, "")
	if err != nil {
		t.Fatalf("Nearby() = %v", err)
	}
	if want := []uint32{2}; !slices.Equal(ids(got), want) {
		t.Fatalf("Nearby() = %v, want %v", ids(got), want)
	}
	if got[0].StraightLineM > 60 {
		t.Fatalf("driver 2 at %v m, want the last position", got[0].StraightLineM)
	}
}

func TestSnapshotSwappedMidRead(t *testing.T) {
	x := NewIndex()
	x.Publish([]Entry{driver(1, north(100), "standard"), driver(2, north(200), "standard")}, 0)
	old := x.snap.Load()

	x.Publish([]Entry{driver(3, north(50), "standard")}, 0)

	got, err := x.query(old, pickup, 1000, 10, "")
	if err != nil {
		t.Fatalf("query(old) = %v", err)
	}
	if want := []uint32{1, 2}; !slices.Equal(ids(got), want) {
		t.Fatalf("query(old) = %v, want %v", ids(got), want)
	}

	got, err = x.Nearby(pickup, 1000, 10, "")
	if err != nil {
		t.Fatalf("Nearby() = %v", err)
	}
	if want := []uint32{3}; !slices.Equal(ids(got), want) {
		t.Fatalf("Nearby() = %v, want %v", ids(got), want)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	x := NewIndex()
	x.Publish(nil, 0)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 200 {
			x.Apply(driver(uint32(i%20), north(float64(i)), "standard"))
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 20 {
			upTo := x.Version()
			entries := make([]Entry, 0, 20)
			for id := range 20 {
				entries = append(entries, driver(uint32(id), north(float64(id*i)), "standard"))
			}
			x.Publish(entries, upTo)
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				got, err := x.Nearby(pickup, 5000, 5, "standard")
				if err != nil {
					t.Errorf("Nearby() = %v", err)
					return
				}
				if len(got) > 5 {
					t.Errorf("len = %d, want <= 5", len(got))
					return
				}
				if !slices.IsSortedFunc(got, func(a, b Result) int {
					return cmp.Or(cmp.Compare(a.StraightLineM, b.StraightLineM), cmp.Compare(a.ID, b.ID))
				}) {
					t.Errorf("results not sorted: %v", ids(got))
					return
				}
			}
		}()
	}
	wg.Wait()
}
