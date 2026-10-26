// Command mapgen writes data/maps/graph.txt from the driveable streets
// around Praça Raul Soares, in central Belo Horizonte.
//
//	go run ./web/demo/mapgen
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/graph"
)

const (
	version = "bh-raul-soares-v1"
	south   = -19.9338
	west    = -43.9581
	north   = -19.9117
	east    = -43.9320
	// pad keeps a junction that sits just outside the box.
	pad = 0.0009
	// focus is Praça Raul Soares.
	focusLat = -19.92275
	focusLng = -43.94506

	keepDistanceM = 40.0
	keepTurnDeg   = 25.0
)

func main() {
	outPath := flag.String("out", "data/maps/graph.txt", "arquivo do grafo")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	raw, err := fetch(ctx, query())
	if err != nil {
		fmt.Fprintf(os.Stderr, "overpass: %v\n", err)
		os.Exit(1)
	}
	file, spots, err := build(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grafo: %v\n", err)
		os.Exit(1)
	}
	if _, err := graph.Build(file); err != nil {
		fmt.Fprintf(os.Stderr, "grafo inválido: %v\n", err)
		os.Exit(1)
	}
	payload := encode(file)
	if len(payload) > 900_000 {
		fmt.Fprintf(os.Stderr, "arquivo com %d bytes, acima de 900 KB\n", len(payload))
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, payload, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gravar: %v\n", err)
		os.Exit(1)
	}

	focus := geo.Point{Lat: focusLat, Lng: focusLng}
	var pickup graph.Node
	best := math.MaxFloat64
	for _, n := range file.Nodes {
		d := geo.Distance(geo.Point{Lat: n.Lat, Lng: n.Lng}, focus)
		if d < best {
			best = d
			pickup = n
		}
	}
	fmt.Printf("nodes %d\nedges %d\nbytes %d\n", len(file.Nodes), len(file.Edges), len(payload))
	fmt.Printf("pickup %d %.7f %.7f offset_m %.1f\n", pickup.ID, pickup.Lat, pickup.Lng, best)
	fmt.Println("SPOTS")
	for _, s := range spots {
		fmt.Printf("%.7f %.7f\n", s.Lat, s.Lng)
	}
}

func query() string {
	return fmt.Sprintf(`
[out:json][timeout:90];
(
  way["highway"~"^(motorway|motorway_link|trunk|trunk_link|primary|primary_link|secondary|secondary_link|tertiary|tertiary_link|unclassified|residential|living_street)$"](%g,%g,%g,%g);
);
out body;
>;
out skel qt;
`, south, west, north, east)
}

func fetch(ctx context.Context, q string) ([]byte, error) {
	endpoints := []string{
		"https://overpass-api.de/api/interpreter",
		"https://overpass.kumi.systems/api/interpreter",
	}
	var last error
	for _, endpoint := range endpoints {
		body := url.Values{"data": {q}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "nearby-drivers-mapgen/1.0 (local presentation graph)")
		client := &http.Client{Timeout: 100 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("%s: %s: %s", endpoint, resp.Status, clip(raw))
			continue
		}
		if !bytes.Contains(raw, []byte(`"elements"`)) {
			last = fmt.Errorf("%s: resposta sem elements: %s", endpoint, clip(raw))
			continue
		}
		return raw, nil
	}
	return nil, last
}

func clip(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 240 {
		return text[:240]
	}
	return text
}

type osmDoc struct {
	Elements []struct {
		Type  string            `json:"type"`
		ID    int64             `json:"id"`
		Lat   float64           `json:"lat"`
		Lon   float64           `json:"lon"`
		Nodes []int64           `json:"nodes"`
		Tags  map[string]string `json:"tags"`
	} `json:"elements"`
}

type point struct {
	lat float64
	lng float64
}

type roadEdge struct {
	from int64
	to   int64
	dist int
	dur  int
}

