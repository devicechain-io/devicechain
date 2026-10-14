// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { FIT_FALLBACK_FONT_PX, FIT_MIN_FONT_PX, fitValueFont } from './fit-text';

describe('fitValueFont', () => {
  it('never shrinks as the box grows in either dimension', () => {
    for (const chars of [1, 4, 9]) {
      let prev = 0;
      for (let w = 40; w <= 1200; w += 20) {
        const f = fitValueFont({ width: w, height: 300 }, chars);
        expect(f).toBeGreaterThanOrEqual(prev);
        prev = f;
      }
      prev = 0;
      for (let h = 20; h <= 600; h += 10) {
        const f = fitValueFont({ width: 300, height: h }, chars);
        expect(f).toBeGreaterThanOrEqual(prev);
        prev = f;
      }
    }
  });

  it('grows in both dimensions somewhere (it is not a one-axis clamp)', () => {
    expect(fitValueFont({ width: 400, height: 200 }, 3)).toBeGreaterThan(fitValueFont({ width: 200, height: 200 }, 3));
    expect(fitValueFont({ width: 400, height: 200 }, 3)).toBeGreaterThan(fitValueFont({ width: 400, height: 100 }, 3));
  });

  it('gives a longer string a smaller font in a width-bound box', () => {
    const box = { width: 160, height: 400 };
    expect(fitValueFont(box, 12)).toBeLessThan(fitValueFont(box, 4));
  });

  it('has no hard cap: a wall-sized box gets a wall-sized number', () => {
    expect(fitValueFont({ width: 1600, height: 800 }, 3)).toBeGreaterThan(56);
  });

  it('floors at a readable minimum for a tiny box or a very long string', () => {
    expect(fitValueFont({ width: 20, height: 10 }, 30)).toBe(FIT_MIN_FONT_PX);
  });

  it('falls back to a nominal size before the first measure', () => {
    expect(fitValueFont({ width: 0, height: 0 }, 5)).toBe(FIT_FALLBACK_FONT_PX);
  });

  it('treats a degenerate length as one character rather than dividing by zero', () => {
    expect(fitValueFont({ width: 100, height: 100 }, 0)).toBe(fitValueFont({ width: 100, height: 100 }, 1));
    expect(Number.isFinite(fitValueFont({ width: 100, height: 100 }, NaN))).toBe(true);
  });

  it('does not overflow the sitepulse card (about 153x88 px) for a 6-char value', () => {
    // Content box: the 153x88 frame minus the 1px border, a ~30px title bar and 12/16 padding.
    const box = { width: 153 - 2 - 32, height: 88 - 2 - 30 - 24 };
    const chars = 6;
    const f = fitValueFont(box, chars);
    expect(f * 0.65 * chars).toBeLessThanOrEqual(box.width);
    expect(f * 1.1).toBeLessThanOrEqual(box.height + 1);
  });
});
