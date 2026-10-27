package tree

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// HCLOptions are what an HCL2 Compose file may refer to.
type HCLOptions struct {
	// Variables are the values of var.NAME: the project's environment
	// (the explicit environment, then .env). A `variable "NAME"` block's
	// default applies when NAME is not among them.
	Variables map[string]string
}

// The HCL2 mapping of the Compose model (docs/HCL.md is its
// specification). Labelled blocks name map entries; some unlabelled blocks
// repeat to make a sequence; every other block is a mapping.

// labelled maps a block type to the key of the mapping its label indexes,
// per context ("" is the top level, "service" a service body).
var labelled = map[string]map[string]string{
	"": {
		"service": "services",
		"network": "networks",
		"volume":  "volumes",
		"secret":  "secrets",
		"config":  "configs",
		"model":   "models",
	},
	"service": {
		"depends_on": "depends_on",
		"network":    "networks",
	},
}

// repeated maps an unlabelled block type to the sequence it appends to.
var repeated = map[string]map[string]string{
	"": {"include": "include"},
	"service": {
		"volume":   "volumes",
		"env_file": "env_file",
		"port":     "ports",
		"secret":   "secrets",
		"config":   "configs",
	},
}

// Operator tokens and postfix index chains build expression trees deeper
// than their brackets: they are counted, so evaluation stays shallow.
const maxHCLOperators = 256

// maxTemplateOutput bounds what template interpolations may produce.
const maxTemplateOutput = 64 << 20

// ParseHCL reads an HCL2 native-syntax Compose file into a tree.
//
// Expressions are evaluated in a static context: the only variable is
// `var` (HCLOptions.Variables), there are no functions, and `for`
// expressions and directives are refused (they multiply sizes). A quoted
// string is a template: a literal "${" is written "$${". Values are
// final: Compose's own `${VAR}` interpolation does not apply to HCL files.
func ParseHCL(file string, data []byte, lim Limits, opt HCLOptions) (*Node, error) {
	b := &budget{lim: lim.WithDefaults(), file: file}
	if err := b.size(data); err != nil {
		return nil, err
	}
	interps, err := hclPrescan(file, data, b.lim.MaxDepth)
	if err != nil {
		return nil, err
	}
	f, diags := hclsyntax.ParseConfig(data, file, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diagError(file, diags)
	}
	body := f.Body.(*hclsyntax.Body)
	if d := hclsyntax.VisitAll(body, func(n hclsyntax.Node) hcl.Diagnostics {
		if fe, ok := n.(*hclsyntax.ForExpr); ok {
			r := fe.SrcRange
			return hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "for expressions and %{for} directives are not allowed in a Compose file", Subject: &r}}
		}
		return nil
	}); d.HasErrors() {
		return nil, diagError(file, d)
	}
	h := &hconv{b: b}
	vars, err := h.variables(body, opt.Variables)
	if err != nil {
		return nil, err
	}
	// Templates copy variables: N interpolations of a value of L bytes
	// may produce N×L bytes. Bound the product, not each string.
	longest := 0
	for _, v := range vars {
		if v.Type() == cty.String && v.IsKnown() && !v.IsNull() && len(v.AsString()) > longest {
			longest = len(v.AsString())
		}
	}
	if interps > 0 && int64(interps)*int64(longest) > maxTemplateOutput { // int64: no overflow on 32-bit
		return nil, Errorf(Pos{File: file}, "%d template interpolations of variables up to %d bytes could produce more than %d bytes", interps, longest, maxTemplateOutput)
	}
	h.ctx = &hcl.EvalContext{Variables: map[string]cty.Value{"var": cty.ObjectVal(vars)}}
	return h.body(body, "", 0, hpos(file, body.SrcRange))
}

