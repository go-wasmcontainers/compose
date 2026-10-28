// Package interp implements the Compose Specification's variable
// interpolation: `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR-default}`,
// `${VAR:?error}`, `${VAR?error}`, and the reference implementation's
// `${VAR:+replacement}` and `${VAR+replacement}`; `$$` is a literal `$`.
// Defaults and messages are interpolated in turn (nesting).
//
// Nothing is ever executed: a value is looked up in a map, never in a
// shell. Output and nesting are bounded (the input is untrusted).
package interp

import (
	"errors"
	"fmt"
	"strings"
)

// Lookup returns a variable's value and whether it is set.
type Lookup func(name string) (string, bool)

// MissingError is a `${VAR:?msg}` / `${VAR?msg}` whose variable is unset
// (or empty, for `:?`).
type MissingError struct {
	Name, Message string
}

func (e *MissingError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("required variable %s is missing a value: %s", e.Name, e.Message)
	}
	return fmt.Sprintf("required variable %s is missing a value", e.Name)
}

// ErrTooLarge is returned when a result outgrows its budget.
var ErrTooLarge = errors.New("interpolation result too large")

// MaxNesting bounds `${A:-${B:-...}}` nesting.
const MaxNesting = 32

// Interpolator substitutes variables, within a byte budget shared by every
// call (a file's values together, not each one alone).
type Interpolator struct {
	Lookup Lookup
	// Budget is how many bytes all results may total (0: 64 MiB); MaxString
	// bounds one result (0: 1 MiB).
	Budget, MaxString int
	// Unset, when not nil, is told of each variable used without a value
	// and without a default (it then reads as "").
	Unset func(name string)
	used  int
}

// Substitute interpolates s.
func (in *Interpolator) Substitute(s string) (string, error) {
	if strings.IndexByte(s, '$') < 0 {
		return s, nil
	}
	if in.Budget == 0 {
		in.Budget = 64 << 20
	}
	if in.MaxString == 0 {
		in.MaxString = 1 << 20
	}
	var b strings.Builder
	if err := in.run(&b, s, 0); err != nil {
		return "", err
	}
	in.used += b.Len()
	if in.used > in.Budget {
		return "", ErrTooLarge
	}
	return b.String(), nil
}

func isNameStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isName(c byte) bool { return isNameStart(c) || c >= '0' && c <= '9' }

func (in *Interpolator) write(b *strings.Builder, s string) error {
	if b.Len()+len(s) > in.MaxString || in.used+b.Len()+len(s) > in.Budget {
		return ErrTooLarge
	}
	b.WriteString(s)
	return nil
}

// closing returns the index of the brace closing the `${` at s[0:2],
// counting every `{` (as the reference implementation does), or -1.
func closing(s string) int {
	open := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			open++
		case '}':
			open--
			if open == 0 {
				return i
			}
		}
	}
	return -1
}

func (in *Interpolator) run(b *strings.Builder, s string, depth int) error {
	if depth > MaxNesting {
		return fmt.Errorf("variables nested deeper than %d levels", MaxNesting)
	}
	for len(s) > 0 {
		i := strings.IndexByte(s, '$')
		if i < 0 {
			return in.write(b, s)
		}
		if err := in.write(b, s[:i]); err != nil {
			return err
		}
		s = s[i:]
		switch {
		case len(s) >= 2 && s[1] == '$':
			if err := in.write(b, "$"); err != nil {
				return err
			}
			s = s[2:]
		case len(s) >= 2 && isNameStart(s[1]):
			j := 2
			for j < len(s) && isName(s[j]) {
				j++
			}
			if err := in.write(b, in.value(s[1:j])); err != nil {
				return err
			}
			s = s[j:]
		case len(s) >= 2 && s[1] == '{':
			end := closing(s[1:])
			if end < 0 {
				return fmt.Errorf("invalid template %q: missing closing brace", s)
			}
			if err := in.braced(b, s[2:end+1], depth); err != nil {
				return err
			}
			s = s[end+2:]
		default:
			// A `$` that starts no variable is kept as it is.
			if err := in.write(b, "$"); err != nil {
				return err
			}
			s = s[1:]
		}
	}
	return nil
}

func (in *Interpolator) value(name string) string {
	v, ok := in.Lookup(name)
	if !ok && in.Unset != nil {
		in.Unset(name)
	}
	return v
}

// sub interpolates a default or a message into a string of its own.
func (in *Interpolator) sub(s string, depth int) (string, error) {
	var b strings.Builder
	if err := in.run(&b, s, depth+1); err != nil {
		return "", err
	}
	return b.String(), nil
}

// braced handles the inside of `${...}`.
func (in *Interpolator) braced(b *strings.Builder, body string, depth int) error {
	if body == "" || !isNameStart(body[0]) {
		return fmt.Errorf("invalid template \"${%s}\"", body)
	}
	j := 1
	for j < len(body) && isName(body[j]) {
		j++
	}
	name, rest := body[:j], body[j:]
	if rest == "" {
		return in.write(b, in.value(name))
	}
	colon := strings.HasPrefix(rest, ":")
	if colon {
		rest = rest[1:]
	}
	if rest == "" {
		return fmt.Errorf("invalid template \"${%s}\"", body)
	}
	op, arg := rest[0], rest[1:]
	v, set := in.Lookup(name)
	present := set && (!colon || v != "")
	switch op {
	case '-':
		if present {
			return in.write(b, v)
		}
		d, err := in.sub(arg, depth)
		if err != nil {
			return err
		}
		return in.write(b, d)
	case '+':
		if !present {
			return in.write(b, v)
		}
		d, err := in.sub(arg, depth)
		if err != nil {
			return err
		}
		return in.write(b, d)
	case '?':
		if present {
			return in.write(b, v)
		}
		msg, err := in.sub(arg, depth)
		if err != nil {
			return err
		}
		return &MissingError{Name: name, Message: msg}
	}
	return fmt.Errorf("invalid template \"${%s}\"", body)
}
