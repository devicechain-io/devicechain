// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- topology -----------------------------------------------------------------

func TestBatchManifestShape(t *testing.T) {
	cfg := BatchConfig{Seed: 5, TargetDevices: 6, BystanderDevices: 2, PoisonDevices: 1, BackgroundDevices: 10}.withDefaults()
	m := cfg.harnessManifest()

	require.Len(t, m.Profiles, 2)
	require.Len(t, m.DeviceTypes, 2)
	require.Len(t, m.Populations, 4)
	// The poison cohort must sit on the poison PROFILE, not merely on a second type of
	// the same one — the vocabulary is a property of the profile.
	byToken := map[string]string{}
	for _, dt := range m.DeviceTypes {
		byToken[dt.Token] = dt.ProfileToken
	}
	assert.Equal(t, HarnessBatchProfileToken, byToken[HarnessBatchDeviceTypeToken])
	assert.Equal(t, HarnessBatchPoisonProfileToken, byToken[HarnessBatchPoisonDeviceTypeToken])

	cohorts, err := partitionByPrefixes(m.Expand(m.Seed),
		[]string{batchTargetTokenPrefix, batchBystanderTokenPrefix, batchPoisonTokenPrefix, batchBgTokenPrefix})
	require.NoError(t, err)
	assert.Len(t, cohorts[0], 6)
	assert.Len(t, cohorts[1], 2)
	assert.Len(t, cohorts[2], 1)
	assert.Len(t, cohorts[3], 10)
}

// 🔴🔴 THE FINDING THIS HARNESS WAS REDESIGNED AROUND.
//
// The obvious poison device is one whose profile declares NO commands. It does not
// work: a profile with an empty vocabulary is UNCONSTRAINED, and an unconstrained
// profile accepts any command key — so that device would be admitted, the whole-batch
// refusal would never fire, and the fail-closed invariant would be a check that cannot
// fail. The poison profile must therefore publish a command of its own, and it must be
// a DIFFERENT one.
func TestThePoisonProfileIsConstrainedByADifferentCommand(t *testing.T) {
	specs := batchVocabularySpecs()

	byProfile := map[string]batchDefSpec{}
	for _, s := range specs {
		require.NotEmpty(t, s.Key, "a definition with no command key would not constrain anything")
		require.NotEmpty(t, s.Token)
		byProfile[s.Profile] = s
	}

	target, ok := byProfile[HarnessBatchProfileToken]
	require.True(t, ok, "the target profile must publish the batch command, or every target is refused")
	poison, ok := byProfile[HarnessBatchPoisonProfileToken]
	require.True(t, ok, "the poison profile must publish SOMETHING, or it is unconstrained and accepts the batch command")

	assert.Equal(t, HarnessBatchCommandKey, target.Key)
	assert.NotEqual(t, HarnessBatchCommandKey, poison.Key,
		"the poison profile publishing the batch command would make it a valid target, not a poison one")
	assert.NotEqual(t, target.Token, poison.Token, "two definitions cannot share one token")
}

// --- config -------------------------------------------------------------------

func TestBatchConfigRefusesAOneDeviceBatch(t *testing.T) {
	err := BatchConfig{TargetDevices: 1}.withDefaults().Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least two target devices")
}

func TestBatchConfigRefusesASerialReplay(t *testing.T) {
	err := BatchConfig{Replays: 1}.withDefaults().Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least two concurrent replays")
}

func TestBatchConfigRefusesAMissingCohort(t *testing.T) {
	for name, cfg := range map[string]BatchConfig{
		"bystanders": {BystanderDevices: -1},
		"poison":     {PoisonDevices: -1},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, cfg.withDefaults().Validate())
		})
	}
}

func TestBatchConfigRefusesAnUnknownControl(t *testing.T) {
	err := BatchConfig{Control: "burn-it-down"}.withDefaults().Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown control")
}

