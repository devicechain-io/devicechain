// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/sim"
)

// cfgFloor is a config at a given expected floor with a modest load floor.
func cfgFloor(floor int) ContentionConfig {
	return ContentionConfig{Profile: Profile{Manifest: "devicepulse", MinAccepted: 1000}, ExpectFloor: floor}
}

// A clean CONTENDED outcome pair (floor >= 1): gold rode through zero-loss, the shed
// probe shed some and everything it kept persisted.
func contendedGold() *tenantOutcome {
	return &tenantOutcome{Role: "gold", Tenant: "t-gold", Accepted: 15000, Shed: 0, Failed: 0, Persisted: 15000, Reached: true, Identity: reconciled()}
}

// reconciled is an identity report that held.
func reconciled() IdentityReport { return IdentityReport{Reconciled: true} }
func contendedShed() *tenantOutcome {
	// Attempted 15000, ~2/3 shed, the accepted third all persisted.
	return &tenantOutcome{Role: "shed", Tenant: "t-shed", Accepted: 5000, Shed: 10000, Failed: 0, Persisted: 5000, Reached: true, Identity: reconciled()}
}

func allPass(invs []Invariant) bool {
	for _, inv := range invs {
		if !inv.Passed {
			return false
		}
	}
	return len(invs) > 0
}

// TestClassifyContentionContendedPasses pins the positive case: at a floor, gold rides
// through and the shed probe sheds — every invariant holds.
func TestClassifyContentionContendedPasses(t *testing.T) {
	invs := classifyContention(cfgFloor(2), contendedGold(), contendedShed())
	// Asserted by NAME: an invariant that was never added would leave allPass green.
	if !invByName(t, invs, InvContentionUnbackpressured).Passed {
		t.Error("no tenant was backpressured, but contention-not-backpressured failed")
	}
	if !allPass(invs) {
		for _, inv := range invs {
			if !inv.Passed {
				t.Errorf("expected PASS, but %q failed: %s", inv.Name, inv.Detail)
			}
		}
	}
}

// TestClassifyContentionNegativeControlPasses pins the negative control: at floor 0
// NEITHER tenant sheds, and that is a pass (it proves a positive run's sheds were
// caused by the floor).
func TestClassifyContentionNegativeControlPasses(t *testing.T) {
	gold := contendedGold()
	shed := &tenantOutcome{Role: "shed", Tenant: "t-shed", Accepted: 15000, Shed: 0, Failed: 0, Persisted: 15000, Reached: true, Identity: reconciled()}
	invs := classifyContention(cfgFloor(0), gold, shed)
	if !invByName(t, invs, InvContentionUnbackpressured).Passed {
		t.Error("no tenant was backpressured, but contention-not-backpressured failed")
	}
	if !allPass(invs) {
		for _, inv := range invs {
			if !inv.Passed {
				t.Errorf("expected negative-control PASS, but %q failed: %s", inv.Name, inv.Detail)
			}
		}
	}
}

// TestGoldShedFailsTheGate is the promise's teeth: if gold shed even ONE event, the
// gate fails on gold-never-shed. This is the mutation the whole feature must not allow.
func TestGoldShedFailsTheGate(t *testing.T) {
	gold := contendedGold()
	gold.Shed = 1 // gold shed one event
	invs := classifyContention(cfgFloor(2), gold, contendedShed())
	if invByName(t, invs, InvGoldNeverShed).Passed {
		t.Error("gold shed 1 event but gold-never-shed passed — the promise has no teeth")
	}
	if allPass(invs) {
		t.Error("a gold shed must fail the overall gate")
	}
}

// TestShedNotEngagedFailsTheGate: at a floor, if the shed probe did NOT shed, "gold
// rode through" is vacuous — the mechanism never engaged, so the gate must fail rather
// than falsely certify.
func TestShedNotEngagedFailsTheGate(t *testing.T) {
	shed := contendedShed()
	shed.Shed = 0         // nothing shed
	shed.Accepted = 15000 // so load-floor still met via accepted
	shed.Persisted = 15000
	invs := classifyContention(cfgFloor(2), contendedGold(), shed)
	if invByName(t, invs, InvShedEngaged).Passed {
		t.Error("shed probe shed nothing at floor 2 but shed-mechanism-engaged passed — the test would be vacuous")
	}
}

