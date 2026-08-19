// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- topology -----------------------------------------------------------------

func TestPresenceManifestShape(t *testing.T) {
	cfg := PresenceConfig{Seed: 11, SteadyDevices: 3, ChurnDevices: 4, DepartedDevices: 2, BackgroundDevices: 20}.withDefaults()
	m := cfg.harnessManifest()

	require.Len(t, m.Profiles, 1)
	assert.Equal(t, HarnessPresenceProfileToken, m.Profiles[0].Token)
	require.Len(t, m.DeviceTypes, 1)
	assert.Equal(t, HarnessPresenceProfileToken, m.DeviceTypes[0].ProfileToken)
	require.Len(t, m.Populations, 4)

	cohorts, err := partitionByPrefixes(m.Expand(m.Seed),
		[]string{presSteadyTokenPrefix, presChurnTokenPrefix, presGoneTokenPrefix, presBgTokenPrefix})
	require.NoError(t, err)
	assert.Len(t, cohorts[0], 3)
	assert.Len(t, cohorts[1], 4)
	assert.Len(t, cohorts[2], 2)
	assert.Len(t, cohorts[3], 20)
}

// The four cohorts ARE the oracle, so a prefix pair that cannot tell two of them
// apart must be refused rather than silently routing one cohort's devices into
// another — which would report a full complement in both.
func TestPartitionRefusesAPrefixThatSwallowsAnother(t *testing.T) {
	devices := []sim.DeviceInstance{{Token: "harness-pres-gone-001"}}
	_, err := partitionByPrefixes(devices, []string{"harness-pres-", "harness-pres-gone-"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be told apart")
}

func TestPartitionRefusesAnEmptyPrefix(t *testing.T) {
	_, err := partitionByPrefixes([]sim.DeviceInstance{{Token: "x"}}, []string{"", "y"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestPartitionRefusesADeviceMatchingNoCohort(t *testing.T) {
	_, err := partitionByPrefixes([]sim.DeviceInstance{{Token: "stranger-001"}}, []string{presSteadyTokenPrefix})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches no harness population prefix")
}

// The three-role wrapper must keep behaving exactly as it did — the two existing
// harnesses partition through it.
func TestThreeRoleWrapperStillSplits(t *testing.T) {
	devices := []sim.DeviceInstance{
		{Token: "harness-cmd-safe-001"}, {Token: "harness-cmd-probe-001"}, {Token: "harness-cmd-bg-00001"},
	}
	safety, probes, bg, err := partitionByPrefix(devices, cmdSafeTokenPrefix, cmdProbeTokenPrefix, cmdBgTokenPrefix)
	require.NoError(t, err)
	assert.Len(t, safety, 1)
	assert.Len(t, probes, 1)
	assert.Len(t, bg, 1)
}

// --- config -------------------------------------------------------------------

// The paged read is an invariant, not a convenience: a page size that swallows the
// whole asserted cohort completes in one request and exercises no cursor at all.
func TestPresenceConfigRefusesAPageSizeThatSkipsTheCursor(t *testing.T) {
	cfg := PresenceConfig{SteadyDevices: 2, ChurnDevices: 2, PageSize: 4}.withDefaults()
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exercise no cursor")
}

func TestPresenceConfigAcceptsAPageSizeSmallerThanTheCohort(t *testing.T) {
	cfg := PresenceConfig{SteadyDevices: 2, ChurnDevices: 2, PageSize: 3}.withDefaults()
	require.NoError(t, cfg.Validate())
}

func TestPresenceConfigRefusesTheControlWithoutASpareSteadyDevice(t *testing.T) {
	cfg := PresenceConfig{SteadyDevices: 1, Control: ControlDropSteadyDevice}.withDefaults()
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 2 steady devices")
}

func TestPresenceConfigRefusesAnUnknownControl(t *testing.T) {
	cfg := PresenceConfig{Control: "drop-everything"}.withDefaults()
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown control")
}

// 🔴 A negative count must REACH Validate. The other harnesses fold one into the
// default, which makes every "at least one" line in their Validate a check that
// cannot fail; this one passes it through so the check is real.
func TestPresenceConfigRefusesNegativeCohorts(t *testing.T) {
	for name, cfg := range map[string]PresenceConfig{
		"steady":   {SteadyDevices: -1},
		"churn":    {ChurnDevices: -1},
		"departed": {DepartedDevices: -1},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, cfg.withDefaults().Validate())
		})
	}
}

func TestPresenceConfigFillsOnlyUnsetFields(t *testing.T) {
	cfg := PresenceConfig{SteadyDevices: 7, Poll: 250 * time.Millisecond}.withDefaults()
	assert.Equal(t, 7, cfg.SteadyDevices)
	assert.Equal(t, 250*time.Millisecond, cfg.Poll)
	assert.Equal(t, DefaultPresenceChurn, cfg.ChurnDevices)
	assert.Equal(t, DefaultPresenceTailHold, cfg.TailHold)
}

// --- disposition (pure) -------------------------------------------------------

// allPass builds a full, healthy invariant set for the declared names.
func allInvariantsPass(names []string) []Invariant {
	out := make([]Invariant, 0, len(names))
	for _, n := range names {
		out = append(out, Invariant{Name: n, Passed: true})
	}
	return out
}

// A tap that is not running is not a presence defect, and the advice has to name
// every switch: there are six, and naming one would misdiagnose the other five.
func TestTapOffIsInconclusiveAndNamesEverySwitch(t *testing.T) {
	var m measurability
	m.cannot("%s", tapOffAdvice)
	d, reason := decideDisposition(m, PresenceInvariants, allInvariantsPass(PresenceInvariants))
	assert.Equal(t, DispositionInconclusive, d)
	for _, want := range []string{"brokerPresence", "sysUser", "no event source", "serviceAuth", "system-account dial", "advisory subscribe"} {
		assert.Contains(t, reason, want, "the tap-off advice must name this switch")
	}
}

// 🔴 PRECEDENCE. A run that could not measure is INCONCLUSIVE even when invariants
// also failed: the unmeasurability is a reason those invariants would fail for a
// cause that is not the platform, and reporting FAIL names the platform for the
// environment's problem.
func TestUnmeasurabilityOutranksAFailedInvariant(t *testing.T) {
	var m measurability
	m.cannot("the background fleet was shed at the ingest ceiling")
	invs := allInvariantsPass(PresenceInvariants)
	invs[2].Passed = false
	d, reason := decideDisposition(m, PresenceInvariants, invs)
	assert.Equal(t, DispositionInconclusive, d)
	assert.Contains(t, reason, "ceiling")
}

// Every reason is reported, not just the first — an operator fixing one and re-running
// into the next has learned the gate is unreliable rather than that it is thorough.
func TestEveryReasonIsCarriedIntoTheVerdict(t *testing.T) {
	var m measurability
	m.cannot("first cause")
	m.cannot("second cause")
	_, reason := decideDisposition(m, PresenceInvariants, allInvariantsPass(PresenceInvariants))
	assert.Contains(t, reason, "first cause")
	assert.Contains(t, reason, "second cause")
}

// An empty invariant set is not a pass: a report with nothing asserted has proven
// nothing, which is a broken harness rather than a healthy platform.
func TestNoInvariantsIsAFailure(t *testing.T) {
	d, reason := decideDisposition(measurability{}, PresenceInvariants, nil)
	assert.Equal(t, DispositionFail, d)
	assert.Contains(t, reason, "not evaluated")
}

// 🔴🔴 THE BACKSTOP UNDER EVERYTHING ELSE. A verdict reads the invariants that ARE in
// the report, and a control compares only the names it expects to flip — so an
// invariant that stopped being appended at all is invisible to both. A gate could
// ship asserting two of its six properties, print PASS, and have its own negative
// control confirm it was working.
func TestAPartialInvariantSetIsNotAPass(t *testing.T) {
	partial := allInvariantsPass(PresenceInvariants)[:2]
	d, reason := decideDisposition(measurability{}, PresenceInvariants, partial)
	assert.Equal(t, DispositionFail, d)
	assert.Contains(t, reason, "evaluated 2 invariant(s)")
	for _, missing := range PresenceInvariants[2:] {
		assert.Contains(t, reason, missing)
	}
}

func TestAnUnrecognisedOrDuplicatedInvariantIsAFailure(t *testing.T) {
	extra := append(allInvariantsPass(PresenceInvariants), Invariant{Name: "presence-something-invented", Passed: true})
	d, reason := decideDisposition(measurability{}, PresenceInvariants, extra)
	assert.Equal(t, DispositionFail, d)
	assert.Contains(t, reason, "presence-something-invented")

	dup := append(allInvariantsPass(PresenceInvariants), Invariant{Name: InvPresenceSteadyOnline, Passed: true})
	d2, reason2 := decideDisposition(measurability{}, PresenceInvariants, dup)
	assert.Equal(t, DispositionFail, d2)
	assert.Contains(t, reason2, "duplicated: ["+InvPresenceSteadyOnline)
}

func TestAFailedInvariantIsNamedInTheReason(t *testing.T) {
	invs := allInvariantsPass(PresenceInvariants)
	invs[2].Passed = false
	d, reason := decideDisposition(measurability{}, PresenceInvariants, invs)
	assert.Equal(t, DispositionFail, d)
	assert.Contains(t, reason, invs[2].Name)
}

func TestAllHeldIsAPass(t *testing.T) {
	d, _ := decideDisposition(measurability{}, PresenceInvariants, allInvariantsPass(PresenceInvariants))
	assert.Equal(t, DispositionPass, d)
}

func TestExitCodeHasThreeValues(t *testing.T) {
	for disp, want := range map[Disposition]int{
		DispositionPass: 0, DispositionFail: 1, DispositionInconclusive: 2,
	} {
		assert.Equal(t, want, (&PresenceReport{Disposition: disp}).ExitCode(), "disposition %s", disp)
		assert.Equal(t, want, (&BatchReport{Disposition: disp}).ExitCode(), "batch disposition %s", disp)
	}
	assert.False(t, (&PresenceReport{Disposition: DispositionInconclusive}).Passed(),
		"an inconclusive run has NOT passed — it has no opinion")
	assert.False(t, (&BatchReport{Disposition: DispositionInconclusive}).Passed())
}

// --- the negative control (pure) ----------------------------------------------

// controlFlipping builds a full invariant set with exactly the named ones failing.
func controlFlipping(declared []string, failing ...string) []Invariant {
	fail := map[string]bool{}
	for _, n := range failing {
		fail[n] = true
	}
	out := allInvariantsPass(declared)
	for i := range out {
		out[i].Passed = !fail[out[i].Name]
	}
	return out
}

func TestControlIsSatisfiedByExactlyItsExpectedSet(t *testing.T) {
	ok, detail := evaluateControl(ControlDropSteadyDevice,
		controlFlipping(PresenceInvariants, InvPresenceSteadyOnline, InvPresenceReconcilerExact))
	assert.True(t, ok, detail)
}

// 🔴 An oracle that failed EVERYTHING would be passed by a control demanding merely
// "some failure". Demanding the set exactly is what discriminates.
func TestControlIsViolatedWhenSomethingElseAlsoFails(t *testing.T) {
	ok, detail := evaluateControl(ControlDropSteadyDevice,
		controlFlipping(PresenceInvariants, InvPresenceSteadyOnline, InvPresenceReconcilerExact, InvPresenceLoadFloor))
	assert.False(t, ok)
	assert.Contains(t, detail, InvPresenceLoadFloor)
}

func TestControlIsViolatedWhenAnExpectedInvariantStillPasses(t *testing.T) {
	ok, detail := evaluateControl(ControlDropSteadyDevice,
		controlFlipping(PresenceInvariants, InvPresenceSteadyOnline))
	assert.False(t, ok)
	assert.Contains(t, detail, InvPresenceReconcilerExact)
}

// 🔴🔴 A CONTROL MUST POLICE THE WHOLE SET, NOT JUST THE PART IT EXPECTS TO FLIP.
// Otherwise the one check whose entire job is proving the oracle still works will
// happily certify an oracle that has quietly stopped asserting four of its six
// properties — and both halves of the gate go green together.
func TestControlIsViolatedWhenInvariantsHaveVanishedFromTheReport(t *testing.T) {
	crippled := []Invariant{
		{Name: InvPresenceSteadyOnline, Passed: false},
		{Name: InvPresenceReconcilerExact, Passed: false},
	}
	ok, detail := evaluateControl(ControlDropSteadyDevice, crippled)
	assert.False(t, ok, "a 2-of-6 report must not satisfy a control")
	assert.Contains(t, detail, "cannot be judged")
	assert.Contains(t, detail, InvPresenceLoadFloor)
}

func TestBatchControlAlsoPolicesTheWholeSet(t *testing.T) {
	ok, _ := evaluateBatchControl(ControlDeafDevice, []Invariant{{Name: InvBatchRoundTrip, Passed: false}})
	assert.False(t, ok, "a 1-of-7 report must not satisfy the batch control")

	ok2, detail := evaluateBatchControl(ControlDeafDevice,
		controlFlipping(BatchInvariants, InvBatchRoundTrip))
	assert.True(t, ok2, detail)
}

func TestControlIsViolatedForAnEmptyReportOrAnUnknownName(t *testing.T) {
	ok, _ := evaluateControl(ControlDropSteadyDevice, nil)
	assert.False(t, ok)
	ok2, detail := evaluateControl("no-such-control", allInvariantsPass(PresenceInvariants))
	assert.False(t, ok2)
	assert.Contains(t, detail, "no expected failure set")
}

// The declared set must match what the classifier really produces. A list that has
// drifted from the code makes every verdict above a failure on a healthy run.
func TestTheDeclaredSetsMatchWhatTheClassifiersProduce(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	assert.NoError(t, requireInvariantSet(PresenceInvariants,
		classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)))

	bobs, targets, bystanders := healthyBatch()
	assert.NoError(t, requireInvariantSet(BatchInvariants,
		classifyBatch(bobs, targets, bystanders, healthyBatchConfig(), 5000)))
}

// --- classification (pure) ----------------------------------------------------

// healthyObservations is one clean run: three steady, two churn over R rounds, one
// departed. Tests mutate a copy to drive a single invariant into its failure.
func healthyObservations(rounds int) (presenceObservations, []string, []string, []string) {
	steady := []string{"harness-pres-steady-001", "harness-pres-steady-002"}
	churn := []string{"harness-pres-churn-001"}
	departed := []string{"harness-pres-gone-001"}

	online := deviceStateObs{Present: true, Active: true, PresenceSource: presenceSourceAsserted, SessionID: 100, Source: "mqtt1"}
	offline := deviceStateObs{Present: true, Active: false, PresenceSource: presenceSourceAsserted, SessionID: 200, Source: "mqtt1"}

	baseline := map[string]deviceStateObs{}
	for _, t := range append(append([]string{}, steady...), append(churn, departed...)...) {
		baseline[t] = online
	}
	sessions := map[string][]int64{}
	for _, t := range churn {
		s := []int64{100}
		for i := 1; i <= rounds; i++ {
			s = append(s, int64(100+i*10))
		}
		sessions[t] = s
	}
	obs := presenceObservations{
		CleanSteadyPolls: 9,
		SteadyBad:        map[string]string{},
		ConnectBaseline:  baseline,
		DepartedFinal:    map[string]deviceStateObs{departed[0]: offline},
		ChurnSessions:    sessions,
		AssertedActive:   append(append([]string{}, steady...), churn...),
		Pages:            3,
		AssertedAll:      append(append(append([]string{}, steady...), churn...), departed...),
		AssertedAllPages: 3,
	}
	return obs, steady, churn, departed
}

func healthyPresenceConfig(rounds int) PresenceConfig {
	return PresenceConfig{SteadyDevices: 2, ChurnDevices: 1, DepartedDevices: 1, ChurnRounds: rounds, PageSize: 2}.withDefaults()
}

// invariant finds one invariant by name, failing the test when it is absent — an
// assertion that was never evaluated is not one that passed.
func invariant(t *testing.T, invs []Invariant, name string) Invariant {
	t.Helper()
	for _, inv := range invs {
		if inv.Name == name {
			return inv
		}
	}
	t.Fatalf("invariant %q is not in the report: %v", name, invs)
	return Invariant{}
}

// assertOnlyTheseFailed pins the exact failure set of a classifier result.
//
// 🔴 IT ASSERTS THE NAMED INVARIANTS ARE PRESENT FIRST, AND THAT IS NOT PEDANTRY. The
// obvious form of this helper iterates the invariants it was GIVEN — so an invariant
// that stopped being appended at all is never visited, and every test naming it as
// expected-to-fail passes silently. Seven tests across the two harnesses were relying
// on exactly that loop.
func assertOnlyTheseFailed(t *testing.T, invs []Invariant, want ...string) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, n := range want {
		wantSet[n] = true
	}
	present := map[string]bool{}
	for _, inv := range invs {
		present[inv.Name] = true
	}
	for _, n := range want {
		if !present[n] {
			t.Errorf("invariant %q is expected to FAIL but is not in the result at all — an assertion that did not run cannot have failed", n)
		}
	}
	for _, inv := range invs {
		if wantSet[inv.Name] {
			assert.False(t, inv.Passed, "%s should have failed: %s", inv.Name, inv.Detail)
		} else {
			assert.True(t, inv.Passed, "%s should have held: %s", inv.Name, inv.Detail)
		}
	}
}

