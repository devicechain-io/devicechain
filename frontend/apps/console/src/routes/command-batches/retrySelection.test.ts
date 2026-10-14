// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  buildCandidates,
  classOf,
  completeRefusals,
  defaultSelection,
  isSelectable,
  planSubmission,
} from './retrySelection';

describe('classOf', () => {
  it('reads the stored outcome and nothing else', () => {
    // The error text deliberately says the opposite: prose must not decide.
    expect(
      classOf({ deviceToken: 'a', outcome: 'NOT_DELIVERED', error: 'device returned 4.04' }),
    ).toBe('NOT_DELIVERED');
    expect(
      classOf({ deviceToken: 'a', outcome: 'REJECTED', error: 'It was never delivered' }),
    ).toBe('REJECTED');
    expect(classOf({ deviceToken: 'a', outcome: 'UNKNOWN' })).toBe('UNKNOWN');
  });

  it('never offers a succeeded or unfinished command', () => {
    expect(classOf({ deviceToken: 'a', outcome: 'SUCCEEDED' })).toBeNull();
    expect(classOf({ deviceToken: 'a', outcome: null })).toBeNull();
    expect(classOf({ deviceToken: 'a' })).toBeNull();
  });

  it('treats an outcome it does not know as unknown, not safe', () => {
    expect(classOf({ deviceToken: 'a', outcome: 'SOMETHING_NEW' })).toBe('UNKNOWN');
  });
});

describe('completeRefusals', () => {
  it('keeps a code only when its sample equals its exact total', () => {
    const refusals = [
      { deviceToken: 'x', code: 'DEVICE_DISABLED', reason: 'r' },
      { deviceToken: 'y', code: 'NOT_IN_VOCAB', reason: 'r' },
    ];
    const counts = [
      { code: 'DEVICE_DISABLED', count: 1 },
      { code: 'NOT_IN_VOCAB', count: 150 },
    ];
    expect(completeRefusals(refusals, counts).map((r) => r.deviceToken)).toEqual(['x']);
  });
});

describe('selection', () => {
  const candidates = buildCandidates(
    [
      { deviceToken: 'nd', outcome: 'NOT_DELIVERED', error: 'e' },
      { deviceToken: 'rj', outcome: 'REJECTED', error: 'e' },
      { deviceToken: 'uk', outcome: 'UNKNOWN', error: 'e' },
      { deviceToken: 'ok', outcome: 'SUCCEEDED' },
    ],
    [{ deviceToken: 'rf', code: 'DEVICE_DISABLED', reason: 'disabled' }],
    [{ code: 'DEVICE_DISABLED', count: 1 }],
  );

  it('lists one entry per retryable device', () => {
    expect(candidates.map((c) => [c.deviceToken, c.cls])).toEqual([
      ['nd', 'NOT_DELIVERED'],
      ['rj', 'REJECTED'],
      ['uk', 'UNKNOWN'],
      ['rf', 'REFUSED'],
    ]);
  });

  it('preselects only the proven-not-acted classes', () => {
    expect([...defaultSelection(candidates)].sort()).toEqual(['nd', 'rj']);
  });

  it('makes unknown devices selectable only after acknowledgement', () => {
    const uk = candidates.find((c) => c.cls === 'UNKNOWN')!;
    expect(isSelectable(uk, false)).toBe(false);
    expect(isSelectable(uk, true)).toBe(true);
  });

  it('sends includeUnknown only when an acknowledged unknown device is chosen', () => {
    const all = new Set(['nd', 'uk']);
    expect(planSubmission(candidates, all, false)).toEqual({
      deviceTokens: ['nd'],
      includeUnknown: false,
    });
    expect(planSubmission(candidates, all, true)).toEqual({
      deviceTokens: ['nd', 'uk'],
      includeUnknown: true,
    });
    // Acknowledged, but no unknown device chosen: nothing to acknowledge on the wire.
    expect(planSubmission(candidates, new Set(['nd']), true).includeUnknown).toBe(false);
  });
});