// TestNegativeControlSheddingFailsTheGate: at floor 0, ANY shed by the shed probe is a
// base-ceiling artifact that would invalidate a positive run's attribution — it must
// fail the negative control.
func TestNegativeControlSheddingFailsTheGate(t *testing.T) {
	shed := contendedShed() // sheds 10000
	invs := classifyContention(cfgFloor(0), contendedGold(), shed)
	if invByName(t, invs, InvShedEngaged).Passed {
		t.Error("shed probe shed at floor 0 but the negative control passed — a base-ceiling shed must fail it")
	}
}

// TestShedCorruptionFailsTheGate: what the shed probe DID accept must all persist. A
// dropped accepted event (persisted < accepted) is corruption of what got through.
func TestShedCorruptionFailsTheGate(t *testing.T) {
	shed := contendedShed()
	shed.Persisted = shed.Accepted - 1 // one accepted event lost
	invs := classifyContention(cfgFloor(2), contendedGold(), shed)
	if invByName(t, invs, InvShedNoCorruption).Passed {
		t.Error("shed probe lost an ACCEPTED event but shed-no-corruption passed — shedding must never corrupt")
	}
}

// TestGoldLossFailsTheGate: gold must lose nothing it accepted.
func TestGoldLossFailsTheGate(t *testing.T) {
	gold := contendedGold()
	gold.Persisted = gold.Accepted - 1
	invs := classifyContention(cfgFloor(2), gold, contendedShed())
	if invByName(t, invs, InvGoldZeroLoss).Passed {
		t.Error("gold lost an accepted event but gold-zero-loss passed")
	}
}

// TestRealFailureFailsTheGate: a real (non-shed) emit failure makes the ledger
// ambiguous and must fail the clean-drive invariant — a shed is not a failure, but a
// 503/timeout is.
func TestRealFailureFailsTheGate(t *testing.T) {
	gold := contendedGold()
	gold.Failed = 3
	invs := classifyContention(cfgFloor(2), gold, contendedShed())
	if invByName(t, invs, InvContentionCleanGold).Passed {
		t.Error("gold had 3 real failures but gold-clean-drive passed")
	}
	if invByName(t, invs, InvGoldZeroLoss).Passed {
		t.Error("gold-zero-loss must not pass when the drive was not clean (the ledger is ambiguous)")
	}
}

// TestLoadFloorFailsOnTrivialDrive: a run that applied too little load must fail — a
// gate certifies correctness UNDER LOAD, not a smoke test.
func TestLoadFloorFailsOnTrivialDrive(t *testing.T) {
	gold := &tenantOutcome{Role: "gold", Accepted: 10, Persisted: 10, Reached: true}
	shed := &tenantOutcome{Role: "shed", Accepted: 5, Shed: 3, Persisted: 5, Reached: true}
	invs := classifyContention(cfgFloor(2), gold, shed)
	if invByName(t, invs, InvContentionLoadFloor).Passed {
		t.Error("a trivial drive (10 events) passed the load floor — a gate must apply real load")
	}
}

// TestShedProbeLoadFloorCountsAttempts: the shed probe's load floor is on ATTEMPTS
// (accepted + shed), so a heavily-shed probe that drove real load still clears it even
// though its accepted count alone is small.
func TestShedProbeLoadFloorCountsAttempts(t *testing.T) {
	// accepted 200 but shed 5000 → attempted 5200 >= 1000 floor.
	shed := &tenantOutcome{Role: "shed", Accepted: 200, Shed: 5000, Failed: 0, Persisted: 200, Reached: true}
	invs := classifyContention(cfgFloor(2), contendedGold(), shed)
	if !invByName(t, invs, InvContentionLoadFloor).Passed {
		t.Error("a heavily-shed probe that attempted real load failed the load floor — attempts, not accepted, is the measure")
	}
}

// TestShedProbeLoadFloorCountsBackpressuredAttempts: an emit the backpressure gate
// refused was still OFFERED, so it counts toward the shed probe's attempted load —
// the same value the floor read when a 503 was filed as a shed.
func TestShedProbeLoadFloorCountsBackpressuredAttempts(t *testing.T) {
	// accepted 200 + shed 500 + backpressured 400 = 1100 >= 1000; without the 503s, 700.
	shed := &tenantOutcome{Role: "shed", Accepted: 200, Shed: 500, Backpressured: 400, Persisted: 200, Reached: true}
	inv := invByName(t, classifyContention(cfgFloor(2), contendedGold(), shed), InvContentionLoadFloor)
	if !inv.Passed {
		t.Errorf("the shed probe offered 1100 emits but failed the 1000 load floor: %s", inv.Detail)
	}
	if want := "shed attempted 1100"; !strings.Contains(inv.Detail, want) {
		t.Errorf("load-floor detail %q does not report %q", inv.Detail, want)
	}
}