// hclPrescan bounds what makes the parser or the evaluator recurse:
// bracket nesting, operators and postfix index chains.
func hclPrescan(file string, data []byte, maxDepth int) (interps int, err error) {
	toks, diags := hclsyntax.LexConfig(data, file, hcl.InitialPos)
	if diags.HasErrors() {
		return 0, diagError(file, diags)
	}
	depth, ops := 0, 0
	var prev hclsyntax.TokenType
	for _, t := range toks {
		switch t.Type {
		case hclsyntax.TokenOBrace, hclsyntax.TokenOBrack, hclsyntax.TokenOParen,
			hclsyntax.TokenOQuote, hclsyntax.TokenOHeredoc, hclsyntax.TokenTemplateInterp,
			hclsyntax.TokenTemplateControl:
			depth++
			if depth > maxDepth {
				return 0, Errorf(hpos(file, t.Range), "nesting deeper than %d levels", maxDepth)
			}
			if t.Type == hclsyntax.TokenTemplateInterp {
				interps++
			}
			if t.Type == hclsyntax.TokenOBrack {
				switch prev {
				case hclsyntax.TokenCBrack, hclsyntax.TokenCParen, hclsyntax.TokenCBrace,
					hclsyntax.TokenCQuote, hclsyntax.TokenIdent, hclsyntax.TokenNumberLit:
					ops++
				}
			}
		case hclsyntax.TokenCBrace, hclsyntax.TokenCBrack, hclsyntax.TokenCParen,
			hclsyntax.TokenCQuote, hclsyntax.TokenCHeredoc, hclsyntax.TokenTemplateSeqEnd:
			depth--
		case hclsyntax.TokenStar, hclsyntax.TokenSlash, hclsyntax.TokenPlus, hclsyntax.TokenMinus,
			hclsyntax.TokenPercent, hclsyntax.TokenEqualOp, hclsyntax.TokenNotEqual,
			hclsyntax.TokenLessThan, hclsyntax.TokenLessThanEq, hclsyntax.TokenGreaterThan,
			hclsyntax.TokenGreaterThanEq, hclsyntax.TokenAnd, hclsyntax.TokenOr,
			hclsyntax.TokenBang, hclsyntax.TokenQuestion, hclsyntax.TokenEllipsis:
			ops++
		}
		if ops > maxHCLOperators {
			return 0, Errorf(hpos(file, t.Range), "more than %d operators or index chains in the file", maxHCLOperators)
		}
		if t.Type != hclsyntax.TokenNewline && t.Type != hclsyntax.TokenComment {
			prev = t.Type
		}
	}
	return interps, nil
}

func hpos(file string, r hcl.Range) Pos {
	if r.Filename != "" {
		file = r.Filename
	}
	return Pos{File: file, Line: r.Start.Line, Col: r.Start.Column}
}

func diagError(file string, diags hcl.Diagnostics) error {
	for _, d := range diags {
		if d.Severity != hcl.DiagError {
			continue
		}
		p := Pos{File: file}
		if d.Subject != nil {
			p = hpos(file, *d.Subject)
		}
		msg := d.Summary
		if d.Detail != "" {
			msg += ": " + d.Detail
		}
		return &Error{Pos: p, Msg: msg}
	}
	return Errorf(Pos{File: file}, "invalid HCL")
}

type hconv struct {
	b   *budget
	ctx *hcl.EvalContext
}

// variables evaluates the `variable` blocks' defaults (in an empty
// context: a default is a constant) and lays the environment over them.
func (h *hconv) variables(body *hclsyntax.Body, env map[string]string) (map[string]cty.Value, error) {
	vars := map[string]cty.Value{}
	seen := map[string]Pos{}
	for _, blk := range body.Blocks {
		if blk.Type != "variable" {
			continue
		}
		p := hpos(h.b.file, blk.TypeRange)
		if len(blk.Labels) != 1 {
			return nil, Errorf(p, `a variable block takes one label: variable "NAME" { default = ... }`)
		}
		name := blk.Labels[0]
		if !hclsyntax.ValidIdentifier(name) {
			return nil, Errorf(hpos(h.b.file, blk.LabelRanges[0]), "%q is not a valid variable name", name)
		}
		if first, dup := seen[name]; dup {
			return nil, Errorf(p, "variable %q declared twice (first at %s)", name, first)
		}
		seen[name] = p
		if len(blk.Body.Blocks) > 0 {
			return nil, Errorf(hpos(h.b.file, blk.Body.Blocks[0].TypeRange), "a variable block holds only attributes")
		}
		for an, a := range blk.Body.Attributes {
			switch an {
			case "default":
				v, d := a.Expr.Value(nil)
				if d.HasErrors() {
					return nil, diagError(h.b.file, d)
				}
				vars[name] = v
			case "description":
			default:
				return nil, Errorf(hpos(h.b.file, a.NameRange), "unknown variable attribute %q (default, description)", an)
			}
		}
	}
	for k, v := range env {
		vars[k] = cty.StringVal(v)
	}
	return vars, nil
}

type item struct {
	start hcl.Pos
	attr  *hclsyntax.Attribute
	block *hclsyntax.Block
}

