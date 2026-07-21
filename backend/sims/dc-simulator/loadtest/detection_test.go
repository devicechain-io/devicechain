// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-simulator/sim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- harness manifest ---------------------------------------------------------

func TestHarnessManifestShape(t *testing.T) {
	cfg := DetectionConfig{Seed: 7, SafetyProbes: 3, EdgeProbes: 2, BackgroundDevices: 40}.withDefaults()
	m := cfg.harnessManifest()

	require.Len(t, m.Profiles, 1)
	assert.Equal(t, HarnessProfileToken, m.Profiles[0].Token)
	require.Len(t, m.Profiles[0].Metrics, 1)
	assert.Equal(t, HarnessMetricKey, m.Profiles[0].Metrics[0].Key)
	require.Len(t, m.DeviceTypes, 1)
	assert.Equal(t, HarnessProfileToken, m.DeviceTypes[0].ProfileToken)

	require.Len(t, m.Populations, 3)
	assert.Equal(t, 3, m.Populations[0].Count)
	assert.Equal(t, 2, m.Populations[1].Count)
	assert.Equal(t, 40, m.Populations[2].Count)
	assert.Equal(t, 3+2+40, sim.DeviceCount(m))

	require.NoError(t, m.Validate())

	safety, edge, background, err := partitionHarnessDevices(m.Expand(m.Seed))
	require.NoError(t, err)
	assert.Len(t, safety, 3)
	assert.Len(t, edge, 2)
	assert.Len(t, background, 40)
}

func TestPartitionRejectsUnknownPrefix(t *testing.T) {
	_, _, _, err := partitionHarnessDevices([]sim.DeviceInstance{{Token: "not-a-harness-device"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches no harness population prefix")
}

// --- rule definition JSON -----------------------------------------------------

// The definition is decoded by event-processing with DisallowUnknownFields, so its
// keys must be EXACTLY the rules.Rule / Condition / Action json tags. This pins the
// shape we author (the authoritative check is the live publish, which rejects an
// unknown key); here we guard the structure + the raiseAlarm action that drives the
// durable alarm the oracle reconciles against.
func TestRuleDefinitionJSON(t *testing.T) {
	cfg := DetectionConfig{}.withDefaults()
	s, err := cfg.ruleDefinitionJSON()
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &got))

	assert.ElementsMatch(t, []string{"name", "type", "severity", "when", "actions"}, keysOf(got))
	assert.Equal(t, "threshold", got["type"])
	assert.Equal(t, HarnessSeverity, got["severity"])

	when, ok := got["when"].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"metric", "op", "threshold"}, keysOf(when))
	assert.Equal(t, HarnessMetricKey, when["metric"])
	assert.Equal(t, "gt", when["op"])
	assert.Equal(t, harnessThreshold, when["threshold"])

	actions, ok := got["actions"].([]any)
	require.True(t, ok)
	require.Len(t, actions, 1)
	a0, ok := actions[0].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"type", "raiseAlarm"}, keysOf(a0))
	assert.Equal(t, "raiseAlarm", a0["type"])
	ra, ok := a0["raiseAlarm"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, HarnessAlarmKey, ra["alarmKey"])
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// --- durable alarm oracle -----------------------------------------------------

// fakeSession scripts the tenant GraphQL Query surface: it returns a canned JSON
// response (or error) that gets unmarshalled into the caller's out, so the oracle's
// parsing + fail-closed behavior is testable with no cluster.
type fakeSession struct {
	respond func(vars map[string]any) (json.RawMessage, error)
}

func (f *fakeSession) Query(ctx context.Context, endpoint, query string, vars map[string]any, out any) error {
	raw, err := f.respond(vars)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func alarmsJSON(total int64, rows ...map[string]any) json.RawMessage {
	results := make([]map[string]any, 0, len(rows))
	results = append(results, rows...)
	body, _ := json.Marshal(map[string]any{
		"alarms": map[string]any{
			"results":    results,
			"pagination": map[string]any{"totalRecords": total},
		},
	})
	return body
}

func TestAlarmOracleSnapshotFound(t *testing.T) {
	o := &alarmOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return alarmsJSON(1, map[string]any{
			"state": "CLEARED", "raisedTime": "2026-07-21T10:00:00Z", "clearedTime": "2026-07-21T10:00:05Z", "severity": "MAJOR",
		}), nil
	}}}
	s, err := o.snapshot(context.Background(), "harness-edge-001")
	require.NoError(t, err)
	assert.True(t, s.Found)
	assert.Equal(t, "CLEARED", s.State)
	assert.Equal(t, "2026-07-21T10:00:05Z", s.ClearedAt)
	assert.True(t, s.wentActive(), "a MAJOR tier is durable proof the alarm went ACTIVE")
	assert.True(t, s.raisedAndCleared())
}

