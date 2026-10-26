package matching

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

// TestAssignRide needs the compose dependencies and this process as the only
// member of the matchers group:
//
//	RIDE_DATABASE_URL=postgres://ride:ride@localhost:5432/nearby?sslmode=disable
//	DRIVER_DATABASE_URL=postgres://driver:driver@localhost:5432/nearby?sslmode=disable
//	REDIS_URL=redis://localhost:6379
//	KAFKA_BROKERS=localhost:9092
func TestAssignRide(t *testing.T) {
	rideDB := os.Getenv("RIDE_DATABASE_URL")
	driverDB := os.Getenv("DRIVER_DATABASE_URL")
	redisURL := os.Getenv("REDIS_URL")
	brokers := os.Getenv("KAFKA_BROKERS")
	if rideDB == "" || driverDB == "" || redisURL == "" || brokers == "" {
		t.Skip("set RIDE_DATABASE_URL, DRIVER_DATABASE_URL, REDIS_URL and KAFKA_BROKERS")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	root := filepath.Clean(filepath.Join("..", ".."))
	bin := t.TempDir()
	buildServices(t, root, bin)

	routingAddr := freeAddr(t)
	driverAddr := freeAddr(t)
	rideAddr := freeAddr(t)
	matchAddr := freeAddr(t)
	graph := filepath.Join(root, "data", "maps", "fixture.txt")
	matchLog := filepath.Join(bin, "matching.log")

	stopRouting := startService(t, filepath.Join(bin, exeName("routing-service")), filepath.Join(bin, "routing.log"),
		"HTTP_ADDR="+routingAddr, "GRAPH_PATH="+graph, "ROUTING_POOL_SIZE=2")
	defer stopRouting()
	stopDriver := startService(t, filepath.Join(bin, exeName("driver-service")), filepath.Join(bin, "driver.log"),
		"HTTP_ADDR="+driverAddr, "DATABASE_URL="+driverDB, "REDIS_URL="+redisURL, "KAFKA_BROKERS="+brokers)
	defer stopDriver()
	stopRide := startService(t, filepath.Join(bin, exeName("ride-service")), filepath.Join(bin, "ride.log"),
		"HTTP_ADDR="+rideAddr, "DATABASE_URL="+rideDB, "REDIS_URL="+redisURL, "KAFKA_BROKERS="+brokers)
	defer stopRide()
	stopMatch := startService(t, filepath.Join(bin, exeName("matching-service")), matchLog,
		"HTTP_ADDR="+matchAddr, "REDIS_URL="+redisURL, "KAFKA_BROKERS="+brokers,
		"DRIVER_URL=http://"+driverAddr, "ROUTING_URL=http://"+routingAddr,
		"MATCH_K=10", "MATCH_RADIUS_M=3000", "HAVERSINE_SPEED_MPS=8.33")
	defer stopMatch()

	waitReady(ctx, t, "http://"+routingAddr)
	waitReady(ctx, t, "http://"+driverAddr)
	waitReady(ctx, t, "http://"+rideAddr)
	waitReady(ctx, t, "http://"+matchAddr)

	trace := "req-match-" + randHex(t)
	client := &http.Client{Timeout: 15 * time.Second}

	driverID := postDriver(ctx, t, client, "http://"+driverAddr)
	patchJSON(ctx, t, client, "http://"+driverAddr+"/drivers/"+driverID+"/status", `{"status":"AVAILABLE"}`)
	observed, err := time.Now().UTC().MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	patchJSON(ctx, t, client, "http://"+driverAddr+"/drivers/"+driverID+"/location", fmt.Sprintf(
		`{"lat":-23.5601,"lng":-46.6588,"seq":1,"observed_at":%q,"accuracy_m":5}`, string(observed)))

	matched := watchMatched(ctx, t, brokers)
	body := []byte(`{"passenger_id":"018f3a2b-1111-7abc-8def-0123456789ab","pickup":{"lat":-23.5613,"lng":-46.6565},"vehicle_class":"standard"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+rideAddr+"/rides", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "ride-"+trace)
	req.Header.Set("X-Request-Id", trace)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /rides = %d %s", resp.StatusCode, raw)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(25 * time.Second)
	var assigned struct {
		Status string `json:"status"`
		Driver *struct {
			ID        string `json:"id"`
			ETAS      int    `json:"eta_s"`
			DistanceM int    `json:"distance_m"`
			Engine    string `json:"eta_engine"`
		} `json:"driver"`
	}
	for time.Now().Before(deadline) {
		getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+rideAddr+"/rides/"+created.ID+"?wait=3s", nil)
		if err != nil {
			t.Fatal(err)
		}
		getResp, err := client.Do(getReq)
		if err != nil {
			t.Fatal(err)
		}
		getRaw, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("GET /rides = %d %s", getResp.StatusCode, getRaw)
		}
		if err := json.Unmarshal(getRaw, &assigned); err != nil {
			t.Fatal(err)
		}
		if assigned.Status == "DRIVER_ASSIGNED" || assigned.Status == "NO_DRIVER_AVAILABLE" {
			break
		}
	}
	if assigned.Status != "DRIVER_ASSIGNED" || assigned.Driver == nil || assigned.Driver.ID != driverID {
		t.Fatalf("ride = %+v, want DRIVER_ASSIGNED to %s\n%s", assigned, driverID, readLog(t, matchLog))
	}
	if assigned.Driver.Engine != event.EngineDijkstra || assigned.Driver.ETAS <= 0 || assigned.Driver.DistanceM <= 0 {
		t.Fatalf("driver = %+v", assigned.Driver)
	}

	waitEvent := time.After(15 * time.Second)
	for {
		select {
		case got := <-matched:
			if got.TraceID == trace && got.RideID == created.ID {
				logBody := readLog(t, matchLog)
				if !bytes.Contains(logBody, []byte(trace)) {
					t.Fatalf("matching log has no trace_id %s\n%s", trace, logBody)
				}
				return
			}
		case <-waitEvent:
			t.Fatalf("rides.matched missing trace_id %s\n%s", trace, readLog(t, matchLog))
		}
	}
}

func buildServices(t *testing.T, root, bin string) {
	t.Helper()
	for _, name := range []string{"routing-service", "driver-service", "ride-service", "matching-service"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, exeName(name)), "./cmd/"+name)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
	}
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func startService(t *testing.T, bin, logPath string, env ...string) func() {
	t.Helper()
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		_ = logFile.Close()
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitReady(ctx context.Context, t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		if err := ctx.Err(); err != nil {
			t.Fatalf("%s not ready: %v", base, err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s not ready: %v", base, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func postDriver(ctx context.Context, t *testing.T, client *http.Client, base string) string {
	t.Helper()
	body := []byte(`{"name":"Match","phone":"11999999999","vehicle_class":"standard","plate":"` + randHex(t) + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/drivers", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "drv-"+randHex(t))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /drivers = %d %s", resp.StatusCode, raw)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.ID == "" {
		t.Fatalf("driver = %s %v", raw, err)
	}
	return created.ID
}

func patchJSON(ctx context.Context, t *testing.T, client *http.Client, url, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case bytes.Contains([]byte(url), []byte("/status")):
		req.Method = http.MethodPatch
	case bytes.Contains([]byte(url), []byte("/location")):
		req.Method = http.MethodPut
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("%s %s = %d %s", req.Method, url, resp.StatusCode, raw)
	}
}

func watchMatched(ctx context.Context, t *testing.T, brokers string) <-chan event.Matched {
	t.Helper()
	out := make(chan event.Matched, 32)
	ready := make(chan struct{})
	var once sync.Once
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(splitSeeds(brokers)...),
		kgo.ConsumerGroup("matching-observe-"+randHex(t)),
		kgo.ConsumeTopics(event.TopicRidesMatched),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
		kgo.DisableAutoCommit(),
		kgo.OnPartitionsAssigned(func(context.Context, *kgo.Client, map[string][]int32) {
			once.Do(func() { close(ready) })
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	go func() {
		for ctx.Err() == nil {
			fetches := cl.PollFetches(ctx)
			if ctx.Err() != nil {
				return
			}
			fetches.EachRecord(func(r *kgo.Record) {
				got, err := event.Decode[event.Matched](r.Value)
				if err != nil {
					return
				}
				select {
				case out <- got:
				default:
				}
			})
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("matched observer was not assigned")
	}
	return out
}

func randHex(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

func readLog(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
