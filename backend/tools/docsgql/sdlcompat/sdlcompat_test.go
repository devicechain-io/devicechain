// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sdlcompat

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/graph-gophers/graphql-go/ast"
)

// 🔴 THE SERVICES TREE LIVES OUTSIDE THIS MODULE AND GO'S TEST CACHE DOES NOT TRACK
// IT; CI runs every module with -count=1.
const servicesTree = "../../../services"

// allKinds is written out by hand, not derived from the severities map, so that a kind
// constant added without a severity — or a severity for a kind that was dropped — fails
// here instead of being classified Breaking by change()'s fallback.
var allKinds = []string{
	SchemaRemoved, SchemaAdded, RootOperationChanged, TypeRemoved, TypeAdded, TypeKindChanged,
	FieldRemoved, FieldAdded, FieldTypeChanged, FieldTypeNarrowed,
	ArgumentRemoved, ArgumentAdded, RequiredArgumentAdded, ArgumentTypeChanged, ArgumentTypeWidened, ArgumentDefaultChanged, ArgumentBecameRequired,
	InputFieldRemoved, InputFieldAdded, RequiredInputFieldAdded, InputFieldTypeChanged, InputFieldTypeWidened, InputFieldDefaultChanged, InputFieldBecameRequired,
	EnumValueRemoved, EnumValueAdded, UnionMemberRemoved, UnionMemberAdded, InterfaceRemoved, InterfaceAdded,
	DeprecationAdded, DeprecationRemoved,
	DirectiveRemoved, DirectiveAdded, DirectiveArgumentRemoved, RequiredDirectiveArgAdded, DirectiveArgumentTypeChanged, DirectiveLocationRemoved,
	DirectiveArgBecameRequired,
}

func TestEveryKindIsClassified(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range allKinds {
		if seen[k] {
			t.Errorf("kind %q listed twice", k)
		}
		seen[k] = true
		if _, ok := SeverityOf(k); !ok {
			t.Errorf("kind %q has no severity", k)
		}
	}
	for k := range severities {
		if !seen[k] {
			t.Errorf("severity for %q, which is not a listed kind", k)
		}
	}
}

func mustParse(t *testing.T, sdl string) Parsed {
	t.Helper()
	a, err := Parse(sdl)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, sdl)
	}
	return Parsed{Name: "s", AST: a}
}

// got renders the changes as "coord kind severity" lines, sorted, for comparison.
func got(cs []Change) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s %s %s", c.Coord, c.Kind, c.Severity))
	}
	sort.Strings(out)
	return out
}

