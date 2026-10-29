package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-wasmcontainers/build/builder"
	"github.com/go-wasmcontainers/engine/api"
	"github.com/go-wasmcontainers/engine/api/inproc"
	"github.com/go-wasmcontainers/engine/api/server"
	"github.com/go-wasmcontainers/engine/container"
	"github.com/go-wasmcontainers/engine/daemon"
	"github.com/go-wasmcontainers/engine/image"
	"github.com/go-wasmcontainers/engine/portmap"
	"github.com/go-wasmcontainers/engine/runtime/wazero"
)

var (
	guestOnce sync.Once
	guest     []byte
	guestErr  error
)

// labapp builds testdata/wasm/labapp (a stock Go wasip1 program using
// stealthrocket/net) once per test binary.
func labapp(t *testing.T) []byte {
	t.Helper()
	guestOnce.Do(func() {
		dir, err := os.MkdirTemp("", "labapp-")
		if err != nil {
			guestErr = err
			return
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "labapp.wasm")
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", out, ".")
		cmd.Dir = filepath.Join("..", "testdata", "wasm", "labapp")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOWORK=off")
		if msg, err := cmd.CombinedOutput(); err != nil {
			guestErr = err
			guest = msg
			return
		}
		guest, guestErr = os.ReadFile(out)
	})
	if guestErr != nil {
		t.Fatalf("building labapp: %v\n%s", guestErr, guest)
	}
	return guest
}

// writeLayout writes an OCI image layout holding module as the CNCF wasm
// artifact `name` (config application/vnd.wasm.config.v0+json, one
// application/wasm layer).
func writeLayout(t *testing.T, dir, name string, module []byte) {
	t.Helper()
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	add := func(mt string, b []byte) image.Descriptor {
		d := image.FromBytes(b)
		if err := os.WriteFile(filepath.Join(blobs, d.Encoded()), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return image.Descriptor{MediaType: mt, Digest: d, Size: int64(len(b))}
	}
	js := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	layer := add(image.MediaTypeWasmLayer, module)
	layer.Annotations = map[string]string{image.AnnotationTitle: "app.wasm"}
	cfg := add(image.MediaTypeWasmConfig, js(map[string]any{"architecture": "wasm", "os": "wasip1", "layerDigests": []string{string(layer.Digest)}}))
	m := image.Manifest{SchemaVersion: 2, MediaType: image.MediaTypeOCIManifest, Config: cfg, Layers: []image.Descriptor{layer}}
	md := add(image.MediaTypeOCIManifest, js(m))
	md.Annotations = map[string]string{image.AnnotationRefName: name}
	x := image.Index{SchemaVersion: 2, MediaType: image.MediaTypeOCIIndex, Manifests: []image.Descriptor{md}}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), js(x), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// engine starts an in-process engine (wazero, the engine's WASI with
// sockets) and returns an API client reaching it with no socket.
func engine(t *testing.T) *api.Client {
	t.Helper()
	e := wazero.New(wazero.Options{EngineWASI: true})
	// Published ports listen on this machine's sockets, as under wctrd.
	d, err := daemon.New(daemon.Options{Root: filepath.Join(t.TempDir(), "data"), Containers: container.Options{Executor: e, Ports: portmap.OS{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Close(context.Background())
		e.Close(context.Background())
	})
	hc := &http.Client{Transport: &inproc.Transport{Handler: server.New(d, server.Info{Version: "test"})}}
	return api.NewClientWith(hc, "http://engine")
}

type result struct {
	out, err string
	failed   error
}

// wc runs `wctr compose ARGS` in-process against c.
func wc(t *testing.T, c *api.Client, dir string, args ...string) result {
	t.Helper()
	cmd := Command(Options{
		Client:     func() (*api.Client, error) { return c, nil },
		Environ:    func() []string { return []string{"GREETING=hi"} },
		BuildCache: builder.NewMemoryCache(0), // never the user's cache
	})
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"--project-directory", dir}, args...))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return result{out.String(), errb.String(), err}
}

