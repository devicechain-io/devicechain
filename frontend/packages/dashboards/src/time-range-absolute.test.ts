// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';

import {
  absoluteRange,
  absoluteToParams,
  initialTimeRange,
  parseInstantParam,
  pauseRange,
  rangeWindow,
  resumeRange,
  timeRangeFromSearch,
  withSpan,
  type TimeRangeState,
} from './time-range';

const NOW = Date.parse('2026-10-22T12:00:00Z');
const Z = (s: string) => Date.parse(s);
const stateOf = (from: number, to: number) => (absoluteRange(from, to, NOW) as { state: TimeRangeState }).state;

describe('absolute range', () => {
  it('accepts a window and projects it as a frozen rolling range', () => {
    expect(absoluteRange(Z('2026-10-22T02:00:00Z'), Z('2026-10-22T04:00:00Z'), NOW)).toEqual({
      state: {
        spanSeconds: 7200,
        paused: true,
        pausedAt: Z('2026-10-22T04:00:00Z'),
        absolute: { fromMs: Z('2026-10-22T02:00:00Z'), toMs: Z('2026-10-22T04:00:00Z') },
      },
    });
  });

  it.each([
    ['order', '2026-10-22T04:00:00Z', '2026-10-22T02:00:00Z'],
    ['order', '2026-10-22T02:00:00Z', '2026-10-22T02:00:00Z'],
    ['tooShort', '2026-10-22T02:00:00Z', '2026-10-22T02:00:30Z'],
    ['tooLong', '2026-09-01T00:00:00Z', '2026-10-22T00:00:00Z'],
    ['future', '2026-10-22T11:00:00Z', '2026-10-22T13:00:00Z'],
    ['tooEarly', '1999-12-31T00:00:00Z', '2000-01-02T00:00:00Z'],
  ])('refuses %s', (problem, from, to) => {
    expect(absoluteRange(Z(from), Z(to), NOW)).toEqual({ problem });
  });

  it('refuses non-finite ends', () => {
    expect(absoluteRange(Number.NaN, NOW, NOW)).toEqual({ problem: 'unparseable' });
  });

  it('keeps a one-minute edge and a 31-day edge, and tolerates a minute of clock skew', () => {
    expect('state' in absoluteRange(NOW - 60_000, NOW, NOW)).toBe(true);
    expect('state' in absoluteRange(NOW - 31 * 86_400_000, NOW, NOW)).toBe(true);
    expect('state' in absoluteRange(NOW - 3_600_000, NOW + 30_000, NOW)).toBe(true);
  });

  it('parses only zoned RFC 3339 values', () => {
    expect(parseInstantParam('2026-10-22T02:00:00Z')).toBe(Z('2026-10-22T02:00:00Z'));
    expect(parseInstantParam('2026-10-22T04:00:00+02:00')).toBe(Z('2026-10-22T02:00:00Z'));
    expect(parseInstantParam('2026-10-22T02:00Z')).toBe(Z('2026-10-22T02:00:00Z'));
    for (const bad of ['2026-10-22T02:00:00', '2026-10-22', '1792000000', '', '2026-13-45T00:00:00Z', 'yesterday']) {
      expect(parseInstantParam(bad), bad).toBeUndefined();
    }
  });

  it('reads a link: a window wins over range and paused; a bad one is rejected with its values, never thrown', () => {
    expect(timeRangeFromSearch('?range=7d&paused=1&from=2026-10-22T02:00:00Z&to=2026-10-22T04:00:00Z', NOW)).toEqual({
      absolute: { fromMs: Z('2026-10-22T02:00:00Z'), toMs: Z('2026-10-22T04:00:00Z') },
    });
    expect(timeRangeFromSearch('?range=6h&from=2026-10-22T04:00:00Z&to=2026-10-22T02:00:00Z', NOW)).toEqual({
      spanSeconds: 21600,
      rejected: { reason: 'order', from: '2026-10-22T04:00:00Z', to: '2026-10-22T02:00:00Z' },
    });
    expect(timeRangeFromSearch('?from=2026-10-22T02:00:00Z', NOW).rejected?.reason).toBe('incomplete');
    expect(timeRangeFromSearch('?from=2026-10-22T02:00:00&to=2026-10-22T04:00:00', NOW).rejected?.reason).toBe('unparseable');
    expect(timeRangeFromSearch('?from=%E0%A4%A&to=x', NOW).rejected?.reason).toBe('unparseable');
    expect(timeRangeFromSearch('', NOW)).toEqual({});
  });

  it('a rolling-shape reader gets the exact window; a fixed window ignores withSpan and pause', () => {
    const state = stateOf(Z('2026-10-22T02:00:00Z'), Z('2026-10-22T04:00:00Z'));
    expect(rangeWindow(state, NOW + 1e9)).toEqual({
      startTime: '2026-10-22T02:00:00.000Z',
      endTime: '2026-10-22T04:00:00.000Z',
    });
    expect(withSpan(state, 900)).toBe(state);
    expect(pauseRange(state, NOW)).toBe(state);
    expect(resumeRange(state)).toEqual({ spanSeconds: 7200, paused: false });
    expect(absoluteToParams(state.absolute!)).toEqual({
      from: '2026-10-22T02:00:00.000Z',
      to: '2026-10-22T04:00:00.000Z',
    });
  });

  it('a window longer than the longest preset resumes at that preset', () => {
    expect(resumeRange(stateOf(NOW - 20 * 86_400_000, NOW))).toEqual({ spanSeconds: 7 * 86400, paused: false });
  });

  it('initialTimeRange prefers a valid window over range/paused, and falls back when it is not valid', () => {
    const w = { fromMs: Z('2026-10-22T02:00:00Z'), toMs: Z('2026-10-22T04:00:00Z') };
    expect(initialTimeRange(undefined, { absolute: w, spanSeconds: 900 }, NOW).absolute).toEqual(w);
    expect(initialTimeRange(undefined, { absolute: { fromMs: w.toMs, toMs: w.fromMs }, spanSeconds: 900 }, NOW)).toEqual({
      spanSeconds: 900,
      paused: false,
    });
  });
});
