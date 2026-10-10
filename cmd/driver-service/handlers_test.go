package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/driver"
	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

const (
	rideA = "018f9c00-3333-7abc-8def-0123456789ab"
	rideB = "018f9c00-4444-7abc-8def-0123456789ab"
)

func TestCreateIdempotencyAndGet(t *testing.T) {
	_, gate, svc := readyHarness(t)
	body := `{"name":"Ana","phone":"11999999999","vehicle_class":"standard","plate":"ABC1D23"}`

	rec := do(gate, http.MethodPost, "/drivers", body, "idem-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var created driverJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.InternalID != 0 || created.Status != driver.StatusPending || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(rec.Body.String(), `"lat"`) || strings.Contains(rec.Body.String(), `"lng"`) {
		t.Fatalf("registration includes a GPS trail: %s", rec.Body)
	}

	again := do(gate, http.MethodPost, "/drivers", body, "idem-1")
	if again.Code != http.StatusOK {
		t.Fatalf("idempotent = %d %s", again.Code, again.Body)
	}
	var same driverJSON
	if err := json.Unmarshal(again.Body.Bytes(), &same); err != nil {
		t.Fatal(err)
	}
	if same.ID != created.ID || same.InternalID != created.InternalID {
		t.Fatalf("idempotent driver = %+v, want %+v", same, created)
	}

	other := do(gate, http.MethodPost, "/drivers", `{"name":"Bia","phone":"11888888888","vehicle_class":"comfort","plate":"XYZ9Z99"}`, "idem-2")
	if other.Code != http.StatusCreated {
		t.Fatalf("second = %d %s", other.Code, other.Body)
	}
	var second driverJSON
	if err := json.Unmarshal(other.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.InternalID != 1 || second.ID == created.ID {
		t.Fatalf("second = %+v", second)
	}

	got := do(gate, http.MethodGet, "/drivers/"+created.ID, "", "")
	if got.Code != http.StatusOK {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), `"lat"`) {
		t.Fatalf("get includes a GPS trail: %s", got.Body)
	}

	assertProblem(t, do(gate, http.MethodPost, "/drivers", body, ""), http.StatusBadRequest)
	assertProblem(t, do(gate, http.MethodPost, "/drivers", `{"name":"C","phone":"1","vehicle_class":"standard","plate":"ABC1D23"}`, "idem-3"), http.StatusConflict)
	assertProblem(t, do(gate, http.MethodGet, "/drivers/not-a-uuid", "", ""), http.StatusNotFound)

	patched := do(gate, http.MethodPatch, "/drivers/"+created.ID+"/status", `{"status":"AVAILABLE"}`, "")
	if patched.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", patched.Code, patched.Body)
	}
	assertProblem(t, do(gate, http.MethodPatch, "/drivers/"+created.ID+"/status", `{"status":"ON_TRIP"}`, ""), http.StatusConflict)
	assertProblem(t, do(gate, http.MethodPatch, "/drivers/"+created.ID+"/status", `{"status":"PENDING"}`, ""), http.StatusConflict)

	if err := svc.Assign(t.Context(), created.ID, rideA); err != nil {
		t.Fatal(err)
	}
	assertProblem(t, do(gate, http.MethodPatch, "/drivers/"+created.ID+"/status", `{"status":"OFFLINE"}`, ""), http.StatusConflict)
	onTrip := do(gate, http.MethodGet, "/drivers/"+created.ID, "", "")
	if !strings.Contains(onTrip.Body.String(), `"status":"ON_TRIP"`) || !strings.Contains(onTrip.Body.String(), rideA) {
		t.Fatalf("assigned get = %s", onTrip.Body)
	}
}

