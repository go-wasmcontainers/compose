package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-wasmcontainers/engine/api"
)

const netCompose = `name: net
services:
  web:
    image: labapp
    command: [serve, "80", /none]
    networks:
      front: {aliases: [www]}
      back: {}
    deploy: {replicas: 2}
  db:
    image: labapp
    command: [serve, "81", /none]
    networks: [back]
  client:
    image: labapp
    command: [get, "http://www:80/"]
    networks: [front]
    depends_on: [web]
  worker:
    image: labapp
    command: [get, "http://web:80/"]
    networks: [back]
    depends_on: [web, db]
  dbcheck:
    image: labapp
    command: [get, "http://db:81/"]
    networks: [back]
    depends_on: [db]
  resolver:
    image: labapp
    command: [lookup, www]
    networks: [front]
    depends_on: [web]
  isolated:
    image: labapp
    command: [probe, "http://db:81/"]
    networks: [front]
    depends_on: [db, client]
networks:
  front: {}
  back: {}
`

// recorder remembers the query of every list request.
type recorder struct {
	next http.RoundTripper
	mu   sync.Mutex
	urls []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet {
		r.mu.Lock()
		r.urls = append(r.urls, req.URL.String())
		r.mu.Unlock()
	}
	return r.next.RoundTrip(req)
}

func exitCode(t *testing.T, c *api.Client, name string) int64 {
	t.Helper()
	w, err := c.Wait(context.Background(), name, "not-running")
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.Result()
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode
}

// TestNetworks: a service on two networks answers to its name on each and
// to its alias where it has one; the alias names every replica; a
// service only on one network cannot reach one only on the other; lists
// are filtered by label on the engine's side.
func TestNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a wasip1 guest")
	}
	base := engine(t)
	hc, b := base.HTTP()
	rec := &recorder{next: hc.Transport}
	c := api.NewClientWith(&http.Client{Transport: rec}, strings.TrimSuffix(b, "/v"+api.APIVersion))
	layout := filepath.Join(t.TempDir(), "layout")
	writeLayout(t, layout, "labapp", labapp(t))
	ctx := context.Background()
	if err := c.Pull(ctx, "oci:"+layout+":labapp", func(api.JSONMessage) {}); err != nil {
		t.Fatal(err)
	}
	// A container of another project, which nothing here may touch.
	foreign, err := c.Create(ctx, "other-x-1", api.ContainerCreateRequest{
		ContainerConfig: api.ContainerConfig{Image: "labapp", Cmd: api.StrSlice{"fail"}, Labels: map[string]string{"com.docker.compose.project": "other"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(netCompose), 0o644); err != nil {
		t.Fatal(err)
	}
	up := must(t, wc(t, c, dir, "up", "-d"), "Network net_front  Created", "Network net_back  Created",
		"Container net-web-1  Started", "Container net-web-2  Started")
	if strings.Contains(up, "WARN") || strings.Contains(wc(t, c, dir, "config", "-q").err, "network") {
		t.Fatal("a networks warning is left:\n" + up)
	}
	for name, want := range map[string]int64{"net-client-1": 0, "net-worker-1": 0, "net-dbcheck-1": 0, "net-isolated-1": 1, "net-resolver-1": 0} {
		if got := exitCode(t, c, name); got != want {
			t.Fatalf("%s exited %d, want %d\n%s", name, got, want, wc(t, c, dir, "logs").out)
		}
	}
	logs := must(t, wc(t, c, dir, "logs"),
		"net-client-1   | got: open /none", // www, the alias, on front
		"net-worker-1   | got: open /none", // web, the service name, on back
		"net-dbcheck-1  | got: open /none")
	if !strings.Contains(logs, "net-isolated-1 | labapp:") {
		t.Fatalf("the front-only service reached db:\n%s", logs)
	}

	// The alias names every replica, in the network's DNS.
	if !strings.Contains(logs, "net-resolver-1 | www has 2 addresses") {
		t.Fatalf("www does not resolve to both replicas:\n%s", logs)
	}
	for _, name := range []string{"net-web-1", "net-web-2"} {
		ins, err := c.Inspect(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		s := string(ins)
		if !strings.Contains(s, `"net_front"`) || !strings.Contains(s, `"net_back"`) || !strings.Contains(s, `"www"`) {
			t.Fatalf("%s is not on both networks with its alias: %s", name, s)
		}
	}

	// Lists were asked with the project's label filter.
	filtered := 0
	rec.mu.Lock()
	for _, u := range rec.urls {
		if strings.Contains(u, "/containers/json") {
			if !strings.Contains(u, "filters=") || !strings.Contains(u, "com.docker.compose.project%3Dnet") {
				rec.mu.Unlock()
				t.Fatalf("an unfiltered list: %s", u)
			}
			filtered++
		}
	}
	rec.mu.Unlock()
	if filtered == 0 {
		t.Fatal("no container list was made")
	}
	if ps := must(t, wc(t, c, dir, "ps", "-a")); strings.Contains(ps, "other-x-1") {
		t.Fatal("ps lists another project's container:\n" + ps)
	}

	must(t, wc(t, c, dir, "down"), "Network net_front  Removed", "Network net_back  Removed")
	if _, err := c.Inspect(ctx, foreign.ID); err != nil {
		t.Fatalf("down removed another project's container: %v", err)
	}
}
