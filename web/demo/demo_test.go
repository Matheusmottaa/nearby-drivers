package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

func TestPickupAPI(t *testing.T) {
	loaded, err := loadCity(t.Context(), repoFile(t, "data/maps/fixture.txt"), fixtureSpec(0))
	if err != nil {
		t.Fatal(err)
	}
	home := loaded.routes[4].points[0]
	loaded.actors = []cast{{role: "c01", name: "Carro 01", node: 4, lat: home.Lat, lng: home.Lng}}
	app := newDemo(loaded, nil, newClient("", ""), slog.New(slog.DiscardHandler), "")
	app.cars = []car{carFrom(app.city.actors[0], "car-1")}
	app.trips = []trip{{driverID: "car-1", started: time.Now(), duration: time.Minute}}
	srv := httptest.NewServer(app.handler())
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/api/pickup", "application/json", strings.NewReader(`{"node_id":5}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("pickup status = %d %s", res.StatusCode, raw)
	}
	var view mapView
	decode(t, string(raw), &view)
	if view.Pickup.NodeID != 5 || view.Pickup.Label != "ponto clicado" {
		t.Fatalf("pickup view = %+v", view.Pickup)
	}
	if app.cars[0].dijkstraS != 80 || len(app.trips) != 0 || app.cars[0].lat != home.Lat {
		t.Fatalf("car = %+v trips=%d", app.cars[0], len(app.trips))
	}

	if !app.begin("success") {
		t.Fatal("begin")
	}
	busy, err := http.Post(srv.URL+"/api/pickup", "application/json", strings.NewReader(`{"node_id":8}`))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Body.Close()
	if busy.StatusCode != http.StatusConflict {
		t.Fatalf("busy status = %d", busy.StatusCode)
	}
	if app.mapView().Pickup.NodeID != 5 {
		t.Fatal("busy click changed the pickup")
	}
}

func TestReset(t *testing.T) {
	app := newDemo(loadTestCity(t), nil, newClient("", ""), slog.New(slog.DiscardHandler), "")
	first := app.city.actors[0]
	app.cars = []car{carFrom(first, "car-1")}
	if err := app.setPickup(5); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.city.actors[0].node = 8
	app.city.actors[0].lat = 1
	app.city.actors[0].lng = 2
	app.rides = []rideView{{ID: "ride-1", Label: "Pedido A", Status: "DRIVER_ASSIGNED"}}
	app.trips = []trip{{driverID: "car-1", started: time.Now(), duration: time.Minute}}
	app.mu.Unlock()

	if err := app.reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if app.city.pickupID != app.city.originID || app.city.label != "embarque" {
		t.Fatalf("pickup = %d %q", app.city.pickupID, app.city.label)
	}
	if app.cars[0].node != first.node || app.cars[0].lat != first.lat || app.cars[0].lng != first.lng {
		t.Fatalf("car home = node %d at %v,%v, want node %d at %v,%v", app.cars[0].node, app.cars[0].lat, app.cars[0].lng, first.node, first.lat, first.lng)
	}
	if len(app.rides) != 0 || len(app.trips) != 0 {
		t.Fatalf("rides=%d trips=%d", len(app.rides), len(app.trips))
	}
}

