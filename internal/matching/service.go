package matching

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

const (
	defaultK       = 10
	maxK           = 50
	defaultRadiusM = 3000
	defaultSpeed   = 8.33
)

// Options is the ranking policy read from the environment.
type Options struct {
	K        int
	RadiusM  float64
	SpeedMPS float64
}

// Service turns rides.requested into a full ranking or a match failure.
// One process consumes the matchers group. It does not store rides.
type Service struct {
	log    *slog.Logger
	nearby NearbyAPI
	eta    ETAAPI
	dedup  Dedup
	pub    Publisher
	bus    interface {
		Consume(ctx context.Context, handle func(context.Context, string, []byte) error) error
	}
	k      int
	radius float64
	speed  float64

	candidates  atomic.Int64
	unreachable atomic.Int64
	fallback    atomic.Int64
	failed      atomic.Int64
	duplicates  atomic.Int64

	loopWG sync.WaitGroup
}

// New returns a matcher. A nil dedup never drops a duplicate.
func New(log *slog.Logger, nearby NearbyAPI, eta ETAAPI, dedup Dedup, pub Publisher, opt Options) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if dedup == nil {
		dedup = NopDedup{}
	}
	k := opt.K
	if k < 1 {
		k = defaultK
	}
	if k > maxK {
		k = maxK
	}
	radius := opt.RadiusM
	if radius <= 0 {
		radius = defaultRadiusM
	}
	speed := opt.SpeedMPS
	if speed <= 0 {
		speed = defaultSpeed
	}
	return &Service{
		log: log, nearby: nearby, eta: eta, dedup: dedup, pub: pub,
		k: k, radius: radius, speed: speed,
	}
}

// UseKafka attaches the consumer. Loop is a no-op until this is set.
func (s *Service) UseKafka(k *Kafka) {
	s.bus = k
	if s.pub == nil {
		s.pub = k
	}
}

