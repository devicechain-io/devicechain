// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/graph-gophers/graphql-go"
)

// THE LIST OPTIONAL, DRIVEN THROUGH A REAL SCHEMA.
//
// 🔴 A UNIT TEST ON UnmarshalGraphQL CANNOT ESTABLISH THE THING THAT MATTERS HERE, which
// is that an ABSENT list arrives as Set=false. Absent means the library never calls the
// method at all, so a test that calls it directly has already assumed the answer. The
// same reasoning as optional_test.go's, and it applies harder to a list: the failure
// mode being ruled out is that `[]` and "not sent" become the same observation, and the
// only place that distinction exists is inside the packer.
//
// 🔴 AND THE VALUE ARRIVES AS A VARIABLE, NOT A LITERAL. A literal is decoded by the
// query parser and a variable by encoding/json; they reach the packer as different Go
// values, and the pinned graphql-go fork exists because the two paths diverge. Every
// real client — console, SDKs, dcctl, codegen — sends variables, so the variable path is
// the one that has to be right. The literal cases below are the cross-check, not the
// substance.

const listSchema = `
schema { query: Query  mutation: Mutation }
type Query { ping: String! }
type Mutation {
  updateThing(request: ListUpdateRequest!): Thing!
}
input ListUpdateRequest {
  # NULLABLE, deliberately. [String!]! would be required by validation, and the absent
  # state would stop being representable at all.
  tags: [String!]
  # A plain []string alongside it as a control: the optional list must not change how an
  # ordinary nullable list behaves, and it is what the coercion rule below is measured
  # against.
  plain: [String!]
}
type Thing { ok: Boolean! }
`

type listUpdateRequest struct {
	Tags  OptionalStringList
	Plain *[]string
}

type listResolver struct{ last listUpdateRequest }

func (r *listResolver) Ping() string { return "pong" }

func (r *listResolver) UpdateThing(ctx context.Context, args struct{ Request listUpdateRequest }) (*optThing, error) {
	r.last = args.Request
	return &optThing{}, nil
}

func execList(t *testing.T, query string, vars map[string]any) listUpdateRequest {
	t.Helper()
	r := &listResolver{}
	schema := MustParseSchema(listSchema, r)
	resp := schema.Exec(context.Background(), query, "", vars)
	if len(resp.Errors) > 0 {
		t.Fatalf("unexpected GraphQL error: %v", resp.Errors)
	}
	return r.last
}

const listMutationVar = `mutation ($r: ListUpdateRequest!) { updateThing(request: $r) { ok } }`

// THE CENTRAL GUARANTEE, and the reason this type exists at all: an absent list and an
// empty list are two different requests. If this collapses, every list field built on
// this type has become impossible to empty, or impossible to leave alone — depending on
// which way it collapsed.
func TestOptionalStringListDistinguishesAbsentFromEmpty(t *testing.T) {
	absent := execList(t, listMutationVar, map[string]any{"r": map[string]any{}}).Tags
	if absent.Set {
		t.Fatalf("Set is true for a list the request never mentioned (Value=%v) — an absent "+
			"list would then EMPTY the stored one on every update", absent.Value)
	}

	empty := execList(t, listMutationVar, map[string]any{"r": map[string]any{"tags": []any{}}}).Tags
	if !empty.Set {
		t.Fatal("an empty list arrived as absent, so a caller has no way to empty the field")
	}
	if empty.Value == nil {
		t.Fatal("an empty list arrived with a nil Value, which is the reading reserved for an " +
			"explicit null; the two fold the same way but must not be the same observation")
	}
	if len(empty.Value) != 0 {
		t.Fatalf("an empty list arrived carrying %v", empty.Value)
	}
}

