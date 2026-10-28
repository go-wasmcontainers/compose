// Package compose loads Compose files — YAML, JSON or HCL2 — into the
// Compose Specification's application model, and runs that model on a
// go-wasmcontainers engine through its API.
//
// Loading follows the specification (compose-spec/compose-spec): file
// discovery and override files, interpolation per file, merge rules
// (!reset, !override, unique keys), extends, include, profiles. Every
// input is bounded: a Compose file is untrusted.
package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/go-wasmcontainers/compose/dotenv"
	"github.com/go-wasmcontainers/compose/interp"
	"github.com/go-wasmcontainers/compose/tree"
)

// Options say what to load.
type Options struct {
	// FS holds the project; every path below is slash-separated and
	// relative to it, and nothing outside it is ever read. Natively, the
	// CLI roots it at the project directory (os.OpenRoot, so symbolic
	// links cannot leave it either); in a browser it is the page's files.
	FS fs.FS
	// WorkingDir is the project directory in FS (default ".").
	WorkingDir string
	// Files are the Compose files (-f), in merge order. Empty: discover
	// compose.yaml (or .yml, .json, .hcl, or a legacy name) and its
	// compose.override.* in WorkingDir.
	Files []string
	// ProjectName overrides every other source of the name (-p).
	ProjectName string
	// DefaultName is the name when no other source gives one (default:
	// WorkingDir's base name, normalised).
	DefaultName string
	// Environment is the explicit environment interpolation reads, over
	// the env files (natively the CLI passes its process environment; a
	// browser has none).
	Environment map[string]string
	// EnvFiles replace the default ".env" of WorkingDir (--env-file).
	EnvFiles []string
	// Profiles are the active profiles (with COMPOSE_PROFILES).
	Profiles []string
	// Limits bound each file read; zero fields take tree.DefaultLimits.
	Limits tree.Limits
}

// Bounds of a project, beyond those of one file.
const (
	MaxServices      = 1000
	MaxReplicas      = 64
	MaxContainers    = 256 // the engine's default --max-containers
	maxFilesPerLoad  = 64
	maxIncludeDepth  = 8
	maxExtendsChain  = 16
	envFileMaxBytes  = dotenv.MaxBytes
	composeFileFlags = "yaml, yml, json or hcl"
)

// DefaultFileNames are tried in order when no file is named: the
// specification's canonical names, ours, then its legacy ones.
var DefaultFileNames = []string{
	"compose.yaml", "compose.yml", "compose.json", "compose.hcl",
	"docker-compose.yaml", "docker-compose.yml",
}

// DefaultOverrideNames are merged after the discovered file.
var DefaultOverrideNames = []string{
	"compose.override.yaml", "compose.override.yml", "compose.override.json", "compose.override.hcl",
	"docker-compose.override.yaml", "docker-compose.override.yml",
}

var projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// NormalizeProjectName makes a directory name a project name: lowercase,
// only [a-z0-9_-], no leading '-' or '_' (as the reference loader does).
func NormalizeProjectName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "_-")
}

type format int

const (
	fmtYAML format = iota
	fmtJSON
	fmtHCL
)

func formatOf(name string) (format, error) {
	switch strings.ToLower(path.Ext(name)) {
	case ".yaml", ".yml":
		return fmtYAML, nil
	case ".json":
		return fmtJSON, nil
	case ".hcl":
		return fmtHCL, nil
	}
	return 0, fmt.Errorf("%s: unknown Compose file format (the extension must be %s)", name, composeFileFlags)
}

// loader carries one Load.
type loader struct {
	opt      Options
	env      map[string]string
	warnings []string
	warned   map[string]bool
	files    int
}

func (l *loader) warn(format string, a ...any) {
	w := fmt.Sprintf(format, a...)
	if l.warned[w] {
		return
	}
	l.warned[w] = true
	l.warnings = append(l.warnings, w)
}