// Loop consumes rides.requested until ctx is canceled.
func (s *Service) Loop(ctx context.Context) {
	if s.bus == nil {
		return
	}
	s.loopWG.Add(1)
	go func() {
		defer s.loopWG.Done()
		for ctx.Err() == nil {
			err := s.bus.Consume(ctx, s.Handle)
			if ctx.Err() != nil {
				return
			}
			s.log.Error("consume", "err", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

// Wait blocks until Loop has finished.
func (s *Service) Wait() { s.loopWG.Wait() }

// Handle processes one record. A nil error is the signal to ack.
// A failed publish returns an error and leaves the record uncommitted.
func (s *Service) Handle(ctx context.Context, topic string, value []byte) error {
	if topic != event.TopicRidesRequested {
		return nil
	}
	req, err := event.Decode[event.Requested](value)
	if err != nil {
		s.log.WarnContext(ctx, "decode", "err", err, "topic", topic)
		return nil
	}
	return s.Match(ctx, req)
}

// Match builds the ranking for one rides.requested delivery.
func (s *Service) Match(ctx context.Context, req event.Requested) error {
	if req.EventID != "" {
		seen, err := s.dedup.Seen(ctx, req.EventID)
		if err != nil {
			s.log.WarnContext(ctx, "dedup", "err", err, "event_id", req.EventID, "trace_id", req.TraceID, "ride_id", req.RideID)
		} else if seen {
			s.duplicates.Add(1)
			s.log.InfoContext(ctx, "matching.duplicate_event",
				"event_id", req.EventID, "trace_id", req.TraceID, "ride_id", req.RideID,
				"duplicate_total", s.duplicates.Load())
			return nil
		}
	}

	pickup := geo.Point{Lat: req.Pickup.Lat, Lng: req.Pickup.Lng}
	start := time.Now()
	found, err := s.nearby.Nearby(ctx, req.TraceID, pickup, s.radius, s.k, req.VehicleClass)
	s.log.InfoContext(ctx, "matching.duration_ms",
		"stage", "nearby", "duration_ms", time.Since(start).Milliseconds(),
		"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
	if err != nil {
		return fmt.Errorf("nearby: %w", err)
	}
	s.candidates.Add(int64(len(found)))
	s.log.InfoContext(ctx, "matching.candidates",
		"count", len(found), "candidates_total", s.candidates.Load(),
		"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)

	candidates := exclude(found, req.ExcludeDriverIDs)
	if len(candidates) == 0 {
		return s.fail(ctx, req, event.ReasonNoCandidate)
	}

	ranked, engine, err := s.rank(ctx, req, pickup, candidates)
	if err != nil {
		return err
	}
	if len(ranked) == 0 {
		return s.fail(ctx, req, event.ReasonNoneReachable)
	}
	return s.publishMatched(ctx, req, engine, ranked)
}

func exclude(found []NearbyDriver, drop []string) []NearbyDriver {
	if len(drop) == 0 {
		return found
	}
	blocked := make(map[string]struct{}, len(drop))
	for _, id := range drop {
		blocked[id] = struct{}{}
	}
	out := make([]NearbyDriver, 0, len(found))
	for _, d := range found {
		if _, ok := blocked[d.ID]; ok {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (s *Service) rank(ctx context.Context, req event.Requested, pickup geo.Point, candidates []NearbyDriver) ([]event.Candidate, string, error) {
	start := time.Now()
	origins := make([]geo.Point, len(candidates))
	for i, d := range candidates {
		origins[i] = geo.Point{Lat: d.Lat, Lng: d.Lng}
	}
	estimates, err := s.eta.Batch(ctx, req.TraceID, pickup, origins, s.radius)
	etaMS := time.Since(start).Milliseconds()
	if err != nil {
		s.log.WarnContext(ctx, "eta", "err", err, "trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
		ranked := s.haversine(pickup, candidates)
		s.fallback.Add(1)
		s.log.InfoContext(ctx, "matching.duration_ms",
			"stage", "eta", "duration_ms", etaMS, "engine", event.EngineHaversine,
			"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
		s.log.InfoContext(ctx, "matching.fallback",
			"fallback_total", s.fallback.Load(),
			"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
		s.logRank(ctx, req, ranked)
		return ranked, event.EngineHaversine, nil
	}

	byIndex := make(map[int]Estimate, len(estimates))
	for _, e := range estimates {
		byIndex[e.OriginIndex] = e
	}
	ranked := make([]event.Candidate, 0, len(candidates))
	dropped := 0
	for i, d := range candidates {
		e, ok := byIndex[i]
		if !ok || !e.Reachable {
			dropped++
			continue
		}
		ranked = append(ranked, event.Candidate{
			DriverID: d.ID, ETASeconds: e.DurationS, DistanceM: e.DistanceM,
			Location: event.Point{Lat: d.Lat, Lng: d.Lng},
		})
	}
	if dropped > 0 {
		s.unreachable.Add(int64(dropped))
		s.log.InfoContext(ctx, "matching.unreachable",
			"count", dropped, "unreachable_total", s.unreachable.Load(),
			"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
	}
	sortRanking(ranked)
	s.log.InfoContext(ctx, "matching.duration_ms",
		"stage", "eta", "duration_ms", etaMS, "engine", event.EngineDijkstra,
		"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
	s.logRank(ctx, req, ranked)
	return ranked, event.EngineDijkstra, nil
}

func (s *Service) haversine(pickup geo.Point, candidates []NearbyDriver) []event.Candidate {
	ranked := make([]event.Candidate, 0, len(candidates))
	for _, d := range candidates {
		est, err := geo.Fallback(geo.Point{Lat: d.Lat, Lng: d.Lng}, pickup, s.speed)
		if err != nil {
			continue
		}
		ranked = append(ranked, event.Candidate{
			DriverID: d.ID, ETASeconds: est.DurationS, DistanceM: est.DistanceM,
			Location: event.Point{Lat: d.Lat, Lng: d.Lng},
		})
	}
	sortRanking(ranked)
	return ranked
}

func sortRanking(ranked []event.Candidate) {
	slices.SortFunc(ranked, func(a, b event.Candidate) int {
		return cmp.Or(
			cmp.Compare(a.ETASeconds, b.ETASeconds),
			cmp.Compare(a.DistanceM, b.DistanceM),
			cmp.Compare(a.DriverID, b.DriverID),
		)
	})
}

func (s *Service) logRank(ctx context.Context, req event.Requested, ranked []event.Candidate) {
	start := time.Now()
	s.log.InfoContext(ctx, "matching.duration_ms",
		"stage", "rank", "duration_ms", time.Since(start).Milliseconds(), "count", len(ranked),
		"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
}

func (s *Service) publishMatched(ctx context.Context, req event.Requested, engine string, ranked []event.Candidate) error {
	id, err := newEventID()
	if err != nil {
		return fmt.Errorf("event id: %w", err)
	}
	body, err := event.Encode(event.Matched{
		EventID: id, TraceID: req.TraceID, RideID: req.RideID, Attempt: req.Attempt, Engine: engine, Ranking: ranked,
	})
	if err != nil {
		return err
	}
	if err := s.pub.Publish(ctx, event.TopicRidesMatched, req.RideID, body); err != nil {
		return err
	}
	s.mark(ctx, req)
	s.log.InfoContext(ctx, "rides.matched",
		"trace_id", req.TraceID, "event_id", id, "ride_id", req.RideID, "attempt", req.Attempt, "engine", engine, "ranking", len(ranked))
	return nil
}

func (s *Service) fail(ctx context.Context, req event.Requested, reason string) error {
	s.failed.Add(1)
	s.log.InfoContext(ctx, "matching.failed",
		"reason", reason, "failed_total", s.failed.Load(),
		"trace_id", req.TraceID, "event_id", req.EventID, "ride_id", req.RideID)
	id, err := newEventID()
	if err != nil {
		return fmt.Errorf("event id: %w", err)
	}
	body, err := event.Encode(event.MatchFailed{
		EventID: id, TraceID: req.TraceID, RideID: req.RideID, Attempt: req.Attempt, Reason: reason,
	})
	if err != nil {
		return err
	}
	if err := s.pub.Publish(ctx, event.TopicRidesMatchFailed, req.RideID, body); err != nil {
		return err
	}
	s.mark(ctx, req)
	return nil
}

func (s *Service) mark(ctx context.Context, req event.Requested) {
	if req.EventID == "" {
		return
	}
	if err := s.dedup.Mark(ctx, req.EventID); err != nil {
		s.log.WarnContext(ctx, "dedup", "err", err, "event_id", req.EventID, "trace_id", req.TraceID, "ride_id", req.RideID)
	}
}
