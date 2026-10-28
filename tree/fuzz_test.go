package tree

import (
	"testing"
)

// Small limits keep each fuzz input cheap; the bounds are what is fuzzed.
var fuzzLimits = Limits{MaxBytes: 1 << 16, MaxDepth: 32, MaxNodes: 1 << 12, MaxString: 1 << 12}

func checkTree(t *testing.T, n *Node) {
	t.Helper()
	count := 0
	var walk func(*Node, int)
	walk = func(n *Node, depth int) {
		count++
		if depth > fuzzLimits.MaxDepth+1 {
			t.Fatalf("depth %d past the limit", depth)
		}
		seen := map[string]bool{}
		for _, e := range n.Entries {
			if seen[e.Key] {
				t.Fatalf("duplicate key %q kept", e.Key)
			}
			seen[e.Key] = true
			walk(e.Value, depth+1)
		}
		for _, it := range n.Items {
			walk(it, depth+1)
		}
	}
	walk(n, 0)
	if count > fuzzLimits.MaxNodes {
		t.Fatalf("%d nodes, past the limit", count)
	}
}

func FuzzParseYAML(f *testing.F) {
	for _, s := range []string{
		"a: 1\n", "a: &x [1, *x]\n", "a: &a [x]\nb: [*a, *a, *a]\n", "m: {<<: {a: 1}, b: 2}\n",
		"a: !reset []\n", "a: !!binary aGk=\n", "? [a]\n: b\n", "a: 2001-12-14\n", "- 1\n- 2\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := ParseYAML("f.yaml", data, fuzzLimits)
		if err == nil {
			checkTree(t, n)
		}
	})
}

func FuzzParseJSON(f *testing.F) {
	for _, s := range []string{`{"a":[1,2.5,"x",true,null]}`, `[]`, `{"a":{"b":{}}}`, `1`, `"s"`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := ParseJSON("f.json", data, fuzzLimits)
		if err == nil {
			checkTree(t, n)
		}
	})
}

func FuzzParseHCL(f *testing.F) {
	for _, s := range []string{
		"a = 1\n", "service \"x\" {\n  image = \"${var.V}\"\n}\n", "variable \"V\" { default = \"d\" }\n",
		"a = [1, \"x\", {b = true}]\n", "a = 1 + 2 * 3\n", "a = true ? \"y\" : \"n\"\n", "b {\n  c = null\n}\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := ParseHCL("f.hcl", data, fuzzLimits, HCLOptions{Variables: map[string]string{"V": "v"}})
		if err == nil {
			checkTree(t, n)
		}
	})
}
