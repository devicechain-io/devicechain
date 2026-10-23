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

// A narrowing written inside a composition is a narrowing: a property newly required in
// a conditional's then is found where it is, and fails.
func TestANarrowingInsideACompositionFails(t *testing.T) {
	base := `{"allOf":[{"if":{"properties":{"t":{"const":"a"}}},"then":{"required":["p"]}}]}`
	head := `{"allOf":[{"if":{"properties":{"t":{"const":"a"}}},"then":{"required":["p","q"]}}]}`
	got := found(Compare("f.json", parse(t, base), parse(t, head)), decl.Breaking)
	if !has(got, "$(allOf[0])(then).q required-added") {
		t.Fatalf("got %v", got)
	}
}

// What the tool cannot pair or cannot read the direction of fails conservatively, and
// each failure names a coordinate a declaration can cover.
func TestUndecidableChangesFailConservatively(t *testing.T) {
	cases := []struct{ name, base, head, want string }{
		{"a new allOf entry", `{"allOf":[{"required":["a"]}]}`, `{"allOf":[{"required":["a"]},{"required":["b"]}]}`, "$(allOf) composition-narrowed"},
		{"a new anyOf", `{"type":"object"}`, `{"type":"object","anyOf":[{"required":["a"]},{"required":["b"]}]}`, "$(anyOf) composition-narrowed"},
		{"an anyOf branch dropped", `{"anyOf":[{"type":"string"},{"type":"integer"}]}`, `{"anyOf":[{"type":"string"}]}`, "$(anyOf) composition-narrowed"},
		{"a oneOf branch narrowed", `{"oneOf":[{"type":["string","integer"]}]}`, `{"oneOf":[{"type":"string"}]}`, "$(oneOf[0]) type-narrowed"},
		{"a oneOf branch added", `{"oneOf":[{"type":"string"}]}`, `{"oneOf":[{"type":"string"},{"minLength":1}]}`, "$(oneOf) composition-narrowed"},
		{"a changed if", `{"if":{"required":["a"]},"then":{"required":["b"]}}`, `{"if":{"required":["c"]},"then":{"required":["b"]}}`, "$(if) composition-changed"},
		{"a then added", `{"if":{"required":["a"]}}`, `{"if":{"required":["a"]},"then":{"required":["b"]}}`, "$(then) composition-narrowed"},
		{"a changed not", `{"not":{"type":"null"}}`, `{"not":{"type":["null","string"]}}`, "$(not) composition-changed"},
		{"a format added", `{"type":"string"}`, `{"type":"string","format":"date-time"}`, "$ format-added"},
		{"a format changed", `{"type":"string","format":"date-time"}`, `{"type":"string","format":"date"}`, "$ format-changed"},
		{"a pattern changed", `{"type":"string","pattern":"^a"}`, `{"type":"string","pattern":"^ab"}`, "$ pattern-changed"},
		{"an external $ref moved", `{"properties":{"p":{"$ref":"a.schema.json"}}}`, `{"properties":{"p":{"$ref":"b.schema.json"}}}`, "$.p ref-changed"},
		{"a local $ref target narrowed", `{"properties":{"p":{"$ref":"#/$defs/a"}},"$defs":{"a":{"type":["string","null"]},"b":{"type":"string"}}}`, `{"properties":{"p":{"$ref":"#/$defs/b"}},"$defs":{"a":{"type":["string","null"]},"b":{"type":"string"}}}`, "$.p type-narrowed"},
		{"a multipleOf added", `{"type":"number"}`, `{"type":"number","multipleOf":0.5}`, "${multipleOf} bound-tightened"},
		{"a multipleOf raised", `{"type":"integer","multipleOf":2}`, `{"type":"integer","multipleOf":4}`, "${multipleOf} bound-tightened"},
		{"a multipleOf changed sideways", `{"type":"integer","multipleOf":2}`, `{"type":"integer","multipleOf":3}`, "${multipleOf} bound-tightened"},
		{"uniqueItems imposed", `{"type":"array"}`, `{"type":"array","uniqueItems":true}`, "${uniqueItems} bound-tightened"},
		{"an unmodelled keyword", `{"type":"object"}`, `{"type":"object","propertyNames":{"pattern":"^[a-z]+$"}}`, "${propertyNames} keyword-changed"},
		{"a boolean exclusiveMinimum", `{"type":"number","minimum":0}`, `{"type":"number","minimum":0,"exclusiveMinimum":true}`, "${exclusiveMinimum} keyword-changed"},
		{"a false schema", `{"properties":{"p":true}}`, `{"properties":{"p":false}}`, "$.p schema-changed"},
		{"additionalProperties false", `{"type":"object"}`, `{"type":"object","additionalProperties":false}`, "$ additional-properties-closed"},
	}
	for _, c := range cases {
		got := found(Compare("f.json", parse(t, c.base), parse(t, c.head)), decl.Breaking)
		if !has(got, c.want) {
			t.Errorf("%s: want breaking %q, got %v", c.name, c.want, got)
		}
	}
}