// The classification rules, one case per rule, each with a minimal schema pair. Every
// case lists EVERY change it expects, so a rule that also emits a spurious change fails.
func TestClassification(t *testing.T) {
	const base = `
		interface Node { id: ID! }
		interface Named { name: String }
		type Thing implements Node & Named {
			id: ID!
			name: String
			size: Int!
			tags: [String]
			find(q: String, limit: Int = 10, mode: Mode!): [Thing!]!
		}
		type Other { x: Int }
		union Any = Thing | Other
		enum Mode { A B }
		input Filter { q: String, n: Int!, max: Int! = 9, depth: Int = 1 }
		scalar Time
		type Query { thing(id: ID!, f: Filter, first: Int! = 5): Thing, any: Any, t: Time }
	`
	cases := []struct {
		name string
		edit func(string) string
		want []string
	}{
		{"identical", func(s string) string { return s }, nil},
		{"field removed", func(s string) string { return strings.Replace(s, "size: Int!", "", 1) },
			[]string{"Thing.size field-removed breaking"}},
		{"field added", func(s string) string { return strings.Replace(s, "size: Int!", "size: Int!\nweight: Float", 1) },
			[]string{"Thing.weight field-added safe"}},
		{"output non-null to nullable", func(s string) string { return strings.Replace(s, "size: Int!", "size: Int", 1) },
			[]string{"Thing.size field-type-changed breaking"}},
		{"output nullable to non-null", func(s string) string { return strings.Replace(s, "\t\t\tname: String\n", "\t\t\tname: String!\n", 1) },
			// The interface still declares String, which an implementation may narrow.
			[]string{"Thing.name field-type-narrowed safe"}},
		{"output list item narrowed", func(s string) string { return strings.Replace(s, "tags: [String]", "tags: [String!]!", 1) },
			[]string{"Thing.tags field-type-narrowed safe"}},
		{"output list item widened", func(s string) string {
			return strings.Replace(s, "[Thing!]!", "[Thing]!", 1)
		}, []string{"Thing.find field-type-changed breaking"}},
		{"output named type changed", func(s string) string { return strings.Replace(s, "size: Int!", "size: Float!", 1) },
			[]string{"Thing.size field-type-changed breaking"}},
		{"output list to scalar", func(s string) string { return strings.Replace(s, "tags: [String]", "tags: String", 1) },
			[]string{"Thing.tags field-type-changed breaking"}},
		{"argument removed", func(s string) string { return strings.Replace(s, "q: String, limit", "limit", 1) },
			[]string{"Thing.find(q:) argument-removed breaking"}},
		{"optional argument added", func(s string) string { return strings.Replace(s, "mode: Mode!)", "mode: Mode!, after: String)", 1) },
			[]string{"Thing.find(after:) argument-added safe"}},
		{"required argument added", func(s string) string { return strings.Replace(s, "mode: Mode!)", "mode: Mode!, after: String!)", 1) },
			[]string{"Thing.find(after:) required-argument-added breaking"}},
		{"required argument with default added", func(s string) string {
			return strings.Replace(s, "mode: Mode!)", "mode: Mode!, after: String! = \"x\")", 1)
		}, []string{"Thing.find(after:) argument-added safe"}},
		{"argument made required", func(s string) string { return strings.Replace(s, "find(q: String,", "find(q: String!,", 1) },
			[]string{"Thing.find(q:) argument-type-changed breaking"}},
		{"argument made optional", func(s string) string { return strings.Replace(s, "mode: Mode!)", "mode: Mode)", 1) },
			[]string{"Thing.find(mode:) argument-type-widened safe"}},
		{"argument named type changed", func(s string) string { return strings.Replace(s, "thing(id: ID!", "thing(id: String!", 1) },
			[]string{"Query.thing(id:) argument-type-changed breaking"}},
		{"argument default changed", func(s string) string { return strings.Replace(s, "limit: Int = 10", "limit: Int = 20", 1) },
			[]string{"Thing.find(limit:) argument-default-changed dangerous"}},
		// A non-null value that loses its default keeps its type string, so only the
		// default comparison sees it; every caller that omitted it now fails validation.
		{"non-null argument default removed", func(s string) string { return strings.Replace(s, "first: Int! = 5", "first: Int!", 1) },
			[]string{"Query.thing(first:) argument-became-required breaking"}},
		{"non-null argument default changed", func(s string) string { return strings.Replace(s, "first: Int! = 5", "first: Int! = 6", 1) },
			[]string{"Query.thing(first:) argument-default-changed dangerous"}},
		{"non-null argument default removed and made nullable", func(s string) string { return strings.Replace(s, "first: Int! = 5", "first: Int", 1) },
			[]string{"Query.thing(first:) argument-default-changed dangerous", "Query.thing(first:) argument-type-widened safe"}},
		{"input list item made nullable", func(s string) string { return strings.Replace(s, "f: Filter,", "f: Filter, ids: [ID!]!,", 1) },
			[]string{"Query.thing(ids:) required-argument-added breaking"}},
		{"input field removed", func(s string) string { return strings.Replace(s, "input Filter { q: String,", "input Filter {", 1) },
			[]string{"Filter.q input-field-removed breaking"}},
		{"optional input field added", func(s string) string { return strings.Replace(s, "depth: Int = 1 }", "depth: Int = 1, z: Int }", 1) },
			[]string{"Filter.z input-field-added safe"}},
		{"required input field added", func(s string) string { return strings.Replace(s, "depth: Int = 1 }", "depth: Int = 1, z: Int! }", 1) },
			[]string{"Filter.z required-input-field-added breaking"}},
		{"input field made required", func(s string) string { return strings.Replace(s, "{ q: String,", "{ q: String!,", 1) },
			[]string{"Filter.q input-field-type-changed breaking"}},
		{"input field made optional", func(s string) string { return strings.Replace(s, "n: Int!", "n: Int", 1) },
			[]string{"Filter.n input-field-type-widened safe"}},
		{"input field default changed", func(s string) string { return strings.Replace(s, "depth: Int = 1", "depth: Int = 2", 1) },
			[]string{"Filter.depth input-field-default-changed dangerous"}},
		{"input field default removed", func(s string) string { return strings.Replace(s, "depth: Int = 1", "depth: Int", 1) },
			[]string{"Filter.depth input-field-default-changed dangerous"}},
		{"non-null input field default removed", func(s string) string { return strings.Replace(s, "max: Int! = 9", "max: Int!", 1) },
			[]string{"Filter.max input-field-became-required breaking"}},
		{"enum value removed", func(s string) string { return strings.Replace(s, "enum Mode { A B }", "enum Mode { A }", 1) },
			[]string{"Mode.B enum-value-removed breaking"}},
		{"enum value added", func(s string) string { return strings.Replace(s, "enum Mode { A B }", "enum Mode { A B C }", 1) },
			[]string{"Mode.C enum-value-added dangerous"}},
		{"union member removed", func(s string) string { return strings.Replace(s, "= Thing | Other", "= Thing", 1) },
			[]string{"Any.Other union-member-removed breaking"}},
		{"union member added", func(s string) string {
			return strings.Replace(s, "= Thing | Other", "= Thing | Other | Third", 1) + "\ntype Third { y: Int }"
		}, []string{"Any.Third union-member-added dangerous", "Third type-added safe"}},
		{"interface dropped", func(s string) string { return strings.Replace(s, "implements Node & Named", "implements Node", 1) },
			[]string{"Thing.Named interface-removed breaking"}},
		{"interface added", func(s string) string {
			return strings.Replace(s, "type Other { x: Int }", "type Other implements Node { x: Int, id: ID! }", 1)
		}, []string{"Other.Node interface-added dangerous", "Other.id field-added safe"}},
		{"type removed", func(s string) string {
			s = strings.Replace(s, "scalar Time", "", 1)
			return strings.Replace(s, ", t: Time", "", 1)
		}, []string{"Query.t field-removed breaking", "Time type-removed breaking"}},
		{"type kind changed", func(s string) string { return strings.Replace(s, "scalar Time", "enum Time { NOW }", 1) },
			[]string{"Time type-kind-changed breaking"}},
		{"deprecation added", func(s string) string {
			return strings.Replace(s, "size: Int!", `size: Int! @deprecated(reason: "Use weight.")`, 1)
		}, []string{"Thing.size deprecation-added safe"}},
		{"enum value deprecated", func(s string) string {
			return strings.Replace(s, "enum Mode { A B }", `enum Mode { A B @deprecated(reason: "Use A.") }`, 1)
		}, []string{"Mode.B deprecation-added safe"}},
	}
	b := mustParse(t, base)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := mustParse(t, tc.edit(base))
			g := got(Diff("s", b.AST, h.AST))
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if strings.Join(g, "\n") != strings.Join(want, "\n") {
				t.Errorf("changes:\n  got  %q\n  want %q", g, want)
			}
		})
	}
}

