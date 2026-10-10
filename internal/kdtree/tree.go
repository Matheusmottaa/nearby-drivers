// Package kdtree is a 2D tree over coordinates, used for graph-node snapping and
// for the available-driver index.
package kdtree

import (
	"cmp"
	"math"
	"slices"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

const metersPerDegree = geo.EarthRadiusM * math.Pi / 180

// cutMargin widens the pruning test so the local projection never discards an
// item whose Haversine distance is inside the bound.
const cutMargin = 1.01

// Item is a point stored in the tree.
type Item struct {
	ID    uint32
	Point geo.Point
}

// Tree is a static 2D tree. Items are projected to local meters for the cuts;
// every reported distance is geo.Distance.
type Tree struct {
	items  []Item
	xs, ys []float64
	lat0   float64
	lng0   float64
	cos0   float64
	minCos float64
}

// Build lays the items out as an implicit median tree. Tree is read-only after Build.
func Build(items []Item) *Tree {
	t := &Tree{items: slices.Clone(items), cos0: 1, minCos: 1}
	n := len(t.items)
	if n == 0 {
		return t
	}

	var sumLat, sinLng, cosLng float64
	for _, it := range t.items {
		sumLat += it.Point.Lat
		sinLng += math.Sin(it.Point.Lng * math.Pi / 180)
		cosLng += math.Cos(it.Point.Lng * math.Pi / 180)
	}
	t.lat0 = sumLat / float64(n)
	t.lng0 = math.Atan2(sinLng, cosLng) * 180 / math.Pi
	t.cos0 = math.Cos(t.lat0 * math.Pi / 180)
	t.minCos = t.cos0
	for _, it := range t.items {
		t.minCos = min(t.minCos, math.Cos(it.Point.Lat*math.Pi/180))
	}

	t.xs = make([]float64, n)
	t.ys = make([]float64, n)
	t.build(0, n, 0)
	for i, it := range t.items {
		t.xs[i], t.ys[i] = t.project(it.Point)
	}
	return t
}

// Len is the number of items in the tree.
func (t *Tree) Len() int {
	return len(t.items)
}

func (t *Tree) build(lo, hi, depth int) {
	if hi-lo <= 1 {
		return
	}
	axis := depth % 2
	part := t.items[lo:hi]
	slices.SortFunc(part, func(a, b Item) int {
		ax, ay := t.project(a.Point)
		bx, by := t.project(b.Point)
		if axis == 0 {
			return cmp.Or(cmp.Compare(ax, bx), cmp.Compare(a.ID, b.ID))
		}
		return cmp.Or(cmp.Compare(ay, by), cmp.Compare(a.ID, b.ID))
	})
	mid := (lo + hi) / 2
	t.build(lo, mid, depth+1)
	t.build(mid+1, hi, depth+1)
}

func (t *Tree) project(p geo.Point) (x, y float64) {
	dLng := math.Remainder(p.Lng-t.lng0, 360)
	return dLng * t.cos0 * metersPerDegree, (p.Lat - t.lat0) * metersPerDegree
}

// slack bounds how much the projection can shrink a distance around p.
func (t *Tree) slack(p geo.Point) float64 {
	c := min(t.minCos, math.Cos(p.Lat*math.Pi/180))
	if c <= 1e-9 {
		return math.Inf(1)
	}
	return cutMargin * max(1, t.cos0/c)
}

// Nearest returns the item closest to p, its distance in meters and false if the tree is empty.
// Equal distances resolve to the lower ID.
func (t *Tree) Nearest(p geo.Point) (Item, float64, bool) {
	if len(t.items) == 0 {
		return Item{}, 0, false
	}
	s := search{t: t, p: p, slack: t.slack(p), best: -1, bestD: math.Inf(1)}
	s.x, s.y = t.project(p)
	s.nearest(0, len(t.items), 0)
	return t.items[s.best], s.bestD, true
}

// Within calls fn for every item at most radiusM meters from p, in no particular order.
func (t *Tree) Within(p geo.Point, radiusM float64, fn func(Item, float64)) {
	if len(t.items) == 0 || !(radiusM >= 0) {
		return
	}
	s := search{t: t, p: p, slack: t.slack(p)}
	s.x, s.y = t.project(p)
	s.within(0, len(t.items), 0, radiusM, fn)
}

type search struct {
	t     *Tree
	p     geo.Point
	x, y  float64
	slack float64
	best  int
	bestD float64
}

func (s *search) diff(i, axis int) float64 {
	if axis == 0 {
		return s.x - s.t.xs[i]
	}
	return s.y - s.t.ys[i]
}

func (s *search) nearest(lo, hi, depth int) {
	if lo >= hi {
		return
	}
	mid := (lo + hi) / 2
	it := s.t.items[mid]
	d := geo.Distance(s.p, it.Point)
	if d < s.bestD || (d == s.bestD && it.ID < s.t.items[s.best].ID) {
		s.best, s.bestD = mid, d
	}

	diff := s.diff(mid, depth%2)
	nearLo, nearHi, farLo, farHi := lo, mid, mid+1, hi
	if diff > 0 {
		nearLo, nearHi, farLo, farHi = mid+1, hi, lo, mid
	}
	s.nearest(nearLo, nearHi, depth+1)
	if math.Abs(diff) <= s.bestD*s.slack {
		s.nearest(farLo, farHi, depth+1)
	}
}

func (s *search) within(lo, hi, depth int, radiusM float64, fn func(Item, float64)) {
	if lo >= hi {
		return
	}
	mid := (lo + hi) / 2
	it := s.t.items[mid]
	if d := geo.Distance(s.p, it.Point); d <= radiusM {
		fn(it, d)
	}

	diff := s.diff(mid, depth%2)
	bound := radiusM * s.slack
	if diff <= bound {
		s.within(lo, mid, depth+1, radiusM, fn)
	}
	if -diff <= bound {
		s.within(mid+1, hi, depth+1, radiusM, fn)
	}
}
