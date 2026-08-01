// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { formatValue } from './format';

describe('formatValue', () => {
  it('shows an em dash for a missing value', () => {
    expect(formatValue(null)).toBe('—');
    expect(formatValue(undefined)).toBe('—');
    expect(formatValue(undefined, 2)).toBe('—');
  });

  it('leaves the value unrounded when no precision is configured', () => {
    expect(formatValue(31.19073834583732)).toBe('31.19073834583732');
  });

  it('rounds to the configured precision', () => {
    expect(formatValue(31.19073834583732, 1)).toBe('31.2');
    expect(formatValue(31.19073834583732, 0)).toBe('31');
  });

  it('never throws on a precision outside toFixed’s domain', () => {
    // The precision comes from an opaque stored definition (options.precision), and the
    // console's config panel can author a negative one today. Unclamped, toFixed raises a
    // RangeError DURING RENDER, which unmounts the widget's whole subtree — every other
    // bad option value in this package degrades instead. Clamped, the value still shows.
    expect(() => formatValue(1.5, -1)).not.toThrow();
    expect(formatValue(1.5, -1)).toBe('2');
    expect(() => formatValue(1.5, 101)).not.toThrow();
    expect(formatValue(1.5, 101)).toBe((1.5).toFixed(100));
    expect(() => formatValue(1.5, Number.NaN)).not.toThrow();
    expect(() => formatValue(1.5, Number.POSITIVE_INFINITY)).not.toThrow();
  });

  it('truncates a fractional precision the way toFixed already would', () => {
    expect(formatValue(1.2345, 2.7)).toBe('1.23');
  });
});
