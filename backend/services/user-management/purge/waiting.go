// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"time"

	"github.com/devicechain-io/dc-user-management/iam"
)

// Wait names what a purge is currently waiting on (ADR-077).
//
// It exists so an operator can be told *which* of three very different situations they are
// looking at, all of which present as "still deleting": something is holding the sweep open,
// or everything is clean and the settle window has not elapsed, or everything is clean and
// settled and only the token hold remains. The three have different remedies — fix
// something, wait minutes, wait hours — and a surface that cannot tell them apart teaches an
// operator to escalate a healthy deletion.
type Wait string

const (
	// WaitStores — at least one store has not reported clean. The only one of the three a
	// human can act on, and `BlockedBy` says which stores and why.
	WaitStores Wait = "STORES"
	// WaitSettle — every store is clean, and the most recent one to go clean has not been
	// clean for long enough yet.
	WaitSettle Wait = "SETTLE"
	// WaitTokenHold — every store is clean and settled; the purge is not yet old enough for
	// its token to be released.
	WaitTokenHold Wait = "TOKEN_HOLD"
	// WaitNone — nothing is outstanding. On a live purge this is a momentary state between
	// the last gate passing and the next pass completing it; on a finished purge it is what
	// a completed record reports.
	WaitNone Wait = "NONE"
)

// Progress is what a purge is waiting on and when that wait elapses.
type Progress struct {
	// Awaiting is the outstanding gate.
	Awaiting Wait
	// ElapsesAt is when the outstanding WINDOW ends, or nil when the wait is not a window.
	//
	// Nil for WaitStores, deliberately: a store that is holding data does not clear on a
	// timer, and a surface that showed a countdown there would promise something nothing
	// will deliver. Nil for WaitNone for the ordinary reason.
	ElapsesAt *time.Time
	// BlockedBy is one sentence per store that is not clean, in the store's own words —
	// what it still holds, or what it failed with. Empty means nothing is blocking.
	BlockedBy []string
}

// Waiting reports what a purge is waiting on, from the same inputs and in the same order the
// coordinator applies them.
//
// 🔴 IT IS THE COORDINATOR'S DECISION, NOT A DESCRIPTION OF IT. The coordinator calls this
// and acts on the result; anything reporting purge progress calls it too. That is the whole
// reason it is a function rather than a paragraph in a resolver: the arithmetic — all stores
// clean, then the settle window measured from the LAST store to go clean, then the token hold
// measured from the epoch and not from clean-since — is easy to restate slightly wrong, and a
// surface that restates it wrong tells an operator a deletion will finish at a time it will
// not. Restating it also silently stops tracking the real rule the moment either changes.
//
// lines are the ledger's per-store rows for one purge. epoch is the cut. now, settle and
// tokenHold are the coordinator's own clock and windows.
func Waiting(lines []iam.TenantPurgeStore, epoch, now time.Time, settle, tokenHold time.Duration) Progress {
	var blocked []string
	var lastCleanAt time.Time
	for _, line := range lines {
		if line.CleanSince == nil {
			blocked = append(blocked, blockingReason(line))
			continue
		}
		// The settle window has to have elapsed for EVERY store, so the constraining
		// timestamp is the most recent one — the last store to go clean. Taking the
		// earliest would let a store that only just went clean ride out on a peer that has
		// been clean for an hour.
		if line.CleanSince.After(lastCleanAt) {
			lastCleanAt = *line.CleanSince
		}
	}

	if len(blocked) > 0 {
		return Progress{Awaiting: WaitStores, BlockedBy: blocked}
	}
	if settledAt := lastCleanAt.Add(settle); now.Before(settledAt) {
		return Progress{Awaiting: WaitSettle, ElapsesAt: &settledAt}
	}
	// The second window, and it is not a longer version of the first. Settling asks whether
	// everything already admitted has finished arriving; this asks whether anything can
	// still be admitted at all.
	if releaseAt := epoch.Add(tokenHold); now.Before(releaseAt) {
		return Progress{Awaiting: WaitTokenHold, ElapsesAt: &releaseAt}
	}
	return Progress{Awaiting: WaitNone}
}

// blockingReason renders one not-clean line as a sentence.
//
// Deferred is preferred over Failure when a store somehow carries both, because they mean
// opposite things about the future and the deferral is the one that will not clear on its
// own: telling an operator "retrying" about a store that is going to keep holding data is
// the wrong half to surface. Note is deliberately NOT consulted — a note never blocks, and a
// store appearing here on the strength of one would send an operator after a store that is
// working exactly as designed.
func blockingReason(line iam.TenantPurgeStore) string {
	switch {
	case line.Deferred != "":
		return line.Store + ": " + line.Deferred
	case line.Failure != "":
		return line.Store + ": " + line.Failure
	default:
		// Not clean, and it said nothing about why. Reachable when a store has never been
		// asked — a line written for a store that errored before it could report, or one
		// carried from a pass interrupted mid-sweep. Saying so plainly beats an empty
		// entry, which would render as a blocker with no text at all.
		return line.Store + ": has not reported clean, and gave no reason"
	}
}
