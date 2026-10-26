package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"net/http"
	"time"
)

//go:embed static/*
var staticFS embed.FS

func (d *demo) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/map", d.handleMap)
	mux.HandleFunc("GET /api/state", d.handleState)
	mux.HandleFunc("POST /api/pickup", d.handlePickup)
	mux.HandleFunc("POST /api/scenarios/{name}", d.handleScenario)
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", noCache{http.FileServer(http.FS(sub))})
	return mux
}

type noCache struct{ handler http.Handler }

func (n noCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	n.handler.ServeHTTP(w, r)
}

func (d *demo) handleScenario(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	fn, timeout, label, ok := d.lookup(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cenário desconhecido"})
		return
	}
	if !d.begin(name) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errBusy.Error()})
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				d.logf("Falhou: %v", rec)
			}
			d.end()
		}()
		parent := d.rootContext()
		ctx, cancel := contextWith(parent, timeout)
		defer cancel()
		d.logf("Cenário: %s.", label)
		if err := fn(ctx); err != nil {
			d.logf("Falhou: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"scenario": name})
}

func (d *demo) lookup(name string) (func(ctx context.Context) error, time.Duration, string, bool) {
	switch name {
	case "fleet":
		return d.fleet, 45 * time.Second, "mostrar a frota", true
	case "success":
		return d.success, 3 * time.Minute, "sucesso no grafo", true
	case "race":
		return d.race, 3 * time.Minute, "dois pedidos juntos", true
	case "routing-down":
		return d.routingDown, 4 * time.Minute, "queda do routing", true
	case "routing-up":
		return d.routingUp, 4 * time.Minute, "religar o routing", true
	case "lost":
		return d.lost, 4 * time.Minute, "motorista some", true
	case "nobody":
		return d.nobody, 2 * time.Minute, "ninguém por perto", true
	case "cancel":
		return d.cancelRide, 3 * time.Minute, "cancelar a corrida", true
	case "reset":
		return d.reset, 2 * time.Minute, "voltar ao padrão", true
	default:
		return nil, 0, "", false
	}
}

func (d *demo) rootContext() context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.root != nil {
		return d.root
	}
	return context.Background()
}

func contextWith(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

func (d *demo) handleMap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.mapView())
}

