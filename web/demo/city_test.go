package main

import (
	"math"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", filepath.FromSlash(rel))
}

func fixtureSpec(fleet int) citySpec {
	return citySpec{
		focus:       geo.Point{Lat: -23.5613, Lng: -46.6565},
		label:       "embarque",
		fleet:       fleet,
		radiusM:     2500,
		separationM: 50,
		seed:        1,
	}
}

func TestCityStory(t *testing.T) {
	loaded, err := loadCity(t.Context(), repoFile(t, "data/maps/fixture.txt"), fixtureSpec(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.nodes) != 19 || len(loaded.edges) != 23 {
		t.Fatalf("graph = %d nodes %d edges, want 19 and 23", len(loaded.nodes), len(loaded.edges))
	}
	if loaded.pickupID != 0 {
		t.Fatalf("pickup node = %d, want 0", loaded.pickupID)
	}
	if _, ok := loaded.routes[2]; ok {
		t.Fatal("alley node 2 is reachable")
	}
	if _, ok := loaded.routes[3]; ok {
		t.Fatal("alley node 3 is reachable")
	}
	viaFast := loaded.routes[4]
	if viaFast.durationS != 20 || viaFast.distanceM != 1000 || len(viaFast.nodes) != 3 || viaFast.nodes[1] != 6 {
		t.Fatalf("node 4 route = %+v, want 20s 1000m via 6", viaFast)
	}
	fast := loaded.routes[6]
	short := loaded.routes[5]
	if fast.durationS != 10 || fast.distanceM != 500 {
		t.Fatalf("node 6 = %ds %dm, want 10s 500m", fast.durationS, fast.distanceM)
	}
	if short.durationS != 80 || short.distanceM != 100 {
		t.Fatalf("node 5 = %ds %dm, want 80s 100m", short.durationS, short.distanceM)
	}
	if fast.durationS >= short.durationS {
		t.Fatal("dijkstra should prefer node 6")
	}

	px, py := loaded.proj.xy(loaded.pickup.Lat, loaded.pickup.Lng)
	fx, fy := loaded.proj.xy(fast.points[0].Lat, fast.points[0].Lng)
	sx, sy := loaded.proj.xy(short.points[0].Lat, short.points[0].Lng)
	if math.Hypot(fx-px, fy-py) <= math.Hypot(sx-px, sy-py) {
		t.Fatal("node 6 should look farther than node 5")
	}
	north := loaded.routes[17].points[0]
	n2, n3 := pointOf(t, loaded, 2), pointOf(t, loaded, 3)
	_, northY := loaded.proj.xy(north.Lat, north.Lng)
	_, y2 := loaded.proj.xy(n2.Lat, n2.Lng)
	_, y3 := loaded.proj.xy(n3.Lat, n3.Lng)
	if northY >= (y2+y3)/2 {
		t.Fatal("node 17 should sit above the alley on the map")
	}
	if px < 0 || px > mapWidth || py < 0 || py > mapHeight {
		t.Fatalf("pickup projects outside the map: %v,%v", px, py)
	}
}

func TestRetarget(t *testing.T) {
	loaded, err := loadCity(t.Context(), repoFile(t, "data/maps/fixture.txt"), fixtureSpec(0))
	if err != nil {
		t.Fatal(err)
	}
	home := loaded.routes[4].points[0]
	blocked := loaded.routes[6].points[0]
	loaded.actors = []cast{
		{role: "c01", name: "Carro 01", node: 4, lat: home.Lat, lng: home.Lng},
		{role: "c02", name: "Carro 02", node: 6, lat: blocked.Lat, lng: blocked.Lng},
	}
	if err := loaded.retarget(5); err != nil {
		t.Fatal(err)
	}
	if loaded.pickupID != 5 || loaded.label != "ponto clicado" {
		t.Fatalf("pickup = %d %q", loaded.pickupID, loaded.label)
	}
	if loaded.actors[0].dijkstraS != 80 || loaded.actors[0].dijkstraM != 100 || len(loaded.actors[0].path) != 2 {
		t.Fatalf("node 4 = %+v, want 80s 100m in 2 points", loaded.actors[0])
	}
	if loaded.actors[1].dijkstraS != noStreetS {
		t.Fatalf("node 6 dijkstra = %d, want no street", loaded.actors[1].dijkstraS)
	}
	if err := loaded.retarget(99); err == nil || loaded.pickupID != 5 {
		t.Fatalf("missing node err=%v pickup=%d", err, loaded.pickupID)
	}
	if err := loaded.retarget(loaded.originID); err != nil {
		t.Fatal(err)
	}
	if loaded.label != "embarque" || loaded.pickupID != 0 {
		t.Fatalf("restored pickup = %d %q", loaded.pickupID, loaded.label)
	}
	if loaded.actors[0].dijkstraS != 20 || loaded.actors[0].dijkstraM != 1000 {
		t.Fatalf("node 4 restored = %ds %dm", loaded.actors[0].dijkstraS, loaded.actors[0].dijkstraM)
	}
}

func TestScatter(t *testing.T) {
	path := repoFile(t, "data/maps/fixture.txt")
	first, err := loadCity(t.Context(), path, fixtureSpec(4))
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadCity(t.Context(), path, fixtureSpec(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.actors) != 4 || len(second.actors) != 4 {
		t.Fatalf("fleet = %d and %d, want 4", len(first.actors), len(second.actors))
	}
	for i := range first.actors {
		if first.actors[i].node != second.actors[i].node {
			t.Fatalf("seed 1 is not stable at %d", i)
		}
		if _, ok := first.routes[first.actors[i].node]; !ok {
			t.Fatalf("actor %s is not reachable", first.actors[i].name)
		}
		if geo.Distance(geo.Point{Lat: first.actors[i].lat, Lng: first.actors[i].lng}, first.pickup) > first.radiusM {
			t.Fatalf("%s is outside the fleet radius", first.actors[i].name)
		}
		for j := i + 1; j < len(first.actors); j++ {
			if first.actors[i].node == first.actors[j].node {
				t.Fatalf("actors share node %d", first.actors[i].node)
			}
		}
	}
}

func TestBeloHorizonte(t *testing.T) {
	path := repoFile(t, "data/maps/graph.txt")
	loaded, err := loadCity(t.Context(), path, presentationSpec(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.nodes) < 400 || len(loaded.edges) < 800 {
		t.Fatalf("city graph = %d nodes %d edges", len(loaded.nodes), len(loaded.edges))
	}
	focus := geo.Point{Lat: pracaLat, Lng: pracaLng}
	if geo.Distance(loaded.pickup, focus) > 100 {
		t.Fatalf("pickup is %.0f m from Praça Raul Soares", geo.Distance(loaded.pickup, focus))
	}
	if len(loaded.actors) != fleetCount {
		t.Fatalf("fleet = %d, want %d", len(loaded.actors), fleetCount)
	}
	reachable := 0
	for _, actor := range loaded.actors {
		rt, ok := loaded.routes[actor.node]
		if !ok || len(rt.points) < 2 {
			t.Fatalf("%s cannot drive to the pickup", actor.name)
		}
		if geo.Distance(geo.Point{Lat: actor.lat, Lng: actor.lng}, loaded.pickup) > fleetRadiusM {
			t.Fatalf("%s is outside %.0f m", actor.name, fleetRadiusM)
		}
	}
	for id := range loaded.routes {
		if id != loaded.pickupID {
			reachable++
		}
	}
	if reachable < 400 {
		t.Fatalf("reachable nodes = %d", reachable)
	}
	view := newDemo(loaded, nil, newClient("", ""), nil, "").mapView()
	if view.Pickup.Label != "Praça Raul Soares" || len(view.Compare) != fleetCount || len(view.Nodes) != len(loaded.nodes) {
		t.Fatalf("map view label=%q cars=%d nodes=%d", view.Pickup.Label, len(view.Compare), len(view.Nodes))
	}
	if view.FleetRadiusM != fleetRadiusM || view.MatchK != matchK {
		t.Fatalf("map radius=%v k=%d", view.FleetRadiusM, view.MatchK)
	}
}

func TestRankPrefersStreetTime(t *testing.T) {
	pickup := geo.Point{Lat: -19.9227, Lng: -43.9451}
	near := car{
		id: "near", role: "c01", homeLat: -19.9229, homeLng: -43.9452,
		dijkstraS: 90, dijkstraM: 120, haversineS: 8, haversineM: 70,
	}
	fast := car{
		id: "fast", role: "c02", homeLat: -19.9255, homeLng: -43.9475,
		dijkstraS: 15, dijkstraM: 700, haversineS: 40, haversineM: 340,
	}
	away := car{
		id: "away", role: "c03", homeLat: -19.80, homeLng: -43.80,
		dijkstraS: 1, dijkstraM: 1, haversineS: 1, haversineM: 1,
	}
	got, ok := rank(event.EngineDijkstra, []car{near, fast, away}, pickup)
	if !ok || got.id != "fast" {
		t.Fatalf("dijkstra winner = %+v", got)
	}
	got, ok = rank(event.EngineHaversine, []car{near, fast, away}, pickup)
	if !ok || got.id != "near" {
		t.Fatalf("haversine winner = %+v", got)
	}
}

func pointOf(t *testing.T, c city, id int) geo.Point {
	t.Helper()
	for _, n := range c.nodes {
		if n.ID == id {
			return geo.Point{Lat: n.Lat, Lng: n.Lng}
		}
	}
	t.Fatalf("missing node %d", id)
	return geo.Point{}
}

func TestAlong(t *testing.T) {
	path := []geo.Point{{Lat: -23.56, Lng: -46.66}, {Lat: -23.56, Lng: -46.65}}
	_, start := along(path, 0)
	if math.Abs(start-path[0].Lng) > 1e-9 {
		t.Fatalf("start lng = %v, want %v", start, path[0].Lng)
	}
	_, end := along(path, 1)
	if math.Abs(end-path[1].Lng) > 1e-9 {
		t.Fatalf("end lng = %v, want %v", end, path[1].Lng)
	}
	_, mid := along(path, 0.5)
	if mid <= path[0].Lng || mid >= path[1].Lng {
		t.Fatalf("mid lng = %v, want between the ends", mid)
	}
}