// A resolve-only TOMBSTONE (CLEARED + INDETERMINATE, raisedTime set) must NOT read as
// a completed lifecycle: it is the durable trace of a DROPPED raise whose resolve
// landed. This is the HIGH-1 hole — a bare raisedTime is set on every row, so only
// the tier distinguishes a real raise from a tombstone.
func TestAlarmOracleSnapshotTombstoneIsNotLive(t *testing.T) {
	o := &alarmOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return alarmsJSON(1, map[string]any{
			"state": "CLEARED", "raisedTime": "2026-07-21T10:00:00Z", "clearedTime": "2026-07-21T10:00:05Z", "severity": "INDETERMINATE",
		}), nil
	}}}
	s, err := o.snapshot(context.Background(), "harness-edge-001")
	require.NoError(t, err)
	assert.True(t, s.Found, "a tombstone row still exists (caught by no-spurious)")
	assert.False(t, s.wentActive(), "but an INDETERMINATE tier means it never genuinely raised")
	assert.False(t, s.raisedAndCleared(), "a tombstone is not a completed lifecycle")
}

func TestAlarmOracleSnapshotAbsentIsNotFound(t *testing.T) {
	o := &alarmOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return alarmsJSON(0), nil
	}}}
	s, err := o.snapshot(context.Background(), "harness-safe-001")
	require.NoError(t, err)
	assert.False(t, s.Found, "no row → never raised")
	assert.False(t, s.raisedAndCleared())
}

