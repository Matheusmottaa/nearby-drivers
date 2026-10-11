package ride

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

const publishEvery = 200 * time.Millisecond

// Service owns ride creation, assignment and cancellation.
// One process runs the publisher and the ride-workers consumer.
type Service struct {
	log         *slog.Logger
	store       Store
	cache       Cache
	bus         Bus
	maxAttempts int
	now         func() time.Time

	conflicts atomic.Int64
	exhausted atomic.Int64
	rematches atomic.Int64
	transMu   sync.Mutex
	trans     map[string]int64

	loopWG sync.WaitGroup
}

// New returns a service. maxAttempts below 1 uses DefaultMaxAttempts.
func New(log *slog.Logger, store Store, cache Cache, bus Bus, maxAttempts int) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if cache == nil {
		cache = downCache{}
	}
	return &Service{
		log:         log,
		store:       store,
		cache:       cache,
		bus:         bus,
		maxAttempts: checkAttempt(maxAttempts),
		now:         time.Now,
		trans:       make(map[string]int64),
	}
}

// ReservationConflicts is the ride.reservation_conflict counter.
func (s *Service) ReservationConflicts() int64 { return s.conflicts.Load() }

// RankingExhausted is the ride.ranking_exhausted counter.
func (s *Service) RankingExhausted() int64 { return s.exhausted.Load() }

// Rematches is the ride.rematch counter.
func (s *Service) Rematches() int64 { return s.rematches.Load() }

// Transitions is the ride.status_transitions counter for from→to.
func (s *Service) Transitions(from, to string) int64 {
	s.transMu.Lock()
	defer s.transMu.Unlock()
	return s.trans[describe(from, to)]
}

// Loop runs the outbox publisher and the single consumer until ctx is canceled.
func (s *Service) Loop(ctx context.Context) {
	s.loopWG.Add(2)
	go func() {
		defer s.loopWG.Done()
		s.publishLoop(ctx)
	}()
	go func() {
		defer s.loopWG.Done()
		s.consume(ctx)
	}()
}

// Wait blocks until Loop has finished.
func (s *Service) Wait() { s.loopWG.Wait() }

func (s *Service) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(publishEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.PublishPending(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("outbox", "err", err)
			}
		}
	}
}

func (s *Service) consume(ctx context.Context) {
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
}

// Create stores a REQUESTED ride and its rides.requested outbox row.
// created is false when the idempotency key already belongs to the same body.
func (s *Service) Create(ctx context.Context, idempotencyKey string, raw []byte, in Request) (Ride, bool, error) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return Ride{}, false, ErrIdempotencyKey
	}
	if len(key) > 200 {
		return Ride{}, false, invalidInput("Idempotency-Key is too long")
	}
	if err := in.validate(); err != nil {
		return Ride{}, false, err
	}
	sum := sha256.Sum256(raw)
	hash := sum[:]

	if id, cached, ok, err := s.lookupIdem(ctx, key, hash); err != nil {
		return Ride{}, false, err
	} else if ok {
		ride, err := s.store.Get(ctx, id)
		if err != nil {
			return Ride{}, false, err
		}
		if cached {
			return ride, false, nil
		}
	}

	id, err := newID()
	if err != nil {
		return Ride{}, false, err
	}
	ride, err := rideOf(in, id, s.now)
	if err != nil {
		return Ride{}, false, err
	}
	ev, err := newRequested(ride)
	if err != nil {
		return Ride{}, false, err
	}
	stored, created, err := s.store.InsertNew(ctx, key, hash, ride, ev)
	if err != nil {
		return Ride{}, false, err
	}
	if created {
		s.log.InfoContext(ctx, "ride.status_transitions",
			"from", "", "to", StatusRequested, "ride_id", stored.ID, "trace_id", stored.TraceID, "event_id", ev.EventID)
		s.countTransition("", StatusRequested)
	}
	if err := s.cache.Remember(ctx, key, hash, stored.ID); err != nil {
		s.log.WarnContext(ctx, "idempotency cache", "err", err, "ride_id", stored.ID, "trace_id", stored.TraceID)
	}
	return stored, created, nil
}

