package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigNative(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.hcl", "service \"web\" {\n  image = \"web:${var.TAG}\"\n  cap_add = [\"NET_ADMIN\"]\n}\n")
	write(".env", "TAG=7\n")
	var out, errb bytes.Buffer
	if code := run([]string{"-f", filepath.Join(dir, "compose.hcl"), "config", "--format", "json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"image": "web:7"`) || !strings.Contains(out.String(), `"name": "mylab"`) {
		t.Fatal(out.String())
	}
	if !strings.Contains(errb.String(), "WARN") || !strings.Contains(errb.String(), "cap_add is not supported") {
		t.Fatal(errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"--project-directory", dir, "-p", "x", "config", "--services"}, &out, &errb); code != 0 || out.String() != "web\n" {
		t.Fatalf("%d %q %q", code, out.String(), errb.String())
	}
	// A file outside the project directory is refused.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "compose.yaml"), []byte("services: {s: {image: a}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--project-directory", dir, "-f", filepath.Join(other, "compose.yaml"), "config"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "outside the project directory") {
		t.Fatalf("%d %s", code, errb.String())
	}
	// A symbolic link out of the project is refused too (os.OpenRoot).
	if err := os.Symlink(filepath.Join(other, "compose.yaml"), filepath.Join(dir, "link.yaml")); err != nil {
		t.Skip(err)
	}
	errb.Reset()
	if code := run([]string{"-f", filepath.Join(dir, "link.yaml"), "config", "-q"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "escapes") {
		t.Fatalf("%d %s", code, errb.String())
	}
}

func TestMainExits(t *testing.T) {
	var code int
	osExit = func(c int) { code = c }
	defer func() { osExit = os.Exit }()
	os.Args = []string{"wctr-compose", "no-such-command"}
	main()
	if code != 1 {
		t.Fatal(code)
	}
}
