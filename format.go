package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-wasmcontainers/compose/tree"
	"go.yaml.in/yaml/v3"
)

// Format is a Compose file format.
type Format string

// The formats a project can be written in.
const (
	YAML Format = "yaml"
	JSON Format = "json"
	HCL  Format = "hcl"
)

// Marshal writes the normalised model in a format; loading the result
// gives the same model back (the round trip is tested for all three).
func Marshal(p *Project, f Format) ([]byte, error) {
	t := p.Tree()
	switch f {
	case YAML:
		return marshalYAML(t)
	case JSON:
		return marshalJSON(t), nil
	case HCL:
		return marshalHCL(t)
	}
	return nil, fmt.Errorf("unknown format %q (yaml, json or hcl)", f)
}

// Tree returns the normalised model as a document: long syntax
// everywhere, keys sorted, defaults made explicit.
func (p *Project) Tree() *tree.Node {
	root := tree.NewMap(tree.Pos{})
	root.Set("name", tree.NewString(p.Name, tree.Pos{}))
	svcs := tree.NewMap(tree.Pos{})
	for _, n := range p.ServiceNames() {
		svcs.Set(n, p.Services[n].tree())
	}
	root.Set("services", svcs)
	if len(p.Networks) > 0 {
		m := tree.NewMap(tree.Pos{})
		for _, k := range sortedKeys(p.Networks) {
			nw := p.Networks[k]
			e := tree.NewMap(tree.Pos{})
			e.Set("name", str2(nw.Name))
			if nw.External {
				e.Set("external", boolNode(true))
			}
			if nw.Driver != "" {
				e.Set("driver", str2(nw.Driver))
			}
			if nw.Internal {
				e.Set("internal", boolNode(true))
			}
			setStringMap(e, "labels", nw.Labels)
			setExtensions(e, nw.Extensions)
			m.Set(k, e)
		}
		root.Set("networks", m)
	}
	if len(p.Volumes) > 0 {
		m := tree.NewMap(tree.Pos{})
		for _, k := range sortedKeys(p.Volumes) {
			v := p.Volumes[k]
			e := tree.NewMap(tree.Pos{})
			e.Set("name", str2(v.Name))
			if v.External {
				e.Set("external", boolNode(true))
			}
			if v.Driver != "" {
				e.Set("driver", str2(v.Driver))
			}
			setStringMap(e, "labels", v.Labels)
			setExtensions(e, v.Extensions)
			m.Set(k, e)
		}
		root.Set("volumes", m)
	}
	setExtensions(root, p.Extensions)
	return root
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str2(s string) *tree.Node { return tree.NewString(s, tree.Pos{}) }

func boolNode(b bool) *tree.Node {
	return &tree.Node{Kind: tree.Bool, Text: strconv.FormatBool(b)}
}

func intNode(i int64) *tree.Node {
	return &tree.Node{Kind: tree.Int, Text: strconv.FormatInt(i, 10)}
}

func strSeq(s []string) *tree.Node {
	out := tree.NewSeq(tree.Pos{})
	out.Items = []*tree.Node{}
	for _, v := range s {
		out.Items = append(out.Items, str2(v))
	}
	return out
}

func setStringMap(n *tree.Node, key string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	out := tree.NewMap(tree.Pos{})
	for _, k := range sortedKeys(m) {
		out.Set(k, str2(m[k]))
	}
	n.Set(key, out)
}

func setExtensions(n *tree.Node, ext map[string]any) {
	for _, k := range sortedKeys(ext) {
		n.Set(k, fromPlain(ext[k]))
	}
}

func fromPlain(v any) *tree.Node {
	switch x := v.(type) {
	case nil:
		return &tree.Node{Kind: tree.Null}
	case string:
		return str2(x)
	case bool:
		return boolNode(x)
	case int64:
		return intNode(x)
	case float64:
		return &tree.Node{Kind: tree.Float, Text: strconv.FormatFloat(x, 'g', -1, 64)}
	case map[string]any:
		m := tree.NewMap(tree.Pos{})
		for _, k := range sortedKeys(x) {
			m.Set(k, fromPlain(x[k]))
		}
		return m
	case []any:
		s := tree.NewSeq(tree.Pos{})
		s.Items = []*tree.Node{}
		for _, it := range x {
			s.Items = append(s.Items, fromPlain(it))
		}
		return s
	}
	return str2(fmt.Sprint(v))
}

func durNode(d *time.Duration) *tree.Node { return str2(d.String()) }

func (s *Service) tree() *tree.Node {
	n := tree.NewMap(tree.Pos{})
	if s.Build != nil {
		b := tree.NewMap(tree.Pos{})
		if len(s.Build.Args) > 0 {
			a := tree.NewMap(tree.Pos{})
			for _, k := range sortedKeys(s.Build.Args) {
				if v := s.Build.Args[k]; v != nil {
					a.Set(k, str2(*v))
				} else {
					a.Set(k, &tree.Node{Kind: tree.Null})
				}
			}
			b.Set("args", a)
		}
		b.Set("context", str2(s.Build.Context))
		if s.Build.Dockerfile != "" {
			b.Set("dockerfile", str2(s.Build.Dockerfile))
		}
		if s.Build.DockerfileInline != "" {
			b.Set("dockerfile_inline", str2(s.Build.DockerfileInline))
		}
		if s.Build.NoCache {
			b.Set("no_cache", boolNode(true))
		}
		if len(s.Build.Tags) > 0 {
			b.Set("tags", strSeq(s.Build.Tags))
		}
		if s.Build.Target != "" {
			b.Set("target", str2(s.Build.Target))
		}
		n.Set("build", b)
	}
	if s.Command != nil {
		n.Set("command", strSeq(s.Command))
	}
	if s.ContainerName != "" {
		n.Set("container_name", str2(s.ContainerName))
	}
	if len(s.DependsOn) > 0 {
		d := tree.NewMap(tree.Pos{})
		for _, k := range sortedKeys(s.DependsOn) {
			dep := s.DependsOn[k]
			e := tree.NewMap(tree.Pos{})
			e.Set("condition", str2(dep.Condition))
			e.Set("required", boolNode(dep.Required))
			if dep.Restart {
				e.Set("restart", boolNode(true))
			}
			d.Set(k, e)
		}
		n.Set("depends_on", d)
	}
	if s.Replicas != 1 {
		d := tree.NewMap(tree.Pos{})
		d.Set("replicas", intNode(int64(s.Replicas)))
		n.Set("deploy", d)
	}
	if s.Entrypoint != nil {
		n.Set("entrypoint", strSeq(s.Entrypoint))
	}
	setStringMap(n, "environment", s.Environment)
	if h := s.Healthcheck; h != nil {
		hn := tree.NewMap(tree.Pos{})
		if h.Disable {
			hn.Set("disable", boolNode(true))
		}
		if h.Interval != nil {
			hn.Set("interval", durNode(h.Interval))
		}
		if h.Retries != nil {
			hn.Set("retries", intNode(*h.Retries))
		}
		if h.StartInterval != nil {
			hn.Set("start_interval", durNode(h.StartInterval))
		}
		if h.StartPeriod != nil {
			hn.Set("start_period", durNode(h.StartPeriod))
		}
		if h.Test != nil {
			hn.Set("test", strSeq(h.Test))
		}
		if h.Timeout != nil {
			hn.Set("timeout", durNode(h.Timeout))
		}
		n.Set("healthcheck", hn)
	}
	if s.Image != "" {
		n.Set("image", str2(s.Image))
	}
	setStringMap(n, "labels", s.Labels)
	if s.NetworkMode != "" {
		n.Set("network_mode", str2(s.NetworkMode))
	}
	if len(s.Networks) > 0 {
		m := tree.NewMap(tree.Pos{})
		for _, k := range sortedKeys(s.Networks) {
			e := tree.NewMap(tree.Pos{})
			if a := s.Networks[k].Aliases; len(a) > 0 {
				e.Set("aliases", strSeq(a))
			}
			m.Set(k, e)
		}
		n.Set("networks", m)
	}
	if len(s.Profiles) > 0 {
		n.Set("profiles", strSeq(s.Profiles))
	}
	if s.Restart != "" {
		n.Set("restart", str2(s.Restart))
	}
	if len(s.Volumes) > 0 {
		v := tree.NewSeq(tree.Pos{})
		for _, m := range s.Volumes {
			e := tree.NewMap(tree.Pos{})
			e.Set("type", str2(m.Type))
			e.Set("source", str2(m.Source))
			e.Set("target", str2(m.Target))
			if m.ReadOnly {
				e.Set("read_only", boolNode(true))
			}
			v.Items = append(v.Items, e)
		}
		n.Set("volumes", v)
	}
	if s.WorkingDir != "" {
		n.Set("working_dir", str2(s.WorkingDir))
	}
	setExtensions(n, s.Extensions)
	return n
}

// escapeDollar protects a value from interpolation when it is read back.
func escapeDollar(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func marshalYAML(t *tree.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(toYAML(t)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func toYAML(n *tree.Node) *yaml.Node {
	switch n.Kind {
	case tree.Map:
		y := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, e := range n.Entries {
			y.Content = append(y.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.Key}, toYAML(e.Value))
		}
		return y
	case tree.Seq:
		y := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, it := range n.Items {
			y.Content = append(y.Content, toYAML(it))
		}
		return y
	case tree.Null:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case tree.Bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: n.Text}
	case tree.Int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: n.Text}
	case tree.Float:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: yamlFloat(n.Text)}
	}
	y := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: escapeDollar(n.Text)}
	if strings.Contains(n.Text, "\n") {
		y.Style = yaml.DoubleQuotedStyle
	}
	return y
}

