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

// These tests drive the REAL schema rather than the packer directly. That is
// deliberate: the entire mechanism lives inside graphql-go's packer, so a unit
// test of UnmarshalGraphQL would prove only that the method we wrote does what we
// wrote — it could not detect the failure that actually matters, which is the
// library never calling it, or calling it for a field the caller never sent.
//
// Both request paths are exercised throughout. A literal is decoded by the query
// parser and a variable by encoding/json, they reach the packer as different Go
// types, and the two paths diverging under the same schema is not hypothetical
// here: it is exactly the defect the pinned graphql-go fork exists to fix.

const optionalSchema = `
schema { query: Query  mutation: Mutation }
type Query { ping: String! }
type Mutation {
  updateThing(request: ThingUpdateRequest!): Thing!
}
input ThingUpdateRequest {
  name: String
  enabled: Boolean
  count: Int
  ratio: Float
  ref: ID
  # A plain pointer field, kept alongside the optional ones as a control: the
  # optional types must not change how an ordinary nullable field behaves.
  plain: String
}
type Thing { ok: Boolean! }
`

type optUpdateRequest struct {
	Name    OptionalString
	Enabled OptionalBool
	Count   OptionalInt32
	Ratio   OptionalFloat64
	Ref     OptionalID
	Plain   *string
}

type optThing struct{}

func (t *optThing) Ok() bool { return true }

type optResolver struct{ last optUpdateRequest }

func (r *optResolver) Ping() string { return "pong" }

func (r *optResolver) UpdateThing(ctx context.Context, args struct{ Request optUpdateRequest }) (*optThing, error) {
	r.last = args.Request
	return &optThing{}, nil
}

// exec runs one mutation and returns what the resolver received, failing the test
// on any GraphQL error — an assertion that reads "absent" from a request that was
// actually rejected would pass for the wrong reason.
func execOptional(t *testing.T, query string, vars map[string]any) optUpdateRequest {
	t.Helper()
	r := &optResolver{}
	schema := MustParseSchema(optionalSchema, r)
	resp := schema.Exec(context.Background(), query, "", vars)
	if len(resp.Errors) > 0 {
		t.Fatalf("unexpected GraphQL error: %v", resp.Errors)
	}
	return r.last
}

const optMutationVar = `mutation ($r: ThingUpdateRequest!) { updateThing(request: $r) { ok } }`

// The central guarantee: absent, null and a value are three distinguishable states.
// If this test ever collapses to two, every update* mutation built on these types
// has silently become a full replace again.
func TestOptionalStringCarriesThreeStates(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		vars    map[string]any
		wantSet bool
		wantNil bool
		want    string
	}{
		{
			name:  "absent, variable",
			query: optMutationVar,
			vars:  map[string]any{"r": map[string]any{}},
			// Set stays false: the packer never calls UnmarshalGraphQL for a field
			// the request map does not contain.
			wantSet: false,
		},
		{
			name:    "explicit null, variable",
			query:   optMutationVar,
			vars:    map[string]any{"r": map[string]any{"name": nil}},
			wantSet: true,
			wantNil: true,
		},
		{
			name:    "value, variable",
			query:   optMutationVar,
			vars:    map[string]any{"r": map[string]any{"name": "renamed"}},
			wantSet: true,
			want:    "renamed",
		},
		{
			name:    "absent, literal",
			query:   `mutation { updateThing(request: {}) { ok } }`,
			wantSet: false,
		},
		{
			name:    "explicit null, literal",
			query:   `mutation { updateThing(request: {name: null}) { ok } }`,
			wantSet: true,
			wantNil: true,
		},
		{
			name:    "value, literal",
			query:   `mutation { updateThing(request: {name: "renamed"}) { ok } }`,
			wantSet: true,
			want:    "renamed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := execOptional(t, tc.query, tc.vars).Name

			if got.Set != tc.wantSet {
				t.Fatalf("Set = %v, want %v — the present/absent distinction is gone, "+
					"which makes this field a full replace", got.Set, tc.wantSet)
			}
			if !tc.wantSet {
				return
			}
			if tc.wantNil {
				if got.Value != nil {
					t.Fatalf("an explicit null produced value %q; null must clear, not set", *got.Value)
				}
				return
			}
			if got.Value == nil {
				t.Fatal("a sent value arrived as nil, which would clear the field instead of setting it")
			}
			if *got.Value != tc.want {
				t.Fatalf("Value = %q, want %q", *got.Value, tc.want)
			}
		})
	}
}

