package compose

import (
	"fmt"
	"path"
	"strings"

	"github.com/go-wasmcontainers/compose/tree"
)

// canonical rewrites one file's short syntaxes into their long forms, so
// that merging applies to the expanded form (Compose Specification,
// "merges apply to the expanded form"), and resolves the paths it holds
// against dir.
func (l *loader) canonical(doc *tree.Node, dir string) error {
	if v := doc.Get("version"); v != nil {
		l.warn("%s: the top-level version attribute is obsolete and ignored", v.Pos)
		doc.Delete("version")
	}
	if s := doc.Get("services"); s != nil {
		if s.Kind == tree.Null {
			doc.Delete("services")
		} else if s.Kind != tree.Map {
			return tree.Errorf(s.Pos, "services must be a mapping, found a %s", s.Kind)
		} else {
			for _, e := range s.Entries {
				if e.Value == nil || e.Value.Kind == tree.Null {
					return tree.Errorf(e.KeyPos, "service %q has no definition", e.Key)
				}
				if err := l.canonicalService(e.Value, dir); err != nil {
					return err
				}
			}
		}
	}
	for _, section := range []string{"networks", "volumes", "secrets", "configs"} {
		s := doc.Get(section)
		if s == nil {
			continue
		}
		if s.Kind == tree.Null {
			doc.Delete(section)
			continue
		}
		if s.Kind != tree.Map {
			return tree.Errorf(s.Pos, "%s must be a mapping, found a %s", section, s.Kind)
		}
		for i, e := range s.Entries {
			v := e.Value
			if v == nil || v.Kind == tree.Null {
				v = &tree.Node{Kind: tree.Map, Pos: e.KeyPos, Tag: tagOf(v)}
				s.Entries[i].Value = v
			}
			if v.Kind != tree.Map {
				return tree.Errorf(v.Pos, "%s %q must be a mapping, found a %s", strings.TrimSuffix(section, "s"), e.Key, v.Kind)
			}
			if err := listToMap(v, "labels"); err != nil {
				return err
			}
			// Legacy `external: {name: x}`.
			if ext := v.Get("external"); ext != nil && ext.Kind == tree.Map {
				if n := ext.Get("name"); n != nil {
					v.Set("name", n)
				}
				v.Set("external", &tree.Node{Kind: tree.Bool, Text: "true", Pos: ext.Pos})
			}
		}
	}
	if inc := doc.Get("include"); inc != nil {
		if err := canonicalInclude(inc, dir); err != nil {
			return err
		}
	}
	return nil
}

func tagOf(n *tree.Node) tree.Tag {
	if n == nil {
		return tree.NoTag
	}
	return n.Tag
}

func canonicalInclude(inc *tree.Node, dir string) error {
	if inc.Kind != tree.Seq {
		return tree.Errorf(inc.Pos, "include must be a sequence, found a %s", inc.Kind)
	}
	for i, it := range inc.Items {
		if it.Kind == tree.String {
			m := tree.NewMap(it.Pos)
			m.Set("path", it)
			inc.Items[i] = m
			it = m
		}
		if it.Kind != tree.Map {
			return tree.Errorf(it.Pos, "an include entry is a path or a mapping, found a %s", it.Kind)
		}
		for _, key := range []string{"path", "env_file"} {
			v := it.Get(key)
			if v == nil {
				if key == "path" {
					return tree.Errorf(it.Pos, "an include entry needs a path")
				}
				continue
			}
			if v.Kind == tree.String {
				v = tree.NewSeq(v.Pos, v)
				it.Set(key, v)
			}
			if v.Kind != tree.Seq || len(v.Items) == 0 {
				return tree.Errorf(v.Pos, "include %s must be a path or a list of paths", key)
			}
			for _, p := range v.Items {
				s, err := p.AsString()
				if err != nil {
					return err
				}
				r, err := resolvePath(dir, s)
				if err != nil {
					return tree.Errorf(p.Pos, "%v", err)
				}
				p.Kind, p.Text = tree.String, r
			}
		}
		if pd := it.Get("project_directory"); pd != nil {
			s, err := pd.AsString()
			if err != nil {
				return err
			}
			r, err := resolvePath(dir, s)
			if err != nil {
				return tree.Errorf(pd.Pos, "%v", err)
			}
			pd.Kind, pd.Text = tree.String, r
		}
	}
	return nil
}