// lookupIdem reports a hit. A Redis error falls through to the table.
// A hash mismatch is ErrIdempotencyConflict. ok with a ride id means replay.
func (s *Service) lookupIdem(ctx context.Context, key string, hash []byte) (string, bool, bool, error) {
	got, id, ok, err := s.cache.Lookup(ctx, key)
	if err != nil {
		s.log.WarnContext(ctx, "idempotency cache", "err", err)
	} else if ok {
		if !bytesEqual(got, hash) {
			return "", false, false, ErrIdempotencyConflict
		}
		return id, true, true, nil
	}
	return "", false, false, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Get returns the current ride.
func (s *Service) Get(ctx context.Context, id string) (Ride, error) {
	return s.store.Get(ctx, id)
}

// Await polls the ride row until it leaves its current version or wait elapses.
// wait is capped at MaxWait. A ride that is not REQUESTED or MATCHING returns immediately.
func (s *Service) Await(ctx context.Context, id string, wait time.Duration) (Ride, error) {
	ride, err := s.store.Get(ctx, id)
	if err != nil || wait <= 0 || !ride.Pending() {
		return ride, err
	}
	if wait > MaxWait {
		wait = MaxWait
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	version := ride.Version
	for {
		select {
		case <-ctx.Done():
			return Ride{}, ctx.Err()
		case <-deadline.C:
			return s.store.Get(ctx, id)
		case <-tick.C:
			cur, err := s.store.Get(ctx, id)
			if err != nil {
				return Ride{}, err
			}
			if cur.Version != version {
				return cur, nil
			}
		}
	}
}

// Stream emits the current ride and each later version until ctx is canceled.
func (s *Service) Stream(ctx context.Context, id string, emit func(Ride) error) error {
	ride, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := emit(ride); err != nil {
		return err
	}
	version := ride.Version
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			cur, err := s.store.Get(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if cur.Version == version {
				continue
			}
			if err := emit(cur); err != nil {
				return err
			}
			version = cur.Version
		}
	}
}

// Cancel moves the ride to CANCELLED. Repeating a cancelled ride returns it unchanged.
// An assigned driver produces rides.released.
func (s *Service) Cancel(ctx context.Context, id, reason string) (Ride, error) {
	if !validUUID(id) {
		return Ride{}, ErrNotFound
	}
	var stored Ride
	err := s.retry(ctx, id, func(ride Ride) (Plan, error) {
		stored = ride
		return planCancel(ride, reason)
	})
	if errors.Is(err, errIgnored) {
		return stored, nil
	}
	if err != nil {
		return Ride{}, err
	}
	return s.store.Get(ctx, id)
}

// Handle dispatches one worker record. A payload that cannot be decoded is acked.
func (s *Service) Handle(ctx context.Context, topic string, value []byte) error {
	switch topic {
	case event.TopicRidesMatched:
		m, err := event.Decode[event.Matched](value)
		if err != nil {
			s.log.WarnContext(ctx, "decode", "err", err, "topic", topic)
			return nil
		}
		return s.ApplyMatched(ctx, m)
	case event.TopicRidesMatchFailed:
		m, err := event.Decode[event.MatchFailed](value)
		if err != nil {
			s.log.WarnContext(ctx, "decode", "err", err, "topic", topic)
			return nil
		}
		return s.ApplyMatchFailed(ctx, m)
	case event.TopicDriversPresence:
		p, err := event.Decode[event.Presence](value)
		if err != nil {
			s.log.WarnContext(ctx, "decode", "err", err, "topic", topic)
			return nil
		}
		return s.ApplyLost(ctx, p)
	default:
		return nil
	}
}

// ApplyMatched reserves the first free driver of the current attempt.
// An older attempt, a repeated delivery, or a ride that can no longer be
// assigned is acked without a write.
func (s *Service) ApplyMatched(ctx context.Context, m event.Matched) error {
	if !validUUID(m.RideID) {
		s.log.WarnContext(ctx, "matched", "ride_id", m.RideID, "event_id", m.EventID)
		return nil
	}
	blocked := make(map[string]struct{})
	for range 16 {
		ride, err := s.store.Get(ctx, m.RideID)
		if errors.Is(err, ErrNotFound) {
			s.log.WarnContext(ctx, "matched ride missing", "ride_id", m.RideID, "event_id", m.EventID, "trace_id", m.TraceID)
			return nil
		}
		if err != nil {
			return err
		}
		plan, err := planMatch(ride, m, blocked)
		if err != nil {
			return err
		}
		if plan.Ignore {
			return nil
		}
		held := false
		if plan.AssignDriver != "" {
			proceed, own, err := s.reserve(ctx, plan.AssignDriver, ride.ID)
			if err != nil {
				return err
			}
			if !proceed {
				s.noteConflict(ride, plan.AssignDriver, m.EventID)
				blocked[plan.AssignDriver] = struct{}{}
				continue
			}
			held = own
		}
		err = s.store.Commit(ctx, ride.Version, plan.Next, plan.Events)
		if held && err != nil {
			if relErr := s.cache.Release(ctx, plan.AssignDriver, ride.ID); relErr != nil {
				s.log.WarnContext(ctx, "release reservation", "err", relErr, "ride_id", ride.ID, "driver_id", plan.AssignDriver)
			}
		}
		if errors.Is(err, ErrDriverTaken) {
			s.noteConflict(ride, plan.AssignDriver, m.EventID)
			blocked[plan.AssignDriver] = struct{}{}
			continue
		}
		if errors.Is(err, ErrVersionConflict) {
			continue
		}
		if err != nil {
			return err
		}
		s.after(ctx, ride, plan)
		return nil
	}
	return fmt.Errorf("match ride %s: too many conflicts", m.RideID)
}

// ApplyMatchFailed moves the current attempt to NO_DRIVER_AVAILABLE.
func (s *Service) ApplyMatchFailed(ctx context.Context, m event.MatchFailed) error {
	if !validUUID(m.RideID) {
		return nil
	}
	err := s.retry(ctx, m.RideID, func(ride Ride) (Plan, error) {
		return planMatchFailed(ride, m), nil
	})
	if errors.Is(err, errIgnored) {
		return nil
	}
	return err
}

// ApplyLost releases the assigned driver. A remaining attempt republishes
// rides.requested. A second lost for the same ride is acked without a write.
func (s *Service) ApplyLost(ctx context.Context, p event.Presence) error {
	if !validUUID(p.Lost.RideID) {
		return nil
	}
	err := s.retry(ctx, p.Lost.RideID, func(ride Ride) (Plan, error) {
		return planLost(ride, p.DriverID, p.Lost.RideID, s.maxAttempts)
	})
	if errors.Is(err, errIgnored) {
		return nil
	}
	return err
}

var errIgnored = errors.New("ignored")

func (s *Service) retry(ctx context.Context, id string, build func(Ride) (Plan, error)) error {
	var missing bool
	for range 8 {
		ride, err := s.store.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			missing = true
			break
		}
		if err != nil {
			return err
		}
		plan, err := build(ride)
		if err != nil {
			return err
		}
		if plan.Ignore {
			return errIgnored
		}
		if plan.Reject {
			return ErrNotCancellable
		}
		err = s.store.Commit(ctx, ride.Version, plan.Next, plan.Events)
		if errors.Is(err, ErrVersionConflict) {
			continue
		}
		if err != nil {
			return err
		}
		s.after(ctx, ride, plan)
		return nil
	}
	if missing {
		s.log.WarnContext(ctx, "ride missing", "ride_id", id)
		return nil
	}
	return fmt.Errorf("ride %s: version conflict", id)
}

