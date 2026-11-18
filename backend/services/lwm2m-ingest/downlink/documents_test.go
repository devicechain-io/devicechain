// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

const documentResponse = `{}`

// The target SDL is outside this module; Go's test cache does not track it.
// Run these contract tests with -count=1.
type documentCall struct {
	target string
	query  string
	vars   map[string]any
}
type documentRecorder struct{ calls []documentCall }

func (r *documentRecorder) Query(_ context.Context, target, _ string, query string, vars map[string]any, out any) error {
	// Serialize just as svcclient does: pointers and input objects become wire values.
	body, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	var wire map[string]any
	if err = json.Unmarshal(body, &wire); err != nil {
		return err
	}
	r.calls = append(r.calls, documentCall{target, query, wire})
	return json.Unmarshal([]byte(documentResponse), out)
}
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

func TestEveryCrossServiceDocumentValidates(t *testing.T) {
	ctx := context.Background()
	r := &documentRecorder{}

	c := NewCommandClaimer(r, "command-delivery")
	if _, _, err := c.Claim(ctx, "tenant", "command"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ClaimDispatch(ctx, "tenant", "command", "nonce"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCommandParker(r, "command-delivery").Park(ctx, "tenant", "command", "nonce"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCommandFetcher(r, "command-delivery").Pending(ctx, "tenant", "device", 10); err != nil {
		t.Fatal(err)
	}
	h := NewHarvester(r, "device-management", HarvestMetrics{}, func(context.Context, string, string) (bool, error) { return false, nil })
	h.publish(ctx, deviceKey{tenant: "tenant", deviceToken: "device"}, DeviceInfo{}, nil)
	h.wait()
	validateDocuments(t, r.calls, map[string]int{"command-delivery": 4, "device-management": 1})
}