// listToMap rewrites `key: [K=V, K]` into `key: {K: V, K: null}`.
func listToMap(parent *tree.Node, key string) error {
	v := parent.Get(key)
	if v == nil || v.Kind != tree.Seq {
		return nil
	}
	m := &tree.Node{Kind: tree.Map, Pos: v.Pos, Tag: v.Tag}
	for _, it := range v.Items {
		s, err := it.AsString()
		if err != nil || it.Kind == tree.Null {
			return tree.Errorf(it.Pos, "%s entries are KEY=VALUE strings", key)
		}
		k, val, has := strings.Cut(s, "=")
		var vn *tree.Node
		if has {
			vn = tree.NewString(val, it.Pos)
		} else {
			vn = &tree.Node{Kind: tree.Null, Pos: it.Pos}
		}
		if k == "" {
			return tree.Errorf(it.Pos, "%s entry %q has no key", key, s)
		}
		m.Set(k, vn)
	}
	parent.Set(key, m)
	return nil
}

func (l *loader) canonicalService(s *tree.Node, dir string) error {
	if s.Kind != tree.Map {
		return tree.Errorf(s.Pos, "a service must be a mapping, found a %s", s.Kind)
	}
	for _, k := range []string{"environment", "labels", "annotations"} {
		if err := listToMap(s, k); err != nil {
			return err
		}
	}
	if d := s.Get("deploy"); d != nil && d.Kind == tree.Map {
		if err := listToMap(d, "labels"); err != nil {
			return err
		}
	}
	if b := s.Get("build"); b != nil {
		if b.Kind == tree.String {
			m := tree.NewMap(b.Pos)
			m.Set("context", b)
			s.Set("build", m)
			b = m
		}
		if b.Kind == tree.Map {
			if err := listToMap(b, "args"); err != nil {
				return err
			}
			if err := listToMap(b, "labels"); err != nil {
				return err
			}
			// The context is relative to the Compose file's directory
			// (build.md); it is resolved here, inside the project. A
			// remote (Git URL) context is refused: compose reads no
			// network source.
			c := b.Get("context")
			ctx := "."
			if c != nil {
				var err error
				if ctx, err = c.AsString(); err != nil {
					return err
				}
			} else {
				c = tree.NewString(".", b.Pos)
				b.Set("context", c)
			}
			if strings.Contains(ctx, "://") || strings.HasPrefix(ctx, "git@") {
				return tree.Errorf(c.Pos, "build context %q: remote build contexts are not supported (use a directory of the project)", ctx)
			}
			r, err := resolvePath(dir, ctx)
			if err != nil {
				return tree.Errorf(c.Pos, "build context: %v", err)
			}
			c.Kind, c.Text = tree.String, r
		}
	}
	if e := s.Get("extends"); e != nil {
		if e.Kind == tree.String {
			m := tree.NewMap(e.Pos)
			m.Set("service", e)
			s.Set("extends", m)
			e = m
		}
		if e.Kind != tree.Map {
			return tree.Errorf(e.Pos, "extends is a service name or a mapping")
		}
		if f := e.Get("file"); f != nil {
			fs, err := f.AsString()
			if err != nil {
				return err
			}
			r, err := resolvePath(dir, fs)
			if err != nil {
				return tree.Errorf(f.Pos, "%v", err)
			}
			f.Kind, f.Text = tree.String, r
		}
	}
	if d := s.Get("depends_on"); d != nil && d.Kind == tree.Seq {
		m := &tree.Node{Kind: tree.Map, Pos: d.Pos, Tag: d.Tag}
		for _, it := range d.Items {
			name, err := it.AsString()
			if err != nil || it.Kind == tree.Null {
				return tree.Errorf(it.Pos, "depends_on entries are service names")
			}
			dep := tree.NewMap(it.Pos)
			dep.Set("condition", tree.NewString(ServiceStarted, it.Pos))
			dep.Set("required", &tree.Node{Kind: tree.Bool, Text: "true", Pos: it.Pos})
			if m.Has(name) {
				return tree.Errorf(it.Pos, "depends_on lists %q twice", name)
			}
			m.Set(name, dep)
		}
		s.Set("depends_on", m)
	}
	if n := s.Get("networks"); n != nil && n.Kind == tree.Seq {
		m := &tree.Node{Kind: tree.Map, Pos: n.Pos, Tag: n.Tag}
		for _, it := range n.Items {
			name, err := it.AsString()
			if err != nil || it.Kind == tree.Null {
				return tree.Errorf(it.Pos, "networks entries are network names")
			}
			m.Set(name, tree.NewMap(it.Pos))
		}
		s.Set("networks", m)
	}
	if n := s.Get("networks"); n != nil && n.Kind == tree.Map {
		for i, e := range n.Entries {
			if e.Value == nil || e.Value.Kind == tree.Null {
				n.Entries[i].Value = &tree.Node{Kind: tree.Map, Pos: e.KeyPos, Tag: tagOf(e.Value)}
			}
		}
	}
	if v := s.Get("volumes"); v != nil && v.Kind == tree.Seq {
		for i, it := range v.Items {
			if it.Kind == tree.String {
				m, err := shortVolume(it)
				if err != nil {
					return err
				}
				v.Items[i] = m
			}
		}
	}
	if ps := s.Get("ports"); ps != nil && ps.Kind == tree.Seq {
		for i, it := range ps.Items {
			if it.Kind == tree.String || it.Kind == tree.Int {
				m, err := shortPort(it)
				if err != nil {
					return err
				}
				ps.Items[i] = m
			}
		}
	}
	if ef := s.Get("env_file"); ef != nil {
		if ef.Kind == tree.String {
			ef = tree.NewSeq(ef.Pos, ef)
			s.Set("env_file", ef)
		}
		if ef.Kind == tree.Seq {
			for i, it := range ef.Items {
				if it.Kind == tree.String {
					m := tree.NewMap(it.Pos)
					m.Set("path", it)
					ef.Items[i] = m
					it = m
				}
				if it.Kind != tree.Map {
					return tree.Errorf(it.Pos, "env_file entries are paths or mappings")
				}
				p := it.Get("path")
				ps, err := p.AsString()
				if err != nil || p == nil {
					return tree.Errorf(it.Pos, "an env_file entry needs a path")
				}
				r, err := resolvePath(dir, ps)
				if err != nil {
					return tree.Errorf(p.Pos, "%v", err)
				}
				p.Kind, p.Text = tree.String, r
			}
		}
	}
	if h := s.Get("healthcheck"); h != nil && h.Kind == tree.Map {
		if t := h.Get("test"); t != nil && t.Kind == tree.String {
			h.Set("test", tree.NewSeq(t.Pos, tree.NewString("CMD-SHELL", t.Pos), t))
		}
	}
	return nil
}

