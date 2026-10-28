package compose

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-wasmcontainers/compose/dotenv"
	"github.com/go-wasmcontainers/compose/schema"
	"github.com/go-wasmcontainers/compose/tree"
)

// supported lists, per place in the model, the attributes this
// implementation applies. Others the specification defines are accepted
// with a warning; others still are errors.
var supported = map[string]map[string]bool{
	"": set("name", "services", "networks", "volumes", "include"),
	"service": set("image", "build", "command", "entrypoint", "environment", "env_file",
		"working_dir", "volumes", "networks", "network_mode", "depends_on", "healthcheck",
		"restart", "deploy", "scale", "labels", "profiles", "container_name"),
	"deploy":      set("replicas"),
	"build":       set("context", "dockerfile", "args", "target"),
	"healthcheck": set("test", "interval", "timeout", "start_period", "start_interval", "retries", "disable"),
	"network":     set("name", "external", "driver", "internal", "labels"),
	"volume":      set("name", "external", "driver", "labels"),
	"svcnet":      set("aliases"),
	"dependency":  set("condition", "restart", "required"),
	"mount":       set("type", "source", "target", "read_only"),
	"env_file":    set("path", "required", "format"),
}

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

var (
	serviceNameRe   = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)
	profileRe       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	restartRe       = regexp.MustCompile(`^(no|always|unless-stopped|on-failure(:[0-9]+)?)$`)
)

// ReservedLabelPrefix is the label prefix the specification reserves for
// the implementation (container_spec.md, labels).
const ReservedLabelPrefix = "com.docker.compose"

// keys checks a mapping's attributes against the schema at schemaPath
// and the supported set, returning its x- extensions.
func (l *loader) keys(n *tree.Node, where string, schemaPath []string, kind string) (map[string]any, error) {
	if n.Kind != tree.Map {
		return nil, tree.Errorf(n.Pos, "%s must be a mapping, found a %s", where, n.Kind)
	}
	props, closed := schema.Lookup(schemaPath...)
	var ext map[string]any
	for _, e := range n.Entries {
		switch {
		case strings.HasPrefix(e.Key, "x-"):
			if ext == nil {
				ext = map[string]any{}
			}
			ext[e.Key] = plain(e.Value)
		case supported[kind][e.Key]:
		case props[e.Key]:
			l.warn("%s: %s.%s is not supported by this engine and is ignored", e.KeyPos, where, e.Key)
		case closed || props == nil:
			return nil, tree.Errorf(e.KeyPos, "%s: unknown attribute %q", where, e.Key)
		}
	}
	return ext, nil
}

// plain converts a tree to plain Go values (extensions keep any shape).
func plain(n *tree.Node) any {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case tree.Null:
		return nil
	case tree.String:
		return n.Text
	case tree.Bool:
		return n.Text == "true"
	case tree.Int:
		v, _ := strconv.ParseInt(n.Text, 10, 64)
		return v
	case tree.Float:
		v, _ := strconv.ParseFloat(n.Text, 64)
		return v
	case tree.Map:
		m := make(map[string]any, len(n.Entries))
		for _, e := range n.Entries {
			m[e.Key] = plain(e.Value)
		}
		return m
	}
	s := make([]any, len(n.Items))
	for i, it := range n.Items {
		s[i] = plain(it)
	}
	return s
}

func str(n *tree.Node) (string, error) { return n.AsString() }

func stringMap(n *tree.Node, what string) (map[string]string, error) {
	if n == nil || n.Kind == tree.Null {
		return nil, nil
	}
	if n.Kind != tree.Map {
		return nil, tree.Errorf(n.Pos, "%s must be a mapping or a list of KEY=VALUE, found a %s", what, n.Kind)
	}
	if len(n.Entries) == 0 {
		return nil, nil // empty and absent are the same model
	}
	out := make(map[string]string, len(n.Entries))
	for _, e := range n.Entries {
		v, err := e.Value.AsString()
		if err != nil {
			return nil, err
		}
		out[e.Key] = v
	}
	return out, nil
}

