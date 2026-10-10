package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/kdtree"
)

const (
	// MaxNearbyK is the largest k GET /drivers/nearby accepts.
	MaxNearbyK = 100
	// MaxRadiusM is the largest radius_m, in meters.
	MaxRadiusM = 50000

	rebuildEvery = time.Second
	sweepEvery   = time.Second
)

// Bus publishes driver events, replays drivers.location at boot and consumes
// reservation topics. This process is the only member of the reservation group.
type Bus interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
	ReplayLocations(ctx context.Context, apply func(event.Location) error) error
	ConsumeReservations(ctx context.Context, handle func(ctx context.Context, topic string, value []byte) error) error
}

// Fix is the body of PUT /drivers/{id}/location.
type Fix struct {
	Lat        float64
	Lng        float64
	Seq        int64
	ObservedAt time.Time
	AccuracyM  float64
}

func (f Fix) validate() error {
	if err := (geo.Point{Lat: f.Lat, Lng: f.Lng}).Validate(); err != nil {
		return err
	}
	if f.Seq < 0 {
		return invalidInput("seq must be >= 0")
	}
	if f.ObservedAt.IsZero() {
		return invalidInput("observed_at is required")
	}
	if math.IsNaN(f.AccuracyM) || math.IsInf(f.AccuracyM, 0) || f.AccuracyM < 0 {
		return invalidInput("accuracy_m must be >= 0")
	}
	return nil
}

func (r Registration) validate() error {
	if r.Name == "" {
		return invalidInput("name is required")
	}
	if r.Phone == "" {
		return invalidInput("phone is required")
	}
	if r.VehicleClass == "" {
		return invalidInput("vehicle_class is required")
	}
	if r.Plate == "" {
		return invalidInput("plate is required")
	}
	return nil
}

// NearbyDriver is one candidate from the spatial index.
type NearbyDriver struct {
	ID            string
	Lat           float64
	Lng           float64
	StraightLineM float64
	VehicleClass  string
}

// Nearby is the answer of a nearby query.
type Nearby struct {
	Drivers     []NearbyDriver
	SnapshotAge time.Duration
}

// Service is the driver registry and the in-memory position index.
// One process owns the index. Boot replays drivers.location before Ready is true.
type Service struct {
	log   *slog.Logger
	store Store
	cache Cache
	bus   Bus

	slots *table
	index *kdtree.Index

	ready       atomic.Bool
	publishedAt atomic.Int64
	staleSeq    atomic.Int64

	now func() time.Time

	loopWG sync.WaitGroup
	pubWG  sync.WaitGroup
}

// New returns a service. Ready stays false until Boot publishes the first snapshot.
func New(log *slog.Logger, store Store, cache Cache, bus Bus) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		log:   log,
		store: store,
		cache: cache,
		bus:   bus,
		slots: newTable(),
		index: kdtree.NewIndex(),
		now:   time.Now,
	}
}

// Ready reports whether the first snapshot has been published.
func (s *Service) Ready() bool { return s.ready.Load() }

// Slot returns a copy of the in-memory slot.
func (s *Service) Slot(id string) (Slot, bool) { return s.slots.Get(id) }

