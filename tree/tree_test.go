package tree

import (
	"fmt"
	"strings"
	"testing"
)

func mustErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want one containing %q", err, want)
	}
}

// billion builds the "billion laughs" document: n levels of k aliases.
func billion(n, k int) string {
	var b strings.Builder
	b.WriteString("a00: &a00 [lol]\n")
	for i := 1; i <= n; i++ {
		b.WriteString("a")
		b.WriteString(itoa(i))
		b.WriteString(": &a")
		b.WriteString(itoa(i))
		b.WriteString(" [")
		for j := 0; j < k; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("*a")
			b.WriteString(itoa(i - 1))
		}
		b.WriteString("]\n")
	}
	return b.String()
}

func itoa(i int) string { return fmt.Sprintf("%02d", i) }

func TestYAMLBounds(t *testing.T) {
	_, err := ParseYAML("bomb.yaml", []byte(billion(9, 9)), Limits{})
	mustErr(t, err, "more than 262144 nodes")
	// The same shape, small, is fine: aliases are expanded.
	n, err := ParseYAML("ok.yaml", []byte(billion(2, 3)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(n.Get("a02").Items); got != 3 || len(n.Get("a02").Items[0].Items) != 3 {
		t.Fatalf("aliases not expanded: %d", got)
	}
	deep := strings.Repeat("[", 100) + strings.Repeat("]", 100)
	_, err = ParseYAML("deep.yaml", []byte("a: "+deep), Limits{})
	mustErr(t, err, "nesting deeper than 64")
	_, err = ParseYAML("big.yaml", make([]byte, 5<<20), Limits{})
	mustErr(t, err, "more than the 4194304 allowed")
	_, err = ParseYAML("long.yaml", []byte("a: "+strings.Repeat("x", 2<<20)), Limits{})
	mustErr(t, err, "longer than")
}

func TestYAMLSemantics(t *testing.T) {
	src := `
base: &b {x: 1, y: 2}
other: &o {y: 20, z: 30}
m:
  <<: [*b, *o]
  x: own
quoted: "true"
plain: true
oct: 0o17
hex: 0x1F
nul: ~
reset: !reset []
over: !override {a: 1}
`
	n, err := ParseYAML("s.yaml", []byte(src), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	m := n.Get("m")
	if m.Get("x").Text != "own" || m.Get("y").Text != "2" || m.Get("z").Text != "30" {
		t.Fatalf("merge keys: own key wins, then earlier sources: %+v", m.Entries)
	}
	if n.Get("quoted").Kind != String || n.Get("plain").Kind != Bool {
		t.Fatal("quoted true is a string, plain true a boolean")
	}
	if n.Get("oct").Text != "15" || n.Get("hex").Text != "31" || n.Get("nul").Kind != Null {
		t.Fatal(n.Get("oct").Text, n.Get("hex").Text)
	}
	if n.Get("reset").Tag != Reset || n.Get("over").Tag != Override {
		t.Fatal("tags")
	}
	if p := m.Get("x").Pos; p.Line != 6 || p.Col != 6 || p.File != "s.yaml" {
		t.Fatalf("position %v", p)
	}
	_, err = ParseYAML("d.yaml", []byte("a: 1\nb: 2\na: 3\n"), Limits{})
	mustErr(t, err, `d.yaml:3:1: duplicate key "a" (first at d.yaml:1:1)`)
	_, err = ParseYAML("t.yaml", []byte("a: !custom x\n"), Limits{})
	mustErr(t, err, "unsupported YAML tag !custom")
	_, err = ParseYAML("m.yaml", []byte("a: 1\n---\nb: 2\n"), Limits{})
	mustErr(t, err, "more than one YAML document")
	_, err = ParseYAML("e.yaml", []byte("# nothing\n"), Limits{})
	mustErr(t, err, "empty file")
	_, err = ParseYAML("x.yaml", []byte("a: [1\n"), Limits{})
	mustErr(t, err, "x.yaml:")
	_, err = ParseYAML("mk.yaml", []byte("a:\n  <<: [1]\n"), Limits{})
	mustErr(t, err, "merge key")
}

func TestJSON(t *testing.T) {
	n, err := ParseJSON("c.json", []byte("{\n  \"a\": [1, 2.5, \"x\", true, null],\n  \"b\": {}\n}"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	a := n.Get("a")
	if a.Items[0].Kind != Int || a.Items[1].Kind != Float || a.Items[2].Kind != String || a.Items[3].Kind != Bool || a.Items[4].Kind != Null {
		t.Fatal(a.Items)
	}
	if p := a.Pos; p.Line != 2 || p.Col != 8 {
		t.Fatalf("pos %v", p)
	}
	if p := n.Entries[1].KeyPos; p.Line != 3 || p.Col != 3 {
		t.Fatalf("key pos %v", p)
	}
	for src, want := range map[string]string{
		`{"a":1,"a":2}`:               `duplicate key "a"`,
		`{"a":1} x`:                   "invalid character",
		`{"a":1} {}`:                  "after the JSON document",
		`{"a":`:                       "c.json",
		"":                            "empty file",
		strings.Repeat("[", 100):      "nesting deeper",
		"\"\xff\"":                    "UTF-8",
		`{"n": 1e999}`:                "out of range",
		strings.Repeat(" ", 10) + "@": "c.json:1:11",
	} {
		_, err := ParseJSON("c.json", []byte(src), Limits{})
		mustErr(t, err, want)
	}
}

func TestHCL(t *testing.T) {
	src := `
name = "p"
variable "TAG" { default = "latest" }
x-ext = { a = 1 }
service "web" {
  image = "img:${var.TAG}"
  literal = "$${HOME} and $USER"
  network "front" { aliases = ["w"] }
  volume { source = "a" }
  volume { source = "b" }
  depends_on { db = { condition = "service_healthy" } }
  replicas = 1 + 1
}
`
	n, err := ParseHCL("c.hcl", []byte(src), Limits{}, HCLOptions{})
	if err != nil {
		t.Fatal(err)
	}
	web := n.Get("services").Get("web")
	if web.Get("image").Text != "img:latest" || web.Get("literal").Text != "${HOME} and $USER" {
		t.Fatal(web.Get("image").Text, web.Get("literal").Text)
	}
	if len(web.Get("volumes").Items) != 2 || web.Get("networks").Get("front").Get("aliases").Items[0].Text != "w" {
		t.Fatal(web.Entries)
	}
	if web.Get("depends_on").Get("db").Get("condition").Text != "service_healthy" || web.Get("replicas").Text != "2" {
		t.Fatal(web.Get("depends_on"))
	}
	if n.Has("variable") {
		t.Fatal("variable blocks are not part of the model")
	}
	n, err = ParseHCL("c.hcl", []byte(src), Limits{}, HCLOptions{Variables: map[string]string{"TAG": "v2"}})
	if err != nil || n.Get("services").Get("web").Get("image").Text != "img:v2" {
		t.Fatal(err)
	}
	for s, want := range map[string]string{
		`a = var.NOPE`:                                             "c.hcl:1:8: Unsupported attribute",
		`a = upper("x")`:                                           "Function calls not allowed",
		`a = [for x in [1, 2]: x]`:                                 "for expressions",
		`a = "%{for x in [1]}${x}%{endfor}"`:                       "for expressions",
		`a = 1` + "\n" + `a = 2`:                                   "Attribute redefined",
		`service "a" "b" {}`:                                       "at most one label",
		`service "a" {}` + "\n" + `service "a" {}`:                 `service "a" is defined twice`,
		`services = {}` + "\n" + `service "a" {}`:                  "both as an attribute and with blocks",
		`a = ` + strings.Repeat("[", 80) + strings.Repeat("]", 80): "nesting deeper than 64",
		`a = ` + strings.Repeat("!", 300) + "true":                 "operators",
		`a = 1` + strings.Repeat(" + 1", 300):                      "operators",
		`variable "X" { default = var.Y }`:                         "Variables not allowed",
		`variable "X" { bogus = 1 }`:                               "unknown variable attribute",
	} {
		_, err := ParseHCL("c.hcl", []byte(s), Limits{}, HCLOptions{})
		mustErr(t, err, want)
	}
	// Interpolations times the longest variable are bounded.
	big := strings.Repeat("x", 1<<20)
	_, err = ParseHCL("c.hcl", []byte("a = \""+strings.Repeat("${var.B}", 100)+"\""), Limits{}, HCLOptions{Variables: map[string]string{"B": big}})
	mustErr(t, err, "could produce more than")
}

func TestNodeHelpers(t *testing.T) {
	n := NewMap(Pos{})
	n.Set("a", NewString("1", Pos{}))
	n.Set("a", NewString("2", Pos{}))
	if len(n.Entries) != 1 || n.Get("a").Text != "2" {
		t.Fatal(n.Entries)
	}
	c := n.Clone()
	c.Get("a").Text = "3"
	if n.Get("a").Text != "2" {
		t.Fatal("clone shares")
	}
	n.Delete("a")
	if n.Has("a") {
		t.Fatal("delete")
	}
	if v, err := NewString("42", Pos{}).AsInt(); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if _, err := NewString("x", Pos{}).AsInt(); err == nil {
		t.Fatal("x is no integer")
	}
	if b, err := NewString("yes", Pos{}).AsBool(); err != nil || !b {
		t.Fatal(b, err)
	}
	if _, err := NewMap(Pos{File: "f", Line: 1, Col: 2}).AsString(); err == nil || err.Error() != "f:1:2: expected a string, found a mapping" {
		t.Fatal(err)
	}
	for k := Null; k <= Seq+1; k++ {
		if k.String() == "" {
			t.Fatal(k)
		}
	}
	if (Pos{}).String() != "<input>" || (Pos{File: "f"}).String() != "f" {
		t.Fatal("pos strings")
	}
}
