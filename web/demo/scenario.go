package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
)

type savedDriver struct {
	Role string `json:"role"`
	ID   string `json:"id"`
}

type savedSession struct {
	Drivers []savedDriver `json:"drivers"`
	Rides   []string      `json:"rides"`
}

func (d *demo) fleet(ctx context.Context) error {
	if err := d.requireAPI(); err != nil {
		return err
	}
	actors, err := d.city.scatter(d.rng, d.city.fleetN, d.city.radiusM, d.city.separationM)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.city.actors = actors
	d.applyActorsLocked(actors)
	d.mu.Unlock()
	if err := d.ensureFleet(ctx); err != nil {
		return err
	}
	if err := d.beat(ctx); err != nil {
		return err
	}
	d.logf("%d carros espalhados a até %.0f m da %s.", len(actors), d.city.radiusM, d.city.label)
	return nil
}

func (d *demo) success(ctx context.Context) error {
	if err := d.ensureRouting(ctx); err != nil {
		return err
	}
	if err := d.waitBreaker(ctx); err != nil {
		return err
	}
	if err := d.prepare(ctx); err != nil {
		return err
	}
	ride, err := d.assign(ctx, "Sucesso", d.city.pickup)
	if err != nil {
		return err
	}
	if ride.Driver != nil && ride.Driver.Engine == event.EngineHaversine {
		d.logf("Breaker ainda aberto. Cancelando e tentando de novo.")
		if _, err := d.client().cancelRide(ctx, ride.ID, "apresentação"); err != nil {
			return err
		}
		if err := d.sleep(ctx, 12*time.Second); err != nil {
			return err
		}
		if err := d.prepare(ctx); err != nil {
			return err
		}
		ride, err = d.assign(ctx, "Sucesso", d.city.pickup)
		if err != nil {
			return err
		}
	}
	return d.expect(ride, event.EngineDijkstra)
}

func (d *demo) race(ctx context.Context) error {
	if err := d.ensureRouting(ctx); err != nil {
		return err
	}
	if err := d.waitBreaker(ctx); err != nil {
		return err
	}
	if err := d.prepare(ctx); err != nil {
		return err
	}
	first, err := d.request(ctx, "Pedido A", d.city.pickup)
	if err != nil {
		return err
	}
	second, err := d.request(ctx, "Pedido B", d.city.pickup)
	if err != nil {
		return err
	}
	left, err := d.waitRide(ctx, first.ID, 90*time.Second, terminalRide)
	if err != nil {
		return err
	}
	right, err := d.waitRide(ctx, second.ID, 90*time.Second, terminalRide)
	if err != nil {
		return err
	}
	if left.Status != "DRIVER_ASSIGNED" || right.Status != "DRIVER_ASSIGNED" || left.Driver == nil || right.Driver == nil {
		return fmt.Errorf("desfechos %s e %s", left.Status, right.Status)
	}
	if left.Driver.ID == right.Driver.ID {
		return errors.New("as duas corridas ficaram com o mesmo motorista")
	}
	d.approach(left.Driver.ID)
	d.approach(right.Driver.ID)
	d.logf("Pedido A: %s. Pedido B: %s.", d.nameLocked(left.Driver.ID), d.nameLocked(right.Driver.ID))
	return nil
}

func (d *demo) routingDown(ctx context.Context) error {
	if d.cluster == nil {
		return errors.New("kubectl não está configurado")
	}
	if err := d.prepare(ctx); err != nil {
		return err
	}
	if err := d.cluster.down(ctx, d.logf); err != nil {
		return err
	}
	d.refreshCluster(ctx)
	if err := d.beat(ctx); err != nil {
		return err
	}
	ride, err := d.assign(ctx, "Routing fora", d.city.pickup)
	if err != nil {
		return err
	}
	return d.expect(ride, event.EngineHaversine)
}