func TestAHealthyRunPassesEveryInvariant(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	require.NoError(t, requireInvariantSet(PresenceInvariants, invs),
		"the classifier must produce exactly the declared set — a count alone would not say WHICH one drifted")
	assertOnlyTheseFailed(t, invs)
}

func TestLoadFloorFailsWhenTheBackgroundNeverGotOffTheGround(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 12)
	assertOnlyTheseFailed(t, invs, InvPresenceLoadFloor)
	assert.Contains(t, invariant(t, invs, InvPresenceLoadFloor).Detail, "12")
}

// The connect baseline is what makes the departed cohort's verdict mean anything: a
// device that never came online cannot prove a disconnect edge by being offline.
func TestConnectBaselineFailsWhenADeviceNeverCameOnline(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.ConnectBaseline[departed[0]] = deviceStateObs{} // no row at all
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceConnectAsserted)
	assert.Contains(t, invariant(t, invs, InvPresenceConnectAsserted).Detail, "no device-state row")
}

func TestConnectBaselineFailsWhenARowIsMerelyInferred(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.ConnectBaseline[steady[0]] = deviceStateObs{Present: true, Active: true, PresenceSource: "INFERRED"}
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceConnectAsserted)
}

func TestSteadyOnlineFailsOnAFlickerThatRecovered(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.SteadyBad[steady[1]] = "active=false presenceSource=ASSERTED"
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceSteadyOnline)
	assert.Contains(t, invariant(t, invs, InvPresenceSteadyOnline).Detail, steady[1])
}

