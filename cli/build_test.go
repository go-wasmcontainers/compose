package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/build/builder"
	"github.com/go-wasmcontainers/build/driver"
	"github.com/go-wasmcontainers/engine/api"
)

// TestDriverBaseForms: imagebuild hands the builder api.Client's base,
// which ends in the API version; build's driver must reach the engine
// with it and without it (build v0.2.0 doubled the version: every
// request was the router's 404).
func TestDriverBaseForms(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a wasip1 guest")
	}
	c := engine(t)
	layout := filepath.Join(t.TempDir(), "layout")
	writeLayout(t, layout, "labapp", labapp(t))
	if err := c.Pull(context.Background(), "oci:"+layout+":labapp", func(api.JSONMessage) {}); err != nil {
		t.Fatal(err)
	}
	hc, base := c.HTTP()
	if !strings.HasSuffix(base, "/v"+api.APIVersion) {
		t.Fatalf("api.Client base %q: the case this test covers is gone", base)
	}
	for _, b := range []string{base, strings.TrimSuffix(base, "/v"+api.APIVersion)} {
		img, err := driver.NewClient(hc, b).InspectImage(context.Background(), "labapp")
		if err != nil || img == nil {
			t.Fatalf("base %q: %v", b, err)
		}
	}
}

const buildCompose = `name: bld
services:
  app:
    build:
      context: app
      args: [GREETING]
      target: final
  inline:
    image: lab/inline:v1
    build:
      context: app
      tags: ["lab/inline:extra"]
      dockerfile_inline: |
        FROM scratch
        COPY labapp.wasm /bin/labapp.wasm
        ENTRYPOINT ["/bin/labapp.wasm", "write", "/x", "inline"]
  other:
    build:
      context: ./app
      dockerfile: ../Other.Containerfile
`

const appContainerfile = `FROM scratch AS base
COPY labapp.wasm /bin/labapp.wasm
ENTRYPOINT ["/bin/labapp.wasm"]

FROM base AS final
ARG GREETING=unset
ENV GREETING=$GREETING
CMD ["env", "GREETING"]

FROM base AS unused
CMD ["fail"]
`

// wcb is wc with an in-memory build cache (the default would be the
// user's cache directory).
func wcb(t *testing.T, c *api.Client, dir string, cache builder.Cache, args ...string) result {
	t.Helper()
	cmd := Command(Options{
		Client:     func() (*api.Client, error) { return c, nil },
		Environ:    func() []string { return []string{"GREETING=from-arg"} },
		BuildCache: cache,
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

// TestBuild builds services with go-wasmcontainers/build through the
// in-process engine: a multi-stage Containerfile with a target and an
// argument from the environment, a dockerfile_inline with tags, a
// dockerfile outside the context (inside the project); then runs them.
func TestBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a wasip1 guest")
	}
	c := engine(t)
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"compose.yaml":         []byte(buildCompose),
		"app/Containerfile":    []byte(appContainerfile),
		"app/labapp.wasm":      labapp(t),
		"app/.containerignore": []byte("secret.txt\n"),
		"app/secret.txt":       []byte("must not reach the engine"),
		"Other.Containerfile":  []byte("FROM scratch\nCOPY labapp.wasm /bin/labapp.wasm\nCOPY secret.txt /secret.txt\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cache := builder.NewMemoryCache(0)

	// The ignore file holds: Other.Containerfile's COPY of secret.txt fails.
	r := wcb(t, c, dir, cache, "build", "other")
	if r.failed == nil || !strings.Contains(r.failed.Error(), "secret.txt") {
		t.Fatalf("an ignored file was copied: %v\n%s", r.failed, r.out)
	}

	must(t, wcb(t, c, dir, cache, "build", "app", "inline"),
		"Image bld-app  Building", "Image bld-app  Built", "Image lab/inline:v1  Built")
	for _, ref := range []string{"bld-app", "lab/inline:v1", "lab/inline:extra"} {
		if _, err := c.ImageInspect(context.Background(), ref); err != nil {
			t.Fatalf("image %s: %v", ref, err)
		}
	}

	// up runs what was built (no pull, no rebuild: the images exist).
	up := must(t, wcb(t, c, dir, cache, "up", "-d", "app", "inline"), "Container bld-app-1  Started", "Container bld-inline-1  Started")
	if strings.Contains(up, "Building") {
		t.Fatal("up rebuilt existing images:\n" + up)
	}
	for _, name := range []string{"bld-app-1", "bld-inline-1"} {
		w, err := c.Wait(context.Background(), name, "not-running")
		if err != nil {
			t.Fatal(err)
		}
		if res, err := w.Result(); err != nil || res.StatusCode != 0 {
			t.Fatalf("%s exited %d %v", name, res.StatusCode, err)
		}
	}
	must(t, wcb(t, c, dir, cache, "logs", "app", "inline"), "bld-app-1    | GREETING=from-arg", "bld-inline-1 | wrote /x")

	// up --build rebuilds (from the cache) and keeps unchanged containers.
	again := must(t, wcb(t, c, dir, cache, "up", "-d", "--build", "app"), "Image bld-app  Built")
	if strings.Contains(again, "Recreate") {
		t.Fatal("a rebuild that changed nothing recreated the container:\n" + again)
	}

	// An argument change gives another image: up --build recreates.
	cmd := Command(Options{
		Client:     func() (*api.Client, error) { return c, nil },
		Environ:    func() []string { return []string{"GREETING=changed"} },
		BuildCache: cache,
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--project-directory", dir, "up", "-d", "--build", "app"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "Container bld-app-1  Recreate") {
		t.Fatal(out.String())
	}
	must(t, wcb(t, c, dir, cache, "down"), "Container bld-app-1  Removed")
}

func TestBuildRefusals(t *testing.T) {
	c := engine(t)
	dir := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services:\n  a: {build: {context: ., dockerfile: ../../etc/passwd}}\n")
	if r := wcb(t, c, dir, builder.NewMemoryCache(0), "build"); r.failed == nil || !strings.Contains(r.failed.Error(), "outside the project directory") {
		t.Fatal(r.failed)
	}
	write("services:\n  a: {build: {context: .}}\n")
	if r := wcb(t, c, dir, builder.NewMemoryCache(0), "build"); r.failed == nil || !strings.Contains(r.failed.Error(), "no Containerfile or Dockerfile") {
		t.Fatal(r.failed)
	}
	write("services:\n  a: {build: 'https://github.com/x/y.git'}\n")
	if r := wcb(t, c, dir, builder.NewMemoryCache(0), "build"); r.failed == nil || !strings.Contains(r.failed.Error(), "remote build contexts are not supported") {
		t.Fatal(r.failed)
	}
	write("services:\n  a: {build: {context: ., dockerfile: X, dockerfile_inline: 'FROM scratch'}}\n")
	if r := wcb(t, c, dir, builder.NewMemoryCache(0), "build"); r.failed == nil || !strings.Contains(r.failed.Error(), "cannot both be set") {
		t.Fatal(r.failed)
	}
}
