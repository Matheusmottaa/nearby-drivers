// Programa da etapa 7. Sobe o compose e percorre o roteiro contra o nginx:
// cadastro, atribuição, idempotência, lost/rematch, cancelamento e fallback
// quando as réplicas de routing saem do pool.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	passengerID = "018f3a2b-1111-7abc-8def-0123456789ab"
	pickupLat   = -23.5613
	pickupLng   = -46.6565
	// Nó 6 chega ao embarque em 10 s. Nó 1 chega em 142 s.
	fastLat = -23.5590
	fastLng = -46.6580
	slowLat = -23.5601
	slowLng = -46.6588
)

func main() {
	skipBuild := flag.Bool("skip-build", false, "não reconstrói as imagens")
	skipUp := flag.Bool("skip-up", false, "não recria o compose")
	flag.Parse()
	root := moduleRoot()
	if err := run(root, *skipBuild, *skipUp); err != nil {
		fmt.Fprintf(os.Stderr, "etapa 7: %v\n", err)
		os.Exit(1)
	}
}

func run(root string, skipBuild, skipUp bool) error {
	ctx := context.Background()
	class := fmt.Sprintf("cls%x", time.Now().UnixNano())
	base := "http://127.0.0.1:8080"
	logf("classe desta rodada: %s", class)

	if !skipBuild {
		logf("docker-build das quatro imagens")
		if err := dockerBuild(ctx, root); err != nil {
			return err
		}
	}
	if !skipUp {
		logf("compose up")
		if err := command(ctx, root, "docker", "compose", "up", "-d", "--build", "--force-recreate"); err != nil {
			return err
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if err := waitReady(ctx, client, base); err != nil {
		return err
	}
	if err := assertPublicEdge(client, base); err != nil {
		return err
	}
	if err := assertNoPublishedMatching(ctx, root); err != nil {
		return err
	}
	if err := assertGraphVersion(ctx, root); err != nil {
		return err
	}

	fast, err := registerDriver(client, base, class, "Ana Rapida", plate("A"), fastLat, fastLng)
	if err != nil {
		return err
	}
	slow, err := registerDriver(client, base, class, "Bia Lenta", plate("B"), slowLat, slowLng)
	if err != nil {
		return err
	}
	logf("motoristas %s (perto) e %s (longe)", fast.id, slow.id)

	beatCtx, stopBeats := context.WithCancel(ctx)
	defer stopBeats()
	var seq sync.Mutex
	nextSeq := map[string]int64{fast.id: 2, slow.id: 2}
	send := func(id string, lat, lng float64) {
		seq.Lock()
		n := nextSeq[id]
		nextSeq[id] = n + 1
		seq.Unlock()
		if err := putLocation(client, base, id, lat, lng, n); err != nil {
			logf("localizacao %s: %v", id, err)
		}
	}
	go heartbeat(beatCtx, 4*time.Second, func() { send(fast.id, fastLat, fastLng) })
	go heartbeat(beatCtx, 4*time.Second, func() { send(slow.id, slowLat, slowLng) })

	if err := waitNearby(client, base, class, fast.id, slow.id); err != nil {
		return err
	}

	body := rideBody(class)
	key := "ride-" + class
	created, status, err := postRide(client, base, key, body)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted || created.Status != "REQUESTED" || created.ID == "" {
		return fmt.Errorf("POST /rides = %d %+v, want 202 REQUESTED", status, created)
	}
	logf("corrida %s aceita", created.ID)

	assigned, err := waitStatus(client, base, created.ID, 45*time.Second, func(r rideView) bool {
		return r.Status == "DRIVER_ASSIGNED" && r.Driver != nil && r.Driver.ID == fast.id &&
			r.Driver.Engine == "dijkstra-one-to-many" && r.Driver.ETAS > 0 && r.Driver.DistanceM > 0
	})
	if err != nil {
		return fmt.Errorf("atribuicao dijkstra: %w", err)
	}
	logf("atribuida a %s eta_s=%d distance_m=%d engine=%s", assigned.Driver.ID, assigned.Driver.ETAS, assigned.Driver.DistanceM, assigned.Driver.Engine)

	replay, status, err := postRide(client, base, key, body)
	if err != nil {
		return err
	}
	if status != http.StatusOK || replay.ID != created.ID {
		return fmt.Errorf("idempotencia = %d id %q, want 200 %s", status, replay.ID, created.ID)
	}
	logf("mesma Idempotency-Key devolveu %s", replay.ID)

	logf("parando o heartbeat de %s", fast.id)
	stopBeats()
	onlySlow, stopSlow := context.WithCancel(ctx)
	defer stopSlow()
	go heartbeat(onlySlow, 4*time.Second, func() { send(slow.id, slowLat, slowLng) })

	rematched, err := waitStatus(client, base, created.ID, 60*time.Second, func(r rideView) bool {
		return r.Status == "DRIVER_ASSIGNED" && r.Driver != nil && r.Driver.ID == slow.id
	})
	if err != nil {
		return fmt.Errorf("rematch depois do lost: %w", err)
	}
	logf("rematch atribuiu %s", rematched.Driver.ID)
	if err := assertRematchEvents(ctx, root, created.ID, fast.id); err != nil {
		return err
	}

	cancelled, err := cancelRide(client, base, created.ID)
	if err != nil {
		return err
	}
	if cancelled.Status != "CANCELLED" {
		return fmt.Errorf("cancel = %s, want CANCELLED", cancelled.Status)
	}
	if err := waitNearby(client, base, class, slow.id); err != nil {
		return fmt.Errorf("motorista nao voltou ao nearby: %w", err)
	}
	released, err := getDriver(client, base, slow.id)
	if err != nil {
		return err
	}
	if released.Status != "AVAILABLE" {
		return fmt.Errorf("status depois do cancel = %s, want AVAILABLE", released.Status)
	}
	if err := assertReleased(ctx, root, created.ID, slow.id); err != nil {
		return err
	}
	logf("cancelamento devolveu %s ao nearby", slow.id)

	logf("derrubando as replicas de routing")
	if err := command(ctx, root, "docker", "compose", "stop", "routing-1", "routing-2"); err != nil {
		return err
	}
	if err := waitUpstreamDown(ctx, root); err != nil {
		return err
	}
	fallbackBody := rideBody(class)
	fallbackKey := "fallback-" + class
	fallback, status, err := postRide(client, base, fallbackKey, fallbackBody)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		return fmt.Errorf("POST fallback = %d, want 202", status)
	}
	got, err := waitStatus(client, base, fallback.ID, 45*time.Second, func(r rideView) bool {
		return r.Status == "DRIVER_ASSIGNED" && r.Driver != nil && r.Driver.ID == slow.id &&
			r.Driver.Engine == "haversine-fallback" && r.Driver.ETAS > 0 && r.Driver.DistanceM > 0
	})
	if err != nil {
		return fmt.Errorf("fallback haversine: %w", err)
	}
	logf("sem routing: %s engine=%s eta_s=%d distance_m=%d", got.Driver.ID, got.Driver.Engine, got.Driver.ETAS, got.Driver.DistanceM)
	logf("etapa 7 ok")
	return nil
}

func waitReady(ctx context.Context, client *http.Client, base string) error {
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/nginx-health", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("nginx-health %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("nginx nao ficou pronto: %w", last)
}

func assertPublicEdge(client *http.Client, base string) error {
	for _, path := range []string{"/routes/eta:batch", "/nodes/snap?lat=-23.5613&lng=-46.6565"} {
		resp, err := client.Get(base + path)
		if err != nil {
			return fmt.Errorf("GET %s: %w", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("host publico %s = %d, want 404", path, resp.StatusCode)
		}
	}
	return nil
}

func assertNoPublishedMatching(ctx context.Context, root string) error {
	cmd := exec.CommandContext(ctx, "docker", "compose", "port", "matching-service", "8080")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil || text == "" || strings.Contains(text, "invalid") {
		return nil
	}
	return fmt.Errorf("matching-service publicado em %s", text)
}

func assertGraphVersion(ctx context.Context, root string) error {
	v1, err := snapVersion(ctx, root, "routing-1")
	if err != nil {
		return err
	}
	v2, err := snapVersion(ctx, root, "routing-2")
	if err != nil {
		return err
	}
	if v1 == "" || v1 != v2 {
		return fmt.Errorf("graph_version routing-1=%q routing-2=%q", v1, v2)
	}
	logf("graph_version %s nas duas replicas", v1)
	return nil
}

func snapVersion(ctx context.Context, root, host string) (string, error) {
	out, err := compose(ctx, root, "exec", "-T", "nginx", "wget", "-qO-", "http://"+host+":8080/nodes/snap?lat=-23.5613&lng=-46.6565")
	if err != nil {
		return "", fmt.Errorf("snap %s: %w", host, err)
	}
	var body struct {
		GraphVersion string `json:"graph_version"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return "", fmt.Errorf("snap %s: %w body %s", host, err, out)
	}
	return body.GraphVersion, nil
}

func waitUpstreamDown(ctx context.Context, root string) error {
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := compose(ctx, root, "exec", "-T", "nginx", "cat", "/etc/nginx/upstreams/routing.conf")
		last = string(out)
		if err == nil && strings.Contains(last, "routing-1:8080 down") && strings.Contains(last, "routing-2:8080 down") {
			logf("pool de routing sem replicas prontas")
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("routing nao saiu do pool: %s", last)
}

type driver struct {
	id string
}

type driverView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func registerDriver(client *http.Client, base, class, name, plate string, lat, lng float64) (driver, error) {
	raw := fmt.Sprintf(`{"name":%q,"phone":"11999999999","vehicle_class":%q,"plate":%q}`, name, class, plate)
	req, err := http.NewRequest(http.MethodPost, base+"/drivers", strings.NewReader(raw))
	if err != nil {
		return driver{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "drv-"+plate)
	resp, err := client.Do(req)
	if err != nil {
		return driver{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return driver{}, fmt.Errorf("POST /drivers = %d %s", resp.StatusCode, body)
	}
	var created driverView
	if err := json.Unmarshal(body, &created); err != nil {
		return driver{}, err
	}
	statusRaw := `{"status":"AVAILABLE"}`
	req, err = http.NewRequest(http.MethodPatch, base+"/drivers/"+created.ID+"/status", strings.NewReader(statusRaw))
	if err != nil {
		return driver{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return driver{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return driver{}, fmt.Errorf("PATCH status = %d %s", resp.StatusCode, b)
	}
	if err := putLocation(client, base, created.ID, lat, lng, 1); err != nil {
		return driver{}, err
	}
	return driver{id: created.ID}, nil
}

func putLocation(client *http.Client, base, id string, lat, lng float64, seq int64) error {
	raw := fmt.Sprintf(`{"lat":%g,"lng":%g,"seq":%d,"observed_at":%q,"accuracy_m":8.5}`, lat, lng, seq, time.Now().UTC().Format(time.RFC3339Nano))
	req, err := http.NewRequest(http.MethodPut, base+"/drivers/"+id+"/location", strings.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT location = %d %s", resp.StatusCode, b)
	}
	return nil
}

func heartbeat(ctx context.Context, every time.Duration, send func()) {
	send()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func waitNearby(client *http.Client, base, class string, ids ...string) error {
	deadline := time.Now().Add(45 * time.Second)
	q := fmt.Sprintf("/drivers/nearby?lat=%g&lng=%g&k=10&radius_m=3000&class=%s", pickupLat, pickupLng, class)
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + q)
		if err != nil {
			last = err.Error()
			time.Sleep(time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = fmt.Sprintf("%d %s", resp.StatusCode, body)
		if resp.StatusCode == http.StatusOK && containsAll(string(body), ids) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("nearby sem %v: %s", ids, last)
}

func containsAll(body string, ids []string) bool {
	for _, id := range ids {
		if !strings.Contains(body, id) {
			return false
		}
	}
	return true
}

type accepted struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type rideView struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Driver *driverJSON `json:"driver"`
}

type driverJSON struct {
	ID        string `json:"id"`
	ETAS      int    `json:"eta_s"`
	DistanceM int    `json:"distance_m"`
	Engine    string `json:"eta_engine"`
}

func rideBody(class string) []byte {
	return fmt.Appendf(nil, `{"passenger_id":%q,"pickup":{"lat":%g,"lng":%g},"vehicle_class":%q}`, passengerID, pickupLat, pickupLng, class)
}

func postRide(client *http.Client, base, key string, body []byte) (accepted, int, error) {
	req, err := http.NewRequest(http.MethodPost, base+"/rides", bytes.NewReader(body))
	if err != nil {
		return accepted{}, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := client.Do(req)
	if err != nil {
		return accepted{}, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var got accepted
	if err := json.Unmarshal(raw, &got); err != nil && resp.StatusCode < 300 {
		return accepted{}, resp.StatusCode, fmt.Errorf("decode ride: %w body %s", err, raw)
	}
	if resp.StatusCode >= 300 {
		return got, resp.StatusCode, fmt.Errorf("POST /rides = %d %s", resp.StatusCode, raw)
	}
	return got, resp.StatusCode, nil
}

func waitStatus(client *http.Client, base, id string, timeout time.Duration, want func(rideView) bool) (rideView, error) {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/rides/" + id + "?wait=3s")
		if err != nil {
			last = err.Error()
			time.Sleep(time.Second)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = fmt.Sprintf("%d %s", resp.StatusCode, raw)
		var got rideView
		if resp.StatusCode == http.StatusOK && json.Unmarshal(raw, &got) == nil && want(got) {
			return got, nil
		}
	}
	return rideView{}, fmt.Errorf("timeout em %s: %s", id, last)
}

func cancelRide(client *http.Client, base, id string) (rideView, error) {
	req, err := http.NewRequest(http.MethodPost, base+"/rides/"+id+"/cancel", strings.NewReader(`{"reason":"passenger"}`))
	if err != nil {
		return rideView{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return rideView{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return rideView{}, fmt.Errorf("cancel = %d %s", resp.StatusCode, raw)
	}
	var got rideView
	if err := json.Unmarshal(raw, &got); err != nil {
		return rideView{}, err
	}
	return got, nil
}

func getDriver(client *http.Client, base, id string) (driverView, error) {
	resp, err := client.Get(base + "/drivers/" + id)
	if err != nil {
		return driverView{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return driverView{}, fmt.Errorf("GET driver = %d %s", resp.StatusCode, raw)
	}
	var got driverView
	if err := json.Unmarshal(raw, &got); err != nil {
		return driverView{}, err
	}
	return got, nil
}

func assertRematchEvents(ctx context.Context, root, rideID, excluded string) error {
	released, err := topicMessages(ctx, root, "rides.released")
	if err != nil {
		return err
	}
	if !eventHas(released, rideID, excluded) {
		return fmt.Errorf("rides.released sem %s/%s", rideID, excluded)
	}
	requested, err := topicMessages(ctx, root, "rides.requested")
	if err != nil {
		return err
	}
	for _, raw := range requested {
		var ev struct {
			RideID  string   `json:"ride_id"`
			Attempt int      `json:"attempt"`
			Exclude []string `json:"exclude_driver_ids"`
		}
		if json.Unmarshal([]byte(raw), &ev) != nil {
			continue
		}
		if ev.RideID == rideID && ev.Attempt > 1 && containsAll(raw, []string{excluded}) {
			return nil
		}
	}
	return fmt.Errorf("rides.requested sem rematch excluindo %s", excluded)
}

func assertReleased(ctx context.Context, root, rideID, driverID string) error {
	released, err := topicMessages(ctx, root, "rides.released")
	if err != nil {
		return err
	}
	if !eventHas(released, rideID, driverID) {
		return fmt.Errorf("rides.released do cancelamento sem %s/%s", rideID, driverID)
	}
	return nil
}

func eventHas(lines []string, parts ...string) bool {
	for _, line := range lines {
		if containsAll(line, parts) {
			return true
		}
	}
	return false
}

func topicMessages(ctx context.Context, root, topic string) ([]string, error) {
	out, err := compose(ctx, root, "exec", "-T", "kafka",
		"/opt/kafka/bin/kafka-console-consumer.sh",
		"--bootstrap-server", "localhost:9092",
		"--topic", topic,
		"--from-beginning",
		"--timeout-ms", "8000",
	)
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("consumir %s: %w", topic, err)
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func plate(suffix string) string {
	return fmt.Sprintf("E7%s%04d", suffix, time.Now().UnixNano()%10000)
}

func compose(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w\n%s", strings.Join(cmd.Args, " "), err, out)
	}
	return out, nil
}

func dockerBuild(ctx context.Context, root string) error {
	images := []string{"ride-service", "driver-service", "matching-service", "routing-service"}
	for _, name := range images {
		logf("imagem %s", name)
		if err := command(ctx, root, "docker", "build", "-f", "deploy/"+name+"/Dockerfile", "-t", "nearby-drivers/"+name+":latest", "."); err != nil {
			return err
		}
	}
	return nil
}

func command(ctx context.Context, root, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func logf(format string, args ...any) {
	fmt.Printf("etapa7: "+format+"\n", args...)
}