// 🔴 "No bad observation" across too few observations is not a pass. A run whose
// reads all failed would otherwise certify a cohort it never looked at.
func TestSteadyOnlineFailsWhenThereWereTooFewCleanPolls(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.CleanSteadyPolls = 1
	obs.SteadyReadErrors = 40
	cfg := healthyPresenceConfig(3)
	cfg.MinSteadyPolls = 5
	invs := classifyPresence(obs, steady, churn, departed, cfg, 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceSteadyOnline)
	d := invariant(t, invs, InvPresenceSteadyOnline).Detail
	assert.Contains(t, d, "too few observations")
	assert.Contains(t, d, "40 read error")
}

func TestDepartedOfflineFailsWhenTheDeviceStillReadsOnline(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.DepartedFinal[departed[0]] = deviceStateObs{Present: true, Active: true, PresenceSource: presenceSourceAsserted}
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceDepartedOffline)
}

// "The row says offline" and "there is no row" are different claims, and only the
// first is a transition the platform applied.
func TestDepartedOfflineFailsWhenThereIsNoRowAtAll(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.DepartedFinal[departed[0]] = deviceStateObs{}
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceDepartedOffline)
	assert.Contains(t, invariant(t, invs, InvPresenceDepartedOffline).Detail, "no device-state row")
}