func (d *demo) routingUp(ctx context.Context) error {
	if d.cluster == nil {
		return errors.New("kubectl não está configurado")
	}
	before := d.cluster.inspect(ctx)
	if before.Error != "" {
		return errors.New(before.Error)
	}
	if before.Ready >= 2 && before.HPA && before.Endpoints > 0 && !d.cluster.heldDown() {
		d.refreshCluster(ctx)
		d.logf("Routing já está no ar.")
		return nil
	}
	if err := d.cluster.up(ctx, d.logf); err != nil {
		return err
	}
	d.mu.Lock()
	d.breakerTill = d.now().Add(12 * time.Second)
	d.mu.Unlock()
	d.refreshCluster(ctx)
	d.logf("Routing no ar. A próxima corrida de sucesso espera o breaker fechar.")
	return nil
}

func (d *demo) lost(ctx context.Context) error {
	if err := d.ensureRouting(ctx); err != nil {
		return err
	}
	if err := d.waitBreaker(ctx); err != nil {
		return err
	}
	if err := d.prepare(ctx); err != nil {
		return err
	}
	ride, err := d.assign(ctx, "Rematch", d.city.pickup)
	if err != nil {
		return err
	}
	if ride.Status != "DRIVER_ASSIGNED" || ride.Driver == nil {
		return fmt.Errorf("corrida terminou em %s", ride.Status)
	}
	first := ride.Driver.ID
	firstName := d.nameLocked(first)
	d.approach(first)
	if err := d.sleep(ctx, 2*time.Second); err != nil {
		return err
	}
	d.freeze(first)
	d.logf("Heartbeat de %s parado. O lost sai em cerca de 30 s.", firstName)
	again, err := d.waitRide(ctx, ride.ID, 90*time.Second, func(r rideBody) bool {
		if r.Status == "NO_DRIVER_AVAILABLE" || r.Status == "CANCELLED" {
			return true
		}
		return r.Status == "DRIVER_ASSIGNED" && r.Driver != nil && r.Driver.ID != first
	})
	if err != nil {
		return err
	}
	if again.Status != "DRIVER_ASSIGNED" || again.Driver == nil || again.Driver.ID == first {
		return fmt.Errorf("rematch terminou em %s", again.Status)
	}
	d.approach(again.Driver.ID)
	d.logf("Rematch: %s saiu, %s entrou (%s, %d s).", firstName, d.nameLocked(again.Driver.ID), again.Driver.Engine, again.Driver.ETAS)
	return nil
}

func (d *demo) nobody(ctx context.Context) error {
	if err := d.prepare(ctx); err != nil {
		return err
	}
	ride, err := d.assign(ctx, "Rio", d.city.far)
	if err != nil {
		return err
	}
	if ride.Status != "NO_DRIVER_AVAILABLE" {
		return fmt.Errorf("esperava NO_DRIVER_AVAILABLE, veio %s", ride.Status)
	}
	d.logf("Ninguém por perto: o embarque foi no Rio e a frota está em Belo Horizonte.")
	return nil
}

func (d *demo) cancelRide(ctx context.Context) error {
	if err := d.prepare(ctx); err != nil {
		return err
	}
	ride, err := d.assign(ctx, "Cancelar", d.city.pickup)
	if err != nil {
		return err
	}
	if ride.Status != "DRIVER_ASSIGNED" || ride.Driver == nil {
		return fmt.Errorf("corrida terminou em %s", ride.Status)
	}
	name := d.nameLocked(ride.Driver.ID)
	d.approach(ride.Driver.ID)
	if err := d.sleep(ctx, 3*time.Second); err != nil {
		return err
	}
	if _, err := d.client().cancelRide(ctx, ride.ID, "apresentação"); err != nil {
		return err
	}
	if _, err := d.waitRide(ctx, ride.ID, 20*time.Second, func(r rideBody) bool {
		return r.Status == "CANCELLED"
	}); err != nil {
		return err
	}
	d.homeAll()
	if err := d.beat(ctx); err != nil {
		return err
	}
	d.logf("Corrida cancelada. %s voltou ao ponto.", name)
	return nil
}