func TestDirectives(t *testing.T) {
	const base = `
		directive @auth(scope: String!, level: Int) on FIELD_DEFINITION | OBJECT
		type Query { x: Int @auth(scope: "a") }
	`
	cases := []struct {
		name, from, to string
		want           []string
	}{
		{"removed", "directive @auth(scope: String!, level: Int) on FIELD_DEFINITION | OBJECT\n", "",
			[]string{"@auth directive-removed breaking"}},
		{"argument removed", "level: Int)", ")", []string{"@auth(level:) directive-argument-removed breaking"}},
		{"required argument added", "level: Int)", "level: Int, tier: Int!)",
			[]string{"@auth(tier:) required-directive-argument-added breaking"}},
		{"argument type changed", "scope: String!", "scope: Int!",
			[]string{"@auth(scope:) directive-argument-type-changed breaking"}},
		{"location removed", "on FIELD_DEFINITION | OBJECT", "on FIELD_DEFINITION",
			[]string{"@auth.OBJECT directive-location-removed breaking"}},
	}
	b := mustParse(t, base)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edited := strings.Replace(base, tc.from, tc.to, 1)
			if tc.name == "removed" {
				edited = strings.Replace(edited, ` @auth(scope: "a")`, "", 1)
			}
			g := got(Diff("s", b.AST, mustParse(t, edited).AST))
			if strings.Join(g, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("changes:\n  got  %q\n  want %q", g, tc.want)
			}
		})
	}
}

