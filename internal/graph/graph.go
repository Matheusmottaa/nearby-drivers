package graph

import (
	"errors"
	"fmt"
	"math"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/kdtree"
)

// ErrEmpty means the graph has no nodes to snap to.
var ErrEmpty = errors.New("graph has no nodes")

// csr is a compressed sparse row adjacency: the edges leaving node v are
// to[offsets[v]:offsets[v+1]], with the matching weights.
type csr struct {
	offsets []int32
	to      []int32
	distM   []int32
	durS    []int32
}

func (c *csr) edges(v int32) (lo, hi int32) {
	return c.offsets[v], c.offsets[v+1]
}

// Graph is the read-only road graph: forward and reverse CSR and a KD-Tree of
// the nodes. Nodes are dense indexes 0..n-1; ids maps them back to file IDs.
type Graph struct {
	Version string

	ids    []int
	points []geo.Point
	fwd    csr
	rev    csr
	nodes  *kdtree.Tree
}

// Build maps file node IDs to dense indexes and builds both CSRs and the node tree.
func Build(f File) (*Graph, error) {
	n := len(f.Nodes)
	if n > math.MaxInt32 || len(f.Edges) > math.MaxInt32 {
		return nil, fmt.Errorf("graph size %d nodes, %d edges: %w", n, len(f.Edges), ErrInvalid)
	}
	g := &Graph{
		Version: f.Version,
		ids:     make([]int, n),
		points:  make([]geo.Point, n),
	}
	dense := make(map[int]int32, n)
	items := make([]kdtree.Item, n)
	for i, node := range f.Nodes {
		if _, ok := dense[node.ID]; ok {
			return nil, fmt.Errorf("duplicate node %d: %w", node.ID, ErrInvalid)
		}
		p := geo.Point{Lat: node.Lat, Lng: node.Lng}
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("node %d: %w", node.ID, errors.Join(ErrInvalid, err))
		}
		dense[node.ID] = int32(i)
		g.ids[i] = node.ID
		g.points[i] = p
		items[i] = kdtree.Item{ID: uint32(i), Point: p}
	}

	from := make([]int32, len(f.Edges))
	to := make([]int32, len(f.Edges))
	for i, e := range f.Edges {
		u, ok := dense[e.From]
		if !ok {
			return nil, fmt.Errorf("edge %d: unknown from %d: %w", i, e.From, ErrInvalid)
		}
		v, ok := dense[e.To]
		if !ok {
			return nil, fmt.Errorf("edge %d: unknown to %d: %w", i, e.To, ErrInvalid)
		}
		if e.DistanceM < 0 || e.DurationS < 0 || e.DistanceM > math.MaxInt32 || e.DurationS > math.MaxInt32 {
			return nil, fmt.Errorf("edge %d: weight out of range: %w", i, ErrInvalid)
		}
		from[i], to[i] = u, v
	}
	g.fwd = buildCSR(n, from, to, f.Edges)
	g.rev = buildCSR(n, to, from, f.Edges)
	g.nodes = kdtree.Build(items)
	return g, nil
}

func buildCSR(n int, from, to []int32, edges []Edge) csr {
	c := csr{
		offsets: make([]int32, n+1),
		to:      make([]int32, len(edges)),
		distM:   make([]int32, len(edges)),
		durS:    make([]int32, len(edges)),
	}
	for _, u := range from {
		c.offsets[u+1]++
	}
	for v := range n {
		c.offsets[v+1] += c.offsets[v]
	}
	next := make([]int32, n)
	copy(next, c.offsets[:n])
	for i, u := range from {
		j := next[u]
		next[u]++
		c.to[j] = to[i]
		c.distM[j] = int32(edges[i].DistanceM)
		c.durS[j] = int32(edges[i].DurationS)
	}
	return c
}

// NodeCount is the number of nodes, the buffer size a Pool needs.
func (g *Graph) NodeCount() int {
	return len(g.ids)
}

// EdgeCount is the number of directed edges.
func (g *Graph) EdgeCount() int {
	return len(g.fwd.to)
}

// Snap returns the file ID of the node closest to p and the distance to it in meters.
func (g *Graph) Snap(p geo.Point) (nodeID int, distM float64, err error) {
	v, d, err := g.snap(p)
	if err != nil {
		return 0, 0, err
	}
	return g.ids[v], d, nil
}

func (g *Graph) snap(p geo.Point) (int32, float64, error) {
	if err := p.Validate(); err != nil {
		return 0, 0, err
	}
	it, d, ok := g.nodes.Nearest(p)
	if !ok {
		return 0, 0, ErrEmpty
	}
	return int32(it.ID), d, nil
}
