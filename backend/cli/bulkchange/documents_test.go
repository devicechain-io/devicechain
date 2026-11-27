// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// servedSDL is device-management's tenant schema as this tree serves it.
func servedSDL(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("tenant schema: served=%v err=%v", served, err)
	}
	return sdl
}

// bulkChangeTestSDL is the served schema, which declares the bulk-change operations.
func bulkChangeTestSDL(t *testing.T) string {
	t.Helper()
	sdl := servedSDL(t)
	if !strings.Contains(sdl, "createBulkChange(") {
		t.Fatal("the served schema does not declare createBulkChange")
	}
	return sdl
}

// bulkChangeOperations are the operations a build that serves bulk changes adds to Query and Mutation.
var bulkChangeOperations = []string{
	"bulkChange", "bulkChanges", "bulkChangeLines", "planBulkChange", "previewBulkChangeResume", "previewBulkChangeReversal",
	"createBulkChange", "startBulkChange", "pauseBulkChange", "resumeBulkChange", "abortBulkChange",
	"reverseBulkChange", "resolveAmbiguousBulkChangeLines",
}

// unservedSDL is the served schema with bulk changes taken out: what a build from before they were
// served answers (bulkChangeReadiness only). It drops the bulk-change section of type definitions and
// each operation's field with the description above it, and fails rather than return a schema that
// still declares one.
func unservedSDL(t *testing.T) string {
	t.Helper()
	sdl, _, found := strings.Cut(bulkChangeTestSDL(t), "\n# Bulk changes (Preview)\n")
	if !found {
		t.Fatal("the bulk-change section marker is missing from the served schema")
	}
	lines := strings.Split(sdl, "\n")
	drop := map[int]bool{}
	for i, line := range lines {
		field := strings.TrimPrefix(line, "    ")
		if field == line || strings.HasPrefix(field, " ") {
			continue
		}
		for _, op := range bulkChangeOperations {
			if !strings.HasPrefix(field, op+"(") && !strings.HasPrefix(field, op+":") {
				continue
			}
			drop[i] = true
			if field == op+"(" { // arguments on lines of their own, up to the closing "    ): Type"
				for k := i + 1; k < len(lines); k++ {
					drop[k] = true
					if strings.HasPrefix(lines[k], "    )") {
						break
					}
				}
			}
			j := i - 1
			if d := strings.TrimSpace(lines[j]); d == `"""` {
				drop[j] = true
				for j--; j >= 0 && !strings.HasPrefix(strings.TrimSpace(lines[j]), `"""`); j-- {
					drop[j] = true
				}
				drop[j] = true
			} else if strings.HasPrefix(d, `"""`) && strings.HasSuffix(d, `"""`) && len(d) > 6 {
				drop[j] = true
			}
		}
	}
	kept := make([]string, 0, len(lines))
	for i, line := range lines {
		if !drop[i] {
			kept = append(kept, line)
		}
	}
	out := strings.Join(kept, "\n")
	for _, op := range bulkChangeOperations {
		if strings.Contains(out, " "+op+"(") || strings.Contains(out, " "+op+":") {
			t.Fatalf("the unserved schema still declares %s", op)
		}
	}
	if _, err := graphql.ParseSchema(out, nil, graphql.UseFieldResolvers()); err != nil {
		t.Fatalf("the unserved schema does not parse: %v", err)
	}
	return out
}

func TestBulkChangeDocumentsMatchServedSchema(t *testing.T) {
	schema, err := graphql.ParseSchema(bulkChangeTestSDL(t), nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	variables := map[string]any{
		"token": "t", "digest": "reviewed",
		"criteria": map[string]any{"pageNumber": 1, "pageSize": 1000},
		"request": map[string]any{
			"token": "t", "name": "test", "kind": "config.set",
			"configSet": map[string]any{"key": "k", "unset": true}, "deviceTokens": []any{"d"},
		},
	}
	for name, document := range map[string]string{
		"plan": planQuery, "create": createQuery, "start": startQuery, "status": statusQuery,
		"lines": linesQuery, "pause": pauseQuery, "resume": resumeQuery, "abort": abortQuery,
		"resume preview": resumePreviewQuery,
	} {
		if errs := schema.ValidateWithVariables(document, variables); len(errs) != 0 {
			t.Errorf("%s: %v", name, errs)
		}
	}
	// The variables are the exact JSON each request type sends, so a field the input type does
	// not declare is refused here rather than by a server.
	reversal := ReversalRequest{Original: "o", Token: "r", Name: "n", Ordinals: []int{1, 2}, OverrideOrdinals: []int{2},
		Safety: map[string]any{"maxPostsPerSecond": 1, "failureThreshold": 1, "ambiguityThreshold": 1, "settleDeadlineSeconds": 60}}
	resolution := Resolution{Token: "t", Ordinals: []int{1}, Verdict: "CONFIRMED", Note: "checked on site"}
	for name, tc := range map[string]struct {
		document string
		request  any
	}{
		"reverse preview": {reversePreviewQuery, reversal},
		"reverse":         {reverseQuery, reversal},
		"reverse all":     {reverseQuery, ReversalRequest{Original: "o", Token: "r", Name: "n", OverrideChanged: true}},
		"resolve":         {resolveQuery, resolution},
		"resolve all":     {resolveQuery, Resolution{Token: "t", AllAmbiguous: true, Verdict: "FAILED", Note: "n"}},
	} {
		raw, err := json.Marshal(map[string]any{"request": tc.request})
		if err != nil {
			t.Fatal(err)
		}
		var vars map[string]any
		if err := json.Unmarshal(raw, &vars); err != nil {
			t.Fatal(err)
		}
		if errs := schema.ValidateWithVariables(tc.document, vars); len(errs) != 0 {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// The SDL lives outside this module; run with -count=1 after schema edits.
func TestReadinessDocumentMatchesServedSchema(t *testing.T) {
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("tenant SDL: served=%v, err=%v", served, err)
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{readinessQuery, legacyReadinessQuery} {
		if errs := schema.Validate(query); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
}