func TestBatchConfigFillsOnlyUnsetFields(t *testing.T) {
	cfg := BatchConfig{TargetDevices: 30}.withDefaults()
	assert.Equal(t, 30, cfg.TargetDevices)
	assert.Equal(t, DefaultBatchBystanders, cfg.BystanderDevices)
	assert.Equal(t, DefaultBatchReplays, cfg.Replays)
}

// --- pure helpers -------------------------------------------------------------

// 🔴 A COUNT IS NOT A MEMBERSHIP. Two rows for one target and none for another counts
// correctly and is a device that never got its actuation plus one that got it twice.
func TestDiffMultisetSeparatesADuplicateFromAnOmission(t *testing.T) {
	missing, extra, dup := diffMultiset([]string{"a", "b", "c"}, []string{"a", "a", "c"})
	assert.Equal(t, []string{"b"}, missing)
	assert.Empty(t, extra)
	require.Len(t, dup, 1)
	assert.Contains(t, dup[0], "a")

	// The count of the two sides is IDENTICAL, which is the whole point.
	assert.Equal(t, 3, len([]string{"a", "a", "c"}))
}

func TestDiffMultisetIsCleanOnAnExactMatch(t *testing.T) {
	missing, extra, dup := diffMultiset([]string{"a", "b"}, []string{"b", "a"})
	assert.Empty(t, missing)
	assert.Empty(t, extra)
	assert.Empty(t, dup)
}

func TestDiffMultisetNamesAnUntargetedDevice(t *testing.T) {
	_, extra, _ := diffMultiset([]string{"a"}, []string{"a", "stranger"})
	require.Len(t, extra, 1)
	assert.Contains(t, extra[0], "stranger")
}

// 🔴 A STABILITY TEST NEEDS ENOUGH KEYS TO BE ONE. Go randomizes map iteration, so a
// three-key tally rendered in map order lands sorted about one run in six — which
// makes the test a coin flip rather than a check, and it scored an unsorted
// implementation as correct. Eight keys and repeated calls put that below one in a
// million per run.
func TestSortedTallyIsStableAcrossMapIterationOrder(t *testing.T) {
	tally := map[string]int{
		"SUCCESSFUL": 3, "QUEUED": 1, "SENT": 2, "HELD": 4,
		"PARKED": 5, "CANCELLED": 6, "EXPIRED": 7, "TIMEOUT": 8,
	}
	want := "{CANCELLED=6 EXPIRED=7 HELD=4 PARKED=5 QUEUED=1 SENT=2 SUCCESSFUL=3 TIMEOUT=8}"
	for i := 0; i < 20; i++ {
		assert.Equal(t, want, sortedTally(tally), "call %d rendered a different order", i)
	}
	assert.Equal(t, "{}", sortedTally(nil), "an empty tally still renders")
}

// --- classification (pure) ----------------------------------------------------

// healthyBatch is one clean fleet write. Tests mutate a copy to drive a single
// invariant into its failure.
func healthyBatch() (batchObservations, []string, []string) {
	targets := []string{"harness-batch-tgt-001", "harness-batch-tgt-002", "harness-batch-tgt-003"}
	bystanders := []string{"harness-batch-by-001", "harness-batch-by-002"}

	obs := batchObservations{
		Batch:           batchRecord{Created: true, Token: "harness-batch-7", Resolved: 3, Accepted: 3},
		Receipts:        map[string]int{targets[0]: 1, targets[1]: 1, targets[2]: 1},
		FanoutRows:      append([]string{}, targets...),
		FanoutTotal:     3,
		Statuses:        map[string]int{cmdStatusSuccessful: 3},
		Successful:      3,
		SettleReached:   true,
		BystanderCounts: map[string]int{bystanders[0]: 0, bystanders[1]: 0},
		Replays: []batchRecord{
			{Created: true, Token: "harness-batch-7", Resolved: 3, Accepted: 3},
			{Created: true, Token: "harness-batch-7", Resolved: 3, Accepted: 3},
		},
		ReplayTotalAfter: 3,
		Refused:          batchRecord{Created: false},
		RefusedCode:      batchRejectPartialRefused,
		RefusedRefusals:  []batchRefusal{{DeviceToken: "harness-batch-poison-01", Code: batchRefusalNotInVocab}},
		TargetDelta:      map[string]int{targets[0]: 0, targets[1]: 0, targets[2]: 0},
	}
	return obs, targets, bystanders
}

