package compose

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-wasmcontainers/compose/tree"
)

// FuzzLoad feeds Load a Compose file in any of the three formats. A file
// that loads must survive the round trip through every format: config's
// output, read back, is the same model.
func FuzzLoad(f *testing.F) {
	for _, name := range []string{"compose.yaml", "compose.json", "compose.hcl"} {
		data, err := os.ReadFile("testdata/lab/" + name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(name, data)
	}
	f.Add("compose.yaml", []byte("services:\n  s:\n    image: a\n    command: echo \"$${X}\" '%{y}'\n    environment: {K: \"a\\nb\"}\n"))
	f.Add("compose.yaml", []byte("services: {s: {image: a, healthcheck: {test: x, interval: 1.5}, deploy: {replicas: 0}}}\nx-n: [1, 2.5, 1e300, null]\n"))
	f.Fuzz(func(t *testing.T, name string, data []byte) {
		switch name {
		case "compose.yaml", "compose.json", "compose.hcl":
		default:
			return
		}
		lim := tree.Limits{MaxBytes: 1 << 16, MaxNodes: 1 << 12, MaxString: 1 << 12}
		p, err := Load(Options{
			FS: fstest.MapFS{name: {Data: data}}, Files: []string{name}, ProjectName: "fz",
			Environment: map[string]string{"TAG": "1"}, Limits: lim,
		})
		if err != nil {
			return
		}
		for _, f := range []Format{YAML, JSON, HCL} {
			out, err := Marshal(p, f)
			if err != nil {
				// An x- key that is no HCL identifier has no HCL spelling.
				if f == HCL && strings.Contains(err.Error(), "cannot be an HCL attribute name") {
					continue
				}
				t.Fatalf("%s: %v", f, err)
			}
			n := "compose." + string(f)
			back, err := Load(Options{FS: fstest.MapFS{n: {Data: out}}, Files: []string{n}})
			if err != nil {
				t.Fatalf("%s output does not load: %v\n%s", f, err, out)
			}
			if !reflect.DeepEqual(comparable(p), comparable(back)) {
				t.Fatalf("%s round trip changed the model:\n%s", f, out)
			}
		}
	})
}