func TestPages(t *testing.T) {
	loaded := loadTestCity(t)
	app := newDemo(loaded, nil, newClient("", ""), slog.New(slog.DiscardHandler), "")
	srv := httptest.NewServer(app.handler())
	t.Cleanup(srv.Close)

	index := getText(t, srv.URL+"/")
	if !strings.Contains(index, "nearby-drivers") || !strings.Contains(index, "Cenários") {
		t.Fatalf("index missing the presentation shell: %s", index[:min(200, len(index))])
	}
	script := getText(t, srv.URL+"/app.js")
	for _, name := range []string{"fleet", "success", "race", "routing-down", "routing-up", "lost", "nobody", "cancel", "reset"} {
		if !strings.Contains(script, name) {
			t.Fatalf("app.js missing scenario %s", name)
		}
	}
	if !strings.Contains(getText(t, srv.URL+"/app.css"), "#map") {
		t.Fatal("css missing the map")
	}

	var view mapView
	decode(t, getText(t, srv.URL+"/api/map"), &view)
	if len(view.Nodes) != 19 || len(view.Compare) != 4 {
		t.Fatalf("map nodes=%d compare=%d", len(view.Nodes), len(view.Compare))
	}

	if !app.begin("success") {
		t.Fatal("begin")
	}
	res, err := http.Post(srv.URL+"/api/scenarios/fleet", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("busy status = %d, want 409", res.StatusCode)
	}
	missing, err := http.Post(srv.URL+"/api/scenarios/nope", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", missing.StatusCode)
	}
}

