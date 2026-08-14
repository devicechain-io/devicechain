// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// batchCancelPasses bounds how many times a cancel re-checks its own work.
//
// A pass cancels every dispatchable command in the batch and then counts what is left.
// It repeats only if something is dispatchable AGAIN, which needs a specific race: a
// command that was SENT when the pass ran, whose publish failed, and whose dispatcher
// released it back to the queue before the count — all inside one transaction's
// lifetime. One repeat settles it in every case anyone has constructed; three is room
// for a pathological broker, not an expectation.
//
// 🔴 REACHING THE BOUND IS NOT AN ERROR AND MUST NEVER BECOME ONE. By then rows have
// already been cancelled, and failing the mutation would un-report work that actually
// happened. The cancel degrades to reporting honestly instead — see BatchCancellation.
const batchCancelPasses = 3

// BatchCancellation is what a batch cancel was able to do.
//
// 🔑 CANCELLED IS MEASURED; THE OTHER THREE ARE OBSERVED. Cancelled is the number of
// rows the conditional UPDATE actually moved, summed over the passes — a fact, not a
// count of anything read back. The rest come from counting the batch's rows immediately
// afterwards, which is a snapshot of a table other writers are still using.
//
// 🔴 THE FOUR NEED NOT SUM, AND THE GAP IS MEANINGFUL RATHER THAN A ROUNDING ERROR. A
// dispatcher can return a command to the queue between the last pass and the count, and
// such a row is neither cancelled nor sent nor finished. It is left OUT of every bucket
// on purpose: folding it into AlreadyFinished would report a live, dispatchable command
// as finished, which is the single lie this whole status vocabulary exists to prevent.
// Matched is therefore >= the sum, and a difference means "some rows are back in play" —
// which a second cancel resolves, and which the release path (see ReleaseClaim) stops
// happening once this cancel is committed.
type BatchCancellation struct {
	// Cancelled is how many commands this call moved to CANCELLED.
	Cancelled int
	// AlreadySent is how many had already gone to the device. Those devices will still
	// act on the command — the cancel cannot recall it, and pretending otherwise is the
	// reason SENT is not in the cancellable set.
	AlreadySent int
	// AlreadyFinished is how many had already reached a terminal state before this call.
	AlreadyFinished int
	// Matched is how many of the batch's command rows exist NOW.
	//
	// 🔴 IT IS NOT THE BATCH'S `Accepted`. Accepted is a stored fact about the moment the
	// batch fired; this counts live rows, and rows are not immortal — a soft delete or a
	// tenant purge lowers it. Matched < Accepted is ordinary and says nothing about the
	// cancel.
	Matched int
}

// CancelCommandBatch calls off a fleet write: every command the platform still holds for
// the batch moves to CANCELLED, and the batch record is stamped with the fact.
//
// 🔴 IT DOES NOT REFUSE, EVER. A brake that declined to engage because part of the fleet
// had already moved would leave the REST of the fleet commanded — the opposite of what
// the caller asked for, at the moment they least want it. Commands already sent are
// reported, not treated as an error.
//
// 🔴 THE STAMP GOES FIRST BECAUSE IT TAKES THE BATCH ROW'S LOCK, WHICH SERIALIZES
// CONCURRENT CANCELS OF THE SAME BATCH. This is the third explanation this ordering has
// had and the first one that holds; the two it replaces are recorded because each looked
// right.
//
//	NOT because a concurrent ReleaseClaim reads the stamp — it cannot, until this commits.
//	NOT merely because the two writes commit together, true but not a reason to order them.
//
// The actual failure the order prevents: two cancels of one batch, A catching three
// commands and B catching none. With the stamp last, both pass loops run concurrently and
// whichever commits first owns the record — so B can write cancelledCount=0 over A's
// three, and the record then reports that pulling the brake stopped nothing when it
// stopped three. Taking the lock up front makes B block until A commits, after which B's
// `cancelled_at IS NULL` correctly matches nothing AND its pass loop correctly finds
// nothing left to cancel.
//
// So: do not fold the two writes into one statement at the end. It is a real
// simplification — it would delete a helper, a bool and a branch — and it costs the
// serialization that makes cancelledCount belong to the call that did the work.
//
// One transaction is still the right shape for its own reason: it makes "this batch is
// called off" and "its commands are stopped" become true at the same instant. A crash
// leaves neither.
//
// What that leaves, stated rather than papered over:
//
//   - AFTER the commit, a failed publish retires its command instead of requeueing it,
//     because ReleaseClaim can now see the stamp. This is the case that matters, and it is
//     closed.
//   - DURING the transaction, a release that commits before the next pass reads the table
//     is caught by the pass loop below — that is what the loop is for.
//   - A release committing between the LAST pass and this transaction's commit is caught
//     by neither. That row stays dispatchable with its batch called off. It is reported:
//     it lands in `matched` and in no bucket, which is the gap BatchCancellation documents,
//     and cancelling again takes it.
//
// It is safe to call twice — which is also the remedy for that last case. The second call
// cancels whatever it finds, reports the batch as it now stands, and leaves the original
// stamp intact.
func (api *Api) CancelCommandBatch(ctx context.Context, batch *CommandBatch) (*BatchCancellation, error) {
	result := &BatchCancellation{}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		stamped, err := stampBatchCancelled(tx, batch.ID)
		if err != nil {
			return err
		}
		// Cancelled ACCUMULATES across passes; the snapshot does not — only the last one
		// describes the batch as it ends up. Folding each pass's snapshot in as it arrived
		// put an assignment directly under an accumulation, which reads as though both
		// were adding up.
		var counts map[string]int
		for pass := 0; pass < batchCancelPasses; pass++ {
			cancelled, passCounts, err := cancelBatchPass(tx, batch.ID)
			if err != nil {
				return err
			}
			result.Cancelled += cancelled
			counts = passCounts
			if counts[CommandQueued.String()]+counts[CommandHeld.String()] == 0 {
				break
			}
		}
		result.observe(counts)
		if !stamped {
			// An earlier cancel owns the record. This call still cancelled whatever it
			// found — a release can have re-armed a row since — but the stored count
			// describes the intervention that placed the stamp, and overwriting it here
			// would replace the number that stopped something with a later, smaller one.
			return nil
		}
		return recordBatchCancelledCount(tx, batch.ID, result.Cancelled)
	})
	if err != nil {
		return nil, err
	}
	api.BatchMetrics.recordCancel(cancelDispositionCancelled, result.Cancelled)
	api.BatchMetrics.recordCancel(cancelDispositionAlreadySent, result.AlreadySent)
	api.BatchMetrics.recordCancel(cancelDispositionAlreadyFinished, result.AlreadyFinished)
	return result, nil
}

