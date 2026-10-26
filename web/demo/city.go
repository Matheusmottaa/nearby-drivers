package main

import (
	"cmp"
	"container/heap"
	"context"
	"fmt"
	"math"
	"math/rand"
	"slices"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/graph"
)

// haversineMPS matches the matching-service default HAVERSINE_SPEED_MPS (30 km/h).
const haversineMPS = 8.33

const (
	mapWidth  = 920.0
	mapHeight = 640.0
	// noStreetS marks a car with no drive to the pickup. It stays above any real ETA.
	noStreetS = 1 << 30
)

// Praça Raul Soares, in central Belo Horizonte. The far pickup is Rio de Janeiro,
// outside the 3 km match radius. matchK and matchRadiusM follow the matching service.
const (
	pracaLat         = -19.92275
	pracaLng         = -43.94506
	fleetCount       = 20
	fleetRadiusM     = 1000.0
	fleetSeparationM = 80.0
	matchK           = 10
	matchRadiusM     = 3000.0
	farLat           = -22.9068
	farLng           = -43.1729
)

type citySpec struct {
	focus       geo.Point
	label       string
	fleet       int
	radiusM     float64
	separationM float64
	seed        int64
}

func presentationSpec(seed int64) citySpec {
	return citySpec{
		focus:       geo.Point{Lat: pracaLat, Lng: pracaLng},
		label:       "Praça Raul Soares",
		fleet:       fleetCount,
		radiusM:     fleetRadiusM,
		separationM: fleetSeparationM,
		seed:        seed,
	}
}

// cast is one car the presentation places on the graph.
type cast struct {
	role       string
	name       string
	node       int
	lat        float64
	lng        float64
	dijkstraS  int
	dijkstraM  int
	haversineS int
	haversineM int
	path       []geo.Point
}

type city struct {
	pickup      geo.Point
	pickupID    int
	label       string
	originID    int
	originLabel string
	far         geo.Point
	actors      []cast
	nodes       []graph.Node
	edges       []graph.Edge
	routes      map[int]route
	proj        projection
	fleetN      int
	radiusM     float64
	separationM float64
}

type route struct {
	nodes     []int
	points    []geo.Point
	distanceM int
	durationS int
}

type projection struct {
	x0     float64
	y0     float64
	xScale float64
	yScale float64
	minLng float64
	maxLat float64
}

func (p projection) xy(lat, lng float64) (x, y float64) {
	return p.x0 + (lng-p.minLng)*p.xScale, p.y0 + (p.maxLat-lat)*p.yScale
}

func loadCity(ctx context.Context, path string, spec citySpec) (city, error) {
	file, err := graph.Load(ctx, path)
	if err != nil {
		return city{}, err
	}
	return assemble(file, spec)
}

func assemble(file graph.File, spec citySpec) (city, error) {
	if len(file.Nodes) == 0 {
		return city{}, fmt.Errorf("graph has no nodes")
	}
	at := make(map[int]geo.Point, len(file.Nodes))
	pickupID := file.Nodes[0].ID
	pickup := geo.Point{Lat: file.Nodes[0].Lat, Lng: file.Nodes[0].Lng}
	best := geo.Distance(pickup, spec.focus)
	for _, n := range file.Nodes {
		pt := geo.Point{Lat: n.Lat, Lng: n.Lng}
		at[n.ID] = pt
		d := geo.Distance(pt, spec.focus)
		if d < best || (d == best && n.ID < pickupID) {
			best = d
			pickupID = n.ID
			pickup = pt
		}
	}
	built := city{
		pickup:      pickup,
		pickupID:    pickupID,
		label:       spec.label,
		originID:    pickupID,
		originLabel: spec.label,
		far:         geo.Point{Lat: farLat, Lng: farLng},
		nodes:       file.Nodes,
		edges:       file.Edges,
		routes:      routesTo(pickupID, at, file.Edges),
		proj:        fit(file.Nodes),
		fleetN:      spec.fleet,
		radiusM:     spec.radiusM,
		separationM: spec.separationM,
	}
	if spec.fleet == 0 {
		return built, nil
	}
	actors, err := built.scatter(rand.New(rand.NewSource(spec.seed)), spec.fleet, spec.radiusM, spec.separationM)
	if err != nil {
		return city{}, err
	}
	built.actors = actors
	return built, nil
}