// The same three states for every other scalar. Int and Float matter more than they
// look: the packer hands an unmarshaler the value the decoder produced WITHOUT
// running the library's own scalar coercion, so a literal and a variable arrive as
// different Go types for the same field.
func TestOptionalScalarsCarryThreeStates(t *testing.T) {
	t.Run("absent leaves every field unset", func(t *testing.T) {
		got := execOptional(t, optMutationVar, map[string]any{"r": map[string]any{}})
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"Name", got.Name.Set},
			{"Enabled", got.Enabled.Set},
			{"Count", got.Count.Set},
			{"Ratio", got.Ratio.Set},
			{"Ref", got.Ref.Set},
		} {
			if f.set {
				t.Errorf("%s.Set is true for a field the request never mentioned", f.name)
			}
		}
		if got.Plain != nil {
			t.Error("the plain *string control was populated from an empty request")
		}
	})

	t.Run("values arrive intact, variable", func(t *testing.T) {
		got := execOptional(t, optMutationVar, map[string]any{"r": map[string]any{
			"enabled": true, "count": 42, "ratio": 1.5, "ref": "abc", "plain": "p",
		}})
		if !got.Enabled.Set || got.Enabled.Value == nil || *got.Enabled.Value != true {
			t.Errorf("Boolean did not round-trip: %+v", got.Enabled)
		}
		if !got.Count.Set || got.Count.Value == nil || *got.Count.Value != 42 {
			t.Errorf("Int did not round-trip: %+v", got.Count)
		}
		if !got.Ratio.Set || got.Ratio.Value == nil || *got.Ratio.Value != 1.5 {
			t.Errorf("Float did not round-trip: %+v", got.Ratio)
		}
		if !got.Ref.Set || got.Ref.Value == nil || *got.Ref.Value != graphql.ID("abc") {
			t.Errorf("ID did not round-trip: %+v", got.Ref)
		}
		if got.Plain == nil || *got.Plain != "p" {
			t.Errorf("the plain *string control did not round-trip: %v", got.Plain)
		}
	})

	t.Run("values arrive intact, literal", func(t *testing.T) {
		got := execOptional(t,
			`mutation { updateThing(request: {enabled: true, count: 42, ratio: 1.5, ref: "abc"}) { ok } }`, nil)
		if !got.Enabled.Set || got.Enabled.Value == nil || *got.Enabled.Value != true {
			t.Errorf("Boolean did not round-trip from a literal: %+v", got.Enabled)
		}
		if !got.Count.Set || got.Count.Value == nil || *got.Count.Value != 42 {
			t.Errorf("Int did not round-trip from a literal: %+v", got.Count)
		}
		if !got.Ratio.Set || got.Ratio.Value == nil || *got.Ratio.Value != 1.5 {
			t.Errorf("Float did not round-trip from a literal: %+v", got.Ratio)
		}
		if !got.Ref.Set || got.Ref.Value == nil || *got.Ref.Value != graphql.ID("abc") {
			t.Errorf("ID did not round-trip from a literal: %+v", got.Ref)
		}
	})

	t.Run("explicit null clears every field", func(t *testing.T) {
		got := execOptional(t, optMutationVar, map[string]any{"r": map[string]any{
			"enabled": nil, "count": nil, "ratio": nil, "ref": nil,
		}})
		for _, f := range []struct {
			name  string
			set   bool
			isNil bool
		}{
			{"Enabled", got.Enabled.Set, got.Enabled.Value == nil},
			{"Count", got.Count.Set, got.Count.Value == nil},
			{"Ratio", got.Ratio.Set, got.Ratio.Value == nil},
			{"Ref", got.Ref.Set, got.Ref.Value == nil},
		} {
			if !f.set {
				t.Errorf("%s: an explicit null was not seen as present, so it would not clear", f.name)
			}
			if !f.isNil {
				t.Errorf("%s: an explicit null produced a value", f.name)
			}
		}
	})
}

// 🔴 NEGATIVE CONTROL for the marker method.
//
// Nullable() is a no-op method with no body, which makes it exactly the kind of
// thing a later reader deletes as dead code, or forgets when adding OptionalTime.
// The failure is loud but its message names neither the field nor the marker, so
// this test exists to convert "schema fails to build with an inexplicable error"
// into a named, explained failure.
//
// It also proves the marker is load-bearing at all. Without this control the three
// Nullable() methods above are unfalsifiable decoration — the state tests would
// pass identically whether or not the marker did anything.
func TestMissingNullableMarkerFailsSchemaConstruction(t *testing.T) {
	_, err := graphql.ParseSchema(unmarkedSchema, &unmarkedResolver{})
	if err == nil {
		t.Fatal("a nullable field held in a non-pointer type WITHOUT the Nullable() marker " +
			"built successfully — either the library changed, or the marker on the Optional* " +
			"types is no longer doing anything and an explicit null will stop clearing")
	}
	if !strings.Contains(err.Error(), "not a pointer or a nullable type") {
		t.Fatalf("schema construction failed for an unexpected reason, so this control is "+
			"no longer measuring the marker: %v", err)
	}
}

