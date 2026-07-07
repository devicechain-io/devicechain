// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The depth/length ceilings are fail-safe: unset uses the secure default, a valid
// positive override is honored, and anything that would weaken or disable a ceiling
// (unparseable, zero, negative) falls back to the default.
func TestEnvPositiveIntFailSafe(t *testing.T) {
	const env = "DC_GRAPHQL_TEST_LIMIT"
	const def = 15
	cases := []struct {
		val  string
		want int
	}{
		{"", def},        // unset → secure default
		{"25", 25},       // valid override (upward)
		{"3", 3},         // valid override (downward, still positive)
		{"0", def},       // would disable the ceiling → default
		{"-5", def},      // negative → default
		{"garbage", def}, // unparseable → default
		{"1.5", def},     // non-integer → default
	}
	for _, tc := range cases {
		t.Setenv(env, tc.val)
		if got := envPositiveInt(env, def); got != tc.want {
			t.Errorf("envPositiveInt(%q) = %d, want %d", tc.val, got, tc.want)
		}
	}
}

// The default ceiling must clear the canonical introspection query's depth (~13),
// or /graphiql breaks when dev tools are enabled.
func TestDefaultDepthClearsIntrospection(t *testing.T) {
	if DefaultGraphQLMaxDepth < 13 {
		t.Fatalf("DefaultGraphQLMaxDepth=%d is below the introspection query depth (~13); /graphiql would break with dev tools on", DefaultGraphQLMaxDepth)
	}
}

// Pin the shipped defaults so a future edit that silently loosens or tightens a
// ceiling has to change this test on purpose.
func TestDefaultConstants(t *testing.T) {
	if DefaultGraphQLMaxDepth != 15 {
		t.Errorf("DefaultGraphQLMaxDepth = %d, want 15", DefaultGraphQLMaxDepth)
	}
	if DefaultGraphQLMaxQueryLength != 100_000 {
		t.Errorf("DefaultGraphQLMaxQueryLength = %d, want 100000", DefaultGraphQLMaxQueryLength)
	}
	if DefaultGraphQLMaxBodyBytes != 4<<20 {
		t.Errorf("DefaultGraphQLMaxBodyBytes = %d, want %d", DefaultGraphQLMaxBodyBytes, 4<<20)
	}
}

// nestRoot is a self-referential type so a test query can nest arbitrarily deep.
type nestRoot struct{}

func (nestRoot) Self() *nestRoot { return &nestRoot{} }
func (nestRoot) Name() string    { return "leaf" }

const nestSDL = `schema { query: Query } type Query { self: Query! name: String! }`

// query builds `{ self { self { ... name } } }` nested to the given depth: depth 1
// selects name at the top level, depth n nests (n-1) `self` levels before name.
func nestedQuery(depth int) string {
	var b strings.Builder
	for i := 1; i < depth; i++ {
		b.WriteString("self { ")
	}
	b.WriteString("name")
	for i := 1; i < depth; i++ {
		b.WriteString(" }")
	}
	return "{ " + b.String() + " }"
}

// MustParseSchema enforces the depth ceiling: a query at the limit executes, one
// past it is rejected before execution.
func TestMustParseSchemaEnforcesMaxDepth(t *testing.T) {
	t.Setenv(EnvGraphQLMaxDepth, "5")
	schema := MustParseSchema(nestSDL, &nestRoot{})

	if resp := schema.Exec(context.Background(), nestedQuery(5), "", nil); len(resp.Errors) != 0 {
		t.Errorf("depth 5 (at the limit) was rejected: %v", resp.Errors)
	}
	resp := schema.Exec(context.Background(), nestedQuery(6), "", nil)
	if len(resp.Errors) == 0 {
		t.Fatalf("depth 6 (over the limit) was not rejected")
	}
	if !strings.Contains(fmt.Sprint(resp.Errors), "depth") {
		t.Errorf("expected a depth error, got: %v", resp.Errors)
	}
}

// MustParseSchema enforces the query-length ceiling: an over-length document is
// rejected before it is parsed.
func TestMustParseSchemaEnforcesMaxQueryLength(t *testing.T) {
	t.Setenv(EnvGraphQLMaxQueryLength, "64")
	schema := MustParseSchema(nestSDL, &nestRoot{})

	if resp := schema.Exec(context.Background(), "{ name }", "", nil); len(resp.Errors) != 0 {
		t.Errorf("a short query was rejected under a 64-byte limit: %v", resp.Errors)
	}
	// A query padded past 64 bytes with a comment is rejected on length alone.
	long := "{ name } # " + strings.Repeat("x", 100)
	resp := schema.Exec(context.Background(), long, "", nil)
	if len(resp.Errors) == 0 {
		t.Fatalf("an over-length query was not rejected")
	}
	if !strings.Contains(fmt.Sprint(resp.Errors), "query length") {
		t.Errorf("expected a query-length error, got: %v", resp.Errors)
	}
}
