package compose

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// comparable strips what differs between equal models read from
// different files.
func comparable(p *Project) *Project {
	c := *p
	c.Files, c.Warnings, c.Environment, c.WorkingDir = nil, nil, nil, ""
	return &c
}

func loadLab(t *testing.T, file string) *Project {
	t.Helper()
	p, err := Load(Options{FS: os.DirFS("testdata/lab"), Files: []string{file}, Environment: map[string]string{"TAG": "1.2"}})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return p
}

// TestThreeFormatsOneModel is the round trip of docs/HCL.md: the same
// application written in YAML, JSON and HCL2 loads to the same model.
func TestThreeFormatsOneModel(t *testing.T) {
	y := loadLab(t, "compose.yaml")
	for _, f := range []string{"compose.json", "compose.hcl"} {
		got := loadLab(t, f)
		if !reflect.DeepEqual(comparable(y), comparable(got)) {
			a, _ := Marshal(y, YAML)
			b, _ := Marshal(got, YAML)
			t.Fatalf("%s differs from compose.yaml:\n--- yaml\n%s\n--- %s\n%s", f, a, f, b)
		}
	}
	web := y.Services["web"]
	if web.Image != "localhost:5000/web:1.2" {
		t.Errorf("image %q", web.Image)
	}
	if want := (Command{"serve", "--port", "8080", "--greeting", "hello $USER"}); !reflect.DeepEqual(web.Command, want) {
		t.Errorf("command %q", web.Command)
	}
	if web.Environment["TAG"] != "1.2" || web.Environment["MODE"] != "production" {
		t.Errorf("environment %v", web.Environment)
	}
	if web.Replicas != 2 || web.Labels["org.example.lab"] != "true" {
		t.Errorf("replicas %d labels %v", web.Replicas, web.Labels)
	}
	if web.DependsOn["db"] != (Dependency{Condition: ServiceHealthy, Required: true}) {
		t.Errorf("depends_on %v", web.DependsOn)
	}
	if !reflect.DeepEqual(web.Networks["front"].Aliases, []string{"www"}) {
		t.Errorf("networks %v", web.Networks)
	}
	db := y.Services["db"]
	if len(db.Volumes) != 1 || db.Volumes[0] != (Mount{Type: "volume", Source: "data", Target: "/var/lib/db"}) {
		t.Errorf("volumes %v", db.Volumes)
	}
	if h := db.Healthcheck; h == nil || *h.Interval != 5*time.Second || *h.Retries != 3 {
		t.Errorf("healthcheck %+v", db.Healthcheck)
	}
	if y.Networks["front"].Name != "lab_front" || y.Volumes["data"].Name != "lab_data" {
		t.Errorf("names %v %v", y.Networks["front"], y.Volumes["data"])
	}
	if _, ok := y.Networks["default"]; ok {
		t.Error("no service uses the default network, yet it was created")
	}
}

// TestMarshalRoundTrip: config --format X, loaded again, is the model.
func TestMarshalRoundTrip(t *testing.T) {
	p := loadLab(t, "compose.yaml")
	p.Services["web"].Environment["DOLLAR"] = "a $b ${c} $$d %{e}"
	p.Services["web"].Labels["multi"] = "line1\nline2 \"q\""
	for _, f := range []Format{YAML, JSON, HCL} {
		out, err := Marshal(p, f)
		if err != nil {
			t.Fatal(err)
		}
		name := "compose." + string(f)
		back, err := Load(Options{FS: fstest.MapFS{name: {Data: out}}, Files: []string{name}})
		if err != nil {
			t.Fatalf("%s: %v\n%s", f, err, out)
		}
		if !reflect.DeepEqual(comparable(p), comparable(back)) {
			b, _ := Marshal(back, YAML)
			t.Fatalf("%s round trip differs:\n%s\n--- read back as\n%s", f, out, b)
		}
	}
}

func load(t *testing.T, files map[string]string, opt Options) (*Project, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	for k, v := range files {
		fsys[k] = &fstest.MapFile{Data: []byte(v)}
	}
	opt.FS = fsys
	return Load(opt)
}