func TestNearbyNotReadyEmptyAndOrdered(t *testing.T) {
	_, gate, svc, cache := harness(t)
	pickup := geo.Point{Lat: -23.5613, Lng: -46.6565}

	if rec := do(gate, http.MethodGet, "/readyz", "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before snapshot = %d", rec.Code)
	}
	assertProblem(t, do(gate, http.MethodGet, nearbyQuery(pickup, 10, 3000, ""), "", ""), http.StatusServiceUnavailable)

	if err := svc.PublishSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate.SetReady(true)

	empty := do(gate, http.MethodGet, nearbyQuery(pickup, 10, 3000, ""), "", "")
	if empty.Code != http.StatusOK {
		t.Fatalf("empty = %d %s", empty.Code, empty.Body)
	}
	var none nearbyJSON
	if err := json.Unmarshal(empty.Body.Bytes(), &none); err != nil {
		t.Fatal(err)
	}
	if none.Drivers == nil || len(none.Drivers) != 0 || none.Index != "kdtree" || none.SnapshotAgeMS < 0 {
		t.Fatalf("empty nearby = %+v", none)
	}
	if !strings.Contains(empty.Body.String(), `"drivers":[]`) {
		t.Fatalf("empty list encoded as %s", empty.Body)
	}

	near := register(t, gate, "Near", "AAA0001", "standard")
	far := register(t, gate, "Far", "BBB0002", "standard")
	comfort := register(t, gate, "Comfort", "CCC0003", "comfort")
	offline := register(t, gate, "Off", "DDD0004", "standard")
	for _, id := range []string{near, far, comfort} {
		if rec := do(gate, http.MethodPatch, "/drivers/"+id+"/status", `{"status":"AVAILABLE"}`, ""); rec.Code != http.StatusOK {
			t.Fatalf("patch %s = %d %s", id, rec.Code, rec.Body)
		}
	}
	now := time.Now().UTC()
	putLoc(t, gate, near, north(pickup, 200), 1, now)
	putLoc(t, gate, far, north(pickup, 800), 1, now)
	putLoc(t, gate, comfort, north(pickup, 100), 1, now)
	putLoc(t, gate, offline, north(pickup, 50), 1, now.Add(-31*time.Second))
	if do(gate, http.MethodPatch, "/drivers/"+offline+"/status", `{"status":"AVAILABLE"}`, "").Code != http.StatusOK {
		t.Fatal("patch offline")
	}
	// The stale fix was written while the driver was still PENDING, so the
	// delta said unavailable. Declare AVAILABLE and publish the heartbeat again
	// at the same old instant with a newer seq: the slot is fresh only if the
	// heartbeat is inside 30s.
	putLoc(t, gate, offline, north(pickup, 50), 2, now.Add(-31*time.Second))

	writes := cache.n()
	rec := do(gate, http.MethodGet, nearbyQuery(pickup, 10, 3000, "standard"), "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("nearby = %d %s", rec.Code, rec.Body)
	}
	var got nearbyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Drivers) != 2 || got.Drivers[0].ID != near || got.Drivers[1].ID != far {
		t.Fatalf("nearby = %+v, want near then far", got.Drivers)
	}
	if got.Drivers[0].StraightLineM >= got.Drivers[1].StraightLineM {
		t.Fatalf("not ordered by straight_line_m: %+v", got.Drivers)
	}
	if cache.n() != writes {
		t.Fatal("nearby touched the last-position cache")
	}

	if err := svc.Assign(t.Context(), near, rideA); err != nil {
		t.Fatal(err)
	}
	onlyFar := decodeNearby(t, do(gate, http.MethodGet, nearbyQuery(pickup, 10, 3000, "standard"), "", ""))
	if len(onlyFar) != 1 || onlyFar[0].ID != far {
		t.Fatalf("reserved driver still listed: %+v", onlyFar)
	}
	if err := svc.Release(t.Context(), near, rideB); err != nil {
		t.Fatal(err)
	}
	stillFar := decodeNearby(t, do(gate, http.MethodGet, nearbyQuery(pickup, 10, 3000, "standard"), "", ""))
	if len(stillFar) != 1 || stillFar[0].ID != far {
		t.Fatalf("foreign release returned the driver: %+v", stillFar)
	}
	if err := svc.Release(t.Context(), near, rideA); err != nil {
		t.Fatal(err)
	}
	back := decodeNearby(t, do(gate, http.MethodGet, nearbyQuery(pickup, 10, 3000, "standard"), "", ""))
	if len(back) != 2 || back[0].ID != near {
		t.Fatalf("after release = %+v", back)
	}
}

func TestNearbyUnprocessable(t *testing.T) {
	_, gate, _ := readyHarness(t)
	pickup := geo.Point{Lat: -23.5613, Lng: -46.6565}
	cases := []string{
		nearbyQuery(pickup, 101, 3000, ""),
		nearbyQuery(pickup, 10, 50001, ""),
		nearbyQuery(geo.Point{Lat: 91, Lng: 0}, 10, 3000, ""),
		"/drivers/nearby?lat=abc&lng=0&k=1&radius_m=1",
		"/drivers/nearby?lat=0&lng=0&k=0&radius_m=1",
	}
	for _, q := range cases {
		assertProblem(t, do(gate, http.MethodGet, q, "", ""), http.StatusUnprocessableEntity)
	}
}

