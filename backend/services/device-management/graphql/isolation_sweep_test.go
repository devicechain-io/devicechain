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
