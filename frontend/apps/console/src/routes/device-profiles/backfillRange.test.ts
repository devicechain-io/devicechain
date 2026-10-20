// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { backfillRangeProblem, defaultBackfillRange, parseDeviceTokens } from './backfillRange';

const DAY = 86_400_000;
const NOW = new Date('2026-10-20T12:00:00Z');

describe('defaultBackfillRange', () => {
  it('ends at activation and starts 30 days before it', () => {
    const r = defaultBackfillRange('2026-10-10T12:00:00Z', NOW);
    expect(r.to.toISOString()).toBe('2026-10-10T12:00:00.000Z');
    expect(r.to.getTime() - r.from.getTime()).toBe(30 * DAY);
  });

  it('ends now for a version that is not active', () => {
    expect(defaultBackfillRange(null, NOW).to).toEqual(NOW);
  });

  it('does not end in the future if the activation instant is ahead of the clock', () => {
    expect(defaultBackfillRange('2026-10-21T00:00:00Z', NOW).to).toEqual(NOW);
  });
});

describe('backfillRangeProblem', () => {
  const to = new Date('2026-10-10T00:00:00Z');
  it('accepts 90 days exactly and refuses one minute more', () => {
    expect(backfillRangeProblem(new Date(to.getTime() - 90 * DAY), to, NOW)).toBeNull();
    expect(backfillRangeProblem(new Date(to.getTime() - 90 * DAY - 60_000), to, NOW)).toBe('span');
  });
  it('refuses an empty, reversed, future or unreadable range', () => {
    expect(backfillRangeProblem(to, to, NOW)).toBe('order');
    expect(backfillRangeProblem(new Date(to.getTime() + 1), to, NOW)).toBe('order');
    expect(backfillRangeProblem(new Date(NOW.getTime() - DAY), new Date(NOW.getTime() + DAY), NOW)).toBe('future');
    expect(backfillRangeProblem(new Date(NaN), to, NOW)).toBe('invalid');
  });
});

describe('parseDeviceTokens', () => {
  it('splits on commas and whitespace, drops blanks and repeats', () => {
    expect(parseDeviceTokens('a, b\n\nc  a,')).toEqual(['a', 'b', 'c']);
    expect(parseDeviceTokens('  ')).toEqual([]);
  });
});
