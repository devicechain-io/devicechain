// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
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

// slidingScopedFor is a sliding-window aggregate — a kind that RETAINS one record per sample —
// for an arbitrary tenant. Its threshold is unreachable so it accumulates without firing, which
// is exactly the shape the retained-sample budget exists to watch.
func slidingScopedFor(tenant, pvt, id string) runtime.ScopedRule {
	return runtime.ScopedRule{
		Tenant:              tenant,
		ProfileVersionToken: pvt,
		Compiled: &rules0.CompiledRule{ID: id, Core: detectcore.Rule{ID: id, Kind: detectcore.SlidingAgg,
			Window: time.Hour, Agg: detectcore.AggMax, Op: detectcore.GT, Thresh: 1e9}},
	}
}

// TestComputeStateBudgetSeesRetainedSamplesTheKeyBudgetCannot is the processor-level half of the
// retained-sample control. It pins the whole point of adding a second axis: a tenant can sit well
// INSIDE a generous live-key budget while its windows retain an unbounded pile of samples, so the
// key dimension alone reports a clean process. Both dimensions are asserted in the same run — if
// tenantsOverKeys ever starts firing here, the test has stopped isolating the blind spot.
func TestComputeStateBudgetSeesRetainedSamplesTheKeyBudgetCannot(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)
	// Deliberately generous key budget, tight sample budget: the tenant must breach ONLY the axis
	// that can see per-sample retention.
	rp.cfg.MaxRulesPerTenant = 100
	rp.cfg.MaxLiveKeysPerTenant = 100
	rp.cfg.MaxRetainedSamplesPerTenant = 20

	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{slidingScopedFor("acme", "p@1", "acme/p@1/slide")}})

	// 50 samples, ONE series: one live key, fifty retained records.
	for i := 0; i < 50; i++ {
		rp.engine.ProcessEvent(detectcore.Event{Seq: uint64(200 + i),
			Key:  detectcore.SeriesKey{Rule: "acme/p@1/slide", Series: "d1"},
			Time: testBase.Add(time.Duration(i) * time.Second), Value: float64(i), HasValue: true, Match: true})
	}
	rp.engine.Drain()

	s := rp.computeStateBudget()
	if s.totalLiveKeys != 1 {
		t.Fatalf("50 samples on one series is ONE live key; got %d", s.totalLiveKeys)
	}
	if s.tenantsOverKeys != 0 {
		t.Fatalf("the live-key budget (100) must NOT be breached by 1 key — this test no longer isolates "+
			"the blind spot it guards; got tenantsOverKeys=%d", s.tenantsOverKeys)
	}
	if s.totalRetainedSamples != 50 {
		t.Fatalf("want 50 retained samples in the aggregate; got %d", s.totalRetainedSamples)
	}
	if s.tenantsOverSamples != 1 {
		t.Fatalf("acme retains 50 samples against a budget of 20 and must be flagged on the sample axis; "+
			"got tenantsOverSamples=%d", s.tenantsOverSamples)
	}
}

// TestComputeStateBudgetSampleAxisIsSilentWithinBudget is the counterweight: a budget that flags
// every tenant is as useless as one that flags none.
func TestComputeStateBudgetSampleAxisIsSilentWithinBudget(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)
	rp.cfg.MaxRulesPerTenant = 100
	rp.cfg.MaxLiveKeysPerTenant = 100
	rp.cfg.MaxRetainedSamplesPerTenant = 100

	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{slidingScopedFor("acme", "p@1", "acme/p@1/slide")}})
	for i := 0; i < 50; i++ {
		rp.engine.ProcessEvent(detectcore.Event{Seq: uint64(300 + i),
			Key:  detectcore.SeriesKey{Rule: "acme/p@1/slide", Series: "d1"},
			Time: testBase.Add(time.Duration(i) * time.Second), Value: float64(i), HasValue: true, Match: true})
	}
	rp.engine.Drain()

	s := rp.computeStateBudget()
	if s.totalRetainedSamples != 50 {
		t.Fatalf("want 50 retained samples; got %d", s.totalRetainedSamples)
	}
	if s.tenantsOverSamples != 0 {
		t.Fatalf("50 samples against a budget of 100 must not be flagged; got %d", s.tenantsOverSamples)
	}
}
