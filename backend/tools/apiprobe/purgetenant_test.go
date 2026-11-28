// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
)

// Every document the purge and the save-door read send validates against the schema that serves it,
// with the variables they send, and clears the root-field ceiling. A document that cannot be sent
// would otherwise be found on a cluster, an hour into a drill.
func TestPurgeAndAuthoredViaDocumentsValidateAgainstTheServedSchemas(t *testing.T) {
	docs := purgeSeedDocs(2)
	docs = append(docs, probeDoc{"authoredVia", "device-management", authoredViaQuery, map[string]any{"token": "apiprobe-device-profile"}})
	docs = append(docs, probeDoc{"attribute delete", "device-management", bcUnsetDoc, map[string]any{
		"entityType": "device", "entity": "apiprobe-device", "scope": probeAttrScope, "attrKey": "k"}})
	for _, d := range docs {
		schema := servedSchema(t, d.area, schemaplane.MountTenant)
		if errs := schema.ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against %s: %v\n  document: %s", d.what, d.area, errs, d.doc)
		}
		if err := gqlcore.CheckWork(d.doc); err != nil {
			t.Errorf("%s exceeds the served root-field ceiling: %v", d.what, err)
		}
	}
	admin := servedSchema(t, "user-management", schemaplane.MountAdmin)
	for _, d := range purgeDeleteDocs(purgeState{Tenant: "agpurge-1", Email: "agpurge-1@apiprobe.invalid"}) {
		if errs := admin.ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against the admin schema: %v\n  document: %s", d.what, errs, d.doc)
		}
		if err := gqlcore.CheckWork(d.doc); err != nil {
			t.Errorf("%s exceeds the served root-field ceiling: %v", d.what, err)
		}
	}
}

// The seed sets exactly --keys SHARED attributes, each under its own key, on the device it created.
func TestPurgeSeedSetsEveryKeyOnTheSeededDevice(t *testing.T) {
	keys := map[string]bool{}
	for _, d := range purgeSeedDocs(3) {
		if d.doc != attrWriteMutation {
			continue
		}
		req := d.vars["req"].(map[string]any)
		if req["scope"] != "SHARED" || req["entity"] != purgeDeviceToken || req["entityType"] != "device" {
			t.Fatalf("an attribute write is not a SHARED write to the seeded device: %v", req)
		}
		keys[req["attrKey"].(string)] = true
	}
	if len(keys) != 3 {
		t.Fatalf("seed sets %d distinct keys for --keys 3, want 3", len(keys))
	}
}

// delete refuses a state without the tenant or the identity, and seed refuses --keys 0: a purge of a
// tenant with no attribute rows would pass the rig's "nothing left" check by having had nothing.
func TestPurgeTenantRefusesWhatProvesNothing(t *testing.T) {
	err := purgeTenantTo(context.Background(), []string{"seed", "--out", t.TempDir() + "/s.json", "--keys", "0"}, &strings.Builder{})
	assertCode(t, err, exitSetup)
	p := t.TempDir() + "/s.json"
	if werr := os.WriteFile(p, []byte(`{"tenant":"agpurge-1"}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	err = purgeTenantTo(context.Background(), []string{"delete", "--state", p}, &strings.Builder{})
	assertCode(t, err, exitSetup)
	err = purgeTenantTo(context.Background(), []string{"wipe"}, &strings.Builder{})
	assertCode(t, err, exitSetup)
}

// The verdict: every version must read exactly the one door. The UNKNOWN case is the drill's pass;
// the others are what it must refuse, the DESCRIBE case being the rig's own control.
func TestAuthoredViaVerdict(t *testing.T) {
	unknown := []versionDoors{{Version: 1, AuthoredVia: []string{"UNKNOWN"}}, {Version: 2, AuthoredVia: []string{"UNKNOWN"}}}
	if err := authoredViaVerdict("p", unknown, "UNKNOWN"); err != nil {
		t.Fatalf("every version reading [UNKNOWN] must pass: %v", err)
	}
	assertCode(t, authoredViaVerdict("p", unknown, "DESCRIBE"), exitMismatch)
	assertCode(t, authoredViaVerdict("p", []versionDoors{{Version: 1, AuthoredVia: nil}}, "UNKNOWN"), exitMismatch)
	assertCode(t, authoredViaVerdict("p", []versionDoors{{Version: 1, AuthoredVia: []string{"API", "UNKNOWN"}}}, "UNKNOWN"), exitMismatch)
	assertCode(t, authoredViaVerdict("p", []versionDoors{{Version: 1, AuthoredVia: []string{"UNKNOWN"}}, {Version: 2, AuthoredVia: []string{"API"}}}, "UNKNOWN"), exitMismatch)
	assertCode(t, authoredViaVerdict("p", nil, "UNKNOWN"), exitMissing)
}