func TestStaleSeqDoesNotMoveOrPublish(t *testing.T) {
	_, gate, _, cache, bus := harnessWithBus(t)
	id := register(t, gate, "Ana", "ABC1D23", "standard")
	if do(gate, http.MethodPatch, "/drivers/"+id+"/status", `{"status":"AVAILABLE"}`, "").Code != http.StatusOK {
		t.Fatal("patch")
	}
	pickup := geo.Point{Lat: -23.56, Lng: -46.65}
	putLoc(t, gate, id, pickup, 5, time.Now().UTC())
	bus.waitLocations(t, 1)
	if cache.n() != 1 || cache.lastKey != "driver:"+id+":last" {
		t.Fatalf("cache = %d %q", cache.n(), cache.lastKey)
	}

	rec := do(gate, http.MethodPut, "/drivers/"+id+"/location", locBody(geo.Point{Lat: 10, Lng: 10}, 4, time.Now().UTC()), "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("stale put = %d %q", rec.Code, rec.Body)
	}
	if cache.n() != 1 || bus.locations() != 1 {
		t.Fatalf("stale seq wrote cache %d or published %d", cache.n(), bus.locations())
	}
	sl, ok := bus.svc.Slot(id)
	if !ok || sl.Seq != 5 || sl.Lat != pickup.Lat {
		t.Fatalf("slot = %+v", sl)
	}
}

func TestLocationPublishDoesNotBlock204(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	bus := &fakeBus{started: started, block: release}
	_, gate, _ := harnessBus(t, bus)
	id := register(t, gate, "Ana", "ABC1D23", "standard")

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- do(gate, http.MethodPut, "/drivers/"+id+"/location", locBody(geo.Point{Lat: -23.56, Lng: -46.65}, 1, time.Now().UTC()), "")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("publish did not start")
	}
	select {
	case rec := <-done:
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("204 waited for the location publish")
	}
	close(release)
}

func readyHarness(t *testing.T) (*server, *httpsrv.Gate, *driver.Service) {
	t.Helper()
	srv, gate, svc, _ := harness(t)
	if err := svc.PublishSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate.SetReady(true)
	return srv, gate, svc
}

func harness(t *testing.T) (*server, *httpsrv.Gate, *driver.Service, *fakeCache) {
	t.Helper()
	cache := &fakeCache{}
	srv, gate, svc := harnessCache(t, cache, &fakeBus{})
	return srv, gate, svc, cache
}

func harnessWithBus(t *testing.T) (*server, *httpsrv.Gate, *driver.Service, *fakeCache, *fakeBus) {
	t.Helper()
	cache := &fakeCache{}
	bus := &fakeBus{}
	srv, gate, svc := harnessCache(t, cache, bus)
	if err := svc.PublishSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate.SetReady(true)
	bus.svc = svc
	return srv, gate, svc, cache, bus
}

func harnessBus(t *testing.T, bus *fakeBus) (*server, *httpsrv.Gate, *driver.Service) {
	t.Helper()
	cache := &fakeCache{}
	srv, gate, svc := harnessCache(t, cache, bus)
	if err := svc.PublishSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate.SetReady(true)
	bus.svc = svc
	return srv, gate, svc
}

func harnessCache(t *testing.T, cache *fakeCache, bus driver.Bus) (*server, *httpsrv.Gate, *driver.Service) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	svc := driver.New(log, newMemDB(), cache, bus)
	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)
	srv.bind(svc)
	return srv, gate, svc
}

