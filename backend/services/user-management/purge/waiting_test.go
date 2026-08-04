// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	waitSettle    = 5 * time.Minute
	waitTokenHold = 12 * time.Hour
)

func at(base time.Time, d time.Duration) *time.Time {
	t := base.Add(d)
	return &t
}

// cleanLine is a ledger line for a store that reported clean at the given time.
func cleanLine(store string, since *time.Time) iam.TenantPurgeStore {
	return iam.TenantPurgeStore{Store: store, Complete: true, CleanSince: since}
}

// TestWaitingReportsTheOutstandingGate walks the three waits in the order the coordinator
// applies them, plus the state with nothing outstanding.
//
// The four are asserted in ONE test on purpose: the property that matters is not that each
// answer is reachable but that they are ORDERED, and a suite with four independent tests
// would pass with the order scrambled — reporting SETTLE for a deletion that a store is
// actually blocking, which sends an operator to wait out a window that will never end it.
func TestWaitingReportsTheOutstandingGate(t *testing.T) {
	now := time.Now().UTC()
	epoch := now.Add(-24 * time.Hour) // old enough that the token hold is long elapsed

	t.Run("a store that is not clean blocks, whatever the windows say", func(t *testing.T) {
		lines := []iam.TenantPurgeStore{
			cleanLine("rdb", at(now, -time.Hour)),
			{Store: "detect", Deferred: "the checkpoint still holds this tenant's windows"},
		}
		p := Waiting(lines, epoch, now, waitSettle, waitTokenHold)

		assert.Equal(t, WaitStores, p.Awaiting)
		require.Len(t, p.BlockedBy, 1)
		assert.Contains(t, p.BlockedBy[0], "detect")
		assert.Contains(t, p.BlockedBy[0], "checkpoint")
		assert.Nil(t, p.ElapsesAt, "a store holding data does not clear on a timer, so promising "+
			"a time here would promise something nothing will deliver")
	})

	t.Run("everything clean but not for long enough", func(t *testing.T) {
		lines := []iam.TenantPurgeStore{
			cleanLine("rdb", at(now, -time.Hour)),
			cleanLine("kv", at(now, -time.Minute)),
		}
		p := Waiting(lines, epoch, now, waitSettle, waitTokenHold)

		assert.Equal(t, WaitSettle, p.Awaiting)
		assert.Empty(t, p.BlockedBy)
		require.NotNil(t, p.ElapsesAt)
		assert.Equal(t, now.Add(-time.Minute).Add(waitSettle), *p.ElapsesAt,
			"the settle window runs from the LAST store to go clean, not the first — otherwise a "+
				"store that just went clean rides out on a peer that has been clean for an hour")
	})

	t.Run("clean and settled, waiting out the token hold", func(t *testing.T) {
		young := now.Add(-time.Hour)
		lines := []iam.TenantPurgeStore{cleanLine("rdb", at(now, -30*time.Minute))}
		p := Waiting(lines, young, now, waitSettle, waitTokenHold)

		assert.Equal(t, WaitTokenHold, p.Awaiting)
		assert.Empty(t, p.BlockedBy)
		require.NotNil(t, p.ElapsesAt)
		assert.Equal(t, young.Add(waitTokenHold), *p.ElapsesAt,
			"the token hold runs from the EPOCH, not from clean-since — it asks whether anything "+
				"can still be admitted at all, which the sweep going quiet does not answer")
	})

	t.Run("nothing outstanding", func(t *testing.T) {
		lines := []iam.TenantPurgeStore{cleanLine("rdb", at(now, -time.Hour))}
		p := Waiting(lines, epoch, now, waitSettle, waitTokenHold)

		assert.Equal(t, WaitNone, p.Awaiting)
		assert.Empty(t, p.BlockedBy)
		assert.Nil(t, p.ElapsesAt)
	})
}

// TestTheSettleWindowIsCheckedBeforeTheTokenHold is the ordering control the subtests above
// cannot give on their own.
//
// It builds a deletion where BOTH windows are outstanding and asserts the settle window is
// the one reported. Reversed, an operator watching a fresh deletion would be told to wait
// twelve hours when the real next event is five minutes away — and every subtest above would
// still pass, because each is constructed with only one window outstanding.
func TestTheSettleWindowIsCheckedBeforeTheTokenHold(t *testing.T) {
	now := time.Now().UTC()
	epoch := now.Add(-time.Minute) // token hold outstanding
	lines := []iam.TenantPurgeStore{cleanLine("rdb", at(now, -time.Second))}

	p := Waiting(lines, epoch, now, waitSettle, waitTokenHold)

	assert.Equal(t, WaitSettle, p.Awaiting,
		"with both windows outstanding the NEARER one is what an operator needs to be told about")
}

// TestANoteNeverBlocks pins that a note stays a qualifier all the way to this surface.
//
// The ledger's Note field exists precisely so a store can say what it declined to look at
// WITHOUT holding the deletion open. A blockedBy entry sourced from it would send an operator
// after a store that is working exactly as designed — for example the key-value store, which
// notes its exempted buckets on every single pass of every single deletion.
func TestANoteNeverBlocks(t *testing.T) {
	now := time.Now().UTC()
	line := cleanLine("kv", at(now, -time.Hour))
	line.Note = "the exempted buckets were not scanned"

	p := Waiting([]iam.TenantPurgeStore{line}, now.Add(-24*time.Hour), now, waitSettle, waitTokenHold)

	assert.Equal(t, WaitNone, p.Awaiting)
	assert.Empty(t, p.BlockedBy, "a note must never appear as a blocker")
}

// TestADeferralIsReportedOverAFailure covers a store carrying both.
//
// They mean opposite things about the future — a failure retries itself, a deferral does not
// clear until someone changes something — so surfacing the failure would tell an operator to
// wait on the half that is never going to resolve.
func TestADeferralIsReportedOverAFailure(t *testing.T) {
	now := time.Now().UTC()
	lines := []iam.TenantPurgeStore{{
		Store:    "blob",
		Deferred: "an object remains and object storage is not configured",
		Failure:  "connection refused",
	}}

	p := Waiting(lines, now, now, waitSettle, waitTokenHold)

	require.Len(t, p.BlockedBy, 1)
	assert.Contains(t, p.BlockedBy[0], "object storage is not configured")
	assert.NotContains(t, p.BlockedBy[0], "connection refused",
		"the deferral is the half that will not clear on its own; reporting the retryable half "+
			"tells an operator to wait when they need to act")
}

// TestAStoreThatIsNotCleanAndSaidNothingStillAppears keeps a silent store from vanishing.
//
// A line that is neither clean nor carrying text is reachable — a store that errored before
// it could report, or a pass interrupted mid-sweep. Rendering it as an empty string would put
// a blank row in front of an operator; omitting it entirely would be worse, since blockedBy
// would then be empty and the deletion would read as "waiting normally" while a store held it.
func TestAStoreThatIsNotCleanAndSaidNothingStillAppears(t *testing.T) {
	now := time.Now().UTC()
	p := Waiting([]iam.TenantPurgeStore{{Store: "tsdb"}}, now, now, waitSettle, waitTokenHold)

	assert.Equal(t, WaitStores, p.Awaiting)
	require.Len(t, p.BlockedBy, 1)
	assert.Contains(t, p.BlockedBy[0], "tsdb")
	assert.NotEqual(t, "tsdb: ", p.BlockedBy[0], "a silent store must still say something")
}
