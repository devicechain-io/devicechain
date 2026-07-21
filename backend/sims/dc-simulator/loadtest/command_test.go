// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- harness manifest ---------------------------------------------------------

func TestCommandManifestShape(t *testing.T) {
	cfg := CommandConfig{Seed: 7, SafetyProbes: 3, CommandProbes: 2, BackgroundDevices: 40}.withDefaults()
	m := cfg.harnessManifest()

	require.Len(t, m.Profiles, 1)
	assert.Equal(t, HarnessCommandProfileToken, m.Profiles[0].Token)
	require.Len(t, m.Profiles[0].Metrics, 1)
	assert.Equal(t, HarnessMetricKey, m.Profiles[0].Metrics[0].Key)
	require.Len(t, m.DeviceTypes, 1)
	assert.Equal(t, HarnessCommandProfileToken, m.DeviceTypes[0].ProfileToken)

	require.Len(t, m.Populations, 3)
	assert.Equal(t, 3, m.Populations[0].Count)
	assert.Equal(t, 2, m.Populations[1].Count)
	assert.Equal(t, 40, m.Populations[2].Count)

	// The three roles partition cleanly by their distinct prefixes.
	safety, probes, background, err := partitionByPrefix(m.Expand(m.Seed), cmdSafeTokenPrefix, cmdProbeTokenPrefix, cmdBgTokenPrefix)
	require.NoError(t, err)
	assert.Len(t, safety, 3)
	assert.Len(t, probes, 2)
	assert.Len(t, background, 40)
}

// --- rule definition JSON -----------------------------------------------------

// The definition is decoded by event-processing with DisallowUnknownFields, so its
// keys must be EXACTLY the rules.Rule / Condition / Action json tags. The sendCommand
// action nests {command} (NOT commandKey/name/parameters), carries no payload (a
// no-argument command), and the rule has no severity (valid for a rule with no
// raiseAlarm). This pins the shape we author; the authoritative check is the live
// publish, which rejects an unknown key.
func TestCommandRuleDefinitionJSON(t *testing.T) {
	cfg := CommandConfig{}.withDefaults()
	raw, err := cfg.ruleDefinitionJSON()
	require.NoError(t, err)

	var def map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &def))
	assert.Equal(t, "threshold", def["type"])
	assert.NotContains(t, def, "severity", "a sendCommand-only rule authors no severity")

	when := def["when"].(map[string]any)
	assert.Equal(t, HarnessMetricKey, when["metric"])
	assert.Equal(t, "gt", when["op"])
	assert.Equal(t, harnessThreshold, when["threshold"])

	actions := def["actions"].([]any)
	require.Len(t, actions, 1)
	action := actions[0].(map[string]any)
	assert.Equal(t, "sendCommand", action["type"])
	assert.NotContains(t, action, "raiseAlarm")
	sc := action["sendCommand"].(map[string]any)
	assert.Equal(t, HarnessCommandKey, sc["command"], "the nested key is `command`, carrying the CommandKey")
	assert.NotContains(t, sc, "payload", "a no-argument command carries no payload")
	assert.NotContains(t, sc, "commandKey")
}

// --- durable command oracle ---------------------------------------------------

func commandsJSON(total int64, rows ...map[string]any) json.RawMessage {
	results := make([]map[string]any, 0, len(rows))
	results = append(results, rows...)
	body, _ := json.Marshal(map[string]any{
		"commands": map[string]any{
			"results":    results,
			"pagination": map[string]any{"totalRecords": total},
		},
	})
	return body
}

func TestCommandOracleSnapshotCounts(t *testing.T) {
	o := &commandOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return commandsJSON(3,
			map[string]any{"token": "c1", "status": "SUCCESSFUL"},
			map[string]any{"token": "c2", "status": "SUCCESSFUL"},
			map[string]any{"token": "c3", "status": "SENT"},
		), nil
	}}}
	s, err := o.snapshot(context.Background(), "harness-cmd-probe-001")
	require.NoError(t, err)
	assert.Equal(t, 3, s.Count)
	assert.Equal(t, 2, s.Successful)
	assert.False(t, s.roundTripComplete(), "one command stuck SENT ⇒ round-trip incomplete")
}