func healthyBatchConfig() BatchConfig {
	return BatchConfig{TargetDevices: 3, BystanderDevices: 2, PoisonDevices: 1}.withDefaults()
}

func TestAHealthyFleetWritePassesEveryInvariant(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	require.NoError(t, requireInvariantSet(BatchInvariants, invs),
		"the classifier must produce exactly the declared set — a count alone would not say WHICH one drifted")
	assertOnlyTheseFailed(t, invs)
}

func TestBatchLoadFloorFails(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 3)
	assertOnlyTheseFailed(t, invs, InvBatchLoadFloor)
}

// 🔴 THE MEMBERSHIP CASE. The count is right, the record is right, and one device got
// two commands while another got none.
func TestFanoutFailsOnADuplicateThatBalancesAnOmission(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.FanoutRows = []string{targets[0], targets[0], targets[2]}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete)
	d := invariant(t, invs, InvBatchFanoutComplete).Detail
	assert.Contains(t, d, "no command row for: "+targets[1])
	assert.Contains(t, d, "more than one command row for")
}

// The DETAIL is asserted, not only the verdict. A refused batch fails these three
// invariants either way — every downstream number is zero — but "createCommandBatch
// returned a rejection" and a cascade of count mismatches send a reader to completely
// different places, and only the first one is true.
func TestFanoutFailsWhenTheBatchWasRejected(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Batch = batchRecord{Created: false}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete, InvBatchRoundTrip, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchFanoutComplete).Detail, "returned a rejection rather than a batch")
	assert.Contains(t, invariant(t, invs, InvBatchRoundTrip).Detail, "no batch was created")
	assert.Contains(t, invariant(t, invs, InvBatchReplayIdempotent).Detail, "no batch to replay")
}

// 🔴 A row for a device the batch never named. The count can be perfectly right — a
// stranger in, a target out — so this is the same class as the duplicate case and
// needs its own observation, not a diffMultiset unit test.
func TestFanoutFailsOnARowForAnUntargetedDevice(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.FanoutRows = []string{targets[0], targets[1], "harness-batch-bg-00042"}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete)
	d := invariant(t, invs, InvBatchFanoutComplete).Detail
	assert.Contains(t, d, "devices the batch did not target")
	assert.Contains(t, d, "harness-batch-bg-00042")
	assert.Contains(t, d, "no command row for: "+targets[2])
}

func TestFanoutFailsWhenTheRecordAcceptedFewerThanItTargeted(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Batch.Accepted = 2
	obs.FanoutRows = targets[:2]
	obs.FanoutTotal = 2
	obs.Statuses = map[string]int{cmdStatusSuccessful: 2}
	obs.Successful = 2
	obs.ReplayTotalAfter = 2
	obs.Replays = []batchRecord{{Created: true, Token: obs.Batch.Token, Resolved: 3, Accepted: 2}}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete)
	assert.Contains(t, invariant(t, invs, InvBatchFanoutComplete).Detail, "accepted 2 device(s), want 3")
}

// totalRecords is a COUNT(*); the rows are a page. Comparing a TRUNCATED page against
// the target set would report a paging problem as a fan-out omission.
func TestFanoutFailsLoudlyWhenThePageWasTruncated(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.FanoutRows = targets[:2]
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete, InvBatchRoundTrip)
	assert.Contains(t, invariant(t, invs, InvBatchFanoutComplete).Detail, "truncated")
}

