package compose

import (
	"fmt"
	"testing"
	"testing/fstest"
)

// fifty is a project of 50 services, each with an image, a command, five
// environment variables, labels, two networks with an alias, a volume, a
// dependency and a health check: a large lab.
func fifty(tb testing.TB) *Project {
	var src []byte
	src = append(src, "name: big\nservices:\n"...)
	for i := 0; i < 50; i++ {
		src = fmt.Appendf(src, `  svc%02d:
    image: registry.local:5000/lab/svc%02d:${TAG:-1.0}
    command: ["/bin/svc", "--port", "80%02d", "--name", "svc%02d"]
    environment:
      A: "1"
      B: two
      C: ${TAG:-1.0}
      D: "$$literal"
      E: "e"
    labels: {org.example.tier: back, org.example.index: "%d"}
    networks:
      front: {aliases: [s%02d]}
      back: {}
    volumes: [data%02d:/data]
    healthcheck: {test: [CMD, /bin/probe], interval: 5s, retries: 3}
`, i, i, i, i, i, i, i)
		if i > 0 {
			src = fmt.Appendf(src, "    depends_on: {svc%02d: {condition: service_started}}\n", i-1)
		}
	}
	src = append(src, "networks: {front: {}, back: {}}\nvolumes:\n"...)
	for i := 0; i < 50; i++ {
		src = fmt.Appendf(src, "  data%02d: {}\n", i)
	}
	p, err := Load(Options{FS: fstest.MapFS{"compose.yaml": {Data: src}}})
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

func BenchmarkLoad50(b *testing.B) {
	p := fifty(b)
	for _, f := range []Format{YAML, JSON, HCL} {
		data, err := Marshal(p, f)
		if err != nil {
			b.Fatal(err)
		}
		name := "compose." + string(f)
		fsys := fstest.MapFS{name: {Data: data}}
		b.Run(string(f), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Load(Options{FS: fsys, Files: []string{name}}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestFifty(t *testing.T) {
	p := fifty(t)
	if len(p.Services) != 50 || p.Services["svc49"].DependsOn["svc48"].Condition != ServiceStarted {
		t.Fatal(len(p.Services))
	}
}
