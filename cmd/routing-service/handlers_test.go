package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/graph"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

const fixturePath = "../../data/maps/graph.txt"

// The fixture: pickup at node 0, corridor from node 1, dead end at nodes 2 and 3.
const (
	pickup   = `{"lat": -23.5613, "lng": -46.6565}`
	corridor = `{"lat": -23.5601, "lng": -46.6588}`
	deadEnd  = `{"lat": -23.5630, "lng": -46.6565}`
)

// newTestServer serves the routes on a gate. With poolSize > 0 the fixture is
// published as the boot would; with 0 the graph stays loading.
func newTestServer(t *testing.T, poolSize int) (*server, *httpsrv.Gate) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)
	if poolSize > 0 {
		g, err := loadGraph(t.Context(), fixturePath)
		if err != nil {
			t.Fatalf("load fixture: %v", err)
		}
		srv.publish(g, graph.NewPool(poolSize, g.NodeCount()))
		gate.SetReady(true)
	}
	return srv, gate
}

func do(gate *httpsrv.Gate, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Request-Id", "req-test")
	rec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(rec, req)
	return rec
}

func etaBody(origins ...string) string {
	return fmt.Sprintf(`{"destination": %s, "origins": [%s], "max_radius_m": 15000}`, pickup, strings.Join(origins, ","))
}

func TestETABatchCorridorAndDeadEnd(t *testing.T) {
	_, gate := newTestServer(t, 2)

	rec := do(gate, http.MethodPost, "/routes/eta:batch", etaBody(corridor, deadEnd))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got etaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []estimate{
		{OriginIndex: 0, DurationS: 142, DistanceM: 820, Reachable: true},
		{OriginIndex: 1, Reachable: false},
	}
	if len(got.Estimates) != len(want) {
		t.Fatalf("estimates = %+v, want %+v", got.Estimates, want)
	}
	for i := range want {
		if got.Estimates[i] != want[i] {
			t.Errorf("estimates[%d] = %+v, want %+v", i, got.Estimates[i], want[i])
		}
	}
	if got.Engine != "dijkstra-one-to-many" {
		t.Errorf("engine = %q", got.Engine)
	}
	if got.GraphVersion != "fixture-v1" {
		t.Errorf("graph_version = %q", got.GraphVersion)
	}
	if got.NodesVisited <= 0 {
		t.Errorf("nodes_visited = %d", got.NodesVisited)
	}
	if !strings.Contains(rec.Body.String(), `"duration_s":0,"distance_m":0,"reachable":false`) {
		t.Errorf("unreachable estimate must keep zero fields: %s", rec.Body)
	}
}

func TestETABatchUnprocessable(t *testing.T) {
	_, gate := newTestServer(t, 1)

	many := make([]string, maxOrigins+1)
	for i := range many {
		many[i] = corridor
	}
	cases := map[string]string{
		"51 origins":      etaBody(many...),
		"invalid origin":  etaBody(`{"lat": 91, "lng": 0}`),
		"invalid dest":    `{"destination": {"lat": 0, "lng": 181}, "origins": []}`,
		"missing dest":    `{"origins": []}`,
		"negative radius": fmt.Sprintf(`{"destination": %s, "origins": [], "max_radius_m": -1}`, pickup),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(gate, http.MethodPost, "/routes/eta:batch", body)
			assertProblem(t, rec, http.StatusUnprocessableEntity)
		})
	}
}

func TestETABatchBadJSON(t *testing.T) {
	_, gate := newTestServer(t, 1)
	rec := do(gate, http.MethodPost, "/routes/eta:batch", `{"destination":`)
	assertProblem(t, rec, http.StatusBadRequest)
}

func TestETABatchPoolSaturated(t *testing.T) {
	srv, gate := newTestServer(t, 1)
	release, err := srv.current.Load().pool.Take()
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	defer release()

	rec := do(gate, http.MethodPost, "/routes/eta:batch", etaBody(corridor))
	assertProblem(t, rec, http.StatusServiceUnavailable)
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if got := srv.poolRejected.Load(); got != 1 {
		t.Errorf("pool_rejected = %d, want 1", got)
	}
}

func TestLoadingThenReady(t *testing.T) {
	srv, gate := newTestServer(t, 0)

	if rec := do(gate, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before load = %d, want 503", rec.Code)
	}
	rec := do(gate, http.MethodPost, "/routes/eta:batch", etaBody(corridor))
	assertProblem(t, rec, http.StatusServiceUnavailable)
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After while loading = %q, want none", got)
	}
	rec = do(gate, http.MethodGet, "/nodes/snap?lat=-23.5601&lng=-46.6588", "")
	assertProblem(t, rec, http.StatusServiceUnavailable)

	watchGraph(t.Context(), slog.New(slog.DiscardHandler), fixturePath, 1, gate, srv)

	if rec := do(gate, http.MethodGet, "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("readyz after load = %d, want 200", rec.Code)
	}
	if rec := do(gate, http.MethodPost, "/routes/eta:batch", etaBody(corridor)); rec.Code != http.StatusOK {
		t.Fatalf("eta after load = %d, body %s", rec.Code, rec.Body)
	}
}

func TestSnap(t *testing.T) {
	_, gate := newTestServer(t, 1)

	rec := do(gate, http.MethodGet, "/nodes/snap?lat=-23.56015&lng=-46.65885", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got snapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.NodeID != 1 || got.GraphVersion != "fixture-v1" {
		t.Errorf("snap = %+v, want node 1 on fixture-v1", got)
	}
	if got.DistanceM <= 0 || got.DistanceM > 20 {
		t.Errorf("distance_m = %v, want a few meters", got.DistanceM)
	}

	for _, q := range []string{"lat=91&lng=0", "lat=abc&lng=0", "lng=0"} {
		assertProblem(t, do(gate, http.MethodGet, "/nodes/snap?"+q, ""), http.StatusUnprocessableEntity)
	}
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, status, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", got)
	}
	var p problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.TraceID != "req-test" || p.Status != status {
		t.Fatalf("problem = %+v", p)
	}
}