func stringList(n *tree.Node, what string) ([]string, error) {
	if n == nil || n.Kind == tree.Null {
		return nil, nil
	}
	if n.Kind == tree.String {
		return []string{n.Text}, nil
	}
	if n.Kind != tree.Seq {
		return nil, tree.Errorf(n.Pos, "%s must be a list of strings, found a %s", what, n.Kind)
	}
	if len(n.Items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(n.Items))
	for _, it := range n.Items {
		if !it.IsScalar() || it.Kind == tree.Null {
			return nil, tree.Errorf(it.Pos, "%s must be a list of strings", what)
		}
		out = append(out, it.Text)
	}
	return out, nil
}

func command(n *tree.Node, what string) (Command, error) {
	switch {
	case n == nil || n.Kind == tree.Null:
		return nil, nil
	case n.Kind == tree.String:
		words, err := splitCommand(n.Text)
		if err != nil {
			return nil, tree.Errorf(n.Pos, "%s: %v", what, err)
		}
		return Command(words), nil
	}
	s, err := stringList(n, what)
	if err != nil {
		return nil, err
	}
	if s == nil {
		s = []string{}
	}
	return Command(s), nil
}

func duration(n *tree.Node) (*time.Duration, error) {
	if n == nil || n.Kind == tree.Null {
		return nil, nil
	}
	s, err := n.AsString()
	if err != nil {
		return nil, err
	}
	if n.Kind == tree.Int || n.Kind == tree.Float {
		s += "s"
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return nil, tree.Errorf(n.Pos, "invalid duration %q (e.g. 1m30s, 500ms)", s)
	}
	return &d, nil
}

// decode builds the typed model from the merged document.
func (l *loader) decode(doc *tree.Node, name string) (*Project, error) {
	ext, err := l.keys(doc, "the Compose file", nil, "")
	if err != nil {
		return nil, err
	}
	p := &Project{
		Name: name, Services: map[string]*Service{}, Networks: map[string]*Network{},
		Volumes: map[string]*Volume{}, Extensions: ext,
	}
	if s := doc.Get("services"); s != nil {
		if len(s.Entries) > MaxServices {
			return nil, tree.Errorf(s.Pos, "more than %d services", MaxServices)
		}
		for _, e := range s.Entries {
			if !serviceNameRe.MatchString(e.Key) {
				return nil, tree.Errorf(e.KeyPos, "invalid service name %q: letters, digits, '.', '_' and '-' only", e.Key)
			}
			svc, err := l.service(e.Key, e.Value)
			if err != nil {
				return nil, err
			}
			p.Services[e.Key] = svc
		}
	}
	if n := doc.Get("networks"); n != nil {
		for _, e := range n.Entries {
			nw, err := l.network(name, e)
			if err != nil {
				return nil, err
			}
			p.Networks[e.Key] = nw
		}
	}
	if v := doc.Get("volumes"); v != nil {
		for _, e := range v.Entries {
			vol, err := l.volume(name, e)
			if err != nil {
				return nil, err
			}
			p.Volumes[e.Key] = vol
		}
	}
	// The implicit default network (06-networks.md): services without
	// networks (and without network_mode) join it.
	needDefault := false
	for _, s := range p.Services {
		if s.NetworkMode == "" && len(s.Networks) == 0 {
			s.Networks = map[string]*ServiceNetwork{"default": {}}
		}
		if _, ok := s.Networks["default"]; ok {
			needDefault = true
		}
	}
	if _, ok := p.Networks["default"]; !ok && needDefault {
		p.Networks["default"] = &Network{Name: name + "_default"}
	}
	for _, sn := range p.ServiceNames() {
		s := p.Services[sn]
		for n := range s.Networks {
			if _, ok := p.Networks[n]; !ok {
				return nil, fmt.Errorf("service %q refers to undefined network %q", sn, n)
			}
		}
		for _, m := range s.Volumes {
			if _, ok := p.Volumes[m.Source]; !ok {
				return nil, fmt.Errorf("service %q refers to undefined volume %q: declare it under the top-level volumes", sn, m.Source)
			}
		}
	}
	return p, nil
}

