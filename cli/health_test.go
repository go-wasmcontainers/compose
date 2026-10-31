package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/api"
)

func startedAt(t *testing.T, c *api.Client, name string) time.Time {
	t.Helper()
	raw, err := c.Inspect(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	var in struct{ State api.ContainerState }
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339Nano, in.State.StartedAt)
	if err != nil {
		t.Fatalf("%s StartedAt %q: %v", name, in.State.StartedAt, err)
	}
	return ts
}

func healthFile(t *testing.T, dir, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestServiceHealthy: a dependency that becomes ready after 1.5 s is
// probed by its health check (run in the container by the engine); the
// dependent service starts only once it is healthy, which up says. A
// dependency that never gets ready stops up when its check says
// unhealthy; one still starting stops it at --wait-timeout.
func TestServiceHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a wasip1 guest")
	}
	c := engine(t)
	layout := filepath.Join(t.TempDir(), "layout")
	writeLayout(t, layout, "labapp", labapp(t))
	if err := c.Pull(context.Background(), "oci:"+layout+":labapp", func(api.JSONMessage) {}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	healthFile(t, dir, `name: hc
services:
  db:
    image: labapp
    command: [ready-later, "1500", /ready]
    healthcheck:
      test: [CMD, /app.wasm, exists, /ready]
      interval: 200ms
      start_interval: 200ms
      start_period: 30s
      retries: 2
  app:
    image: labapp
    command: [write, /x, started]
    depends_on:
      db: {condition: service_healthy}
`)
	begin := time.Now()
	up := must(t, wc(t, c, dir, "up", "-d"), "Container hc-db-1  Started", "Container hc-db-1  Healthy", "Container hc-app-1  Started")
	if i, j := strings.Index(up, "hc-db-1  Healthy"), strings.Index(up, "hc-app-1  Created"); i < 0 || j < i {
		t.Fatalf("app was created before db was healthy:\n%s", up)
	}
	if took := time.Since(begin); took < 1500*time.Millisecond {
		t.Fatalf("up returned after %s: it did not wait for the check", took)
	}
	if d := startedAt(t, c, "hc-app-1").Sub(startedAt(t, c, "hc-db-1")); d < 1500*time.Millisecond {
		t.Fatalf("app started %s after db, before db could be ready", d)
	}
	must(t, wc(t, c, dir, "down"), "Container hc-app-1  Removed")

	healthFile(t, dir, `name: hu
services:
  db:
    image: labapp
    command: [ready-later, "600000", /ready]
    healthcheck: {test: [CMD, /app.wasm, exists, /ready], interval: 200ms, retries: 2}
  app:
    image: labapp
    command: [write, /x, started]
    depends_on: {db: {condition: service_healthy}}
`)
	r := wc(t, c, dir, "up", "-d")
	if r.failed == nil || !strings.Contains(r.failed.Error(), "hu-db-1 is unhealthy") || !strings.Contains(r.failed.Error(), "not ready") {
		t.Fatalf("%v\n%s", r.failed, r.out)
	}
	must(t, wc(t, c, dir, "down"), "Container hu-db-1  Removed")

	healthFile(t, dir, `name: ht
services:
  db:
    image: labapp
    command: [ready-later, "600000", /ready]
    healthcheck: {test: [CMD, /app.wasm, exists, /ready], interval: 200ms, start_interval: 200ms, start_period: 600s}
  app:
    image: labapp
    command: [write, /x, started]
    depends_on: {db: {condition: service_healthy}}
  opt:
    image: labapp
    command: [write, /x, started]
    depends_on: {db: {condition: service_healthy, required: false}}
`)
	begin = time.Now()
	r = wc(t, c, dir, "up", "-d", "--wait-timeout", "1s", "opt")
	if r.failed != nil || !strings.Contains(r.err, "was not healthy after 1s") || !strings.Contains(r.out, "Container ht-opt-1  Started") {
		t.Fatalf("a dependency that is not required: %v\n%s\n%s", r.failed, r.out, r.err)
	}
	r = wc(t, c, dir, "up", "-d", "--wait-timeout", "1s")
	if r.failed == nil || !strings.Contains(r.failed.Error(), "was not healthy after 1s (status starting") {
		t.Fatalf("%v\n%s", r.failed, r.out)
	}
	if took := time.Since(begin); took > 20*time.Second {
		t.Fatalf("the timeouts took %s", took)
	}
	must(t, wc(t, c, dir, "down"), "Container ht-db-1  Removed")
}

// TestServiceHealthyShell is the coordinator's case with the shell base
// image: CMD-SHELL `ls /ready` (coreutils' ls, started by sh through
// WASIX; a FILE operand is listed since coreutils v0.5.0 — shell 0.1.0
// and 0.2.0 said "not a directory"), and a service that creates /ready
// after a delay with the shell's sleep and touch.
func TestServiceHealthyShell(t *testing.T) {
	if testing.Short() {
		t.Skip("pulls ghcr.io/go-wasmcontainers/shell")
	}
	c := engine(t)
	const shell = "ghcr.io/go-wasmcontainers/shell:0.3.0"
	if err := c.Pull(context.Background(), shell, func(api.JSONMessage) {}); err != nil {
		if os.Getenv("COMPOSE_TEST_OFFLINE") != "" {
			t.Skipf("offline: %v", err)
		}
		t.Fatalf("pull %s (set COMPOSE_TEST_OFFLINE=1 to skip without a network): %v", shell, err)
	}
	dir := t.TempDir()
	healthFile(t, dir, `name: sh
services:
  ready:
    image: `+shell+`
    command: ["/bin/sh", "-c", "sleep 1; touch /ready; sleep 600"]
    healthcheck:
      test: ["CMD-SHELL", "ls /ready"]
      interval: 200ms
      start_interval: 200ms
      start_period: 30s
  app:
    image: `+shell+`
    command: ["/bin/sh", "-c", "echo app started"]
    depends_on:
      ready: {condition: service_healthy}
`)
	res := wc(t, c, dir, "up", "-d")
	if res.failed != nil {
		t.Fatalf("%v\n%s\nlogs:\n%s", res.failed, res.out, wc(t, c, dir, "logs").out)
	}
	up := must(t, res, "Container sh-ready-1  Healthy", "Container sh-app-1  Started")
	if strings.Index(up, "sh-app-1  Created") < strings.Index(up, "sh-ready-1  Healthy") {
		t.Fatalf("app before ready was healthy:\n%s", up)
	}
	if d := startedAt(t, c, "sh-app-1").Sub(startedAt(t, c, "sh-ready-1")); d < time.Second {
		t.Fatalf("app started %s after ready, before /ready existed", d)
	}
	must(t, wc(t, c, dir, "down"), "Container sh-ready-1  Removed")
}
