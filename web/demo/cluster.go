package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cluster talks to kubectl for the routing deployment only.
type cluster struct {
	namespace string
	context   string
	manifest  string
	run       func(ctx context.Context, stdin string, args ...string) ([]byte, error)
	pollEvery time.Duration
	waitFor   time.Duration

	mu      sync.Mutex
	hpaGone bool
}

type clusterView struct {
	Known     bool   `json:"known"`
	Context   string `json:"context"`
	Ready     int    `json:"ready"`
	HPA       bool   `json:"hpa"`
	Endpoints int    `json:"endpoints"`
	HeldDown  bool   `json:"held_down"`
	Error     string `json:"error,omitempty"`
}

func (c *cluster) heldDown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hpaGone
}

func (c *cluster) setHeld(down bool) {
	c.mu.Lock()
	c.hpaGone = down
	c.mu.Unlock()
}

func (c *cluster) inspect(ctx context.Context) clusterView {
	view := clusterView{Known: true, HeldDown: c.heldDown()}
	out, err := c.exec(ctx, "", "kubectl", "config", "current-context")
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Context = strings.TrimSpace(string(out))
	if c.context != "" && view.Context != c.context {
		view.Error = fmt.Sprintf("kubectl está em %s; a apresentação usa %s", view.Context, c.context)
		return view
	}
	out, err = c.exec(ctx, "", "kubectl", "get", "deployment", "routing", "-n", c.namespace, "-o", "jsonpath={.status.readyReplicas}")
	if err != nil {
		view.Error = err.Error()
		return view
	}
	ready := strings.TrimSpace(string(out))
	if ready != "" {
		n, conv := strconv.Atoi(ready)
		if conv != nil {
			view.Error = fmt.Sprintf("readyReplicas %q: %v", ready, conv)
			return view
		}
		view.Ready = n
	}
	out, err = c.exec(ctx, "", "kubectl", "get", "hpa", "routing", "-n", c.namespace, "--ignore-not-found", "-o", "name")
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.HPA = strings.TrimSpace(string(out)) != ""
	out, err = c.exec(ctx, "", "kubectl", "get", "endpoints", "routing", "-n", c.namespace, "-o", "jsonpath={.subsets[*].addresses[*].ip}")
	if err != nil {
		view.Error = err.Error()
		return view
	}
	for _, ip := range strings.Fields(string(out)) {
		if ip != "" {
			view.Endpoints++
		}
	}
	return view
}

func (c *cluster) down(ctx context.Context, logf func(string, ...any)) error {
	if err := c.readyContext(ctx); err != nil {
		return err
	}
	logf("Removendo o HPA para o autoscaler não recriar os pods.")
	if _, err := c.exec(ctx, "", "kubectl", "delete", "hpa", "routing", "-n", c.namespace, "--ignore-not-found", "--wait=true"); err != nil {
		return err
	}
	c.setHeld(true)
	logf("Escalando o routing para 0.")
	if _, err := c.exec(ctx, "", "kubectl", "scale", "deployment/routing", "-n", c.namespace, "--replicas=0"); err != nil {
		return err
	}
	return c.wait(ctx, 0, logf)
}

func (c *cluster) up(ctx context.Context, logf func(string, ...any)) error {
	if err := c.readyContext(ctx); err != nil {
		return err
	}
	manifest, err := hpaManifest(c.manifest, c.namespace)
	if err != nil {
		return err
	}
	logf("Recriando o HPA e voltando para 2 réplicas.")
	if _, err := c.exec(ctx, manifest, "kubectl", "apply", "-f", "-"); err != nil {
		return err
	}
	if _, err := c.exec(ctx, "", "kubectl", "scale", "deployment/routing", "-n", c.namespace, "--replicas=2"); err != nil {
		return err
	}
	if err := c.wait(ctx, 2, logf); err != nil {
		return err
	}
	c.setHeld(false)
	return nil
}

func (c *cluster) readyContext(ctx context.Context) error {
	view := c.inspect(ctx)
	if view.Error != "" {
		return errors.New(view.Error)
	}
	return nil
}

func (c *cluster) wait(ctx context.Context, want int, logf func(string, ...any)) error {
	every := c.pollEvery
	if every <= 0 {
		every = 2 * time.Second
	}
	limit := c.waitFor
	if limit <= 0 {
		limit = 180 * time.Second
	}
	deadline := time.Now().Add(limit)
	var last string
	for {
		view := c.inspect(ctx)
		if view.Error != "" && ctx.Err() != nil {
			return ctx.Err()
		}
		line := fmt.Sprintf("routing pronto %d, hpa %s, endpoints %d", view.Ready, simNao(view.HPA), view.Endpoints)
		if view.Error != "" {
			line = view.Error
		}
		if line != last {
			logf("%s", line)
			last = line
		}
		if view.Error == "" && want == 0 && view.Ready == 0 && view.Endpoints == 0 {
			return nil
		}
		if view.Error == "" && want > 0 && view.Ready >= want && view.HPA && view.Endpoints > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("routing não chegou a %d pods prontos", want)
		}
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *cluster) exec(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(ctx, stdin, args...)
	}
	return kubectl(ctx, stdin, args...)
}

func kubectl(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			return nil, fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("%s: %w (%s)", strings.Join(args, " "), err, detail)
	}
	return stdout.Bytes(), nil
}

var docSplit = regexp.MustCompile(`(?m)^---\s*$`)

func hpaManifest(path, namespace string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read routing manifest: %w", err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var found string
	for _, doc := range docSplit.Split(text, -1) {
		if strings.Contains(doc, "kind: HorizontalPodAutoscaler") {
			found = strings.TrimSpace(doc)
			break
		}
	}
	if found == "" {
		return "", fmt.Errorf("no HorizontalPodAutoscaler in %s", path)
	}
	if !strings.Contains(found, "\n  namespace:") {
		found = strings.Replace(found, "metadata:\n", "metadata:\n  namespace: "+namespace+"\n", 1)
	}
	return found + "\n", nil
}

func simNao(v bool) string {
	if v {
		return "sim"
	}
	return "não"
}
