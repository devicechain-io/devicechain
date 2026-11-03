// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package failedevents

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// The documents this package sends are strings, and the schema they must satisfy lives in
// another module. The fake-client tests drive canned responses, never a schema, so these
// validate the documents with the exact variables the code paths send against
// event-management's served tenant SDL. The SDL is outside this module and Go's test cache
// does not track it; CI runs with -count=1.

type recordedCall struct {
	query string
	vars  map[string]any
}

type recorder struct{ calls []recordedCall }

func (r *recorder) Query(_ context.Context, _ string, q string, v map[string]any, _ any) error {
	r.calls = append(r.calls, recordedCall{q, v})
	return nil
}

func servedSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "event-management", "graphql")
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

func recordAll(t *testing.T) (list, summary recordedCall) {
	t.Helper()
	r := &recorder{}
	ctx := context.Background()
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	// Every filter set, so every criteria key is validated.
	if _, err := List(ctx, r, Options{Limit: 10, Reason: "DEVICE_NOT_FOUND", Device: "d1",
		Since: &since, Until: &until}); err != nil {
		t.Fatal(err)
	}
	if _, err := Summarize(ctx, r, "u", since); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(r.calls))
	}
	return r.calls[0], r.calls[1]
}

func TestEveryDocumentValidatesAgainstTheServedSchema(t *testing.T) {
	schema := servedSchema(t)
	list, summary := recordAll(t)
	if list.query != listQuery || summary.query != summaryQuery {
		t.Fatal("the code paths did not send the documents under test")
	}
	for _, c := range []recordedCall{list, summary} {
		if errs := schema.ValidateWithVariables(c.query, c.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against the served schema: %v", c.query, errs)
		}
	}
}

// NEGATIVE CONTROL: a misnamed field and a misnamed input key must be rejected, by name.
func TestValidatorRejectsMisnamedFieldsAndVariables(t *testing.T) {
	schema := servedSchema(t)
	list, _ := recordAll(t)

	bad := strings.Replace(listQuery, "sourceArea", "sourceAreaX", 1)
	if errs := schema.ValidateWithVariables(bad, list.vars); len(errs) == 0 || !strings.Contains(errs[0].Message, "sourceAreaX") {
		t.Errorf("a misnamed selection field must be rejected by name, got %v", errs)
	}

	crit := map[string]any{}
	for k, v := range list.vars["criteria"].(map[string]any) {
		crit[k] = v
	}
	delete(crit, "deviceToken")
	crit["device"] = "d1"
	errs := schema.ValidateWithVariables(listQuery, map[string]any{"criteria": crit})
	if len(errs) == 0 || !strings.Contains(errs[0].Message, "device") {
		t.Errorf("a misnamed input key must be rejected by name, got %v", errs)
	}
}
