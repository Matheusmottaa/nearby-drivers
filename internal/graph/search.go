package graph

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

// EngineDijkstra names estimates computed by ETA.
const EngineDijkstra = "dijkstra-one-to-many"

// ErrInvalidRadius means max_radius_m is negative or NaN.
var ErrInvalidRadius = errors.New("invalid max radius")

// ErrPoolTooSmall means the pool buffers were sized for a smaller graph.
var ErrPoolTooSmall = errors.New("pool buffers smaller than graph")

// ctxCheckEvery is how many heap pops run between context checks.
const ctxCheckEvery = 256

// Estimate is the travel from one origin to the destination.
type Estimate struct {
	OriginIndex int
	DurationS   int
	DistanceM   int
	Reachable   bool
}

// Result holds one estimate per origin, in request order, and the number of settled nodes.
type Result struct {
	Estimates    []Estimate
	NodesVisited int
}

// ETA runs one Dijkstra on the reverse graph from the node nearest to
// destination, weighted by duration_s and accumulating distance_m. An edge that
// takes the accumulated distance past maxRadiusM is not relaxed. It stops when
// every distinct origin node is settled or the heap is empty; unsettled origins
// are not reachable. The snap legs are not part of the estimate.
func (g *Graph) ETA(ctx context.Context, pool *Pool, destination geo.Point, origins []geo.Point, maxRadiusM float64) (Result, error) {
	if math.IsNaN(maxRadiusM) || maxRadiusM < 0 {
		return Result{}, fmt.Errorf("max radius %v: %w", maxRadiusM, ErrInvalidRadius)
	}
	dest, _, err := g.snap(destination)
	if err != nil {
		return Result{}, fmt.Errorf("snap destination: %w", err)
	}
	nodes := make([]int32, len(origins))
	pending := make(map[int32]struct{}, len(origins))
	for i, o := range origins {
		v, _, err := g.snap(o)
		if err != nil {
			return Result{}, fmt.Errorf("snap origin %d: %w", i, err)
		}
		nodes[i] = v
		pending[v] = struct{}{}
	}
	if len(origins) == 0 {
		return Result{Estimates: []Estimate{}}, nil
	}
	if pool.nNodes < g.NodeCount() {
		return Result{}, fmt.Errorf("pool for %d nodes, graph has %d: %w", pool.nNodes, g.NodeCount(), ErrPoolTooSmall)
	}

	b, err := pool.acquire()
	if err != nil {
		return Result{}, err
	}
	defer pool.release(b)

	maxDistM := int64(math.MaxInt64)
	if maxRadiusM < math.MaxInt64 {
		maxDistM = int64(maxRadiusM)
	}
	visited, err := g.search(ctx, b, dest, pending, maxDistM)
	if err != nil {
		return Result{}, err
	}

	res := Result{Estimates: make([]Estimate, len(origins)), NodesVisited: visited}
	for i, v := range nodes {
		res.Estimates[i] = Estimate{OriginIndex: i}
		if b.settled(v) {
			res.Estimates[i].DurationS = int(b.dur[v])
			res.Estimates[i].DistanceM = int(b.dist[v])
			res.Estimates[i].Reachable = true
		}
	}
	return res, nil
}

func (g *Graph) search(ctx context.Context, b *buffer, dest int32, pending map[int32]struct{}, maxDistM int64) (int, error) {
	b.reset()
	b.stamp[dest] = b.gen
	b.dur[dest], b.dist[dest], b.pred[dest] = 0, 0, -1
	b.heap.push(heapItem{node: dest})

	visited := 0
	for pops := 0; len(b.heap) > 0 && len(pending) > 0; pops++ {
		if pops%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return visited, err
			}
		}
		it := b.heap.pop()
		v := it.node
		if b.settled(v) {
			continue
		}
		b.done[v] = b.gen
		visited++
		delete(pending, v)

		lo, hi := g.rev.edges(v)
		for j := lo; j < hi; j++ {
			u := g.rev.to[j]
			if b.settled(u) {
				continue
			}
			dist := it.dist + int64(g.rev.distM[j])
			if dist > maxDistM {
				continue
			}
			dur := it.dur + int64(g.rev.durS[j])
			next := heapItem{dur: dur, dist: dist, node: u}
			if b.seen(u) && !next.less(heapItem{dur: b.dur[u], dist: b.dist[u]}) {
				continue
			}
			b.stamp[u] = b.gen
			b.dur[u], b.dist[u], b.pred[u] = dur, dist, v
			b.heap.push(next)
		}
	}
	return visited, nil
}
