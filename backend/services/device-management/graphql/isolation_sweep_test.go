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
	for _, op := range []string{"query devices", "query customers", "query deviceTypes"} {
		o, ok := isolationsweep.Find(out, op)
		if !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for acme's member", op, o, ok)
		}
	}
}
