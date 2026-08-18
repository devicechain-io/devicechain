// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// StrandedCursor is where a stranded-SENT walk resumes: the (sent_time, id) of the last
// row the previous page returned. The zero value starts at the beginning.
//
// 🔴 IT IS A COMPOUND CURSOR BECAUSE sent_time IS NOT UNIQUE. A dispatch sweep marks a
// whole batch sent inside one tick, so many rows can share a sent_time to whatever
// precision the column stores. A cursor of sent_time alone would either re-read those
// ties forever (with `>=`) or skip all but the first of them (with `>`); carrying the id
// resumes at an exact row instead of at a timestamp.
type StrandedCursor struct {
	SentTime time.Time
	ID       uint
}

// AtStart reports whether this cursor is the beginning of a walk. Exported because the
// processor's tests assert on where a pass resumed, and "did it restart?" is the whole
// question the cursor exists to answer.
func (c StrandedCursor) AtStart() bool {
	return c.ID == 0 && c.SentTime.IsZero()
}

// strandedLockName namespaces the stranded-reconcile pass's own advisory lock.
//
// 🔑 ITS OWN LOCK, LIKE THE HOLD RECONCILER'S AND FOR THE SAME REASON. Sharing the
// sweep's lock would serialize a slow walk of the in-flight set against the 30-second
// delivery sweep, starving the fast path behind the slow one. Sharing the HOLD
// reconciler's lock would be subtler and no better: the two walk disjoint state sets
// (HELD versus SENT) and neither's writes depend on the other's, so one lock would only
// mean that whichever pass ran first silently suppressed the other on that replica.
//
// Overlap between the three is safe because every write on all of them is predicated on
// the exact state its scan observed — here, additionally, on the exact dispatch nonce it
// observed. A park that races anything else matches zero rows and does nothing. The lock
// exists only to stop N replicas performing the same WALK, which would be wasted reads;
// the reconciler publishes nothing and actuates nothing.
const strandedLockName = "command-delivery-stranded-reconcile"

// TryStrandedLock runs fn while holding the cross-replica stranded-reconcile lock,
// reporting whether it ran. Like the other two it does not wait.
func (api *Api) TryStrandedLock(ctx context.Context, fn func() error) (bool, error) {
	return api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(strandedLockName), fn)
}

// StrandedSentCommands returns one bounded page of commands that have been reading SENT,
// with no outcome, since before olderThan — oldest first — along with the cursor the next
// page resumes from.
//
// A row here is not necessarily stranded. It is a row about which the platform has
// learned NOTHING for longer than the messaging layer could still be retrying, which is
// the strongest statement a database scan can make on its own; deciding what to do about
// it belongs to the reconciler, which can ask the device's transport and its presence.
// See StrandedSentGrace for why "longer than the messaging layer could still be retrying"
// is a derived duration rather than a chosen one.
//
// 🔴 THE HORIZON IS A STRICT `<` ON A NON-NULL sent_time, AND THE NULL CASE IS THE TRAP.
// `sent_time < ?` already excludes NULL, because a comparison against NULL is NULL and
// never true — but that exclusion is INVISIBLE in the source, which is what makes it
// dangerous. A future reader who notices the column is nullable and "fixes" this into
// `sent_time IS NULL OR sent_time < ?` sweeps in every row that has never been
// dispatched at all, and the reconciler then parks commands the device was never sent.
// The IS NOT NULL below is therefore written out even though it changes no result: it
// states the intent that the comparison merely implies, so the refactor above reads as
// the contradiction it is. TestScanIgnoresRowsThatWereNeverDispatched pins it.
//
// 🔑 THE CURSOR IS MANDATORY HERE FOR A SHARPER REASON THAN IN THE HOLD RECONCILER. Many
// eligible rows will be DECLINED by the reconciler's transport gate rather than acted on
// — on an MQTT-heavy instance, nearly all of them — and a declined row stays exactly as
// eligible as it was. A plain LIMIT with no cursor would therefore re-read the same
// page of undeclinable MQTT rows on every pass, forever, and never reach the LwM2M rows
// behind them. The reconciler would run, report work, and be a permanent no-op.
func (api *Api) StrandedSentCommands(ctx context.Context, cursor StrandedCursor,
	olderThan time.Time, limit int) ([]*Command, StrandedCursor, error) {
	found := make([]*Command, 0)
	q := api.RDB.DB(ctx).
		Where("status = ? AND sent_time IS NOT NULL AND sent_time < ?", CommandSent.String(), olderThan)
	if !cursor.AtStart() {
		// Spelled out rather than as a row-value comparison ((sent_time, id) > (?, ?)),
		// which Postgres supports and the SQLite the unit tests run on does not
		// uniformly. The two forms are equivalent; only this one is portable to both.
		q = q.Where("(sent_time > ? OR (sent_time = ? AND id > ?))",
			cursor.SentTime, cursor.SentTime, cursor.ID)
	}
	result := q.Order("sent_time ASC, id ASC").Limit(limit).Find(&found)
	if result.Error != nil {
		return nil, StrandedCursor{}, result.Error
	}
	// A short page means the walk is done; answering the zero cursor restarts it.
	// Answering the last row instead would leave the cursor parked past the end, and
	// every subsequent pass would read nothing at all — a reconciler that quietly stops
	// reconciling. This mirrors HeldCommands, deliberately.
	if len(found) < limit {
		return found, StrandedCursor{}, nil
	}
	last := found[len(found)-1]
	return found, StrandedCursor{SentTime: last.SentTime.Time, ID: last.ID}, nil
}
