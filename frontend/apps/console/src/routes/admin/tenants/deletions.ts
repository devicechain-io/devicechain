import type { AdminTenantDeletion, AdminTenantDeletionStore } from '@/lib/api/admin';

/**
 * The reading rules for a tenant deletion record (ADR-077).
 *
 * They live here, apart from the components, because every one of them is a judgement that
 * is easy to get plausibly wrong — and each wrong reading tells an operator something
 * actively misleading about an erasure. Keeping them as plain functions makes them testable
 * without rendering anything, which is this codebase's convention for exactly this reason.
 */

/** What a deletion is waiting on. Mirrors the server's DeletionWait enum. */
export type DeletionWait = 'STORES' | 'SETTLE' | 'TOKEN_HOLD' | 'NONE';

/**
 * How one storage system's line should read.
 *
 * 🔴 THREE STATES, NOT TWO, AND `retaining` IS NOT A KIND OF ERROR. A store that is
 * RETAINING holds data and will keep holding it until a human changes something; a store
 * that is RETRYING failed a pass and is expected to clear by itself. Collapsing them into
 * one "problem" column tells an operator to act on the one that is already fixing itself
 * and to wait on the one that never will.
 */
export type StoreState = 'clean' | 'retaining' | 'retrying';

export function storeState(line: AdminTenantDeletionStore): StoreState {
  if (line.complete) return 'clean';
  if (line.retaining) return 'retaining';
  return 'retrying';
}

/**
 * Whether a line's note should be shown.
 *
 * A note is a QUALIFIER on a clean pass — what the store declined to look at, or the ground
 * it reported clean on. It is never a problem to act on, so it is rendered as a footnote to
 * "clean" rather than in the column that carries retaining/error text. A store carrying only
 * a note is working exactly as designed.
 */
export function hasNote(line: AdminTenantDeletionStore): boolean {
  return Boolean(line.note);
}

/**
 * The text explaining a not-clean line, or null for a clean one.
 *
 * The note is deliberately NOT a fallback here. A clean store with a note would otherwise
 * render its note in the problem column, which is precisely the confusion the three-field
 * split on the server exists to prevent.
 */
export function storeProblem(line: AdminTenantDeletionStore): string | null {
  if (line.complete) return null;
  return line.retaining || line.lastError || null;
}

/** Whether a record has finished. */
export function isComplete(d: Pick<AdminTenantDeletion, 'completedAt'>): boolean {
  return Boolean(d.completedAt);
}

/**
 * Remedy hints for the two blockers an operator can actually do something about.
 *
 * Returns a translation key, or null when there is nothing useful to say. Suggesting an
 * action for a store whose deferral no human can clear would be worse than saying nothing:
 * it sends someone to look for a control that does not exist.
 *
 * Matching is on the store's ledger KEY, never on the wording of its sentence — the
 * sentences are prose written for a human and will be reworded; the keys are a fixed
 * vocabulary the server persists.
 */
export function remedyKey(line: AdminTenantDeletionStore): string | null {
  if (line.complete) return null;
  switch (line.store) {
    case 'detect':
      return line.retaining ? 'remedyDetect' : null;
    case 'blob':
      return line.retaining ? 'remedyBlob' : null;
    default:
      return null;
  }
}

/**
 * How long until a wait elapses, in whole minutes, or null when there is nothing to count
 * down to.
 *
 * 🔴 NULL FOR `STORES` IS NOT AN OMISSION. A storage system holding data does not clear on
 * a timer, so a countdown there would promise something nothing will deliver — the server
 * sends no elapsesAt for that case and this must not invent one. Null also for a wait
 * already elapsed, which happens between the last gate passing and the coordinator's next
 * pass completing the deletion.
 */
export function minutesUntil(elapsesAt: string | null | undefined, now: number): number | null {
  if (!elapsesAt) return null;
  const at = Date.parse(elapsesAt);
  if (Number.isNaN(at)) return null;
  const remaining = at - now;
  if (remaining <= 0) return null;
  return Math.ceil(remaining / 60000);
}

/** A summary line's translation key and interpolation values. */
export interface DeletionSummary {
  key: string;
  values: Record<string, string | number>;
}

/**
 * The one-line answer to "is it done, and if not, why not".
 *
 * This is the sentence the whole panel exists to produce, and the states it distinguishes
 * are the ones an operator confuses at cost:
 *
 *  - a deletion with EVERYTHING CLEAN can still be hours from finishing. Showing "all
 *    systems clean" and then nothing for twelve hours reads as a hang and gets escalated as
 *    one, so the outstanding wait is named and dated.
 *  - a deletion that is BLOCKED is the only one a human can act on, and it is the only one
 *    with no time to quote.
 */
export function summarize(d: AdminTenantDeletion, now: number): DeletionSummary {
  if (isComplete(d)) {
    return { key: 'deletionSummaryComplete', values: { rows: d.rowsErased } };
  }
  const awaiting = d.awaiting as DeletionWait;
  if (awaiting === 'STORES') {
    return { key: 'deletionSummaryBlocked', values: { count: d.blockedBy.length } };
  }
  const minutes = minutesUntil(d.elapsesAt, now);
  if (minutes === null) {
    // Clean and every window elapsed: the next coordinator pass completes it. Saying
    // "shortly" rather than quoting a time is the honest reading — the exact moment depends
    // on the coordinator's interval, which this surface does not know.
    return { key: 'deletionSummaryFinishing', values: {} };
  }
  if (awaiting === 'TOKEN_HOLD') {
    return { key: 'deletionSummaryTokenHold', values: { token: d.token, ...humanize(minutes) } };
  }
  return { key: 'deletionSummarySettling', values: humanize(minutes) };
}

/**
 * Splits a minute count into the unit an operator would actually say.
 *
 * Twelve hours quoted as "720 minutes" is technically right and unreadable, and the token
 * hold is twelve hours by default — so the common case is the one that needs the larger
 * unit.
 */
export function humanize(minutes: number): { amount: number; unit: 'minutes' | 'hours' } {
  if (minutes >= 90) return { amount: Math.round(minutes / 60), unit: 'hours' };
  return { amount: minutes, unit: 'minutes' };
}

/**
 * Whether a record should keep being polled.
 *
 * A finished deletion never changes again, so polling one is pure waste — and the history
 * page can list many. Only an in-flight record is live.
 */
export function shouldPoll(d: AdminTenantDeletion | null): boolean {
  return d !== null && !isComplete(d);
}
