package kdtree

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

var fixtureNodes = []Item{
	{ID: 0, Point: geo.Point{Lat: -23.5613, Lng: -46.6565}},
	{ID: 1, Point: geo.Point{Lat: -23.5601, Lng: -46.6588}},
	{ID: 2, Point: geo.Point{Lat: -23.5630, Lng: -46.6565}},
	{ID: 3, Point: geo.Point{Lat: -23.5640, Lng: -46.6565}},
	{ID: 4, Point: geo.Point{Lat: -23.5613, Lng: -46.6600}},
	{ID: 5, Point: geo.Point{Lat: -23.5613, Lng: -46.6580}},
	{ID: 6, Point: geo.Point{Lat: -23.5590, Lng: -46.6580}},
}

func TestNearestFixture(t *testing.T) {
	tree := Build(fixtureNodes)
	tests := []struct {
		name   string
		p      geo.Point
		wantID uint32
	}{
		{name: "on pickup", p: fixtureNodes[0].Point, wantID: 0},
		{name: "near corridor origin", p: geo.Point{Lat: -23.56012, Lng: -46.65878}, wantID: 1},
		{name: "near alley end", p: geo.Point{Lat: -23.5645, Lng: -46.6566}, wantID: 3},
		{name: "west of node 4", p: geo.Point{Lat: -23.5613, Lng: -46.6700}, wantID: 4},
		{name: "north of node 6", p: geo.Point{Lat: -23.5500, Lng: -46.6580}, wantID: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, d, ok := tree.Nearest(tt.p)
			if !ok {
				t.Fatal("Nearest() ok = false")
			}
			if got.ID != tt.wantID {
				t.Fatalf("Nearest() = %d, want %d", got.ID, tt.wantID)
			}
			if want := geo.Distance(tt.p, got.Point); d != want {
				t.Errorf("distance = %v, want %v", d, want)
			}
		})
	}
}

func TestNearestTieLowestID(t *testing.T) {
	p := geo.Point{Lat: -23.56, Lng: -46.65}
	tree := Build([]Item{{ID: 9, Point: p}, {ID: 3, Point: p}, {ID: 5, Point: p}})
	got, _, _ := tree.Nearest(p)
	if got.ID != 3 {
		t.Fatalf("Nearest() = %d, want 3", got.ID)
	}
}

func TestEmptyTree(t *testing.T) {
	tree := Build(nil)
	if _, _, ok := tree.Nearest(geo.Point{}); ok {
		t.Fatal("Nearest() ok = true on an empty tree")
	}
	tree.Within(geo.Point{}, 1e9, func(Item, float64) { t.Fatal("Within() called fn on an empty tree") })
	if tree.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", tree.Len())
	}
}

func TestBuildDoesNotAlias(t *testing.T) {
	items := slices.Clone(fixtureNodes)
	tree := Build(items)
	items[1].Point = geo.Point{}
	got, _, _ := tree.Nearest(fixtureNodes[1].Point)
	if got.ID != 1 || got.Point != fixtureNodes[1].Point {
		t.Fatalf("Nearest() = %+v after mutating the input", got)
	}
}

func TestMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	tests := []struct {
		name          string
		lat, lng, deg float64
	}{
		{name: "sao paulo", lat: -23.56, lng: -46.65, deg: 0.3},
		{name: "high latitude", lat: 70, lng: 20, deg: 5},
		{name: "antimeridian", lat: 10, lng: 179.9, deg: 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := make([]Item, 500)
			for i := range items {
				items[i] = Item{ID: uint32(i), Point: randomPoint(rng, tt.lat, tt.lng, tt.deg)}
			}
			tree := Build(items)
			for range 200 {
				q := randomPoint(rng, tt.lat, tt.lng, tt.deg*1.5)

				got, gotD, _ := tree.Nearest(q)
				want, wantD := bruteNearest(items, q)
				if got.ID != want.ID || gotD != wantD {
					t.Fatalf("Nearest(%v) = %d (%v m), want %d (%v m)", q, got.ID, gotD, want.ID, wantD)
				}

				radius := rng.Float64() * tt.deg * 111_000 / 3
				var ids []uint32
				tree.Within(q, radius, func(it Item, d float64) {
					if d > radius {
						t.Fatalf("Within() reported %v m over radius %v", d, radius)
					}
					ids = append(ids, it.ID)
				})
				slices.Sort(ids)
				if wantIDs := bruteWithin(items, q, radius); !slices.Equal(ids, wantIDs) {
					t.Fatalf("Within(%v, %v) = %v, want %v", q, radius, ids, wantIDs)
				}
			}
		})
	}
}

func randomPoint(rng *rand.Rand, lat, lng, deg float64) geo.Point {
	p := geo.Point{
		Lat: lat + (rng.Float64()*2-1)*deg,
		Lng: lng + (rng.Float64()*2-1)*deg,
	}
	p.Lng = math.Remainder(p.Lng, 360)
	return p
}

func bruteNearest(items []Item, p geo.Point) (Item, float64) {
	best, bestD := Item{}, math.Inf(1)
	for _, it := range items {
		d := geo.Distance(p, it.Point)
		if d < bestD || (d == bestD && it.ID < best.ID) {
			best, bestD = it, d
		}
	}
	return best, bestD
}

func bruteWithin(items []Item, p geo.Point, radius float64) []uint32 {
	var ids []uint32
	for _, it := range items {
		if geo.Distance(p, it.Point) <= radius {
			ids = append(ids, it.ID)
		}
	}
	slices.Sort(ids)
	return ids
}
