package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":8090", "endereço HTTP da apresentação")
	namespace := flag.String("namespace", "nearby-drivers", "namespace Kubernetes")
	kubeContext := flag.String("kube-context", "docker-desktop", "contexto kubectl exigido para escalar o routing")
	ridePort := flag.Int("ride-port", 18182, "porta local do port-forward do ride-service")
	driverPort := flag.Int("driver-port", 18183, "porta local do port-forward do driver-service")
	forward := flag.Bool("forward", true, "abre kubectl port-forward para ride e driver")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	demo, err := run(ctx, log, options{
		addr: *addr, namespace: *namespace, kubeContext: *kubeContext,
		ridePort: *ridePort, driverPort: *driverPort, forward: *forward,
	})
	if demo != nil && demo.needsRestore() {
		log.Info("A apresentação parou com o routing fora. Restaurando.")
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		if rerr := demo.routingUp(rctx); rerr != nil {
			log.Error("não restaurei o routing", "err", rerr)
			log.Info("Rode .\\deploy\\k8s\\up.ps1 -SkipBuild se o routing continuar em 0.")
		}
		cancel()
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
		log.Error("apresentação encerrada", "err", err)
		os.Exit(1)
	}
}

type options struct {
	addr        string
	namespace   string
	kubeContext string
	ridePort    int
	driverPort  int
	forward     bool
}

func run(ctx context.Context, log *slog.Logger, opt options) (*demo, error) {
	graphPath, err := findUp("data/maps/graph.txt")
	if err != nil {
		return nil, err
	}
	manifest, err := findUp("deploy/k8s/routing.yaml")
	if err != nil {
		return nil, err
	}
	loaded, err := loadCity(ctx, graphPath, presentationSpec(time.Now().UnixNano()))
	if err != nil {
		return nil, err
	}
	session, err := sessionPath()
	if err != nil {
		return nil, err
	}
	app := newDemo(loaded, &cluster{
		namespace: opt.namespace,
		context:   opt.kubeContext,
		manifest:  manifest,
	}, newClient("", ""), log, session)
	app.bind(ctx)

	srv := &http.Server{
		Addr:              opt.addr,
		Handler:           app.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("apresentação", "url", listenURL(opt.addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	if opt.forward {
		rideURL := fmt.Sprintf("http://127.0.0.1:%d", opt.ridePort)
		driverURL := fmt.Sprintf("http://127.0.0.1:%d", opt.driverPort)
		if err := portForward(ctx, log, opt.namespace, "ride", opt.ridePort); err != nil {
			app.logf("Sem port-forward do ride: %v", err)
		}
		if err := portForward(ctx, log, opt.namespace, "driver", opt.driverPort); err != nil {
			app.logf("Sem port-forward do driver: %v", err)
		}
		if newClient(rideURL, driverURL).ready(ctx) {
			app.setClient(newClient(rideURL, driverURL))
			app.setConnected(true)
			app.logf("Conectado em %s e %s.", rideURL, driverURL)
		} else {
			app.logf("O cluster não respondeu. Rode make k8s-up e reinicie a apresentação.")
		}
	}
	app.Start(ctx)
	app.logf("Abra %s", listenURL(opt.addr))

	select {
	case err := <-errCh:
		return app, err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil {
		return app, fmt.Errorf("shutdown: %w", err)
	}
	return app, nil
}

func portForward(ctx context.Context, log *slog.Logger, namespace, service string, port int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/readyz", port)
	if ready(ctx, url) {
		log.Info("port-forward já ativo", "service", service, "port", port)
		return nil
	}
	cmd := exec.CommandContext(ctx, "kubectl", "port-forward", "-n", namespace, "svc/"+service, fmt.Sprintf("%d:8080", port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kubectl port-forward %s: %w", service, err)
	}
	go func() {
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ready(ctx, url) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s não respondeu /readyz em 127.0.0.1:%d", service, port)
}

func ready(ctx context.Context, url string) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func listenURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func findUp(rel string) (string, error) {
	if _, err := os.Stat(rel); err == nil {
		return rel, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("não achei %s a partir do diretório atual", rel)
		}
		dir = parent
	}
}

func sessionPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "nearby-drivers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "demo-session-bh.json"), nil
}
