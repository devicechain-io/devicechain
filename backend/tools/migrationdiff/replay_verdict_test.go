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

// The real registered entry, checked against the real error text this gate produced when
// it first ran. Without this the symptom string is only ever compared to itself.
func TestTheLiveExemptionMatchesTheErrorItWasWrittenFor(t *testing.T) {
	ex, ok := replayExemptionFor("event-management", "20260729000000")
	require.True(t, ok, "the worked example must still be registered for this test to mean anything")

	observed := errors.New("collate measurement_events.device_token: ERROR: cannot alter type of " +
		"a column used by a view or rule (SQLSTATE 0A000)")
	assert.Empty(t, exemptionVerdict(ex, observed),
		"the registered symptom must match the error the gate actually observed")
}
