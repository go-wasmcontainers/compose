package compose

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestConfigHashAndRequest(t *testing.T) {
	files := map[string]string{"compose.yaml": `
services:
  web:
    image: img
    command: [serve]
    environment: {B: "2", A: "1"}
    labels: {org.example.x: y}
    volumes: ["data:/d:ro"]
    networks: [front, back]
    depends_on: {db: {condition: service_completed_successfully}}
  db: {image: db, network_mode: none}
networks: {front: {}, back: {}}
volumes: {data: {}}
`}
	p := mustLoad(t, files, Options{ProjectName: "h"})
	h1, _ := ConfigHash(p, p.Services["web"])
	p.Services["web"].Environment["A"] = "changed"
	h2, _ := ConfigHash(p, p.Services["web"])
	if h1 == h2 || len(h1) != 64 {
		t.Fatalf("hash did not change with the environment: %s %s", h1, h2)
	}
	p.Volumes["data"].Name = "elsewhere"
	if h3, _ := ConfigHash(p, p.Services["web"]); h3 == h2 {
		t.Fatal("hash did not change with a volume's engine name")
	}

	var errb bytes.Buffer
	r := &Runner{Err: &errb}
	s := p.Services["web"]
	net := r.serviceNetwork(p, s)
	if net != "h_back" || !strings.Contains(errb.String(), `joining "back" only, not front`) {
		t.Fatalf("%q %s", net, errb.String())
	}
	if r.serviceNetwork(p, p.Services["db"]) != "none" {
		t.Fatal("network_mode none")
	}
	req, err := r.createRequest(p, s, 2, net, "sha256:abc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(req.Env, ",") != "A=changed,B=2" || req.Cmd[0] != "serve" || req.Entrypoint != nil {
		t.Fatalf("%+v", req.ContainerConfig)
	}
	if req.HostConfig.Binds[0] != "elsewhere:/d:ro" || req.HostConfig.NetworkMode != "h_back" {
		t.Fatalf("%+v", req.HostConfig)
	}
	l := req.Labels
	if l[LabelProject] != "h" || l[LabelService] != "web" || l[LabelContainerNumber] != "2" || l[LabelImage] != "sha256:abc" ||
		l["org.example.x"] != "y" || l[LabelDependsOn] != "db:service_completed_successfully:false" || l[LabelConfigHash] == "" {
		t.Fatalf("%v", l)
	}
	if got := strings.Join(p.Order(), ","); got != "db,web" {
		t.Fatal(got)
	}
	if p.ContainerName(s, 3) != "h-web-3" {
		t.Fatal(p.ContainerName(s, 3))
	}
	if _, err := p.withDeps([]string{"nope"}); err == nil {
		t.Fatal("unknown service")
	}
	if sel, _ := p.withDeps([]string{"web"}); !sel["db"] || len(sel) != 2 {
		t.Fatal(sel)
	}
}

func TestLineWriter(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	w := &lineWriter{mu: &mu, w: &out, prefix: "p | "}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\nthree"))
	w.flush()
	w.Write(bytes.Repeat([]byte("x"), maxLine+10))
	w.flush()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 5 || lines[0] != "p | one" || lines[1] != "p | two" || lines[2] != "p | three" || len(lines[3]) != maxLine+4 || len(lines[4]) != 14 {
		t.Fatalf("%d lines: %q", len(lines), lines[:3])
	}
}