func (d *demo) expect(ride rideBody, engine string) error {
	if ride.Status != "DRIVER_ASSIGNED" || ride.Driver == nil {
		return fmt.Errorf("corrida terminou em %s", ride.Status)
	}
	d.approach(ride.Driver.ID)
	want, ok := d.winner(engine)
	if ok && ride.Driver.Engine == engine && ride.Driver.ID == want.id {
		d.logf("Certo: %s, %s, %d s, %d m.", want.name, engine, ride.Driver.ETAS, ride.Driver.DistanceM)
		return nil
	}
	name := want.name
	if name == "" {
		name = "o carro previsto"
	}
	return fmt.Errorf("veio %s com %s; esperava %s com %s", d.nameLocked(ride.Driver.ID), ride.Driver.Engine, name, engine)
}

var errBusy = errors.New("já existe um cenário em andamento")

func (d *demo) setPickup(id int) error {
	d.mu.Lock()
	if d.busy != "" {
		d.mu.Unlock()
		return errBusy
	}
	if err := d.city.retarget(id); err != nil {
		d.mu.Unlock()
		return err
	}
	d.trips = nil
	d.applyActorsLocked(d.city.actors)
	for i := range d.cars {
		if d.cars[i].frozen {
			continue
		}
		d.cars[i].lat = d.cars[i].homeLat
		d.cars[i].lng = d.cars[i].homeLng
	}
	msg := d.pickupMessageLocked()
	d.mu.Unlock()
	d.logf("%s", msg)
	return nil
}

func (d *demo) pickupMessageLocked() string {
	if d.city.pickupID == d.city.originID {
		return fmt.Sprintf("Embarque na %s.", d.city.originLabel)
	}
	nearest := -1.0
	for _, actor := range d.city.actors {
		dist := geo.Distance(geo.Point{Lat: actor.lat, Lng: actor.lng}, d.city.pickup)
		if nearest < 0 || dist < nearest {
			nearest = dist
		}
	}
	if nearest > matchRadiusM {
		return fmt.Sprintf("Embarque no ponto clicado, a %.0f m do carro mais perto. Mostrar a frota traz os carros.", nearest)
	}
	return "Embarque no ponto clicado."
}

func (d *demo) reset(ctx context.Context) error {
	d.mu.Lock()
	d.city.actors = cloneCasts(d.opened)
	if err := d.city.retarget(d.city.originID); err != nil {
		d.mu.Unlock()
		return err
	}
	d.trips = nil
	d.rides = nil
	d.applyActorsLocked(d.city.actors)
	for i := range d.cars {
		d.cars[i].frozen = false
		d.cars[i].lat = d.cars[i].homeLat
		d.cars[i].lng = d.cars[i].homeLng
	}
	label := d.city.originLabel
	d.mu.Unlock()
	d.logf("Volta ao padrão: embarque na %s.", label)
	api := d.client()
	if api != nil && api.ride != "" && len(d.trackedRides()) > 0 {
		if err := d.release(ctx); err != nil {
			return err
		}
		d.mu.Lock()
		d.rides = nil
		d.mu.Unlock()
		return nil
	}
	return d.beat(ctx)
}

func (d *demo) winner(engine string) (car, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return rank(engine, d.cars, d.city.pickup)
}

func (d *demo) applyActorsLocked(actors []cast) {
	for _, actor := range actors {
		i := d.indexOf(actor.role)
		if i < 0 {
			continue
		}
		c := &d.cars[i]
		moving := false
		for _, t := range d.trips {
			if t.driverID == c.id {
				moving = true
				break
			}
		}
		c.node = actor.node
		c.homeLat, c.homeLng = actor.lat, actor.lng
		c.dijkstraS, c.dijkstraM = actor.dijkstraS, actor.dijkstraM
		c.haversineS, c.haversineM = actor.haversineS, actor.haversineM
		c.path = append([]geo.Point(nil), actor.path...)
		if !moving && !c.frozen {
			c.lat, c.lng = actor.lat, actor.lng
		}
	}
}