func TestFanoutFailsWhenTheRecordAndTheRowsDisagree(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Batch.Accepted = 5
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchFanoutComplete).Detail, "3 command row(s) exist")
}

func TestRoundTripFailsOnAStrandedCommand(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Statuses = map[string]int{cmdStatusSuccessful: 2, cmdStatusSent: 1}
	obs.Successful = 2
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRoundTrip)
	assert.Contains(t, invariant(t, invs, InvBatchRoundTrip).Detail, "SENT=1")
}

// A HELD row is a command withheld for a device the platform believes absent. Every
// target here is connected, so it is a finding, not a state to pass over.
func TestRoundTripFailsOnAHeldCommand(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Statuses = map[string]int{cmdStatusSuccessful: 2, "HELD": 1}
	obs.Successful = 2
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRoundTrip)
	assert.Contains(t, invariant(t, invs, InvBatchRoundTrip).Detail, "HELD=1")
}

// Every row reads terminal but the wait never confirmed it: trust the timeout, not
// the later snapshot.
func TestRoundTripFailsWhenTheSettleNeverConfirmed(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.SettleReached = false
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRoundTrip)
	assert.Contains(t, invariant(t, invs, InvBatchRoundTrip).Detail, "never confirmed")
}

func TestTargetOnlyFailsWhenAByStanderWasWrittenTo(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.BystanderCounts[bystanders[1]] = 1
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchTargetOnly)
	assert.Contains(t, invariant(t, invs, InvBatchTargetOnly).Detail, bystanders[1])
}

func TestReplayFailsWhenAReplayWasRejected(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Replays[1] = batchRecord{Created: false}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchReplayIdempotent).Detail, "REJECTED")
}

func TestReplayFailsWhenTheBatchWasToppedUp(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Replays[0].Accepted = 4
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchReplayIdempotent).Detail, "topped up")
}

func TestReplayFailsWhenAReplayAnsweredWithADifferentBatch(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Replays[0].Token = "some-other-batch"
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchReplayIdempotent)
}

// 🔴 The response can be perfectly idempotent while the ROWS are not: a replay that
// answered with the original record and created a second set of commands anyway.
func TestReplayFailsWhenTheRowsGrewDespiteAnIdenticalResponse(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.ReplayTotalAfter = 6
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchReplayIdempotent).Detail, "holds 6 command row(s)")
}

func TestReplayFailsWhenNoReplayWasIssued(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Replays = nil
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchReplayIdempotent).Detail, "no replay was issued")
}

func TestReplayFailsWhenAReplayErroredOutright(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.ReplayErrs = []string{"connection reset"}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchReplayIdempotent)
	assert.Contains(t, invariant(t, invs, InvBatchReplayIdempotent).Detail, "connection reset")
}

// 🔴 THE ADMITTED-POISON CASE. Its likeliest cause is a cluster misconfiguration — a
// nil batch validator SKIPS the vocabulary gate rather than failing it — so the
// detail must point there before it accuses the fan-out. A gate that spent a day
// hunting a product defect for an unwired service secret has cost more than it saved.
func TestRefusalFailsWithACauseHintWhenThePoisonWasAdmitted(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Refused = batchRecord{Created: true, Token: "harness-batch-7-refused", Resolved: 4, Accepted: 4}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRefusedWhole)
	d := invariant(t, invs, InvBatchRefusedWhole).Detail
	assert.Contains(t, d, "service secret")
	assert.Contains(t, d, "SKIP")
}

func TestRefusalFailsOnTheWrongRejectionCode(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.RefusedCode = "BATCH_TOO_LARGE"
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRefusedWhole)
	assert.Contains(t, invariant(t, invs, InvBatchRefusedWhole).Detail, "BATCH_TOO_LARGE")
}

// A refusal that does not name its offender leaves an operator bisecting a fleet.
func TestRefusalFailsWhenItNamesNoPoisonDevice(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.RefusedRefusals = nil
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRefusedWhole)
	assert.Contains(t, invariant(t, invs, InvBatchRefusedWhole).Detail, "names no poison device")
}

