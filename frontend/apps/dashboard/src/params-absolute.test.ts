// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest';

import { parseDashParams } from './params';

describe('from and to (the absolute-window deep link)', () => {
  it('reads a zoned window, silently', () => {
    const warn = vi.fn();
    const got = parseDashParams('?from=2026-10-22T02:00:00Z&to=2026-10-22T04:00:00%2B02:00', warn);
    // 04:00 at +02:00 is 02:00Z, so this window is empty and is refused; the second case is the good one.
    expect(got.absolute).toBeUndefined();
    expect(got.rejected?.reason).toBe('order');
    const ok = parseDashParams('?from=2026-10-22T02:00:00Z&to=2026-10-22T06:00:00%2B02:00', vi.fn());
    expect(ok.absolute).toEqual({ fromMs: Date.UTC(2026, 9, 22, 2), toMs: Date.UTC(2026, 9, 22, 4) });
    expect(ok.rejected).toBeUndefined();
  });

  it('leaves both unset, silently, when absent', () => {
    const warn = vi.fn();
    const got = parseDashParams('?range=1h', warn);
    expect(got.absolute).toBeUndefined();
    expect(got.rejected).toBeUndefined();
    expect(warn).not.toHaveBeenCalled();
  });

  it.each([
    ['one end only', '?from=2026-10-22T02:00:00Z', 'incomplete'],
    ['no zone', '?from=2026-10-22T02:00:00&to=2026-10-22T04:00:00', 'unparseable'],
    ['epoch seconds', '?from=1792000000&to=1792003600', 'unparseable'],
    ['reversed', '?from=2026-10-22T04:00:00Z&to=2026-10-22T02:00:00Z', 'order'],
    ['years long', '?from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z', 'tooLong'],
    ['the future', '?from=2999-01-01T00:00:00Z&to=2999-01-01T01:00:00Z', 'future'],
  ])('ignores %s with a warning and a rejection to show', (_name, search, reason) => {
    const warn = vi.fn();
    const got = parseDashParams(search, warn);
    expect(got.absolute).toBeUndefined();
    expect(got.rejected?.reason).toBe(reason);
    expect(warn).toHaveBeenCalledTimes(1);
  });
});
