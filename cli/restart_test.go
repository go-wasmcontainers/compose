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

const restartCompose = `name: rst
services:
  flaky:
    image: labapp
    command: [fail]
    restart: on-failure:2
  bounded:
    image: labapp
    command: [fail]
    deploy:
      restart_policy: {condition: on-failure, max_attempts: 1}
  forever:
    image: labapp
    command: [write, /x, y]
    restart: always
  never:
    image: labapp
    command: [fail]
    deploy:
      restart_policy: {condition: none}
    restart: always
`

type inspected struct {
	RestartCount int
	State        api.ContainerState
	HostConfig   struct{ RestartPolicy *api.RestartPolicy }
}

func inspect(t *testing.T, c *api.Client, name string) inspected {
	t.Helper()
	raw, err := c.Inspect(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	var in inspected
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// TestRestartPolicies: restart and deploy.restart_policy reach the engine,
// which restarts a failing on-failure:2 container exactly twice, a
// max_attempts: 1 one once, an exiting `always` container again and
// again, and a condition: none one never (restart_policy wins over
// restart, deploy.md).
func TestRestartPolicies(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(restartCompose), 0o644); err != nil {
		t.Fatal(err)
	}
	up := must(t, wc(t, c, dir, "up", "-d"), "Container rst-flaky-1  Started")
	if strings.Contains(up, "WARN") {
		t.Fatal(up)
	}
	for name, want := range map[string]api.RestartPolicy{
		"rst-flaky-1":   {Name: "on-failure", MaximumRetryCount: 2},
		"rst-bounded-1": {Name: "on-failure", MaximumRetryCount: 1},
		"rst-forever-1": {Name: "always"},
		"rst-never-1":   {Name: "no"},
	} {
		if got := inspect(t, c, name).HostConfig.RestartPolicy; got == nil || *got != want {
			t.Fatalf("%s: policy %+v, want %+v", name, got, want)
		}
	}
	// Backoff is 100 ms doubling: two restarts take ~0.3 s, the third of
	// `forever` ~0.7 s. Wait for the bounded ones to settle.
	deadline := time.Now().Add(20 * time.Second)
	for {
		f, b, a := inspect(t, c, "rst-flaky-1"), inspect(t, c, "rst-bounded-1"), inspect(t, c, "rst-forever-1")
		settled := f.RestartCount == 2 && !f.State.Running && !f.State.Restarting &&
			b.RestartCount == 1 && !b.State.Running && !b.State.Restarting && a.RestartCount >= 3
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flaky %d %+v, bounded %d %+v, forever %d", f.RestartCount, f.State, b.RestartCount, b.State, a.RestartCount)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if f := inspect(t, c, "rst-flaky-1"); f.RestartCount != 2 || f.State.ExitCode != 3 {
		t.Fatalf("on-failure:2 went past 2 restarts: %d, exit %d", f.RestartCount, f.State.ExitCode)
	}
	if n := inspect(t, c, "rst-never-1"); n.RestartCount != 0 {
		t.Fatalf("condition: none restarted %d times", n.RestartCount)
	}
	// stop ends `always` until the next start.
	must(t, wc(t, c, dir, "stop", "forever"), "Container rst-forever-1  Stopped")
	before := inspect(t, c, "rst-forever-1").RestartCount
	time.Sleep(time.Second)
	if a := inspect(t, c, "rst-forever-1"); a.RestartCount != before || a.State.Running {
		t.Fatalf("stopped container restarted: %d -> %d, %+v", before, a.RestartCount, a.State)
	}
	must(t, wc(t, c, dir, "down"), "Container rst-forever-1  Removed")
}
