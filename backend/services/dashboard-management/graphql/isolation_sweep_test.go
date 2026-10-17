// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
)

// TestIsolationSweep calls every query, mutation and subscription dashboard-management
// serves as acme's member, aimed at the dashboard shared only with globex, and fails on any
// answer that carries it or tells it apart from a dashboard that does not exist (ADR-081).
// Its negative control is the same queries as the unscoped author, which must surface it.
func TestIsolationSweep(t *testing.T) {
	f := newShareFixture(t)
	// One connection: each new connection to sqlite's :memory: is an empty database, and
	// the sweep's many calls would otherwise reach one with no tables.
	sqldb, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqldb.SetMaxOpenConns(1)
	f.dashboard(t, "acme-tenant", "d-acme", true, "acme")
	f.dashboard(t, "acme-tenant", "d-globex", true, "globex")
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   f.member("customer:acme"),
		Unscoped: f.author("acme-tenant"),
		Probe:    "d-globex",
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
	// The sweep reached the opted-in reads, and they answered data rather than a refusal.
	for _, op := range []string{"query dashboards", "query publishedDashboard"} {
		o, ok := isolationsweep.OutcomeOf(out, op)
		if !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for acme's member", op, o, ok)
		}
	}
}

// TestIsolationSweepForAGroupMember is the same sweep for a member limited to a GROUP: every
// operation, as group:north's member, aimed at the dashboard shared only with another group,
// must neither carry it nor tell it apart from a dashboard that does not exist. The opted-in
// reads must still answer with data — the dashboard shared with north — or it proves nothing.
func TestIsolationSweepForAGroupMember(t *testing.T) {
	f := newShareFixture(t)
	sqldb, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqldb.SetMaxOpenConns(1)
	f.targets.shareable["group:yard"] = true
	f.dashboard(t, "acme-tenant", "d-north", true, "group:north")
	f.dashboard(t, "acme-tenant", "d-yard", true, "group:yard")
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:   gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:   f.member("group:north"),
		Unscoped: f.author("acme-tenant"),
		Probe:    "d-yard",
		Hidden:   []string{"yard"},
	})
	t.Logf("swept %d operations as group:north's member", len(out))
	for _, op := range []string{"query dashboards", "query publishedDashboard"} {
		o, ok := isolationsweep.OutcomeOf(out, op)
		if !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for group:north's member", op, o, ok)
		}
	}
	if got := listedTokens(t, f.member("group:north")); len(got) != 1 || got[0] != "d-north" {
		t.Fatalf("group:north's member listed %v, want only d-north", got)
	}
}