const unmarkedSchema = `
schema { query: Query  mutation: Mutation }
type Query { ping: String! }
type Mutation { updateThing(request: UnmarkedRequest!): Boolean! }
input UnmarkedRequest { name: String }
`

// Identical to OptionalString in every respect EXCEPT the Nullable() marker. That
// is what makes this a control rather than a second test: the only difference
// between building and not building is the method under test.
type unmarkedOptionalString struct {
	Set   bool
	Value *string
}

func (unmarkedOptionalString) ImplementsGraphQLType(name string) bool { return name == "String" }

func (o *unmarkedOptionalString) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	s, _ := input.(string)
	o.Value = &s
	return nil
}

type unmarkedResolver struct{}

func (r *unmarkedResolver) Ping() string { return "pong" }
func (r *unmarkedResolver) UpdateThing(ctx context.Context, args struct {
	Request struct{ Name unmarkedOptionalString }
}) (bool, error) {
	return true, nil
}

// 🔴 NEGATIVE CONTROL for the SDL-default trap.
//
// An SDL default on an optional field destroys the absent state, and does it
// silently — the schema builds, every request succeeds, and absent starts arriving
// as Set=true carrying the default. That is the original full-replace bug wearing
// the new types.
//
// Two things in the library conspire. makeStructPacker rewrites a defaulted field's
// type to NonNull, which skips the isNullable branch the marker exists to reach;
// and StructPacker.Pack seeds its result with defaultStruct, so the default is
// written into the field before the present/absent loop runs at all.
//
// This test pins the BAD behaviour on purpose. It is the cheapest way to make the
// rule falsifiable: if a future library version fixes this, the test fails and the
// prohibition in optional.go can be relaxed deliberately rather than discovered.
func TestSDLDefaultDefeatsTheAbsentState(t *testing.T) {
	r := &defaultedResolver{}
	schema := MustParseSchema(defaultedSchema, r)
	resp := schema.Exec(context.Background(), `mutation { updateThing(request: {}) { ok } }`, "", nil)
	if len(resp.Errors) > 0 {
		t.Fatalf("unexpected GraphQL error: %v", resp.Errors)
	}

	if !r.last.Name.Set {
		t.Skip("an SDL default no longer populates an absent optional field — the library " +
			"behaviour this control pins has changed, and the prohibition in optional.go " +
			"against SDL defaults on optional fields can be revisited")
	}
	if r.last.Name.Value == nil || *r.last.Name.Value != "fallback" {
		t.Fatalf("absent field arrived Set with %v, expected the SDL default; the trap this "+
			"control documents has changed shape and optional.go's comment is now wrong",
			r.last.Name.Value)
	}
	// Reaching here IS the documented failure: a field the caller never mentioned is
	// indistinguishable from one they sent explicitly as "fallback".
}

const defaultedSchema = `
schema { query: Query  mutation: Mutation }
type Query { ping: String! }
type Mutation { updateThing(request: DefaultedRequest!): Thing! }
input DefaultedRequest { name: String = "fallback" }
type Thing { ok: Boolean! }
`

type defaultedResolver struct {
	last struct{ Name OptionalString }
}

func (r *defaultedResolver) Ping() string { return "pong" }
func (r *defaultedResolver) UpdateThing(ctx context.Context, args struct {
	Request struct{ Name OptionalString }
}) (*optThing, error) {
	r.last = args.Request
	return &optThing{}, nil
}