func (d *demo) ensureRouting(ctx context.Context) error {
	if d.cluster == nil {
		return nil
	}
	view := d.cluster.inspect(ctx)
	if view.Error != "" {
		return errors.New(view.Error)
	}
	d.mu.Lock()
	d.clusterNow = view
	d.mu.Unlock()
	if view.Ready >= 2 && view.HPA && view.Endpoints > 0 && !d.cluster.heldDown() {
		return nil
	}
	d.logf("O routing não está com 2 pods. Religando antes da corrida.")
	if err := d.cluster.up(ctx, d.logf); err != nil {
		return err
	}
	d.mu.Lock()
	d.breakerTill = d.now().Add(12 * time.Second)
	d.mu.Unlock()
	d.refreshCluster(ctx)
	return nil
}

func (d *demo) waitBreaker(ctx context.Context) error {
	d.mu.Lock()
	until := d.breakerTill
	d.mu.Unlock()
	wait := until.Sub(d.now())
	if wait <= 0 {
		return nil
	}
	d.logf("Esperando o circuit breaker fechar (%s).", wait.Round(time.Second))
	return d.sleep(ctx, wait)
}

func (d *demo) prepare(ctx context.Context) error {
	if err := d.requireAPI(); err != nil {
		return err
	}
	if err := d.ensureFleet(ctx); err != nil {
		return err
	}
	if err := d.release(ctx); err != nil {
		return err
	}
	return d.waitAvailable(ctx, 25*time.Second)
}

func (d *demo) requireAPI() error {
	api := d.client()
	if api == nil || api.ride == "" || api.driver == "" {
		return errors.New("ride-service e driver-service ainda não conectaram")
	}
	return nil
}

func (d *demo) ensureFleet(ctx context.Context) error {
	if err := d.loadSession(); err != nil {
		return err
	}
	for _, actor := range d.city.actors {
		if err := d.ensureCar(ctx, actor); err != nil {
			return err
		}
	}
	return nil
}

func (d *demo) ensureCar(ctx context.Context, actor cast) error {
	api := d.client()
	id := d.roleID(actor.role)
	if id != "" {
		got, code, err := api.getDriver(ctx, id)
		if err != nil && code != 404 {
			return err
		}
		if code == 404 {
			d.dropRole(actor.role)
			id = ""
		} else if got.Status != "ON_TRIP" {
			if code, err := api.patchStatus(ctx, id, "AVAILABLE"); err != nil && code != 409 {
				return err
			}
		}
	}
	if id != "" {
		return nil
	}
	idem, err := newID()
	if err != nil {
		return err
	}
	created, err := api.createDriver(ctx, idem, registration{
		Name: actor.name, Phone: "+55119" + randDigits(8), VehicleClass: vehicleClass, Plate: "D" + strings.ToUpper(randHex(6)),
	})
	if err != nil {
		return err
	}
	if _, err := api.patchStatus(ctx, created.ID, "AVAILABLE"); err != nil {
		return err
	}
	d.mu.Lock()
	d.cars = append(d.cars, carFrom(actor, created.ID))
	d.mu.Unlock()
	if err := d.saveSession(); err != nil {
		return err
	}
	d.logf("Motorista %s cadastrado.", actor.name)
	return nil
}

func (d *demo) release(ctx context.Context) error {
	api := d.client()
	for _, id := range d.trackedRides() {
		got, err := api.getRide(ctx, id)
		if err != nil {
			continue
		}
		d.remember("", got)
		if finished(got.Status) {
			continue
		}
		cancelled, err := api.cancelRide(ctx, id, "apresentação")
		if err != nil {
			return err
		}
		d.remember("", cancelled)
		if _, err := d.waitRide(ctx, id, 20*time.Second, func(r rideBody) bool {
			return r.Status == "CANCELLED"
		}); err != nil {
			return err
		}
	}
	d.homeAll()
	d.mu.Lock()
	d.rideIDs = nil
	d.mu.Unlock()
	if err := d.saveSession(); err != nil {
		return err
	}
	return d.beat(ctx)
}

func (d *demo) waitAvailable(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	api := d.client()
	for {
		ready := true
		for _, id := range d.carIDs() {
			got, _, err := api.getDriver(ctx, id)
			if err != nil {
				return err
			}
			d.setStatus(id, got.Status)
			if got.Status != "AVAILABLE" {
				ready = false
			}
		}
		if ready {
			return d.beat(ctx)
		}
		if time.Now().After(deadline) {
			return errors.New("um motorista não voltou para AVAILABLE")
		}
		if err := d.sleep(ctx, d.pollEvery); err != nil {
			return err
		}
	}
}

