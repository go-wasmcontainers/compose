package tree

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ParseYAML reads one YAML 1.2 document into a tree.
//
// Anchors and aliases are resolved here (Compose Specification, fragments:
// before interpolation), with every use of an alias counted against
// Limits.MaxNodes. Merge keys (`<<`, the YAML 1.1 merge type that Compose
// files rely on) take a mapping or a sequence of mappings; keys of the
// mapping itself win, then earlier sources over later ones. Keys must be
// unique (YAML 1.2 §3.2.1.3). The tags `!reset` and `!override` are kept
// for the merge; any other non-standard tag is refused.
func ParseYAML(file string, data []byte, lim Limits) (*Node, error) {
	b := &budget{lim: lim.WithDefaults(), file: file}
	if err := b.size(data); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Errorf(Pos{File: file}, "empty file")
		}
		return nil, yamlError(file, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, Errorf(Pos{File: file, Line: extra.Line, Col: extra.Column}, "more than one YAML document in the file")
	} else if !errors.Is(err, io.EOF) {
		return nil, yamlError(file, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, Errorf(Pos{File: file}, "empty file")
	}
	c := &yconv{b: b, active: map[*yaml.Node]bool{}}
	return c.conv(doc.Content[0], 0)
}

// yamlError turns the parser's "yaml: line N: msg" into a positioned error.
func yamlError(file string, err error) error {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: ")
	if rest, ok := strings.CutPrefix(msg, "line "); ok {
		if i := strings.Index(rest, ": "); i > 0 {
			if n, aerr := strconv.Atoi(rest[:i]); aerr == nil {
				return &Error{Pos: Pos{File: file, Line: n, Col: 1}, Msg: rest[i+2:]}
			}
		}
	}
	return &Error{Pos: Pos{File: file}, Msg: msg}
}

type yconv struct {
	b      *budget
	active map[*yaml.Node]bool // anchors being expanded: an alias to one is a cycle
}

func (c *yconv) pos(n *yaml.Node) Pos { return Pos{File: c.b.file, Line: n.Line, Col: n.Column} }

func (c *yconv) conv(n *yaml.Node, depth int) (*Node, error) {
	p := c.pos(n)
	if err := c.b.node(p, depth); err != nil {
		return nil, err
	}
	var tag Tag
	switch n.Tag {
	case "!reset":
		tag = Reset
	case "!override":
		tag = Override
	}
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, Errorf(p, "unknown anchor %q", n.Value)
		}
		if c.active[n.Alias] {
			return nil, Errorf(p, "alias *%s refers to a node that contains it", n.Value)
		}
		c.active[n.Alias] = true
		out, err := c.conv(n.Alias, depth)
		delete(c.active, n.Alias)
		if err != nil {
			return nil, err
		}
		// The alias is where the value is used: report it there.
		out = out.Clone()
		out.Pos = p
		return out, nil
	case yaml.ScalarNode:
		return c.scalar(n, p, tag)
	case yaml.SequenceNode:
		if err := c.checkTag(n, p, "!!seq"); err != nil {
			return nil, err
		}
		out := &Node{Kind: Seq, Pos: p, Tag: tag, Items: make([]*Node, 0, len(n.Content))}
		for _, it := range n.Content {
			v, err := c.conv(it, depth+1)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, v)
		}
		return out, nil
	case yaml.MappingNode:
		if err := c.checkTag(n, p, "!!map"); err != nil {
			return nil, err
		}
		return c.mapping(n, p, tag, depth)
	}
	return nil, Errorf(p, "unsupported YAML node")
}

func (c *yconv) checkTag(n *yaml.Node, p Pos, std string) error {
	switch n.Tag {
	case "", "!", "!reset", "!override", std:
		return nil
	}
	if n.ShortTag() == std {
		return nil
	}
	return Errorf(p, "unsupported YAML tag %s", n.Tag)
}