func TestSessionsMonotonicFailsOnASessionThatDidNotClimb(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.ChurnSessions[churn[0]] = []int64{100, 110, 110, 130} // round 2 repeated round 1
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceSessionsMonotonic)
	assert.Contains(t, invariant(t, invs, InvPresenceSessionsMonotonic).Detail, "did not exceed")
}

func TestSessionsMonotonicFailsOnARegression(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.ChurnSessions[churn[0]] = []int64{100, 110, 105, 130}
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceSessionsMonotonic)
}

// A round whose reading was never taken leaves the wrong number of readings, and
// that must fail rather than be silently compared as a shorter climb.
func TestSessionsMonotonicFailsOnAMissingReading(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.ChurnSessions[churn[0]] = []int64{100, 110}
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceSessionsMonotonic)
	assert.Contains(t, invariant(t, invs, InvPresenceSessionsMonotonic).Detail, "want 4")
}

func TestReconcilerReadFailsWhenItUnderReads(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedActive = []string{steady[0], churn[0]} // steady[1] missing
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerExact)
	assert.Contains(t, invariant(t, invs, InvPresenceReconcilerExact).Detail, "missing: ["+steady[1])
}

func TestReconcilerReadFailsWhenItOverReads(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedActive = append(obs.AssertedActive, departed[0]) // a device that left
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerExact)
	assert.Contains(t, invariant(t, invs, InvPresenceReconcilerExact).Detail, "unexpected: ["+departed[0])
}