func TestRefusalFailsWhenThePoisonWasRefusedForTheWrongReason(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.RefusedRefusals = []batchRefusal{{DeviceToken: "harness-batch-poison-01", Code: "DEVICE_NOT_FOUND"}}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRefusedWhole)
	assert.Contains(t, invariant(t, invs, InvBatchRefusedWhole).Detail, "DEVICE_NOT_FOUND")
}

func TestRefusalFailsWhenARecordSurvived(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.RefusedRecordSeen = true
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRefusedWhole)
	assert.Contains(t, invariant(t, invs, InvBatchRefusedWhole).Detail, "must leave no record")
}

// The delta is the assertion, not an absolute: the targets legitimately carry the
// fan-out's commands, and a refused batch must add none.
func TestRefusalFailsWhenATargetGainedACommandAnyway(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.TargetDelta[targets[1]] = 1
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRefusedWhole)
	assert.Contains(t, invariant(t, invs, InvBatchRefusedWhole).Detail, targets[1])
}

// 🔴 THE CONTROL, TIED TO THE CLASSIFIER RATHER THAN TO MY BELIEF ABOUT IT.
//
// The expected failure set is a claim about what leaving one target deaf DOES. The
// only honest check is to build that scenario out of observations and ask the
// classifier; reading the expectation out of batchControlExpectations and asserting it
// against itself would pass no matter which invariants the perturbation really flips.
func TestADeafDeviceFlipsExactlyTheControlsExpectedSet(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	// The deaf device's row IS created — the fan-out is unaffected — and nothing ever
	// answers it, so it is stranded at SENT.
	obs.Statuses = map[string]int{cmdStatusSuccessful: 2, cmdStatusSent: 1}
	obs.Successful = 2
	obs.SettleReached = false
	// The deaf device is the one with no receiver, so it receives nothing — and the
	// receipt invariant EXPECTS that, which is what keeps the control's expected set a
	// single name.
	obs.Deaf = targets[2]
	obs.Receipts = map[string]int{targets[0]: 1, targets[1]: 1}

	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchRoundTrip)

	ok, detail := evaluateBatchControl(ControlDeafDevice, invs)
	assert.True(t, ok, "the declared expected set must be what the classifier actually produces: %s", detail)
}

func TestBatchControlIsViolatedWhenNothingFlipped(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	ok, detail := evaluateBatchControl(ControlDeafDevice, invs)
	assert.False(t, ok)
	assert.Contains(t, detail, InvBatchRoundTrip)
}

func TestBatchControlIsViolatedWhenSomethingElseFlippedToo(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Successful = 2
	obs.BystanderCounts[bystanders[0]] = 1
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	ok, detail := evaluateBatchControl(ControlDeafDevice, invs)
	assert.False(t, ok)
	assert.Contains(t, detail, InvBatchTargetOnly)
}

// --- the oracle's reads -------------------------------------------------------

func batchMutationJSON(t *testing.T, body map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"createCommandBatch": body})
	require.NoError(t, err)
	return raw
}

func TestOracleReadsACreatedBatch(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchMutationJSON(t, map[string]any{
			"batch": map[string]any{"token": "b1", "resolved": 4, "accepted": 4, "refusals": []any{}},
		}), nil
	}}}
	rec, code, refusals, err := o.create(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.True(t, rec.Created)
	assert.Equal(t, "b1", rec.Token)
	assert.Equal(t, 4, rec.Accepted)
	assert.Empty(t, code)
	assert.Empty(t, refusals)
}

// A PARTIAL fan-out returns a record AND names the devices it left out. Dropping them
// would leave a caller to bisect the fleet by hand to learn which.
func TestOracleKeepsTheRefusalsOfAPartialFanout(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchMutationJSON(t, map[string]any{
			"batch": map[string]any{"token": "b1", "resolved": 4, "accepted": 3, "refusals": []any{
				map[string]any{"deviceToken": "d4", "code": "COMMAND_NOT_IN_VOCABULARY", "reason": "no"},
			}},
		}), nil
	}}}
	rec, _, refusals, err := o.create(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.True(t, rec.Created)
	require.Len(t, refusals, 1)
	assert.Equal(t, "d4", refusals[0].DeviceToken)
}

