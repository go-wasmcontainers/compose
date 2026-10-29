package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-wasmcontainers/engine/api"
)

// freePort returns a TCP port free on 127.0.0.1 a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// get fetches url from the host, retrying while the guest starts.
func get(t *testing.T, url string) string {
	t.Helper()
	var last error
	for i := 0; i < 100; i++ {
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return string(b)
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, last)
	return ""
}

// TestPublishedPorts: a service's ports, short syntax with and without a
// host address and long syntax, are published by the engine and reach the
// service from the host; the default host address is 127.0.0.1.
func TestPublishedPorts(t *testing.T) {
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
	p1, p2, p3 := freePort(t), freePort(t), freePort(t)
	dir := t.TempDir()
	src := fmt.Sprintf(`name: pub
services:
  web:
    image: labapp
    command: [serve, "80", /none]
    ports:
      - "%d:80"
      - "127.0.0.1:%d:80"
      - target: 80
        published: "%d"
        host_ip: 127.0.0.1
        protocol: tcp
        name: web
`, p1, p2, p3)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	up := must(t, wc(t, c, dir, "up", "-d"), "Container pub-web-1  Started")
	if strings.Contains(up, "WARN") {
		t.Fatal(up)
	}
	for _, p := range []int{p1, p2, p3} {
		body := get(t, fmt.Sprintf("http://127.0.0.1:%d/", p))
		if !strings.Contains(body, "open /none") {
			t.Fatalf("port %d: %q", p, body)
		}
	}
	// "%d:80" names no host address: the engine listens on 127.0.0.1 only.
	ins, err := c.Inspect(ctx, "pub-web-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ins), fmt.Sprintf(`"HostIp":"127.0.0.1","HostPort":"%d"`, p1)) {
		t.Fatalf("port %d is not bound to 127.0.0.1: %s", p1, ins)
	}
	must(t, wc(t, c, dir, "down"), "Container pub-web-1  Removed")
	if _, err := (&http.Client{Timeout: time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/", p1)); err == nil {
		t.Fatal("the port still answers after down")
	}
}