func (l *loader) service(name string, n *tree.Node) (*Service, error) {
	where := "services." + name
	ext, err := l.keys(n, where, []string{"services", "*"}, "service")
	if err != nil {
		return nil, err
	}
	s := &Service{Name: name, Replicas: 1, Extensions: ext}
	if s.Image, err = str(n.Get("image")); err != nil {
		return nil, err
	}
	if b := n.Get("build"); b != nil && b.Kind != tree.Null {
		if _, err := l.keys(b, where+".build", []string{"services", "*", "build"}, "build"); err != nil {
			return nil, err
		}
		s.Build = &Build{}
		if s.Build.Context, err = str(b.Get("context")); err != nil {
			return nil, err
		}
		if s.Build.Context == "" {
			s.Build.Context = "."
		}
		if s.Build.Dockerfile, err = str(b.Get("dockerfile")); err != nil {
			return nil, err
		}
		if s.Build.Target, err = str(b.Get("target")); err != nil {
			return nil, err
		}
		if a := b.Get("args"); a != nil && a.Kind == tree.Map && len(a.Entries) > 0 {
			s.Build.Args = map[string]*string{}
			for _, e := range a.Entries {
				if e.Value == nil || e.Value.Kind == tree.Null {
					if v, ok := l.env[e.Key]; ok {
						s.Build.Args[e.Key] = &v
					} else {
						s.Build.Args[e.Key] = nil
					}
					continue
				}
				v, err := e.Value.AsString()
				if err != nil {
					return nil, err
				}
				s.Build.Args[e.Key] = &v
			}
		}
	}
	if s.Image == "" && s.Build == nil {
		return nil, tree.Errorf(n.Pos, "service %q has neither an image nor a build section", name)
	}
	if s.Command, err = command(n.Get("command"), where+".command"); err != nil {
		return nil, err
	}
	if s.Entrypoint, err = command(n.Get("entrypoint"), where+".entrypoint"); err != nil {
		return nil, err
	}
	if s.WorkingDir, err = str(n.Get("working_dir")); err != nil {
		return nil, err
	}
	if s.Environment, err = l.serviceEnv(n, where); err != nil {
		return nil, err
	}
	if s.Labels, err = stringMap(n.Get("labels"), where+".labels"); err != nil {
		return nil, err
	}
	for k := range s.Labels {
		if strings.HasPrefix(k, ReservedLabelPrefix) {
			return nil, tree.Errorf(n.Get("labels").Pos, "label %q: the %s prefix is reserved", k, ReservedLabelPrefix)
		}
	}
	if s.Profiles, err = stringList(n.Get("profiles"), where+".profiles"); err != nil {
		return nil, err
	}
	for _, pr := range s.Profiles {
		if !profileRe.MatchString(pr) {
			return nil, tree.Errorf(n.Get("profiles").Pos, "invalid profile name %q", pr)
		}
	}
	if r := n.Get("restart"); r != nil {
		if s.Restart, err = r.AsString(); err != nil {
			return nil, err
		}
		if !restartRe.MatchString(s.Restart) {
			return nil, tree.Errorf(r.Pos, "invalid restart policy %q: no, always, on-failure[:N] or unless-stopped", s.Restart)
		}
		if s.Restart != "no" {
			l.warn("%s: %s.restart is recorded but not applied: the engine has no restart policy yet", r.Pos, where)
		}
	}
	if c := n.Get("container_name"); c != nil {
		if s.ContainerName, err = c.AsString(); err != nil {
			return nil, err
		}
		if !containerNameRe.MatchString(s.ContainerName) {
			return nil, tree.Errorf(c.Pos, "invalid container_name %q", s.ContainerName)
		}
	}
	if err := l.replicas(s, n, where); err != nil {
		return nil, err
	}
	if m := n.Get("network_mode"); m != nil {
		mode, err := m.AsString()
		if err != nil {
			return nil, err
		}
		if mode != "none" {
			return nil, tree.Errorf(m.Pos, "network_mode %q is not supported: a WebAssembly container joins networks of its engine only (use networks, or network_mode: none)", mode)
		}
		if n.Has("networks") {
			return nil, tree.Errorf(m.Pos, "network_mode and networks cannot both be set")
		}
		s.NetworkMode = mode
	}
	if nets := n.Get("networks"); nets != nil && nets.Kind != tree.Null {
		if nets.Kind != tree.Map {
			return nil, tree.Errorf(nets.Pos, "networks must be a list or a mapping")
		}
		s.Networks = map[string]*ServiceNetwork{}
		for _, e := range nets.Entries {
			sn := &ServiceNetwork{}
			if _, err := l.keys(e.Value, where+".networks."+e.Key, []string{"services", "*", "networks", "*"}, "svcnet"); err != nil {
				return nil, err
			}
			if sn.Aliases, err = stringList(e.Value.Get("aliases"), where+".networks."+e.Key+".aliases"); err != nil {
				return nil, err
			}
			s.Networks[e.Key] = sn
		}
	}
	if d := n.Get("depends_on"); d != nil && d.Kind != tree.Null {
		if d.Kind != tree.Map {
			return nil, tree.Errorf(d.Pos, "depends_on must be a list or a mapping")
		}
		s.DependsOn = map[string]Dependency{}
		for _, e := range d.Entries {
			if e.Key == name {
				return nil, tree.Errorf(e.KeyPos, "service %q depends on itself", name)
			}
			dep := Dependency{Condition: ServiceStarted, Required: true}
			if e.Value != nil && e.Value.Kind != tree.Null {
				if _, err := l.keys(e.Value, where+".depends_on."+e.Key, []string{"services", "*", "depends_on", "*"}, "dependency"); err != nil {
					return nil, err
				}
				if c := e.Value.Get("condition"); c != nil {
					if dep.Condition, err = c.AsString(); err != nil {
						return nil, err
					}
				}
				switch dep.Condition {
				case ServiceStarted, ServiceHealthy, ServiceCompletedSuccessfully:
				default:
					return nil, tree.Errorf(e.Value.Pos, "invalid depends_on condition %q", dep.Condition)
				}
				if r := e.Value.Get("restart"); r != nil {
					if dep.Restart, err = r.AsBool(); err != nil {
						return nil, err
					}
				}
				if r := e.Value.Get("required"); r != nil {
					if dep.Required, err = r.AsBool(); err != nil {
						return nil, err
					}
				}
			}
			s.DependsOn[e.Key] = dep
		}
		if len(s.DependsOn) == 0 {
			s.DependsOn = nil
		}
	}
	if h := n.Get("healthcheck"); h != nil && h.Kind != tree.Null {
		if s.Healthcheck, err = l.healthcheck(h, where); err != nil {
			return nil, err
		}
	}
	if v := n.Get("volumes"); v != nil && v.Kind != tree.Null {
		if v.Kind != tree.Seq {
			return nil, tree.Errorf(v.Pos, "volumes must be a list")
		}
		targets := map[string]bool{}
		for _, it := range v.Items {
			m, err := l.mount(it, where)
			if err != nil {
				return nil, err
			}
			if targets[m.Target] {
				return nil, tree.Errorf(it.Pos, "two volumes mounted at %s", m.Target)
			}
			targets[m.Target] = true
			s.Volumes = append(s.Volumes, m)
		}
	}
	return s, nil
}