func mustLoad(t *testing.T, files map[string]string, opt Options) *Project {
	t.Helper()
	p, err := load(t, files, opt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustFail(t *testing.T, files map[string]string, opt Options, want string) {
	t.Helper()
	_, err := load(t, files, opt)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want one containing %q", err, want)
	}
}

func TestDiscoveryAndOverride(t *testing.T) {
	p := mustLoad(t, map[string]string{
		"app/compose.yaml": "services:\n  web:\n    image: a\n    command: [x, y]\n    environment: {A: '1'}\n",
		"app/compose.override.hcl": `service "web" {
  command = ["z"]
  environment = { B = "2" }
}`,
	}, Options{WorkingDir: "app"})
	if p.Name != "app" {
		t.Errorf("name %q", p.Name)
	}
	w := p.Services["web"]
	if !reflect.DeepEqual(w.Command, Command{"z"}) || w.Environment["A"] != "1" || w.Environment["B"] != "2" {
		t.Errorf("%+v", w)
	}
	if !reflect.DeepEqual(p.Files, []string{"app/compose.yaml", "app/compose.override.hcl"}) {
		t.Errorf("files %v", p.Files)
	}
	if p.Networks["default"].Name != "app_default" {
		t.Errorf("default network %+v", p.Networks)
	}
	mustFail(t, map[string]string{"x.txt": ""}, Options{}, "no Compose file found")
}

func TestMergeRules(t *testing.T) {
	p := mustLoad(t, map[string]string{
		"a.yaml": `
services:
  s:
    image: a
    volumes: [v1:/work, v2:/keep]
    ports: ["80:80"]
    labels: {k1: a, k2: b}
    dns: [1.1.1.1]
volumes: {v1: {}, v2: {}, v3: {}}
`,
		"b.yaml": `
services:
  s:
    volumes: [v3:/work]
    labels: {k1: !reset null}
    dns: !override [8.8.8.8]
`,
	}, Options{Files: []string{"a.yaml", "b.yaml"}, ProjectName: "m"})
	s := p.Services["s"]
	if len(s.Volumes) != 2 || s.Volumes[0].Source != "v3" || s.Volumes[1].Source != "v2" {
		t.Errorf("volumes merged by target: %+v", s.Volumes)
	}
	if _, ok := s.Labels["k1"]; ok || s.Labels["k2"] != "b" {
		t.Errorf("labels %v", s.Labels)
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "ports is not supported") {
		t.Errorf("warnings %v", p.Warnings)
	}
}

func TestInterpolation(t *testing.T) {
	files := map[string]string{
		".env":         "TAG=from-dotenv\nOTHER=${TAG}-x\n",
		"compose.yaml": "services:\n  s:\n    image: img:${TAG}\n    working_dir: /${OTHER}\n    command: echo $$HOME ${UNSET}\n",
	}
	p := mustLoad(t, files, Options{ProjectName: "i"})
	if s := p.Services["s"]; s.Image != "img:from-dotenv" || s.WorkingDir != "/from-dotenv-x" || !reflect.DeepEqual(s.Command, Command{"echo", "$HOME"}) {
		t.Errorf("%+v", s)
	}
	if !strings.Contains(strings.Join(p.Warnings, "\n"), `"UNSET" variable is not set`) {
		t.Errorf("warnings %v", p.Warnings)
	}
	p = mustLoad(t, files, Options{ProjectName: "i", Environment: map[string]string{"TAG": "explicit"}})
	if s := p.Services["s"]; s.Image != "img:explicit" || s.WorkingDir != "/explicit-x" {
		t.Errorf("explicit environment wins: %+v", s)
	}
	mustFail(t, map[string]string{"compose.yaml": "services:\n  s:\n    image: ${X:?X is required}\n"}, Options{ProjectName: "i"}, "compose.yaml:3:12: required variable X is missing a value: X is required")
}

func TestProjectName(t *testing.T) {
	f := map[string]string{"My App/compose.yaml": "name: ${N:-fromfile}\nservices: {s: {image: a, environment: [P=$COMPOSE_PROJECT_NAME]}}\n"}
	if p := mustLoad(t, f, Options{WorkingDir: "My App"}); p.Name != "fromfile" || p.Services["s"].Environment["P"] != "fromfile" {
		t.Errorf("%q %v", p.Name, p.Services["s"].Environment)
	}
	if p := mustLoad(t, f, Options{WorkingDir: "My App", ProjectName: "flag"}); p.Name != "flag" || p.Services["s"].Environment["P"] != "flag" {
		t.Errorf("%q", p.Name)
	}
	if p := mustLoad(t, map[string]string{"My App/compose.yaml": "services: {s: {image: a}}\n"}, Options{WorkingDir: "My App"}); p.Name != "myapp" {
		t.Errorf("%q", p.Name)
	}
	mustFail(t, f, Options{WorkingDir: "My App", ProjectName: "Bad Name"}, "invalid project name")
}

func TestProfiles(t *testing.T) {
	// 15-profiles.md, the illustrative example.
	f := map[string]string{"compose.yaml": `
services:
  foo: {image: foo}
  bar: {image: bar, profiles: [test]}
  baz: {image: baz, depends_on: [bar], profiles: [test]}
  zot: {image: zot, depends_on: [bar], profiles: [debug]}
`}
	names := func(p *Project) string { return strings.Join(p.ServiceNames(), ",") }
	if p := mustLoad(t, f, Options{ProjectName: "p"}); names(p) != "foo" {
		t.Error(names(p))
	}
	if p := mustLoad(t, f, Options{ProjectName: "p", Profiles: []string{"test"}}); names(p) != "bar,baz,foo" {
		t.Error(names(p))
	}
	mustFail(t, f, Options{ProjectName: "p", Profiles: []string{"debug"}}, `"zot" depends on "bar", which is not enabled`)
	if p := mustLoad(t, f, Options{ProjectName: "p", Profiles: []string{"debug", "test"}}); names(p) != "bar,baz,foo,zot" {
		t.Error(names(p))
	}
	if p := mustLoad(t, f, Options{ProjectName: "p", Environment: map[string]string{"COMPOSE_PROFILES": "test"}}); names(p) != "bar,baz,foo" {
		t.Error(names(p))
	}
}

func TestExtendsAndInclude(t *testing.T) {
	p := mustLoad(t, map[string]string{
		"compose.yaml": `
include:
  - lib/compose.yaml
services:
  base: {image: busybox, environment: {TZ: utc, PORT: "80"}}
  cli:
    extends: base
    environment: {PORT: "8080"}
  far:
    extends: {file: common.yaml, service: webapp}
    depends_on: [included]
`,
		"common.yaml": "services:\n  webapp:\n    image: web\n    env_file: web.env\n",
		"web.env":     "FROM_FILE=yes\n",
		"lib/compose.yaml": `
services:
  included: {image: inc, env_file: lib.env}
`,
		"lib/lib.env": "L=${LIBVAR}\n",
		"lib/.env":    "LIBVAR=from-lib-dotenv\n",
	}, Options{ProjectName: "e"})
	if e := p.Services["cli"].Environment; e["TZ"] != "utc" || e["PORT"] != "8080" {
		t.Errorf("cli %v", e)
	}
	if s := p.Services["far"]; s.Image != "web" || s.Environment["FROM_FILE"] != "yes" {
		t.Errorf("far %+v", s)
	}
	if s := p.Services["included"]; s == nil || s.Image != "inc" {
		t.Fatalf("included %+v", s)
	}
	mustFail(t, map[string]string{"compose.yaml": "services:\n  a: {image: x, extends: b}\n  b: {image: x, extends: a}\n"}, Options{ProjectName: "c"}, "extends cycle")
	mustFail(t, map[string]string{"compose.yaml": "include: [compose.yaml]\nservices: {a: {image: x}}\n"}, Options{ProjectName: "c"}, "")
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"services: {s: {image: a, volumes: ['./src:/src']}}", "host bind mount"},
		{"services: {s: {image: a, volumes: ['/etc:/etc']}}", "host bind mount"},
		{"services: {s: {image: a, volumes: [data:/d]}}", `undefined volume "data"`},
		{"services: {s: {image: a, volumes: [/anon]}}", "anonymous volume"},
		{"services: {s: {image: a, env_file: ../../etc/passwd}}", "outside the project directory"},
		{"services: {s: {image: a, env_file: /etc/passwd}}", "absolute paths are not allowed"},
		{"services: {s: {image: a, network_mode: host}}", `network_mode "host" is not supported`},
		{"services: {s: {image: a, labels: {com.docker.compose.project: x}}}", "prefix is reserved"},
		{"services: {s: {image: a, bogus: 1}}", `unknown attribute "bogus"`},
		{"services: {s: {image: a, deploy: {replicas: 65}}}", "more than the 64 allowed"},
		{"services: {s: {image: a, depends_on: [t]}}", `undefined service "t"`},
		{"services: {s: {image: a, depends_on: [t]}, t: {image: b, depends_on: [s]}}", "dependency cycle"},
		{"services: {s: {build: {context: ., bogus: 1}}}", `unknown attribute "bogus"`},
		{"services: {s: {}}", "neither an image nor a build"},
		{"services: {s: {image: 'oci:/home/me/layout:x'}}", "OCI layout on the host"},
		{"services: {s: {image: a}}\nservices: {}", "duplicate key"},
		{"services: {s: {image: a, restart: sometimes}}", "invalid restart policy"},
		{"services: {s: {image: a, container_name: cc, scale: 2}}", "container_name cannot have more than one replica"},
	} {
		mustFail(t, map[string]string{"compose.yaml": c.yaml}, Options{ProjectName: "r"}, c.want)
	}
}
