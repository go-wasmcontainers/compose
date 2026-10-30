package compose

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/api"
)

func TestEngineHealth(t *testing.T) {
	d := func(s string) *time.Duration { v, _ := time.ParseDuration(s); return &v }
	three := int64(3)
	if engineHealth(nil) != nil {
		t.Fatal("no healthcheck: the image's")
	}
	if got := engineHealth(&Healthcheck{Disable: true, Test: []string{"CMD", "x"}}); !reflect.DeepEqual(got, &api.HealthConfig{Test: []string{"NONE"}}) {
		t.Fatalf("%+v", got)
	}
	got := engineHealth(&Healthcheck{Test: []string{"CMD-SHELL", "test -f /ready"}, Interval: d("1m30s"), Timeout: d("10s"),
		StartPeriod: d("40s"), StartInterval: d("5s"), Retries: &three})
	want := &api.HealthConfig{Test: []string{"CMD-SHELL", "test -f /ready"}, Interval: int64(90 * time.Second), Timeout: int64(10 * time.Second),
		StartPeriod: int64(40 * time.Second), StartInterval: int64(5 * time.Second), Retries: 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v\n%+v", got, want)
	}
	if s := lastProbe(&api.Health{Log: []api.HealthcheckResult{{ExitCode: 1, Output: strings.Repeat("x", 300)}}}); !strings.Contains(s, "exited 1") || len(s) > 260 {
		t.Fatalf("%q", s)
	}
	if lastProbe(&api.Health{}) != "" {
		t.Fatal("no probe yet")
	}
}