func (d *demo) handlePickup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID int `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pedido inválido"})
		return
	}
	if err := d.setPickup(body.NodeID); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errBusy) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, d.mapView())
}

func (d *demo) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.snapshot())
}

type mapView struct {
	Width        float64      `json:"width"`
	Height       float64      `json:"height"`
	X0           float64      `json:"x0"`
	Y0           float64      `json:"y0"`
	XScale       float64      `json:"x_scale"`
	YScale       float64      `json:"y_scale"`
	MinLng       float64      `json:"min_lng"`
	MaxLat       float64      `json:"max_lat"`
	FleetRadiusM float64      `json:"fleet_radius_m"`
	MatchK       int          `json:"match_k"`
	MatchRadiusM float64      `json:"match_radius_m"`
	Pickup       pointView    `json:"pickup"`
	Far          pointView    `json:"far_pickup"`
	Nodes        []nodeView   `json:"nodes"`
	Edges        []edgeView   `json:"edges"`
	Compare      []compareRow `json:"compare"`
}

type pointView struct {
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Label  string  `json:"label,omitempty"`
	NodeID int     `json:"node_id"`
}

type nodeView struct {
	ID   int     `json:"id"`
	Lat  float64 `json:"lat"`
	Lng  float64 `json:"lng"`
	Kind string  `json:"kind"`
}

type edgeView struct {
	From      int    `json:"from"`
	To        int    `json:"to"`
	DistanceM int    `json:"distance_m"`
	DurationS int    `json:"duration_s"`
	Kind      string `json:"kind"`
}

type compareRow struct {
	Role       string `json:"role"`
	Name       string `json:"name"`
	DijkstraS  int    `json:"dijkstra_s"`
	DijkstraM  int    `json:"dijkstra_m"`
	HaversineS int    `json:"haversine_s"`
	HaversineM int    `json:"haversine_m"`
}

func (d *demo) mapView() mapView {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.city
	view := mapView{
		Width: mapWidth, Height: mapHeight,
		X0: c.proj.x0, Y0: c.proj.y0, XScale: c.proj.xScale, YScale: c.proj.yScale,
		MinLng: c.proj.minLng, MaxLat: c.proj.maxLat,
		FleetRadiusM: c.radiusM, MatchK: matchK, MatchRadiusM: matchRadiusM,
		Pickup: pointView{Lat: c.pickup.Lat, Lng: c.pickup.Lng, Label: c.label, NodeID: c.pickupID},
		Far:    pointView{Lat: c.far.Lat, Lng: c.far.Lng, Label: "Rio de Janeiro"},
	}
	for _, n := range c.nodes {
		kind := "road"
		if n.ID == c.pickupID {
			kind = "pickup"
		}
		view.Nodes = append(view.Nodes, nodeView{ID: n.ID, Lat: n.Lat, Lng: n.Lng, Kind: kind})
	}
	for _, e := range c.edges {
		view.Edges = append(view.Edges, edgeView{
			From: e.From, To: e.To, DistanceM: e.DistanceM, DurationS: e.DurationS, Kind: "road",
		})
	}
	for _, a := range c.actors {
		view.Compare = append(view.Compare, compareRow{
			Role: a.role, Name: a.name,
			DijkstraS: a.dijkstraS, DijkstraM: a.dijkstraM,
			HaversineS: a.haversineS, HaversineM: a.haversineM,
		})
	}
	return view
}

type stateView struct {
	Now       time.Time    `json:"now"`
	Connected bool         `json:"connected"`
	Busy      string       `json:"busy"`
	Cluster   clusterView  `json:"cluster"`
	Pickup    pointView    `json:"pickup"`
	Drivers   []driverView `json:"drivers"`
	Rides     []rideView   `json:"rides"`
	Trips     []tripView   `json:"trips"`
	Log       []logLine    `json:"log"`
}

type driverView struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Name       string  `json:"name"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Status     string  `json:"status"`
	Frozen     bool    `json:"frozen"`
	LostInS    int     `json:"lost_in_s"`
	DijkstraS  int     `json:"dijkstra_s"`
	DijkstraM  int     `json:"dijkstra_m"`
	HaversineS int     `json:"haversine_s"`
	HaversineM int     `json:"haversine_m"`
}

type tripView struct {
	DriverID   string      `json:"driver_id"`
	Started    time.Time   `json:"started"`
	DurationMS int64       `json:"duration_ms"`
	Path       []pointView `json:"path"`
}

func (d *demo) snapshot() stateView {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.placeLocked(now)
	view := stateView{
		Now: now, Connected: d.connected, Busy: d.busy, Cluster: d.clusterNow,
		Pickup:  pointView{Lat: d.city.pickup.Lat, Lng: d.city.pickup.Lng, Label: d.city.label, NodeID: d.city.pickupID},
		Drivers: []driverView{}, Rides: append([]rideView(nil), d.rides...), Trips: []tripView{},
		Log: append([]logLine(nil), d.logs...),
	}
	for _, c := range d.cars {
		item := driverView{
			ID: c.id, Role: c.role, Name: c.name, Lat: c.lat, Lng: c.lng,
			Status: c.status, Frozen: c.frozen,
			DijkstraS: c.dijkstraS, DijkstraM: c.dijkstraM,
			HaversineS: c.haversineS, HaversineM: c.haversineM,
		}
		if c.frozen && !c.lastBeat.IsZero() {
			left := heartbeatWindow - now.Sub(c.lastBeat)
			if left > 0 {
				item.LostInS = int(math.Ceil(left.Seconds()))
			}
		}
		view.Drivers = append(view.Drivers, item)
	}
	for _, t := range d.trips {
		path := make([]pointView, len(t.path))
		for i, p := range t.path {
			path[i] = pointView{Lat: p.Lat, Lng: p.Lng}
		}
		view.Trips = append(view.Trips, tripView{
			DriverID: t.driverID, Started: t.started, DurationMS: t.duration.Milliseconds(), Path: path,
		})
	}
	return view
}

func (d *demo) placeLocked(now time.Time) {
	for i := range d.cars {
		if d.cars[i].frozen {
			continue
		}
		if t, ok := d.tripOf(d.cars[i].id); ok {
			d.cars[i].lat, d.cars[i].lng = t.at(now)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
