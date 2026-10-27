// Package schema embeds the Compose Specification's JSON schema
// (compose-spec/compose-spec, schema/compose-spec.json, Apache-2.0; see
// LICENSE in this directory) and answers which attributes it defines
// where. The loader uses it to tell an attribute the specification
// defines but this implementation does not apply (a warning) from one it
// does not define at all (an error, as the schema's additionalProperties
// false makes it).
//
// The full schema validation runs in the tests (compose_test.go), where a
// validator's weight costs nothing: the browser build stays light.
package schema

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed compose-spec.json
var raw []byte

// Raw returns the embedded schema document.
func Raw() []byte { return raw }

type node = map[string]any

var (
	once sync.Once
	doc  node
)

func load() node {
	once.Do(func() {
		if err := json.Unmarshal(raw, &doc); err != nil {
			panic("schema: embedded compose-spec.json: " + err.Error())
		}
	})
	return doc
}

func resolve(n node) node {
	for i := 0; i < 8; i++ {
		ref, ok := n["$ref"].(string)
		if !ok {
			return n
		}
		target := load()
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			next, _ := target[part].(node)
			if next == nil {
				return n
			}
			target = next
		}
		n = target
	}
	return n
}

// objects returns n and every object schema reachable through $ref,
// allOf, oneOf and anyOf: where an attribute may be defined.
func objects(n node, out []node, depth int) []node {
	if n == nil || depth > 8 {
		return out
	}
	n = resolve(n)
	out = append(out, n)
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		if alts, ok := n[k].([]any); ok {
			for _, a := range alts {
				if an, ok := a.(node); ok {
					out = objects(an, out, depth+1)
				}
			}
		}
	}
	return out
}

// Lookup walks path from the document root: each element is a property
// name, "*" for an entry of a mapping with user-defined keys
// (patternProperties), or "[]" for an item of an array. It reports the attributes defined there and
// whether the place is a closed object (only those attributes allowed).
func Lookup(path ...string) (props map[string]bool, closed bool) {
	key := strings.Join(path, "\x00")
	if v, ok := cache.Load(key); ok {
		r := v.(result)
		return r.props, r.closed
	}
	props, closed = lookup(path)
	cache.Store(key, result{props, closed})
	return props, closed
}

type result struct {
	props  map[string]bool
	closed bool
}

// cache holds Lookup's answers: the schema never changes, and the loader
// asks the same few places for every service. Callers must not modify
// the maps it returns.
var cache sync.Map

func lookup(path []string) (props map[string]bool, closed bool) {
	cur := []node{load()}
	for _, p := range path {
		var next []node
		for _, n := range cur {
			for _, o := range objects(n, nil, 0) {
				if p == "[]" {
					if it, ok := o["items"].(node); ok {
						next = append(next, it)
					}
					continue
				}
				if p == "*" {
					if pp, ok := o["patternProperties"].(node); ok {
						for pat, v := range pp {
							if vn, ok := v.(node); ok && pat != "^x-" {
								next = append(next, vn)
							}
						}
					}
					continue
				}
				if pr, ok := o["properties"].(node); ok {
					if v, ok := pr[p].(node); ok {
						next = append(next, v)
					}
				}
			}
		}
		if len(next) == 0 {
			return nil, false
		}
		cur = next
	}
	props = map[string]bool{}
	for _, n := range cur {
		for _, o := range objects(n, nil, 0) {
			if pr, ok := o["properties"].(node); ok {
				for k := range pr {
					props[k] = true
				}
			}
			if ap, ok := o["additionalProperties"].(bool); ok && !ap {
				closed = true
			}
			if up, ok := o["unevaluatedProperties"].(bool); ok && !up {
				closed = true
			}
		}
	}
	return props, closed
}
