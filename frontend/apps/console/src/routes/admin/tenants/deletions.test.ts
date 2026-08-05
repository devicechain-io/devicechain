import { describe, it, expect } from 'vitest';

import type { AdminTenantDeletion, AdminTenantDeletionStore } from '@/lib/api/admin';
import {
  hasNote,
  humanize,
  isComplete,
  minutesUntil,
  remedyKey,
  shouldPoll,
  storeProblem,
  storeState,
  summarize,
} from './deletions';

const NOW = Date.parse('2026-08-04T12:00:00Z');

function line(over: Partial<AdminTenantDeletionStore> = {}): AdminTenantDeletionStore {
  return {
    store: 'rdb',
    complete: true,
    rowsErased: 0,
    retaining: null,
    lastError: null,
    note: null,
    attemptedAt: '2026-08-04T11:59:00Z',
    cleanSince: '2026-08-04T11:59:00Z',
    ...over,
  } as AdminTenantDeletionStore;
}

function record(over: Partial<AdminTenantDeletion> = {}): AdminTenantDeletion {
  return {
    token: 'acme',
    epoch: '2026-08-04T00:00:00Z',
    completedAt: null,
    rowsErased: 0,
    awaiting: 'NONE',
    elapsesAt: null,
    blockedBy: [],
    stores: [],
    ...over,
  } as AdminTenantDeletion;
}

describe('storeState', () => {
  // 🔴 The three states must stay three. Collapsing retaining and retrying into one
  // "problem" state tells an operator to act on the half that is already fixing itself and
  // to wait on the half that never will.
  it('separates holding data from failing a pass', () => {
    expect(storeState(line())).toBe('clean');
    expect(storeState(line({ complete: false, retaining: 'an object remains' }))).toBe('retaining');
    expect(storeState(line({ complete: false, lastError: 'connection refused' }))).toBe('retrying');
  });

  it('reads a store that holds data AND errored as retaining', () => {
    // The deferral is the half that will not clear on its own, so it is the half to show.
    const both = line({ complete: false, retaining: 'an object remains', lastError: 'refused' });
    expect(storeState(both)).toBe('retaining');
    expect(storeProblem(both)).toBe('an object remains');
  });
});

describe('storeProblem', () => {
  it('is null for a clean store even when it carries a note', () => {
    // The note must never reach the problem column: a store carrying only a note is working
    // exactly as designed, and rendering it as a problem is the failure this whole
    // three-field split exists to prevent.
    const noted = line({ note: 'the exempted buckets were not scanned' });
    expect(storeProblem(noted)).toBeNull();
    expect(hasNote(noted)).toBe(true);
    expect(storeState(noted)).toBe('clean');
  });

  it('is the error when a failing store has nothing it is retaining', () => {
    expect(storeProblem(line({ complete: false, lastError: 'refused' }))).toBe('refused');
  });
});

describe('remedyKey', () => {
  it('offers a remedy only for the blockers a human can clear', () => {
    expect(remedyKey(line({ store: 'detect', complete: false, retaining: 'engine state' })))
      .toBe('remedyDetect');
    expect(remedyKey(line({ store: 'blob', complete: false, retaining: 'an object remains' })))
      .toBe('remedyBlob');
  });

  it('offers nothing for a clean store, or for one that is merely retrying', () => {
    // A retrying store needs no action — the coordinator retries every pass. Suggesting one
    // implies the absence of a retry and invites an operator to believe they caused it.
    expect(remedyKey(line({ store: 'detect' }))).toBeNull();
    expect(remedyKey(line({ store: 'detect', complete: false, lastError: 'refused' }))).toBeNull();
    expect(remedyKey(line({ store: 'rdb', complete: false, retaining: 'rows remain' }))).toBeNull();
  });
});

describe('minutesUntil', () => {
  it('counts down to a window', () => {
    expect(minutesUntil('2026-08-04T12:30:00Z', NOW)).toBe(30);
  });

  it('is null with no time to count down to', () => {
    // Null for STORES is the important one: a store holding data does not clear on a timer,
    // so a countdown would promise something nothing will deliver.
    expect(minutesUntil(null, NOW)).toBeNull();
    expect(minutesUntil(undefined, NOW)).toBeNull();
    expect(minutesUntil('not-a-time', NOW)).toBeNull();
    expect(minutesUntil('2026-08-04T11:00:00Z', NOW)).toBeNull();
  });
});

describe('humanize', () => {
  it('switches to hours before the number becomes unreadable', () => {
    // The token hold is twelve hours by default, so the common case is the one that needs
    // the larger unit — "720 minutes" is correct and useless.
    expect(humanize(45)).toEqual({ amount: 45, unit: 'minutes' });
    expect(humanize(720)).toEqual({ amount: 12, unit: 'hours' });
  });
});

describe('summarize', () => {
  it('reports a completed deletion as done', () => {
    const done = record({ completedAt: '2026-08-04T11:00:00Z', rowsErased: 91 });
    expect(summarize(done, NOW)).toEqual({ key: 'deletionSummaryComplete', values: { rows: 91 } });
    expect(isComplete(done)).toBe(true);
  });

  it('reports a blocked deletion by count, with no time quoted', () => {
    const blocked = record({ awaiting: 'STORES', blockedBy: ['detect: engine state remains'] });
    expect(summarize(blocked, NOW)).toEqual({
      key: 'deletionSummaryBlocked',
      values: { count: 1 },
    });
  });

  // 🔴 THE CASE THAT GETS ESCALATED AS A HANG. Everything is clean and the deletion is
  // still hours from finishing. A surface that said "all systems clean" and then sat there
  // would be reported as broken, so the outstanding wait must be named AND dated.
  it('names the settle wait rather than claiming everything is done', () => {
    const settling = record({ awaiting: 'SETTLE', elapsesAt: '2026-08-04T12:04:00Z' });
    expect(summarize(settling, NOW)).toEqual({
      key: 'deletionSummarySettling',
      values: { amount: 4, unit: 'minutes' },
    });
  });

  it('names the token hold and the token it will release', () => {
    const holding = record({ awaiting: 'TOKEN_HOLD', elapsesAt: '2026-08-04T21:00:00Z' });
    expect(summarize(holding, NOW)).toEqual({
      key: 'deletionSummaryTokenHold',
      values: { token: 'acme', amount: 9, unit: 'hours' },
    });
  });

  it('says finishing when every window has already elapsed', () => {
    // Between the last gate passing and the coordinator's next pass. Quoting a time here
    // would mean inventing the coordinator's interval, which this surface does not know.
    const ready = record({ awaiting: 'TOKEN_HOLD', elapsesAt: '2026-08-04T11:00:00Z' });
    expect(summarize(ready, NOW)).toEqual({ key: 'deletionSummaryFinishing', values: {} });
  });

  it('never quotes a countdown for a blocked deletion, even if the server sent one', () => {
    // Defence in depth against a future server that populates elapsesAt for STORES: the
    // blocked branch must win, because the wait is not a window.
    const blocked = record({
      awaiting: 'STORES',
      blockedBy: ['blob: an object remains'],
      elapsesAt: '2026-08-04T12:30:00Z',
    });
    expect(summarize(blocked, NOW).key).toBe('deletionSummaryBlocked');
  });
});

describe('shouldPoll', () => {
  it('polls an in-flight deletion and never a finished one', () => {
    expect(shouldPoll(record())).toBe(true);
    expect(shouldPoll(record({ completedAt: '2026-08-04T11:00:00Z' }))).toBe(false);
    expect(shouldPoll(null)).toBe(false);
  });
});