// A cursor that re-serves a row it already served produces the right SET and the
// wrong page — which a set comparison alone would call correct.
func TestReconcilerReadFailsOnADuplicatedRow(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedActive = append(obs.AssertedActive, steady[0])
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerExact)
	assert.Contains(t, invariant(t, invs, InvPresenceReconcilerExact).Detail, "duplicated: ["+steady[0])
}

// A walk that could not complete is not an empty set. Failing it as a membership
// mismatch would report a read outage as a reconciler defect.
func TestReconcilerReadFailsLoudlyWhenTheWalkErrored(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedReadErr = "assertedDeviceStates page 2: connection reset"
	obs.AssertedActive = nil
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerExact)
	assert.Contains(t, invariant(t, invs, InvPresenceReconcilerExact).Detail, "did not complete")
}

// 🔴 THE CONTROL, TIED TO THE CLASSIFIER RATHER THAN TO MY BELIEF ABOUT IT.
//
// The expected failure set is a claim about what dropping a steady device DOES, and
// the only honest way to check it is to build that scenario out of observations and
// ask the classifier. Reading the expectation out of controlExpectations and
// asserting it against itself would be tautological — it would pass no matter which
// invariants the drop really flips.
func TestDroppingASteadyDeviceFlipsExactlyTheControlsExpectedSet(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	victim := steady[len(steady)-1]
	// What a real drop produces: the watcher saw it offline, and the reconciler can no
	// longer enumerate a device that is genuinely offline.
	obs.SteadyBad[victim] = "active=false presenceSource=ASSERTED sessionId=140 source=mqtt1"
	obs.AssertedActive = []string{steady[0], churn[0]}

	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceSteadyOnline, InvPresenceReconcilerExact)

	ok, detail := evaluateControl(ControlDropSteadyDevice, invs)
	assert.True(t, ok, "the declared expected set must be what the classifier actually produces: %s", detail)
}

// --- the steady watcher -------------------------------------------------------

// A deviation is the verdict, not the last reading: a device that dropped and came
// back looks perfect at the end and has already fired every offline alarm.
func TestSteadyWatchRemembersADeviationTheDeviceRecoveredFrom(t *testing.T) {
	w := newSteadyWatch()
	tokens := []string{"a"}
	online := deviceStateObs{Present: true, Active: true, PresenceSource: presenceSourceAsserted}
	w.record(map[string]deviceStateObs{"a": online}, tokens)
	w.record(map[string]deviceStateObs{"a": {Present: true, Active: false, PresenceSource: presenceSourceAsserted}}, tokens)
	w.record(map[string]deviceStateObs{"a": online}, tokens)

	clean, errs, bad := w.snapshot()
	assert.Equal(t, 3, clean)
	assert.Equal(t, 0, errs)
	require.Contains(t, bad, "a")
	assert.Contains(t, bad["a"], "active=false")
}

// 🔴 FIRST, not last. A device that flaps repeatedly would otherwise report only its
// final flap — the reading furthest from whatever caused it, and the least useful one
// to hand an operator. A single bad observation cannot tell these two apart, which is
// why this case has two.
func TestSteadyWatchKeepsTheFirstDeviationNotTheLatest(t *testing.T) {
	w := newSteadyWatch()
	tokens := []string{"a"}
	w.record(map[string]deviceStateObs{"a": {Present: true, Active: false, PresenceSource: presenceSourceAsserted, SessionID: 11}}, tokens)
	w.record(map[string]deviceStateObs{"a": {Present: true, Active: true, PresenceSource: "INFERRED", SessionID: 22}}, tokens)

	_, _, bad := w.snapshot()
	require.Contains(t, bad, "a")
	assert.Contains(t, bad["a"], "sessionId=11", "the FIRST deviation is the one kept")
	assert.NotContains(t, bad["a"], "sessionId=22")
}

// A read that failed is not evidence the cohort was healthy.
func TestSteadyWatchCountsAReadErrorApartFromACleanPoll(t *testing.T) {
	w := newSteadyWatch()
	w.recordError()
	w.recordError()
	w.record(map[string]deviceStateObs{"a": {Present: true, Active: true, PresenceSource: presenceSourceAsserted}}, []string{"a"})
	clean, errs, bad := w.snapshot()
	assert.Equal(t, 1, clean)
	assert.Equal(t, 2, errs)
	assert.Empty(t, bad)
}

// --- the oracle's reads -------------------------------------------------------

func statesJSON(rows ...map[string]any) json.RawMessage {
	body, _ := json.Marshal(map[string]any{"deviceStatesByDeviceToken": rows})
	return body
}