func build(raw []byte) (graph.File, []geo.Point, error) {
	var doc osmDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return graph.File{}, nil, err
	}
	at := make(map[int64]point)
	type way struct {
		nodes []int64
		speed float64
		dir   int
	}
	var ways []way
	for _, el := range doc.Elements {
		switch el.Type {
		case "node":
			at[el.ID] = point{lat: el.Lat, lng: el.Lon}
		case "way":
			if el.Tags["area"] == "yes" || blocked(el.Tags) {
				continue
			}
			speed := speedMPS(el.Tags["highway"], el.Tags["maxspeed"])
			if speed <= 0 {
				continue
			}
			ways = append(ways, way{nodes: el.Nodes, speed: speed, dir: direction(el.Tags)})
		}
	}
	if len(ways) == 0 {
		return graph.File{}, nil, fmt.Errorf("nenhuma rua na área")
	}

	edges := make(map[[2]int64]roadEdge)
	add := func(from, to int64, dist, dur int) {
		if from == to || dist <= 0 {
			return
		}
		if dur < 1 {
			dur = 1
		}
		key := [2]int64{from, to}
		if old, ok := edges[key]; ok && (dur > old.dur || (dur == old.dur && dist >= old.dist)) {
			return
		}
		edges[key] = roadEdge{from: from, to: to, dist: dist, dur: dur}
	}

	chainOf := make([][]int64, 0, len(ways))
	speedOf := make([]float64, 0, len(ways))
	dirOf := make([]int, 0, len(ways))
	degree := make(map[int64]int)
	for _, w := range ways {
		var chain []int64
		flush := func() {
			if len(chain) >= 2 {
				chainOf = append(chainOf, append([]int64(nil), chain...))
				speedOf = append(speedOf, w.speed)
				dirOf = append(dirOf, w.dir)
			}
			chain = nil
		}
		for _, id := range w.nodes {
			p, ok := at[id]
			if !ok || !inside(p) {
				flush()
				continue
			}
			if len(chain) > 0 && chain[len(chain)-1] == id {
				continue
			}
			chain = append(chain, id)
		}
		flush()
	}
	for _, chain := range chainOf {
		seen := make(map[int64]struct{}, len(chain))
		for _, id := range chain {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			degree[id]++
		}
	}
	for i, chain := range chainOf {
		kept := thin(chain, at, degree)
		if len(kept) < 2 {
			continue
		}
		ordered := kept
		if dirOf[i] < 0 {
			ordered = reversed(kept)
		}
		for j := 0; j < len(ordered)-1; j++ {
			dist := span(chain, ordered[j], ordered[j+1], at)
			dur := int(math.Round(float64(dist) / speedOf[i]))
			add(ordered[j], ordered[j+1], dist, dur)
			if dirOf[i] == 0 {
				add(ordered[j+1], ordered[j], dist, dur)
			}
		}
	}
	if len(edges) == 0 {
		return graph.File{}, nil, fmt.Errorf("nenhuma aresta")
	}

	focus := geo.Point{Lat: focusLat, Lng: focusLng}
	var seed int64
	best := math.MaxFloat64
	seenNode := make(map[int64]struct{})
	for key := range edges {
		for _, id := range []int64{key[0], key[1]} {
			if _, ok := seenNode[id]; ok {
				continue
			}
			seenNode[id] = struct{}{}
			p := at[id]
			d := geo.Distance(geo.Point{Lat: p.lat, Lng: p.lng}, focus)
			if d < best {
				best = d
				seed = id
			}
		}
	}
	if best > 120 {
		return graph.File{}, nil, fmt.Errorf("nó mais próximo da praça está a %.0f m", best)
	}

	adj := make(map[int64][]int64)
	for key := range edges {
		adj[key[0]] = append(adj[key[0]], key[1])
		adj[key[1]] = append(adj[key[1]], key[0])
	}
	keep := bfs(seed, adj)

	ids := make([]int64, 0, len(keep))
	for id := range keep {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	index := make(map[int64]int, len(ids))
	nodes := make([]graph.Node, len(ids))
	for i, id := range ids {
		index[id] = i
		p := at[id]
		nodes[i] = graph.Node{ID: i, Lat: p.lat, Lng: p.lng}
	}
	var outEdges []graph.Edge
	for _, e := range edges {
		from, okF := index[e.from]
		to, okT := index[e.to]
		if !okF || !okT {
			continue
		}
		outEdges = append(outEdges, graph.Edge{From: from, To: to, DistanceM: e.dist, DurationS: e.dur})
	}
	slices.SortFunc(outEdges, func(a, b graph.Edge) int {
		if a.From != b.From {
			return a.From - b.From
		}
		if a.To != b.To {
			return a.To - b.To
		}
		return a.DurationS - b.DurationS
	})

	file := graph.File{Version: version, Nodes: nodes, Edges: outEdges}
	return file, sampleSpots(file, focus), nil
}

func sampleSpots(file graph.File, focus geo.Point) []geo.Point {
	at := make(map[int]geo.Point, len(file.Nodes))
	for _, n := range file.Nodes {
		at[n.ID] = geo.Point{Lat: n.Lat, Lng: n.Lng}
	}
	var pickup int
	best := math.MaxFloat64
	for _, n := range file.Nodes {
		d := geo.Distance(at[n.ID], focus)
		if d < best {
			best = d
			pickup = n.ID
		}
	}
	rev := make(map[int][]int)
	for _, e := range file.Edges {
		rev[e.To] = append(rev[e.To], e.From)
	}
	reach := map[int]bool{pickup: true}
	queue := []int{pickup}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, from := range rev[cur] {
			if reach[from] {
				continue
			}
			reach[from] = true
			queue = append(queue, from)
		}
	}
	type spot struct {
		p geo.Point
		d float64
	}
	var spots []spot
	for id := range reach {
		if id == pickup {
			continue
		}
		d := geo.Distance(at[id], focus)
		if d < 80 || d > 1400 {
			continue
		}
		spots = append(spots, spot{p: at[id], d: d})
	}
	slices.SortFunc(spots, func(a, b spot) int {
		if a.d < b.d {
			return -1
		}
		if a.d > b.d {
			return 1
		}
		return 0
	})
	var chosen []geo.Point
	for _, s := range spots {
		close := false
		for _, have := range chosen {
			if geo.Distance(s.p, have) < 180 {
				close = true
				break
			}
		}
		if close {
			continue
		}
		chosen = append(chosen, s.p)
		if len(chosen) == 12 {
			break
		}
	}
	return chosen
}

