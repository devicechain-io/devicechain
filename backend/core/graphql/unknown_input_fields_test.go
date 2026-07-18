// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"
)

// This schema mirrors the shapes our services actually declare: a required input
// object, a nested input object, and a list of input objects. Each is a distinct
// code path in the validator, and the nested/list cases are the ones a top-level-only
// check would silently miss.
const strictInputSchema = `
schema { query: Query  mutation: Mutation }
type Query { ping: String! }
type Mutation {
  createThing(request: ThingRequest!): Thing!
  createThings(requests: [ThingRequest!]!): Thing!
}
input ThingRequest {
  token: String!
  name: String
  profileToken: String
  meta: MetaRequest
}
input MetaRequest { label: String }
type Thing { token: String! }
`

type strictResolver struct{}

func (r *strictResolver) Ping() string { return "pong" }

type strictThing struct{ token string }

func (t *strictThing) Token() string { return t.token }

type strictMeta struct{ Label *string }

type strictRequest struct {
	Token        string
	Name         *string
	ProfileToken *string
	Meta         *strictMeta
}

func (r *strictResolver) CreateThing(ctx context.Context, args struct{ Request strictRequest }) (*strictThing, error) {
	return &strictThing{token: args.Request.Token}, nil
}

func (r *strictResolver) CreateThings(ctx context.Context, args struct{ Requests []strictRequest }) (*strictThing, error) {
	return &strictThing{token: args.Requests[0].Token}, nil
}

// An input field the schema does not declare must be REJECTED, not silently dropped.
//
// This is a fail-closed guarantee, and it is load-bearing well beyond typo comfort: a
// field that vanishes is indistinguishable from one that was applied, so a misnamed
// field yields a success response and a half-configured entity. It bit us for real —
// sending "deviceProfileToken" instead of "profileToken" to createDeviceType returned
// success and created a device type with NO profile, which left its devices with no
// command vocabulary and made a correct enqueue gate look broken.
//
// The literal path was always enforced by graph-gophers/graphql-go. The VARIABLE path
// was not (its validator walked the schema's declared fields and looked each up in the
// supplied map, so undefined entries were never visited), and every real client —
// console, SDKs, dcctl, codegen — sends variables. We carry a patched graphql-go via a
// replace directive until the upstream fix lands; if that replace is ever dropped, the
// variable subtests below fail. That is the point of them.
func TestUnknownInputFieldIsRejected(t *testing.T) {
	schema := MustParseSchema(strictInputSchema, &strictResolver{})

	cases := []struct {
		name  string
		query string
		vars  map[string]any
	}{
		{
			name:  "literal, top level",
			query: `mutation { createThing(request: {token: "t", bogus: "x"}) { token } }`,
		},
		{
			name:  "variable, top level",
			query: `mutation ($r: ThingRequest!) { createThing(request: $r) { token } }`,
			vars:  map[string]any{"r": map[string]any{"token": "t", "bogus": "x"}},
		},
		{
			// A nested input object is a separate recursion step; a check applied only
			// to the outermost object would pass this.
			name:  "variable, nested input object",
			query: `mutation ($r: ThingRequest!) { createThing(request: $r) { token } }`,
			vars: map[string]any{"r": map[string]any{
				"token": "t",
				"meta":  map[string]any{"label": "l", "bogus": "x"},
			}},
		},
		{
			// List elements are validated one at a time; an unknown field in a
			// non-leading element is the easiest to miss.
			name:  "variable, inside a list element",
			query: `mutation ($rs: [ThingRequest!]!) { createThings(requests: $rs) { token } }`,
			vars: map[string]any{"rs": []any{
				map[string]any{"token": "a"},
				map[string]any{"token": "b", "bogus": "x"},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := schema.Exec(context.Background(), tc.query, "", tc.vars)
			if len(resp.Errors) == 0 {
				t.Fatalf("an undeclared input field was accepted (fail-open); response data: %s", resp.Data)
			}
			if !strings.Contains(resp.Errors[0].Message, "bogus") {
				t.Fatalf("error should name the offending field: %q", resp.Errors[0].Message)
			}
		})
	}
}

// The counterweight to the test above. Rejecting unknown fields is only safe if
// well-formed input still passes untouched — a validator that over-rejects would
// break every client at once, which is a far worse failure than the one being fixed.
// Omitted optional fields, explicit nulls, nested objects and lists must all survive.
func TestValidInputIsNotRejected(t *testing.T) {
	schema := MustParseSchema(strictInputSchema, &strictResolver{})

	cases := []struct {
		name  string
		query string
		vars  map[string]any
	}{
		{
			name:  "only required fields",
			query: `mutation ($r: ThingRequest!) { createThing(request: $r) { token } }`,
			vars:  map[string]any{"r": map[string]any{"token": "t"}},
		},
		{
			name:  "every declared field populated",
			query: `mutation ($r: ThingRequest!) { createThing(request: $r) { token } }`,
			vars: map[string]any{"r": map[string]any{
				"token": "t", "name": "n", "profileToken": "p",
				"meta": map[string]any{"label": "l"},
			}},
		},
		{
			// Codegen clients routinely send explicit nulls for absent optionals.
			// A null must not be mistaken for an undeclared entry.
			name:  "explicit nulls for optional fields",
			query: `mutation ($r: ThingRequest!) { createThing(request: $r) { token } }`,
			vars: map[string]any{"r": map[string]any{
				"token": "t", "name": nil, "profileToken": nil, "meta": nil,
			}},
		},
		{
			name:  "list of valid objects",
			query: `mutation ($rs: [ThingRequest!]!) { createThings(requests: $rs) { token } }`,
			vars: map[string]any{"rs": []any{
				map[string]any{"token": "a"},
				map[string]any{"token": "b", "name": "n"},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := schema.Exec(context.Background(), tc.query, "", tc.vars)
			if len(resp.Errors) != 0 {
				t.Fatalf("valid input was rejected — this would break every client: %v", resp.Errors)
			}
		})
	}
}

// The rejection should point at the intended field rather than just refusing. This is
// the exact near-miss that cost us a live debugging session, so pin that the
// suggestion actually names the field the caller meant.
func TestUnknownInputFieldSuggestsTheIntendedField(t *testing.T) {
	schema := MustParseSchema(strictInputSchema, &strictResolver{})

	resp := schema.Exec(context.Background(),
		`mutation ($r: ThingRequest!) { createThing(request: $r) { token } }`, "",
		map[string]any{"r": map[string]any{"token": "t", "deviceProfileToken": "p"}})

	if len(resp.Errors) == 0 {
		t.Fatal("a near-miss field name was accepted")
	}
	if !strings.Contains(resp.Errors[0].Message, "profileToken") {
		t.Fatalf("error should suggest the intended field: %q", resp.Errors[0].Message)
	}
}