func TestDirectiveArgumentDefaultRemoved(t *testing.T) {
	b := mustParse(t, `directive @lim(n: Int! = 1) on FIELD_DEFINITION
		type Query { x: Int }`)
	h := mustParse(t, `directive @lim(n: Int!) on FIELD_DEFINITION
		type Query { x: Int }`)
	g := got(Diff("s", b.AST, h.AST))
	want := []string{"@lim(n:) directive-argument-became-required breaking"}
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", g, want)
	}
}

func TestRootOperationRemoved(t *testing.T) {
	b := mustParse(t, `type Query { x: Int } type Mutation { y: Int }`)
	h := mustParse(t, `type Query { x: Int }`)
	g := got(Diff("s", b.AST, h.AST))
	want := []string{"Mutation root-operation-changed breaking", "Mutation type-removed breaking"}
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", g, want)
	}
}

func TestSchemaRemovedAndAdded(t *testing.T) {
	a := mustParse(t, `type Query { x: Int }`)
	gone := Parsed{Name: "gone", AST: a.AST}
	kept := Parsed{Name: "kept", AST: a.AST}
	added := Parsed{Name: "new", AST: a.AST}
	cs := Compare([]Parsed{gone, kept}, []Parsed{kept, added})
	var g []string
	for _, c := range cs {
		g = append(g, fmt.Sprintf("%s %s %s", c.Schema, c.Coord, c.Kind))
	}
	want := []string{"gone * schema-removed", "new * schema-added"}
	if strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", g, want)
	}
}

// THE REAL TREE. Two properties, both on the schemas the services actually serve: the
// tree compared with itself reports nothing (the check does not cry wolf on correct
// code), and a field removed from it is reported by name (the check can fail on the
// real thing, not only on a fixture).
func TestRealTree(t *testing.T) {
	head, err := Load(servicesTree)
	if err != nil {
		t.Fatal(err)
	}
	if len(head) < 8 {
		t.Fatalf("loaded %d schemas; a scan that found almost nothing passes everything", len(head))
	}
	if cs := Compare(head, head); len(cs) != 0 {
		t.Fatalf("the tree compared with itself reports %d change(s), first %s", len(cs), cs[0])
	}

	// Reload the baseline so the two sides share no AST, then drop the first Query field
	// of the first schema from the CURRENT side.
	base, err := Load(servicesTree)
	if err != nil {
		t.Fatal(err)
	}
	q, ok := head[0].AST.RootOperationTypes["query"]
	if !ok || q == nil {
		t.Fatalf("%s has no query root", head[0].Name)
	}
	obj := head[0].AST.Types[q.TypeName()]
	fields := fieldsOf(t, obj)
	removed := (*fields)[0].Name
	*fields = (*fields)[1:]

	res := Evaluate("v0.0.0", Compare(base, head), nil)
	if len(res.Undeclared) != 1 {
		t.Fatalf("removing %s.%s from %s: %d undeclared break(s), want 1: %v",
			q.TypeName(), removed, head[0].Name, len(res.Undeclared), res.Undeclared)
	}
	c := res.Undeclared[0]
	if c.Schema != head[0].Name || c.Coord != q.TypeName()+"."+removed || c.Kind != FieldRemoved {
		t.Errorf("got %s, want %s: %s.%s %s", c, head[0].Name, q.TypeName(), removed, FieldRemoved)
	}
	if !res.Failed() {
		t.Error("an undeclared removal did not fail the check")
	}
}

func TestRealTreeDeprecationsCarryReasons(t *testing.T) {
	head, err := Load(servicesTree)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range LintDeprecations(head) {
		t.Errorf("%s", f)
	}
}

func TestLintDeprecations(t *testing.T) {
	p := mustParse(t, `
		enum E { A @deprecated B @deprecated(reason: "Use A.") }
		input I { a: Int @deprecated(reason: "  ") b: Int @deprecated(reason: "Use c.") c: Int }
		type Query {
			bare: Int @deprecated
			spelled: Int @deprecated(reason: "No longer supported")
			good: Int @deprecated(reason: "Use other.")
			arg(x: Int @deprecated, y: Int): Int
			block: Int @deprecated(reason: """Use good.""")
			other: Int
		}
	`)
	var g []string
	for _, f := range LintDeprecations([]Parsed{p}) {
		g = append(g, f.Coord)
	}
	want := []string{"E.A", "I.a", "Query.arg(x:)", "Query.bare", "Query.spelled"}
	if strings.Join(g, " ") != strings.Join(want, " ") {
		t.Errorf("flagged %q, want %q", g, want)
	}
}

