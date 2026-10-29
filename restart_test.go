package compose

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestEngineRestart(t *testing.T) {
	two := int64(2)
	for _, c := range []struct {
		s    Service
		name string
		n    int
	}{
		{Service{}, "", 0},
		{Service{Restart: "no"}, "no", 0},
		{Service{Restart: "always"}, "always", 0},
		{Service{Restart: "unless-stopped"}, "unless-stopped", 0},
		{Service{Restart: "on-failure"}, "on-failure", 0},
		{Service{Restart: "on-failure:5"}, "on-failure", 5},
		{Service{Restart: "always", RestartPolicy: &RestartPolicy{Condition: "none"}}, "no", 0},
		{Service{RestartPolicy: &RestartPolicy{Condition: "any"}}, "always", 0},
		{Service{RestartPolicy: &RestartPolicy{Condition: "on-failure", MaxAttempts: &two}}, "on-failure", 2},
		{Service{RestartPolicy: &RestartPolicy{Condition: "on-failure"}}, "on-failure", 0},
	} {
		if name, n := c.s.EngineRestart(); name != c.name || n != c.n {
			t.Errorf("%+v: %q %d, want %q %d", c.s, name, n, c.name, c.n)
		}
	}
}

func TestRestartPolicyLoad(t *testing.T) {
	src := "services:\n  s:\n    image: a\n    restart: always\n    deploy:\n      restart_policy: {condition: on-failure, max_attempts: 3, delay: 5s, window: 120s}\n"
	p := mustLoad(t, map[string]string{"compose.yaml": src}, Options{ProjectName: "r"})
	s := p.Services["s"]
	if s.RestartPolicy == nil || s.RestartPolicy.Condition != "on-failure" || *s.RestartPolicy.MaxAttempts != 3 {
		t.Fatalf("%+v", s.RestartPolicy)
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "restart_policy.delay is not supported") || !strings.Contains(w, "restart_policy.window is not supported") || strings.Contains(w, ".restart is recorded") {
		t.Fatalf("warnings: %s", w)
	}
	for _, f := range []Format{YAML, JSON, HCL} {
		out, err := Marshal(p, f)
		if err != nil {
			t.Fatal(err)
		}
		n := "compose." + string(f)
		back, err := Load(Options{FS: fstest.MapFS{n: {Data: out}}, Files: []string{n}})
		if err != nil {
			t.Fatal(err)
		}
		if b := back.Services["s"]; b.RestartPolicy == nil || *b.RestartPolicy.MaxAttempts != 3 || b.Restart != "always" {
			t.Fatalf("%s: %+v", f, b)
		}
	}
	mustFail(t, map[string]string{"compose.yaml": "services: {s: {image: a, deploy: {restart_policy: {condition: sometimes}}}}"}, Options{ProjectName: "r"}, "condition")
}