// 🔴 sessionId is a String carrying a UnixNano. Compared as TEXT, "9" sorts above
// "10" and a perfectly ordered fleet reads as a monotonicity finding.
func TestOracleParsesSessionIdAsANumberNotText(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return statesJSON(map[string]any{
			"deviceToken": "a", "active": true, "presenceSource": "ASSERTED",
			"sessionId": "1755600000123456789", "source": "mqtt1",
		}), nil
	}}}
	obs, err := o.states(context.Background(), []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, int64(1755600000123456789), obs["a"].SessionID)
	assert.True(t, obs["a"].assertedActive())
}

func TestOracleReportsAMissingRowAsAbsentNotOffline(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return statesJSON(), nil
	}}}
	obs, err := o.states(context.Background(), []string{"ghost"})
	require.NoError(t, err)
	assert.False(t, obs["ghost"].Present)
	assert.False(t, obs["ghost"].assertedOffline(), "no row is not an applied transition")
	assert.Equal(t, "no device-state row", obs["ghost"].describe())
}

func TestOracleFailsClosedOnASessionIdThatIsNotAnInteger(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return statesJSON(map[string]any{
			"deviceToken": "a", "active": true, "presenceSource": "ASSERTED", "sessionId": "later", "source": "mqtt1",
		}), nil
	}}}
	_, err := o.states(context.Background(), []string{"a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an integer")
}

// pagedFake serves a fixed row list through the cursor contract, so the walk is
// exercised the way the schema documents it.
func pagedFake(t *testing.T, tokens []string, pageSize int) (*fakeSession, *int) {
	t.Helper()
	calls := 0
	return &fakeSession{respond: func(vars map[string]any) (json.RawMessage, error) {
		calls++
		start := 0
		if after, ok := vars["afterId"].(string); ok {
			for i, tok := range tokens {
				if fmt.Sprintf("id-%s", tok) == after {
					start = i + 1
					break
				}
			}
		}
		end := start + pageSize
		if end > len(tokens) {
			end = len(tokens)
		}
		rows := make([]map[string]any, 0, pageSize)
		for _, tok := range tokens[start:end] {
			rows = append(rows, map[string]any{
				"id": "id-" + tok, "deviceToken": tok, "active": true, "presenceSource": "ASSERTED", "source": "mqtt1",
			})
		}
		body, _ := json.Marshal(map[string]any{"assertedDeviceStates": rows})
		return body, nil
	}}, &calls
}

func TestAssertedWalkPagesToAShortPage(t *testing.T) {
	tokens := []string{"a", "b", "c", "d", "e"}
	session, calls := pagedFake(t, tokens, 2)
	o := &presenceOracle{session: session}
	got, pages, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.NoError(t, err)
	assert.Equal(t, tokens, got)
	assert.Equal(t, 3, pages, "5 rows at 2 per page is two full pages and one short one")
	assert.Equal(t, 3, *calls)
}

// An exactly-full last page still needs one more request to learn the walk is done.
func TestAssertedWalkAsksAgainAfterAnExactlyFullPage(t *testing.T) {
	tokens := []string{"a", "b", "c", "d"}
	session, _ := pagedFake(t, tokens, 2)
	o := &presenceOracle{session: session}
	got, pages, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.NoError(t, err)
	assert.Equal(t, tokens, got)
	assert.Equal(t, 3, pages)
}

// A cursor that does not advance would page forever; the walk must say so instead.
func TestAssertedWalkRefusesANonAdvancingCursor(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"assertedDeviceStates":[
			{"id":"same","deviceToken":"a","active":true,"presenceSource":"ASSERTED","source":"mqtt1"},
			{"id":"same","deviceToken":"b","active":true,"presenceSource":"ASSERTED","source":"mqtt1"}]}`), nil
	}}}
	_, _, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not advancing")
}

// A row the query's own filters exclude is the SURFACE misbehaving. Folding it into
// the token set would report it as a membership mismatch with no hint of the cause.
func TestAssertedWalkFailsClosedOnARowItsFiltersExclude(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"assertedDeviceStates":[
			{"id":"1","deviceToken":"a","active":false,"presenceSource":"ASSERTED","source":"mqtt1"}]}`), nil
	}}}
	_, _, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "its own filter excludes")
}

// 🔴 THE SOURCE PREDICATE IS THE ONE THIS HARNESS CANNOT SEE ANY OTHER WAY. It owns
// the whole tenant, so a resolver that stopped scoping by source returns exactly the
// rows a correct one does. In production that defect is one gateway's reconciler
// enumerating another's devices and probing devices it does not own.
func TestAssertedWalkFailsClosedOnARowFromAnotherSource(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"assertedDeviceStates":[
			{"id":"1","deviceToken":"a","active":true,"presenceSource":"ASSERTED","source":"http1"}]}`), nil
	}}}
	_, _, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the source predicate is not being applied")
}

// 🔴 A PAGE MAY NOT EXCEED THE SIZE ASKED FOR. The walk stops on a page SHORTER than
// pageSize, which says nothing about the upper side — so a server ignoring the limit
// and dumping the whole set on page one satisfies the membership check and reports a
// plausible page count, while in production the response is truncated rather than
// paged and a reconciler marks live devices offline.
func TestAssertedWalkRefusesAPageBiggerThanItAskedFor(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"assertedDeviceStates":[
			{"id":"1","deviceToken":"a","active":true,"presenceSource":"ASSERTED","source":"mqtt1"},
			{"id":"2","deviceToken":"b","active":true,"presenceSource":"ASSERTED","source":"mqtt1"},
			{"id":"3","deviceToken":"c","active":true,"presenceSource":"ASSERTED","source":"mqtt1"}]}`), nil
	}}}
	_, _, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the page limit is not being applied")
}