// PublishPending sends unpublished outbox rows. A crash before the row is
// marked sends the same event_id on the next pass. Publishing rides.requested
// moves REQUESTED to MATCHING.
func (s *Service) PublishPending(ctx context.Context) error {
	became, err := s.store.PublishPending(ctx, 32, func(row PendingPublish) error {
		lag := s.now().Sub(row.CreatedAt).Milliseconds()
		s.log.InfoContext(ctx, "outbox.publish_lag_ms",
			"subject", row.Subject, "ride_id", row.RideID, "event_id", row.EventID, "lag_ms", lag)
		return s.bus.Publish(ctx, row.Subject, row.RideID, row.Payload)
	})
	if pending, lag, serr := s.store.OutboxStats(ctx); serr == nil && (pending > 0 || len(became) > 0) {
		s.log.InfoContext(ctx, "outbox.pending", "pending", pending, "outbox.publish_lag_ms", lag.Milliseconds())
	}
	for _, id := range became {
		s.countTransition(StatusRequested, StatusMatching)
		s.log.InfoContext(ctx, "ride.status_transitions",
			"from", StatusRequested, "to", StatusMatching, "ride_id", id)
	}
	return err
}

func (s *Service) reserve(ctx context.Context, driverID, rideID string) (proceed, held bool, err error) {
	ok, err := s.cache.TryReserve(ctx, driverID, rideID)
	if err != nil {
		s.log.WarnContext(ctx, "reservation cache", "err", err, "driver_id", driverID, "ride_id", rideID)
		return true, false, nil
	}
	return ok, ok, nil
}

