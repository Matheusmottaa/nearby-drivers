package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

const (
	vehicleClass   = "presentation"
	approachFor    = 12 * time.Second
	heartbeatEvery = 2 * time.Second
	// heartbeatWindow matches driver.HeartbeatWindow in internal/driver/slot.go.
	heartbeatWindow = 30 * time.Second
	logLimit        = 80
)

type car struct {
	role       string
	name       string
	node       int
	id         string
	homeLat    float64
	homeLng    float64
	lat        float64
	lng        float64
	status     string
	seq        int64
	lastBeat   time.Time
	frozen     bool
	dijkstraS  int
	dijkstraM  int
	haversineS int
	haversineM int
	path       []geo.Point
}

type trip struct {
	driverID string
	path     []geo.Point
	started  time.Time
	duration time.Duration
}

func (t trip) at(now time.Time) (lat, lng float64) {
	frac := 1.0
	if t.duration > 0 {
		frac = now.Sub(t.started).Seconds() / t.duration.Seconds()
	}
	return along(t.path, frac)
}

type rideView struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Status     string  `json:"status"`
	Engine     string  `json:"eta_engine,omitempty"`
	ETAS       int     `json:"eta_s,omitempty"`
	DistanceM  int     `json:"distance_m,omitempty"`
	DriverID   string  `json:"driver_id,omitempty"`
	DriverName string  `json:"driver_name,omitempty"`
	PickupLat  float64 `json:"pickup_lat"`
	PickupLng  float64 `json:"pickup_lng"`
}

type logLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

type demo struct {
	city    city
	cluster *cluster
	api     *client
	opened  []cast
	log     *slog.Logger
	session string
	rng     *rand.Rand

	sleep     func(context.Context, time.Duration) error
	pollEvery time.Duration
	now       func() time.Time
	root      context.Context

	mu          sync.Mutex
	cars        []car
	rides       []rideView
	rideIDs     []string
	trips       []trip
	logs        []logLine
	busy        string
	connected   bool
	clusterNow  clusterView
	loaded      bool
	breakerTill time.Time
}

func newDemo(city city, cl *cluster, api *client, log *slog.Logger, session string) *demo {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &demo{
		city: city, cluster: cl, api: api, opened: cloneCasts(city.actors), log: log, session: session,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		sleep:     sleepCtx,
		pollEvery: 500 * time.Millisecond,
		now:       time.Now,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *demo) bind(ctx context.Context) {
	d.mu.Lock()
	d.root = ctx
	d.mu.Unlock()
}

func (d *demo) setClient(api *client) {
	d.mu.Lock()
	d.api = api
	d.mu.Unlock()
}

func (d *demo) setConnected(ok bool) {
	d.mu.Lock()
	d.connected = ok
	d.mu.Unlock()
}

func (d *demo) client() *client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.api
}

func (d *demo) logf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	d.log.Info(text)
	d.mu.Lock()
	d.logs = append(d.logs, logLine{At: d.now(), Text: text})
	if len(d.logs) > logLimit {
		d.logs = d.logs[len(d.logs)-logLimit:]
	}
	d.mu.Unlock()
}

func (d *demo) begin(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy != "" {
		return false
	}
	d.busy = name
	return true
}

func (d *demo) end() {
	d.mu.Lock()
	d.busy = ""
	d.mu.Unlock()
}

func (d *demo) Start(ctx context.Context) {
	go d.heartbeatLoop(ctx)
	go d.clusterLoop(ctx)
	go d.probeLoop(ctx)
}

func (d *demo) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	var lastErr time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.beat(ctx); err != nil && time.Since(lastErr) > 15*time.Second {
				d.logf("Heartbeat: %v", err)
				lastErr = time.Now()
			}
		}
	}
}

func (d *demo) clusterLoop(ctx context.Context) {
	d.refreshCluster(ctx)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshCluster(ctx)
		}
	}
}

func (d *demo) refreshCluster(ctx context.Context) {
	if d.cluster == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	view := d.cluster.inspect(cctx)
	cancel()
	d.mu.Lock()
	d.clusterNow = view
	d.mu.Unlock()
}

func (d *demo) probeLoop(ctx context.Context) {
	d.probe(ctx)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.probe(ctx)
		}
	}
}

func (d *demo) probe(ctx context.Context) {
	ok := d.client().ready(ctx)
	d.mu.Lock()
	changed := d.connected != ok
	d.connected = ok
	d.mu.Unlock()
	if changed && ok {
		d.logf("ride-service e driver-service responderam.")
	}
}

func (d *demo) beat(ctx context.Context) error {
	api := d.client()
	if api == nil || api.driver == "" {
		return nil
	}
	now := d.now()
	d.mu.Lock()
	type fix struct {
		id   string
		name string
		body locationBody
	}
	var fixes []fix
	for i := range d.cars {
		c := &d.cars[i]
		if c.id == "" || c.frozen {
			continue
		}
		if trip, ok := d.tripOf(c.id); ok {
			c.lat, c.lng = trip.at(now)
		}
		seq := now.UnixMilli()
		if seq <= c.seq {
			seq = c.seq + 1
		}
		c.seq = seq
		c.lastBeat = now
		fixes = append(fixes, fix{
			id: c.id, name: c.name,
			body: locationBody{Lat: c.lat, Lng: c.lng, Seq: seq, ObservedAt: now.UTC(), AccuracyM: 5},
		})
	}
	d.mu.Unlock()
	var errs []error
	for _, f := range fixes {
		if err := api.putLocation(ctx, f.id, f.body); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
		}
	}
	return errors.Join(errs...)
}

func (d *demo) tripOf(id string) (trip, bool) {
	for _, t := range d.trips {
		if t.driverID == id {
			return t, true
		}
	}
	return trip{}, false
}

func (d *demo) needsRestore() bool {
	return d.cluster != nil && d.cluster.heldDown()
}
