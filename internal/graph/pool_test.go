package graph

import (
	"errors"
	"sync"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func TestPoolOfOneSaturates(t *testing.T) {
	g := fixtureGraph(t)
	pool := NewPool(1, g.NodeCount())
	pickup := nodePoint(t, g, 0)
	origins := []geo.Point{nodePoint(t, g, 1)}

	held, err := pool.acquire()
	if err != nil {
		t.Fatalf("acquire() = %v", err)
	}
	if pool.InUse() != 1 {
		t.Fatalf("InUse() = %d, want 1", pool.InUse())
	}

	_, err = g.ETA(t.Context(), pool, pickup, origins, 15000)
	if !errors.Is(err, ErrSaturated) {
		t.Fatalf("second search = %v, want ErrSaturated", err)
	}
	if pool.InUse() != 1 {
		t.Fatalf("InUse() = %d after saturation, want 1", pool.InUse())
	}

	pool.release(held)
	if pool.InUse() != 0 {
		t.Fatalf("InUse() = %d after release, want 0", pool.InUse())
	}

	got, err := g.ETA(t.Context(), pool, pickup, origins, 15000)
	if err != nil {
		t.Fatalf("search after release = %v", err)
	}
	if !got.Estimates[0].Reachable {
		t.Fatalf("estimate = %+v, want reachable", got.Estimates[0])
	}
	if pool.InUse() != 0 {
		t.Fatalf("InUse() = %d after search, want 0", pool.InUse())
	}
}

func TestPoolAcquireNeverBlocks(t *testing.T) {
	pool := NewPool(0, 1)
	if _, err := pool.acquire(); !errors.Is(err, ErrSaturated) {
		t.Fatalf("acquire() on empty pool = %v, want ErrSaturated", err)
	}
	if pool.Size() != 0 {
		t.Fatalf("Size() = %d, want 0", pool.Size())
	}
}

func TestPoolConcurrentSearches(t *testing.T) {
	g := fixtureGraph(t)
	pool := NewPool(2, g.NodeCount())
	pickup := nodePoint(t, g, 0)
	origins := []geo.Point{nodePoint(t, g, 1), nodePoint(t, g, 4), nodePoint(t, g, 3)}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				got, err := g.ETA(t.Context(), pool, pickup, origins, 15000)
				if errors.Is(err, ErrSaturated) {
					continue
				}
				if err != nil {
					t.Errorf("ETA() = %v", err)
					return
				}
				if got.Estimates[0].DurationS != 142 || got.Estimates[1].DurationS != 20 || got.Estimates[2].Reachable {
					t.Errorf("estimates = %+v", got.Estimates)
					return
				}
			}
		}()
	}
	wg.Wait()
	if pool.InUse() != 0 {
		t.Fatalf("InUse() = %d, want 0", pool.InUse())
	}
}
