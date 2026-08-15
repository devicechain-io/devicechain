// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The four counts a batch cancellation returns DELIBERATELY NEED NOT SUM, and the gap
// between `matched` and the other three is the only evidence an operator gets that the
// cancel did not stop everything. These are the rules that find it.

import { describe, expect, it } from 'vitest';

import {
  STILL_HELD_STATUSES,
  accountedFor,
  hasUnstoppable,
  needsSecondCancel,
  unaccounted,
} from './cancelOutcome';

const counts = (over: Partial<Parameters<typeof unaccounted>[0]> = {}) => ({
  cancelled: 0,
  alreadySent: 0,
  alreadyFinished: 0,
  matched: 0,
  ...over,
});

describe('the shortfall against matched', () => {
  // 🔴 THE ONE THAT MATTERS. A command whose dispatcher tried to send it and failed goes
  // back into the delivery queue between the write and the count, landing in none of the
  // three buckets. It is live, this cancel did not stop it, and nothing retries.
  it('counts the commands the cancel could not account for', () => {
    const c = counts({ cancelled: 900, alreadySent: 60, alreadyFinished: 20, matched: 1000 });
    expect(accountedFor(c)).toBe(980);
    expect(unaccounted(c)).toBe(20);
    expect(needsSecondCancel(c)).toBe(true);
  });

  // The counterweight: a rule that always said "cancel again" would pass the test above
  // and cry wolf on every settled batch, which is how an operator learns to ignore it.
  it('asks for nothing more when every command is accounted for', () => {
    const c = counts({ cancelled: 7, alreadySent: 2, alreadyFinished: 1, matched: 10 });
    expect(unaccounted(c)).toBe(0);
    expect(needsSecondCancel(c)).toBe(false);
  });

  // `matched` BELOW the sum is the opposite race — a command counted in a bucket and then
  // deleted — and means nothing is wrong. Rendering "-2 commands are back in the delivery
  // queue" would invent an alarm out of it.
  it('never reports a negative shortfall when matched falls below the sum', () => {
    const c = counts({ cancelled: 5, alreadySent: 5, alreadyFinished: 5, matched: 3 });
    expect(unaccounted(c)).toBe(0);
    expect(needsSecondCancel(c)).toBe(false);
  });

  it('sees a shortfall even when nothing at all was stopped', () => {
    const c = counts({ cancelled: 0, alreadySent: 0, alreadyFinished: 0, matched: 4 });
    expect(unaccounted(c)).toBe(4);
    expect(needsSecondCancel(c)).toBe(true);
  });
});

describe('commands that cannot be recalled', () => {
  // `alreadySent` is the count of commands that are AT their devices. It is never part of
  // "stopped", and a screen that has any of them has to say the fleet is still acting.
  it('is flagged whenever any command had already reached a device', () => {
    expect(hasUnstoppable(counts({ cancelled: 999, alreadySent: 1, matched: 1000 }))).toBe(true);
    expect(hasUnstoppable(counts({ cancelled: 1000, alreadySent: 0, matched: 1000 }))).toBe(false);
  });

  it('is not folded into what the cancel accounted for as though it were stopped', () => {
    const c = counts({ cancelled: 1, alreadySent: 300, alreadyFinished: 0, matched: 301 });
    // The sum exists — that is what makes the batch settled — but it is emphatically not
    // the number of commands that were stopped.
    expect(accountedFor(c)).toBe(301);
    expect(c.cancelled).toBe(1);
    expect(needsSecondCancel(c)).toBe(false);
  });
});

describe('the states a cancel can actually stop', () => {
  // Derived from the shared vocabulary through the shared predicate, not hand-written —
  // asserted here so a status the service adds cannot quietly drop out of the count.
  it('is exactly the three states in which the platform still holds the command', () => {
    expect([...STILL_HELD_STATUSES].sort()).toEqual(['HELD', 'PARKED', 'QUEUED']);
  });

  // 🔴 SENT is in flight but NOT stoppable: the command is at the device. Counting it as
  // "still held" would tell an operator a cancel can take back something it cannot.
  it('excludes SENT', () => {
    expect(STILL_HELD_STATUSES).not.toContain('SENT');
  });
});
