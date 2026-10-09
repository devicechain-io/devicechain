// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import { formatDateTime, formatTimestamp, formatValue } from './format';

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

describe('formatDateTime / formatTimestamp time zone', () => {
  const iso = '2026-10-07T02:08:54.423Z';

  it('without a zone is exactly the viewer-local rendering it always was', () => {
    expect(formatDateTime(iso)).toBe(new Date(iso).toLocaleString());
    expect(formatTimestamp(iso)).toBe(new Date(iso).toLocaleTimeString());
  });

  it('with a zone prints that zone’s clock and names it', () => {
    expect(formatDateTime(iso, 'UTC')).toBe(new Date(iso).toLocaleString(undefined, { timeZone: 'UTC', timeZoneName: 'short' }));
    expect(formatDateTime(iso, 'UTC')).toContain('UTC');
    // Not the same instant on the wall in a zone a day behind: the date differs.
    const ny = formatDateTime(iso, 'America/New_York');
    expect(ny).not.toBe(formatDateTime(iso, 'UTC'));
    expect(formatTimestamp(iso, 'Asia/Tokyo')).toMatch(/JST|GMT[+]9/);
  });

  it('leaves an unparseable value or an absent one as before, zone or not', () => {
    expect(formatDateTime('not a date', 'UTC')).toBe('not a date');
    expect(formatDateTime(null, 'UTC')).toBe('');
    expect(formatTimestamp(null, 'UTC')).toBe('');
  });
});
