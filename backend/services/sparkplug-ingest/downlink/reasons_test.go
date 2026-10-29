// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The console decides what to say about a resolved command from the Reason code, never from the
// human error text. The vocabulary is therefore an interface: adding, renaming or removing a code
// must change this test, and with it the console's sparkplugCommands.ts, in the same commit.
func TestTheReasonVocabularyIsPinned(t *testing.T) {
	assert.Equal(t, []Reason{
		"confirmed", "unchanged", "unconfirmed_timeout", "superseded", "session_dropped", "source_disabled",
	}, Reasons)
	seen := map[Reason]bool{}
	for _, r := range Reasons {
		require.NotEmpty(t, r)
		require.False(t, seen[r], "duplicate reason %q", r)
		seen[r] = true
	}
}

// An outcome keeps its reason through the record that survives a change of leader.
func TestAReasonSurvivesTheStoredOutcome(t *testing.T) {
	for _, r := range Reasons {
		o := &outcome{reason: r, result: ResultUnconfirmed, text: "x"}
		raw, err := json.Marshal(o.stored())
		require.NoError(t, err)
		var back StoredOutcome
		require.NoError(t, json.Unmarshal(raw, &back))
		assert.Equal(t, r, outcomeOf(&back).reason)
	}
}

// Every way a command can end without an answer sets a reason, so the console never has to read
// the error text to tell them apart. The window endings are asserted here; the superseded and
// session-dropped endings (dispatcher_test.go) and the source-disabled one (handover_test.go) are
// asserted on the response each actually publishes, which is where their code is set.
func TestEveryUnansweredEndingCarriesAReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  *outcome
		want Reason
	}{
		{"window", (&entry{a: Awaiting{Kind: KindWrite, Metric: "m"}}).expiry(nil), ReasonUnconfirmedTimeout},
		{"rebirth window", (&entry{a: Awaiting{Kind: KindRebirth, Target: "g/n"}}).expiry(nil), ReasonUnconfirmedTimeout},
	} {
		assert.Equal(t, tc.want, tc.out.reason, tc.name)
	}
	ev := (&entry{a: Awaiting{Kind: KindWrite}}).confirmed("DDATA", nil, nil, nil)
	assert.Equal(t, ReasonConfirmed, ev.reason)
	assert.Contains(t, ev.payload, `"reason":"confirmed"`)
	un := (&entry{a: Awaiting{Kind: KindWrite}}).confirmedWith("UNCHANGED", nil, nil, nil, nil, ResultConfirmedUnchanged)
	assert.Equal(t, ReasonUnchanged, un.reason)
	assert.Contains(t, un.payload, `"reason":"unchanged"`)
}

// The refusal reasons are an interface in the same way: the console keys a sentence on each.
func TestTheRefusalReasonVocabularyIsPinned(t *testing.T) {
	assert.Equal(t, []Reason{
		"commands_not_enabled", "broker_refused", "expired", "invalid_parameters", "unknown_command",
		"metric_protocol_control", "metric_not_writable", "metric_not_declared", "datatype_unsupported",
		"value_not_accepted", "datatype_changed", "node_rebirthing", "node_offline", "reboot_impact",
		"reboot_not_allowed", "reboot_not_a_node", "reboot_not_declared", "reboot_not_boolean",
		"reboot_unverified", "reboot_rate_limited", "no_dispatch", "publish_failed",
	}, RefusalReasons)
	seen := map[Reason]bool{}
	for _, r := range append(append([]Reason{}, Reasons...), RefusalReasons...) {
		require.False(t, seen[r], "duplicate reason %q across the resolved and refusal sets", r)
		seen[r] = true
	}
}