func TestOracleReadsARejection(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchMutationJSON(t, map[string]any{
			"rejection": map[string]any{
				"code": "BATCH_PARTIAL_REFUSED", "reason": "one device cannot receive it", "resolved": 4,
				"refusals":      []any{map[string]any{"deviceToken": "p1", "code": "COMMAND_NOT_IN_VOCABULARY", "reason": "no"}},
				"refusalCounts": []any{map[string]any{"code": "COMMAND_NOT_IN_VOCABULARY", "count": 1}},
			},
		}), nil
	}}}
	rec, code, refusals, err := o.create(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.False(t, rec.Created)
	assert.Equal(t, batchRejectPartialRefused, code)
	require.Len(t, refusals, 1)
	assert.Equal(t, "p1", refusals[0].DeviceToken)
}

// 🔴 Exactly one of the two is non-null. Neither is not "an empty batch" — it is a
// response this harness cannot interpret, and folding it into either branch would
// invent an answer the server never gave.
func TestOracleFailsClosedWhenNeitherHalfIsPresent(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchMutationJSON(t, map[string]any{}), nil
	}}}
	_, _, _, err := o.create(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a batch nor a rejection")
}

func TestOracleFailsClosedWhenBothHalvesArePresent(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchMutationJSON(t, map[string]any{
			"batch":     map[string]any{"token": "b1", "resolved": 1, "accepted": 1, "refusals": []any{}},
			"rejection": map[string]any{"code": "BATCH_TOO_LARGE", "reason": "no", "refusals": []any{}, "refusalCounts": []any{}},
		}), nil
	}}}
	_, _, _, err := o.create(context.Background(), map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BOTH")
}

func batchRowsJSON(t *testing.T, total int64, rows ...map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"commands": map[string]any{
		"results":    rows,
		"pagination": map[string]any{"totalRecords": total},
	}})
	require.NoError(t, err)
	return raw
}

func TestOracleRowsKeepsTheDeviceMultiset(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchRowsJSON(t, 3,
			map[string]any{"token": "c1", "deviceToken": "d1", "status": "SUCCESSFUL"},
			map[string]any{"token": "c2", "deviceToken": "d1", "status": "SUCCESSFUL"},
			map[string]any{"token": "c3", "deviceToken": "d2", "status": "SENT"},
		), nil
	}}}
	r, err := o.rows(context.Background(), "b1")
	require.NoError(t, err)
	assert.Equal(t, []string{"d1", "d1", "d2"}, r.Devices, "the duplicate must survive into the classifier")
	assert.Equal(t, 3, r.Total)
	assert.Equal(t, 2, r.Successful)
	assert.Equal(t, map[string]int{"SUCCESSFUL": 2, "SENT": 1}, r.Statuses)
}

// A null totalRecords is a fail-closed error, not a silent zero a fan-out check would
// read as "the batch created nothing" and blame the platform for.
func TestOracleRowsFailsClosedOnANullTotal(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"commands":{"results":[],"pagination":{"totalRecords":null}}}`), nil
	}}}
	_, err := o.rows(context.Background(), "b1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null totalRecords")
}

func TestOracleRecordExists(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"a refused token has no record": {`{"commandBatchesByToken":[]}`, false},
		"a created token has one":       {`{"commandBatchesByToken":[{"token":"b1","accepted":3,"resolved":3}]}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
				return json.RawMessage(tc.body), nil
			}}}
			got, err := o.recordExists(context.Background(), "b1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOracleRecordExistsPropagatesAReadError(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return nil, fmt.Errorf("connection reset")
	}}}
	_, err := o.recordExists(context.Background(), "b1")
	require.Error(t, err)
}

