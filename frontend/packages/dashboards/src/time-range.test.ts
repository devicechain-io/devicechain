// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The board's time range, pure: the window each preset asks for, the bucket width that keeps a
// long span's seed read bounded, pause and resume, the deep-link parsers and the authoring helper.

import { describe, expect, it } from 'vitest';

import { parseDashboardDefinition } from './definition';
import {
  DEFAULT_TIME_RANGE_SECONDS,
  MAX_SEED_BUCKETS,
  TIME_RANGE_PRESETS,
  bucketSecondsFor,
  initialTimeRange,
  isValidSpanSeconds,
  parsePausedParam,
  parseRangeParam,
  pauseRange,
  presetIdOf,
  rangeEnd,
  rangeWindow,
  resumeRange,
  seedBucketCount,
  setBoardTimeRange,
  spanLabel,
  timeRangeFromSearch,
  withSpan,
} from './time-range';

const NOW = Date.parse('2026-10-14T12:00:00.000Z');

describe('rangeWindow: from = end - span, to = end', () => {
  const expected: Record<string, string> = {
    '15m': '2026-10-14T11:45:00.000Z',
    '1h': '2026-10-14T11:00:00.000Z',
    '6h': '2026-10-14T06:00:00.000Z',
    '24h': '2026-10-13T12:00:00.000Z',
    '7d': '2026-10-07T12:00:00.000Z',
  };

  it.each(TIME_RANGE_PRESETS.map((p) => [p.id, p.seconds] as const))('%s', (id, seconds) => {
    const w = rangeWindow({ spanSeconds: seconds, paused: false }, NOW);
    expect(w.endTime).toBe('2026-10-14T12:00:00.000Z');
    expect(w.startTime).toBe(expected[id]);
  });

  it('ends at the pause instant while frozen, not at the wall clock', () => {
    const pausedAt = NOW - 90_000;
    const w = rangeWindow({ spanSeconds: 3600, paused: true, pausedAt }, NOW + 3_600_000);
    expect(w.endTime).toBe(new Date(pausedAt).toISOString());
    expect(Date.parse(w.endTime) - Date.parse(w.startTime)).toBe(3_600_000);
  });

  it('falls back to now for a paused state that carries no instant (cannot be built, but must not NaN)', () => {
    expect(rangeEnd({ spanSeconds: 60, paused: true }, NOW)).toBe(NOW);
  });
});

describe('bucketSecondsFor: a seed read stays bounded whatever the span', () => {
  it.each([
    ['15m', 60],
    ['1h', 60],
    ['6h', 120],
    ['24h', 360],
    ['7d', 2520],
  ])('%s buckets at %d s', (id, bucket) => {
    const span = TIME_RANGE_PRESETS.find((p) => p.id === id)!.seconds;
    expect(bucketSecondsFor(span)).toBe(bucket);
  });

  it('never asks for more than MAX_SEED_BUCKETS per measurement, at any authorable span', () => {
    for (let span = 60; span <= 604800; span += 97) {
      expect(seedBucketCount(span), `span ${span}`).toBeLessThanOrEqual(MAX_SEED_BUCKETS);
    }
  });

  it('keeps 7 days far inside the backend bucket cap (10 000), where per-minute buckets would not be', () => {
    expect(seedBucketCount(7 * 86400)).toBe(MAX_SEED_BUCKETS);
    expect((7 * 86400) / 60).toBeGreaterThan(10_000); // the unbounded alternative the cap exists for
  });

  it('is always a whole number of minutes, so the read is served from the one-minute summaries', () => {
    for (let span = 60; span <= 604800; span += 211) expect(bucketSecondsFor(span) % 60, `span ${span}`).toBe(0);
  });
});

describe('pause and resume', () => {
  it('stamps the instant on pause and drops it on resume, keeping the span', () => {
    const live = initialTimeRange(undefined, {}, NOW);
    expect(live).toEqual({ spanSeconds: DEFAULT_TIME_RANGE_SECONDS, paused: false });
    const frozen = pauseRange({ ...live, spanSeconds: 21600 }, NOW);
    expect(frozen).toEqual({ spanSeconds: 21600, paused: true, pausedAt: NOW });
    expect(resumeRange(frozen)).toEqual({ spanSeconds: 21600, paused: false });
  });

  it('a second pause does not move the freeze instant forward, and resuming live is a no-op', () => {
    const frozen = pauseRange({ spanSeconds: 3600, paused: false }, NOW);
    expect(pauseRange(frozen, NOW + 60_000)).toBe(frozen);
    const live = { spanSeconds: 3600, paused: false };
    expect(resumeRange(live)).toBe(live);
  });

  it('changing the span keeps the pause and its instant', () => {
    const frozen = pauseRange({ spanSeconds: 3600, paused: false }, NOW);
    expect(withSpan(frozen, 86400)).toEqual({ spanSeconds: 86400, paused: true, pausedAt: NOW });
  });

  it('refuses a span that is not usable', () => {
    const live = { spanSeconds: 3600, paused: false };
    for (const bad of [0, 59, 604801, 1.5, Number.NaN, -3600]) expect(withSpan(live, bad)).toBe(live);
  });
});