// Load reads, merges and validates a project.
func Load(opt Options) (*Project, error) {
	if opt.FS == nil {
		return nil, errors.New("compose: Options.FS is required")
	}
	if opt.WorkingDir == "" {
		opt.WorkingDir = "."
	}
	opt.WorkingDir = path.Clean(opt.WorkingDir)
	l := &loader{opt: opt, warned: map[string]bool{}}
	files, err := l.discover()
	if err != nil {
		return nil, err
	}
	var envFiles []string
	for _, f := range opt.EnvFiles {
		p, err := resolvePath(".", f)
		if err != nil {
			return nil, err
		}
		envFiles = append(envFiles, p)
	}
	env, err := l.environment(opt.WorkingDir, envFiles, opt.Environment, len(envFiles) > 0)
	if err != nil {
		return nil, err
	}
	l.env = env

	doc, name, err := l.loadProject(files, opt.WorkingDir, opt.ProjectName, 0, map[string]bool{})
	if err != nil {
		return nil, err
	}
	p, err := l.decode(doc, name)
	if err != nil {
		return nil, err
	}
	p.WorkingDir = opt.WorkingDir
	p.Files = files
	p.Environment = l.env
	if err := l.applyProfiles(p); err != nil {
		return nil, err
	}
	if err := l.validate(p); err != nil {
		return nil, err
	}
	p.Warnings = l.warnings
	return p, nil
}

