// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { parseThresholds } from '@devicechain/widget-sdk';
import { describe, expect, it } from 'vitest';

import { bandSpans, fillOf, litBars, readRange } from './value-scale';

describe('readRange', () => {
  it('reads both ends from the options', () => {
    expect(readRange(-10, 40)).toEqual({ ok: true, range: { min: -10, max: 40 } });
  });

  it('never invents a range: an unset end is missing, with or without the other', () => {
    expect(readRange(undefined, undefined)).toEqual({ ok: false, reason: 'missing' });
    expect(readRange(0, undefined)).toEqual({ ok: false, reason: 'missing' });
    expect(readRange(undefined, 100)).toEqual({ ok: false, reason: 'missing' });
  });

  it('uses the documented fallback for an unset end, and only for that end', () => {
    const fallback = { min: 0, max: 100 };
    expect(readRange(undefined, undefined, fallback)).toEqual({ ok: true, range: { min: 0, max: 100 } });
    expect(readRange(3, 4.2, fallback)).toEqual({ ok: true, range: { min: 3, max: 4.2 } });
    expect(readRange(50, undefined, fallback)).toEqual({ ok: true, range: { min: 50, max: 100 } });
  });

  it('refuses an empty or inverted scale', () => {
    expect(readRange(5, 5)).toEqual({ ok: false, reason: 'invalid' });
    expect(readRange(10, 0)).toEqual({ ok: false, reason: 'invalid' });
    expect(readRange(200, undefined, { min: 0, max: 100 })).toEqual({ ok: false, reason: 'invalid' });
  });
});

describe('fillOf', () => {
  const range = { min: 10, max: 30 };

  it.each([
    [10, 0],
    [20, 0.5],
    [30, 1],
    [15, 0.25],
  ])('value %s fills %s of the scale', (value, fraction) => {
    expect(fillOf(value, range)).toEqual({ fraction, position: 'within', ratio: fraction });
  });

  it('pins a value outside the scale to its end and says which side', () => {
    expect(fillOf(50, range)).toEqual({ fraction: 1, position: 'above', ratio: 2 });
    expect(fillOf(-10, range)).toEqual({ fraction: 0, position: 'below', ratio: -1 });
  });

  it('has no fill without a usable value', () => {
    expect(fillOf(null, range)).toBeUndefined();
    expect(fillOf(undefined, range)).toBeUndefined();
    expect(fillOf(Number.NaN, range)).toBeUndefined();
    expect(fillOf(Number.POSITIVE_INFINITY, range)).toBeUndefined();
  });
});

describe('bandSpans', () => {
  it('lays the bands over the scale as fractions, with a neutral lead-in', () => {
    const t = parseThresholds([
      { value: 20, level: 'warning' },
      { value: 30, level: 'critical' },
    ]);
    expect(bandSpans(t, { min: 10, max: 40 })).toEqual([
      { from: 0, to: 1 / 3, level: undefined },
      { from: 1 / 3, to: 2 / 3, level: 'warning' },
      { from: 2 / 3, to: 1, level: 'critical' },
    ]);
  });

  it('has no spans without thresholds', () => {
    expect(bandSpans([], { min: 0, max: 1 })).toEqual([]);
  });
});

describe('litBars', () => {
  const steps = parseThresholds([
    { value: -110, level: 'critical' },
    { value: -100, level: 'warning' },
    { value: -90, level: 'normal' },
    { value: -80, level: 'normal' },
  ]);

  it.each([
    [-120, 0], // below the lowest step: no signal, not a missing reading
    [-110, 1], // ON a step counts as having reached it
    [-101, 1],
    [-100, 2],
    [-85, 3],
    [-80, 4],
    [-20, 4],
  ])('%s dBm lights %s bars', (value, lit) => {
    expect(litBars(steps, value)).toEqual({ total: 4, lit });
  });

  it('has nothing without a value', () => {
    expect(litBars(steps, null)).toBeUndefined();
    expect(litBars(steps, undefined)).toBeUndefined();
  });
});