// body converts attributes and blocks, in source order.
func (h *hconv) body(body *hclsyntax.Body, ctx string, depth int, p Pos) (*Node, error) {
	if err := h.b.node(p, depth); err != nil {
		return nil, err
	}
	var items []item
	for _, a := range body.Attributes {
		items = append(items, item{start: a.SrcRange.Start, attr: a})
	}
	for _, blk := range body.Blocks {
		if ctx == "" && blk.Type == "variable" {
			continue
		}
		items = append(items, item{start: blk.TypeRange.Start, block: blk})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].start.Byte < items[j].start.Byte })

	out := &Node{Kind: Map, Pos: p}
	fromBlocks := map[string]bool{}
	for _, it := range items {
		if a := it.attr; a != nil {
			kp := hpos(h.b.file, a.NameRange)
			if out.Has(a.Name) {
				return nil, Errorf(kp, "%q is set both as an attribute and with blocks", a.Name)
			}
			v, d := a.Expr.Value(h.ctx)
			if d.HasErrors() {
				return nil, diagError(h.b.file, d)
			}
			n, err := h.value(v, hpos(h.b.file, a.Expr.Range()), depth+1)
			if err != nil {
				return nil, err
			}
			out.Entries = append(out.Entries, Entry{Key: a.Name, KeyPos: kp, Value: n})
			continue
		}
		blk := it.block
		kp := hpos(h.b.file, blk.TypeRange)
		child := childContext(ctx, blk)
		switch {
		case len(blk.Labels) > 1:
			return nil, Errorf(kp, "block %q takes at most one label", blk.Type)
		case len(blk.Labels) == 1:
			key := blk.Type
			if k, ok := labelled[ctx][blk.Type]; ok {
				key = k
			}
			m := out.Get(key)
			if m == nil {
				if out.Has(key) {
					return nil, Errorf(kp, "%q is set both as an attribute and with blocks", key)
				}
				m = &Node{Kind: Map, Pos: kp}
				out.Entries = append(out.Entries, Entry{Key: key, KeyPos: kp, Value: m})
				fromBlocks[key] = true
			} else if !fromBlocks[key] || m.Kind != Map {
				return nil, Errorf(kp, "%q is set both as an attribute and with blocks", key)
			}
			label := blk.Labels[0]
			lp := hpos(h.b.file, blk.LabelRanges[0])
			if i := m.Index(label); i >= 0 {
				return nil, Errorf(lp, "%s %q is defined twice (first at %s)", blk.Type, label, m.Entries[i].KeyPos)
			}
			v, err := h.body(blk.Body, child, depth+2, kp)
			if err != nil {
				return nil, err
			}
			m.Entries = append(m.Entries, Entry{Key: label, KeyPos: lp, Value: v})
		default:
			v, err := h.body(blk.Body, child, depth+1, kp)
			if err != nil {
				return nil, err
			}
			if key, ok := repeated[ctx][blk.Type]; ok {
				s := out.Get(key)
				if s == nil {
					if out.Has(key) {
						return nil, Errorf(kp, "%q is set both as an attribute and with blocks", key)
					}
					s = &Node{Kind: Seq, Pos: kp}
					out.Entries = append(out.Entries, Entry{Key: key, KeyPos: kp, Value: s})
					fromBlocks[key] = true
				} else if !fromBlocks[key] || s.Kind != Seq {
					return nil, Errorf(kp, "%q is set both as an attribute and with blocks", key)
				}
				s.Items = append(s.Items, v)
				continue
			}
			if i := out.Index(blk.Type); i >= 0 {
				return nil, Errorf(kp, "block %q appears twice (first at %s)", blk.Type, out.Entries[i].KeyPos)
			}
			out.Entries = append(out.Entries, Entry{Key: blk.Type, KeyPos: kp, Value: v})
		}
	}
	return out, nil
}

func childContext(ctx string, blk *hclsyntax.Block) string {
	if ctx == "" && blk.Type == "service" {
		return "service"
	}
	return "other"
}

// value converts an evaluated expression.
func (h *hconv) value(v cty.Value, p Pos, depth int) (*Node, error) {
	if err := h.b.node(p, depth); err != nil {
		return nil, err
	}
	if v.IsNull() {
		return &Node{Kind: Null, Pos: p}, nil
	}
	if !v.IsWhollyKnown() {
		return nil, Errorf(p, "value is not known")
	}
	v, _ = v.Unmark()
	ty := v.Type()
	switch {
	case ty == cty.String:
		s := v.AsString()
		if err := h.b.str(p, s); err != nil {
			return nil, err
		}
		return &Node{Kind: String, Pos: p, Text: s}, nil
	case ty == cty.Bool:
		return &Node{Kind: Bool, Pos: p, Text: fmt.Sprint(v.True())}, nil
	case ty == cty.Number:
		bf := v.AsBigFloat()
		if bf.IsInf() {
			return nil, Errorf(p, "infinities are not Compose values")
		}
		if bf.IsInt() {
			if i, acc := bf.Int64(); acc == big.Exact {
				return &Node{Kind: Int, Pos: p, Text: fmt.Sprint(i)}, nil
			}
		}
		f, _ := bf.Float64()
		return Number(f, p), nil
	case ty.IsObjectType() || ty.IsMapType():
		out := &Node{Kind: Map, Pos: p}
		for it := v.ElementIterator(); it.Next(); {
			k, ev := it.Element()
			n, err := h.value(ev, p, depth+1)
			if err != nil {
				return nil, err
			}
			out.Entries = append(out.Entries, Entry{Key: k.AsString(), KeyPos: p, Value: n})
		}
		return out, nil
	case ty.IsTupleType() || ty.IsListType() || ty.IsSetType():
		out := &Node{Kind: Seq, Pos: p, Items: []*Node{}}
		for it := v.ElementIterator(); it.Next(); {
			_, ev := it.Element()
			n, err := h.value(ev, p, depth+1)
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, n)
		}
		return out, nil
	}
	return nil, Errorf(p, "unsupported value of type %s", ty.FriendlyName())
}