// 🔴 The platform's backpressure gate refusing GOLD is not the shed-priority promise
// breaking: that gate is shared and refuses every tenant alike. Filed as a shed it read as
// "gold was shed"; it must instead fail the run as unattributable, by name.
func TestBackpressureIsNotAGoldShed(t *testing.T) {
	gold := contendedGold()
	gold.Backpressured = 5
	invs := classifyContention(cfgFloor(2), gold, contendedShed())
	if !invByName(t, invs, InvGoldNeverShed).Passed {
		t.Error("gold was backpressured, never shed, but gold-never-shed failed — a 503 was read as a 429")
	}
	bp := invByName(t, invs, InvContentionUnbackpressured)
	if bp.Passed {
		t.Error("gold was refused 5 times by the backpressure gate but contention-not-backpressured passed")
	}
	if want := "gold \"t-gold\" was refused 5"; !strings.Contains(bp.Detail, want) {
		t.Errorf("detail %q does not name gold's 5 backpressure refusals (%q)", bp.Detail, want)
	}
	if allPass(invs) {
		t.Error("a run that backpressured gold must fail the gate")
	}
}

// 🔴 At a floor, backpressure 503s alone must not satisfy "the mechanism engaged": the
// floor never refused anything, the platform was just behind. When a 503 counted as a
// shed this was a FALSE PASS of the whole gate.
func TestBackpressureCannotEngageTheMechanism(t *testing.T) {
	shed := &tenantOutcome{Role: "shed", Tenant: "t-shed", Accepted: 5000, Shed: 0, Backpressured: 10000, Persisted: 5000, Reached: true}
	invs := classifyContention(cfgFloor(1), contendedGold(), shed)
	if invByName(t, invs, InvShedEngaged).Passed {
		t.Error("the shed probe took zero 429s at floor 1, only 503s, but shed-mechanism-engaged passed")
	}
	if allPass(invs) {
		t.Error("a floor that never shed must fail the gate however much the backpressure gate refused")
	}
}

// At floor 0 the negative control requires that NOTHING refused the shed probe for a
// reason the run would attribute; backpressure there fails the run by name, not as a
// "base-ceiling artifact" (a 429), which names the wrong cause.
func TestNegativeControlBackpressureFailsAsBackpressure(t *testing.T) {
	shed := &tenantOutcome{Role: "shed", Tenant: "t-shed", Accepted: 15000, Shed: 0, Backpressured: 7, Persisted: 15000, Reached: true}
	invs := classifyContention(cfgFloor(0), contendedGold(), shed)
	if !invByName(t, invs, InvShedEngaged).Passed {
		t.Error("the shed probe took zero 429s at floor 0 but shed-mechanism-engaged failed — a 503 was read as a 429")
	}
	if invByName(t, invs, InvContentionUnbackpressured).Passed {
		t.Error("the shed probe was backpressured 7 times in the negative control but contention-not-backpressured passed")
	}
	if allPass(invs) {
		t.Error("a negative control the backpressure gate touched must fail")
	}
}

// At a floor, the shed probe being backpressured AS WELL AS shed does not void the run:
// the verdict reads 429s only, gold took no refusal of either kind, and the shed probe's
// 429s are the floor at work. Pinned so the scope of the backpressure invariant is a
// decision rather than an accident.
func TestShedProbeBackpressureAtAFloorDoesNotVoidTheRun(t *testing.T) {
	shed := contendedShed()
	shed.Backpressured = 300
	invs := classifyContention(cfgFloor(2), contendedGold(), shed)
	if !invByName(t, invs, InvContentionUnbackpressured).Passed {
		t.Error("only the shed probe was backpressured, at floor 2, but contention-not-backpressured failed")
	}
	if !allPass(invs) {
		for _, inv := range invs {
			if !inv.Passed {
				t.Errorf("expected PASS, but %q failed: %s", inv.Name, inv.Detail)
			}
		}
	}
}

