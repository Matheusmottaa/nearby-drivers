// Package graph reads the versioned road-graph file mounted at /data/maps.
package graph

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// ErrInvalid means the file is not the graph text format.
var ErrInvalid = errors.New("invalid graph file")

// Node is a vertex in the graph file.
type Node struct {
	ID  int
	Lat float64
	Lng float64
}

// Edge is a directed road segment. DistanceM and DurationS are the two weights.
type Edge struct {
	From      int
	To        int
	DistanceM int
	DurationS int
}

// File is the graph as stored on disk, before CSR and the reverse graph.
type File struct {
	Version string
	Nodes   []Node
	Edges   []Edge
}

// Load reads path and parses the graph text format.
func Load(ctx context.Context, path string) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read graph %s: %w", path, err)
	}
	f, err := Parse(data)
	if err != nil {
		return File{}, fmt.Errorf("parse graph %s: %w", path, err)
	}
	return f, nil
}

// Parse decodes the graph text format:
//
//	graph_version
//	<n_nodes> <n_edges>
//	<id> <lat> <lng>
//	<from> <to> <distance_m> <duration_s>
func Parse(data []byte) (File, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return File{}, fmt.Errorf("empty file: %w", ErrInvalid)
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	if len(lines) < 2 {
		return File{}, fmt.Errorf("missing counts: %w", ErrInvalid)
	}

	version := strings.Fields(lines[0])
	if len(version) != 1 {
		return File{}, fmt.Errorf("graph version: %w", ErrInvalid)
	}

	counts := strings.Fields(lines[1])
	if len(counts) != 2 {
		return File{}, fmt.Errorf("node and edge counts: %w", ErrInvalid)
	}
	nNodes, err := strconv.Atoi(counts[0])
	if err != nil {
		return File{}, fmt.Errorf("node count: %w", ErrInvalid)
	}
	nEdges, err := strconv.Atoi(counts[1])
	if err != nil {
		return File{}, fmt.Errorf("edge count: %w", ErrInvalid)
	}
	if nNodes < 0 || nEdges < 0 {
		return File{}, fmt.Errorf("negative count: %w", ErrInvalid)
	}
	if len(lines) != 2+nNodes+nEdges {
		return File{}, fmt.Errorf("line count %d, want %d: %w", len(lines), 2+nNodes+nEdges, ErrInvalid)
	}

	nodes := make([]Node, nNodes)
	index := make(map[int]struct{}, nNodes)
	for i := range nNodes {
		node, err := parseNode(lines[2+i])
		if err != nil {
			return File{}, fmt.Errorf("line %d: %w", 3+i, err)
		}
		if _, ok := index[node.ID]; ok {
			return File{}, fmt.Errorf("line %d: duplicate node %d: %w", 3+i, node.ID, ErrInvalid)
		}
		index[node.ID] = struct{}{}
		nodes[i] = node
	}

	edges := make([]Edge, nEdges)
	for i := range nEdges {
		edge, err := parseEdge(lines[2+nNodes+i])
		if err != nil {
			return File{}, fmt.Errorf("line %d: %w", 3+nNodes+i, err)
		}
		if _, ok := index[edge.From]; !ok {
			return File{}, fmt.Errorf("line %d: unknown from %d: %w", 3+nNodes+i, edge.From, ErrInvalid)
		}
		if _, ok := index[edge.To]; !ok {
			return File{}, fmt.Errorf("line %d: unknown to %d: %w", 3+nNodes+i, edge.To, ErrInvalid)
		}
		edges[i] = edge
	}

	return File{Version: version[0], Nodes: nodes, Edges: edges}, nil
}

func parseNode(line string) (Node, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return Node{}, fmt.Errorf("node: %w", ErrInvalid)
	}
	id, err := strconv.Atoi(fields[0])
	if err != nil {
		return Node{}, fmt.Errorf("node id: %w", ErrInvalid)
	}
	lat, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return Node{}, fmt.Errorf("lat: %w", ErrInvalid)
	}
	lng, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return Node{}, fmt.Errorf("lng: %w", ErrInvalid)
	}
	if !validCoord(lat, lng) {
		return Node{}, fmt.Errorf("coordinate: %w", ErrInvalid)
	}
	return Node{ID: id, Lat: lat, Lng: lng}, nil
}

func parseEdge(line string) (Edge, error) {
	fields := strings.Fields(line)
	if len(fields) != 4 {
		return Edge{}, fmt.Errorf("edge: %w", ErrInvalid)
	}
	from, err := strconv.Atoi(fields[0])
	if err != nil {
		return Edge{}, fmt.Errorf("edge from: %w", ErrInvalid)
	}
	to, err := strconv.Atoi(fields[1])
	if err != nil {
		return Edge{}, fmt.Errorf("edge to: %w", ErrInvalid)
	}
	distance, err := strconv.Atoi(fields[2])
	if err != nil {
		return Edge{}, fmt.Errorf("distance_m: %w", ErrInvalid)
	}
	duration, err := strconv.Atoi(fields[3])
	if err != nil {
		return Edge{}, fmt.Errorf("duration_s: %w", ErrInvalid)
	}
	if distance < 0 || duration < 0 {
		return Edge{}, fmt.Errorf("negative weight: %w", ErrInvalid)
	}
	return Edge{From: from, To: to, DistanceM: distance, DurationS: duration}, nil
}

func validCoord(lat, lng float64) bool {
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lng) || math.IsInf(lng, 0) {
		return false
	}
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}
