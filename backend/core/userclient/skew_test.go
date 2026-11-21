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

const skewOldSchema = `type Query { item: Item! }
type Item { name: String! }
input Child { name: String }
input Request { children: [Child!] }
type Mutation { save(request: Request!): Item! }`

const skewNewSchema = `type Query { item: Item! extra: String }
type Item { name: String! modern: String status: String }
input Child { name: String modern: Boolean }
input Request { children: [Child!] }
type Mutation { save(request: Request!): Item! }`

func TestSkewNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		schema    string
		query     string
		variables map[string]any
		refuse    bool
		legacy    bool
	}{
		{name: "older missing selected field", schema: skewOldSchema, query: `{item{name modern}}`, refuse: true},
		{name: "older missing operation", schema: skewOldSchema, query: `{extra}`, refuse: true},
		{name: "older nested variable field", schema: skewOldSchema, query: `mutation($r:Request!){save(request:$r){name}}`, variables: map[string]any{"r": map[string]any{"children": []any{map[string]any{"name": "kept", "modern": true}}}}, refuse: true},
		{name: "older inline input", schema: skewOldSchema, query: `mutation{save(request:{children:[{modern:true}]}){name}}`, refuse: true},
		{name: "newer field works", schema: skewNewSchema, query: `{item{name modern status}}`},
		{name: "newer nested input preserved", schema: skewNewSchema, query: `mutation($r:Request!){save(request:$r){name}}`, variables: map[string]any{"r": map[string]any{"children": []any{map[string]any{"name": "kept", "modern": true}}}}},
		{name: "legacy baseline works", schema: skewOldSchema, query: `{item{name}}`, legacy: true},
		{name: "legacy baseline refuses new field", schema: skewOldSchema, query: `{item{modern}}`, legacy: true, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contract := graphql.MustParseSchema(tc.schema, nil)
			calls, discoveries := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodOptions {
					discoveries++
					if r.Header.Get("Authorization") != "Bearer token" {
						t.Error("discovery lost the caller's bearer")
					}
					if tc.legacy {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"format": 1, "schema": tc.schema})
					return
				}
				calls++
				var body struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if errs := contract.ValidateWithVariables(body.Query, body.Variables); len(errs) != 0 {
					t.Errorf("unsupported request reached instance: %v", errs)
				}
				if tc.variables != nil {
					want, _ := json.Marshal(tc.variables)
					got, _ := json.Marshal(body.Variables)
					if string(want) != string(got) {
						t.Errorf("input changed: got %s, want %s", got, want)
					}
				}
				_, _ = w.Write([]byte(`{"data":{"item":{"name":"kept","modern":"yes","status":"FUTURE_ENUM","future":true}}}`))
			}))
			defer srv.Close()
			s := NewTenantSession(srv.Client(), srv.URL, "", "", "tenant").WithSchemaNegotiation(func(string) string { return skewOldSchema })
			s.access, s.expiresAt = "token", time.Now().Add(time.Hour)
			var out struct {
				Item struct{ Name, Modern, Status string }
			}
			err := s.Query(context.Background(), srv.URL, tc.query, tc.variables, &out)
			if tc.refuse {
				var unsupported *UnsupportedFeatureError
				if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "upgrade to v1.9.0") {
					t.Fatalf("expected actionable refusal, got %v", err)
				}
				if calls != 0 {
					t.Fatalf("sent %d unsupported operations", calls)
				}
			} else {
				if err != nil || calls != 1 || out.Item.Name != "kept" || out.Item.Status != "FUTURE_ENUM" {
					t.Fatalf("supported operation failed: err=%v calls=%d out=%+v", err, calls, out)
				}
			}
			if discoveries != 1 {
				t.Fatalf("expected discovery before operation; got %d", discoveries)
			}
		})
	}
}
