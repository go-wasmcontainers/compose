package compose

import (
	"reflect"
	"strings"
	"testing"
)

// The short-syntax examples of workload_spec.md, ports.
func TestPortsShortSyntax(t *testing.T) {
	src := `services:
  s:
    image: a
    ports:
      - "3000"
      - "3000-3005"
      - "8000:8000"
      - "9090-9091:8080-8081"
      - "49100:22"
      - "8000-9000:80"
      - "127.0.0.1:8001:8001"
      - "127.0.0.1:5000-5010:5000-5010"
      - "::1:6000:6000"
      - "[::1]:6001:6001"
      - "6060:6060/udp"
      - 4000
      - "127.0.0.1::7000"
`
	p := mustLoad(t, map[string]string{"compose.yaml": src}, Options{ProjectName: "pt"})
	want := []Port{
		{Target: "3000"}, {Target: "3000-3005"}, {Target: "8000", Published: "8000"},
		{Target: "8080-8081", Published: "9090-9091"}, {Target: "22", Published: "49100"},
		{Target: "80", Published: "8000-9000"}, {Target: "8001", Published: "8001", HostIP: "127.0.0.1"},
		{Target: "5000-5010", Published: "5000-5010", HostIP: "127.0.0.1"},
		{Target: "6000", Published: "6000", HostIP: "::1"}, {Target: "6001", Published: "6001", HostIP: "::1"},
		{Target: "6060", Published: "6060", Protocol: "udp"}, {Target: "4000"},
		{Target: "7000", HostIP: "127.0.0.1"},
	}
	if got := p.Services["s"].Ports; !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if strings.Contains(strings.Join(p.Warnings, "\n"), "ports") {
		t.Fatalf("ports warned: %v", p.Warnings)
	}
	specs := []string{}
	for _, pt := range p.Services["s"].Ports {
		specs = append(specs, pt.Spec())
	}
	if got := strings.Join(specs[:4], " "); got != "3000/tcp 3000-3005/tcp 8000:8000/tcp 9090-9091:8080-8081/tcp" {
		t.Fatal(got)
	}
	if specs[8] != "[::1]:6000:6000/tcp" || specs[12] != "127.0.0.1::7000/tcp" {
		t.Fatal(specs)
	}
}

func TestPortsLongSyntaxMergeAndRefusals(t *testing.T) {
	p := mustLoad(t, map[string]string{
		"a.yaml": `services:
  s:
    image: a
    ports:
      - name: web
        target: 80
        host_ip: 127.0.0.1
        published: "8080"
        protocol: tcp
        app_protocol: http
        mode: host
      - "9000:90"
    expose: ["3000", "4000-4001/udp"]
`,
		"b.yaml": `services:
  s:
    ports:
      - "127.0.0.1:8080:80"
      - "9100:91"
`,
	}, Options{Files: []string{"a.yaml", "b.yaml"}, ProjectName: "pt"})
	ps := p.Services["s"].Ports
	// The override's 127.0.0.1:8080:80/tcp has the same unique key as
	// the long-syntax entry: it replaces it (13-merge.md).
	if len(ps) != 3 || ps[0].Name != "" || ps[0].Published != "8080" || ps[2].Target != "91" {
		t.Fatalf("%+v", ps)
	}
	if !reflect.DeepEqual(p.Services["s"].Expose, []string{"3000", "4000-4001/udp"}) {
		t.Fatal(p.Services["s"].Expose)
	}
	for _, c := range []struct{ yaml, want string }{
		{"services: {s: {image: a, ports: ['80:80:80:80:x']}}", "invalid"},
		{"services: {s: {image: a, ports: ['99999']}}", "invalid"},
		{"services: {s: {image: a, ports: ['80/sctp']}}", "protocol"},
		{"services: {s: {image: a, ports: [{published: '80'}]}}", "needs a target"},
		{"services: {s: {image: a, ports: [{target: 80, mode: weird}]}}", "mode"},
		{"services: {s: {image: a, network_mode: none, ports: ['80']}}", "network_mode: none"},
		{"services: {s: {image: a, expose: ['80:80']}}", "only container ports"},
	} {
		mustFail(t, map[string]string{"compose.yaml": c.yaml}, Options{ProjectName: "pt"}, c.want)
	}
}