// The counterweight: what can be read as a loosening or a reordering does not fail.
func TestCompositionLooseningsAndReorderingsDoNotFail(t *testing.T) {
	cases := []struct{ name, base, head string }{
		{"allOf reordered", `{"allOf":[{"required":["a"]},{"required":["b"]}]}`, `{"allOf":[{"required":["b"]},{"required":["a"]}]}`},
		{"allOf entry dropped", `{"allOf":[{"required":["a"]},{"required":["b"]}]}`, `{"allOf":[{"required":["a"]}]}`},
		{"anyOf branch added", `{"anyOf":[{"type":"string"}]}`, `{"anyOf":[{"type":"string"},{"type":"integer"}]}`},
		{"then loosened", `{"if":{"required":["a"]},"then":{"required":["b","c"]}}`, `{"if":{"required":["a"]},"then":{"required":["b"]}}`},
		{"integer to number", `{"type":"integer"}`, `{"type":"number"}`},
		{"multipleOf lowered to a divisor", `{"type":"integer","multipleOf":4}`, `{"type":"integer","multipleOf":2}`},
		{"format removed", `{"type":"string","format":"date-time"}`, `{"type":"string"}`},
		{"description changed", `{"type":"string","description":"a"}`, `{"type":"string","description":"b","title":"t","examples":["x"]}`},
	}
	for _, c := range cases {
		if got := found(Compare("f.json", parse(t, c.base), parse(t, c.head)), decl.Breaking); len(got) != 0 {
			t.Errorf("%s: must not break, got %v", c.name, got)
		}
	}
}

// The real contract evolves its event dispatch by appending an allOf branch for a new
// event type. That is a declared break, not a silent pass: the test pins that the
// coordinate is the composition, so the declaration line is stable.
func TestAddingAnEventTypeBranchNeedsADeclaration(t *testing.T) {
	branch := func(t string) string {
		return `{"if":{"properties":{"eventType":{"const":"` + t + `"}},"required":["eventType"]},"then":{"properties":{"payload":{"$ref":"` + t + `.schema.json"}}}}`
	}
	base := `{"allOf":[` + branch("A") + `]}`
	head := `{"allOf":[` + branch("A") + `,` + branch("B") + `]}`
	got := found(Compare("f.json", parse(t, base), parse(t, head)), decl.Breaking)
	if len(got) != 1 || got[0] != "$(allOf) composition-narrowed" {
		t.Fatalf("got %v", got)
	}
}

func TestAnUnlistedKindPanicsRatherThanPassing(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an emitted kind missing from Kinds must not quietly take the Safe severity")
		}
	}()
	(&differ{surface: "f.json"}).add("$", "no-such-kind", "")
}

func TestEveryKindEmittedIsDeclaredInKinds(t *testing.T) {
	// The declarations parser rejects a kind it does not know, so a kind the differ can
	// emit but Kinds lacks could never be declared.
	for _, k := range []string{"property-removed", "required-added", "type-narrowed", "enum-narrowed", "const-changed",
		"additional-properties-closed", "bound-tightened", "pattern-added", "items-constrained", "schema-removed",
		"pattern-changed", "format-added", "format-changed", "composition-narrowed", "composition-changed", "ref-changed",
		"keyword-changed", "schema-changed"} {
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