// The activeOnly:false branch must accept the offline rows its own filter admits —
// this is the branch the broker-presence reconciler reads, and refusing them would
// make the harness unable to walk it at all.
func TestAssertedWalkAcceptsOfflineRowsWhenActiveOnlyIsFalse(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(vars map[string]any) (json.RawMessage, error) {
		if _, seen := vars["afterId"]; seen {
			return json.RawMessage(`{"assertedDeviceStates":[]}`), nil
		}
		return json.RawMessage(`{"assertedDeviceStates":[
			{"id":"1","deviceToken":"gone-1","active":false,"presenceSource":"ASSERTED","source":"mqtt1"},
			{"id":"2","deviceToken":"steady-1","active":true,"presenceSource":"ASSERTED","source":"mqtt1"}]}`), nil
	}}}
	got, _, err := o.assertedTokens(context.Background(), "mqtt1", false, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"gone-1", "steady-1"}, got)
}

func TestAssertedWalkPropagatesAReadError(t *testing.T) {
	o := &presenceOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return nil, fmt.Errorf("connection reset")
	}}}
	_, pages, err := o.assertedTokens(context.Background(), "mqtt1", true, 2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "page 1")
	assert.Equal(t, 0, pages)
}

// --- report rendering ---------------------------------------------------------

// 🔴 The legend is printed WITH the verdict. The upgrade rig announced "rows did NOT
// survive" four lines above its own legend saying code 1 meant the drill could not
// run; a reader should never have to reconcile a headline against an exit code.
func TestHumanReportPrintsTheDispositionAndItsLegendTogether(t *testing.T) {
	r := &PresenceReport{
		Disposition: DispositionInconclusive,
		Reason:      tapOffAdvice,
		Invariants:  []Invariant{{Name: InvPresenceLoadFloor, Passed: true, Detail: "d"}},
	}
	out := r.Human()
	assert.Contains(t, out, "INCONCLUSIVE")
	assert.Contains(t, out, "exit 2")
	assert.Contains(t, out, "2=could not measure")
	assert.True(t, strings.Index(out, "INCONCLUSIVE") < strings.Index(out, "brokerPresence"),
		"the verdict comes first, then why")
}

// --- the verdict fold ---------------------------------------------------------

// The tap-off path is the one this design was written to keep REACHABLE. Slice 2's
// upgrade drill shipped an INCONCLUSIVE branch no run could enter, and the drill it
// was written to fix had already made the same mistake — so this asserts the wiring,
// not only the decision function underneath it.
func TestFinishKeepsATapOffRunInconclusive(t *testing.T) {
	r := &PresenceReport{TapLive: false}
	r.cannotMeasure.cannot("%s", tapOffAdvice)
	r.finish(nil, "")
	assert.Equal(t, DispositionInconclusive, r.Disposition)
	assert.Equal(t, 2, r.ExitCode())
	assert.Contains(t, r.Reason, "brokerPresence")
}

// A caller that knows a SPECIFIC reason says so instead of printing six guesses.
func TestASpecificReasonReplacesTheGenericAdvice(t *testing.T) {
	r := &PresenceReport{}
	r.cannotMeasure.cannot("its row carries no source id")
	r.finish(nil, "")
	assert.Equal(t, DispositionInconclusive, r.Disposition)
	assert.Equal(t, "its row carries no source id", r.Reason)
	assert.NotContains(t, r.Reason, "brokerPresence")
}

// 🔴 A CONTROL RUN INVERTS THE MEANING OF ITS OWN INVARIANTS. They are expected to
// fail, so a control that behaved must report PASS — otherwise every green control
// reads as a release-blocking finding and the gate teaches people to ignore it.
func TestFinishReportsASatisfiedControlAsAPass(t *testing.T) {
	r := &PresenceReport{TapLive: true}
	r.finish(controlFlipping(PresenceInvariants, InvPresenceSteadyOnline, InvPresenceReconcilerExact), ControlDropSteadyDevice)
	assert.Equal(t, DispositionPass, r.Disposition)
	assert.True(t, r.ControlSatisfied)
	assert.Equal(t, 0, r.ExitCode())
}

// A control that did NOT flip its set is a broken oracle — including the case that
// looks most like success: every invariant still green.
func TestFinishReportsAnUnflippedControlAsAFailure(t *testing.T) {
	r := &PresenceReport{TapLive: true}
	r.finish(allInvariantsPass(PresenceInvariants), ControlDropSteadyDevice)
	assert.Equal(t, DispositionFail, r.Disposition)
	assert.False(t, r.ControlSatisfied)
	assert.Equal(t, 1, r.ExitCode())
}

// A control cannot rescue an inconclusive run, and must not leave a verdict behind:
// a stale "VIOLATED" beside INCONCLUSIVE reads as two answers to one question.
func TestFinishDoesNotEvaluateAControlOnAnInconclusiveRun(t *testing.T) {
	r := &PresenceReport{TapLive: true}
	r.cannotMeasure.cannot("shed at the ingest ceiling")
	r.finish(controlFlipping(PresenceInvariants, InvPresenceSteadyOnline, InvPresenceReconcilerExact), ControlDropSteadyDevice)
	assert.Equal(t, DispositionInconclusive, r.Disposition)
	assert.False(t, r.ControlSatisfied)
	assert.Empty(t, r.ControlDetail, "an unevaluated control must not claim a verdict")
	assert.Equal(t, 2, r.ExitCode())
	assert.NotContains(t, r.Human(), "VIOLATED", "an unevaluated control must not print a verdict either")
}