func must(t *testing.T, r result, want ...string) string {
	t.Helper()
	if r.failed != nil {
		t.Fatalf("%v\nstdout:\n%s\nstderr:\n%s", r.failed, r.out, r.err)
	}
	for _, w := range want {
		if !strings.Contains(r.out, w) {
			t.Fatalf("output lacks %q:\n%s\nstderr:\n%s", w, r.out, r.err)
		}
	}
	return r.out
}

const labCompose = `name: lab
services:
  init:
    image: labapp
    command: [write, /data/msg, "hello from a volume"]
    volumes: [data:/data]
  web:
    image: labapp
    command: [serve, "8080", /data/msg]
    environment: [GREETING]
    volumes: ["data:/data:ro"]
    depends_on:
      init: {condition: service_completed_successfully}
  client:
    image: labapp
    command: [get, "http://web:8080/"]
    depends_on: [web]
volumes:
  data: {}
`

// TestLab brings up a three-service application on an in-process engine:
// a one-shot writer fills a volume, a server serves it read-only on the
// project network, a client fetches it by the server's name; then the
// lifecycle commands, then down.
func TestLab(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a wasip1 guest")
	}
	c := engine(t)
	layout := filepath.Join(t.TempDir(), "layout")
	writeLayout(t, layout, "labapp", labapp(t))
	ctx := context.Background()
	if err := c.Pull(ctx, "oci:"+layout+":labapp", func(api.JSONMessage) {}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(labCompose), 0o644); err != nil {
		t.Fatal(err)
	}

	up := must(t, wc(t, c, dir, "up", "-d"),
		"Network lab_default  Created", "Volume lab_data  Created",
		"Container lab-init-1  Started", "Container lab-init-1  Exited (0)",
		"Container lab-web-1  Started", "Container lab-client-1  Started")

	// The client fetched the server by name, through the volume.
	w, err := c.Wait(ctx, "lab-client-1", "not-running")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := w.Result(); err != nil || res.StatusCode != 0 {
		t.Fatalf("client exited %d %v\n%s", res.StatusCode, err, wc(t, c, dir, "logs").out)
	}
	logs := must(t, wc(t, c, dir, "logs", "client"), "lab-client-1 | got: hello from a volume (greeting hi)")
	t.Logf("up -d:\n%slogs client:\n%s", up, logs)
	if strings.Contains(logs, "lab-web-1") {
		t.Fatal("logs client printed another service:\n" + logs)
	}
	must(t, wc(t, c, dir, "logs"), "lab-init-1   | wrote /data/msg", "lab-web-1    | listening on 8080")

	ps := must(t, wc(t, c, dir, "ps"), "NAME", "lab-web-1", "web")
	if strings.Contains(ps, "lab-client-1") {
		t.Fatal("ps without -a lists an exited container:\n" + ps)
	}
	must(t, wc(t, c, dir, "ps", "-a"), "lab-client-1", "lab-init-1")
	if q := must(t, wc(t, c, dir, "ps", "-q")); len(strings.Fields(q)) != 1 {
		t.Fatalf("ps -q: %q", q)
	}
	must(t, wc(t, c, dir, "images"), "CONTAINER", "lab-web-1", "labapp", "latest")

	// Idempotent: nothing changed, nothing is recreated; the running
	// server is kept.
	again := must(t, wc(t, c, dir, "up", "-d", "web"), "Container lab-web-1  Running")
	if strings.Contains(again, "Created") || strings.Contains(again, "Recreate") {
		t.Fatal("second up recreated:\n" + again)
	}
	must(t, wc(t, c, dir, "up", "-d", "--force-recreate", "web"), "Container lab-web-1  Recreate", "Container lab-web-1  Started")

	must(t, wc(t, c, dir, "stop", "web"), "Container lab-web-1  Stopped")
	must(t, wc(t, c, dir, "start", "web"), "Container lab-web-1  Started")
	must(t, wc(t, c, dir, "restart", "web"), "Container lab-web-1  Restarted")
	must(t, wc(t, c, dir, "rm", "client"), "Container lab-client-1  Removed")
	if r := wc(t, c, dir, "rm", "web"); r.failed != nil || strings.Contains(r.out, "lab-web-1") {
		t.Fatalf("rm without -s removed a running container: %v %s", r.failed, r.out)
	}

	// down -v removes containers, the network and the volume.
	must(t, wc(t, c, dir, "down", "-v"), "Container lab-web-1  Removed", "Network lab_default  Removed", "Volume lab_data  Removed")
	if cs, _ := c.Containers(ctx, true); len(cs) != 0 {
		t.Fatalf("containers left: %v", cs)
	}
	if vs, _ := c.Volumes(ctx); len(vs) != 0 {
		t.Fatalf("volumes left: %v", vs)
	}
	nets, _ := c.Networks(ctx)
	for _, n := range nets {
		if strings.HasPrefix(n.Name, "lab") {
			t.Fatalf("network left: %s", n.Name)
		}
	}
}

