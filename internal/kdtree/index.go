package kdtree

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

// ErrNotReady means no snapshot has been published yet.
var ErrNotReady = errors.New("index not ready")

// ErrInvalidQuery means the radius or K of a nearby query is out of range.
var ErrInvalidQuery = errors.New("invalid nearby query")

// Entry is the indexed state of one driver.
type Entry struct {
	ID        uint32
	Point     geo.Point
	Class     string
	Available bool
}

// Result is one driver returned by Nearby.
type Result struct {
	Entry
	StraightLineM float64
}

type snapshot struct {
	tree    *Tree
	entries map[uint32]Entry
	upTo    uint64
}

type deltaEntry struct {
	entry   Entry
	version uint64
}

// Index answers nearby queries from an atomically published snapshot plus the
// delta of updates the snapshot has not incorporated yet.
type Index struct {
	snap atomic.Pointer[snapshot]

	mu      sync.RWMutex
	delta   map[uint32]deltaEntry
	version uint64
}

// NewIndex returns an index with no snapshot; Nearby fails with ErrNotReady until Publish.
func NewIndex() *Index {
	return &Index{delta: make(map[uint32]deltaEntry)}
}

// Apply records an update in the delta and returns its version.
func (x *Index) Apply(e Entry) uint64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.version++
	x.delta[e.ID] = deltaEntry{entry: e, version: x.version}
	return x.version
}

// Version is the version of the latest Apply. A rebuild reads it before copying
// the slots and passes it to Publish as upTo.
func (x *Index) Version() uint64 {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.version
}

// Publish swaps in a snapshot built from the available entries and drops the
// delta updates with version <= upTo. The last entry wins on a repeated ID.
func (x *Index) Publish(entries []Entry, upTo uint64) {
	byID := make(map[uint32]Entry, len(entries))
	for _, e := range entries {
		if e.Available {
			byID[e.ID] = e
		} else {
			delete(byID, e.ID)
		}
	}
	items := make([]Item, 0, len(byID))
	for _, e := range byID {
		items = append(items, Item{ID: e.ID, Point: e.Point})
	}
	x.snap.Store(&snapshot{tree: Build(items), entries: byID, upTo: upTo})

	x.mu.Lock()
	defer x.mu.Unlock()
	for id, d := range x.delta {
		if d.version <= upTo {
			delete(x.delta, id)
		}
	}
}

// Nearby returns up to k available drivers of class within radiusM of p,
// ordered by straight-line distance and then by ID. An empty class matches any class.
func (x *Index) Nearby(p geo.Point, radiusM float64, k int, class string) ([]Result, error) {
	return x.query(x.snap.Load(), p, radiusM, k, class)
}

func (x *Index) query(view *snapshot, p geo.Point, radiusM float64, k int, class string) ([]Result, error) {
	if view == nil {
		return nil, ErrNotReady
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if math.IsNaN(radiusM) || radiusM < 0 || k <= 0 {
		return nil, fmt.Errorf("radius %v, k %d: %w", radiusM, k, ErrInvalidQuery)
	}
	match := func(e Entry) bool {
		return e.Available && (class == "" || e.Class == class)
	}

	x.mu.RLock()
	newer := make(map[uint32]Entry)
	for id, d := range x.delta {
		if d.version > view.upTo {
			newer[id] = d.entry
		}
	}
	x.mu.RUnlock()

	results := make([]Result, 0, k)
	view.tree.Within(p, radiusM, func(it Item, d float64) {
		if _, ok := newer[it.ID]; ok {
			return
		}
		if e := view.entries[it.ID]; match(e) {
			results = append(results, Result{Entry: e, StraightLineM: d})
		}
	})
	for _, e := range newer {
		if !match(e) {
			continue
		}
		if d := geo.Distance(p, e.Point); d <= radiusM {
			results = append(results, Result{Entry: e, StraightLineM: d})
		}
	}

	slices.SortFunc(results, func(a, b Result) int {
		return cmp.Or(cmp.Compare(a.StraightLineM, b.StraightLineM), cmp.Compare(a.ID, b.ID))
	})
	return results[:min(len(results), k)], nil
}