func (d *demo) assign(ctx context.Context, label string, at geo.Point) (rideBody, error) {
	created, err := d.request(ctx, label, at)
	if err != nil {
		return rideBody{}, err
	}
	return d.waitRide(ctx, created.ID, 90*time.Second, terminalRide)
}

func (d *demo) request(ctx context.Context, label string, at geo.Point) (rideBody, error) {
	idem, err := newID()
	if err != nil {
		return rideBody{}, err
	}
	trace, err := newID()
	if err != nil {
		return rideBody{}, err
	}
	passenger, err := newID()
	if err != nil {
		return rideBody{}, err
	}
	created, err := d.client().createRide(ctx, idem, trace, createRideBody{
		PassengerID: passenger, Pickup: latlng{Lat: at.Lat, Lng: at.Lng}, VehicleClass: vehicleClass,
	})
	if err != nil {
		return rideBody{}, err
	}
	created.Pickup.Lat = at.Lat
	created.Pickup.Lng = at.Lng
	d.remember(label, created)
	d.track(created.ID)
	d.logf("%s aceita.", label)
	return created, nil
}

func (d *demo) waitRide(ctx context.Context, id string, timeout time.Duration, done func(rideBody) bool) (rideBody, error) {
	deadline := time.Now().Add(timeout)
	var last rideBody
	var shown string
	for {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		got, err := d.client().getRide(ctx, id)
		if err != nil {
			return last, err
		}
		last = got
		d.remember("", got)
		line := got.Status
		if got.Driver != nil {
			line = fmt.Sprintf("%s · %s · %s · %d s · %d m", got.Status, d.nameLocked(got.Driver.ID), got.Driver.Engine, got.Driver.ETAS, got.Driver.DistanceM)
		}
		if line != shown {
			d.logf("%s", line)
			shown = line
		}
		if done(got) {
			return got, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("corrida %s parou em %s", shortID(id), got.Status)
		}
		if err := d.sleep(ctx, d.pollEvery); err != nil {
			return last, err
		}
	}
}

func terminalRide(r rideBody) bool {
	return r.Status == "DRIVER_ASSIGNED" || r.Status == "NO_DRIVER_AVAILABLE" || r.Status == "CANCELLED"
}

func finished(status string) bool {
	return status == "CANCELLED" || status == "NO_DRIVER_AVAILABLE"
}

func (d *demo) remember(label string, body rideBody) {
	d.mu.Lock()
	defer d.mu.Unlock()
	view := rideView{
		ID: body.ID, Label: label, Status: body.Status,
		PickupLat: body.Pickup.Lat, PickupLng: body.Pickup.Lng,
	}
	if body.Driver != nil {
		view.DriverID = body.Driver.ID
		view.ETAS = body.Driver.ETAS
		view.DistanceM = body.Driver.DistanceM
		view.Engine = body.Driver.Engine
		view.DriverName = d.nameOf(body.Driver.ID)
	}
	for i := range d.rides {
		if d.rides[i].ID == view.ID {
			if view.Label == "" {
				view.Label = d.rides[i].Label
			}
			if view.PickupLat == 0 && view.PickupLng == 0 {
				view.PickupLat = d.rides[i].PickupLat
				view.PickupLng = d.rides[i].PickupLng
			}
			d.rides[i] = view
			return
		}
	}
	d.rides = append(d.rides, view)
	if len(d.rides) > 8 {
		d.rides = d.rides[len(d.rides)-8:]
	}
}

func (d *demo) approach(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var path []geo.Point
	for i := range d.cars {
		if d.cars[i].id != id {
			continue
		}
		path = d.cars[i].path
		d.cars[i].frozen = false
		d.cars[i].status = "ON_TRIP"
		break
	}
	if len(path) < 2 {
		return
	}
	kept := d.trips[:0]
	for _, t := range d.trips {
		if t.driverID != id {
			kept = append(kept, t)
		}
	}
	d.trips = append(kept, trip{driverID: id, path: path, started: d.now(), duration: approachFor})
}

