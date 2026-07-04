// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The dev-tools flag is secure by default: unset or false → disabled, an explicit
// truthy value → enabled, and a set-but-unparseable value fails closed (disabled).
func TestDevToolsEnabled(t *testing.T) {
	cases := []struct {
		val  string
		set  bool
		want bool
	}{
		{"", false, false},     // unset → secure default
		{"", true, false},      // empty → disabled
		{"true", true, true},   // explicit enable
		{"1", true, true},      // strconv truthy
		{"false", true, false}, // explicit disable
		{"0", true, false},     // strconv falsy
		{"yes", true, false},   // unparseable → fail closed
		{"garbage", true, false},
	}
	for _, tc := range cases {
		if tc.set {
			t.Setenv(EnvGraphQLDevTools, tc.val)
		} else {
			// t.Setenv requires a value; emulate "unset" by clearing.
			t.Setenv(EnvGraphQLDevTools, "")
		}
		if got := DevToolsEnabled(); got != tc.want {
			t.Errorf("DevToolsEnabled() with %q(set=%v) = %v, want %v", tc.val, tc.set, got, tc.want)
		}
	}
}

type introspectRoot struct{}

func (introspectRoot) Hello() string { return "hi" }

// MustParseSchema disables introspection unless dev tools are on: with the flag off
// (prod default) an `__schema` query is rejected; with it on the same query works.
func TestMustParseSchemaGatesIntrospection(t *testing.T) {
	const sdl = `schema { query: Query } type Query { hello: String! }`
	const introspection = `{ __schema { queryType { name } } }`

	// The security property: with dev tools off, an introspection query leaks no
	// schema data (the query type name never appears in the response); with it on,
	// the same query returns it.
	t.Run("disabled by default leaks no schema", func(t *testing.T) {
		t.Setenv(EnvGraphQLDevTools, "false")
		schema := MustParseSchema(sdl, &introspectRoot{})
		resp := schema.Exec(context.Background(), introspection, "", nil)
		if strings.Contains(mustJSON(resp.Data), "Query") {
			t.Errorf("introspection leaked schema with dev tools off: %s", mustJSON(resp.Data))
		}
	})

	t.Run("enabled exposes schema", func(t *testing.T) {
		t.Setenv(EnvGraphQLDevTools, "true")
		schema := MustParseSchema(sdl, &introspectRoot{})
		resp := schema.Exec(context.Background(), introspection, "", nil)
		if !strings.Contains(mustJSON(resp.Data), "Query") {
			t.Errorf("introspection did not expose schema with dev tools on: data=%s errs=%v", mustJSON(resp.Data), resp.Errors)
		}
	})
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
