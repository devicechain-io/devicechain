// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// TestComputeStateBudget proves the per-tenant state-budget rollup (ADR-023, slice 6c-1): the rule
// dimension counts a tenant over its rule budget, the live-key dimension counts a tenant over its
// live-key budget, a co-tenant within budget is not counted (fail-open is the enforcement posture;
// here it must simply not be flagged), and the aggregate live-key total is the sum across tenants.
func TestComputeStateBudget(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)
	rp.cfg.MaxRulesPerTenant = 1
	rp.cfg.MaxLiveKeysPerTenant = 2

	// One rule for acme, no live state yet — within both budgets.
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{scopedCoreThreshold("acme", "p@1", "acme/p@1/r1")}})
	if s := rp.computeStateBudget(); s.tenantsOverRules != 0 || s.tenantsOverKeys != 0 || s.totalLiveKeys != 0 {
		t.Fatalf("one rule, no keys must be clean; got %+v", s)
	}

	// A second acme rule breaches the rule budget (2 > 1). beta gets a SINGLE Duration rule (within
	// its rule budget) but holds 3 devices => 3 active + 3 wheel timers = 6 live keys > 2, so beta
	// breaches the LIVE-KEY budget only. Distinct tenants, distinct dimensions.
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{scopedCoreThreshold("acme", "p@1", "acme/p@1/r2")}})
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScopedFor("beta", "beta/p@1/dur", `{"v":"A"}`)}})
	for i, dev := range []string{"d1", "d2", "d3"} {
		rp.engine.ProcessEvent(detectcore.Event{Seq: uint64(100 + i), Key: detectcore.SeriesKey{Rule: "beta/p@1/dur", Series: dev}, Time: testBase, Match: true})
	}
	rp.engine.Drain()

	s := rp.computeStateBudget()
	if s.tenantsOverRules != 1 {
		t.Fatalf("only acme is over the rule budget (beta has 1 rule); got tenantsOverRules=%d", s.tenantsOverRules)
	}
	if s.tenantsOverKeys != 1 {
		t.Fatalf("only beta is over the live-key budget (6 > 2; acme's thresholds hold no keys); got tenantsOverKeys=%d", s.tenantsOverKeys)
	}
	if s.totalLiveKeys != 6 {
		t.Fatalf("aggregate live-key total must be 6 (beta's duration state); got %d", s.totalLiveKeys)
	}
}

// durScopedFor is a Duration rule for an arbitrary tenant (durScoped hardcodes acme).
func durScopedFor(tenant, id, definition string) runtime.ScopedRule {
	sr := durScoped(id, definition)
	sr.Tenant = tenant
	return sr
}