// outcomeOf is the one place a tenant's 503s could be re-filed as 429s before the
// classifier sees them; every counter must land in its own field.
func TestOutcomeOfKeepsRefusalsApart(t *testing.T) {
	snap := sim.Snapshot{Emitted: 2, Shed: 3, Backpressured: 4, Failed: 1, Rate: 12.5}
	id := IdentityReport{Accepted: 2, Missing: 1, Samples: IdentitySamples{Missing: []string{"d@x"}}}
	o := outcomeOf("gold", "t-gold", 10, snap, QuiesceResult{Persisted: 2, Reached: true}, id)
	want := tenantOutcome{Role: "gold", Tenant: "t-gold", Devices: 10, Accepted: 2, Shed: 3,
		Backpressured: 4, Failed: 1, Persisted: 2, Reached: true, Rate: 12.5, Identity: id}
	if !reflect.DeepEqual(*o, want) {
		t.Errorf("outcomeOf = %+v, want %+v", *o, want)
	}
}

func TestContentionReportPassedRequiresInvariants(t *testing.T) {
	empty := &ContentionReport{}
	if empty.Passed() {
		t.Error("a report with no invariants must not pass — it asserted nothing")
	}
	idInvs := []Invariant{{Name: InvGoldIdentity, Passed: true}, {Name: InvShedIdentity, Passed: true}}
	pass := &ContentionReport{Invariants: append([]Invariant{{Name: "x", Passed: true}}, idInvs...)}
	if !pass.Passed() {
		t.Error("a report with all-passing invariants should pass")
	}
	fail := &ContentionReport{Invariants: append([]Invariant{{Name: "x", Passed: true}, {Name: "y", Passed: false}}, idInvs...)}
	if fail.Passed() {
		t.Error("a report with any failing invariant must fail")
	}
	// A report whose invariants all passed but that never checked identity proves only
	// that the totals agreed: it must not certify the run.
	for _, missing := range []string{InvGoldIdentity, InvShedIdentity} {
		var invs []Invariant
		for _, inv := range pass.Invariants {
			if inv.Name != missing {
				invs = append(invs, inv)
			}
		}
		if (&ContentionReport{Invariants: invs}).Passed() {
			t.Errorf("a report without %s passed", missing)
		}
	}
}

// Each tenant's identity verdict fails the gate on its own, and only its own invariant:
// equal totals (persisted == accepted, which every count invariant here reads) must not
// carry a tenant whose events did not reconcile one by one.
func TestClassifyContentionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		breakGold bool
		inv       string
	}{
		{"gold", true, InvGoldIdentity},
		{"shed", false, InvShedIdentity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gold, shed := contendedGold(), contendedShed()
			bad := IdentityReport{Accepted: 5000, Persisted: 5000, Missing: 1, DuplicateKeys: 1, ExtraCopies: 1,
				Samples: IdentitySamples{Missing: []string{"dev-1@B"}, Duplicate: []string{"dev-1@A x2"}}}
			if tc.breakGold {
				gold.Identity = bad
			} else {
				shed.Identity = bad
			}
			invs := classifyContention(cfgFloor(2), gold, shed)
			for _, inv := range invs {
				want := inv.Name != tc.inv
				if inv.Passed != want {
					t.Errorf("%s passed=%v, want %v (%s)", inv.Name, inv.Passed, want, inv.Detail)
				}
			}
			got := invByName(t, invs, tc.inv)
			if !strings.Contains(got.Detail, "dev-1@B") || !strings.Contains(got.Detail, "dev-1@A x2") {
				t.Errorf("%s detail %q does not name the missing and duplicated events", tc.inv, got.Detail)
			}
		})
	}
}

func TestContentionConfigValidate(t *testing.T) {
	if err := cfgFloor(2).withDefaults().Validate(); err != nil {
		t.Errorf("a floor-2 config should validate: %v", err)
	}
	for _, bad := range []int{-1, 4} {
		if err := (ContentionConfig{Profile: Profile{Manifest: "devicepulse"}, ExpectFloor: bad}).Validate(); err == nil {
			t.Errorf("expectFloor %d should be rejected", bad)
		}
	}
	if err := (ContentionConfig{Profile: Profile{Manifest: ""}, ExpectFloor: 1}).Validate(); err == nil {
		t.Error("an empty manifest should be rejected")
	}
}