// shortVolume parses `[SOURCE:]TARGET[:MODE]` (container_spec.md,
// volumes short syntax).
func shortVolume(n *tree.Node) (*tree.Node, error) {
	parts := strings.Split(n.Text, ":")
	m := tree.NewMap(n.Pos)
	var src, target, mode string
	switch len(parts) {
	case 1:
		target = parts[0]
	case 2:
		src, target = parts[0], parts[1]
	case 3:
		src, target, mode = parts[0], parts[1], parts[2]
	default:
		return nil, tree.Errorf(n.Pos, "invalid volume %q: [SOURCE:]TARGET[:MODE]", n.Text)
	}
	typ := "volume"
	if src != "" && (strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~")) {
		typ = "bind"
	}
	m.Set("type", tree.NewString(typ, n.Pos))
	if src != "" {
		m.Set("source", tree.NewString(src, n.Pos))
	}
	m.Set("target", tree.NewString(target, n.Pos))
	for _, o := range strings.Split(mode, ",") {
		switch o {
		case "", "rw":
		case "ro":
			m.Set("read_only", &tree.Node{Kind: tree.Bool, Text: "true", Pos: n.Pos})
		case "z", "Z":
			// SELinux relabelling: ignored where there is no SELinux.
		default:
			return nil, tree.Errorf(n.Pos, "invalid volume mode %q in %q", o, n.Text)
		}
	}
	return m, nil
}