describe('initialTimeRange', () => {
  it('prefers the link over the board default, and the board default over the hour', () => {
    expect(initialTimeRange(21600, { spanSeconds: 900 }, NOW).spanSeconds).toBe(900);
    expect(initialTimeRange(21600, {}, NOW).spanSeconds).toBe(21600);
    expect(initialTimeRange(undefined, {}, NOW).spanSeconds).toBe(3600);
  });

  it('ignores a board default that is not a usable span', () => {
    for (const bad of [0, 30, 10_000_000, 90.5, Number.NaN]) {
      expect(initialTimeRange(bad, {}, NOW).spanSeconds, String(bad)).toBe(3600);
    }
  });

  it('opens frozen at the load instant when the link says paused', () => {
    expect(initialTimeRange(undefined, { paused: true }, NOW)).toEqual({ spanSeconds: 3600, paused: true, pausedAt: NOW });
  });
});

describe('deep-link parsing is fail-closed', () => {
  it('reads each preset id, exactly', () => {
    for (const p of TIME_RANGE_PRESETS) expect(parseRangeParam(p.id)).toBe(p.seconds);
  });

  it('ignores everything else, so the board default is used', () => {
    for (const bad of [null, undefined, '', '1H', '2h', '3600', '1h ', ' 1h', '30d', '1h;drop', '__proto__', 'NaN', '-1h']) {
      expect(parseRangeParam(bad as string | null | undefined), JSON.stringify(bad)).toBeUndefined();
    }
  });

  it('pauses only for the literal 1', () => {
    expect(parsePausedParam('1')).toBe(true);
    for (const other of [null, undefined, '', '0', 'true', 'yes', '11', ' 1']) {
      expect(parsePausedParam(other as string | null | undefined), JSON.stringify(other)).toBe(false);
    }
  });

  it('reads both from a query string and never throws on a malformed one', () => {
    expect(timeRangeFromSearch('?range=6h&paused=1')).toEqual({ spanSeconds: 21600, paused: true });
    expect(timeRangeFromSearch('?range=bogus&paused=maybe')).toEqual({});
    expect(() => timeRangeFromSearch('?range=%E0%A4%A&paused=%')).not.toThrow();
    expect(timeRangeFromSearch('')).toEqual({});
  });
});

describe('labels', () => {
  it('names a preset and a custom span exactly', () => {
    expect(presetIdOf(3600)).toBe('1h');
    expect(presetIdOf(7200)).toBeUndefined();
    expect(spanLabel(900)).toBe('15m');
    expect(spanLabel(7200)).toBe('2h');
    expect(spanLabel(172800)).toBe('2d');
    expect(spanLabel(90)).toBe('90s');
  });
});

describe('the board default as authored', () => {
  const doc = (timeRange: unknown) => ({ schemaVersion: 3, title: 't', canvas: {}, widgets: [], timeRange });

  it('is read from the definition', () => {
    const def = parseDashboardDefinition(doc({ defaultSeconds: 21600 }));
    expect(def.timeRange).toEqual({ defaultSeconds: 21600 });
  });

  it('is ignored, not clamped, when it is not a usable span', () => {
    for (const bad of [0, 59, 604801, 3600.5, '1h', null, {}]) {
      expect(parseDashboardDefinition(doc({ defaultSeconds: bad })).timeRange, JSON.stringify(bad)).toBeUndefined();
    }
    expect(parseDashboardDefinition(doc('1h')).timeRange).toBeUndefined();
  });

  it('is stored as no range at all when it is the default hour, so an untouched board stays byte-identical', () => {
    const def = parseDashboardDefinition(doc(undefined));
    expect(setBoardTimeRange(def, 3600).timeRange).toBeUndefined();
    expect(setBoardTimeRange(def, 21600).timeRange).toEqual({ defaultSeconds: 21600 });
    expect(setBoardTimeRange(setBoardTimeRange(def, 21600), 3600).timeRange).toBeUndefined();
    expect(setBoardTimeRange(def, 5).timeRange).toBeUndefined();
  });

  it('isValidSpanSeconds bounds a minute to a week', () => {
    expect(isValidSpanSeconds(60)).toBe(true);
    expect(isValidSpanSeconds(604800)).toBe(true);
    expect(isValidSpanSeconds(59)).toBe(false);
    expect(isValidSpanSeconds(604801)).toBe(false);
  });
});
