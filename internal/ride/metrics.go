package ride

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
)

const maxRecentAssignments = 20

// Assignment is one driver reserved for a ride.
type Assignment struct {
	Seq      int64
	RideID   string
	DriverID string
	Engine   string
	ETAS     int
}

type driverKey struct {
	driver string
	engine string
}

// Snapshot is a stable copy of the ride counters for /metrics.
type Snapshot struct {
	Recent    []Assignment
	ByDriver  map[driverKey]int64
	Conflicts int64
	Exhausted int64
	Rematches int64
}

// Snapshot copies the counters. Safe to call while rides are assigned.
func (s *Service) Snapshot() Snapshot {
	s.assignMu.Lock()
	recent := slices.Clone(s.recent)
	byDriver := maps.Clone(s.byDriver)
	s.assignMu.Unlock()

	return Snapshot{
		Recent:    recent,
		ByDriver:  byDriver,
		Conflicts: s.conflicts.Load(),
		Exhausted: s.exhausted.Load(),
		Rematches: s.rematches.Load(),
	}
}

func (s *Service) noteAssigned(next Ride) {
	engine := next.ETAEngine
	if engine == "" {
		engine = "unknown"
	}
	driver := next.AssignedDriverID
	if driver == "" {
		return
	}
	s.assignMu.Lock()
	defer s.assignMu.Unlock()
	s.assignN++
	s.byDriver[driverKey{driver: driver, engine: engine}]++
	s.recent = append(s.recent, Assignment{
		Seq: s.assignN, RideID: next.ID, DriverID: driver, Engine: engine, ETAS: next.AssignedETAS,
	})
	if len(s.recent) > maxRecentAssignments {
		s.recent = s.recent[len(s.recent)-maxRecentAssignments:]
	}
}

// WriteMetrics writes the ride counters in the Prometheus text format.
func WriteMetrics(w io.Writer, snap Snapshot, counts map[string]int) error {
	var b []byte
	b = append(b, `# HELP ride_assigned_total Drivers reserved for a ride, by driver and ETA engine.
# TYPE ride_assigned_total counter
`...)
	keys := slices.SortedFunc(maps.Keys(snap.ByDriver), func(a, b driverKey) int {
		return cmp.Or(cmp.Compare(a.driver, b.driver), cmp.Compare(a.engine, b.engine))
	})
	for _, key := range keys {
		b = fmt.Appendf(b, "ride_assigned_total{driver_id=%q,engine=%q} %d\n", key.driver, key.engine, snap.ByDriver[key])
	}
	b = append(b, `# HELP ride_assignment_eta_seconds ETA of a recent assignment. The series is the last 20.
# TYPE ride_assignment_eta_seconds gauge
`...)
	for _, a := range snap.Recent {
		b = fmt.Appendf(b, "ride_assignment_eta_seconds{seq=%q,ride_id=%q,driver_id=%q,engine=%q} %d\n",
			fmt.Sprintf("%d", a.Seq), a.RideID, a.DriverID, a.Engine, a.ETAS)
	}
	b = append(b, `# HELP ride_requests Distinct ride requests currently stored, one row per ride.
# TYPE ride_requests gauge
`...)
	for _, status := range []string{StatusRequested, StatusMatching, StatusAssigned, StatusCancelled, StatusNoDriver} {
		b = fmt.Appendf(b, "ride_requests{status=%q} %d\n", status, counts[status])
	}
	b = fmt.Appendf(b, `# HELP ride_reservation_conflict_total Reservations lost to another ride.
# TYPE ride_reservation_conflict_total counter
ride_reservation_conflict_total %d
# HELP ride_ranking_exhausted_total Rankings that produced no free driver.
# TYPE ride_ranking_exhausted_total counter
ride_ranking_exhausted_total %d
# HELP ride_rematch_total Rematches after a lost driver.
# TYPE ride_rematch_total counter
ride_rematch_total %d
`, snap.Conflicts, snap.Exhausted, snap.Rematches)
	_, err := w.Write(b)
	return err
}