// shortPort parses `[[IP:]HOST:]CONTAINER[/PROTOCOL]` (workload_spec.md,
// ports short syntax): ports or ranges, an IPv6 address bare ("::1:80:80")
// or in brackets ("[::1]:80:80"), an empty host part for a port the
// system picks ("127.0.0.1::80").
func shortPort(n *tree.Node) (*tree.Node, error) {
	s := n.Text
	m := tree.NewMap(n.Pos)
	bad := func() (*tree.Node, error) {
		return nil, tree.Errorf(n.Pos, "invalid port %q: [[IP:]HOST:]CONTAINER[/PROTOCOL]", s)
	}
	rest, proto, hasProto := strings.Cut(s, "/")
	if hasProto && (proto == "" || strings.Contains(proto, "/")) {
		return bad()
	}
	var ip, published, target string
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]:")
		if end < 0 {
			return bad()
		}
		ip, rest = rest[1:end], rest[end+2:]
		var ok bool
		if published, target, ok = strings.Cut(rest, ":"); !ok {
			return bad()
		}
	} else {
		parts := strings.Split(rest, ":")
		switch len(parts) {
		case 1:
			target = parts[0]
		case 2:
			published, target = parts[0], parts[1]
		default:
			target = parts[len(parts)-1]
			published = parts[len(parts)-2]
			ip = strings.Join(parts[:len(parts)-2], ":")
		}
	}
	if target == "" {
		return bad()
	}
	m.Set("target", tree.NewString(target, n.Pos))
	if published != "" {
		m.Set("published", tree.NewString(published, n.Pos))
	}
	if ip != "" {
		m.Set("host_ip", tree.NewString(ip, n.Pos))
	}
	if hasProto {
		m.Set("protocol", tree.NewString(proto, n.Pos))
	}
	return m, nil
}

// pathIs matches a path of keys against a pattern of keys and "*".
func pathIs(p []string, pattern ...string) bool {
	if len(p) != len(pattern) {
		return false
	}
	for i := range p {
		if pattern[i] != "*" && pattern[i] != p[i] {
			return false
		}
	}
	return true
}

// mergeNodes merges over onto base (Compose Specification, merge and
// override): mappings merge, sequences append, scalars override, with
// the exceptions of command, entrypoint and healthcheck.test (replaced)
// and of volumes, ports, secrets and configs (unique keys). `!reset`
// removes a value, `!override` replaces it whole. A nil result removes
// the key.
func mergeNodes(base, over *tree.Node, p []string) *tree.Node {
	switch {
	case over == nil:
		return base
	case over.Tag == tree.Reset:
		return nil
	case over.Tag == tree.Override:
		return clean(over)
	case base == nil:
		return clean(over)
	}
	if base.Kind == tree.Map && over.Kind == tree.Map {
		out := base.Clone()
		for _, e := range over.Entries {
			m := mergeNodes(out.Get(e.Key), e.Value, append(p, e.Key))
			if m == nil {
				out.Delete(e.Key)
				continue
			}
			if i := out.Index(e.Key); i >= 0 {
				out.Entries[i].Value = m
			} else {
				out.Entries = append(out.Entries, tree.Entry{Key: e.Key, KeyPos: e.KeyPos, Value: m})
			}
		}
		return out
	}
	if base.Kind == tree.Seq && over.Kind == tree.Seq {
		switch {
		case pathIs(p, "services", "*", "command"), pathIs(p, "services", "*", "entrypoint"),
			pathIs(p, "services", "*", "healthcheck", "test"):
			return over
		case pathIs(p, "services", "*", "volumes"), pathIs(p, "services", "*", "secrets"),
			pathIs(p, "services", "*", "configs"):
			return mergeUnique(base, over, func(n *tree.Node) string {
				if n.Kind == tree.Map {
					if t := n.Get("target"); t != nil {
						return t.Text
					}
					if s := n.Get("source"); s != nil {
						return s.Text
					}
				}
				return n.Text
			})
		case pathIs(p, "services", "*", "ports"):
			// Unique by {ip, target, published, protocol} (13-merge.md).
			return mergeUnique(base, over, func(n *tree.Node) string {
				if n.Kind != tree.Map {
					return scalarKey(n)
				}
				get := func(k string) string { v, _ := n.Get(k).AsString(); return v }
				proto := get("protocol")
				if proto == "" {
					proto = "tcp"
				}
				return get("host_ip") + "|" + get("target") + "|" + get("published") + "|" + proto
			})
		case pathIs(p, "services", "*", "profiles"):
			return mergeUnique(base, over, scalarKey)
		}
		out := base.Clone()
		for _, it := range over.Items {
			if c := clean(it); c != nil {
				out.Items = append(out.Items, c)
			}
		}
		return out
	}
	return clean(over)
}

func scalarKey(n *tree.Node) string {
	if n.IsScalar() {
		return n.Text
	}
	return fmt.Sprintf("%p", n)
}

