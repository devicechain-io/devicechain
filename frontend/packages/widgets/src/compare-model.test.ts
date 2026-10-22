// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  bucketSamples,
  capMembers,
  composeSeries,
  DEFAULT_MAX_SERIES,
  LIVE_PER_MEMBER,
  MAX_SERIES_LIMIT,
  memberLabels,
  pushLive,
  seriesLimit,
} from './compare-model';

const T0 = Date.parse('2026-10-21T12:00:00Z');
const iso = (offsetMin: number) => new Date(T0 + offsetMin * 60_000).toISOString();
const s = (token: string, offsetMin: number, value: number, name = 'temperature'): MeasurementSample => ({
  id: `${token}-${name}-${offsetMin}`,
  deviceToken: token,
  eventType: 0,
  occurredTime: iso(offsetMin),
  name,
  value,
  classifier: null,
});
const members = (n: number) => Array.from({ length: n }, (_, i) => ({ token: `d${i + 1}`, name: `Pump ${i + 1}` }));

describe('seriesLimit', () => {
  it('is the default when unset or unusable, and never past the chart maximum', () => {
    expect(seriesLimit(undefined)).toBe(DEFAULT_MAX_SERIES);
    expect(seriesLimit(0)).toBe(DEFAULT_MAX_SERIES);
    expect(seriesLimit(-3)).toBe(DEFAULT_MAX_SERIES);
    expect(seriesLimit(2.5)).toBe(DEFAULT_MAX_SERIES);
    expect(seriesLimit(Number.NaN)).toBe(DEFAULT_MAX_SERIES);
    expect(seriesLimit(7)).toBe(7);
    expect(seriesLimit(500)).toBe(MAX_SERIES_LIMIT);
  });
});

describe('capMembers', () => {
  it('draws everything of a small group and calls nothing capped', () => {
    const c = capMembers({ members: members(3), total: 3 }, 5);
    expect(c.shown).toHaveLength(3);
    expect(c.capped).toBe(false);
  });

  it('keeps the roster order and counts a group past the limit against the group', () => {
    const c = capMembers({ members: members(9), total: 9 }, 4);
    expect(c.shown.map((m) => m.token)).toEqual(['d1', 'd2', 'd3', 'd4']);
    expect(c).toMatchObject({ total: 9, capped: true, pagedBySource: false });
  });

  it('knows when the source itself read only a page of the group', () => {
    const c = capMembers({ members: members(5), total: 812 }, 5);
    expect(c).toMatchObject({ total: 812, read: 5, capped: true, pagedBySource: true });
  });
});

describe('memberLabels', () => {
  it('names a member by its name, else its token, and keeps two equal names apart', () => {
    const labels = memberLabels([
      { token: 'a', name: 'Pump' },
      { token: 'b', name: 'Pump' },
      { token: 'c', name: null },
      { token: 'd', name: '  ' },
    ]);
    expect(labels.get('a')).toBe('Pump (a)');
    expect(labels.get('b')).toBe('Pump (b)');
    expect(labels.get('c')).toBe('c');
    expect(labels.get('d')).toBe('d');
  });
});

describe('bucketSamples', () => {
  it('turns the named measurement into one point per bucket with an average, oldest first', () => {
    const out = bucketSamples('d1', 'temperature', [
      { bucketStart: iso(2), name: 'temperature', avg: 22 },
      { bucketStart: iso(0), name: 'temperature', avg: 20 },
      { bucketStart: iso(1), name: 'temperature', avg: null },
      { bucketStart: iso(1), name: 'humidity', avg: 50 },
    ]);
    expect(out.map((p) => p.value)).toEqual([20, 22]);
    expect(out.every((p) => p.deviceToken === 'd1')).toBe(true);
  });
});

describe('pushLive', () => {
  it('drops a repeated id and keeps the newest of a long run', () => {
    const buf: MeasurementSample[] = [];
    pushLive(buf, s('d1', 0, 1));
    pushLive(buf, s('d1', 0, 1));
    expect(buf).toHaveLength(1);
    for (let i = 1; i < LIVE_PER_MEMBER + 10; i++) pushLive(buf, s('d1', i, i));
    expect(buf).toHaveLength(LIVE_PER_MEMBER);
    expect(buf[buf.length - 1].value).toBe(LIVE_PER_MEMBER + 9);
  });
});

describe('composeSeries', () => {
  const shown = members(2);
  const labels = memberLabels(shown);
  const base = { shown, labels, measurement: 'temperature', since: T0 - 60 * 60_000, until: T0 + 60 * 60_000 };

  it('names each point by its member and puts live readings only after the history read ended', () => {
    const history = new Map([['d1', { samples: [s('d1', -10, 20), s('d1', -5, 21)], end: T0 - 1 }]]);
    const live = new Map([['d1', [s('d1', -4, 99), s('d1', 1, 22)]]]);
    const out = composeSeries({ ...base, history, live });
    const d1 = out.samples.filter((p) => p.name === 'Pump 1').map((p) => p.value);
    // The live reading at -4 min falls inside the history window: the history holds it (averaged).
    expect(d1).toEqual([20, 21, 22]);
    expect(out.newest).toBe(T0 + 60_000);
  });

  it('draws a member with no history from its live readings and counts a failed read', () => {
    const history = new Map<string, 'failed'>([['d2', 'failed']]);
    const live = new Map([['d2', [s('d2', -1, 5)]]]);
    const out = composeSeries({ ...base, history, live });
    expect(out.historyFailed).toBe(1);
    expect(out.samples.map((p) => [p.name, p.value])).toEqual([['Pump 2', 5]]);
  });

  it('cuts the view at the freeze instant and at the start of the span', () => {
    const live = new Map([['d1', [s('d1', -120, 1), s('d1', -30, 2), s('d1', 30, 3)]]]);
    const out = composeSeries({ ...base, until: T0, history: new Map(), live });
    expect(out.samples.map((p) => p.value)).toEqual([2]);
  });

  it('ignores a reading of another measurement', () => {
    const live = new Map([['d1', [s('d1', -1, 7, 'humidity')]]]);
    expect(composeSeries({ ...base, history: new Map(), live }).samples).toEqual([]);
  });
});