// cancelBatchPass takes one sweep at the batch: cancel what is dispatchable, then count
// what remains.
//
// Split out because the race this exists for cannot be staged through the public API —
// it needs a dispatcher to release a claim between the UPDATE and the count — but it can
// be staged HERE, by calling a pass, writing a row back to QUEUED, and calling another.
//
// The UPDATE's RowsAffected is the only trustworthy count of what was cancelled. Counting
// first and updating second would race the delivery sweep, which is free to claim any of
// these rows right up to the moment the UPDATE takes them.
func cancelBatchPass(tx *gorm.DB, batchId uint) (int, map[string]int, error) {
	res := tx.Model(&Command{}).
		Where("batch_id = ? AND status IN ?", batchId, batchCancellableStatusStrings()).
		Updates(map[string]any{"status": CommandCancelled.String()})
	if res.Error != nil {
		return 0, nil, res.Error
	}

	type statusCount struct {
		Status string
		Total  int
	}
	rows := make([]statusCount, 0)
	if err := tx.Model(&Command{}).
		Select("status, count(*) as total").
		Where("batch_id = ?", batchId).
		Group("status").Find(&rows).Error; err != nil {
		return 0, nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.Total
	}
	return int(res.RowsAffected), counts, nil
}

// observe reads the three reported-but-not-measured fields off the FINAL snapshot.
//
// AlreadyFinished subtracts what THIS call cancelled, because those rows are terminal in
// the snapshot too and would otherwise be counted as having finished before the cancel
// arrived. It floors at zero: a row cancelled by an earlier pass and soft-deleted before
// the count would otherwise drive it negative, which is a nonsense number to hand an
// operator over a race that changes nothing about the outcome.
func (c *BatchCancellation) observe(counts map[string]int) {
	terminal := 0
	for status, n := range counts {
		c.Matched += n
		if CommandStatus(status).Terminal() {
			terminal += n
		}
	}
	c.AlreadySent = counts[CommandSent.String()]
	c.AlreadyFinished = terminal - c.Cancelled
	if c.AlreadyFinished < 0 {
		c.AlreadyFinished = 0
	}
}

// stampBatchCancelled records that this batch was called off, if nothing already had.
//
// 🔴 FIRST WINS, AND THAT IS THE WHOLE PREDICATE. A second cancel catches nothing, so
// stamping unconditionally would overwrite the record of the intervention that DID stop
// something with a later one that stopped nothing. A first cancel that catches nothing
// still stamps: "someone pulled the brake and there was already nothing to stop" is a
// fact, and the timestamp is what distinguishes it from a batch nobody ever tried to
// stop.
// It reports whether THIS call placed the stamp. That answer, rather than a re-read, is
// what decides who owns the count: a conditional UPDATE's RowsAffected cannot be
// confused by a concurrent cancel the way "is it null now?" can.
func stampBatchCancelled(tx *gorm.DB, batchId uint) (bool, error) {
	res := tx.Model(&CommandBatch{}).
		Where("id = ? AND cancelled_at IS NULL", batchId).
		Update("cancelled_at", sql.NullTime{Time: time.Now(), Valid: true})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// recordBatchCancelledCount writes what the stamping call caught.
//
// It runs only for the call that placed the stamp, and in the same transaction, so the
// two columns are never observable apart.
//
// 🔑 IT IS "WHAT THIS CALL CAUGHT", NOT A LIVE COUNT OF CANCELLED ROWS, and the two can
// legitimately differ: a later release (ReleaseClaim) drives an already-sent command to
// CANCELLED long after this number was written. That matches the rest of the record,
// whose counts are all stored facts about a moment rather than queries pretending to be.
func recordBatchCancelledCount(tx *gorm.DB, batchId uint, cancelled int) error {
	return tx.Model(&CommandBatch{}).
		Where("id = ?", batchId).
		Update("cancelled_count", sql.NullInt32{Int32: int32(cancelled), Valid: true}).Error
}

// There is deliberately no CancelCommandBatchByToken here. It existed briefly, with a
// comment describing callers that hold only a token — and there were none: every caller
// has to load the batch first anyway, because a group-targeted batch takes a second
// authority and that check has to happen BEFORE anything is cancelled. A convenience
// wrapper that folds the lookup in would make it easy to write the one caller that skips
// the gate.
