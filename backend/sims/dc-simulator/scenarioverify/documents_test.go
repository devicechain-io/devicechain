// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// Every document the verifier sends is a string the compiler never reads and the schema lives in
// other modules, so this validates each against the TENANT plane its area serves, with the
// server's own validator and the server's own options (see loadtest/documents_test.go for the
// reasoning, and for why `go test -count=1` matters: the SDL is outside this module).

func servedSchema(t *testing.T, area string) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "services", area, "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil {
		t.Fatalf("classify %s: %v", dir, err)
	}
	if !served {
		t.Fatalf("no tenant-plane schema found for area %q under %s", area, dir)
	}
	schema, err := graphql.ParseSchema(sdl, nil,
		graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatalf("parse %s: %v", area, err)
	}
	return schema
}

// verifierDocuments is every document the verifier sends, with the KEY SET of the variables it
// really sends (the validator rejects an input key the schema does not define).
func verifierDocuments() []struct {
	name, area, doc string
	vars            map[string]any
} {
	return []struct {
		name, area, doc string
		vars            map[string]any
	}{
		{"queryAlarms", "device-management", queryAlarms, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": alarmPageSize},
		}},
		{"queryCommands", "command-delivery", queryCommands, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": commandPageSize, "deviceToken": "hp-pump-north"},
		}},
		{"queryMeasurements", "event-management", queryMeasurements, map[string]any{
			"c": map[string]any{
				"pageNumber": 1, "pageSize": eventPageSize, "deviceToken": "hp-charger-east-1",
				"startTime": "2026-11-16T12:00:00Z", "endTime": "2026-11-16T12:05:00Z",
			},
		}},
		{"queryPreview", "event-processing", queryPreview, map[string]any{
			"input": map[string]any{
				"ruleDefinition": "{}", "profileToken": "hp-gauge-profile",
				"start": "2026-11-16T12:00:00Z", "end": "2026-11-16T12:05:00Z",
			},
		}},
	}
}

func TestEveryVerifierDocumentValidatesAgainstItsServedSchema(t *testing.T) {
	docs := verifierDocuments()
	if len(docs) != 4 {
		t.Fatalf("%d documents listed; the verifier sends 4 — a new one must be listed here", len(docs))
	}
	for _, d := range docs {
		if errs := servedSchema(t, d.area).ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against %s's tenant schema: %v\n%s", d.name, d.area, errs, d.doc)
		}
	}
}

// The control: a document naming a field the schema does not have, and a variable key the input
// does not define, must both be refused, or the test above validates nothing.
func TestTheValidatorRefusesWhatItExistsFor(t *testing.T) {
	schema := servedSchema(t, "device-management")
	bad := `query { alarms(criteria: {pageNumber: 1, pageSize: 1}) { results { tokenn } } }`
	if errs := schema.ValidateWithVariables(bad, nil); len(errs) == 0 {
		t.Fatal("a misspelled field validated")
	}
	if errs := schema.ValidateWithVariables(queryAlarms, map[string]any{
		"c": map[string]any{"pageNumber": 1, "pageSize": 1, "originatorTokn": "x"},
	}); len(errs) == 0 {
		t.Fatal("an unknown input key validated")
	}
}