func TestScenarios(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		app, rides, _ := newStage(t, nil)
		rides.onCreate = func(body createRideBody) (string, *assignment) {
			if math.Abs(body.Pickup.Lat-farLat) < 0.01 {
				return "NO_DRIVER_AVAILABLE", nil
			}
			w := mustWinner(t, app, event.EngineDijkstra)
			return "DRIVER_ASSIGNED", &assignment{id: w.id, eta: w.dijkstraS, meters: w.dijkstraM, engine: event.EngineDijkstra}
		}
		if err := app.success(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := app.snapshot(); len(got.Drivers) != 4 {
			t.Fatalf("drivers = %d, want 4", len(got.Drivers))
		}
		if !hasTrip(app, mustWinner(t, app, event.EngineDijkstra).id) {
			t.Fatal("winning car is not moving")
		}
	})

	t.Run("haversine retry", func(t *testing.T) {
		app, rides, _ := newStage(t, nil)
		var n int
		rides.onCreate = func(createRideBody) (string, *assignment) {
			n++
			if n == 1 {
				w := mustWinner(t, app, event.EngineHaversine)
				return "DRIVER_ASSIGNED", &assignment{id: w.id, eta: w.haversineS, meters: w.haversineM, engine: event.EngineHaversine}
			}
			w := mustWinner(t, app, event.EngineDijkstra)
			return "DRIVER_ASSIGNED", &assignment{id: w.id, eta: w.dijkstraS, meters: w.dijkstraM, engine: event.EngineDijkstra}
		}
		if err := app.success(t.Context()); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("creates = %d, want 2", n)
		}
	})

	t.Run("race", func(t *testing.T) {
		app, rides, _ := newStage(t, nil)
		if err := app.ensureFleet(t.Context()); err != nil {
			t.Fatal(err)
		}
		ids := app.carIDs()
		if len(ids) < 2 {
			t.Fatal("need two cars")
		}
		var n int
		rides.onCreate = func(createRideBody) (string, *assignment) {
			n++
			id := ids[0]
			if n > 1 {
				id = ids[1]
			}
			return "DRIVER_ASSIGNED", &assignment{id: id, eta: 10, meters: 100, engine: event.EngineDijkstra}
		}
		if err := app.race(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !hasTrip(app, ids[0]) || !hasTrip(app, ids[1]) {
			t.Fatal("both cars should be moving")
		}
	})

	t.Run("routing down and restore", func(t *testing.T) {
		kube := &fakeKube{contextName: "docker-desktop", ready: 2, hpa: true, ips: "10.0.0.1"}
		app, rides, _ := newStage(t, testCluster(t, kube))
		rides.onCreate = func(createRideBody) (string, *assignment) {
			w := mustWinner(t, app, event.EngineHaversine)
			return "DRIVER_ASSIGNED", &assignment{id: w.id, eta: w.haversineS, meters: w.haversineM, engine: event.EngineHaversine}
		}
		if err := app.routingDown(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !app.needsRestore() {
			t.Fatal("routing should stay down after the scenario")
		}
		if err := app.routingUp(t.Context()); err != nil {
			t.Fatal(err)
		}
		if app.needsRestore() {
			t.Fatal("routing was not restored")
		}
	})

	t.Run("lost", func(t *testing.T) {
		app, rides, _ := newStage(t, nil)
		first := mustWinner(t, app, event.EngineDijkstra)
		other := otherCar(t, app, first.id)
		rides.onCreate = func(createRideBody) (string, *assignment) {
			return "DRIVER_ASSIGNED", &assignment{id: first.id, eta: first.dijkstraS, meters: first.dijkstraM, engine: event.EngineDijkstra}
		}
		rides.onGet = func(ride *rideBody) {
			if !app.isFrozen(first.id) {
				return
			}
			if ride.Status == "DRIVER_ASSIGNED" && ride.Driver != nil && ride.Driver.ID == first.id {
				ride.Status = "MATCHING"
				ride.Driver = nil
				return
			}
			if ride.Status == "MATCHING" {
				ride.Status = "DRIVER_ASSIGNED"
				ride.Driver = driverOf(&assignment{id: other, eta: 25, meters: 180, engine: event.EngineDijkstra})
			}
		}
		if err := app.lost(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !app.isFrozen(first.id) || !hasTrip(app, other) {
			t.Fatal("rematch did not move the next car")
		}
	})

	t.Run("nobody", func(t *testing.T) {
		app, rides, _ := newStage(t, nil)
		rides.onCreate = func(body createRideBody) (string, *assignment) {
			if math.Abs(body.Pickup.Lat-farLat) < 0.01 {
				return "NO_DRIVER_AVAILABLE", nil
			}
			t.Errorf("pickup lat = %v, want Rio", body.Pickup.Lat)
			return "DRIVER_ASSIGNED", nil
		}
		if err := app.nobody(t.Context()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		app, rides, drivers := newStage(t, nil)
		id := mustWinner(t, app, event.EngineDijkstra).id
		rides.onCreate = func(createRideBody) (string, *assignment) {
			drivers.set(id, "ON_TRIP")
			return "DRIVER_ASSIGNED", &assignment{id: id, eta: 10, meters: 500, engine: event.EngineDijkstra}
		}
		rides.onCancel = func(string) {
			drivers.set(id, "AVAILABLE")
		}
		if err := app.cancelRide(t.Context()); err != nil {
			t.Fatal(err)
		}
		snap := app.snapshot()
		for _, driver := range snap.Drivers {
			if driver.ID == id && (driver.Lat != snapLat(app, driver.Role) || hasTrip(app, driver.ID)) {
				t.Fatalf("car did not return home: %+v", driver)
			}
		}
	})
}

func mustWinner(t *testing.T, app *demo, engine string) car {
	t.Helper()
	if len(app.carIDs()) == 0 {
		if err := app.ensureFleet(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := app.winner(engine)
	if !ok || got.id == "" {
		t.Fatalf("no %s winner", engine)
	}
	return got
}

func otherCar(t *testing.T, app *demo, id string) string {
	t.Helper()
	for _, other := range app.carIDs() {
		if other != "" && other != id {
			return other
		}
	}
	t.Fatal("need a second car")
	return ""
}

func snapLat(app *demo, role string) float64 {
	app.mu.Lock()
	defer app.mu.Unlock()
	for _, car := range app.cars {
		if car.role == role {
			return car.homeLat
		}
	}
	return 0
}

func hasTrip(app *demo, id string) bool {
	app.mu.Lock()
	defer app.mu.Unlock()
	_, ok := app.tripOf(id)
	return ok
}

func loadTestCity(t *testing.T) city {
	t.Helper()
	loaded, err := loadCity(t.Context(), repoFile(t, "data/maps/fixture.txt"), fixtureSpec(4))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func newStage(t *testing.T, cl *cluster) (*demo, *rideFake, *driverFake) {
	t.Helper()
	drivers := &driverFake{status: map[string]string{}}
	rides := &rideFake{rides: map[string]*rideBody{}}
	driverSrv := httptest.NewServer(drivers)
	rideSrv := httptest.NewServer(rides)
	t.Cleanup(driverSrv.Close)
	t.Cleanup(rideSrv.Close)
	app := newDemo(loadTestCity(t), cl, newClient(rideSrv.URL, driverSrv.URL), slog.New(slog.DiscardHandler), filepath.Join(t.TempDir(), "session.json"))
	app.sleep = func(context.Context, time.Duration) error { return nil }
	return app, rides, drivers
}

type assignment struct {
	id     string
	eta    int
	meters int
	engine string
}

func driverOf(a *assignment) *struct {
	ID        string `json:"id"`
	ETAS      int    `json:"eta_s"`
	DistanceM int    `json:"distance_m"`
	Engine    string `json:"eta_engine"`
} {
	if a == nil {
		return nil
	}
	return &struct {
		ID        string `json:"id"`
		ETAS      int    `json:"eta_s"`
		DistanceM int    `json:"distance_m"`
		Engine    string `json:"eta_engine"`
	}{ID: a.id, ETAS: a.eta, DistanceM: a.meters, Engine: a.engine}
}

type rideFake struct {
	mu       sync.Mutex
	rides    map[string]*rideBody
	onCreate func(createRideBody) (string, *assignment)
	onGet    func(*rideBody)
	onCancel func(string)
}

func (f *rideFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/readyz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/rides" {
		var body createRideBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := newID()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stored := &rideBody{ID: id, Status: "REQUESTED"}
		stored.Pickup.Lat = body.Pickup.Lat
		stored.Pickup.Lng = body.Pickup.Lng
		if f.onCreate != nil {
			status, driver := f.onCreate(body)
			stored.Status = status
			stored.Driver = driverOf(driver)
		}
		f.mu.Lock()
		f.rides[id] = stored
		f.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "REQUESTED"})
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rides/"), "/cancel")
		f.mu.Lock()
		ride := f.rides[id]
		if ride != nil {
			ride.Status = "CANCELLED"
			ride.Driver = nil
		}
		f.mu.Unlock()
		if f.onCancel != nil {
			f.onCancel(id)
		}
		writeJSON(w, http.StatusOK, ride)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rides/") {
		id := strings.TrimPrefix(r.URL.Path, "/rides/")
		f.mu.Lock()
		defer f.mu.Unlock()
		ride := f.rides[id]
		if ride == nil {
			http.NotFound(w, r)
			return
		}
		if f.onGet != nil {
			f.onGet(ride)
		}
		writeJSON(w, http.StatusOK, ride)
		return
	}
	http.NotFound(w, r)
}

type driverFake struct {
	mu     sync.Mutex
	status map[string]string
}

func (f *driverFake) set(id, status string) {
	f.mu.Lock()
	f.status[id] = status
	f.mu.Unlock()
}

func (f *driverFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/readyz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/drivers" {
		id, err := newID()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.status[id] = "PENDING"
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, driverBody{ID: id, Status: "PENDING"})
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "drivers" {
		http.NotFound(w, r)
		return
	}
	id := parts[1]
	if len(parts) == 3 && parts[2] == "status" && r.Method == http.MethodPatch {
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status[id] == "ON_TRIP" {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		f.status[id] = body.Status
		writeJSON(w, http.StatusOK, driverBody{ID: id, Status: body.Status})
		return
	}
	if len(parts) == 3 && parts[2] == "location" && r.Method == http.MethodPut {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		f.mu.Lock()
		status, ok := f.status[id]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, driverBody{ID: id, Status: status})
		return
	}
	http.NotFound(w, r)
}

func getText(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, raw)
	}
	return string(raw)
}

func decode(t *testing.T, raw string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		t.Fatal(err)
	}
}