// The remaining wire states, both request paths. The literal cases are here because the
// two paths are decoded by different code and this is exactly the class of divergence
// the pinned fork exists to fix — a list that worked as a variable and not as a literal
// would be a defect nobody driving the console would ever see.
func TestOptionalStringListCarriesEveryWireState(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		vars    map[string]any
		wantSet bool
		wantNil bool
		want    []string
	}{
		{
			name:    "absent, variable",
			query:   listMutationVar,
			vars:    map[string]any{"r": map[string]any{}},
			wantSet: false,
		},
		{
			name:    "explicit null, variable",
			query:   listMutationVar,
			vars:    map[string]any{"r": map[string]any{"tags": nil}},
			wantSet: true, wantNil: true,
		},
		{
			name:    "empty list, variable",
			query:   listMutationVar,
			vars:    map[string]any{"r": map[string]any{"tags": []any{}}},
			wantSet: true, want: []string{},
		},
		{
			name:    "entries, variable",
			query:   listMutationVar,
			vars:    map[string]any{"r": map[string]any{"tags": []any{"a", "b"}}},
			wantSet: true, want: []string{"a", "b"},
		},
		{
			name:    "absent, literal",
			query:   `mutation { updateThing(request: {}) { ok } }`,
			wantSet: false,
		},
		{
			name:    "explicit null, literal",
			query:   `mutation { updateThing(request: {tags: null}) { ok } }`,
			wantSet: true, wantNil: true,
		},
		{
			name:    "empty list, literal",
			query:   `mutation { updateThing(request: {tags: []}) { ok } }`,
			wantSet: true, want: []string{},
		},
		{
			name:    "entries, literal",
			query:   `mutation { updateThing(request: {tags: ["a", "b"]}) { ok } }`,
			wantSet: true, want: []string{"a", "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := execList(t, tc.query, tc.vars).Tags
			if got.Set != tc.wantSet {
				t.Fatalf("Set = %v, want %v", got.Set, tc.wantSet)
			}
			if !tc.wantSet {
				return
			}
			if tc.wantNil {
				if got.Value != nil {
					t.Fatalf("an explicit null produced %v; null must empty, not set", got.Value)
				}
				return
			}
			if got.Value == nil {
				t.Fatal("a sent list arrived as nil")
			}
			if !reflect.DeepEqual(got.Value, tc.want) {
				t.Fatalf("Value = %v, want %v", got.Value, tc.want)
			}
		})
	}
}

// 🔴 THE ORDER IS PART OF THE VALUE. A list of authorities or redirect URIs that came
// back re-ordered would make "I sent you what you gave me" a change, which is the same
// rule ApplyToRequired's no-trimming paragraph is about, one datatype along.
func TestOptionalStringListPreservesOrderAndDuplicates(t *testing.T) {
	got := execList(t, listMutationVar,
		map[string]any{"r": map[string]any{"tags": []any{"b", "a", "b"}}}).Tags
	if !reflect.DeepEqual(got.Value, []string{"b", "a", "b"}) {
		t.Fatalf("Value = %v, want [b a b] — the list was reordered or de-duplicated, so a "+
			"caller restating what they read back would be making a change", got.Value)
	}
}

// A bare value where a list is expected is coerced to a one-entry list. The assertion is
// not that the coercion is desirable — it is that this type agrees with the library's own
// listPacker, which is measured HERE rather than asserted: the plain []string control
// receives the identical request in the identical position.
func TestOptionalStringListCoercesASingleValueLikeAPlainList(t *testing.T) {
	got := execList(t, listMutationVar,
		map[string]any{"r": map[string]any{"tags": "solo", "plain": "solo"}})
	if got.Plain == nil {
		t.Fatal("the plain []string control did not receive the coerced value, so this test " +
			"has nothing to compare against and proves nothing about agreement")
	}
	if !reflect.DeepEqual(got.Tags.Value, *got.Plain) {
		t.Fatalf("OptionalStringList produced %v where a plain []string field produced %v — "+
			"the same request means two things under one schema", got.Tags.Value, *got.Plain)
	}
}

// The counterweight to the coercion above: a value that is not a String is refused
// rather than silently dropped or stringified.
func TestOptionalStringListRefusesANonStringEntry(t *testing.T) {
	schema := MustParseSchema(listSchema, &listResolver{})
	resp := schema.Exec(context.Background(), listMutationVar, "",
		map[string]any{"r": map[string]any{"tags": []any{"a", 7}}})
	if len(resp.Errors) == 0 {
		t.Fatal("a non-String entry in a [String!] was accepted")
	}
}

