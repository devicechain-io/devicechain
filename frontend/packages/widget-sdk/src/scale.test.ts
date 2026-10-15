// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { SIZE_CLASS_BREAKS as B, makeWidgetScale, sizeClassOf, useStickyChars } from './scale';

describe('sizeClassOf', () => {
  it('is regular before the first measure (the default rendering, not a degraded one)', () => {
    expect(sizeClassOf({ width: 0, height: 0 })).toBe('regular');
  });

  it('is compact when either dimension is under its break', () => {
    expect(sizeClassOf({ width: B.compactWidth - 1, height: 500 })).toBe('compact');
    expect(sizeClassOf({ width: 500, height: B.compactHeight - 1 })).toBe('compact');
  });

  it('is large only when both dimensions clear the large break', () => {
    expect(sizeClassOf({ width: B.largeWidth, height: B.largeHeight })).toBe('large');
    expect(sizeClassOf({ width: B.largeWidth, height: B.largeHeight - 1 })).toBe('regular');
  });

  it('moves the breaks with density', () => {
    const box = { width: B.compactWidth + 10, height: B.compactHeight + 10 };
    expect(sizeClassOf(box, 1)).toBe('regular');
    expect(sizeClassOf(box, 1.5)).toBe('compact');
  });
});

describe('makeWidgetScale', () => {
  it('px() resolves a size token at the density scale', () => {
    const s = makeWidgetScale({ width: 400, height: 200 }, 1.5);
    expect(s.px('font-md')).toBeCloseTo(13 * 1.5);
    expect(s.px('space-1')).toBeCloseTo(4 * 1.5);
  });
});

describe('useStickyChars', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('holds a longer length, then lets it go when the hold runs out with NO new render', () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(({ chars }) => useStickyChars(chars, 60_000), {
      initialProps: { chars: 4 },
    });
    rerender({ chars: 1 });
    expect(result.current).toBe(4);
    act(() => {
      vi.advanceTimersByTime(59_000);
    });
    expect(result.current).toBe(4);
    // The value has not changed since, so nothing but the hook's own timer re-renders it: a
    // count that fell to 9 and stays there must not keep the 4-character size forever.
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(result.current).toBe(1);
  });
});