// Boot loads registrations, replays the location window and publishes the first snapshot.
// /readyz stays closed until this returns nil.
func (s *Service) Boot(ctx context.Context) error {
	start := time.Now()
	rows, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	s.slots.Load(rows)

	var replayed int
	if err := s.bus.ReplayLocations(ctx, func(loc event.Location) error {
		if s.applyReplay(loc) {
			replayed++
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.PublishSnapshot(ctx); err != nil {
		return err
	}
	s.log.Info("index ready",
		"drivers", len(rows),
		"replayed", replayed,
		"replay_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

func (s *Service) applyReplay(loc event.Location) bool {
	if err := (geo.Point{Lat: loc.Lat, Lng: loc.Lng}).Validate(); err != nil {
		s.log.Warn("replay location", "err", err, "driver_id", loc.DriverID, "seq", loc.Seq)
		return false
	}
	if loc.ObservedAt.IsZero() || loc.Seq < 0 {
		return false
	}
	var applied bool
	_, ok := s.slots.Update(loc.DriverID, func(sl *Slot) {
		applied = sl.ApplyLocation(loc.Lat, loc.Lng, loc.Seq, loc.ObservedAt)
	})
	return ok && applied
}

// PublishSnapshot rebuilds the KD-Tree from slot positions and the declared
// status in the database, then drops delta updates already included.
func (s *Service) PublishSnapshot(ctx context.Context) error {
	upTo := s.index.Version()
	rows, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	s.index.Publish(s.slots.Entries(rows, now), upTo)
	s.publishedAt.Store(now.UnixNano())
	s.ready.Store(true)
	return nil
}

// Loop runs the snapshot rebuild, the lost sweeper and the reservation consumer
// until ctx is canceled.
func (s *Service) Loop(ctx context.Context) {
	s.loopWG.Add(3)
	go func() {
		defer s.loopWG.Done()
		s.rebuild(ctx)
	}()
	go func() {
		defer s.loopWG.Done()
		s.sweepLoop(ctx)
	}()
	go func() {
		defer s.loopWG.Done()
		s.consume(ctx)
	}()
}

// Wait blocks until Loop and in-flight location publishes have finished.
func (s *Service) Wait() {
	s.loopWG.Wait()
	s.pubWG.Wait()
}

func (s *Service) rebuild(ctx context.Context) {
	ticker := time.NewTicker(rebuildEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.PublishSnapshot(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("rebuild snapshot", "err", err)
			}
		}
	}
}

func (s *Service) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}

func (s *Service) consume(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.bus.ConsumeReservations(ctx, s.onReservation)
		if ctx.Err() != nil {
			return
		}
		s.log.Error("consume reservations", "err", err)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Create registers a driver. The same Idempotency-Key returns the original row
// and does not allocate another internal_id. created is false in that case.
func (s *Service) Create(ctx context.Context, idempotencyKey string, in Registration) (Driver, bool, error) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return Driver{}, false, ErrIdempotencyKey
	}
	if len(key) > 200 {
		return Driver{}, false, invalidInput("Idempotency-Key is too long")
	}
	in = Registration{
		Name:         strings.TrimSpace(in.Name),
		Phone:        strings.TrimSpace(in.Phone),
		VehicleClass: strings.TrimSpace(in.VehicleClass),
		Plate:        strings.TrimSpace(in.Plate),
	}
	if err := in.validate(); err != nil {
		return Driver{}, false, err
	}
	d, created, err := s.store.Create(ctx, key, in)
	if err != nil {
		return Driver{}, false, err
	}
	if created {
		s.slots.Insert(d)
	}
	return d, created, nil
}

// Get returns the registration. It does not include the GPS trail.
func (s *Service) Get(ctx context.Context, id string) (Driver, error) {
	return s.store.Get(ctx, id)
}

// PatchStatus changes the declared status. ON_TRIP is not a PATCH transition.
func (s *Service) PatchStatus(ctx context.Context, id, status string) (Driver, error) {
	if !validUUID(id) {
		return Driver{}, ErrNotFound
	}
	switch status {
	case StatusOffline, StatusAvailable, StatusSuspended, StatusPending, StatusOnTrip:
	default:
		return Driver{}, invalidInput("unknown status")
	}
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return Driver{}, err
	}
	if !CanTransit(d.Status, status) {
		return Driver{}, ErrIllegalStatus
	}
	if d.Status != status {
		if err := s.store.UpdateStatus(ctx, id, status); err != nil {
			return Driver{}, err
		}
	}
	d.Status = status
	if sl, ok := s.slots.Update(id, func(sl *Slot) {
		sl.Available = status == StatusAvailable
	}); ok {
		s.project(sl)
	}
	return d, nil
}

// PutLocation writes the slot when seq is newer, updates the last-position
// cache and returns. The drivers.location publish runs after that, off the
// request. A stale seq leaves the slot unchanged and still succeeds.
func (s *Service) PutLocation(ctx context.Context, id string, fix Fix, traceID string) error {
	if !validUUID(id) {
		return ErrNotFound
	}
	if err := fix.validate(); err != nil {
		return err
	}
	var applied bool
	sl, ok := s.slots.Update(id, func(sl *Slot) {
		applied = sl.ApplyLocation(fix.Lat, fix.Lng, fix.Seq, fix.ObservedAt)
	})
	if !ok {
		return ErrNotFound
	}
	if !applied {
		n := s.staleSeq.Add(1)
		s.log.Info("location", "trace_id", traceID, "driver_id", id, "seq", fix.Seq, "stale", true, "stale_seq_total", n)
		return nil
	}
	if err := s.cache.Put(ctx, id, Position{Lat: fix.Lat, Lng: fix.Lng, Seq: fix.Seq, At: fix.ObservedAt}); err != nil {
		s.log.Error("last position", "err", err, "trace_id", traceID, "driver_id", id, "seq", fix.Seq)
	}
	s.project(sl)
	s.asyncLocation(event.Location{
		EventID:    s.eventID(),
		TraceID:    traceID,
		DriverID:   id,
		Lat:        fix.Lat,
		Lng:        fix.Lng,
		Seq:        fix.Seq,
		ObservedAt: fix.ObservedAt,
		AccuracyM:  fix.AccuracyM,
	})
	s.log.Info("location", "trace_id", traceID, "driver_id", id, "seq", fix.Seq, "stale", false)
	return nil
}

func (s *Service) asyncLocation(loc event.Location) {
	raw, err := event.Encode(loc)
	if err != nil {
		s.log.Error("encode location", "err", err, "driver_id", loc.DriverID, "seq", loc.Seq)
		return
	}
	s.pubWG.Add(1)
	go func() {
		defer s.pubWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.bus.Publish(ctx, event.TopicDriversLocation, loc.DriverID, raw); err != nil {
			s.log.Error("publish location",
				"err", err,
				"trace_id", loc.TraceID,
				"driver_id", loc.DriverID,
				"seq", loc.Seq,
			)
		}
	}()
}

// Nearby returns up to k available drivers of class within radiusM, ordered by
// straight-line distance. An empty result is not an error. class empty matches any class.
func (s *Service) Nearby(p geo.Point, radiusM float64, k int, class string) (Nearby, error) {
	if !s.ready.Load() {
		return Nearby{}, ErrIndexNotReady
	}
	if err := p.Validate(); err != nil {
		return Nearby{}, err
	}
	if k < 1 || k > MaxNearbyK {
		return Nearby{}, invalidInput(fmt.Sprintf("k must be between 1 and %d", MaxNearbyK))
	}
	if math.IsNaN(radiusM) || math.IsInf(radiusM, 0) || radiusM < 0 || radiusM > MaxRadiusM {
		return Nearby{}, invalidInput(fmt.Sprintf("radius_m must be between 0 and %d", MaxRadiusM))
	}
	rs, err := s.index.Nearby(p, radiusM, k, class)
	if err != nil {
		if errors.Is(err, kdtree.ErrNotReady) {
			return Nearby{}, ErrIndexNotReady
		}
		return Nearby{}, err
	}
	out := make([]NearbyDriver, 0, len(rs))
	for _, r := range rs {
		sl, ok := s.slots.ByInternal(r.ID)
		if !ok {
			continue
		}
		out = append(out, NearbyDriver{
			ID:            sl.DriverID,
			Lat:           r.Point.Lat,
			Lng:           r.Point.Lng,
			StraightLineM: r.StraightLineM,
			VehicleClass:  r.Class,
		})
	}
	return Nearby{Drivers: out, SnapshotAge: s.snapshotAge()}, nil
}

func (s *Service) snapshotAge() time.Duration {
	published := time.Unix(0, s.publishedAt.Load())
	age := s.now().Sub(published)
	if age < 0 {
		return 0
	}
	return age
}

// Assign applies rides.assigned: the slot bit and the conditional ON_TRIP update.
// A repeat of the same ride is a no-op. A different current ride is left alone.
func (s *Service) Assign(ctx context.Context, driverID, rideID string) error {
	res, err := s.store.Assign(ctx, driverID, rideID)
	if err != nil {
		return err
	}
	if res != AssignApplied && res != AssignSame {
		return nil
	}
	sl, ok := s.slots.Update(driverID, func(sl *Slot) {
		sl.Assign(rideID)
	})
	if ok {
		s.project(sl)
	}
	if res == AssignApplied {
		s.log.Info("assigned", "driver_id", driverID, "ride_id", rideID)
	}
	return nil
}

// Release applies rides.released when rideID is the current reservation.
func (s *Service) Release(ctx context.Context, driverID, rideID string) error {
	released, err := s.store.Release(ctx, driverID, rideID)
	if err != nil {
		return err
	}
	if !released {
		return nil
	}
	sl, ok := s.slots.Update(driverID, func(sl *Slot) {
		if !sl.Release(rideID) {
			sl.Reserved = false
			sl.RideID = ""
			sl.Lost = false
			sl.Available = true
		}
	})
	if ok {
		s.project(sl)
	}
	s.log.Info("released", "driver_id", driverID, "ride_id", rideID)
	return nil
}

// Sweep publishes drivers.presence lost once for each reserved driver whose
// heartbeat has expired. Available drivers are not lost; they leave the index
// when the heartbeat bit goes stale.
func (s *Service) Sweep(ctx context.Context) int {
	var n int
	for _, item := range s.slots.DueLost(s.now()) {
		if err := s.publishLost(ctx, item); err != nil {
			s.log.Error("lost", "err", err, "driver_id", item.DriverID, "ride_id", item.RideID)
			continue
		}
		s.slots.Update(item.DriverID, func(sl *Slot) {
			if sl.Reserved && sl.RideID == item.RideID {
				sl.MarkLost()
			}
		})
		n++
		s.log.Info("lost", "driver_id", item.DriverID, "ride_id", item.RideID)
	}
	return n
}

func (s *Service) publishLost(ctx context.Context, item dueReservation) error {
	raw, err := event.Encode(event.Presence{
		EventID:  s.eventID(),
		TraceID:  s.eventID(),
		DriverID: item.DriverID,
		Lost:     event.Lost{RideID: item.RideID},
	})
	if err != nil {
		return err
	}
	return s.bus.Publish(ctx, event.TopicDriversPresence, item.DriverID, raw)
}

func (s *Service) onReservation(ctx context.Context, topic string, value []byte) error {
	switch topic {
	case event.TopicRidesAssigned:
		ev, err := event.Decode[event.Assigned](value)
		if err != nil {
			s.log.Error("assigned", "err", err)
			return nil
		}
		return s.Assign(ctx, ev.DriverID, ev.RideID)
	case event.TopicRidesReleased:
		ev, err := event.Decode[event.Released](value)
		if err != nil {
			s.log.Error("released", "err", err)
			return nil
		}
		return s.Release(ctx, ev.DriverID, ev.RideID)
	default:
		return nil
	}
}

func (s *Service) project(sl Slot) {
	s.index.Apply(kdtree.Entry{
		ID:        sl.InternalID,
		Point:     geo.Point{Lat: sl.Lat, Lng: sl.Lng},
		Class:     sl.Class,
		Available: sl.Eligible(s.now()),
	})
}

func (s *Service) eventID() string {
	id, err := newID()
	if err != nil {
		return fmt.Sprintf("id-%d", s.now().UnixNano())
	}
	return id
}
