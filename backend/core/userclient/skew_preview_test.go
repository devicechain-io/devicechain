// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	graphql "github.com/graph-gophers/graphql-go"
)

const skewPreviewSchema = `
type Query {
    item: Item!
    echo(value: String): String
    choose(state: Status): String
    """(Preview: may change in a minor release.) An experimental operation."""
    experiment: String
}

type Item {
    name: String!
    agent: Boolean
    """(Preview: may change in a minor release.) An experimental selection."""
    modern: String
}
input Request {
    name: String
    """(Preview: may change in a minor release.) Experimental authoring."""
    agent: Boolean
}
type Mutation { save(request: Request!): Item! }
enum Status {
    STABLE
    """(Preview: may change in a minor release.) Experimental input value."""
    EXPERIMENTAL
}
"""(Preview: may change in a minor release.) Not part of the stable contract."""
type Experimental {
    """(Preview: may change in a minor release.) A common name on this type."""
    name: String
}
`

func TestLegacyPreviewRequiresDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		variables  map[string]any
		discovered bool
		refuse     bool
	}{
		{name: "preview operation", query: `{experiment}`, refuse: true},
		{name: "preview selection", query: `{item{modern}}`, refuse: true},
		{name: "preview input even when false", query: `mutation($r:Request!){save(request:$r){name}}`, variables: map[string]any{"r": map[string]any{"agent": false}}, refuse: true},
		{name: "inline preview input", query: `mutation{save(request:{agent:false}){name}}`, refuse: true},
		{name: "preview enum input", query: `query($s:Status){choose(state:$s)}`, variables: map[string]any{"s": "EXPERIMENTAL"}, refuse: true},
		{name: "stable enum input", query: `query($s:Status){choose(state:$s)}`, variables: map[string]any{"s": "STABLE"}},
		{name: "covered output sharing an input name", query: `{item{agent}}`},
		{name: "common stable field", query: `{item{name}}`},
		{name: "comment is not a selection", query: "# modern\n{item{name}}"},
		{name: "string is not a selection", query: `{echo(value:"modern experiment agent")}`},
		{name: "block string is not a selection", query: `{echo(value:"""modern experiment agent""")}`},
		{name: "discovered preview is supported", query: `{experiment}`, discovered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodOptions {
					if tc.discovered {
						_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": skewPreviewSchema})
					} else {
						w.WriteHeader(http.StatusBadRequest)
					}
					return
				}
				calls++
				_, _ = w.Write([]byte(`{"data":{}}`))
			}))
			defer srv.Close()
			s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(func(string) string { return skewPreviewSchema })
			s.WithLegacyEndpointVersion(srv.URL, "v1.7.0")
			s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
			err := s.Query(context.Background(), srv.URL, tc.query, tc.variables, nil)
			if tc.refuse {
				var unsupported *UnsupportedFeatureError
				if !errors.As(err, &unsupported) || calls != 0 {
					t.Fatalf("unconfirmed preview reached instance: err=%v calls=%d", err, calls)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("stable/discovered operation refused: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestLegacyPreviewValuesUseDeclaredInputTypes(t *testing.T) {
	sdl := skewPreviewSchema + `
input StableInput { agent: String states: [StableStatus!] children: [StableInput!] }
input TypedInput { text: String status: Status statuses: [Status!] children: [TypedInput!] }
enum StableStatus { STABLE EXPERIMENTAL }
`
	sdl = strings.Replace(sdl, "choose(state: Status): String", "choose(state: Status): String stable(input: StableInput): String typed(input: TypedInput): String texts(values: [String!]): String stableEnum(value: StableStatus): String", 1)
	contract := graphql.MustParseSchema(sdl, nil, graphql.UseStringDescriptions())
	for _, tc := range []struct {
		name, query string
		variables   map[string]any
		refuse      bool
	}{
		{"string", `query($v:String){echo(value:$v)}`, map[string]any{"v": "EXPERIMENTAL"}, false},
		{"string list", `query($v:[String!]){texts(values:$v)}`, map[string]any{"v": []any{"EXPERIMENTAL"}}, false},
		{"nested string", `query($v:TypedInput){typed(input:$v)}`, map[string]any{"v": map[string]any{"children": []any{map[string]any{"text": "EXPERIMENTAL"}}}}, false},
		{"unrelated input and enum", `query($v:StableInput){stable(input:$v)}`, map[string]any{"v": map[string]any{"agent": "EXPERIMENTAL", "states": []any{"EXPERIMENTAL"}}}, false},
		{"unrelated enum", `query($v:StableStatus){stableEnum(value:$v)}`, map[string]any{"v": "EXPERIMENTAL"}, false},
		{"preview enum", `query($v:Status){choose(state:$v)}`, map[string]any{"v": "EXPERIMENTAL"}, true},
		{"nested preview enum", `query($v:TypedInput){typed(input:$v)}`, map[string]any{"v": map[string]any{"children": []any{map[string]any{"status": "EXPERIMENTAL"}}}}, true},
		{"preview enum list", `query($v:TypedInput){typed(input:$v)}`, map[string]any{"v": map[string]any{"statuses": []any{"EXPERIMENTAL"}}}, true},
		{"preview enum singleton coercion", `query($v:TypedInput){typed(input:$v)}`, map[string]any{"v": map[string]any{"statuses": "EXPERIMENTAL"}}, true},
		{"null", `query($v:TypedInput){typed(input:$v)}`, map[string]any{"v": nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errs := contract.ValidateWithVariables(tc.query, tc.variables); len(errs) != 0 {
				t.Fatalf("invalid test operation: %v", errs)
			}
			if name := legacyPreviewCapability(contract, tc.query, tc.variables); (name != "") != tc.refuse {
				t.Fatalf("preview capability=%q, want refusal=%t", name, tc.refuse)
			}
		})
	}
}