func thin(chain []int64, at map[int64]point, degree map[int64]int) []int64 {
	if len(chain) < 2 {
		return nil
	}
	kept := []int64{chain[0]}
	for i := 1; i < len(chain)-1; i++ {
		id := chain[i]
		prev := kept[len(kept)-1]
		if degree[id] >= 2 || geo.Distance(asPoint(at[prev]), asPoint(at[id])) >= keepDistanceM || turnDeg(at[prev], at[id], at[chain[i+1]]) >= keepTurnDeg {
			kept = append(kept, id)
		}
	}
	kept = append(kept, chain[len(chain)-1])
	return kept
}

func span(chain []int64, from, to int64, at map[int64]point) int {
	i := slices.Index(chain, from)
	j := slices.Index(chain, to)
	if i < 0 || j < 0 {
		return int(math.Round(geo.Distance(asPoint(at[from]), asPoint(at[to]))))
	}
	if i > j {
		i, j = j, i
	}
	sum := 0.0
	for k := i; k < j; k++ {
		sum += geo.Distance(asPoint(at[chain[k]]), asPoint(at[chain[k+1]]))
	}
	return int(math.Round(sum))
}

func reversed(ids []int64) []int64 {
	out := make([]int64, len(ids))
	for i := range ids {
		out[i] = ids[len(ids)-1-i]
	}
	return out
}

func bfs(seed int64, adj map[int64][]int64) map[int64]struct{} {
	keep := map[int64]struct{}{seed: {}}
	queue := []int64{seed}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if _, ok := keep[next]; ok {
				continue
			}
			keep[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	return keep
}

func inside(p point) bool {
	return p.lat >= south-pad && p.lat <= north+pad && p.lng >= west-pad && p.lng <= east+pad
}

func asPoint(p point) geo.Point {
	return geo.Point{Lat: p.lat, Lng: p.lng}
}

func turnDeg(a, b, c point) float64 {
	h1 := math.Atan2(b.lat-a.lat, b.lng-a.lng)
	h2 := math.Atan2(c.lat-b.lat, c.lng-b.lng)
	d := (h2 - h1) * 180 / math.Pi
	for d > 180 {
		d -= 360
	}
	for d < -180 {
		d += 360
	}
	if d < 0 {
		return -d
	}
	return d
}

func blocked(tags map[string]string) bool {
	switch tags["access"] {
	case "private", "no":
		return true
	}
	switch tags["motor_vehicle"] {
	case "no", "private":
		return true
	}
	switch tags["motorcar"] {
	case "no", "private":
		return true
	}
	return false
}

func direction(tags map[string]string) int {
	switch tags["oneway"] {
	case "yes", "true", "1":
		return 1
	case "-1", "reverse":
		return -1
	case "no":
		return 0
	}
	if tags["junction"] == "roundabout" || tags["junction"] == "circular" {
		return 1
	}
	return 0
}

func speedMPS(highway, maxspeed string) float64 {
	if v, ok := parseKMH(maxspeed); ok {
		if v < 10 {
			v = 10
		}
		if v > 80 {
			v = 80
		}
		return v / 3.6
	}
	switch highway {
	case "motorway", "motorway_link", "trunk", "trunk_link":
		return 60 / 3.6
	case "primary", "primary_link":
		return 50 / 3.6
	case "secondary", "secondary_link":
		return 40 / 3.6
	case "tertiary", "tertiary_link":
		return 35 / 3.6
	case "unclassified":
		return 30 / 3.6
	case "residential":
		return 25 / 3.6
	case "living_street":
		return 15 / 3.6
	default:
		return 0
	}
}

func parseKMH(raw string) (float64, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || strings.Contains(raw, ";") {
		return 0, false
	}
	mph := strings.HasSuffix(raw, "mph")
	raw = strings.TrimSuffix(raw, "km/h")
	raw = strings.TrimSuffix(raw, "kmh")
	raw = strings.TrimSuffix(raw, "mph")
	raw = strings.TrimSpace(raw)
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	if mph {
		v *= 1.60934
	}
	return v, true
}

func encode(file graph.File) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%d %d\n", file.Version, len(file.Nodes), len(file.Edges))
	for _, n := range file.Nodes {
		fmt.Fprintf(&b, "%d %s %s\n", n.ID, strconv.FormatFloat(n.Lat, 'f', 7, 64), strconv.FormatFloat(n.Lng, 'f', 7, 64))
	}
	for _, e := range file.Edges {
		fmt.Fprintf(&b, "%d %d %d %d\n", e.From, e.To, e.DistanceM, e.DurationS)
	}
	return []byte(b.String())
}
