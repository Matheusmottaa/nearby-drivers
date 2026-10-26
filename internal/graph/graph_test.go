package graph

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func fixtureGraph(t *testing.T) *Graph {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "data", "maps", "fixture.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	g, err := Build(f)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return g
}

// nodePoint returns the coordinate of a node by its file ID.
func nodePoint(t *testing.T, g *Graph, id int) geo.Point {
	t.Helper()
	i := slices.Index(g.ids, id)
	if i < 0 {
		t.Fatalf("missing node %d", id)
	}
	return g.points[i]
}

type arc struct{ from, to, distM, durS int }

func arcs(g *Graph, c csr) []arc {
	var out []arc
	for v := range int32(len(g.ids)) {
		lo, hi := c.edges(v)
		for j := lo; j < hi; j++ {
			out = append(out, arc{g.ids[v], g.ids[c.to[j]], int(c.distM[j]), int(c.durS[j])})
		}
	}
	slices.SortFunc(out, func(a, b arc) int {
		if a.from != b.from {
			return a.from - b.from
		}
		return a.to - b.to
	})
	return out
}

func TestBuildCSR(t *testing.T) {
	g := fixtureGraph(t)
	if g.Version != "fixture-v1" || g.NodeCount() != 19 || g.EdgeCount() != 23 {
		t.Fatalf("graph = %q, %d nodes, %d edges", g.Version, g.NodeCount(), g.EdgeCount())
	}

	wantFwd := []arc{
		{1, 0, 820, 142},
		{2, 3, 40, 20},
		{4, 5, 100, 80},
		{4, 6, 500, 10},
		{5, 0, 100, 80},
		{6, 0, 500, 10},
		{7, 0, 180, 25},
		{8, 7, 180, 25},
		{9, 8, 180, 25},
		{10, 7, 170, 22},
		{11, 8, 170, 22},
		{11, 10, 180, 25},
		{12, 9, 170, 22},
		{12, 11, 180, 25},
		{13, 7, 170, 22},
		{14, 8, 170, 28},
		{14, 13, 180, 25},
		{15, 9, 170, 28},
		{15, 14, 180, 25},
		{16, 6, 170, 30},
		{17, 0, 170, 30},
		{18, 10, 180, 25},
		{18, 17, 180, 25},
	}
	if got := arcs(g, g.fwd); !slices.Equal(got, wantFwd) {
		t.Fatalf("forward = %v, want %v", got, wantFwd)
	}

	var wantRev []arc
	for _, a := range wantFwd {
		wantRev = append(wantRev, arc{a.to, a.from, a.distM, a.durS})
	}
	slices.SortFunc(wantRev, func(a, b arc) int {
		if a.from != b.from {
			return a.from - b.from
		}
		return a.to - b.to
	})
	if got := arcs(g, g.rev); !slices.Equal(got, wantRev) {
		t.Fatalf("reverse = %v, want %v", got, wantRev)
	}
	if len(g.fwd.offsets) != 20 || len(g.rev.offsets) != 20 {
		t.Fatalf("offsets = %d and %d, want 20", len(g.fwd.offsets), len(g.rev.offsets))
	}
}

func TestBuildSparseIDs(t *testing.T) {
	g, err := Build(File{
		Version: "v",
		Nodes:   []Node{{ID: 100, Lat: 0, Lng: 0}, {ID: 7, Lat: 0, Lng: 0.01}},
		Edges:   []Edge{{From: 100, To: 7, DistanceM: 1, DurationS: 2}},
	})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if got := arcs(g, g.fwd); !slices.Equal(got, []arc{{100, 7, 1, 2}}) {
		t.Fatalf("forward = %v", got)
	}
	id, _, err := g.Snap(geo.Point{Lat: 0, Lng: 0.009})
	if err != nil || id != 7 {
		t.Fatalf("Snap() = %d, %v, want 7", id, err)
	}
}

func TestBuildRejects(t *testing.T) {
	tests := []struct {
		name string
		f    File
	}{
		{name: "duplicate node", f: File{Nodes: []Node{{ID: 1}, {ID: 1}}}},
		{name: "invalid coordinate", f: File{Nodes: []Node{{ID: 1, Lat: 91}}}},
		{name: "unknown from", f: File{Nodes: []Node{{ID: 1}}, Edges: []Edge{{From: 2, To: 1}}}},
		{name: "unknown to", f: File{Nodes: []Node{{ID: 1}}, Edges: []Edge{{From: 1, To: 2}}}},
		{name: "negative weight", f: File{Nodes: []Node{{ID: 1}}, Edges: []Edge{{From: 1, To: 1, DistanceM: -1}}}},
		{name: "weight overflow", f: File{Nodes: []Node{{ID: 1}}, Edges: []Edge{{From: 1, To: 1, DurationS: math.MaxInt32 + 1}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.f)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Build() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSnap(t *testing.T) {
	g := fixtureGraph(t)
	tests := []struct {
		name    string
		p       geo.Point
		wantID  int
		maxM    float64
		wantErr error
	}{
		{name: "on the pickup", p: geo.Point{Lat: -23.5613, Lng: -46.6565}, wantID: 0, maxM: 0},
		{name: "near corridor origin", p: geo.Point{Lat: -23.56015, Lng: -46.65875}, wantID: 1, maxM: 10},
		{name: "near alley", p: geo.Point{Lat: -23.5641, Lng: -46.6566}, wantID: 3, maxM: 20},
		{name: "invalid", p: geo.Point{Lat: 0, Lng: 200}, wantErr: geo.ErrInvalidCoordinate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, d, err := g.Snap(tt.p)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Snap() error = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if id != tt.wantID {
				t.Fatalf("Snap() = %d, want %d", id, tt.wantID)
			}
			if d < 0 || d > tt.maxM {
				t.Fatalf("snap distance = %v m, want <= %v", d, tt.maxM)
			}
		})
	}
}

func TestSnapEmpty(t *testing.T) {
	g, err := Build(File{Version: "empty"})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if _, _, err := g.Snap(geo.Point{}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Snap() = %v, want ErrEmpty", err)
	}
}