func (c *yconv) scalar(n *yaml.Node, p Pos, tag Tag) (*Node, error) {
	if err := c.b.str(p, n.Value); err != nil {
		return nil, err
	}
	out := &Node{Pos: p, Tag: tag, Text: n.Value}
	st := n.ShortTag()
	if tag != NoTag {
		// !reset / !override on a scalar: resolve as if untagged.
		cp := *n
		cp.Tag = ""
		st = cp.ShortTag()
	}
	switch st {
	case "!!str", "!!timestamp", "!!binary":
		out.Kind = String
	case "!!null":
		out.Kind, out.Text = Null, ""
	case "!!bool":
		out.Kind = Bool
		out.Text = strconv.FormatBool(strings.EqualFold(n.Value, "true"))
	case "!!int":
		if v, err := parseYAMLInt(n.Value); err == nil {
			out.Kind, out.Text = Int, strconv.FormatInt(v, 10)
		} else if f, ferr := strconv.ParseFloat(n.Value, 64); ferr == nil {
			num := Number(f, p)
			out.Kind, out.Text = num.Kind, num.Text
		} else {
			out.Kind = String
		}
	case "!!float":
		// The parser calls 0800 a float (Go reads 0-prefixed integers as
		// octal); YAML 1.2's core schema calls it the integer 800.
		if v, err := strconv.ParseInt(strings.TrimPrefix(n.Value, "+"), 10, 64); err == nil {
			out.Kind, out.Text = Int, strconv.FormatInt(v, 10)
			break
		}
		f, err := parseYAMLFloat(n.Value)
		switch {
		case err != nil:
			out.Kind = String
		case math.IsInf(f, 0) || math.IsNaN(f):
			// The Compose model is JSON's (its schema is a JSON schema).
			return nil, Errorf(p, "%s: infinities and NaN are not Compose values", n.Value)
		default:
			num := Number(f, p)
			out.Kind, out.Text = num.Kind, num.Text
		}
	case "!!merge":
		out.Kind = String
	default:
		return nil, Errorf(p, "unsupported YAML tag %s", n.Tag)
	}
	return out, nil
}

// parseYAMLInt reads a YAML 1.2 core-schema integer: base 10, 0o octal,
// 0x hexadecimal (a leading zero is not octal, unlike in Go).
func parseYAMLInt(s string) (int64, error) {
	s = strings.ReplaceAll(s, "_", "")
	digits := strings.TrimLeft(s, "+-")
	if len(digits) > 2 && digits[0] == '0' && strings.ContainsRune("oOxXbB", rune(digits[1])) {
		return strconv.ParseInt(s, 0, 64)
	}
	return strconv.ParseInt(s, 10, 64)
}

func parseYAMLFloat(s string) (float64, error) {
	switch strings.ToLower(strings.TrimPrefix(s, "+")) {
	case ".inf":
		return math.Inf(1), nil
	case "-.inf":
		return math.Inf(-1), nil
	case ".nan":
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && k.Style == 0 && (k.Tag == "" || k.Tag == "!" || k.Tag == "!!merge" || k.Tag == "tag:yaml.org,2002:merge")
}

func (c *yconv) mapping(n *yaml.Node, p Pos, tag Tag, depth int) (*Node, error) {
	out := &Node{Kind: Map, Pos: p, Tag: tag}
	var merges []*yaml.Node
	if len(n.Content)%2 != 0 {
		return nil, Errorf(p, "malformed mapping")
	}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if isMergeKey(k) {
			merges = append(merges, v)
			continue
		}
		kp := c.pos(k)
		key, err := c.key(k, kp)
		if err != nil {
			return nil, err
		}
		if j := out.Index(key); j >= 0 {
			return nil, Errorf(kp, "duplicate key %q (first at %s)", key, out.Entries[j].KeyPos)
		}
		val, err := c.conv(v, depth+1)
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, Entry{Key: key, KeyPos: kp, Value: val})
	}
	// Merge keys: each source in turn fills keys not yet present, so
	// the mapping's own keys win, then earlier sources over later ones.
	for _, m := range merges {
		srcs := []*yaml.Node{m}
		mp := c.pos(m)
		target := m
		if target.Kind == yaml.AliasNode && target.Alias != nil {
			target = target.Alias
		}
		if target.Kind == yaml.SequenceNode {
			srcs = target.Content
		}
		for _, s := range srcs {
			v, err := c.conv(s, depth+1)
			if err != nil {
				return nil, err
			}
			if v.Kind != Map {
				return nil, Errorf(mp, "a merge key (<<) takes a mapping or a sequence of mappings, found a %s", v.Kind)
			}
			for _, e := range v.Entries {
				if !out.Has(e.Key) {
					out.Entries = append(out.Entries, e)
				}
			}
		}
	}
	return out, nil
}

func (c *yconv) key(k *yaml.Node, p Pos) (string, error) {
	if k.Kind == yaml.AliasNode && k.Alias != nil {
		k = k.Alias
	}
	if k.Kind != yaml.ScalarNode {
		return "", Errorf(p, "a mapping key must be a scalar")
	}
	if err := c.b.str(p, k.Value); err != nil {
		return "", err
	}
	if k.ShortTag() == "!!null" && k.Style == 0 && (k.Value == "" || k.Value == "~") {
		return "", Errorf(p, "a mapping key cannot be null")
	}
	return k.Value, nil
}