func TestCommandOracleRoundTripComplete(t *testing.T) {
	o := &commandOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return commandsJSON(2,
			map[string]any{"token": "c1", "status": "SUCCESSFUL"},
			map[string]any{"token": "c2", "status": "SUCCESSFUL"},
		), nil
	}}}
	s, err := o.snapshot(context.Background(), "harness-cmd-probe-001")
	require.NoError(t, err)
	assert.Equal(t, 2, s.Count)
	assert.Equal(t, 2, s.Successful)
	assert.True(t, s.roundTripComplete())
}

func TestCommandOracleAbsentIsEmpty(t *testing.T) {
	o := &commandOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return commandsJSON(0), nil
	}}}
	s, err := o.snapshot(context.Background(), "harness-cmd-safe-001")
	require.NoError(t, err)
	assert.Equal(t, 0, s.Count, "no row → enqueued no command")
	assert.False(t, s.roundTripComplete())
}

// A null totalRecords must be a fail-closed error, not a silent "no commands" that
// no-spurious would read as a clean pass.
func TestCommandOracleNullTotalIsError(t *testing.T) {
	o := &commandOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"commands":{"results":[],"pagination":{"totalRecords":null}}}`), nil
	}}}
	_, err := o.snapshot(context.Background(), "harness-cmd-safe-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null totalRecords")
}

// totalRecords outrunning the returned page is a fail-closed error, not a silently
// undercounted SUCCESSFUL tally (which could make an incomplete round-trip look done).
func TestCommandOraclePageOverflowIsError(t *testing.T) {
	o := &commandOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return commandsJSON(3, map[string]any{"token": "c1", "status": "SUCCESSFUL"}), nil
	}}}
	_, err := o.snapshot(context.Background(), "harness-cmd-probe-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds returned page")
}

func TestCommandOracleQueryErrorPropagates(t *testing.T) {
	o := &commandOracle{session: &fakeSession{respond: func(map[string]any) (json.RawMessage, error) {
		return nil, fmt.Errorf("boom")
	}}}
	_, err := o.snapshotAll(context.Background(), []string{"harness-cmd-probe-001"})
	require.Error(t, err, "a read error must surface, never a 0 that reads as 'no command'")
}

func TestCommandSnapshotRoundTripComplete(t *testing.T) {
	assert.False(t, commandSnapshot{Count: 0}.roundTripComplete(), "no command = nothing round-tripped")
	assert.False(t, commandSnapshot{Count: 3, Successful: 2}.roundTripComplete(), "one stuck non-SUCCESSFUL")
	assert.True(t, commandSnapshot{Count: 3, Successful: 3}.roundTripComplete())
}

// --- classifyCommands (the pure heart) ----------------------------------------

func cmdSnap(count, successful int) commandSnapshot {
	st := map[string]int{}
	if successful > 0 {
		st[cmdStatusSuccessful] = successful
	}
	if count > successful {
		st[cmdStatusSent] = count - successful
	}
	return commandSnapshot{Count: count, Successful: successful, Statuses: st}
}

func TestClassifyCommandsCleanPasses(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{
		"harness-cmd-safe-001": cmdSnap(0, 0),
		"harness-cmd-safe-002": cmdSnap(0, 0),
	}
	probes := map[string]commandSnapshot{
		"harness-cmd-probe-001": cmdSnap(5, 5),
		"harness-cmd-probe-002": cmdSnap(5, 5),
	}
	invs := classifyCommands(safety, probes, true, cfg, 2000)
	assert.True(t, passes(invs), "a clean run passes every invariant: %v", invs)
}

// A spurious command on a below-threshold safety probe is a durable command row a
// query WILL see — the false-react class the durable oracle exists to catch.
func TestClassifyCommandsSpuriousFails(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{
		"harness-cmd-safe-001": cmdSnap(0, 0),
		"harness-cmd-safe-002": cmdSnap(1, 1), // spurious!
	}
	probes := map[string]commandSnapshot{"harness-cmd-probe-001": cmdSnap(5, 5)}
	invs := classifyCommands(safety, probes, true, cfg, 2000)
	assert.False(t, invByName(t, invs, "no-spurious-command").Passed)
	assert.True(t, invByName(t, invs, "command-count").Passed, "the probe still enqueued K")
	assert.True(t, invByName(t, invs, "command-roundtrip").Passed)
}

// Fewer than K commands = a dropped rising edge (a detection/enqueue miss).
func TestClassifyCommandsShortCountFails(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{"harness-cmd-safe-001": cmdSnap(0, 0)}
	probes := map[string]commandSnapshot{
		"harness-cmd-probe-001": cmdSnap(5, 5),
		"harness-cmd-probe-002": cmdSnap(4, 4), // one rising edge dropped
	}
	invs := classifyCommands(safety, probes, true, cfg, 2000)
	assert.False(t, invByName(t, invs, "command-count").Passed)
	assert.True(t, invByName(t, invs, "no-spurious-command").Passed)
	// roundtrip looks at each existing command; all 4 + 5 are SUCCESSFUL, so it holds —
	// the DROP is isolated to command-count, exactly as intended.
	assert.True(t, invByName(t, invs, "command-roundtrip").Passed)
}

// More than K commands = a spurious duplicate the idempotency token should have
// collapsed — itself a finding.
func TestClassifyCommandsOverCountFails(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{"harness-cmd-safe-001": cmdSnap(0, 0)}
	probes := map[string]commandSnapshot{"harness-cmd-probe-001": cmdSnap(6, 6)}
	invs := classifyCommands(safety, probes, true, cfg, 2000)
	assert.False(t, invByName(t, invs, "command-count").Passed)
}

// K commands but one stuck SENT (delivered but never answered, or never delivered) =
// an incomplete round-trip.
func TestClassifyCommandsStuckSentFails(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{"harness-cmd-safe-001": cmdSnap(0, 0)}
	probes := map[string]commandSnapshot{"harness-cmd-probe-001": cmdSnap(5, 4)} // one SENT
	invs := classifyCommands(safety, probes, true, cfg, 2000)
	assert.True(t, invByName(t, invs, "command-count").Passed, "exactly K exist")
	assert.False(t, invByName(t, invs, "command-roundtrip").Passed, "one did not reach SUCCESSFUL")
}

// The defensive reached-gate: every snapshot looks complete but the await timed out —
// the disagreement fails rather than trusting the later snapshot.
func TestClassifyCommandsNotReachedFails(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{"harness-cmd-safe-001": cmdSnap(0, 0)}
	probes := map[string]commandSnapshot{"harness-cmd-probe-001": cmdSnap(5, 5)}
	invs := classifyCommands(safety, probes, false, cfg, 2000)
	assert.False(t, invByName(t, invs, "command-roundtrip").Passed, "await timeout is a finding even if the final read looks complete")
}

func TestClassifyCommandsBelowFloorFails(t *testing.T) {
	cfg := CommandConfig{Cycles: 5, MinAccepted: 1000}.withDefaults()
	safety := map[string]commandSnapshot{"harness-cmd-safe-001": cmdSnap(0, 0)}
	probes := map[string]commandSnapshot{"harness-cmd-probe-001": cmdSnap(5, 5)}
	invs := classifyCommands(safety, probes, true, cfg, 12) // way under the floor
	assert.False(t, invByName(t, invs, "load-floor").Passed)
}

// An empty invariant set is never a pass (a report that asserted nothing proved
// nothing).
func TestCommandReportEmptyIsNotPass(t *testing.T) {
	r := &CommandReport{}
	assert.False(t, r.Passed())
}

// --- config validation --------------------------------------------------------

func TestCommandConfigValidate(t *testing.T) {
	require.NoError(t, CommandConfig{SafetyProbes: 1, CommandProbes: 1, Cycles: 1}.Validate())

	err := CommandConfig{SafetyProbes: 1, CommandProbes: 0, Cycles: 1}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command probe")

	err = CommandConfig{SafetyProbes: 0, CommandProbes: 1, Cycles: 1}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safety probe")

	err = CommandConfig{SafetyProbes: 1, CommandProbes: 1, Cycles: 0}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycles")
}

// withDefaults supplies the MQTT broker default so a bare config still points at the
// local port-forward.
func TestCommandConfigDefaultsBroker(t *testing.T) {
	cfg := CommandConfig{}.withDefaults()
	assert.Equal(t, defaultMqttBroker, cfg.MqttBroker)
}
