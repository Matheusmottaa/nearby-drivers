package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/geo"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
	"github.com/Matheusmottaa/nearby-drivers/internal/ride"
)

const maxBodyBytes = 1 << 20

type server struct {
	log *slog.Logger
	svc atomic.Pointer[ride.Service]
}

func newServer(log *slog.Logger) *server {
	return &server{log: log}
}

func (s *server) bind(svc *ride.Service) { s.svc.Store(svc) }

func (s *server) register(gate *httpsrv.Gate) {
	gate.SetExtraMetrics(func(w io.Writer) {
		var snap ride.Snapshot
		var counts map[string]int
		if svc := s.svc.Load(); svc != nil {
			snap = svc.Snapshot()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			counts, _ = svc.CountByStatus(ctx)
			cancel()
		}
		_ = ride.WriteMetrics(w, snap, counts)
	})
	gate.Handle("/rides/{id}/events", http.HandlerFunc(s.events))
	gate.Handle("/rides/{id}/cancel", http.HandlerFunc(s.cancel))
	gate.Handle("/rides/{id}", http.HandlerFunc(s.get))
	gate.Handle("/rides", http.HandlerFunc(s.create))
}

func (s *server) current(w http.ResponseWriter, r *http.Request) (*ride.Service, bool) {
	svc := s.svc.Load()
	if svc == nil {
		problem.Write(w, http.StatusServiceUnavailable, "service unavailable", "database is not ready", problem.TraceID(r))
		return nil, false
	}
	return svc, true
}

type createJSON struct {
	PassengerID  string      `json:"passenger_id"`
	Pickup       *pickupJSON `json:"pickup"`
	VehicleClass string      `json:"vehicle_class"`
}

type pickupJSON struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

type acceptedJSON struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	PollAfterMS int    `json:"poll_after_ms"`
}

type rideJSON struct {
	ID      string      `json:"id"`
	Status  string      `json:"status"`
	Pickup  pointJSON   `json:"pickup"`
	Driver  *driverJSON `json:"driver,omitempty"`
	Version int         `json:"version"`
}

type pointJSON struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type driverJSON struct {
	ID        string `json:"id"`
	ETAS      int    `json:"eta_s"`
	DistanceM int    `json:"distance_m"`
	Engine    string `json:"eta_engine"`
}

func rideBody(r ride.Ride) rideJSON {
	body := rideJSON{
		ID:      r.ID,
		Status:  r.Status,
		Pickup:  pointJSON{Lat: r.PickupLat, Lng: r.PickupLng},
		Version: r.Version,
	}
	if r.Status == ride.StatusAssigned {
		body.Driver = &driverJSON{
			ID: r.AssignedDriverID, ETAS: r.AssignedETAS, DistanceM: r.AssignedDistanceM, Engine: r.ETAEngine,
		}
	}
	return body
}

func (s *server) create(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	trace := problem.TraceID(r)
	defer s.observe(r, "POST /rides", trace, start)
	if r.Method != http.MethodPost {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use POST", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	var req createJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	if req.Pickup == nil || req.Pickup.Lat == nil || req.Pickup.Lng == nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", "pickup.lat and pickup.lng are required", trace)
		return
	}
	created, fresh, err := svc.Create(r.Context(), r.Header.Get("Idempotency-Key"), raw, ride.Request{
		PassengerID: req.PassengerID, PickupLat: *req.Pickup.Lat, PickupLng: *req.Pickup.Lng,
		VehicleClass: req.VehicleClass, TraceID: trace,
	})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if fresh {
		status = http.StatusAccepted
	}
	w.Header().Set("Location", "/rides/"+created.ID)
	writeJSON(w, status, acceptedJSON{ID: created.ID, Status: created.Status, PollAfterMS: ride.PollAfterMS})
}

func (s *server) get(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	trace := problem.TraceID(r)
	defer s.observe(r, "GET /rides/{id}", trace, start)
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	wait, err := parseWait(r)
	if err != nil {
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", err.Error(), trace)
		return
	}
	got, err := svc.Await(r.Context(), r.PathValue("id"), wait)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	etag := strconv.Itoa(got.Version)
	w.Header().Set("ETag", `"`+etag+`"`)
	if etagMatches(r.Header.Get("If-None-Match"), got.Version) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, rideBody(got))
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodGet {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use GET", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	started := false
	err := svc.Stream(r.Context(), r.PathValue("id"), func(got ride.Ride) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			started = true
		}
		raw, err := json.Marshal(rideBody(got))
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: ride\ndata: %s\n\n", got.Version, raw); err != nil {
			return err
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	})
	if err != nil && r.Context().Err() == nil && !started {
		s.writeErr(w, r, err)
	}
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	trace := problem.TraceID(r)
	if r.Method != http.MethodPost {
		problem.Write(w, http.StatusMethodNotAllowed, "method not allowed", "use POST", trace)
		return
	}
	svc, ok := s.current(w, r)
	if !ok {
		return
	}
	reason, err := cancelReason(w, r)
	if err != nil {
		problem.Write(w, http.StatusBadRequest, "bad request", "body is not valid JSON", trace)
		return
	}
	got, err := svc.Cancel(r.Context(), r.PathValue("id"), reason)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rideBody(got))
}

func cancelReason(w http.ResponseWriter, r *http.Request) (string, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var body struct {
		Reason string `json:"reason"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return "", err
	}
	return body.Reason, nil
}

func parseWait(r *http.Request) (time.Duration, error) {
	q := r.URL.Query().Get("wait")
	if q == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(q)
	if err != nil || d < 0 {
		return 0, errors.New("wait must be a duration up to 3s")
	}
	if d > ride.MaxWait {
		d = ride.MaxWait
	}
	return d, nil
}

func etagMatches(header string, version int) bool {
	if header == "" {
		return false
	}
	quoted := `"` + strconv.Itoa(version) + `"`
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == quoted || part == "W/"+quoted {
			return true
		}
	}
	return false
}

func (s *server) observe(r *http.Request, route, trace string, start time.Time) {
	s.log.Info("http.server.duration",
		"route", route, "duration_ms", time.Since(start).Milliseconds(), "trace_id", trace, "path", r.URL.Path)
}

func (s *server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	trace := problem.TraceID(r)
	switch {
	case errors.Is(err, ride.ErrNotFound):
		problem.Write(w, http.StatusNotFound, "not found", "ride not found", trace)
	case errors.Is(err, ride.ErrIdempotencyConflict), errors.Is(err, ride.ErrNotCancellable):
		problem.Write(w, http.StatusConflict, "conflict", err.Error(), trace)
	case errors.Is(err, ride.ErrIdempotencyKey):
		problem.Write(w, http.StatusBadRequest, "bad request", err.Error(), trace)
	case errors.Is(err, ride.ErrInvalid), errors.Is(err, geo.ErrInvalidCoordinate):
		problem.Write(w, http.StatusUnprocessableEntity, "unprocessable entity", err.Error(), trace)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	default:
		problem.Write(w, http.StatusInternalServerError, "internal error", "request failed", trace)
		s.log.Error("request", "err", err, "trace_id", trace, "path", r.URL.Path)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