func (s *Service) noteConflict(ride Ride, driverID, eventID string) {
	s.conflicts.Add(1)
	s.log.Info("ride.reservation_conflict",
		"ride_id", ride.ID, "driver_id", driverID, "trace_id", ride.TraceID, "event_id", eventID)
}

func (s *Service) after(ctx context.Context, from Ride, plan Plan) {
	if plan.Exhausted {
		s.exhausted.Add(1)
		s.log.InfoContext(ctx, "ride.ranking_exhausted", "ride_id", from.ID, "trace_id", from.TraceID)
	}
	if plan.Rematch {
		s.rematches.Add(1)
		s.log.InfoContext(ctx, "ride.rematch", "ride_id", from.ID, "trace_id", from.TraceID, "attempt", plan.Next.Attempt)
	}
	if from.Status != plan.Next.Status {
		s.countTransition(from.Status, plan.Next.Status)
		eventID := ""
		if len(plan.Events) > 0 {
			eventID = plan.Events[0].EventID
		}
		s.log.InfoContext(ctx, "ride.status_transitions",
			"from", from.Status, "to", plan.Next.Status, "ride_id", from.ID, "trace_id", from.TraceID, "event_id", eventID)
	}
	if plan.Next.Status == StatusAssigned {
		s.log.InfoContext(ctx, "ride.time_to_assign_ms",
			"ride_id", from.ID, "trace_id", from.TraceID, "ms", s.now().Sub(from.RequestedAt).Milliseconds())
	}
	if plan.ReleaseDriver != "" {
		if err := s.cache.Release(ctx, plan.ReleaseDriver, from.ID); err != nil {
			s.log.WarnContext(ctx, "release reservation", "err", err, "ride_id", from.ID, "driver_id", plan.ReleaseDriver)
		}
	}
}

func (s *Service) countTransition(from, to string) {
	s.transMu.Lock()
	s.trans[describe(from, to)]++
	s.transMu.Unlock()
}

// downCache reports Redis as unavailable so the database remains the guarantee.
type downCache struct{}

func (downCache) Lookup(context.Context, string) ([]byte, string, bool, error) {
	return nil, "", false, errCacheDown
}
func (downCache) Remember(context.Context, string, []byte, string) error { return errCacheDown }
func (downCache) TryReserve(context.Context, string, string) (bool, error) {
	return false, errCacheDown
}
func (downCache) Release(context.Context, string, string) error { return errCacheDown }