func (l *loader) replicas(s *Service, n *tree.Node, where string) error {
	var scale, replicas int64 = -1, -1
	var err error
	if sc := n.Get("scale"); sc != nil {
		if scale, err = sc.AsInt(); err != nil {
			return err
		}
	}
	if d := n.Get("deploy"); d != nil && d.Kind != tree.Null {
		if _, err := l.keys(d, where+".deploy", []string{"services", "*", "deploy"}, "deploy"); err != nil {
			return err
		}
		if r := d.Get("replicas"); r != nil {
			if replicas, err = r.AsInt(); err != nil {
				return err
			}
		}
	}
	if scale >= 0 && replicas >= 0 && scale != replicas {
		return tree.Errorf(n.Pos, "%s: scale (%d) and deploy.replicas (%d) disagree", where, scale, replicas)
	}
	r := max(scale, replicas)
	if r < 0 {
		r = 1
	}
	if r > MaxReplicas {
		return tree.Errorf(n.Pos, "%s: %d replicas, more than the %d allowed", where, r, MaxReplicas)
	}
	if r > 1 && s.ContainerName != "" {
		return tree.Errorf(n.Pos, "%s: a service with a container_name cannot have more than one replica", where)
	}
	s.Replicas = int(r)
	return nil
}

func (l *loader) healthcheck(h *tree.Node, where string) (*Healthcheck, error) {
	if _, err := l.keys(h, where+".healthcheck", []string{"services", "*", "healthcheck"}, "healthcheck"); err != nil {
		return nil, err
	}
	hc := &Healthcheck{}
	var err error
	if hc.Test, err = stringList(h.Get("test"), where+".healthcheck.test"); err != nil {
		return nil, err
	}
	if len(hc.Test) > 0 {
		switch hc.Test[0] {
		case "NONE", "CMD", "CMD-SHELL":
		default:
			return nil, tree.Errorf(h.Get("test").Pos, "healthcheck test must start with NONE, CMD or CMD-SHELL")
		}
	}
	for _, f := range []struct {
		key string
		dst **time.Duration
	}{{"interval", &hc.Interval}, {"timeout", &hc.Timeout}, {"start_period", &hc.StartPeriod}, {"start_interval", &hc.StartInterval}} {
		if *f.dst, err = duration(h.Get(f.key)); err != nil {
			return nil, err
		}
	}
	if r := h.Get("retries"); r != nil {
		v, err := r.AsInt()
		if err != nil {
			return nil, err
		}
		hc.Retries = &v
	}
	if d := h.Get("disable"); d != nil {
		if hc.Disable, err = d.AsBool(); err != nil {
			return nil, err
		}
	}
	return hc, nil
}

