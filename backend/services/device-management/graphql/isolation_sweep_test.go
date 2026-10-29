// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
)

// TestIsolationSweep calls every query, mutation and subscription device-management
// serves as acme's member, aimed at globex's device, and fails on any answer that carries
// globex's data or tells globex's device apart from a missing one (ADR-081). Its negative
// control is the same queries unscoped, which must surface globex.
func TestIsolationSweep(t *testing.T) {
	f := newScopeFixture(t)
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   f.member("customer:acme"),
		Unscoped: f.operator(),
		Probe:    "globex-pump",
		Hidden:   []string{"globex"},
		// The alarm reads and the alarm stream name their originator as a (type, token)
		// pair, and validate the state and severity filters before resolving it; with these
		// filled, the all-arguments documents aim the token at globex's device rather than
		// stopping at an unknown entity type or an invalid filter.
		Literals: map[string]string{"originatorType": `"device"`, "state": `"ACTIVE"`, "severity": `"WARNING"`},
	})
	counts := map[string]int{}
	for _, o := range out {
		switch {
		case o.Data:
			counts["data"]++
		case len(o.Codes) > 0:
			counts[o.Codes[0]]++
		default:
			counts["silent"]++
		}
	}
	t.Logf("swept %d operations as acme's member: %v", len(out), counts)
	for _, o := range out {
		if !o.Data && (len(o.Codes) == 0 || o.Codes[0] == "-") {
			t.Logf("  answered with no code: %s %v", o.Operation, o.Codes)
		}
	}
	// The sweep reached the opted-in reads, and they answered data rather than a refusal.
	for _, op := range []string{"query devices", "query customers", "query deviceTypes",
		"query areas", "query areasById", "query areasByToken", "query assets", "query assetsById", "query assetsByToken",
		"query areaTypes", "query assetTypes"} {
		o, ok := isolationsweep.OutcomeOf(out, op)
		if !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for acme's member", op, o, ok)
		}
	}
	// deviceScopeAnchors stays closed to a scoped member even though Device now admits one:
	// a device's anchors can name another customer sharing it, so the query is gated on
	// Authorize, not AuthorizeScoped. The sweep must reach it and see it refused.
	// customerDevices is opted in, but answers a scoped member only about a customer its
	// scope names; the sweep aims it at the probe, which names none, so it must be reached
	// and refused (its hidden-vs-missing twin is the sweep's own second call).
	if o, ok := isolationsweep.OutcomeOf(out, "query customerDevices"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query customerDevices: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	// anchorDevices is opted in, but the sweep aims it at the probe's token with the probe as
	// the anchor KIND, which names no anchor a scoped member may ask about: reached, refused.
	if o, ok := isolationsweep.OutcomeOf(out, "query anchorDevices"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query anchorDevices: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	// The relationship graph names co-owners: still closed, now with areas and assets open
	// beside it.
	if o, ok := isolationsweep.OutcomeOf(out, "query entityRelationships"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query entityRelationships: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	if o, ok := isolationsweep.OutcomeOf(out, "query deviceScopeAnchors"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query deviceScopeAnchors: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	// deviceConstantsPage reads device METADATA (the numbers under the names asked for) for every
	// device of a profile. It is the rule preview's door, reached over a service token, so it is
	// gated on Authorize: a scoped member is refused before any profile or device is looked up.
	if o, ok := isolationsweep.OutcomeOf(out, "query deviceConstantsPage"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query deviceConstantsPage: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
}

// LOW-2: the main sweep aims its probe at globex's DEVICE, so its hidden-versus-missing
// comparison never met a real hidden area or asset. These runs aim it at floor-globex and at
// asset-globex, which the unscoped control sees and acme's member must not: every operation's
// answer for the hidden place must be indistinguishable from the answer for a name that
// exists nowhere. anchorDevices is called with a literal targetType so its all-arguments
// document reaches the area (or asset) branch rather than stopping at an unknown kind.
func TestIsolationSweepAimedAtAHiddenAreaAndAsset(t *testing.T) {
	for _, c := range []struct{ probe, kind string }{{"floor-globex", "area"}, {"asset-globex", "asset"}} {
		t.Run(c.probe, func(t *testing.T) {
			f := newScopeFixture(t)
			out := isolationsweep.Run(t, isolationsweep.Config{
				Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
				Scoped:   f.member("customer:acme"),
				Unscoped: f.operator(),
				Probe:    c.probe,
				Hidden:   []string{"globex"},
				Literals: map[string]string{"originatorType": `"device"`, "state": `"ACTIVE"`, "severity": `"WARNING"`,
					"targetType": `"` + c.kind + `"`},
			})
			byToken := "query " + c.kind + "sByToken"
			if o, ok := isolationsweep.OutcomeOf(out, byToken); !ok || !o.Data {
				t.Errorf("%s: %+v (reached=%v), want data (an empty list) for acme's member", byToken, o, ok)
			}
			// What the sweep's anchorDevices call means: acme asking about a place only globex
			// has a device on is an empty page with total 0, and the operator's is not.
			q := `{ anchorDevices(criteria: {pageNumber: 1, pageSize: 10, targetType: "` + c.kind + `", target: "` + c.probe +
				`"}) { results { token } pagination { totalRecords } } }`
			acme := serve(t, f.member("customer:acme"), q, nil)
			requireServed(t, acme, "anchorDevices on the hidden place")
			page := acme.data["anchorDevices"].(map[string]any)
			if n := page["pagination"].(map[string]any)["totalRecords"].(float64); n != 0 || len(page["results"].([]any)) != 0 {
				t.Errorf("acme on %s: %v, want an empty page", c.probe, page)
			}
			op := serve(t, f.operator(), q, nil)
			requireServed(t, op, "control anchorDevices")
			if n := op.data["anchorDevices"].(map[string]any)["pagination"].(map[string]any)["totalRecords"].(float64); n != 1 {
				t.Errorf("control: the operator on %s saw %v devices, want globex's one", c.probe, n)
			}
		})
	}
}
