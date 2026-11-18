// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package inference

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	"github.com/devicechain-io/dc-microservice/svcclient"
	graphql "github.com/graph-gophers/graphql-go"
)

// The target SDL is outside this module; Go's test cache does not track it.
// Run these contract tests with -count=1.
type documentCall struct {
	target string
	query  string
	vars   map[string]any
}
type documentRecorder struct{ calls []documentCall }

func validateDocuments(t *testing.T, calls []documentCall, floors map[string]int) {
	t.Helper()
	for target, floor := range floors {
		t.Run(target, func(t *testing.T) {
			dir := filepath.Join("..", "..", target, "graphql")
			sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
			if err != nil || !served {
				t.Fatalf("target SDL %s: served=%v err=%v", dir, served, err)
			}
			schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers(), graphql.MaxDepth(30), graphql.MaxQueryLength(100000))
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			var recorded documentCall
			for _, c := range calls {
				if c.target != target {
					continue
				}
				seen[c.query] = true
				recorded = c
				if errs := schema.ValidateWithVariables(c.query, c.vars); len(errs) != 0 {
					t.Errorf("%s: %v", c.query, errs)
				}
			}
			if len(seen) < floor {
				t.Fatalf("recorded %d distinct operations, want at least %d", len(seen), floor)
			}
			// NEGATIVE CONTROL: corrupt a selection in an ACTUALLY recorded document.
			field := regexp.MustCompile(`\{\s*(?:\w+\s*:\s*)?(\w+)`).FindStringSubmatchIndex(recorded.query)
			if field == nil {
				t.Fatal("recorded document has no selection")
			}
			name := recorded.query[field[2]:field[3]] + "Misspelled"
			bad := recorded.query[:field[2]] + name + recorded.query[field[3]:]
			errs := schema.ValidateWithVariables(bad, recorded.vars)
			if len(errs) != 1 || !strings.Contains(errs[0].Message, `Cannot query field "`+name+`"`) || errs[0].Rule != "FieldsOnCorrectTypeRule" {
				t.Fatalf("misspelled selection must fail FieldsOnCorrectTypeRule: %v", errs)
			}
		})
	}
	for _, c := range calls {
		if _, ok := floors[c.target]; !ok {
			t.Errorf("unvalidated target %q", c.target)
		}
	}
}

func TestFactsQuerySelectsTheContractFields(t *testing.T) {
	ctx := context.Background()
	r := &documentRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path != "/graphql" {
			json.NewEncoder(w).Encode(map[string]any{"token": "test-token", "expiresAt": time.Now().Add(time.Hour).Unix()})
			return
		}
		var wire struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(req.Body).Decode(&wire); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		r.calls = append(r.calls, documentCall{"user-management", wire.Query, wire.Variables})
		w.Write([]byte(`{"data":{"tenantGovernance":{"aiExternalEnabled":true,"tierToken":"gold"}}}`))
	}))
	defer server.Close()
	addr := server.Listener.Addr().(*net.TCPAddr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: "127.0.0.1", InternalPort: uint32(addr.Port)}, "secret", "inference", nil)
	if _, err := NewServiceTenantFactsReader(client, server.URL+"/graphql").Facts(ctx, "tenant"); err != nil {
		t.Fatal(err)
	}
	validateDocuments(t, r.calls, map[string]int{"user-management": 1})
}