// A plain run must not be rewritten by control logic it never asked for.
func TestFinishLeavesAPlainRunAlone(t *testing.T) {
	r := &PresenceReport{TapLive: true}
	r.finish(controlFlipping(PresenceInvariants, InvPresenceSteadyOnline), "")
	assert.Equal(t, DispositionFail, r.Disposition)
	assert.Empty(t, r.Control)
	assert.False(t, r.ControlSatisfied)
}

// 🔴 THE BATCH HARNESS NOW HAS THE THIRD DISPOSITION ITS SIBLING SHIPPED WITH. Before
// it did, every "could not measure" cause — a missing MQTT route, a tenant that is
// not fresh, an unauthored vocabulary — exited 1, and the gate step annotated all of
// them as "the batch oracle FAILED ITS OWN CONTROL".
func TestBatchFinishHasThreeDispositions(t *testing.T) {
	pass := &BatchReport{}
	pass.finish(allInvariantsPass(BatchInvariants), "")
	assert.Equal(t, DispositionPass, pass.Disposition)
	assert.Equal(t, 0, pass.ExitCode())

	fail := &BatchReport{}
	fail.finish(controlFlipping(BatchInvariants, InvBatchRoundTrip), "")
	assert.Equal(t, DispositionFail, fail.Disposition)
	assert.Equal(t, 1, fail.ExitCode())

	blind := &BatchReport{}
	blind.cannotMeasure.cannot("command-delivery was unreachable for the whole settle")
	blind.finish(allInvariantsPass(BatchInvariants), "")
	assert.Equal(t, DispositionInconclusive, blind.Disposition)
	assert.Equal(t, 2, blind.ExitCode())
	assert.Contains(t, blind.Reason, "unreachable")
}

// A batch control cannot rescue an inconclusive run either — and must not leave a
// verdict behind when it was never evaluated.
func TestBatchFinishDoesNotEvaluateAControlOnAnInconclusiveRun(t *testing.T) {
	r := &BatchReport{}
	r.cannotMeasure.cannot("the MQTT gateway was unreachable")
	r.finish(controlFlipping(BatchInvariants, InvBatchRoundTrip), ControlDeafDevice)
	assert.Equal(t, DispositionInconclusive, r.Disposition)
	assert.False(t, r.ControlSatisfied)
	assert.Empty(t, r.ControlDetail, "an unevaluated control must not claim a verdict")
	assert.Equal(t, 2, r.ExitCode())
	assert.NotContains(t, r.Human(), "VIOLATED")
	assert.Contains(t, r.Human(), "NOT EVALUATED")
}

func TestBatchFinishReportsASatisfiedControlAsAPass(t *testing.T) {
	r := &BatchReport{}
	r.finish(controlFlipping(BatchInvariants, InvBatchRoundTrip), ControlDeafDevice)
	assert.True(t, r.ControlSatisfied)
	assert.Equal(t, DispositionPass, r.Disposition)
	assert.True(t, r.Passed())
}

func TestBatchReportWithNoInvariantsIsNotAPass(t *testing.T) {
	assert.False(t, (&BatchReport{}).Passed())
	r := &BatchReport{}
	r.finish(nil, ControlDeafDevice)
	assert.False(t, r.Passed())
}

// --- the reconciler's OTHER branch --------------------------------------------

// 🔴 THE BROKER-PRESENCE RECONCILER READS activeOnly:FALSE, and the harness used to
// walk only the true branch. The offline half is what its synthetic-death path acts
// on, so a defect confined to that branch leaves the exact-set invariant green while
// the real reconciler cannot see the devices it is supposed to repair.
func TestTheDepartedMustAppearInTheActiveOnlyFalseWalk(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedAll = append(append([]string{}, steady...), churn...) // departed dropped
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerAll)
	d := invariant(t, invs, InvPresenceReconcilerAll).Detail
	assert.Contains(t, d, departed[0])
	assert.Contains(t, d, "can never repair")
}

func TestTheActiveOnlyFalseWalkFailsOnAStranger(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedAll = append(obs.AssertedAll, "harness-pres-bg-00007")
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerAll)
}

func TestTheActiveOnlyFalseWalkFailsLoudlyOnAReadError(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedAllReadErr = "assertedDeviceStates page 2: connection reset"
	obs.AssertedAll = nil
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerAll)
	assert.Contains(t, invariant(t, invs, InvPresenceReconcilerAll).Detail, "did not complete")
}

// The two branches are separate invariants on purpose: a defect in one must not be
// masked by the other holding.
func TestTheTwoReconcilerBranchesFailIndependently(t *testing.T) {
	obs, steady, churn, departed := healthyObservations(3)
	obs.AssertedActive = []string{steady[0], churn[0]} // activeOnly:true under-reads
	invs := classifyPresence(obs, steady, churn, departed, healthyPresenceConfig(3), 5000)
	assertOnlyTheseFailed(t, invs, InvPresenceReconcilerExact)
}