func TestParseDeclarations(t *testing.T) {
	in := `
# comment
v0.24.0 device-management Query.x field-removed Replaced by y.
v0.24.0 device-management Query.x field-removed Again.
v0.24.0 device-management Query.z field-removed
v0.24 device-management Query.z field-removed Bad tag.
v0.24.0-rc.1 device-management Query.z field-removed Candidate tag.
v0.24.0 device-management Query.z field-gone Unknown kind.
v0.24.0 device-management Mode.C enum-value-added Not breaking.
v0.24.0 user-management-admin Mutation.a(b:) argument-type-changed Now an ID.
`
	decls, bad := ParseDeclarations(strings.NewReader(in), "breaks.txt")
	if len(decls) != 2 {
		t.Errorf("parsed %d declarations, want 2: %+v", len(decls), decls)
	}
	if len(decls) > 0 && decls[0].Reason != "Replaced by y." {
		t.Errorf("reason %q", decls[0].Reason)
	}
	wantBad := []string{"declared twice", "reason is required", "not a stable release tag", "not a stable release tag",
		"is not a change kind", "nothing to declare"}
	if len(bad) != len(wantBad) {
		t.Fatalf("%d invalid lines, want %d: %v", len(bad), len(wantBad), bad)
	}
	for i, w := range wantBad {
		if !strings.Contains(bad[i].String(), w) {
			t.Errorf("finding %d = %q, want it to say %q", i, bad[i], w)
		}
	}
}

func TestEvaluate(t *testing.T) {
	br := func(coord string) Change { return change("s", coord, FieldRemoved, "") }
	changes := []Change{br("Q.a"), br("Q.b"), change("s", "E.X", EnumValueAdded, ""), change("s", "Q.c", FieldAdded, "")}
	decls := []Declaration{
		{Baseline: "v1.0.0", Schema: "s", Coord: "Q.a", Kind: FieldRemoved, Reason: "r", Line: 1},
		{Baseline: "v0.9.0", Schema: "s", Coord: "Q.b", Kind: FieldRemoved, Reason: "r", Line: 2},     // expired: does not cover Q.b
		{Baseline: "v1.0.0", Schema: "s", Coord: "Q.gone", Kind: FieldRemoved, Reason: "r", Line: 3},  // stale
		{Baseline: "v1.0.0", Schema: "s", Coord: "Q.b", Kind: FieldTypeChanged, Reason: "r", Line: 4}, // wrong kind: stale
	}
	r := Evaluate("v1.0.0", changes, decls)
	if len(r.Declared) != 1 || r.Declared[0].Change.Coord != "Q.a" {
		t.Errorf("declared %+v", r.Declared)
	}
	if len(r.Undeclared) != 1 || r.Undeclared[0].Coord != "Q.b" {
		t.Errorf("undeclared %+v", r.Undeclared)
	}
	if len(r.Expired) != 1 || r.Expired[0].Line != 2 {
		t.Errorf("expired %+v", r.Expired)
	}
	if len(r.Stale) != 2 || r.Stale[0].Line != 3 || r.Stale[1].Line != 4 {
		t.Errorf("stale %+v", r.Stale)
	}
	if len(r.Dangerous) != 1 || r.Safe != 1 {
		t.Errorf("dangerous %d safe %d", len(r.Dangerous), r.Safe)
	}
	if !r.Failed() {
		t.Error("undeclared + stale did not fail")
	}
	if Evaluate("v1.0.0", nil, decls[1:2]).Failed() {
		t.Error("an expired declaration alone failed the check; a release tag would turn main red")
	}
}

func fieldsOf(t *testing.T, nt ast.NamedType) *ast.FieldsDefinition {
	t.Helper()
	o, ok := nt.(*ast.ObjectTypeDefinition)
	if !ok || len(o.Fields) == 0 {
		t.Fatalf("%s is not an object type with fields", nt.TypeName())
	}
	return &o.Fields
}
