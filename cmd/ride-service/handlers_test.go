package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
	"github.com/Matheusmottaa/nearby-drivers/internal/httpsrv"
	"github.com/Matheusmottaa/nearby-drivers/internal/problem"
	"github.com/Matheusmottaa/nearby-drivers/internal/ride"
)

const passenger = "018f3a2b-1111-7abc-8def-0123456789ab"

func TestRideHTTP(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	svc := ride.New(log, ride.NewMemory(), ride.NewMemoryCache(), idleBus{}, 3)
	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)

	body := `{"passenger_id":"` + passenger + `","pickup":{"lat":-23.5613,"lng":-46.6565},"vehicle_class":"standard"}`
	assertProblem(t, do(gate, http.MethodPost, "/rides", body, "idem-1"), http.StatusServiceUnavailable)
	srv.bind(svc)

	assertProblem(t, do(gate, http.MethodPost, "/rides", body, ""), http.StatusBadRequest)

	rec := do(gate, http.MethodPost, "/rides", body, "idem-1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/rides/") {
		t.Fatalf("location = %q", rec.Header().Get("Location"))
	}
	var created acceptedJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != ride.StatusRequested || created.PollAfterMS != ride.PollAfterMS || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}

	again := do(gate, http.MethodPost, "/rides", body, "idem-1")
	if again.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", again.Code, again.Body)
	}
	var same acceptedJSON
	if err := json.Unmarshal(again.Body.Bytes(), &same); err != nil {
		t.Fatal(err)
	}
	if same.ID != created.ID {
		t.Fatalf("replay id = %s, want %s", same.ID, created.ID)
	}
	assertProblem(t, do(gate, http.MethodPost, "/rides", `{"passenger_id":"`+passenger+`","pickup":{"lat":1,"lng":2},"vehicle_class":"comfort"}`, "idem-1"), http.StatusConflict)
	assertProblem(t, do(gate, http.MethodPost, "/rides", `{"passenger_id":"`+passenger+`","pickup":{"lat":91,"lng":0},"vehicle_class":"standard"}`, "idem-2"), http.StatusUnprocessableEntity)

	got := do(gate, http.MethodGet, "/rides/"+created.ID, "", "")
	if got.Code != http.StatusOK {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	if got.Header().Get("ETag") != `"1"` {
		t.Fatalf("etag = %q", got.Header().Get("ETag"))
	}
	notMod := httptest.NewRequest(http.MethodGet, "/rides/"+created.ID, nil)
	notMod.Header.Set("If-None-Match", got.Header().Get("ETag"))
	notMod.Header.Set("X-Request-Id", "req-test")
	notModRec := httptest.NewRecorder()
	gate.Handler().ServeHTTP(notModRec, notMod)
	if notModRec.Code != http.StatusNotModified {
		t.Fatalf("not modified = %d %s", notModRec.Code, notModRec.Body)
	}

	done := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = svc.PublishPending(context.Background())
		close(done)
	}()
	waitRec := do(gate, http.MethodGet, "/rides/"+created.ID+"?wait=3s", "", "")
	<-done
	if waitRec.Code != http.StatusOK || !strings.Contains(waitRec.Body.String(), `"MATCHING"`) {
		t.Fatalf("wait = %d %s", waitRec.Code, waitRec.Body)
	}

	cancelled := do(gate, http.MethodPost, "/rides/"+created.ID+"/cancel", "", "")
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), `"CANCELLED"`) {
		t.Fatalf("cancel = %d %s", cancelled.Code, cancelled.Body)
	}
	repeat := do(gate, http.MethodPost, "/rides/"+created.ID+"/cancel", `{"reason":"again"}`, "")
	if repeat.Code != http.StatusOK || !strings.Contains(repeat.Body.String(), `"CANCELLED"`) {
		t.Fatalf("repeat cancel = %d %s", repeat.Code, repeat.Body)
	}
	assertProblem(t, do(gate, http.MethodGet, "/rides/not-a-uuid", "", ""), http.StatusNotFound)
	assertProblem(t, do(gate, http.MethodGet, "/rides/"+created.ID+"?wait=nope", "", ""), http.StatusUnprocessableEntity)
}

func TestRideSSE(t *testing.T) {
	gate, svc := ready(t)
	body := `{"passenger_id":"` + passenger + `","pickup":{"lat":-23.56,"lng":-46.65},"vehicle_class":"standard"}`
	rec := do(gate, http.MethodPost, "/rides", body, "idem-sse")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var created acceptedJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	httpSrv := httptest.NewServer(gate.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/rides/"+created.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(resp.Body)
	first := readEvent(t, reader)
	if !strings.Contains(first, `"REQUESTED"`) {
		t.Fatalf("first event = %s", first)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = svc.PublishPending(context.Background())
	}()
	second := readEvent(t, reader)
	if !strings.Contains(second, `"MATCHING"`) {
		t.Fatalf("second event = %s", second)
	}
	cancel()
}

func TestAssignThroughHTTPView(t *testing.T) {
	gate, svc := ready(t)
	body := `{"passenger_id":"` + passenger + `","pickup":{"lat":-23.56,"lng":-46.65},"vehicle_class":"standard"}`
	rec := do(gate, http.MethodPost, "/rides", body, "idem-view")
	var created acceptedJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	driverID := "018f9c00-3333-7abc-8def-0123456789ab"
	if err := svc.ApplyMatched(t.Context(), event.Matched{
		EventID: "e", TraceID: "req-test", RideID: current.ID, Attempt: current.Attempt, Engine: event.EngineHaversine,
		Ranking: []event.Candidate{{DriverID: driverID, ETASeconds: 10, DistanceM: 80}},
	}); err != nil {
		t.Fatal(err)
	}
	got := do(gate, http.MethodGet, "/rides/"+created.ID, "", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"DRIVER_ASSIGNED"`) || !strings.Contains(got.Body.String(), `"haversine-fallback"`) {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	cancelled := do(gate, http.MethodPost, "/rides/"+created.ID+"/cancel", "", "")
	if cancelled.Code != http.StatusOK || strings.Contains(cancelled.Body.String(), `"driver"`) {
		t.Fatalf("cancel = %d %s", cancelled.Code, cancelled.Body)
	}
}

func ready(t *testing.T) (*httpsrv.Gate, *ride.Service) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	svc := ride.New(log, ride.NewMemory(), ride.NewMemoryCache(), idleBus{}, 3)
	gate := httpsrv.NewGate()
	srv := newServer(log)
	srv.register(gate)
	srv.bind(svc)
	return gate, svc
}

type idleBus struct{}

func (idleBus) Publish(context.Context, string, string, []byte) error { return nil }

func (idleBus) Consume(ctx context.Context, _ func(context.Context, string, []byte) error) error {
	<-ctx.Done()
	return nil
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
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	var p problem.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != status || p.TraceID != "req-test" || p.Type != "about:blank" {
		t.Fatalf("problem = %+v", p)
	}
}

func readEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v after %q", err, strings.Join(lines, ""))
		}
		if line == "\n" {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "")
}