// scatter puts n cars on distinct nodes that can drive to the pickup.
func (c city) scatter(rng *rand.Rand, n int, radiusM, separationM float64) ([]cast, error) {
	if n <= 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(c.routes))
	for id, rt := range c.routes {
		if id == c.pickupID || len(rt.points) == 0 {
			continue
		}
		if geo.Distance(rt.points[0], c.pickup) > radiusM {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })

	sep := separationM
	chosen := make([]int, 0, n)
	for range 6 {
		chosen = chosen[:0]
		for _, id := range ids {
			pt := c.routes[id].points[0]
			ok := true
			for _, have := range chosen {
				if geo.Distance(pt, c.routes[have].points[0]) < sep {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			chosen = append(chosen, id)
			if len(chosen) == n {
				break
			}
		}
		if len(chosen) == n {
			break
		}
		sep *= 0.65
	}
	if len(chosen) < n {
		return nil, fmt.Errorf("só há %d pontos alcançáveis em %.0f m", len(chosen), radiusM)
	}
	actors := make([]cast, 0, n)
	for i, id := range chosen {
		pt := c.routes[id].points[0]
		actor := cast{
			role: fmt.Sprintf("c%02d", i+1), name: fmt.Sprintf("Carro %02d", i+1), node: id,
			lat: pt.Lat, lng: pt.Lng,
		}
		if err := c.score(&actor); err != nil {
			return nil, err
		}
		actors = append(actors, actor)
	}
	return actors, nil
}

func cloneCasts(in []cast) []cast {
	out := make([]cast, len(in))
	for i, actor := range in {
		out[i] = actor
		out[i].path = append([]geo.Point(nil), actor.path...)
	}
	return out
}

// retarget moves the pickup to a graph node and refreshes the street routes.
// Cars stay where they are. The original square comes back when id is the node
// chosen at startup.
func (c *city) retarget(id int) error {
	at := make(map[int]geo.Point, len(c.nodes))
	var pt geo.Point
	found := false
	for _, n := range c.nodes {
		p := geo.Point{Lat: n.Lat, Lng: n.Lng}
		at[n.ID] = p
		if n.ID == id {
			pt = p
			found = true
		}
	}
	if !found {
		return fmt.Errorf("nó %d não está no mapa", id)
	}
	c.routes = routesTo(id, at, c.edges)
	c.pickupID = id
	c.pickup = pt
	if id == c.originID {
		c.label = c.originLabel
	} else {
		c.label = "ponto clicado"
	}
	for i := range c.actors {
		if err := c.score(&c.actors[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *city) score(a *cast) error {
	pt := geo.Point{Lat: a.lat, Lng: a.lng}
	est, err := geo.Fallback(pt, c.pickup, haversineMPS)
	if err != nil {
		return fmt.Errorf("haversine do nó %d: %w", a.node, err)
	}
	a.haversineS = est.DurationS
	a.haversineM = est.DistanceM
	rt, ok := c.routes[a.node]
	if !ok || len(rt.points) == 0 {
		a.dijkstraS = noStreetS
		a.dijkstraM = 0
		a.path = []geo.Point{pt, c.pickup}
		return nil
	}
	a.dijkstraS = rt.durationS
	a.dijkstraM = rt.distanceM
	a.path = append([]geo.Point(nil), rt.points...)
	return nil
}

// rank copies the matching rule: the K nearest cars by straight line, then the
// smallest ETA, then distance, then driver id.
func rank(engine string, cars []car, pickup geo.Point) (car, bool) {
	type item struct {
		c        car
		straight float64
	}
	pool := make([]item, 0, len(cars))
	for _, c := range cars {
		d := geo.Distance(geo.Point{Lat: c.homeLat, Lng: c.homeLng}, pickup)
		if d > matchRadiusM {
			continue
		}
		pool = append(pool, item{c: c, straight: d})
	}
	slices.SortFunc(pool, func(a, b item) int {
		return cmp.Or(cmp.Compare(a.straight, b.straight), cmp.Compare(a.c.id, b.c.id), cmp.Compare(a.c.role, b.c.role))
	})
	if len(pool) > matchK {
		pool = pool[:matchK]
	}
	if len(pool) == 0 {
		return car{}, false
	}
	best := pool[0].c
	for _, it := range pool[1:] {
		if beats(engine, it.c, best) {
			best = it.c
		}
	}
	return best, true
}

func beats(engine string, a, b car) bool {
	if engine == event.EngineHaversine {
		return cmp.Or(
			cmp.Compare(a.haversineS, b.haversineS),
			cmp.Compare(a.haversineM, b.haversineM),
			cmp.Compare(a.id, b.id),
			cmp.Compare(a.role, b.role),
		) < 0
	}
	return cmp.Or(
		cmp.Compare(a.dijkstraS, b.dijkstraS),
		cmp.Compare(a.dijkstraM, b.dijkstraM),
		cmp.Compare(a.id, b.id),
		cmp.Compare(a.role, b.role),
	) < 0
}

func (c city) actor(role string) (cast, bool) {
	for _, a := range c.actors {
		if a.role == role {
			return a, true
		}
	}
	return cast{}, false
}

// routesTo runs Dijkstra on the reversed edges, so duration is the drive
// toward the pickup. That is the same direction the routing-service searches.
func routesTo(pickup int, at map[int]geo.Point, edges []graph.Edge) map[int]route {
	rev := make(map[int][]graph.Edge)
	for _, e := range edges {
		rev[e.To] = append(rev[e.To], e)
	}
	bestDur := map[int]int{pickup: 0}
	bestDist := map[int]int{pickup: 0}
	parent := map[int]int{pickup: pickup}
	h := &costHeap{{node: pickup}}
	heap.Init(h)
	for h.Len() > 0 {
		cur := heap.Pop(h).(cost)
		if cur.dur != bestDur[cur.node] || cur.dist != bestDist[cur.node] {
			continue
		}
		for _, e := range rev[cur.node] {
			nd := cur.dur + e.DurationS
			nm := cur.dist + e.DistanceM
			bd, seen := bestDur[e.From]
			if seen && (nd > bd || (nd == bd && nm >= bestDist[e.From])) {
				continue
			}
			bestDur[e.From] = nd
			bestDist[e.From] = nm
			parent[e.From] = cur.node
			heap.Push(h, cost{node: e.From, dur: nd, dist: nm})
		}
	}
	out := make(map[int]route, len(bestDur))
	for node, dur := range bestDur {
		nodes := []int{node}
		n := node
		for n != pickup {
			next, ok := parent[n]
			if !ok || next == n || len(nodes) > len(bestDur) {
				nodes = nil
				break
			}
			nodes = append(nodes, next)
			n = next
		}
		if len(nodes) == 0 {
			continue
		}
		pts := make([]geo.Point, len(nodes))
		for i, id := range nodes {
			pts[i] = at[id]
		}
		out[node] = route{nodes: nodes, points: pts, distanceM: bestDist[node], durationS: dur}
	}
	return out
}

type cost struct {
	node int
	dur  int
	dist int
}

type costHeap []cost

func (h costHeap) Len() int { return len(h) }
func (h costHeap) Less(i, j int) bool {
	if h[i].dur != h[j].dur {
		return h[i].dur < h[j].dur
	}
	return h[i].dist < h[j].dist
}
func (h costHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *costHeap) Push(v any)   { *h = append(*h, v.(cost)) }
func (h *costHeap) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}

func fit(nodes []graph.Node) projection {
	minLat, maxLat := nodes[0].Lat, nodes[0].Lat
	minLng, maxLng := nodes[0].Lng, nodes[0].Lng
	for _, n := range nodes[1:] {
		minLat = math.Min(minLat, n.Lat)
		maxLat = math.Max(maxLat, n.Lat)
		minLng = math.Min(minLng, n.Lng)
		maxLng = math.Max(maxLng, n.Lng)
	}
	latSpan := math.Max(maxLat-minLat, 1e-6)
	lngSpan := math.Max(maxLng-minLng, 1e-6)
	minLat -= latSpan * 0.16
	maxLat += latSpan * 0.16
	minLng -= lngSpan * 0.12
	maxLng += lngSpan * 0.12
	latSpan = maxLat - minLat
	lngSpan = maxLng - minLng
	midLat := (minLat + maxLat) / 2
	latM := latSpan * 110540
	lngM := lngSpan * 111320 * math.Cos(midLat*math.Pi/180)
	innerW := mapWidth - 48
	innerH := mapHeight - 48
	scale := math.Min(innerW/lngM, innerH/latM)
	usedW := lngM * scale
	usedH := latM * scale
	return projection{
		x0:     (mapWidth - usedW) / 2,
		y0:     (mapHeight - usedH) / 2,
		xScale: usedW / lngSpan,
		yScale: usedH / latSpan,
		minLng: minLng,
		maxLat: maxLat,
	}
}

// along moves from the start of the path toward the pickup. frac is clamped to [0, 1].
func along(path []geo.Point, frac float64) (lat, lng float64) {
	if len(path) == 0 {
		return 0, 0
	}
	if len(path) == 1 || frac <= 0 {
		return path[0].Lat, path[0].Lng
	}
	last := path[len(path)-1]
	if frac >= 1 {
		return last.Lat, last.Lng
	}
	seg := make([]float64, len(path)-1)
	total := 0.0
	for i := range seg {
		seg[i] = geo.Distance(path[i], path[i+1])
		total += seg[i]
	}
	if total == 0 {
		return path[0].Lat, path[0].Lng
	}
	target := frac * total
	for i, length := range seg {
		if length == 0 {
			continue
		}
		if target <= length {
			t := target / length
			return path[i].Lat + (path[i+1].Lat-path[i].Lat)*t, path[i].Lng + (path[i+1].Lng-path[i].Lng)*t
		}
		target -= length
	}
	return last.Lat, last.Lng
}
