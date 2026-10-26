package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHPAManifest(t *testing.T) {
	manifest, err := hpaManifest(repoFile(t, "deploy/k8s/routing.yaml"), "nearby-drivers")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest, "kind: HorizontalPodAutoscaler") {
		t.Fatalf("manifest missing kind: %s", manifest)
	}
	if !strings.Contains(manifest, "namespace: nearby-drivers") {
		t.Fatalf("manifest missing namespace: %s", manifest)
	}
	if strings.Contains(manifest, "nearby-drivers/routing-service") {
		t.Fatal("manifest includes the deployment")
	}
}

func TestClusterScale(t *testing.T) {
	kube := &fakeKube{contextName: "docker-desktop", ready: 2, hpa: true, ips: "10.0.0.1 10.0.0.2"}
	cl := testCluster(t, kube)
	if err := cl.down(t.Context(), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !cl.heldDown() || kube.ready != 0 || kube.hpa {
		t.Fatalf("after down held=%v ready=%d hpa=%v", cl.heldDown(), kube.ready, kube.hpa)
	}
	if err := cl.up(t.Context(), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if cl.heldDown() || kube.ready != 2 || !kube.hpa {
		t.Fatalf("after up held=%v ready=%d hpa=%v", cl.heldDown(), kube.ready, kube.hpa)
	}
	if !strings.Contains(kube.stdin, "namespace: nearby-drivers") || !strings.Contains(kube.stdin, "kind: HorizontalPodAutoscaler") {
		t.Fatalf("apply stdin = %s", kube.stdin)
	}
}

func TestClusterRefusesForeignContext(t *testing.T) {
	kube := &fakeKube{contextName: "minikube", ready: 2, hpa: true, ips: "10.0.0.1"}
	cl := testCluster(t, kube)
	err := cl.down(t.Context(), func(string, ...any) {})
	if err == nil {
		t.Fatal("down succeeded on the wrong context")
	}
	if kube.hpa == false || kube.ready != 2 {
		t.Fatalf("foreign context changed the cluster: hpa=%v ready=%d", kube.hpa, kube.ready)
	}
	joined := strings.Join(kube.calls, "\n")
	if strings.Contains(joined, "delete") || strings.Contains(joined, "scale") {
		t.Fatalf("issued a mutation: %s", joined)
	}
}

func testCluster(t *testing.T, kube *fakeKube) *cluster {
	t.Helper()
	return &cluster{
		namespace: "nearby-drivers",
		context:   "docker-desktop",
		manifest:  repoFile(t, "deploy/k8s/routing.yaml"),
		run:       kube.run,
		pollEvery: time.Millisecond,
		waitFor:   2 * time.Second,
	}
}

type fakeKube struct {
	mu          sync.Mutex
	contextName string
	ready       int
	hpa         bool
	ips         string
	calls       []string
	stdin       string
}

func (f *fakeKube) run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	if stdin != "" {
		f.stdin = stdin
	}
	switch {
	case strings.Contains(joined, "current-context"):
		return []byte(f.contextName + "\n"), nil
	case strings.Contains(joined, "delete") && strings.Contains(joined, "hpa"):
		f.hpa = false
		return []byte("deleted\n"), nil
	case strings.Contains(joined, "--replicas=0"):
		f.ready = 0
		f.ips = ""
		return []byte("scaled\n"), nil
	case strings.Contains(joined, "--replicas=2"):
		f.ready = 2
		f.ips = "10.0.0.8 10.0.0.9"
		return []byte("scaled\n"), nil
	case strings.Contains(joined, "apply"):
		if !strings.Contains(stdin, "kind: HorizontalPodAutoscaler") || !strings.Contains(stdin, "namespace: nearby-drivers") {
			return nil, fmt.Errorf("bad manifest: %s", stdin)
		}
		f.hpa = true
		return []byte("applied\n"), nil
	case strings.Contains(joined, "readyReplicas"):
		if f.ready == 0 {
			return []byte(""), nil
		}
		return []byte(strconv.Itoa(f.ready)), nil
	case strings.Contains(joined, "hpa"):
		if !f.hpa {
			return []byte(""), nil
		}
		return []byte("horizontalpodautoscaler.autoscaling/routing\n"), nil
	case strings.Contains(joined, "endpoints"):
		return []byte(f.ips), nil
	default:
		return nil, errors.New("unexpected kubectl: " + joined)
	}
}