func register(t *testing.T, gate *httpsrv.Gate, name, plate, class string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"phone":"11999999999","vehicle_class":%q,"plate":%q}`, name, class, plate)
	rec := do(gate, http.MethodPost, "/drivers", body, "key-"+plate)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s = %d %s", name, rec.Code, rec.Body)
	}
	var d driverJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func putLoc(t *testing.T, gate *httpsrv.Gate, id string, p geo.Point, seq int64, at time.Time) {
	t.Helper()
	rec := do(gate, http.MethodPut, "/drivers/"+id+"/location", locBody(p, seq, at), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put %s = %d %s", id, rec.Code, rec.Body)
	}
}

func locBody(p geo.Point, seq int64, at time.Time) string {
	return fmt.Sprintf(`{"lat":%.8f,"lng":%.8f,"seq":%d,"observed_at":%q,"accuracy_m":8.5}`, p.Lat, p.Lng, seq, at.UTC().Format(time.RFC3339Nano))
}

func nearbyQuery(p geo.Point, k int, radius float64, class string) string {
	q := fmt.Sprintf("/drivers/nearby?lat=%.8f&lng=%.8f&k=%d&radius_m=%g", p.Lat, p.Lng, k, radius)
	if class != "" {
		q += "&class=" + class
	}
	return q
}

func north(p geo.Point, meters float64) geo.Point {
	return geo.Point{Lat: p.Lat + meters/(geo.EarthRadiusM*math.Pi/180), Lng: p.Lng}
}

func decodeNearby(t *testing.T, rec *httptest.ResponseRecorder) []nearbyDriverJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("nearby = %d %s", rec.Code, rec.Body)
	}
	var got nearbyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Drivers
}

func do(gate *httpsrv.Gate, method, target, body, idem string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("X-Request-Id", "req-test")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(rec, req)
	return rec
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, status, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var p problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.TraceID != "req-test" || p.Status != status {
		t.Fatalf("problem = %+v", p)
	}
}

type fakeCache struct {
	mu      sync.Mutex
	nPuts   int
	lastKey string
}

func (f *fakeCache) Put(_ context.Context, id string, _ driver.Position) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nPuts++
	f.lastKey = "driver:" + id + ":last"
	return nil
}

func (f *fakeCache) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nPuts
}

type fakeBus struct {
	mu      sync.Mutex
	locs    int
	started chan struct{}
	block   chan struct{}
	svc     *driver.Service
	once    sync.Once
}

func (f *fakeBus) Publish(_ context.Context, topic, _ string, _ []byte) error {
	if topic == event.TopicDriversLocation && f.started != nil {
		f.once.Do(func() { close(f.started) })
		<-f.block
	}
	f.mu.Lock()
	f.locs++
	f.mu.Unlock()
	return nil
}

func (f *fakeBus) locations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.locs
}

func (f *fakeBus) waitLocations(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.locations() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("published locations = %d, want %d", f.locations(), n)
}

func (f *fakeBus) ReplayLocations(context.Context, func(event.Location) error) error { return nil }

func (f *fakeBus) ConsumeReservations(ctx context.Context, _ func(context.Context, string, []byte) error) error {
	<-ctx.Done()
	return nil
}

type memDB struct {
	mu      sync.Mutex
	drivers map[string]driver.Driver
	byKey   map[string]string
	byPlate map[string]string
	next    uint32
}

func newMemDB() *memDB {
	return &memDB{
		drivers: make(map[string]driver.Driver),
		byKey:   make(map[string]string),
		byPlate: make(map[string]string),
	}
}

func (m *memDB) Create(_ context.Context, key string, in driver.Registration) (driver.Driver, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byKey[key]; ok {
		return m.drivers[id], false, nil
	}
	if _, taken := m.byPlate[in.Plate]; taken {
		return driver.Driver{}, false, driver.ErrPlateTaken
	}
	d := driver.Driver{
		ID:           fmt.Sprintf("00000000-0000-7000-8000-%012d", m.next+1),
		InternalID:   m.next,
		Name:         in.Name,
		Phone:        in.Phone,
		VehicleClass: in.VehicleClass,
		Plate:        in.Plate,
		Status:       driver.StatusPending,
		RegisteredAt: time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC),
	}
	m.next++
	m.drivers[d.ID] = d
	m.byKey[key] = d.ID
	m.byPlate[in.Plate] = d.ID
	return d, true, nil
}

func (m *memDB) Get(_ context.Context, id string) (driver.Driver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[id]
	if !ok {
		return driver.Driver{}, driver.ErrNotFound
	}
	return d, nil
}

func (m *memDB) UpdateStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[id]
	if !ok {
		return driver.ErrNotFound
	}
	if d.Status == driver.StatusOnTrip {
		return driver.ErrIllegalStatus
	}
	d.Status = status
	m.drivers[id] = d
	return nil
}

func (m *memDB) Assign(_ context.Context, driverID, rideID string) (driver.AssignResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[driverID]
	if !ok {
		return driver.AssignMissing, nil
	}
	if d.Status == driver.StatusOnTrip && d.CurrentRideID == rideID {
		return driver.AssignSame, nil
	}
	if d.Status != driver.StatusAvailable || d.CurrentRideID != "" {
		return driver.AssignOther, nil
	}
	d.Status = driver.StatusOnTrip
	d.CurrentRideID = rideID
	m.drivers[driverID] = d
	return driver.AssignApplied, nil
}

func (m *memDB) Release(_ context.Context, driverID, rideID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drivers[driverID]
	if !ok || d.CurrentRideID != rideID {
		return false, nil
	}
	d.Status = driver.StatusAvailable
	d.CurrentRideID = ""
	m.drivers[driverID] = d
	return true, nil
}

func (m *memDB) List(context.Context) ([]driver.Driver, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]driver.Driver, 0, len(m.drivers))
	for _, d := range m.drivers {
		out = append(out, d)
	}
	return out, nil
}