func yamlFloat(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	switch {
	case err != nil:
		return s
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	case math.IsNaN(f):
		return ".nan"
	}
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func marshalJSON(t *tree.Node) []byte {
	var b bytes.Buffer
	writeJSON(&b, t, "")
	b.WriteByte('\n')
	return b.Bytes()
}

func jsonString(b *bytes.Buffer, s string) {
	enc, _ := json.Marshal(s)
	b.Write(enc)
}

func writeJSON(b *bytes.Buffer, n *tree.Node, indent string) {
	in := indent + "  "
	switch n.Kind {
	case tree.Map:
		if len(n.Entries) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, e := range n.Entries {
			b.WriteString(in)
			jsonString(b, e.Key)
			b.WriteString(": ")
			writeJSON(b, e.Value, in)
			if i < len(n.Entries)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case tree.Seq:
		if len(n.Items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, it := range n.Items {
			b.WriteString(in)
			writeJSON(b, it, in)
			if i < len(n.Items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case tree.Null:
		b.WriteString("null")
	case tree.Bool, tree.Int:
		b.WriteString(n.Text)
	case tree.Float:
		f, err := strconv.ParseFloat(n.Text, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			jsonString(b, n.Text) // JSON has no infinities
			return
		}
		b.WriteString(n.Text)
	default:
		jsonString(b, escapeDollar(n.Text))
	}
}

// The HCL2 mapping (docs/HCL.md): services, networks and volumes are
// labelled blocks; in a service, depends_on and networks entries are
// labelled blocks, volumes are repeated `volume` blocks, and build,
// deploy and healthcheck are blocks; everything else is an attribute.
// (Written by hand: hclwrite would bring go-cmp into the browser build.)
func marshalHCL(t *tree.Node) ([]byte, error) {
	var b bytes.Buffer
	first := true
	for _, e := range t.Entries {
		switch e.Key {
		case "services", "networks", "volumes":
			blockType := map[string]string{"services": "service", "networks": "network", "volumes": "volume"}[e.Key]
			ctx := "other"
			if e.Key == "services" {
				ctx = "service"
			}
			for _, se := range e.Value.Entries {
				if !first {
					b.WriteByte('\n')
				}
				first = false
				b.WriteString(blockType + " ")
				hclString(&b, se.Key)
				if err := hclBlock(&b, se.Value, ctx, ""); err != nil {
					return nil, err
				}
			}
		default:
			if err := hclAttr(&b, e.Key, e.Value, ""); err != nil {
				return nil, err
			}
			first = false
		}
	}
	return b.Bytes(), nil
}

// hclBlock writes ` {`, a body, `}`.
func hclBlock(b *bytes.Buffer, n *tree.Node, ctx, indent string) error {
	if len(n.Entries) == 0 {
		b.WriteString(" {}\n")
		return nil
	}
	b.WriteString(" {\n")
	if err := hclBody(b, n, ctx, indent+"  "); err != nil {
		return err
	}
	b.WriteString(indent + "}\n")
	return nil
}

func hclBody(b *bytes.Buffer, n *tree.Node, ctx, indent string) error {
	for _, e := range n.Entries {
		if ctx == "service" {
			switch e.Key {
			case "depends_on", "networks":
				bt := map[string]string{"depends_on": "depends_on", "networks": "network"}[e.Key]
				for _, de := range e.Value.Entries {
					b.WriteString(indent + bt + " ")
					hclString(b, de.Key)
					if err := hclBlock(b, de.Value, "other", indent); err != nil {
						return err
					}
				}
				continue
			case "volumes":
				for _, it := range e.Value.Items {
					b.WriteString(indent + "volume")
					if err := hclBlock(b, it, "other", indent); err != nil {
						return err
					}
				}
				continue
			case "build", "deploy", "healthcheck":
				b.WriteString(indent + e.Key)
				if err := hclBlock(b, e.Value, "other", indent); err != nil {
					return err
				}
				continue
			}
		}
		if err := hclAttr(b, e.Key, e.Value, indent); err != nil {
			return err
		}
	}
	return nil
}

func hclAttr(b *bytes.Buffer, key string, v *tree.Node, indent string) error {
	if !hclIdent(key) {
		return fmt.Errorf("%q cannot be an HCL attribute name", key)
	}
	b.WriteString(indent + key + " = ")
	hclValue(b, v, indent)
	b.WriteByte('\n')
	return nil
}

func hclValue(b *bytes.Buffer, n *tree.Node, indent string) {
	switch n.Kind {
	case tree.Null:
		b.WriteString("null")
	case tree.Bool, tree.Int:
		b.WriteString(n.Text)
	case tree.Float:
		f, err := strconv.ParseFloat(n.Text, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			hclString(b, n.Text) // HCL has no literal for these
			return
		}
		b.WriteString(n.Text)
	case tree.String:
		hclString(b, n.Text)
	case tree.Seq:
		b.WriteByte('[')
		for i, it := range n.Items {
			if i > 0 {
				b.WriteString(", ")
			}
			hclValue(b, it, indent)
		}
		b.WriteByte(']')
	case tree.Map:
		if len(n.Entries) == 0 {
			b.WriteString("{}")
			return
		}
		in := indent + "  "
		b.WriteString("{\n")
		for _, e := range n.Entries {
			b.WriteString(in)
			if hclIdent(e.Key) {
				b.WriteString(e.Key)
			} else {
				hclString(b, e.Key)
			}
			b.WriteString(" = ")
			hclValue(b, e.Value, in)
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	}
}

// hclString writes a quoted template whose value is s: escapes per the
// HCL native syntax (\n \r \t \" \\ \uNNNN), and "${" / "%{" doubled.
func hclString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '$', '%':
			b.WriteRune(r)
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteRune(r)
			}
		default:
			if r < 0x20 || r == 0x7f || r == utf8.RuneError {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func hclIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && (r == '-' || r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}