// A batch that reports an acceptance and creates NO rows must fail both the fan-out
// and the round trip. "Every one of zero commands is terminal" is true and worthless.
func TestABatchThatCreatedNoRowsFailsBothFanoutAndRoundTrip(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.FanoutRows = nil
	obs.FanoutTotal = 0
	obs.Statuses = map[string]int{}
	obs.Successful = 0
	obs.ReplayTotalAfter = 0
	obs.Receipts = map[string]int{} // nothing was created, so nothing was received either
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchFanoutComplete, InvBatchRoundTrip, InvBatchReplayIdempotent, InvBatchDevicesReceived)
	assert.Contains(t, invariant(t, invs, InvBatchRoundTrip).Detail, "no command rows")
}

// --- the settle wait ----------------------------------------------------------

func TestAwaitBatchSettledConfirmsWhenEveryRowIsTerminal(t *testing.T) {
	calls := 0
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return batchRowsJSON(t, 2,
				map[string]any{"token": "c1", "deviceToken": "d1", "status": "SENT"},
				map[string]any{"token": "c2", "deviceToken": "d2", "status": "SUCCESSFUL"}), nil
		}
		return batchRowsJSON(t, 2,
			map[string]any{"token": "c1", "deviceToken": "d1", "status": "SUCCESSFUL"},
			map[string]any{"token": "c2", "deviceToken": "d2", "status": "SUCCESSFUL"}), nil
	}}}
	cfg := BatchConfig{Poll: time.Millisecond, Timeout: 5 * time.Second}
	rows, reached, everRead := o.awaitBatchSettled(context.Background(), "b1", 2, cfg)
	assert.True(t, reached)
	assert.True(t, everRead)
	assert.Equal(t, 2, rows.Successful)
}

// 🔴 "THE BATCH CREATED NO ROWS" AND "THIS ORACLE NEVER MANAGED TO READ" FOLD TO THE
// SAME ZERO VALUE AND MEAN OPPOSITE THINGS — one is a fan-out defect, the other is the
// harness being blind. Without the third return the run reports exit 1, "the batch
// created no command rows", for a command-delivery outage.
func TestAwaitBatchSettledReportsThatItNeverManagedToRead(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return nil, fmt.Errorf("connection reset")
	}}}
	cfg := BatchConfig{Poll: time.Millisecond, Timeout: 10 * time.Millisecond}
	rows, reached, everRead := o.awaitBatchSettled(context.Background(), "b1", 2, cfg)
	assert.False(t, reached)
	assert.False(t, everRead, "no read ever succeeded, and the caller has to be able to tell")
	assert.Empty(t, rows.Devices)
}

// One clean read followed by failures still counts as readable: the oracle DID see the
// batch, so a non-terminal verdict is a real observation rather than blindness.
func TestAwaitBatchSettledRemembersASingleCleanRead(t *testing.T) {
	calls := 0
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		calls++
		if calls == 1 {
			return batchRowsJSON(t, 2,
				map[string]any{"token": "c1", "deviceToken": "d1", "status": "SENT"},
				map[string]any{"token": "c2", "deviceToken": "d2", "status": "SENT"}), nil
		}
		return nil, fmt.Errorf("connection reset")
	}}}
	cfg := BatchConfig{Poll: time.Millisecond, Timeout: 15 * time.Millisecond}
	_, reached, everRead := o.awaitBatchSettled(context.Background(), "b1", 2, cfg)
	assert.False(t, reached)
	assert.True(t, everRead)
}

