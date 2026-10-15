// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The wait between reconnect attempts. subscribe() retries a dropped socket for as long as
// anything is subscribed, so this is all that keeps a long outage from becoming a tight loop,
// and a fleet of screens that lost the same server from coming back on the same tick.

import { describe, expect, it } from 'vitest';

import { RETRY_CEILING_MS, retryDelayMs } from './subscribe';

describe('retryDelayMs', () => {
  it('doubles from one second, within the upper half of each step', () => {
    expect([0, 1, 2, 3, 4].map((n) => retryDelayMs(n, () => 1))).toEqual([1000, 2000, 4000, 8000, 16000]);
    expect([0, 1, 2, 3, 4].map((n) => retryDelayMs(n, () => 0))).toEqual([500, 1000, 2000, 4000, 8000]);
  });

  it('stops growing at the ceiling, however many attempts have failed', () => {
    for (const n of [5, 6, 20, 1000]) {
      expect(retryDelayMs(n, () => 1)).toBe(RETRY_CEILING_MS);
      expect(retryDelayMs(n, () => 0)).toBe(RETRY_CEILING_MS / 2);
    }
  });

  it('is spread by the random draw', () => {
    expect(retryDelayMs(10, () => 0.25)).toBe(RETRY_CEILING_MS * 0.625);
    expect(retryDelayMs(10, () => 0.75)).toBe(RETRY_CEILING_MS * 0.875);
  });
});
