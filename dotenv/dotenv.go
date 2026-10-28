// Package dotenv reads env files in the Compose Specification's format
// (container_spec.md, "Env_file format"): `VAR=VAL` lines, `#` comments,
// unquoted and double-quoted values interpolated, single-quoted values
// literal, `\` escapes, an optional `export ` prefix; a line with a bare
// `VAR` takes VAR from the lookup, or leaves it unset.
//
// The quoting and escape rules follow compose-go's dotenv parser, the
// reference implementation the specification points to.
package dotenv

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-wasmcontainers/compose/interp"
)

// MaxBytes is the largest env file read (1 MiB).
const MaxBytes = 1 << 20

// Var is one variable, in file order.
type Var struct {
	Name, Value string
}

// Parse reads src. Values are interpolated against the variables defined
// above them, then lookup (lookup wins, as the project environment does
// over a .env file); raw disables interpolation (`format: raw`).
func Parse(src string, lookup interp.Lookup, raw bool) ([]Var, error) {
	if len(src) > MaxBytes {
		return nil, fmt.Errorf("env file is %d bytes, more than the %d allowed", len(src), MaxBytes)
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	p := &parser{line: 1, lookup: lookup, raw: raw, seen: map[string]int{}}
	p.in = &interp.Interpolator{Lookup: p.find, Budget: 4 * MaxBytes, MaxString: MaxBytes}
	if err := p.parse(src); err != nil {
		return nil, err
	}
	return p.out, nil
}

type parser struct {
	line   int
	lookup interp.Lookup
	raw    bool
	in     *interp.Interpolator
	out    []Var
	seen   map[string]int
}

func (p *parser) find(name string) (string, bool) {
	if v, ok := p.lookup(name); ok {
		return v, true
	}
	if i, ok := p.seen[name]; ok {
		return p.out[i].Value, true
	}
	return "", false
}

func (p *parser) set(name, value string) {
	if i, ok := p.seen[name]; ok {
		p.out[i].Value = value
		return
	}
	p.seen[name] = len(p.out)
	p.out = append(p.out, Var{Name: name, Value: value})
}

// isSpace is a space that is not a line break.
func isSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', '\r', ' ', 0x85, 0xA0:
		return true
	}
	return false
}

func (p *parser) parse(src string) error {
	for {
		src = p.statementStart(src)
		if src == "" {
			return nil
		}
		key, rest, inherited, err := p.key(src)
		if err != nil {
			return err
		}
		if inherited {
			p.line++
			if v, ok := p.lookup(key); ok {
				p.set(key, v)
			}
			src = rest
			continue
		}
		val, rest, err := p.value(rest)
		if err != nil {
			return err
		}
		p.set(key, val)
		src = rest
	}
}

// statementStart skips blank and comment lines.
func (p *parser) statementStart(src string) string {
	for {
		i := strings.IndexFunc(src, func(r rune) bool {
			if r == '\n' {
				p.line++
			}
			return !unicode.IsSpace(r)
		})
		if i < 0 {
			return ""
		}
		src = src[i:]
		if src[0] != '#' {
			return src
		}
		j := strings.IndexByte(src, '\n')
		if j < 0 {
			return ""
		}
		src = src[j:]
	}
}

func (p *parser) key(src string) (key, rest string, inherited bool, err error) {
	if after, ok := strings.CutPrefix(src, "export"); ok && after != "" && isSpace(rune(after[0])) {
		src = strings.TrimLeftFunc(after, isSpace)
	}
	end, off := -1, 0
	for i, r := range src {
		if isSpace(r) {
			continue
		}
		switch r {
		case '=', ':', '\n':
			end, off, inherited = i, i+1, r == '\n'
		case '_', '.', '-', '[', ']':
			continue
		default:
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				continue
			}
			first, _, _ := strings.Cut(src, "\n")
			return "", "", false, fmt.Errorf("line %d: unexpected character %q in variable name %q", p.line, r, first)
		}
		break
	}
	if end < 0 { // the file ends right after a bare name
		end, off, inherited = len(src), len(src), true
	}
	key = strings.TrimRightFunc(src[:end], unicode.IsSpace)
	if key == "" {
		return "", "", false, fmt.Errorf("line %d: key cannot be empty", p.line)
	}
	if strings.ContainsFunc(key, isSpace) {
		return "", "", false, fmt.Errorf("line %d: key cannot contain a space", p.line)
	}
	return key, strings.TrimLeftFunc(src[off:], isSpace), inherited, nil
}

func (p *parser) expand(s string) (string, error) {
	if p.raw {
		return s, nil
	}
	v, err := p.in.Substitute(s)
	if err != nil {
		return "", fmt.Errorf("line %d: %w", p.line, err)
	}
	return v, nil
}

func (p *parser) value(src string) (string, string, error) {
	if src == "" || (src[0] != '"' && src[0] != '\'') {
		v, rest, _ := strings.Cut(src, "\n")
		p.line++
		v, _, _ = strings.Cut(v, " #")
		v = strings.TrimRightFunc(v, unicode.IsSpace)
		v, err := p.expand(v)
		return v, rest, err
	}
	quote := src[0]
	escaped := false
	var chars []byte
	for i := 1; i < len(src); i++ {
		c := src[i]
		if c == '\n' {
			p.line++
		}
		if c != quote {
			if !escaped && c == '\\' {
				escaped = true
				continue
			}
			if escaped {
				escaped = false
				chars = append(chars, '\\')
			}
			chars = append(chars, c)
			continue
		}
		if escaped { // an escaped quote
			escaped = false
			chars = append(chars, c)
			continue
		}
		v := string(chars)
		if quote == '"' {
			var err error
			if v, err = p.expand(unescape(v, p.raw)); err != nil {
				return "", "", err
			}
		}
		return v, src[i+1:], nil
	}
	first, _, _ := strings.Cut(src, "\n")
	return "", "", fmt.Errorf("line %d: unterminated quoted value %s", p.line, first)
}

// unescape expands the shell escapes of a double-quoted value: \a \b \f
// \n \r \t \v \\ \" \$ (which stays a literal `$`) and \0NNN octal.
func unescape(s string, raw bool) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		c := s[i+1]
		switch c {
		case 'a', 'b', 'f', 'n', 'r', 't', 'v', '"', '\\':
			r, _, _, _ := strconv.UnquoteChar(s[i:i+2], '"')
			b.WriteRune(r)
			i++
		case '$':
			if raw {
				b.WriteByte('$')
			} else {
				b.WriteString("$$") // literal $ through interpolation
			}
			i++
		case '0':
			j := i + 2
			for j < len(s) && j < i+5 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			if v, err := strconv.ParseUint(s[i+2:j], 8, 8); err == nil && j > i+2 {
				b.WriteByte(byte(v))
			} else if j == i+2 {
				b.WriteByte(0)
			} else {
				b.WriteString(s[i:j])
			}
			i = j - 1
		default:
			b.WriteByte('\\')
		}
	}
	return b.String()
}