// TestUpAttached: without -d, up follows the logs until every container
// has exited; interrupted, it stops them. `-f FILE` before the
// subcommand is the file, `-f` after `logs` is --follow.
func TestUpAttached(t *testing.T) {
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
	file := filepath.Join(dir, "one-shot.yaml")
	if err := os.WriteFile(file, []byte("services:\n  a: {image: labapp, command: [fail]}\n  b: {image: labapp, command: [write, /x, y]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must(t, wc(t, c, dir, "-f", file, "-p", "att", "up"), "att-a-1 | failing on purpose", "att-b-1 | wrote /x")
	must(t, wc(t, c, dir, "-f", file, "-p", "att", "logs", "-f", "b"), "att-b-1 | wrote /x")

	srv := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(srv, []byte("services:\n  s: {image: labapp, command: [serve, \"80\", /x]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := Command(Options{Client: func() (*api.Client, error) { return c, nil }, BuildCache: builder.NewMemoryCache(0)})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"-f", srv, "-p", "srv", "up"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Gracefully stopping") || !strings.Contains(out.String(), "srv-s-1  Stopped") {
		t.Fatal(out.String())
	}
	cs, err := c.Containers(context.Background(), false)
	if err != nil || len(cs) != 0 {
		t.Fatalf("still running: %v %v", cs, err)
	}
}

func TestConditionsFailClearly(t *testing.T) {
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
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`services:
  db: {image: labapp, command: [serve, "5432", /nope], healthcheck: {test: [CMD, /bin/true]}}
  app: {image: labapp, command: [fail], depends_on: {db: {condition: service_healthy}}}
`)
	r := wc(t, c, dir, "-p", "h", "up", "-d")
	if r.failed == nil || !strings.Contains(r.failed.Error(), `"app" depends on "db" being healthy`) || !strings.Contains(r.failed.Error(), "does not run health checks yet") {
		t.Fatalf("%v\n%s", r.failed, r.out)
	}
	must(t, wc(t, c, dir, "-p", "h", "down"), "Container h-db-1  Removed")

	write(`services:
  setup: {image: labapp, command: [fail]}
  app: {image: labapp, command: [fail], depends_on: {setup: {condition: service_completed_successfully}}}
`)
	r = wc(t, c, dir, "-p", "s", "up", "-d")
	if r.failed == nil || !strings.Contains(r.failed.Error(), "s-setup-1 exited with 3") {
		t.Fatalf("%v\n%s", r.failed, r.out)
	}
	write(`services:
  b: {build: .}
`)
	if r = wc(t, c, dir, "-p", "b", "up", "-d"); r.failed == nil || !strings.Contains(r.failed.Error(), "no Containerfile or Dockerfile") {
		t.Fatal(r.failed)
	}
	// down by name alone: no Compose file needed.
	os.Remove(filepath.Join(dir, "compose.yaml"))
	must(t, wc(t, c, dir, "-p", "s", "down"), "Container s-setup-1  Removed")
}