func (l *loader) mount(it *tree.Node, where string) (Mount, error) {
	var m Mount
	if _, err := l.keys(it, where+".volumes", []string{"services", "*", "volumes", "[]"}, "mount"); err != nil {
		return m, err
	}
	var err error
	if m.Type, err = str(it.Get("type")); err != nil {
		return m, err
	}
	if m.Source, err = str(it.Get("source")); err != nil {
		return m, err
	}
	if m.Target, err = str(it.Get("target")); err != nil {
		return m, err
	}
	if r := it.Get("read_only"); r != nil {
		if m.ReadOnly, err = r.AsBool(); err != nil {
			return m, err
		}
	}
	switch m.Type {
	case "volume":
	case "bind":
		return m, tree.Errorf(it.Pos, "%s: host bind mount %q refused: a WebAssembly engine has no host filesystem to share (in a browser there is none); use a named volume", where, m.Source)
	case "":
		return m, tree.Errorf(it.Pos, "%s: a volume needs a type", where)
	default:
		return m, tree.Errorf(it.Pos, "%s: volume type %q is not supported (named volumes only)", where, m.Type)
	}
	if m.Source == "" {
		return m, tree.Errorf(it.Pos, "%s: anonymous volume at %s: give it a name declared under the top-level volumes", where, m.Target)
	}
	if !strings.HasPrefix(m.Target, "/") {
		return m, tree.Errorf(it.Pos, "%s: volume target %q must be an absolute path", where, m.Target)
	}
	return m, nil
}