// 🔴 NEGATIVE CONTROL for the type spelling.
//
// ImplementsGraphQLType is the only thing keeping this type off a `[String]` field,
// whose entries may be null — a shape UnmarshalGraphQL would meet as a nil entry and
// have no honest reading for. The refusal happens at SCHEMA CONSTRUCTION, which is the
// right time, but the check itself is a one-line string comparison that reads like
// decoration. Without this control it is unfalsifiable: every test above would pass
// identically if it returned true unconditionally.
func TestOptionalStringListRefusesAListOfNullableStrings(t *testing.T) {
	_, err := graphql.ParseSchema(`
schema { query: Query  mutation: Mutation }
type Query { ping: String! }
type Mutation { updateThing(request: NullableEntryRequest!): Boolean! }
input NullableEntryRequest { tags: [String] }
`, &nullableEntryResolver{})
	if err == nil {
		t.Fatal("a [String] field bound to OptionalStringList built successfully — the type " +
			"would then be handed nil entries it has no reading for")
	}
	if !strings.Contains(err.Error(), "[String]") {
		t.Fatalf("schema construction failed for an unexpected reason, so this control is no "+
			"longer measuring the type check: %v", err)
	}
}

type nullableEntryResolver struct{}

func (r *nullableEntryResolver) Ping() string { return "pong" }
func (r *nullableEntryResolver) UpdateThing(ctx context.Context, args struct {
	Request struct{ Tags OptionalStringList }
}) (bool, error) {
	return true, nil
}

// ApplyTo is where the wire states become one stored value, and where the deliberate
// collapse of null and [] onto "empty" lives.
func TestOptionalStringListApplyTo(t *testing.T) {
	stored := []string{"read", "write"}

	if got := (OptionalStringList{}).ApplyTo(stored); !reflect.DeepEqual(got, stored) {
		t.Fatalf("an absent list changed the stored value to %v", got)
	}

	// Null and [] must agree, because they are the same request spelled two ways.
	fromNull := ClearedStringList().ApplyTo(stored)
	fromEmpty := OptionalStringListOf([]string{}).ApplyTo(stored)
	if !reflect.DeepEqual(fromNull, fromEmpty) {
		t.Fatalf("null folded to %v and [] folded to %v — the two spellings of \"empty\" "+
			"have stopped meaning the same thing", fromNull, fromEmpty)
	}
	if len(fromNull) != 0 {
		t.Fatalf("null did not empty the list: %v", fromNull)
	}
	// Non-nil, so a caller re-marshalling the result writes [] rather than null.
	if fromNull == nil {
		t.Fatal("an emptied list folded to nil, so a JSON column would record null and " +
			"\"the caller emptied this\" would look like \"this was never set\"")
	}

	replaced := OptionalStringListOf([]string{"admin"}).ApplyTo(stored)
	if !reflect.DeepEqual(replaced, []string{"admin"}) {
		t.Fatalf("a sent list did not replace the stored one wholesale: %v", replaced)
	}
}

// 🔴 The structural guard in partialupdatetest asks the MECHANISM — a Set bool, the
// Nullable() marker, ImplementsGraphQLType — rather than a roster of type names, which
// is what makes it cover a type added after it was written. This asserts the new type
// actually answers that question, so "the guard already covers it" is measured here
// rather than assumed at the guard.
func TestOptionalStringListCarriesTheThreeStateMarkers(t *testing.T) {
	var v any = OptionalStringList{}
	if _, ok := v.(interface{ Nullable() }); !ok {
		t.Error("OptionalStringList has no Nullable() marker: the schema will not build for a " +
			"nullable field, and the structural update guard will not recognise it")
	}
	if _, ok := v.(interface{ ImplementsGraphQLType(string) bool }); !ok {
		t.Error("OptionalStringList does not implement ImplementsGraphQLType")
	}
	f, ok := reflect.TypeOf(v).FieldByName("Set")
	if !ok || f.Type.Kind() != reflect.Bool {
		t.Error("OptionalStringList has no bool Set field, so nothing can tell an absent list " +
			"from one sent explicitly")
	}
}
