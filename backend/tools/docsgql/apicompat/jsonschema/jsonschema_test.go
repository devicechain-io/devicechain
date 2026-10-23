// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package jsonschema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
)

func parse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// kinds returns "coord kind" for every change of at least the given severity.
func found(cs []decl.Change, min decl.Severity) []string {
	var out []string
	for _, c := range cs {
		if c.Severity >= min {
			out = append(out, c.Coord+" "+c.Kind)
		}
	}
	return out
}

func has(list []string, want string) bool {
	for _, l := range list {
		if l == want {
			return true
		}
	}
	return false
}

func TestEachBreakingRuleFiresAndNamesItsElement(t *testing.T) {
	base := `{
	  "type": "object",
	  "properties": {
	    "a": {"type": "string", "enum": ["x", "y"], "minLength": 1},
	    "gone": {"type": "string"},
	    "n": {"type": ["string", "null"]},
	    "c": {"const": "one"},
	    "list": {"type": "array", "maxItems": 10, "items": {"type": "object", "properties": {"k": {"type": "string"}}}},
	    "open": {"type": "object"}
	  },
	  "required": ["a"]
	}`
	head := `{
	  "type": "object",
	  "properties": {
	    "a": {"type": "string", "enum": ["x"], "minLength": 2},
	    "n": {"type": "string"},
	    "c": {"const": "two"},
	    "list": {"type": "array", "maxItems": 5, "items": {"type": "object", "properties": {}}},
	    "open": {"type": "object", "additionalProperties": false},
	    "fresh": {"type": "string"}
	  },
	  "required": ["a", "fresh"]
	}`
	got := found(Compare("f.json", parse(t, base), parse(t, head)), decl.Breaking)
	for _, want := range []string{
		"$.gone property-removed",
		"$.fresh required-added",
		"$.a enum-narrowed",
		"$.a{minLength} bound-tightened",
		"$.n type-narrowed",
		"$.c const-changed",
		"$.list{maxItems} bound-tightened",
		"$.list[].k property-removed",
		"$.open additional-properties-closed",
	} {
		if !has(got, want) {
			t.Errorf("missing breaking %q in %v", want, got)
		}
	}
}

func TestAdditionsAndLooseningsNeverFail(t *testing.T) {
	base := `{"type":"object","properties":{"a":{"type":"string","enum":["x"],"maxLength":5}},"required":["a"],"additionalProperties":false}`
	head := `{"type":"object","properties":{"a":{"type":["string","null"],"enum":["x","y"],"maxLength":9},"b":{"type":"string"}},"required":[],"additionalProperties":true}`
	cs := Compare("f.json", parse(t, base), parse(t, head))
	if b := found(cs, decl.Breaking); len(b) != 0 {
		t.Fatalf("loosening and adding must not break, got %v", b)
	}
	d := found(cs, decl.Dangerous)
	for _, want := range []string{"$.a enum-widened", "$.a type-widened", "$.a{maxLength} bound-loosened", "$.a required-removed", "$ additional-properties-opened"} {
		if !has(d, want) {
			t.Errorf("missing %q in %v", want, d)
		}
	}
	if !has(found(cs, decl.Safe), "$.b property-added") {
		t.Errorf("an added optional property is a safe change: %v", found(cs, decl.Safe))
	}
}

func TestIdenticalSchemasHaveNoChanges(t *testing.T) {
	s := `{"type":"object","properties":{"a":{"$ref":"#/$defs/a"}},"$defs":{"a":{"type":"string"}}}`
	if cs := Compare("f.json", parse(t, s), parse(t, s)); len(cs) != 0 {
		t.Fatalf("got %v", cs)
	}
}

// A change inside a $defs entry is a change to every property that refers to it, and
// it is found where the property is, not where the definition is.
func TestRefsAreFollowedOnBothSides(t *testing.T) {
	base := `{"type":"object","properties":{"e":{"$ref":"#/$defs/entry"}},"$defs":{"entry":{"type":"object","properties":{"m":{"type":"string"}},"required":["m"]}}}`
	head := `{"type":"object","properties":{"e":{"$ref":"#/$defs/entry"}},"$defs":{"entry":{"type":"object","properties":{},"required":[]}}}`
	got := found(Compare("f.json", parse(t, base), parse(t, head)), decl.Breaking)
	if !has(got, "$.e.m property-removed") {
		t.Fatalf("a property removed inside a $ref target must be found at the referring path: %v", got)
	}
}

func TestRecursiveSchemasTerminate(t *testing.T) {
	s := `{"$ref":"#/$defs/node","$defs":{"node":{"type":"object","properties":{"child":{"$ref":"#/$defs/node"}}}}}`
	h := `{"$ref":"#/$defs/node","$defs":{"node":{"type":"object","properties":{"child":{"$ref":"#/$defs/node"},"x":{"type":"string"}}}}}`
	cs := Compare("f.json", parse(t, s), parse(t, h))
	if !has(found(cs, decl.Safe), "$.x property-added") {
		t.Fatalf("got %v", cs)
	}
}

func TestCompositionIsReportedNotJudged(t *testing.T) {
	base := `{"allOf":[{"if":{"properties":{"t":{"const":"a"}}},"then":{"required":["p"]}}]}`
	head := `{"allOf":[{"if":{"properties":{"t":{"const":"a"}}},"then":{"required":["p","q"]}}]}`
	cs := Compare("f.json", parse(t, base), parse(t, head))
	if len(found(cs, decl.Breaking)) != 0 || !has(found(cs, decl.Dangerous), "$ composition-changed") {
		t.Fatalf("got %v", cs)
	}
}

func TestEveryKindEmittedIsDeclaredInKinds(t *testing.T) {
	// The declarations parser rejects a kind it does not know, so a kind the differ can
	// emit but Kinds lacks could never be declared.
	for _, k := range []string{"property-removed", "required-added", "type-narrowed", "enum-narrowed", "const-changed",
		"additional-properties-closed", "bound-tightened", "pattern-added", "items-constrained", "schema-removed"} {
		if Kinds[k] != decl.Breaking {
			t.Errorf("%s must be a breaking kind", k)
		}
	}
	for k, sev := range Kinds {
		if strings.TrimSpace(k) == "" || k != strings.ToLower(k) || sev < decl.Safe || sev > decl.Breaking {
			t.Errorf("malformed kind %q", k)
		}
	}
}

func TestCRDVersionsAreLoaded(t *testing.T) {
	// The real CRDs: both load, and the schema carries spec.
	for _, f := range []string{"core.devicechain.io_instances.yaml", "core.devicechain.io_instanceconfigurations.yaml"} {
		m, err := LoadCRD("../../../../k8s/config/crd/bases/" + f)
		if err != nil {
			t.Fatal(err)
		}
		for v, s := range m {
			props, _ := s.(map[string]any)["properties"].(map[string]any)
			if _, ok := props["spec"]; !ok {
				t.Errorf("%s %s: no spec in the loaded schema", f, v)
			}
		}
	}
}