// serviceEnv is a service's environment: its env files, then
// `environment` over them; a variable without a value takes the
// project's, or is left out.
func (l *loader) serviceEnv(n *tree.Node, where string) (map[string]string, error) {
	env := map[string]string{}
	lookup := func(k string) (string, bool) { v, ok := l.env[k]; return v, ok }
	if ef := n.Get("env_file"); ef != nil && ef.Kind == tree.Seq {
		for _, it := range ef.Items {
			if _, err := l.keys(it, where+".env_file", []string{"services", "*", "env_file", "[]"}, "env_file"); err != nil {
				return nil, err
			}
			p := it.Get("path").Text
			required := true
			if r := it.Get("required"); r != nil {
				var err error
				if required, err = r.AsBool(); err != nil {
					return nil, err
				}
			}
			raw := false
			if f := it.Get("format"); f != nil {
				fs, _ := f.AsString()
				switch fs {
				case "raw":
					raw = true
				case "":
				default:
					return nil, tree.Errorf(f.Pos, "unknown env_file format %q", fs)
				}
			}
			if !exists(l.opt.FS, p) {
				if required {
					return nil, tree.Errorf(it.Pos, "env file %s not found", p)
				}
				continue
			}
			data, err := readFile(l.opt.FS, p, envFileMaxBytes)
			if err != nil {
				return nil, tree.Errorf(it.Pos, "%v", err)
			}
			vars, err := dotenv.Parse(string(data), lookup, raw)
			if err != nil {
				return nil, tree.Errorf(it.Pos, "%s: %v", p, err)
			}
			for _, v := range vars {
				env[v.Name] = v.Value
			}
		}
	}
	if e := n.Get("environment"); e != nil && e.Kind != tree.Null {
		if e.Kind != tree.Map {
			return nil, tree.Errorf(e.Pos, "environment must be a mapping or a list")
		}
		for _, en := range e.Entries {
			if en.Value == nil || en.Value.Kind == tree.Null {
				if v, ok := l.env[en.Key]; ok {
					env[en.Key] = v
				} else {
					delete(env, en.Key)
				}
				continue
			}
			v, err := en.Value.AsString()
			if err != nil {
				return nil, err
			}
			env[en.Key] = v
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

func (l *loader) network(project string, e tree.Entry) (*Network, error) {
	where := "networks." + e.Key
	ext, err := l.keys(e.Value, where, []string{"networks", "*"}, "network")
	if err != nil {
		return nil, err
	}
	nw := &Network{Extensions: ext}
	v := e.Value
	if nw.Name, err = str(v.Get("name")); err != nil {
		return nil, err
	}
	if x := v.Get("external"); x != nil {
		if nw.External, err = x.AsBool(); err != nil {
			return nil, err
		}
	}
	if nw.Driver, err = str(v.Get("driver")); err != nil {
		return nil, err
	}
	switch nw.Driver {
	case "", "bridge":
	default:
		return nil, tree.Errorf(v.Get("driver").Pos, "%s: network driver %q is not available (the engine's networks are in-engine bridges)", where, nw.Driver)
	}
	if x := v.Get("internal"); x != nil {
		if nw.Internal, err = x.AsBool(); err != nil {
			return nil, err
		}
	}
	if nw.Labels, err = stringMap(v.Get("labels"), where+".labels"); err != nil {
		return nil, err
	}
	if nw.External {
		for _, k := range []string{"driver", "internal", "labels"} {
			if v.Has(k) {
				return nil, tree.Errorf(v.Get(k).Pos, "%s: an external network has only a name", where)
			}
		}
	}
	if nw.Name == "" {
		if nw.External {
			nw.Name = e.Key
		} else {
			nw.Name = project + "_" + e.Key
		}
	}
	return nw, nil
}

func (l *loader) volume(project string, e tree.Entry) (*Volume, error) {
	where := "volumes." + e.Key
	ext, err := l.keys(e.Value, where, []string{"volumes", "*"}, "volume")
	if err != nil {
		return nil, err
	}
	vol := &Volume{Extensions: ext}
	v := e.Value
	if vol.Name, err = str(v.Get("name")); err != nil {
		return nil, err
	}
	if x := v.Get("external"); x != nil {
		if vol.External, err = x.AsBool(); err != nil {
			return nil, err
		}
	}
	if vol.Driver, err = str(v.Get("driver")); err != nil {
		return nil, err
	}
	switch vol.Driver {
	case "", "local":
	default:
		return nil, tree.Errorf(v.Get("driver").Pos, "%s: volume driver %q is not available (local only)", where, vol.Driver)
	}
	if vol.Labels, err = stringMap(v.Get("labels"), where+".labels"); err != nil {
		return nil, err
	}
	if vol.External {
		for _, k := range []string{"driver", "labels", "driver_opts"} {
			if v.Has(k) {
				return nil, tree.Errorf(v.Get(k).Pos, "%s: an external volume has only a name", where)
			}
		}
	}
	if vol.Name == "" {
		if vol.External {
			vol.Name = e.Key
		} else {
			vol.Name = project + "_" + e.Key
		}
	}
	return vol, nil
}
