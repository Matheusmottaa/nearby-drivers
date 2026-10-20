package graph

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "data", "maps", "graph.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if f.Version != "fixture-v1" {
		t.Fatalf("version = %q, want fixture-v1", f.Version)
	}
	if len(f.Nodes) != 19 || len(f.Edges) != 23 {
		t.Fatalf("size = %d nodes, %d edges, want 19 and 23", len(f.Nodes), len(f.Edges))
	}

	pickup := nodeByID(t, f, 0)
	if math.Abs(pickup.Lat-(-23.5613)) > 1e-9 || math.Abs(pickup.Lng-(-46.6565)) > 1e-9 {
		t.Fatalf("pickup = %v, want -23.5613,-46.6565", pickup)
	}
	origin := nodeByID(t, f, 1)
	if math.Abs(origin.Lat-(-23.5601)) > 1e-9 || math.Abs(origin.Lng-(-46.6588)) > 1e-9 {
		t.Fatalf("corridor origin = %v, want -23.5601,-46.6588", origin)
	}

	corridor := edgeBetween(t, f, 1, 0)
	if corridor.DistanceM != 820 || corridor.DurationS != 142 {
		t.Fatalf("corridor = %+v, want 820 m and 142 s", corridor)
	}
	alley := edgeBetween(t, f, 2, 3)
	if alley.DistanceM != 40 || alley.DurationS != 20 {
		t.Fatalf("alley = %+v, want 40 m and 20 s", alley)
	}
	for _, e := range f.Edges {
		if e.From == 2 && e.To == 0 {
			t.Fatal("alley reaches the pickup")
		}
	}

	slowM, slowS := pathWeight(t, f, [][2]int{{4, 5}, {5, 0}})
	fastM, fastS := pathWeight(t, f, [][2]int{{4, 6}, {6, 0}})
	if slowM >= fastM {
		t.Fatalf("short path is %d m, long path is %d m", slowM, fastM)
	}
	if fastS >= slowS {
		t.Fatalf("fast path is %d s, slow path is %d s", fastS, slowS)
	}
	if slowM != 200 || slowS != 160 || fastM != 1000 || fastS != 20 {
		t.Fatalf("paths = slow %dm %ds, fast %dm %ds", slowM, slowS, fastM, fastS)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "missing counts", body: "v1\n"},
		{name: "truncated", body: "v1\n1 0\n"},
		{name: "extra line", body: "v1\n0 0\nextra\n"},
		{name: "bad latitude", body: "v1\n1 0\n0 91 0\n"},
		{name: "bad longitude", body: "v1\n1 0\n0 0 181\n"},
		{name: "duplicate node", body: "v1\n2 0\n1 0 0\n1 1 1\n"},
		{name: "unknown endpoint", body: "v1\n1 1\n1 0 0\n1 2 10 1\n"},
		{name: "negative weight", body: "v1\n2 1\n1 0 0\n2 0 0\n1 2 -1 1\n"},
		{name: "negative count", body: "v1\n-1 0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.body))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestParseCRLF(t *testing.T) {
	body := "v1\r\n1 0\r\n7 -23.5 -46.6\r\n"
	f, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if f.Version != "v1" || len(f.Nodes) != 1 || f.Nodes[0].ID != 7 {
		t.Fatalf("file = %+v", f)
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join("..", "..", "data", "maps", "graph.txt")
	f, err := Load(t.Context(), path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if f.Version != "fixture-v1" {
		t.Fatalf("version = %q", f.Version)
	}

	_, err = Load(t.Context(), filepath.Join(t.TempDir(), "missing.txt"))
	if err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("Load(missing) = %v, want a read error", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Load(ctx, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) = %v, want context.Canceled", err)
	}
}

func nodeByID(t *testing.T, f File, id int) Node {
	t.Helper()
	for _, n := range f.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("missing node %d", id)
	return Node{}
}

func edgeBetween(t *testing.T, f File, from, to int) Edge {
	t.Helper()
	for _, e := range f.Edges {
		if e.From == from && e.To == to {
			return e
		}
	}
	t.Fatalf("missing edge %d -> %d", from, to)
	return Edge{}
}

func pathWeight(t *testing.T, f File, hops [][2]int) (distance, duration int) {
	t.Helper()
	for _, hop := range hops {
		e := edgeBetween(t, f, hop[0], hop[1])
		distance += e.DistanceM
		duration += e.DurationS
	}
	return distance, duration
}
