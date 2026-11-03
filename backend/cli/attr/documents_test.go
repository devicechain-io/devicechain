// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package attr

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// EVERY DOCUMENT THIS PACKAGE SENDS IS A STRING, and the schema it must satisfy lives in
// another module. The fake-client tests above drive canned responses, never a schema, so
// until this file nothing read these documents except a real cluster.
//
// The documents and variables validated here are RECORDED from the real Set/Get/Unset
// code paths rather than written out again, so what is checked is the exact map the
// server would receive. The variables matter: graphql-go rejects an input-object entry the
// schema does not define when it arrives through a variable.
//
// The SDL lives outside this module and Go's test cache does not track it; CI runs with
// -count=1.

// recorder answers every call with an empty object and keeps what it was sent.
type recorder struct{ calls []call }

func (r *recorder) Query(_ context.Context, _ string, q string, v map[string]any, _ any) error {
	r.calls = append(r.calls, call{query: q, vars: v})
	return nil
}

func servedSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil {
		t.Fatalf("classify %s: %v", dir, err)
	}
	if !served {
		t.Fatalf("no tenant-plane schema found under %s", dir)
	}
	schema, err := graphql.ParseSchema(sdl, nil,
		graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return schema
}

func recordAll(t *testing.T) []call {
	t.Helper()
	r := &recorder{}
	ctx := context.Background()
	// An undeclared key with --type skips nothing: SHARED scope still looks the facet up.
	if _, err := Set(ctx, r, SetOptions{Target: target(), Key: "k", Value: "v", ValueType: "STRING"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ctx, r, GetOptions{Target: target(), Key: "k", Scope: "SHARED"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Unset(ctx, r, UnsetOptions{Target: target(), Key: "k"}); err != nil {
		t.Fatal(err)
	}
	return r.calls
}

func TestEveryDocumentValidatesAgainstTheServedSchema(t *testing.T) {
	schema := servedSchema(t)
	calls := recordAll(t)
	// facetKeys, set, get, unset.
	if len(calls) != 4 {
		t.Fatalf("recorded %d calls, want the 4 documents", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.query] = true
		if errs := schema.ValidateWithVariables(c.query, c.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against the served schema: %v", c.query, errs)
		}
	}
	for _, d := range []string{setMutation, unsetMutation, getQuery, facetKeysQuery} {
		if !seen[d] {
			t.Errorf("document never sent by the code paths under test: %s", d)
		}
	}
}

// NEGATIVE CONTROL: the validator must reject a misnamed field and a misnamed input key,
// and the rejection must be about that name, not about something incidental.
func TestValidatorRejectsMisnamedFieldsAndVariables(t *testing.T) {
	schema := servedSchema(t)
	calls := recordAll(t)
	var set call
	for _, c := range calls {
		if c.query == setMutation {
			set = c
		}
	}

	bad := strings.Replace(setMutation, "attrKey valueType", "attrKeyX valueType", 1)
	if errs := schema.ValidateWithVariables(bad, set.vars); len(errs) == 0 || !strings.Contains(errs[0].Message, "attrKeyX") {
		t.Errorf("a misnamed selection field must be rejected by name, got %v", errs)
	}

	req := map[string]any{}
	for k, v := range set.vars["request"].(map[string]any) {
		req[k] = v
	}
	delete(req, "attrKey")
	req["attributeKey"] = "k"
	errs := schema.ValidateWithVariables(setMutation, map[string]any{"request": req})
	if len(errs) == 0 || !strings.Contains(errs[0].Message, "attributeKey") {
		t.Errorf("a misnamed input key must be rejected by name, got %v", errs)
	}
}
