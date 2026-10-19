package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/driver"
	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/kdtree"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
)

const maxBodyBytes = 1 << 20

type server struct {
	log *slog.Logger
	svc atomic.Pointer[driver.Service]
}

func newServer(log *slog.Logger) *server {
	return &server{log: log}
}

func (s *server) bind(svc *driver.Service) { s.svc.Store(svc) }

func (s *server) register(gate *httpsrv.Gate) {
	gate.SetExtraMetrics(func(w io.Writer) {
		var available int
		if svc := s.svc.Load(); svc != nil {
			available = svc.AvailableDrivers()
		}
		_ = driver.WriteMetrics(w, available)
	})
	gate.Handle("/drivers/nearby", http.HandlerFunc(s.nearby))
	gate.Handle("/drivers/{id}/status", http.HandlerFunc(s.patchStatus))
	gate.Handle("/drivers/{id}/location", http.HandlerFunc(s.putLocation))
	gate.Handle("/drivers/{id}", http.HandlerFunc(s.get))
	gate.Handle("/drivers", http.HandlerFunc(s.create))
}

func (s *server) current(w http.ResponseWriter, r *http.Request) (*driver.Service, bool) {
	svc := s.svc.Load()
	if svc == nil || !svc.Ready() {
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "spatial index is not ready", problem.TraceID(r))
		return nil, false
	}
	return svc, true
}

type driverJSON struct {
	ID            string    `json:"id"`
	InternalID    uint32    `json:"internal_id"`
	Name          string    `json:"name"`
	Phone         string    `json:"phone"`
	VehicleClass  string    `json:"vehicle_class"`
	Plate         string    `json:"plate"`
	Status        string    `json:"status"`
	CurrentRideID *string   `json:"current_ride_id"`
	RegisteredAt  time.Time `json:"registered_at"`
}

func driverBody(d driver.Driver) driverJSON {
	body := driverJSON{
		ID:           d.ID,
		InternalID:   d.InternalID,
		Name:         d.Name,
		Phone:        d.Phone,
		VehicleClass: d.VehicleClass,
		Plate:        d.Plate,
		Status:       d.Status,
		RegisteredAt: d.RegisteredAt.UTC(),
	}
	if d.CurrentRideID != "" {
		id := d.CurrentRideID
		body.CurrentRideID = &id
	}
	return body
}

type registrationJSON struct {
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	VehicleClass string `json:"vehicle_class"`
	Plate        string `json:"plate"`
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodPost {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use POST", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	var req registrationJSON
	if err := decodeJSON(w, r, &req); err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	d, created, err := svc.Create(r.Context(), r.Header.Get("Idempotency-Key"), driver.Registration{
		Name: req.Name, Phone: req.Phone, VehicleClass: req.VehicleClass, Plate: req.Plate,
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, driverBody(d))
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	d, err := svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, driverBody(d))
}

type statusJSON struct {
	Status string `json:"status"`
}

func (s *server) patchStatus(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodPatch {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use PATCH", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	var req statusJSON
	if err := decodeJSON(w, r, &req); err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	d, err := svc.PatchStatus(r.Context(), r.PathValue("id"), req.Status)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, driverBody(d))
}

type locationJSON struct {
	Lat        *float64   `json:"lat"`
	Lng        *float64   `json:"lng"`
	Seq        *int64     `json:"seq"`
	ObservedAt *time.Time `json:"observed_at"`
	AccuracyM  *float64   `json:"accuracy_m"`
}

func (s *server) putLocation(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodPut {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use PUT", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	var req locationJSON
	if err := decodeJSON(w, r, &req); err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	if req.Lat == nil || req.Lng == nil || req.Seq == nil || req.ObservedAt == nil || req.AccuracyM == nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", "lat, lng, seq, observed_at and accuracy_m are required", trace)
		return
	}
	err := svc.PutLocation(r.Context(), r.PathValue("id"), driver.Fix{
		Lat: *req.Lat, Lng: *req.Lng, Seq: *req.Seq, ObservedAt: *req.ObservedAt, AccuracyM: *req.AccuracyM,
	}, trace)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type nearbyJSON struct {
	Drivers       []nearbyDriverJSON `json:"drivers"`
	SnapshotAgeMS int64              `json:"snapshot_age_ms"`
	Index         string             `json:"index"`
}

type nearbyDriverJSON struct {
	ID            string  `json:"id"`
	Lat           float64 `json:"lat"`
	Lng           float64 `json:"lng"`
	StraightLineM int     `json:"straight_line_m"`
	VehicleClass  string  `json:"vehicle_class"`
}

func (s *server) nearby(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	k, errK := strconv.Atoi(q.Get("k"))
	radius, errR := strconv.ParseFloat(q.Get("radius_m"), 64)
	if errLat != nil || errLng != nil || errK != nil || errR != nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", "lat, lng, k and radius_m must be numbers", trace)
		return
	}
	res, err := svc.Nearby(geo.Point{Lat: lat, Lng: lng}, radius, k, q.Get("class"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	body := nearbyJSON{
		Drivers:       make([]nearbyDriverJSON, 0, len(res.Drivers)),
		SnapshotAgeMS: res.SnapshotAge.Milliseconds(),
		Index:         "kdtree",
	}
	for _, d := range res.Drivers {
		body.Drivers = append(body.Drivers, nearbyDriverJSON{
			ID:            d.ID,
			Lat:           d.Lat,
			Lng:           d.Lng,
			StraightLineM: int(math.Round(d.StraightLineM)),
			VehicleClass:  d.VehicleClass,
		})
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	trace := problem.TraceID(r)
	switch {
	case errors.Is(err, driver.ErrIndexNotReady):
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "spatial index is not ready", trace)
	case errors.Is(err, driver.ErrNotFound):
		problem.Write(w, http.StatusNotFound, "not found", "driver not found", trace)
	case errors.Is(err, driver.ErrPlateTaken), errors.Is(err, driver.ErrIllegalStatus):
		problem.Write(w, http.StatusConflict, "conflict", err.Error(), trace)
	case errors.Is(err, driver.ErrIdempotencyKey):
		problem.Write(w, http.StatusBadRequest, "bad request", err.Error(), trace)
	case errors.Is(err, driver.ErrInvalid), errors.Is(err, geo.ErrInvalidCoordinate), errors.Is(err, kdtree.ErrInvalidQuery):
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", err.Error(), trace)
	default:
		problem.Write(w, http.StatusInternalServerError, "internal error", "request failed", trace)
		s.log.Error("request", "err", err, "trace_id", trace, "path", r.URL.Path)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(dest)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
