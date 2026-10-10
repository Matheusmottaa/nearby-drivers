package nginx_test

import (
	"os"
	"strings"
	"testing"
)

func TestPublicHostDoesNotExposeRoutingOrMatching(t *testing.T) {
	conf := read(t, "nginx.conf")
	public, internal, ok := strings.Cut(conf, "listen 8081;")
	if !ok {
		t.Fatal("missing internal listener 8081")
	}
	if strings.Contains(public, "matching") {
		t.Fatal("matching-service is on nginx")
	}
	for _, path := range []string{"location /routes", "location /nodes", "proxy_pass http://routing"} {
		if strings.Contains(public, path) {
			t.Fatalf("public listener contains %s", path)
		}
		if !strings.Contains(internal, path) {
			t.Fatalf("internal listener missing %s", path)
		}
	}
	if !strings.Contains(public, "location /rides") || !strings.Contains(public, "location /drivers") {
		t.Fatal("public listener missing /rides or /drivers")
	}
	if !strings.Contains(internal, "proxy_next_upstream error timeout http_503 non_idempotent") {
		t.Fatal("routing pool does not retry 503")
	}
}

func TestRefreshUsesReadyz(t *testing.T) {
	script := read(t, "refresh-upstreams.sh")
	for _, want := range []string{"/readyz", "routing-1", "routing-2", "ride-service", "driver-service"} {
		if !strings.Contains(script, want) {
			t.Fatalf("refresh script missing %s", want)
		}
	}
	if strings.Contains(script, "matching") {
		t.Fatal("refresh script tracks matching")
	}
}

func TestUpstreams(t *testing.T) {
	ride := read(t, "upstreams/ride.conf")
	driver := read(t, "upstreams/driver.conf")
	routing := read(t, "upstreams/routing.conf")
	if !strings.Contains(ride, "server ride-service:8080;") {
		t.Fatalf("ride upstream = %s", ride)
	}
	if !strings.Contains(driver, "server driver-service:8080;") {
		t.Fatalf("driver upstream = %s", driver)
	}
	if !strings.Contains(routing, "server routing-1:8080;") || !strings.Contains(routing, "server routing-2:8080;") {
		t.Fatalf("routing upstream = %s", routing)
	}
	if strings.Contains(ride+driver+routing, "matching") {
		t.Fatal("matching-service is an nginx upstream")
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
