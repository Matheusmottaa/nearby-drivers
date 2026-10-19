package matching

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

// Probe serves GET /metrics. Set attaches the live matcher after dependencies open.
type Probe struct {
	mu      sync.RWMutex
	svc     *Service
	routing *ETAClient
}

// Set publishes the matcher and the routing client that own the counters.
func (p *Probe) Set(svc *Service, routing *ETAClient) {
	p.mu.Lock()
	p.svc = svc
	p.routing = routing
	p.mu.Unlock()
}

func (p *Probe) Write(w io.Writer) {
	p.mu.RLock()
	svc, routing := p.svc, p.routing
	p.mu.RUnlock()
	var counters Counters
	open := false
	if svc != nil {
		counters = svc.Counters()
	}
	if routing != nil {
		open = routing.BreakerOpen()
	}
	_ = WriteMetrics(w, counters, open)
}

// ServeHTTP writes the Prometheus text exposition. An unset probe reports zeros.
func (p *Probe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", problem.TraceID(r))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	p.Write(w)
}

// WriteMetrics writes the matching counters in the Prometheus text format.
func WriteMetrics(w io.Writer, c Counters, breakerOpen bool) error {
	open := 0
	if breakerOpen {
		open = 1
	}
	_, err := fmt.Fprintf(w, `# HELP matching_rankings_total Rankings produced by ETA engine.
# TYPE matching_rankings_total counter
matching_rankings_total{engine=%q} %d
matching_rankings_total{engine=%q} %d
# HELP matching_fallback_total Rankings that used the haversine fallback.
# TYPE matching_fallback_total counter
matching_fallback_total %d
# HELP matching_unreachable_total Candidates dropped because routing marked them unreachable.
# TYPE matching_unreachable_total counter
matching_unreachable_total %d
# HELP matching_duplicate_event_total Repeated rides.requested deliveries.
# TYPE matching_duplicate_event_total counter
matching_duplicate_event_total %d
# HELP matching_failed_total Match failures by reason.
# TYPE matching_failed_total counter
matching_failed_total{reason=%q} %d
matching_failed_total{reason=%q} %d
# HELP matching_routing_breaker_open Routing circuit breaker is open.
# TYPE matching_routing_breaker_open gauge
matching_routing_breaker_open %d
`,
		event.EngineDijkstra, c.Dijkstra,
		event.EngineHaversine, c.Fallback,
		c.Fallback,
		c.Unreachable,
		c.Duplicates,
		event.ReasonNoCandidate, c.FailedNoCandidate,
		event.ReasonNoneReachable, c.FailedNoneReachable,
		open,
	)
	return err
}