// mergeUnique appends over's items to base's, an item sharing a key
// with one of base's replacing it in place.
func mergeUnique(base, over *tree.Node, key func(*tree.Node) string) *tree.Node {
	out := base.Clone()
	idx := map[string]int{}
	for i, it := range out.Items {
		idx[key(it)] = i
	}
	for _, it := range over.Items {
		it = clean(it)
		if it == nil {
			continue
		}
		k := key(it)
		if i, ok := idx[k]; ok {
			out.Items[i] = it
			continue
		}
		idx[k] = len(out.Items)
		out.Items = append(out.Items, it)
	}
	return out
}

// clean drops the `!reset` values of a subtree and clears its tags.
func clean(n *tree.Node) *tree.Node {
	if n == nil || n.Tag == tree.Reset {
		return nil
	}
	n.Tag = tree.NoTag
	if n.Kind == tree.Map {
		kept := n.Entries[:0]
		for _, e := range n.Entries {
			if e.Value = clean(e.Value); e.Value != nil {
				kept = append(kept, e)
			}
		}
		n.Entries = kept
	}
	if n.Kind == tree.Seq {
		kept := n.Items[:0]
		for _, it := range n.Items {
			if it = clean(it); it != nil {
				kept = append(kept, it)
			}
		}
		n.Items = kept
	}
	return n
}

// extendsDedup are the sequences whose duplicates an extends merge drops
// (05-services.md, extends, "Sequences").
var extendsDedup = map[string]bool{
	"cap_add": true, "cap_drop": true, "configs": true, "device_cgroup_rules": true,
	"expose": true, "external_links": true, "ports": true, "secrets": true, "security_opt": true,
}

// resolveExtends merges each service onto the one it extends.
func (l *loader) resolveExtends(doc *tree.Node, dir string) error {
	services := doc.Get("services")
	if services == nil {
		return nil
	}
	files := map[string]*tree.Node{} // extends.file -> its services
	done := map[string]bool{}
	var resolve func(svcs *tree.Node, file, name string, stack []string) (*tree.Node, error)
	resolve = func(svcs *tree.Node, file, name string, stack []string) (*tree.Node, error) {
		id := file + "#" + name
		for _, s := range stack {
			if s == id {
				return nil, fmt.Errorf("extends cycle: %s -> %s", strings.Join(stack, " -> "), id)
			}
		}
		if len(stack) > maxExtendsChain {
			return nil, fmt.Errorf("extends chain longer than %d", maxExtendsChain)
		}
		svc := svcs.Get(name)
		if svc == nil {
			return nil, fmt.Errorf("extends: service %q not found in %s", name, file)
		}
		ext := svc.Get("extends")
		if ext == nil || (file == "" && done[name]) {
			return svc, nil
		}
		baseName, err := ext.Get("service").AsString()
		if err != nil || baseName == "" {
			return nil, tree.Errorf(ext.Pos, "extends needs a service")
		}
		baseSvcs, baseFile := svcs, file
		if f := ext.Get("file"); f != nil {
			baseFile = f.Text
			bs, ok := files[baseFile]
			if !ok {
				p, err := l.parse(baseFile, l.env)
				if err != nil {
					return nil, err
				}
				if err := l.interpolate(p, l.env); err != nil {
					return nil, err
				}
				if err := l.canonical(p.doc, path.Dir(baseFile)); err != nil {
					return nil, err
				}
				bs = p.doc.Get("services")
				if bs == nil {
					return nil, tree.Errorf(f.Pos, "%s defines no services", baseFile)
				}
				files[baseFile] = bs
			}
			baseSvcs = bs
		}
		base, err := resolve(baseSvcs, baseFile, baseName, append(stack, id))
		if err != nil {
			return nil, tree.Errorf(ext.Pos, "%v", err)
		}
		own := svc.Clone()
		own.Delete("extends")
		merged := mergeNodes(clean(base.Clone()), own, []string{"services", name})
		for i, e := range merged.Entries {
			if extendsDedup[e.Key] && e.Value.Kind == tree.Seq {
				merged.Entries[i].Value = mergeUnique(tree.NewSeq(e.Value.Pos), e.Value, scalarKey)
			}
		}
		merged.Delete("extends")
		if file == "" {
			services.Set(name, merged)
			done[name] = true
		}
		return merged, nil
	}
	for _, e := range services.Entries {
		if _, err := resolve(services, "", e.Key, nil); err != nil {
			return err
		}
	}
	return nil
}
