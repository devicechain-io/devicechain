// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import "testing"

// cfgFloor is a config at a given expected floor with a modest load floor.
func cfgFloor(floor int) ContentionConfig {
	return ContentionConfig{Profile: Profile{Manifest: "devicepulse", MinAccepted: 1000}, ExpectFloor: floor}
}

// A clean CONTENDED outcome pair (floor >= 1): gold rode through zero-loss, the shed
// probe shed some and everything it kept persisted.
func contendedGold() *tenantOutcome {
	return &tenantOutcome{Role: "gold", Tenant: "t-gold", Accepted: 15000, Shed: 0, Failed: 0, Persisted: 15000, Reached: true}
}
func contendedShed() *tenantOutcome {
	// Attempted 15000, ~2/3 shed, the accepted third all persisted.
	return &tenantOutcome{Role: "shed", Tenant: "t-shed", Accepted: 5000, Shed: 10000, Failed: 0, Persisted: 5000, Reached: true}
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
	shed := &tenantOutcome{Role: "shed", Tenant: "t-shed", Accepted: 15000, Shed: 0, Failed: 0, Persisted: 15000, Reached: true}
	invs := classifyContention(cfgFloor(0), gold, shed)
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

func TestContentionReportPassedRequiresInvariants(t *testing.T) {
	empty := &ContentionReport{}
	if empty.Passed() {
		t.Error("a report with no invariants must not pass — it asserted nothing")
	}
	pass := &ContentionReport{Invariants: []Invariant{{Name: "x", Passed: true}}}
	if !pass.Passed() {
		t.Error("a report with all-passing invariants should pass")
	}
	fail := &ContentionReport{Invariants: []Invariant{{Name: "x", Passed: true}, {Name: "y", Passed: false}}}
	if fail.Passed() {
		t.Error("a report with any failing invariant must fail")
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
