package interp

import (
	"errors"
	"strings"
	"testing"
)

var env = map[string]string{"SET": "v", "EMPTY": "", "FOO": "foo", "Mixed_9": "m"}

func lookup(k string) (string, bool) { v, ok := env[k]; return v, ok }

// The cases of 12-interpolation.md, and the reference implementation's
// behaviours where the specification is silent.
func TestSubstitute(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                       "plain",
		"$SET":                        "v",
		"${SET}":                      "v",
		"a${SET}b$SET.c":              "avbv.c",
		"$Mixed_9":                    "m",
		"${UNSET:-default}":           "default",
		"${EMPTY:-default}":           "default",
		"${SET:-default}":             "v",
		"${UNSET-default}":            "default",
		"${EMPTY-default}":            "",
		"${UNSET:-${FOO}}":            "foo",
		"${UNSET:-${NOPE:-deep}}":     "deep",
		"${SET:+repl}":                "repl",
		"${EMPTY:+repl}":              "",
		"${EMPTY+repl}":               "repl",
		"${UNSET+repl}":               "",
		"$$VAR_NOT_INTERPOLATED":      "$VAR_NOT_INTERPOLATED",
		"$$${SET}":                    "$v",
		"cost: 5$":                    "cost: 5$",
		"$1 $- $ ":                    "$1 $- $ ",
		"${UNSET:-a{b}c}":             "a{b}c",
		"${SET?unused}":               "v",
		"${UNSET:-}":                  "",
		"${UNSET:-$SET and ${FOO}}x":  "v and foox",
		"multi ${SET} and ${FOO} end": "multi v and foo end",
	} {
		in2 := &Interpolator{Lookup: lookup}
		got, err := in2.Substitute(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestErrors(t *testing.T) {
	for in, want := range map[string]string{
		"${UNSET:?is required}": "required variable UNSET is missing a value: is required",
		"${EMPTY:?empty}":       "required variable EMPTY is missing a value: empty",
		"${UNSET?}":             "required variable UNSET is missing a value",
		"${UNSET?need $FOO}":    "need foo",
		"${":                    "missing closing brace",
		"${}":                   "invalid template",
		"${1A}":                 "invalid template",
		"${A B}":                "invalid template",
		"${A:}":                 "invalid template",
		"${A/x/y}":              "invalid template",
	} {
		in2 := &Interpolator{Lookup: lookup}
		_, err := in2.Substitute(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", in, err, want)
		}
	}
	var me *MissingError
	in2 := &Interpolator{Lookup: lookup}
	if _, err := in2.Substitute("${X:?}"); !errors.As(err, &me) || me.Name != "X" {
		t.Fatal(err)
	}
}

func TestUnsetAndBounds(t *testing.T) {
	var unset []string
	in := &Interpolator{Lookup: lookup, Unset: func(n string) { unset = append(unset, n) }}
	if got, _ := in.Substitute("$A${B}${C:-x}"); got != "x" || strings.Join(unset, ",") != "A,B" {
		t.Fatalf("%q %v", got, unset)
	}
	deep := strings.Repeat("${U:-", 40) + "x" + strings.Repeat("}", 40)
	if _, err := (&Interpolator{Lookup: lookup}).Substitute(deep); err == nil || !strings.Contains(err.Error(), "nested deeper") {
		t.Fatal(err)
	}
	big := map[string]string{"B": strings.Repeat("x", 1000)}
	bl := func(k string) (string, bool) { v, ok := big[k]; return v, ok }
	in = &Interpolator{Lookup: bl, MaxString: 1500}
	if _, err := in.Substitute("$B$B"); !errors.Is(err, ErrTooLarge) {
		t.Fatal("one string over MaxString", err)
	}
	in = &Interpolator{Lookup: bl, Budget: 2500}
	for i := 0; i < 2; i++ {
		if _, err := in.Substitute("$B"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := in.Substitute("$B"); !errors.Is(err, ErrTooLarge) {
		t.Fatal("the budget is shared by every call", err)
	}
}

func FuzzSubstitute(f *testing.F) {
	for _, s := range []string{"${A:-${B:?x}}", "$$A", "${", "a$b${c}", "${A+x}", "${A:-{}}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		in := &Interpolator{Lookup: lookup, MaxString: 1 << 16, Budget: 1 << 20}
		out, err := in.Substitute(s)
		if err == nil && len(out) > 1<<16 {
			t.Fatalf("result of %d bytes", len(out))
		}
		// Without '$' the input comes back unchanged.
		if !strings.Contains(s, "$") && (err != nil || out != s) {
			t.Fatalf("%q -> %q, %v", s, out, err)
		}
	})
}
