// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The rules for reading what a batch cancellation actually did.
//
// These are here, in a plain unit-tested module, rather than inline in the panel that
// renders them, because they are the whole slice: the four numbers `cancelCommandBatch`
// returns are easy to render and easy to misread, and only two of the four readings are
// true.
//
// 🔴 THE FOUR COUNTS DELIBERATELY NEED NOT SUM. `cancelled` is MEASURED — how many
// commands the platform actually moved — while `alreadySent`, `alreadyFinished` and
// `matched` are COUNTED A MOMENT LATER, in a table other writers are still using. A
// command whose dispatcher tried to send it and failed goes back into the delivery queue
// in between, and it is then in NONE of the three buckets: it is neither stopped, nor at a
// device, nor finished. The service leaves it out rather than folding it into
// `alreadyFinished`, which would report a live command as a settled one.
//
// So the shortfall is not a rounding artefact to hide — it is the only evidence that this
// cancel did not stop everything, and the fix for it is to cancel again.

import { COMMAND_STATUSES, isCancellableCommandStatus } from '@devicechain/dashboards';

// The four counts, structurally. Declared here rather than imported from the generated
// operation type so these rules can be unit-tested against literals and so a caller
// holding the counts from anywhere satisfies them.
export interface BatchCancelCounts {
  cancelled: number;
  alreadySent: number;
  alreadyFinished: number;
  matched: number;
}

// The lifecycle states in which the platform still HOLDS a command — the three a cancel
// can actually stop, and therefore the states to count when asking "how much of this
// batch is still stoppable?".
//
// 🔴 DERIVED, NOT WRITTEN OUT. `CANCELLABLE_COMMAND_STATUSES` is deliberately not exported
// from @devicechain/dashboards (a handed-out set invites a hand-rolled membership check
// per call site), so this filters the one status vocabulary through the one predicate. A
// hand-typed list here would be a fourth copy of the set whose triplication is the reason
// that module exists — and it would go stale silently, since a status the service adds
// would still paint correctly in every badge while quietly dropping out of this count.
export const STILL_HELD_STATUSES: string[] = COMMAND_STATUSES.filter(isCancellableCommandStatus);

// The commands the cancel could account for: stopped, at a device, or already settled.
//
// 🔴 THIS IS NOT "HOW MANY WERE STOPPED", and must never be shown as one number under a
// heading that suggests it is. `alreadySent` is in here, and those commands are on their
// way to hardware.
export function accountedFor(counts: BatchCancelCounts): number {
  return counts.cancelled + counts.alreadySent + counts.alreadyFinished;
}

// How many of the batch's commands the cancel did NOT account for — the ones that went
// back into the delivery queue between the write and the count. Each is a live command
// this cancel did not stop.
//
// Clamped at zero: `matched` BELOW the sum is not a shortfall, it is the ordinary opposite
// race (a command counted in a bucket and deleted before `matched` was taken), and
// rendering "-2 commands are back in the delivery queue" would invent an alarm out of a
// number that means nothing is wrong.
export function unaccounted(counts: BatchCancelCounts): number {
  return Math.max(0, counts.matched - accountedFor(counts));
}

// Whether the operator has to press cancel a second time. This is an ACTION, not a
// discrepancy to note in passing: nothing retries on their behalf.
//
// It does not normally keep being true: once a cancellation is COMMITTED, a failed delivery
// retires its command rather than requeueing it — command-delivery predicates that on the
// batch's own stamp — so a second call usually finds a settled batch.
//
// 🔴 USUALLY, NOT ALWAYS, AND THAT DIFFERENCE IS WHY THE HEADER'S CANCEL ACTION IS GATED ON
// A LIVE COUNT RATHER THAN ON THE STAMP. command-delivery documents the one interleaving a
// single statement cannot close: a release whose subquery saw no committed stamp requeues a
// command whose batch is called off an instant later, and nothing downstream re-checks — so
// the next tick delivers it. That command is live inside a cancelled batch, and it becomes
// so AFTER the report that would have named it. No reading of these four numbers can find
// it. Only asking the command rows how many are still held can.
export function needsSecondCancel(counts: BatchCancelCounts): boolean {
  return unaccounted(counts) > 0;
}

// Whether any of the batch's commands had already reached their devices. Those devices
// WILL still act: a command in flight cannot be recalled, and this is the fact a cancel
// screen exists to state plainly rather than bury.
export function hasUnstoppable(counts: BatchCancelCounts): boolean {
  return counts.alreadySent > 0;
}