func (d *demo) freeze(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	kept := d.trips[:0]
	for _, t := range d.trips {
		if t.driverID != id {
			kept = append(kept, t)
		}
	}
	d.trips = kept
	for i := range d.cars {
		if d.cars[i].id == id {
			d.cars[i].frozen = true
		}
	}
}

func (d *demo) homeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trips = nil
	for i := range d.cars {
		d.cars[i].frozen = false
		d.cars[i].status = "AVAILABLE"
		d.cars[i].lat = d.cars[i].homeLat
		d.cars[i].lng = d.cars[i].homeLng
	}
}

func (d *demo) roleID(role string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.cars {
		if c.role == role {
			return c.id
		}
	}
	return ""
}

func (d *demo) nameLocked(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.nameOf(id)
}

func (d *demo) nameOf(id string) string {
	for _, c := range d.cars {
		if c.id == id {
			return c.name
		}
	}
	if id == "" {
		return ""
	}
	return "outro motorista"
}

func (d *demo) carIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.cars))
	for _, c := range d.cars {
		if c.id != "" {
			out = append(out, c.id)
		}
	}
	return out
}

func (d *demo) trackedRides() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.rideIDs...)
}

func (d *demo) track(id string) {
	d.mu.Lock()
	d.rideIDs = append(d.rideIDs, id)
	d.mu.Unlock()
	_ = d.saveSession()
}

func (d *demo) setStatus(id, status string) {
	d.mu.Lock()
	for i := range d.cars {
		if d.cars[i].id == id {
			d.cars[i].status = status
		}
	}
	d.mu.Unlock()
}

func (d *demo) dropRole(role string) {
	d.mu.Lock()
	kept := d.cars[:0]
	for _, c := range d.cars {
		if c.role != role {
			kept = append(kept, c)
		}
	}
	d.cars = kept
	d.mu.Unlock()
}

func (d *demo) isFrozen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.cars {
		if c.id == id {
			return c.frozen
		}
	}
	return false
}

func carFrom(actor cast, id string) car {
	return car{
		role: actor.role, name: actor.name, node: actor.node, id: id,
		homeLat: actor.lat, homeLng: actor.lng, lat: actor.lat, lng: actor.lng,
		dijkstraS: actor.dijkstraS, dijkstraM: actor.dijkstraM,
		haversineS: actor.haversineS, haversineM: actor.haversineM,
		path: actor.path,
	}
}

func (d *demo) loadSession() error {
	d.mu.Lock()
	if d.loaded {
		d.mu.Unlock()
		return nil
	}
	d.loaded = true
	path := d.session
	d.mu.Unlock()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ler sessão: %w", err)
	}
	var saved savedSession
	if err := json.Unmarshal(raw, &saved); err != nil {
		d.logf("Sessão anterior ilegível. Cadastrando a frota de novo.")
		return nil
	}
	d.mu.Lock()
	d.rideIDs = append(d.rideIDs, saved.Rides...)
	for _, item := range saved.Drivers {
		actor, ok := d.city.actor(item.Role)
		if !ok || item.ID == "" {
			continue
		}
		if d.indexOf(item.Role) >= 0 {
			continue
		}
		d.cars = append(d.cars, carFrom(actor, item.ID))
	}
	d.mu.Unlock()
	return nil
}

func (d *demo) indexOf(role string) int {
	for i, c := range d.cars {
		if c.role == role {
			return i
		}
	}
	return -1
}

func (d *demo) saveSession() error {
	d.mu.Lock()
	path := d.session
	saved := savedSession{Rides: append([]string(nil), d.rideIDs...)}
	for _, c := range d.cars {
		if c.id != "" {
			saved.Drivers = append(saved.Drivers, savedDriver{Role: c.role, ID: c.id})
		}
	}
	d.mu.Unlock()
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func randHex(n int) string {
	const digits = "0123456789abcdef"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = digits[buf[i]%16]
	}
	return string(out)
}

func randDigits(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("1", n)
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = '0' + buf[i]%10
	}
	return string(out)
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