// A null totalRecords must be a fail-closed error, not a silent "no alarm" that the
// no-spurious check would read as a clean pass.
func TestAlarmOracleNullTotalIsError(t *testing.T) {
	o := &alarmOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"alarms":{"results":[],"pagination":{"totalRecords":null}}}`), nil
	}}}
	_, err := o.snapshot(context.Background(), "harness-safe-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null totalRecords")
}

func TestAlarmOracleQueryErrorPropagates(t *testing.T) {
	o := &alarmOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return nil, fmt.Errorf("boom")
	}}}
	_, err := o.snapshotAll(context.Background(), []string{"harness-edge-001"})
	require.Error(t, err, "a read error must surface, never a 0 that reads as 'no alarm'")
}

func TestAlarmSnapshotRaisedAndCleared(t *testing.T) {
	assert.False(t, alarmSnapshot{Found: false}.raisedAndCleared(), "absent = never raised")
	assert.False(t, alarmSnapshot{Found: true, Severity: harnessAlarmSeverityWire, State: alarmStateActive}.raisedAndCleared(), "stuck ACTIVE = falling edge dropped")
	assert.False(t, alarmSnapshot{Found: true, Severity: "INDETERMINATE", State: alarmStateCleared}.raisedAndCleared(), "CLEARED tombstone (dropped raise) is not a completed lifecycle")
	assert.True(t, alarmSnapshot{Found: true, Severity: harnessAlarmSeverityWire, State: alarmStateCleared}.raisedAndCleared())
}

// --- classifyAlarms (the pure heart) ------------------------------------------

func states(m map[string]alarmSnapshot) map[string]alarmSnapshot { return m }

func raisedCleared() alarmSnapshot {
	return alarmSnapshot{Found: true, Severity: harnessAlarmSeverityWire, State: alarmStateCleared}
}

func invByName(t *testing.T, invs []Invariant, name string) Invariant {
	t.Helper()
	for _, inv := range invs {
		if inv.Name == name {
			return inv
		}
	}
	t.Fatalf("invariant %q not asserted; got %v", name, invs)
	return Invariant{}
}

func passes(invs []Invariant) bool {
	for _, inv := range invs {
		if !inv.Passed {
			return false
		}
	}
	return len(invs) > 0
}

func TestClassifyCleanRunPasses(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{
		"harness-safe-001": {Found: false},
		"harness-safe-002": {Found: false},
	})
	edge := states(map[string]alarmSnapshot{
		"harness-edge-001": raisedCleared(),
		"harness-edge-002": raisedCleared(),
	})
	invs := classifyAlarms(safety, edge, true, cfg, 4000)
	assert.True(t, passes(invs), "a clean run passes: %v", invs)
}

func TestClassifySpuriousSafetyAlarmFails(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{
		"harness-safe-001": {Found: false},
		// a below-threshold probe that nonetheless raised a DURABLE alarm — the bug
		// this gate exists to catch, and which the live tap could have silently dropped.
		"harness-safe-002": {Found: true, Severity: harnessAlarmSeverityWire, State: alarmStateActive},
	})
	edge := states(map[string]alarmSnapshot{"harness-edge-001": raisedCleared()})
	invs := classifyAlarms(safety, edge, true, cfg, 4000)
	assert.False(t, invByName(t, invs, "no-spurious-alarm").Passed)
	assert.True(t, invByName(t, invs, "alarm-liveness").Passed)
}

func TestClassifyDroppedRaiseFailsLiveness(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{"harness-safe-001": {Found: false}})
	edge := states(map[string]alarmSnapshot{
		"harness-edge-001": raisedCleared(),
		"harness-edge-002": {Found: false}, // never raised — a dropped rising edge under load
	})
	invs := classifyAlarms(safety, edge, true, cfg, 4000)
	assert.False(t, invByName(t, invs, "alarm-liveness").Passed, "an edge probe that never raised fails liveness")
}

// HIGH-1 regression: a dropped RAISE whose resolve landed leaves a CLEARED tombstone
// (INDETERMINATE tier). The gate must NOT count that as a completed lifecycle — a bare
// raisedTime is set on every row, so only the tier proves the alarm went ACTIVE.
func TestClassifyTombstoneFailsLiveness(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{"harness-safe-001": {Found: false}})
	edge := states(map[string]alarmSnapshot{
		"harness-edge-001": {Found: true, Severity: "INDETERMINATE", State: alarmStateCleared}, // resolve-only tombstone
	})
	invs := classifyAlarms(safety, edge, true, cfg, 4000)
	assert.False(t, invByName(t, invs, "alarm-liveness").Passed, "a CLEARED tombstone (dropped raise) must fail liveness, not pass on a vacuous raisedTime")
}

func TestClassifyStuckActiveFailsLiveness(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{"harness-safe-001": {Found: false}})
	edge := states(map[string]alarmSnapshot{
		"harness-edge-001": {Found: true, Severity: harnessAlarmSeverityWire, State: alarmStateActive}, // falling edge dropped → stuck ACTIVE
	})
	invs := classifyAlarms(safety, edge, true, cfg, 4000)
	assert.False(t, invByName(t, invs, "alarm-liveness").Passed, "an alarm stuck ACTIVE fails liveness")
}

// A dead detection/alarm path leaves every edge alarm absent. Even with clean safety
// probes (vacuously no spurious), liveness fails — the durable alarm is the liveness
// witness, so silence can never pass. This is the "check that cannot fail" guard.
func TestClassifyDeadPathFailsViaLiveness(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{"harness-safe-001": {Found: false}})
	edge := states(map[string]alarmSnapshot{
		"harness-edge-001": {Found: false},
		"harness-edge-002": {Found: false},
	})
	invs := classifyAlarms(safety, edge, true, cfg, 4000)
	assert.True(t, invByName(t, invs, "no-spurious-alarm").Passed, "no alarms → no spurious (vacuously)")
	assert.False(t, invByName(t, invs, "alarm-liveness").Passed, "but no edge alarm at all fails liveness")
	assert.False(t, passes(invs))
}

// The await timeout must gate: even if every snapshot happens to look live, a
// reached=false means the durable await never observed them all settle — treat the
// disagreement as a failure, never a silent pass.
func TestClassifyNotReachedFailsLiveness(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{"harness-safe-001": {Found: false}})
	edge := states(map[string]alarmSnapshot{"harness-edge-001": raisedCleared()})
	invs := classifyAlarms(safety, edge, false, cfg, 4000)
	assert.False(t, invByName(t, invs, "alarm-liveness").Passed, "a timeout below target is never a silent pass")
}

func TestClassifyBelowFloorFails(t *testing.T) {
	cfg := DetectionConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := states(map[string]alarmSnapshot{"harness-safe-001": {Found: false}})
	edge := states(map[string]alarmSnapshot{"harness-edge-001": raisedCleared()})
	invs := classifyAlarms(safety, edge, true, cfg, 12)
	assert.False(t, invByName(t, invs, "load-floor").Passed, "a run that never stressed the path cannot certify no-spurious")
}

// --- config validation --------------------------------------------------------

func TestDetectionConfigValidate(t *testing.T) {
	require.NoError(t, DetectionConfig{}.withDefaults().Validate())

	err := DetectionConfig{SafetyProbes: 1, EdgeProbes: 0, Cycles: 1}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "edge probe")

	err = DetectionConfig{SafetyProbes: 0, EdgeProbes: 1, Cycles: 1}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safety probe")
}

func TestDetectionReportPassedEmptyIsNotPass(t *testing.T) {
	r := &DetectionReport{}
	assert.False(t, r.Passed(), "an empty invariant set proves nothing")
	assert.True(t, strings.Contains(r.Human(), "FAIL"))
}