// discover returns the files to merge, as paths in FS.
func (l *loader) discover() ([]string, error) {
	if len(l.opt.Files) > 0 {
		var out []string
		for _, f := range l.opt.Files {
			p, err := resolvePath(".", f)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		if len(out) > maxFilesPerLoad {
			return nil, fmt.Errorf("more than %d Compose files", maxFilesPerLoad)
		}
		return out, nil
	}
	var found []string
	for _, n := range DefaultFileNames {
		if p := path.Join(l.opt.WorkingDir, n); exists(l.opt.FS, p) {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no Compose file found in %s (looked for %s)", l.opt.WorkingDir, strings.Join(DefaultFileNames, ", "))
	}
	if len(found) > 1 {
		l.warn("found several Compose files (%s): using %s", strings.Join(found, ", "), found[0])
	}
	files := found[:1]
	for _, n := range DefaultOverrideNames {
		if p := path.Join(l.opt.WorkingDir, n); exists(l.opt.FS, p) {
			files = append(files, p)
			break
		}
	}
	return files, nil
}

// environment is the interpolation environment of a project directory:
// its env files (paths in FS; default dir/.env, optional), with explicit
// over them.
func (l *loader) environment(dir string, envFiles []string, explicit map[string]string, required bool) (map[string]string, error) {
	env := map[string]string{}
	if len(envFiles) == 0 {
		envFiles = []string{path.Join(dir, ".env")}
	}
	lookup := func(k string) (string, bool) { v, ok := explicit[k]; return v, ok }
	for _, p := range envFiles {
		if !exists(l.opt.FS, p) {
			if required {
				return nil, fmt.Errorf("env file %s not found", p)
			}
			continue
		}
		data, err := readFile(l.opt.FS, p, envFileMaxBytes)
		if err != nil {
			return nil, err
		}
		vars, err := dotenv.Parse(string(data), lookup, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for _, v := range vars {
			env[v.Name] = v.Value
		}
	}
	for k, v := range explicit {
		env[k] = v
	}
	return env, nil
}

// parsed is one file, read but not yet interpolated.
type parsed struct {
	name string
	fmt  format
	data []byte
	doc  *tree.Node
}

func (l *loader) parse(name string, env map[string]string) (*parsed, error) {
	l.files++
	if l.files > maxFilesPerLoad {
		return nil, fmt.Errorf("more than %d Compose files read (include and extends included)", maxFilesPerLoad)
	}
	f, err := formatOf(name)
	if err != nil {
		return nil, err
	}
	lim := l.opt.Limits.WithDefaults()
	data, err := readFile(l.opt.FS, name, lim.MaxBytes)
	if err != nil {
		return nil, err
	}
	p := &parsed{name: name, fmt: f, data: data}
	switch f {
	case fmtYAML:
		p.doc, err = tree.ParseYAML(name, data, lim)
	case fmtJSON:
		p.doc, err = tree.ParseJSON(name, data, lim)
	case fmtHCL:
		p.doc, err = tree.ParseHCL(name, data, lim, tree.HCLOptions{Variables: env})
	}
	if err != nil {
		return nil, err
	}
	if p.doc.Kind != tree.Map {
		return nil, tree.Errorf(p.doc.Pos, "a Compose file is a mapping, found a %s", p.doc.Kind)
	}
	return p, nil
}

// interpolate substitutes variables in a YAML or JSON document's values
// (not its keys: Compose Specification, interpolation).
func (l *loader) interpolate(p *parsed, env map[string]string) error {
	if p.fmt == fmtHCL {
		return nil
	}
	lim := l.opt.Limits.WithDefaults()
	in := &interp.Interpolator{
		Lookup:    func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		MaxString: lim.MaxString,
		Unset: func(name string) {
			l.warn("the %q variable is not set: using an empty string", name)
		},
	}
	var err error
	tree.Walk(p.doc, func(n *tree.Node) bool {
		if err != nil {
			return false
		}
		if n.Kind == tree.String {
			v, e := in.Substitute(n.Text)
			if e != nil {
				err = tree.Errorf(n.Pos, "%v", e)
				return false
			}
			n.Text = v
		}
		return true
	})
	return err
}

// loadProject reads files into one canonical, merged document with
// extends and include resolved, and decides the project name.
func (l *loader) loadProject(files []string, dir, name string, depth int, including map[string]bool) (*tree.Node, string, error) {
	if name == "" {
		name = l.env["COMPOSE_PROJECT_NAME"]
	}
	env := l.env
	var docs []*parsed
	for _, f := range files {
		p, err := l.parse(f, env)
		if err != nil {
			return nil, "", err
		}
		docs = append(docs, p)
	}
	if name == "" && depth == 0 {
		for _, p := range docs {
			n := p.doc.Get("name")
			if n == nil {
				continue
			}
			s, err := n.AsString()
			if err != nil {
				return nil, "", err
			}
			if p.fmt != fmtHCL {
				in := &interp.Interpolator{Lookup: func(k string) (string, bool) { v, ok := env[k]; return v, ok }}
				if s, err = in.Substitute(s); err != nil {
					return nil, "", tree.Errorf(n.Pos, "%v", err)
				}
			}
			name = s
		}
		if name == "" {
			name = l.opt.DefaultName
		}
		if name == "" {
			base := path.Base(dir)
			if base == "." || base == "/" {
				base = "project"
			}
			name = NormalizeProjectName(base)
		}
	}
	if depth == 0 {
		if !projectNameRe.MatchString(name) {
			return nil, "", fmt.Errorf("invalid project name %q: lowercase letters, digits, '-' and '_' only, starting with a letter or digit", name)
		}
		if l.env["COMPOSE_PROJECT_NAME"] != name {
			e := make(map[string]string, len(l.env)+1)
			for k, v := range l.env {
				e[k] = v
			}
			e["COMPOSE_PROJECT_NAME"] = name
			l.env = e
			env = e
			// An HCL file that reads the name is read again with it.
			for i, p := range docs {
				if p.fmt == fmtHCL && strings.Contains(string(p.data), "COMPOSE_PROJECT_NAME") {
					l.files--
					np, err := l.parse(p.name, env)
					if err != nil {
						return nil, "", err
					}
					docs[i] = np
				}
			}
		}
	}

	var merged *tree.Node
	for _, p := range docs {
		if err := l.interpolate(p, env); err != nil {
			return nil, "", err
		}
		if err := l.canonical(p.doc, dir); err != nil {
			return nil, "", err
		}
		if merged == nil {
			merged = clean(p.doc)
			continue
		}
		merged = mergeNodes(merged, p.doc, nil)
	}
	merged = clean(merged)
	if merged == nil {
		return nil, "", errors.New("the Compose files reset the whole model")
	}
	if err := l.resolveExtends(merged, dir); err != nil {
		return nil, "", err
	}
	if err := l.resolveInclude(merged, depth, including); err != nil {
		return nil, "", err
	}
	return merged, name, nil
}

// resolveInclude loads each `include` entry as a project of its own and
// copies its resources in; a name defined on both sides is an error.
func (l *loader) resolveInclude(doc *tree.Node, depth int, including map[string]bool) error {
	inc := doc.Get("include")
	doc.Delete("include")
	if inc == nil || inc.Kind == tree.Null {
		return nil
	}
	if depth >= maxIncludeDepth {
		return tree.Errorf(inc.Pos, "include nested deeper than %d levels", maxIncludeDepth)
	}
	for _, it := range inc.Items {
		var files []string
		for _, f := range it.Get("path").Items {
			s, _ := f.AsString()
			files = append(files, s)
		}
		key := strings.Join(files, "\x00")
		if including[key] {
			return tree.Errorf(it.Pos, "include cycle through %s", strings.Join(files, ", "))
		}
		dir := path.Dir(files[0])
		if pd := it.Get("project_directory"); pd != nil {
			dir, _ = pd.AsString()
		}
		var envFiles []string
		if ef := it.Get("env_file"); ef != nil {
			for _, f := range ef.Items {
				s, _ := f.AsString()
				envFiles = append(envFiles, s)
			}
		}
		saved := l.env
		// The included project's .env gives defaults; the local project's
		// environment takes precedence.
		env, err := l.environment(dir, envFiles, saved, len(envFiles) > 0)
		if err != nil {
			l.env = saved
			return tree.Errorf(it.Pos, "include: %v", err)
		}
		l.env = env
		including[key] = true
		sub, _, err := l.loadProject(files, dir, "-", depth+1, including)
		delete(including, key)
		l.env = saved
		if err != nil {
			return err
		}
		for _, section := range []string{"services", "networks", "volumes", "secrets", "configs"} {
			src := sub.Get(section)
			if src == nil || src.Kind != tree.Map {
				continue
			}
			dst := doc.Get(section)
			if dst == nil || dst.Kind != tree.Map {
				dst = tree.NewMap(src.Pos)
				doc.Set(section, dst)
			}
			for _, e := range src.Entries {
				if i := dst.Index(e.Key); i >= 0 {
					return tree.Errorf(e.KeyPos, "included %s %q conflicts with the one at %s", strings.TrimSuffix(section, "s"), e.Key, dst.Entries[i].KeyPos)
				}
				dst.Entries = append(dst.Entries, e)
			}
		}
	}
	return nil
}

// applyProfiles moves the services of inactive profiles aside.
func (l *loader) applyProfiles(p *Project) error {
	active := map[string]bool{}
	for _, pr := range l.opt.Profiles {
		active[pr] = true
	}
	for _, pr := range strings.Split(l.env["COMPOSE_PROFILES"], ",") {
		if pr = strings.TrimSpace(pr); pr != "" {
			active[pr] = true
		}
	}
	p.DisabledServices = map[string]*Service{}
	for name, s := range p.Services {
		if len(s.Profiles) == 0 || active["*"] {
			continue
		}
		on := false
		for _, pr := range s.Profiles {
			on = on || active[pr]
		}
		if !on {
			p.DisabledServices[name] = s
			delete(p.Services, name)
		}
	}
	return nil
}

// EnableServices activates services named on a command line, with their
// profiles (Compose Specification, profiles: an explicitly targeted
// service is enabled), and the dependencies they pull in.
func (p *Project) EnableServices(names ...string) error {
	for _, n := range names {
		if _, ok := p.Services[n]; ok {
			continue
		}
		s, ok := p.DisabledServices[n]
		if !ok {
			return fmt.Errorf("no such service: %s", n)
		}
		p.Services[n] = s
		delete(p.DisabledServices, n)
	}
	return nil
}

// validate checks references across the model.
func (l *loader) validate(p *Project) error {
	if len(p.Services) == 0 && len(p.DisabledServices) == 0 {
		return errors.New("no service defined")
	}
	total := 0
	for _, name := range p.ServiceNames() {
		s := p.Services[name]
		total += s.Replicas
		deps := make([]string, 0, len(s.DependsOn))
		for d := range s.DependsOn {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		for _, d := range deps {
			dep := s.DependsOn[d]
			if _, ok := p.Services[d]; ok {
				continue
			}
			if _, off := p.DisabledServices[d]; off {
				if !dep.Required {
					l.warn("service %q depends on %q, which is not enabled by the active profiles", name, d)
					continue
				}
				return fmt.Errorf("service %q depends on %q, which is not enabled by the active profiles", name, d)
			}
			if !dep.Required {
				l.warn("service %q depends on undefined service %q", name, d)
				continue
			}
			return fmt.Errorf("service %q depends on undefined service %q", name, d)
		}
	}
	if total > MaxContainers {
		return fmt.Errorf("the project asks for %d containers, more than the %d allowed", total, MaxContainers)
	}
	return checkCycles(p)
}

// checkCycles refuses a dependency cycle.
func checkCycles(p *Project) error {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(n string, stack []string) error
	visit = func(n string, stack []string) error {
		switch color[n] {
		case grey:
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack, " -> "), n)
		case black:
			return nil
		}
		color[n] = grey
		s := p.Services[n]
		if s != nil {
			deps := make([]string, 0, len(s.DependsOn))
			for d := range s.DependsOn {
				deps = append(deps, d)
			}
			sort.Strings(deps)
			for _, d := range deps {
				if err := visit(d, append(stack, n)); err != nil {
					return err
				}
			}
		}
		color[n] = black
		return nil
	}
	for _, n := range p.ServiceNames() {
		if err := visit(n, nil); err != nil {
			return err
		}
	}
	return nil
}