// A timeout is NOT concluded as reached: it is a real undelivered or unanswered
// command, and the classifier must be told so rather than shown a hopeful true.
func TestAwaitBatchSettledDoesNotConcludeOnATimeout(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchRowsJSON(t, 2,
			map[string]any{"token": "c1", "deviceToken": "d1", "status": "SENT"},
			map[string]any{"token": "c2", "deviceToken": "d2", "status": "SUCCESSFUL"}), nil
	}}}
	cfg := BatchConfig{Poll: time.Millisecond, Timeout: 10 * time.Millisecond}
	rows, reached, _ := o.awaitBatchSettled(context.Background(), "b1", 2, cfg)
	assert.False(t, reached)
	assert.Equal(t, 1, rows.Successful, "the last observation is still returned, for the report")
}

// 🔴 An EMPTY batch is not a settled one. Without the row-count floor, a query that
// returned nothing would satisfy "every row is terminal" vacuously and the wait would
// return immediately with a clean bill of health for a fan-out that created nothing.
func TestAwaitBatchSettledDoesNotSettleOnAnEmptyBatch(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchRowsJSON(t, 0), nil
	}}}
	cfg := BatchConfig{Poll: time.Millisecond, Timeout: 10 * time.Millisecond}
	_, reached, _ := o.awaitBatchSettled(context.Background(), "b1", 0, cfg)
	assert.False(t, reached, "zero rows is never a completed round trip")
}

// A row count that does not match what the record accepted is not settled either —
// the wait is for the batch's OWN rows, not for whatever the query happens to return.
func TestAwaitBatchSettledWaitsForTheAcceptedCount(t *testing.T) {
	o := &batchOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return batchRowsJSON(t, 1, map[string]any{"token": "c1", "deviceToken": "d1", "status": "SUCCESSFUL"}), nil
	}}}
	cfg := BatchConfig{Poll: time.Millisecond, Timeout: 10 * time.Millisecond}
	_, reached, _ := o.awaitBatchSettled(context.Background(), "b1", 3, cfg)
	assert.False(t, reached)
}

// --- the wire witness ---------------------------------------------------------

// 🔴 A DURABLE `SUCCESSFUL` DOES NOT SAY WHICH DEVICE ANSWERED. A response carries a
// command token and no device identity, and a device's JWT grants publish on the
// tenant-wide responses subject — so if dispatch mis-routes device A's envelope onto
// device B's topic, B answering it drives A's row to SUCCESSFUL while A never
// actuates, and every durable-state invariant reads green.
func TestAMisroutedCommandFailsTheWireWitness(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Misrouted = 1
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchDevicesReceived)
	d := invariant(t, invs, InvBatchDevicesReceived).Detail
	assert.Contains(t, d, "mis-route")
	assert.Contains(t, d, "may have been written by a device that is not the one the command names")
}

// The scenario that motivates the whole invariant: every durable row is SUCCESSFUL,
// and one target never saw its command.
func TestASilentTargetFailsEvenWhenEveryDurableRowIsSuccessful(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	delete(obs.Receipts, targets[1])
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchDevicesReceived)
	assert.True(t, invariant(t, invs, InvBatchRoundTrip).Passed,
		"the durable half is green — which is exactly why the wire half has to exist")
	assert.Contains(t, invariant(t, invs, InvBatchDevicesReceived).Detail, targets[1])
}

// At-least-once redelivery can only inflate the tally, so counting DISTINCT tokens
// and demanding at least one is safe against it.
func TestRedeliveryDoesNotFailTheWireWitness(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Receipts[targets[0]] = 3
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs)
}

// A control that did not arm — the deaf device received something anyway — must be
// caught, or the control certifies nothing.
func TestADeafDeviceThatReceivedAnythingFailsTheWireWitness(t *testing.T) {
	obs, targets, bystanders := healthyBatch()
	obs.Deaf = targets[2]
	obs.Receipts = map[string]int{targets[0]: 1, targets[1]: 1, targets[2]: 1}
	invs := classifyBatch(obs, targets, bystanders, healthyBatchConfig(), 5000)
	assertOnlyTheseFailed(t, invs, InvBatchDevicesReceived)
	assert.Contains(t, invariant(t, invs, InvBatchDevicesReceived).Detail, "the control did not arm")
}
