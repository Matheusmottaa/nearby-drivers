package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/graph"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

const (
	maxOrigins   = 50
	maxBodyBytes = 1 << 20
)

// routing is the graph and its search pool, published together once built.
type routing struct {
	graph *graph.Graph
	pool  *graph.Pool
}

type server struct {
	log     *slog.Logger
	current atomic.Pointer[routing]

	etaCalls     atomic.Int64
	poolRejected atomic.Int64
	unreachable  atomic.Int64
}

func newServer(log *slog.Logger) *server {
	return &server{log: log}
}

func (s *server) register(gate *httpsrv.Gate) {
	gate.Handle("/routes/eta:batch", http.HandlerFunc(s.etaBatch))
	gate.Handle("/nodes/snap", http.HandlerFunc(s.snap))
}

func (s *server) publish(g *graph.Graph, pool *graph.Pool) {
	s.current.Store(&routing{graph: g, pool: pool})
}

type point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func (p point) geo() geo.Point {
	return geo.Point{Lat: p.Lat, Lng: p.Lng}
}

type etaRequest struct {
	Destination *point   `json:"destination"`
	Origins     []point  `json:"origins"`
	MaxRadiusM  *float64 `json:"max_radius_m"`
}

type estimate struct {
	OriginIndex int  `json:"origin_index"`
	DurationS   int  `json:"duration_s"`
	DistanceM   int  `json:"distance_m"`
	Reachable   bool `json:"reachable"`
}

type etaResponse struct {
	Estimates    []estimate `json:"estimates"`
	Engine       string     `json:"engine"`
	NodesVisited int        `json:"nodes_visited"`
	GraphVersion string     `json:"graph_version"`
}

type snapResponse struct {
	NodeID       int     `json:"node_id"`
	DistanceM    float64 `json:"distance_m"`
	GraphVersion string  `json:"graph_version"`
}

func (s *server) etaBatch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	trace := problem.TraceID(r)
	if r.Method != http.MethodPost {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use POST", trace)
		return
	}

	var req etaRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	dest, origins, radius, err := req.validate()
	if err != nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", err.Error(), trace)
		return
	}

	rt := s.current.Load()
	if rt == nil {
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "graph is loading", trace)
		return
	}

	s.etaCalls.Add(1)
	res, err := rt.graph.ETA(r.Context(), rt.pool, dest, origins, radius)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.writeETAError(w, err, trace, rt)
		return
	}

	resp := etaResponse{
		Estimates:    make([]estimate, len(res.Estimates)),
		Engine:       graph.EngineDijkstra,
		NodesVisited: res.NodesVisited,
		GraphVersion: rt.graph.Version,
	}
	unreachable := 0
	for i, e := range res.Estimates {
		resp.Estimates[i] = estimate(e)
		if !e.Reachable {
			unreachable++
		}
	}
	s.unreachable.Add(int64(unreachable))
	writeJSON(w, http.StatusOK, resp)

	s.log.Info("eta batch",
		"trace_id", trace,
		"graph_version", rt.graph.Version,
		"engine", graph.EngineDijkstra,
		"nodes_visited", res.NodesVisited,
		"origins", len(origins),
		"unreachable", unreachable,
		"pool_in_use", rt.pool.InUse(),
		"duration_ms", time.Since(start).Milliseconds(),
		"eta_calls_total", s.etaCalls.Load(),
		"pool_rejected_total", s.poolRejected.Load(),
		"unreachable_total", s.unreachable.Load(),
	)
}

func (s *server) writeETAError(w http.ResponseWriter, err error, trace string, rt *routing) {
	switch {
	case errors.Is(err, graph.ErrSaturated):
		rejected := s.poolRejected.Add(1)
		w.Header().Set("Retry-After", "1")
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "search pool saturated", trace)
		s.log.Warn("eta batch rejected",
			"trace_id", trace,
			"graph_version", rt.graph.Version,
			"pool_in_use", rt.pool.InUse(),
			"pool_rejected_total", rejected,
		)
	case errors.Is(err, geo.ErrInvalidCoordinate), errors.Is(err, graph.ErrInvalidRadius):
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", err.Error(), trace)
	case errors.Is(err, graph.ErrEmpty):
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "graph has no nodes", trace)
	default:
		problem.Write(w, http.StatusInternalServerError, "internal error", "eta search failed", trace)
		s.log.Error("eta batch", "trace_id", trace, "graph_version", rt.graph.Version, "err", err)
	}
}

func (req etaRequest) validate() (geo.Point, []geo.Point, float64, error) {
	if req.Destination == nil {
		return geo.Point{}, nil, 0, errors.New("destination is required")
	}
	dest := req.Destination.geo()
	if err := dest.Validate(); err != nil {
		return geo.Point{}, nil, 0, fmt.Errorf("destination: %w", err)
	}
	if len(req.Origins) > maxOrigins {
		return geo.Point{}, nil, 0, fmt.Errorf("origins must be <= %d, got %d", maxOrigins, len(req.Origins))
	}
	origins := make([]geo.Point, len(req.Origins))
	for i, o := range req.Origins {
		origins[i] = o.geo()
		if err := origins[i].Validate(); err != nil {
			return geo.Point{}, nil, 0, fmt.Errorf("origins[%d]: %w", i, err)
		}
	}
	radius := math.Inf(1)
	if req.MaxRadiusM != nil {
		radius = *req.MaxRadiusM
		if math.IsNaN(radius) || radius < 0 {
			return geo.Point{}, nil, 0, fmt.Errorf("max_radius_m must be >= 0, got %v", radius)
		}
	}
	return dest, origins, radius, nil
}

func (s *server) snap(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", trace)
		return
	}

	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", "lat and lng must be numbers", trace)
		return
	}
	p := geo.Point{Lat: lat, Lng: lng}
	if err := p.Validate(); err != nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", err.Error(), trace)
		return
	}

	rt := s.current.Load()
	if rt == nil {
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "graph is loading", trace)
		return
	}

	id, dist, err := rt.graph.Snap(p)
	if errors.Is(err, graph.ErrEmpty) {
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "graph has no nodes", trace)
		return
	}
	if err != nil {
		problem.Write(w, http.StatusInternalServerError, "internal error", "snap failed", trace)
		s.log.Error("snap", "trace_id", trace, "graph_version", rt.graph.Version, "err", err)
		return
	}
	writeJSON(w, http.StatusOK, snapResponse{NodeID: id, DistanceM: dist, GraphVersion: rt.graph.Version})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
