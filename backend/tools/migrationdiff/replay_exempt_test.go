// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The replay gate itself needs a database and Docker, so it runs only in
// hack/migration-diff.sh. The exemption registry does not, and it is the part that rots:
// an entry outlives the migration it excused, and nothing says so. These tests run in the
// ordinary `go test ./...` that every PR already runs, which is the point — the check that
// catches a stale exemption must be cheaper than the check it guards.

// TestReplayExemptionsResolveAgainstTheRealChains is the live assertion: every registered
// exemption still names a migration that exists. It fails the day a baseline is re-cut, a
// migration is renamed, or an exemption is pasted with a typo — each of which otherwise
// leaves an entry that silences nothing and misinforms whoever reads it next.
func TestReplayExemptionsResolveAgainstTheRealChains(t *testing.T) {
	require.NoError(t, assertExemptionsResolve(areas))
}

// TestStaleReplayExemptionIsRejected is the counterweight, and without it the test above
// is nearly vacuous: a checker that returned nil unconditionally would pass it. This
// proves the checker can actually fail, and that its message names the offender — the
// whole value of the error is telling the reader which entry to go delete.
func TestStaleReplayExemptionIsRejected(t *testing.T) {
	restore := replayExemptions
	t.Cleanup(func() { replayExemptions = restore })

	replayExemptions = []replayExemption{
		{area: "event-management", id: "19990101000000", reason: "a migration that never existed"},
	}
	err := assertExemptionsResolve(areas)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event-management/19990101000000")
}

// TestExemptionMatchesOnBothAreaAndId pins that the lookup is keyed on the pair. A match on
// the id alone would be a real hazard here rather than a theoretical one: the areas were
// squashed on the same day, so "20260729000000" is the baseline id of SEVEN different
// areas. An id-only lookup would exempt all of them from one entry.
func TestExemptionMatchesOnBothAreaAndId(t *testing.T) {
	restore := replayExemptions
	t.Cleanup(func() { replayExemptions = restore })

	replayExemptions = []replayExemption{
		{area: "event-management", id: "20260729000000", reason: "the registered one"},
	}

	reason, ok := replayExemptionFor("event-management", "20260729000000")
	require.True(t, ok)
	assert.Equal(t, "the registered one", reason)

	_, ok = replayExemptionFor("device-management", "20260729000000")
	assert.False(t, ok, "an exemption must not carry across areas that share a migration id")

	_, ok = replayExemptionFor("event-management", "20260809000000")
	assert.False(t, ok)
}

// TestSharedBaselineIdIsRealNotHypothetical backs the premise of the test above with the
// registry itself, so the reasoning does not quietly become false if the ids diverge.
func TestSharedBaselineIdIsRealNotHypothetical(t *testing.T) {
	const squashDay = "20260729000000"
	var sharing []string
	for _, a := range areas {
		for _, m := range a.migrations {
			if m.ID == squashDay {
				sharing = append(sharing, a.name)
			}
		}
	}
	assert.Greater(t, len(sharing), 1,
		"expected several areas to share the squash-day migration id; if that is no longer "+
			"true, TestExemptionMatchesOnBothAreaAndId has lost its motivating example")
}

// TestEveryExemptionStatesAReason keeps the registry's contract mechanical rather than
// aspirational. An exemption is a known defect carried on purpose; one with an empty or
// placeholder reason is a defect carried by accident.
func TestEveryExemptionStatesAReason(t *testing.T) {
	for _, e := range replayExemptions {
		assert.NotEmpty(t, e.area, "an exemption with no area matches nothing")
		assert.NotEmpty(t, e.id, "an exemption with no id matches nothing")
		assert.Greater(t, len(e.reason), 40,
			"exemption %s/%s needs a reason that says what breaks, not a label", e.area, e.id)
	}
}

// TestExemptionsResolveIsNotFooledByACrossedPair guards the one bug the checker could
// plausibly have: building its "live" set from areas and ids independently, so an
// exemption pairing a real area with another area's real migration id would resolve.
func TestExemptionsResolveIsNotFooledByACrossedPair(t *testing.T) {
	restore := replayExemptions
	t.Cleanup(func() { replayExemptions = restore })

	// Both halves exist; the pair does not.
	replayExemptions = []replayExemption{
		{area: "ai-inference", id: "20260809000000", reason: "event-management's id on ai-inference"},
	}
	require.NotNil(t, findMigration("event-management", "20260809000000"),
		"precondition: the id must be real in the other area")
	require.Nil(t, findMigration("ai-inference", "20260809000000"),
		"precondition: the id must not be real in this one")

	assert.Error(t, assertExemptionsResolve(areas))
}

func findMigration(area, id string) *gormigrate.Migration {
	for _, a := range areas {
		if a.name != area {
			continue
		}
		for _, m := range a.migrations {
			if m.ID == id {
				return m
			}
		}
	}
	return nil
}
