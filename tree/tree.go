// Package tree is the format-neutral document a Compose file is read into:
// mappings (ordered, unique keys), sequences and scalars, each carrying the
// file, line and column it came from. The YAML, JSON and HCL2 readers all
// produce it, so interpolation, merging and decoding are written once.
//
// Every reader is bounded (Limits): a Compose file is untrusted input.
package tree

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Kind is a node's type.
type Kind uint8

// The kinds of node.
const (
	Null Kind = iota
	String
	Int
	Float
	Bool
	Map
	Seq
)

func (k Kind) String() string {
	switch k {
	case Null:
		return "null"
	case String:
		return "string"
	case Int:
		return "integer"
	case Float:
		return "number"
	case Bool:
		return "boolean"
	case Map:
		return "mapping"
	case Seq:
		return "sequence"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Pos is where a node starts: a file and a 1-based line and column.
type Pos struct {
	File      string
	Line, Col int
}

func (p Pos) String() string {
	if p.File == "" && p.Line == 0 {
		return "<input>"
	}
	if p.Line == 0 {
		return p.File
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// Tag is a merge directive attached to a value of an override file
// (Compose Specification, merge rules: !reset and !override).
type Tag uint8

// The merge directives.
const (
	NoTag Tag = iota
	Reset
	Override
)

// Entry is one key of a mapping.
type Entry struct {
	Key    string
	KeyPos Pos
	Value  *Node
}

// Node is a value of a Compose document.
type Node struct {
	Kind Kind
	Pos  Pos
	Tag  Tag
	// Text is a scalar's value: the string itself, a number's literal
	// (base 10 after reading), "true" or "false".
	Text    string
	Entries []Entry // Map
	Items   []*Node // Seq
}

// Error is a problem at a place in a file.
type Error struct {
	Pos Pos
	Msg string
}

func (e *Error) Error() string { return e.Pos.String() + ": " + e.Msg }

// Errorf returns an *Error at p.
func Errorf(p Pos, format string, a ...any) error {
	return &Error{Pos: p, Msg: fmt.Sprintf(format, a...)}
}

// NewString returns a string scalar.
func NewString(s string, p Pos) *Node { return &Node{Kind: String, Text: s, Pos: p} }

// NewMap returns an empty mapping.
func NewMap(p Pos) *Node { return &Node{Kind: Map, Pos: p} }

// NewSeq returns a sequence of items.
func NewSeq(p Pos, items ...*Node) *Node { return &Node{Kind: Seq, Pos: p, Items: items} }

// IsScalar says whether the node is a string, number, boolean or null.
func (n *Node) IsScalar() bool { return n != nil && n.Kind <= Bool }

// Index returns the position of key in a mapping, or -1.
func (n *Node) Index(key string) int {
	if n == nil || n.Kind != Map {
		return -1
	}
	for i := range n.Entries {
		if n.Entries[i].Key == key {
			return i
		}
	}
	return -1
}

// Get returns a mapping's value for key, or nil.
func (n *Node) Get(key string) *Node {
	if i := n.Index(key); i >= 0 {
		return n.Entries[i].Value
	}
	return nil
}

// Has says whether a mapping has key (even with a null value).
func (n *Node) Has(key string) bool { return n.Index(key) >= 0 }

// Set replaces key's value, or appends the entry.
func (n *Node) Set(key string, v *Node) {
	if i := n.Index(key); i >= 0 {
		n.Entries[i].Value = v
		return
	}
	p := n.Pos
	if v != nil {
		p = v.Pos
	}
	n.Entries = append(n.Entries, Entry{Key: key, KeyPos: p, Value: v})
}

// Delete removes key from a mapping.
func (n *Node) Delete(key string) {
	if i := n.Index(key); i >= 0 {
		n.Entries = append(n.Entries[:i], n.Entries[i+1:]...)
	}
}

// Clone returns a deep copy.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Entries != nil {
		c.Entries = make([]Entry, len(n.Entries))
		for i, e := range n.Entries {
			c.Entries[i] = Entry{Key: e.Key, KeyPos: e.KeyPos, Value: e.Value.Clone()}
		}
	}
	if n.Items != nil {
		c.Items = make([]*Node, len(n.Items))
		for i, it := range n.Items {
			c.Items[i] = it.Clone()
		}
	}
	return &c
}

// Scalar returns a scalar's text as a string value: what a string field
// reads (numbers and booleans as written, null as "").
func (n *Node) Scalar() (string, error) {
	if n == nil {
		return "", nil
	}
	if !n.IsScalar() {
		return "", Errorf(n.Pos, "expected a scalar, found a %s", n.Kind)
	}
	return n.Text, nil
}

// AsString reads a string field: a string, or a number or boolean written
// where a string is expected (as YAML users do: `version: 3`).
func (n *Node) AsString() (string, error) {
	switch {
	case n == nil || n.Kind == Null:
		return "", nil
	case n.IsScalar():
		return n.Text, nil
	}
	return "", Errorf(n.Pos, "expected a string, found a %s", n.Kind)
}

// AsBool reads a boolean field: a boolean, or a string spelling one (the
// value of an interpolated variable).
func (n *Node) AsBool() (bool, error) {
	if n == nil || n.Kind == Null {
		return false, nil
	}
	if n.Kind == Bool || n.Kind == String {
		switch strings.ToLower(n.Text) {
		case "true", "yes", "y", "on":
			return true, nil
		case "false", "no", "n", "off", "":
			return false, nil
		}
	}
	return false, Errorf(n.Pos, "expected a boolean, found %s %q", n.Kind, n.Text)
}

// AsInt reads an integer field: an integer, or a string holding one.
func (n *Node) AsInt() (int64, error) {
	if n == nil || n.Kind == Null {
		return 0, nil
	}
	if n.Kind == Int || n.Kind == String || n.Kind == Float {
		v, err := strconv.ParseInt(strings.TrimSpace(n.Text), 10, 64)
		if err == nil {
			return v, nil
		}
		if f, ferr := strconv.ParseFloat(n.Text, 64); ferr == nil && f == float64(int64(f)) && f < 1<<53 && f > -(1<<53) {
			return int64(f), nil
		}
	}
	return 0, Errorf(n.Pos, "expected an integer, found %s %q", n.Kind, n.Text)
}

// Walk calls fn on n and every node below it, depth first; fn returning
// false skips a node's children.
func Walk(n *Node, fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, e := range n.Entries {
		Walk(e.Value, fn)
	}
	for _, it := range n.Items {
		Walk(it, fn)
	}
}

// Number makes a float node, or an integer one when the value is whole:
// HCL2 has one number type, so 1.0 and 1 must be the same value in all
// three formats.
func Number(f float64, p Pos) *Node {
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return &Node{Kind: Int, Pos: p, Text: strconv.FormatInt(int64(f), 10)}
	}
	return &Node{Kind: Float, Pos: p, Text: strconv.FormatFloat(f, 'g', -1, 64)}
}

// Limits bound what a reader accepts.
type Limits struct {
	// MaxBytes is the largest file (default 4 MiB).
	MaxBytes int
	// MaxDepth is the deepest nesting of mappings and sequences (64).
	MaxDepth int
	// MaxNodes is the most nodes a file may expand to, aliases counted
	// at every use (262144): what stops a YAML "billion laughs".
	MaxNodes int
	// MaxString is the longest scalar, also after interpolation (1 MiB).
	MaxString int
}

// DefaultLimits are the bounds used when a field is zero.
var DefaultLimits = Limits{MaxBytes: 4 << 20, MaxDepth: 64, MaxNodes: 1 << 18, MaxString: 1 << 20}

// WithDefaults fills the zero fields.
func (l Limits) WithDefaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultLimits.MaxBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultLimits.MaxDepth
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = DefaultLimits.MaxNodes
	}
	if l.MaxString <= 0 {
		l.MaxString = DefaultLimits.MaxString
	}
	return l
}

// budget counts nodes and checks depth while a reader builds a tree.
type budget struct {
	lim   Limits
	nodes int
	file  string
}

func (b *budget) node(p Pos, depth int) error {
	b.nodes++
	if b.nodes > b.lim.MaxNodes {
		return Errorf(p, "document expands to more than %d nodes (YAML aliases are counted at every use)", b.lim.MaxNodes)
	}
	if depth > b.lim.MaxDepth {
		return Errorf(p, "nesting deeper than %d levels", b.lim.MaxDepth)
	}
	return nil
}

func (b *budget) str(p Pos, s string) error {
	if len(s) > b.lim.MaxString {
		return Errorf(p, "scalar longer than %d bytes", b.lim.MaxString)
	}
	return nil
}

func (b *budget) size(data []byte) error {
	if len(data) > b.lim.MaxBytes {
		return Errorf(Pos{File: b.file}, "file is %d bytes, more than the %d allowed", len(data), b.lim.MaxBytes)
	}
	return nil
}
