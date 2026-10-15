// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { fitValueFont, fitValueFontCss, stickyChars } from './fit-text';

describe('fitValueFont floor (density)', () => {
  it('scales the floor, not the curve', () => {
    // A box so small the floor decides: the floor passed IS the answer.
    expect(fitValueFont({ width: 30, height: 20 }, 8, { floor: 18 })).toBe(18);
    // A roomy box: the curve decides, and the floor changes nothing.
    const roomy = { width: 600, height: 300 };
    expect(fitValueFont(roomy, 3, { floor: 18 })).toBe(fitValueFont(roomy, 3));
  });

  it('keeps the unmeasured fallback above a raised floor', () => {
    expect(fitValueFont({ width: 0, height: 0 }, 3, { floor: 60 })).toBe(60);
  });
});

describe('fitValueFontCss (the cq path)', () => {
  // The CSS string and the JS number are built from the same constants; parse the string back
  // and evaluate it against a box so the two cannot drift apart unnoticed.
  function evalCss(css: string, size: { width: number; height: number }): number {
    const m = /^max\((\d+)px, min\(([\d.]+)cqh, ([\d.]+)cqw\)\)$/.exec(css);
    if (!m) throw new Error(`unexpected shape: ${css}`);
    const [floor, h, w] = [Number(m[1]), Number(m[2]), Number(m[3])];
    return Math.max(floor, Math.min((h / 100) * size.height, (w / 100) * size.width));
  }

  it('agrees with fitValueFont, to the JS path floor() rounding, across boxes and lengths', () => {
    for (const chars of [1, 3, 6, 11]) {
      for (const size of [
        { width: 120, height: 40 },
        { width: 300, height: 200 },
        { width: 960, height: 540 },
        { width: 1900, height: 300 },
      ]) {
        const js = fitValueFont(size, chars);
        const css = evalCss(fitValueFontCss(chars), size);
        expect(css).toBeGreaterThanOrEqual(js);
        expect(css - js).toBeLessThan(1.0001);
      }
    }
  });

  it('carries the floor it is given', () => {
    expect(fitValueFontCss(4, { floor: 18 })).toMatch(/^max\(18px,/);
  });
});

describe('stickyChars', () => {
  it('grows at once', () => {
    expect(stickyChars({ value: 4, since: 0 }, 5, 10, 60_000)).toEqual({ value: 5, since: 10 });
  });

  it('holds the longer length while a shorter value arrives within the hold (9.99 to 10.01 shape)', () => {
    expect(stickyChars({ value: 5, since: 0 }, 4, 1_000, 60_000).value).toBe(5);
  });

  it('lets go after the hold with nothing longer', () => {
    expect(stickyChars({ value: 5, since: 0 }, 4, 60_000, 60_000).value).toBe(4);
  });

  it('a repeat of the longest length restarts the hold', () => {
    expect(stickyChars({ value: 5, since: 0 }, 5, 50_000, 60_000)).toEqual({ value: 5, since: 50_000 });
  });
});
