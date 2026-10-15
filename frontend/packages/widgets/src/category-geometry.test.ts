// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { niceDomain, pieSlices, polar, wedgePath } from './category-geometry';

describe('niceDomain', () => {
  it('rounds the top out to a round tick and starts at zero', () => {
    expect(niceDomain([18.2, 31.5])).toEqual({ min: 0, max: 40, step: 10, ticks: [0, 10, 20, 30, 40] });
    expect(niceDomain([55, 90, 21.5]).ticks).toEqual([0, 25, 50, 75, 100]);
  });

  it('holds every value, whatever the scale', () => {
    for (const top of [0.00037, 0.8, 7, 99, 1234, 56_000]) {
      const d = niceDomain([top / 3, top]);
      expect(d.max).toBeGreaterThanOrEqual(top);
      expect(d.ticks[0]).toBe(0);
      expect(d.ticks[d.ticks.length - 1]).toBe(d.max);
    }
  });

  it('reaches below zero for a negative value and keeps zero on the axis', () => {
    const d = niceDomain([-30, 10]);
    expect(d.min).toBeLessThanOrEqual(-30);
    expect(d.ticks).toContain(0);
  });

  it('gives an empty or all-zero chart an axis anyway', () => {
    expect(niceDomain([])).toMatchObject({ min: 0, max: 1 });
    expect(niceDomain([0, 0])).toMatchObject({ min: 0, max: 1 });
  });

  it('ignores a value that is not a finite number', () => {
    expect(niceDomain([NaN, Infinity, 5]).max).toBeGreaterThanOrEqual(5);
    expect(niceDomain([NaN, Infinity, 5]).max).toBeLessThan(100);
  });

  it('does not let float noise into the tick values', () => {
    expect(niceDomain([0.3]).ticks.every((t) => String(t).length <= 5)).toBe(true);
  });
});

describe('pieSlices', () => {
  it('lays shares out clockwise from the top, in order, adding to a whole turn', () => {
    const s = pieSlices([1, 1, 2]);
    expect(s.map((x) => x.fraction)).toEqual([0.25, 0.25, 0.5]);
    expect(s[0]).toMatchObject({ from: 0, to: 0.25 });
    expect(s[2]).toMatchObject({ from: 0.5, to: 1 });
  });

  it('gives a null, zero or negative value no angle but keeps its place', () => {
    const s = pieSlices([4, null, 0, -3, 4]);
    expect(s.map((x) => x.fraction)).toEqual([0.5, 0, 0, 0, 0.5]);
    expect(s.map((x) => x.index)).toEqual([0, 1, 2, 3, 4]);
  });

  it('has no shares at all when nothing is positive', () => {
    expect(pieSlices([0, null]).every((x) => x.fraction === 0)).toBe(true);
  });
});

describe('wedgePath', () => {
  it('starts at twelve oclock and runs clockwise', () => {
    const p = polar(50, 50, 40, 0.25);
    expect(p.x).toBeCloseTo(90);
    expect(p.y).toBeCloseTo(50);
    expect(polar(50, 50, 40, 0).y).toBeCloseTo(10);
  });

  it('draws a wedge, flagging the long way round for more than half', () => {
    expect(wedgePath(50, 50, 40, 0, 0.25)).toMatch(/A40,40 0 0 1/);
    expect(wedgePath(50, 50, 40, 0, 0.75)).toMatch(/A40,40 0 1 1/);
  });

  it('draws a whole circle for a slice that fills it, and nothing for an empty one', () => {
    expect(wedgePath(50, 50, 40, 0, 1)).toMatch(/^M50,10A40,40 0 1 1 50,90A40,40 0 1 1 50,10Z$/);
    expect(wedgePath(50, 50, 40, 0.3, 0.3)).toBeUndefined();
  });
});