// ApplyTo is where the three states become one stored value, so it gets its own
// table — the resolvers will contain nothing but calls to it, and a wrong branch
// here is a data-loss bug at every call site simultaneously.
func TestApplyTo(t *testing.T) {
	existing := "before"
	replacement := "after"

	t.Run("string", func(t *testing.T) {
		cases := []struct {
			name string
			opt  OptionalString
			want *string
		}{
			{"absent leaves the stored value", OptionalString{}, &existing},
			{"null clears", OptionalString{Set: true}, nil},
			{"value replaces", OptionalString{Set: true, Value: &replacement}, &replacement},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := tc.opt.ApplyTo(&existing)
				switch {
				case tc.want == nil && got != nil:
					t.Fatalf("ApplyTo = %q, want nil", *got)
				case tc.want != nil && got == nil:
					t.Fatalf("ApplyTo = nil, want %q", *tc.want)
				case tc.want != nil && *got != *tc.want:
					t.Fatalf("ApplyTo = %q, want %q", *got, *tc.want)
				}
			})
		}
	})

	t.Run("the other scalars fold the same three states", func(t *testing.T) {
		yes, no := true, false
		if got := (OptionalBool{}).ApplyTo(&yes); got == nil || *got != true {
			t.Fatalf("an absent Boolean changed the stored value to %v", got)
		}
		if got := (OptionalBool{Set: true}).ApplyTo(&yes); got != nil {
			t.Fatalf("a null Boolean did not clear: %v", *got)
		}
		if got := (OptionalBool{Set: true, Value: &no}).ApplyTo(&yes); got == nil || *got != false {
			t.Fatalf("a Boolean value did not replace: %v", got)
		}

		seven, eight := int32(7), int32(8)
		if got := (OptionalInt32{}).ApplyTo(&seven); got == nil || *got != 7 {
			t.Fatalf("an absent Int changed the stored value to %v", got)
		}
		if got := (OptionalInt32{Set: true}).ApplyTo(&seven); got != nil {
			t.Fatalf("a null Int did not clear: %d", *got)
		}
		if got := (OptionalInt32{Set: true, Value: &eight}).ApplyTo(&seven); got == nil || *got != 8 {
			t.Fatalf("an Int value did not replace: %v", got)
		}

		half := 1.5
		if got := (OptionalFloat64{}).ApplyTo(&half); got == nil || *got != 1.5 {
			t.Fatalf("an absent Float changed the stored value to %v", got)
		}
		if got := (OptionalFloat64{Set: true}).ApplyTo(&half); got != nil {
			t.Fatalf("a null Float did not clear: %v", *got)
		}
	})
}

// 🔴 ApplyToValue IS GONE, AND NOTHING MAY QUIETLY GROW IT BACK.
//
// It folded an explicit null to the zero value for a non-pointer column. Every call
// site that reached for it was a NOT NULL column, where that fold ACCEPTS "clear this
// required field" and writes a value the create path would have refused — invisibly on
// a Boolean, where the false it writes is a value the caller could have sent.
// ApplyToRequired refuses instead, and the method was deleted rather than documented,
// because a fold nobody can reach cannot be reached by accident.
//
// This asks the TYPE rather than trusting the deletion to stay done: a method added
// back under that name, on any of the four, fails here on the day it is added. The
// method set is the thing being asserted, so a reflection check is the assertion —
// there is no behaviour left to drive.
func TestApplyToValueHasNotComeBack(t *testing.T) {
	for _, v := range []any{OptionalString{}, OptionalBool{}, OptionalInt32{}, OptionalFloat64{}, OptionalID{}} {
		rt := reflect.TypeOf(v)
		if _, found := rt.MethodByName("ApplyToValue"); found {
			t.Errorf("%s has an ApplyToValue again: folding a null to the zero value accepts "+
				"\"clear this required field\" and writes a value the create path refuses. Use "+
				"ApplyToRequired, which says no.", rt.Name())
		}
		// The counterweight. A reflection check over a type whose methods have all been
		// renamed would report the absence above for the wrong reason.
		if _, found := rt.MethodByName("ApplyTo"); !found {
			t.Errorf("%s has no ApplyTo, so the absence of ApplyToValue proves nothing about "+
				"this check", rt.Name())
		}
	}
}

// The counterweight, mirroring TestValidInputIsNotRejected: the optional types must
// not weaken the fork's unknown-field rejection. An update input is precisely where
// a silently-dropped misnamed field is most damaging — the caller believes they
// changed something and the entity is returned unchanged, with a 200.
func TestOptionalInputStillRejectsUnknownFields(t *testing.T) {
	schema := MustParseSchema(optionalSchema, &optResolver{})
	resp := schema.Exec(context.Background(), optMutationVar, "",
		map[string]any{"r": map[string]any{"name": "n", "nmae": "typo"}})
	if len(resp.Errors) == 0 {
		t.Fatal("a misnamed field on an update input was silently dropped; the caller " +
			"would be told the update succeeded")
	}
	if !strings.Contains(resp.Errors[0].Message, "nmae") {
		t.Fatalf("error should name the offending field: %q", resp.Errors[0].Message)
	}
}
