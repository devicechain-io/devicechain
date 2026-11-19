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
		{"queryS5Artifact", "update-management", queryS5Artifact, map[string]any{"token": "a"}},
		{"queryS5Signer", "update-management", queryS5Signer, map[string]any{"token": "s"}},
		{"queryS5Rollout", "update-management", queryS5Rollout, map[string]any{"token": "r"}},
		{"queryS5Targets", "update-management", queryS5Targets, map[string]any{"token": "r", "c": map[string]any{"pageNumber": 1, "pageSize": 100}}},
		{"queryS5Assignment", "update-management", queryS5Assignment, map[string]any{"token": "a"}},
		{"queryReplacementDevice", "device-management", queryReplacementDevice, map[string]any{"tokens": []string{"hp-gauge-north-3"}}},
		{"queryReplacements", "device-management", queryReplacements, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": 100, "device": "hp-gauge-north-3"}}},
		{"queryReplacementCredentials", "device-management", queryReplacementCredentials, map[string]any{"tokens": []string{"hp-gauge-north-3-cred"}}},
		{"queryCalculatedJob", "event-management", queryCalculatedJob, map[string]any{"token": "job"}},
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
		{"mutationRotateSigning", "outbound-connectors", mutationRotateSigning, map[string]any{"handle": "harborpulse/flood-emergency"}},
		{"mutationEndSigningRotation", "outbound-connectors", mutationEndSigningRotation, map[string]any{"handle": "harborpulse/flood-emergency"}},
		{"queryNotificationAttempts", "notification-management", queryNotificationAttempts, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": attemptPageSize, "alarmToken": "a1"},
		}},
		{"mutationAcknowledgeAlarm", "device-management", mutationAcknowledgeAlarm, map[string]any{"token": "a1"}},
		{"queryDecoderHealth", "device-management", queryDecoderHealth, map[string]any{"profile": "hp-lora-gauge-profile", "minutes": 30}},
		{"queryFailedEvents", "event-management", queryFailedEvents, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": failedPageSize, "reason": failedReasonDecode, "deviceToken": "hp-legacy-gauge-basin"},
		}},
		{"queryGroupTargets", "device-management", queryGroupTargets, map[string]any{"group": "hp-all-lights", "after": "12", "limit": groupPageSize}},
		{"queryCampaign", "command-delivery", queryCampaign, map[string]any{"token": "hp-lights-dusk"}},
		{"queryCampaignCycles", "command-delivery", queryCampaignCycles, map[string]any{"token": "hp-lights-dusk", "limit": cyclePageSize}},
		{"queryCampaignElements", "command-delivery", queryCampaignElements, map[string]any{
			"token": "hp-lights-dusk", "cycle": "d2026-11-17", "after": "5", "limit": elementPageSize}},
		{"queryBatchCommands", "command-delivery", queryBatchCommands, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": commandPageSize, "batchToken": "b1"},
		}},
		{"queryPositions", "device-management", queryPositions, map[string]any{"tokens": []string{"hp-light-east-1"}}},
		{"queryAssetAncestors", "device-management", queryAssetAncestors, map[string]any{"token": "hp-bldg-quayside-lofts"}},
		{"queryAnchorDevices", "device-management", queryAnchorDevices, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": anchorPageSize, "targetType": "asset", "target": "hp-station-basin", "relationshipType": "assigned"},
		}},
		{"queryRelationships", "device-management", queryRelationships, map[string]any{
			"c": map[string]any{"pageNumber": 1, "pageSize": relationPageSize, "sourceType": "asset", "source": "hp-feeder-east", "relationshipType": "hp-feeds"},
		}},
	}
}

func TestEveryVerifierDocumentValidatesAgainstItsServedSchema(t *testing.T) {
	docs := verifierDocuments()
	if len(docs) != 28 {
		t.Fatalf("%d documents listed; the verifier sends 28 — a new one must be listed here", len(docs))
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
