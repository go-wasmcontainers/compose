package dotenv

import (
	"strings"
	"testing"
)

// The rules of container_spec.md, "Env_file format", one per line.
func TestSpecExamples(t *testing.T) {
	src := `# a comment
RACK_ENV=development

VAR="quoted"
SINGLE='VAL'
INLINE=VAL # comment
NOCOMMENT=VAL# not a comment
QCOMMENT="VAL # not a comment"
QAFTER="VAL" # comment
LIT='$OTHER'
LITB='${OTHER}'
ESCQ='Let\'s go!'
JSON="{\"hello\": \"json\"}"
TAB="some\tvalue"
TABS='some\tvalue'
TABU=some\tvalue
EMPTY=
export EXPORTED=yes
INHERIT
INTERP=${RACK_ENV}-x
DOLLAR="\$HOME"
YAMLISH: colon
MULTI="line1
line2"
`
	vars, err := Parse(src, func(k string) (string, bool) {
		if k == "INHERIT" {
			return "from-env", true
		}
		return "", false
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range vars {
		got[v.Name] = v.Value
	}
	for k, want := range map[string]string{
		"RACK_ENV": "development", "VAR": "quoted", "SINGLE": "VAL", "INLINE": "VAL",
		"NOCOMMENT": "VAL# not a comment", "QCOMMENT": "VAL # not a comment", "QAFTER": "VAL",
		"LIT": "$OTHER", "LITB": "${OTHER}", "ESCQ": "Let's go!", "JSON": `{"hello": "json"}`,
		"TAB": "some\tvalue", "TABS": `some\tvalue`, "TABU": `some\tvalue`, "EMPTY": "",
		"EXPORTED": "yes", "INHERIT": "from-env", "INTERP": "development-x", "DOLLAR": "$HOME",
		"YAMLISH": "colon", "MULTI": "line1\nline2",
	} {
		if v, ok := got[k]; !ok || v != want {
			t.Errorf("%s = %q (set %v), want %q", k, v, ok, want)
		}
	}
	if vars[0].Name != "RACK_ENV" {
		t.Error("file order is kept")
	}
	raw, err := Parse(`R="${X} \$y"`, nil, true)
	if err != nil || raw[0].Value != `${X} $y` {
		t.Fatal(raw, err)
	}
	unset, err := Parse("NOT_IN_ENV\n", nil, false)
	if err != nil || len(unset) != 0 {
		t.Fatal("a bare name without a value is left unset", unset, err)
	}
}

func TestErrors(t *testing.T) {
	for src, want := range map[string]string{
		"A B=1":                         "line 1",
		"=x":                            "key cannot be empty",
		"A=\"open":                      "unterminated quoted value",
		"A=${X:?needed}":                "required variable X",
		"ok=1\nb@d=2":                   `line 2: unexpected character '@'`,
		strings.Repeat("A", MaxBytes+1): "more than",
	} {
		_, err := Parse(src, nil, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src[:min(len(src), 20)], err, want)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"A=1\n", "A='x'\nB=\"y\\n\"", "export A=${B:-c}", "A\n", "A=\"\\0101\""} {
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, src string, raw bool) {
		vars, err := Parse(src, nil, raw)
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, v := range vars {
			if v.Name == "" || seen[v.Name] {
				t.Fatalf("bad or repeated name %q", v.Name)
			}
			seen[v.Name] = true
		}
	})
}
