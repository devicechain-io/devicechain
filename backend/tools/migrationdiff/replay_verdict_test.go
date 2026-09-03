// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exemptionVerdict is the decision that turns an exemption from a skip into an assertion,
// and it is pure, so all of its branches are testable without a database. That matters
// more than usual here: the two FAILING branches only ever run on a day something has
// already gone wrong, which is the worst day to discover they were never exercised.

func TestExemptionThatStartsPassingIsAFailure(t *testing.T) {
	ex := replayExemption{area: "a", id: "1", symptom: "boom", reason: "a registered defect"}
	msg := exemptionVerdict(ex, nil)
	require.NotEmpty(t, msg, "a known-bad migration that replays cleanly must not pass silently")
	assert.Contains(t, msg, "replayed cleanly")
	assert.Contains(t, msg, "Delete its entry",
		"the message has to say what to do; whoever hits this did not write the entry")
}

func TestExemptionThatFailsDifferentlyIsAFailure(t *testing.T) {
	ex := replayExemption{area: "a", id: "1", symptom: "cannot alter type", reason: "a registered defect"}
	msg := exemptionVerdict(ex, errors.New("duplicate key value violates unique constraint"))
	require.NotEmpty(t, msg, "the defect moved; the registered symptom no longer describes it")
	assert.Contains(t, msg, "failed differently")
	assert.Contains(t, msg, "cannot alter type", "the message must name the symptom it expected")
}

func TestExemptionThatFailsAsRegisteredPasses(t *testing.T) {
	ex := replayExemption{area: "a", id: "1", symptom: "cannot alter type", reason: "a registered defect"}
	err := errors.New(`collate measurement_events.device_token: ERROR: cannot alter type of a ` +
		`column used by a view or rule (SQLSTATE 0A000)`)
	assert.Empty(t, exemptionVerdict(ex, err))
}

// The registered symptom must be a substring of the real error, not the other way round.
// A symptom broad enough to match anything would accept any failure as "the known one",
// which is the failure mode that makes exception registries stop meaning anything.
func TestExemptionSymptomIsNotMatchedLoosely(t *testing.T) {
	ex := replayExemption{area: "a", id: "1", symptom: "cannot alter type of a column used by a view", reason: "r"}
	assert.NotEmpty(t, exemptionVerdict(ex, errors.New("cannot alter type")),
		"a truncated error must not satisfy a longer registered symptom")
}

// A symptom string, checked against the real error text this gate actually produced.
// Without this the matcher is only ever compared to strings written for it.
//
// 🔴 THIS USED TO READ THE LIVE REGISTRY, AND THE REGISTRY IS NOW EMPTY — which is the
// point, not a problem. The one entry was event-management's baseline, and that baseline
// was re-cut to be re-runnable, so the gate reported the exemption as passing and told us
// to delete it.
//
// 🔴 BE HONEST ABOUT WHAT IS LEFT. While the registry was live this compared a string
// somebody had WRITTEN against a string Postgres had EMITTED, and only the second was
// outside the author's control. With both now literals in this function it is the same
// assertion as TestExemptionThatFailsAsRegisteredPasses above, with historical data: it
// pins the matcher's behaviour against a real error text, and it can no longer catch a
// registered symptom drifting away from reality, because there is no registered symptom.
// It is kept for the recorded error text, not because it still does the harder job. The
// harder job comes back the moment an entry does.
func TestASymptomMatchesTheRealErrorItWasWrittenFor(t *testing.T) {
	ex := replayExemption{
		area:    "event-management",
		id:      "20260729000000",
		symptom: "collate measurement_events.device_token: ERROR: cannot alter type of a column used by a view or rule",
		reason:  "frozen pre-GA baseline; a replay dies on ALTER COLUMN ... COLLATE because the continuous aggregate it later creates reads those columns",
	}

	observed := errors.New("collate measurement_events.device_token: ERROR: cannot alter type of " +
		"a column used by a view or rule (SQLSTATE 0A000)")
	assert.Empty(t, exemptionVerdict(ex, observed),
		"the registered symptom must match the error the gate actually observed")
}

// And the registry really is empty, asserted rather than assumed.
//
// An exemption is a known defect carried on purpose. Zero of them is the state this gate
// exists to reach, so it is worth failing loudly when one reappears: whoever adds the next
// entry should have to change this test and say why in the same commit, rather than
// growing the list quietly.
func TestNoReplayExemptionsAreRegistered(t *testing.T) {
	assert.Empty(t, replayExemptions,
		"a replay exemption is a migration that cannot survive its own replay, carried on "+
			"purpose. If you are adding one, re-read the comment above the list: after GA a "+
			"released instance cannot be told to destroy itself, so the pre-GA remedy that "+
			"justified the last one is not available")
}
