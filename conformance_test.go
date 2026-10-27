package compose

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-wasmcontainers/compose/schema"
	"github.com/go-wasmcontainers/compose/tree"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type example struct {
	line int
	src  string
}

// specExamples are the Compose files of the specification's text
// (testdata/compose-spec/spec.md, compose-spec/compose-spec at
// 3a4a6995e4a9, Apache-2.0): every ```yml block with a top-level
// services key.
func specExamples(t *testing.T) []example {
	data, err := os.ReadFile("testdata/compose-spec/spec.md")
	if err != nil {
		t.Fatal(err)
	}
	var out []example
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line, start := 0, 0
	var cur *strings.Builder
	for sc.Scan() {
		line++
		l := sc.Text()
		switch {
		case cur == nil && (strings.HasPrefix(l, "```yml") || strings.HasPrefix(l, "```yaml")):
			cur, start = &strings.Builder{}, line+1
		case cur != nil && strings.HasPrefix(l, "```"):
			src := cur.String()
			if regexp.MustCompile(`(?m)^services:`).MatchString(src) {
				out = append(out, example{start, src})
			}
			cur = nil
		case cur != nil:
			cur.WriteString(l + "\n")
		}
	}
	return out
}

func officialSchema(t testing.TB) *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.Raw()))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("compose_spec.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("compose_spec.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// jsonValue converts a tree to what the schema validator reads.
func jsonValue(n *tree.Node) any {
	switch n.Kind {
	case tree.Int, tree.Float:
		return json.Number(n.Text)
	case tree.Map:
		m := map[string]any{}
		for _, e := range n.Entries {
			m[e.Key] = jsonValue(e.Value)
		}
		return m
	case tree.Seq:
		s := make([]any, len(n.Items))
		for i, it := range n.Items {
			s[i] = jsonValue(it)
		}
		return s
	}
	return plain(n)
}

// What this implementation refuses on purpose although the schema
// accepts it (each is documented in the README's limitations).
var deliberate = []*regexp.Regexp{
	regexp.MustCompile(`host bind mount`),
	regexp.MustCompile(`network_mode "[^"]+" is not supported`),
	regexp.MustCompile(`(network|volume) driver "[^"]+" is not available`),
	regexp.MustCompile(`anonymous volume`),
	regexp.MustCompile(`volume type "[^"]+" is not supported`),
	// What the schema cannot see: references across the model.
	regexp.MustCompile(`undefined (volume|network|service)`),
	regexp.MustCompile(`neither an image nor a build`),
	regexp.MustCompile(`not enabled by the active profiles`),
	regexp.MustCompile(`no service defined`),
	regexp.MustCompile(`no such file|file does not exist|not found`),
	regexp.MustCompile(`required variable`),
	regexp.MustCompile(`the \S+ prefix is reserved`),
	regexp.MustCompile(`an external (network|volume) has only a name`),
	// Files outside the project directory are never read (security).
	regexp.MustCompile(`outside the project directory`),
	// An unset variable interpolated into a key: the schema sees the
	// text before interpolation.
	regexp.MustCompile(`entry "=[^"]*" has no key`),
}

// TestSpecExamples runs the specification's own examples through the
// official JSON schema (the validator of record) and through Load: what
// the schema refuses, Load must refuse; what Load refuses that the
// schema accepts must be a deliberate refusal.
func TestSpecExamples(t *testing.T) {
	sch := officialSchema(t)
	ex := specExamples(t)
	if len(ex) < 40 {
		t.Fatalf("only %d examples found in spec.md: the extraction is broken", len(ex))
	}
	var accepted, refusedBoth, refusedDeliberately int
	for _, e := range ex {
		doc, perr := tree.ParseYAML("spec.md", []byte(e.src), tree.Limits{})
		var schemaErr error
		if perr != nil {
			schemaErr = perr
		} else {
			schemaErr = sch.Validate(jsonValue(doc))
		}
		_, err := Load(Options{FS: fstest.MapFS{"compose.yaml": {Data: []byte(e.src)}}, ProjectName: "spec"})
		switch {
		case schemaErr != nil && err == nil:
			t.Errorf("spec.md:%d: the schema refuses it (%v) but Load accepts it:\n%s", e.line, firstLine(schemaErr), e.src)
		case schemaErr != nil:
			refusedBoth++
		case err == nil:
			accepted++
		default:
			ok := false
			for _, re := range deliberate {
				ok = ok || re.MatchString(err.Error())
			}
			if !ok {
				t.Errorf("spec.md:%d: the schema accepts it but Load refuses it: %v\n%s", e.line, err, e.src)
			}
			refusedDeliberately++
		}
	}
	t.Logf("%d examples: %d loaded, %d refused by both, %d refused deliberately", len(ex), accepted, refusedBoth, refusedDeliberately)
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(fmt.Sprint(err), "\n")
	return s
}
