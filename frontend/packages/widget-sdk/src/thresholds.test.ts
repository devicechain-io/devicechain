// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { bandAt, parseThresholds, thresholdBands } from './thresholds';

describe('parseThresholds', () => {
  it('keeps value and level (and a non-empty label), sorted ascending', () => {
    expect(
      parseThresholds([
        { value: 90, level: 'critical', label: 'Trip' },
        { value: 10, level: 'warning', label: '' },
      ]),
    ).toEqual([
      { value: 10, level: 'warning' },
      { value: 90, level: 'critical', label: 'Trip' },
    ]);
  });

  it('REJECTS a free colour: only the severity vocabulary survives, and no colour key is read', () => {
    const parsed = parseThresholds([
      { value: 1, level: '#ff0000' },
      { value: 2, level: 'red' },
      { value: 3, level: 'critical', color: '#00ff00' },
    ]);
    expect(parsed).toEqual([{ value: 3, level: 'critical' }]);
    expect(JSON.stringify(parsed)).not.toContain('#');
  });

  it('drops malformed entries instead of throwing during render', () => {
    expect(parseThresholds([null, 7, 'x', { value: 'a', level: 'normal' }, { value: NaN, level: 'normal' }, {}])).toEqual([]);
    expect(parseThresholds(undefined)).toEqual([]);
    expect(parseThresholds({ value: 1, level: 'normal' })).toEqual([]);
  });

  it('keeps authored order for equal values', () => {
    const out = parseThresholds([
      { value: 5, level: 'minor' },
      { value: 5, level: 'major' },
    ]);
    expect(out.map((t) => t.level)).toEqual(['minor', 'major']);
  });
});

describe('bandAt (a threshold starts a band; a value ON it is in that band)', () => {
  const t = parseThresholds([
    { value: 0, level: 'critical' },
    { value: 15, level: 'normal' },
    { value: 80, level: 'major' },
  ]);

  it('selects the band at, below and above each boundary', () => {
    expect(bandAt(t, -0.001)).toBeUndefined(); // below the lowest: neutral
    expect(bandAt(t, 0)?.level).toBe('critical'); // on the lowest: its band
    expect(bandAt(t, 14.999)?.level).toBe('critical');
    expect(bandAt(t, 15)?.level).toBe('normal'); // ON the boundary: the upper band
    expect(bandAt(t, 79.999)?.level).toBe('normal');
    expect(bandAt(t, 80)?.level).toBe('major');
    expect(bandAt(t, 1e9)?.level).toBe('major'); // the last band is open-ended
  });

  it('has no band for a missing, null or non-finite value, or with no thresholds', () => {
    expect(bandAt(t, null)).toBeUndefined();
    expect(bandAt(t, undefined)).toBeUndefined();
    expect(bandAt(t, NaN)).toBeUndefined();
    expect(bandAt([], 5)).toBeUndefined();
  });
});

describe('thresholdBands', () => {
  const t = parseThresholds([
    { value: 50, level: 'warning' },
    { value: 150, level: 'critical' },
  ]);

  it('tiles [min, max] exactly, neutral first', () => {
    expect(thresholdBands(t, 0, 200)).toEqual([
      { from: 0, to: 50, level: undefined },
      { from: 50, to: 150, level: 'warning' },
      { from: 150, to: 200, level: 'critical' },
    ]);
  });

  it('clips to the range and carries the band in effect at min', () => {
    expect(thresholdBands(t, 100, 120)).toEqual([{ from: 100, to: 120, level: 'warning' }]);
    expect(thresholdBands(t, 60, 400).map((b) => b.level)).toEqual(['warning', 'critical']);
  });

  it('is empty without thresholds or for an empty range', () => {
    expect(thresholdBands([], 0, 100)).toEqual([]);
    expect(thresholdBands(t, 10, 10)).toEqual([]);
  });
});
