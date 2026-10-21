// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet state, decided apart from the drawing: which reading a cell shows and why it can be
// empty, how rows are ordered and paged, how the seed window is cut.

import type { FleetMember, MeasurementSample, MemberRoster, MemberSeed } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  buildFleet,
  cellFor,
  clampPage,
  columnNames,
  deviceBars,
  newestMemberTime,
  pageCount,
  seedWindow,
  sortMembers,
  type FleetInputs,
} from './fleet-model';

const T0 = Date.parse('2026-10-14T12:00:00Z');
const iso = (ms: number) => new Date(ms).toISOString();

const s = (deviceToken: string, name: string, value: number | null, at = iso(T0 - 5_000)): MeasurementSample => ({
  id: `${deviceToken}-${name}-${at}`,
  deviceToken,
  eventType: 0,
  occurredTime: at,
  name,
  value,
  classifier: null,
});

const roster = (...tokens: string[]): MemberRoster => ({
  members: tokens.map((token) => ({ token, name: `Name ${token}` })),
  total: tokens.length,
  unbound: false,
});

const seedOf = (token: string, readings: Record<string, number>, at = iso(T0 - 60_000)): MemberSeed => ({
  latest: Object.fromEntries(Object.entries(readings).map(([n, v]) => [n, s(token, n, v, at)])),
  observed: Object.keys(readings),
});

function inputs(over: Partial<FleetInputs> = {}): FleetInputs {
  return { roster: roster('a', 'b'), seeds: new Map(), live: new Map(), since: T0 - 3600_000, forbidden: false, ...over };
}

const live = (...samples: MeasurementSample[]) => {
  const out = new Map<string, Map<string, MeasurementSample>>();
  for (const x of samples) {
    if (!out.has(x.deviceToken)) out.set(x.deviceToken, new Map());
    out.get(x.deviceToken)!.set(x.name, x);
  }
  return out;
};

describe('buildFleet: a value belongs to its device', () => {
  it('keeps two devices that report the same measurement name as two values, never one', () => {
    const fleet = buildFleet(inputs({ live: live(s('a', 'temperature', 20), s('b', 'temperature', 90)) }));
    const [a, b] = fleet.members;
    expect(a.latest.temperature.value).toBe(20);
    expect(b.latest.temperature.value).toBe(90);
    expect(cellFor(a, 'temperature')).toMatchObject({ kind: 'value', value: 20 });
    expect(cellFor(b, 'temperature')).toMatchObject({ kind: 'value', value: 90 });
  });

  it('does the same for seeds', () => {
    const seeds = new Map<string, MemberSeed>([
      ['a', seedOf('a', { temperature: 21 })],
      ['b', seedOf('b', { temperature: 91 })],
    ]);
    const [a, b] = buildFleet(inputs({ seeds })).members;
    expect([a.latest.temperature.value, b.latest.temperature.value]).toEqual([21, 91]);
  });

  it('takes the newer of a seed reading and a live one, either way round', () => {
    const seeds = new Map([['a', seedOf('a', { temperature: 10 }, iso(T0 - 120_000))]]);
    let fleet = buildFleet(inputs({ seeds, live: live(s('a', 'temperature', 30, iso(T0 - 5_000))) }));
    expect(fleet.members[0].latest.temperature.value).toBe(30);
    // A live sample that is OLDER than the seed (out-of-order delivery) must not roll the cell back.
    fleet = buildFleet(inputs({ seeds, live: live(s('a', 'temperature', 5, iso(T0 - 600_000))) }));
    expect(fleet.members[0].latest.temperature.value).toBe(10);
  });

  it('drops a reading older than the span: it is not in range', () => {
    const seeds = new Map([['a', seedOf('a', { temperature: 10 }, iso(T0 - 7200_000))]]);
    const fleet = buildFleet(inputs({ seeds, since: T0 - 3600_000 }));
    expect(fleet.members[0].latest.temperature).toBeUndefined();
    expect(cellFor(fleet.members[0], 'temperature').kind).toBe('no-data');
  });

  it('keeps a seeded reading stamped at the start of the span’s first minute while that minute is in range', () => {
    // An hour's board at 12:00:30. The device last reported at 11:00:45, so the seed read stamps it
    // at the start of its minute, 11:00:00 -- half a minute before the span's exact start. The
    // reading is 59 m 45 s old and inside the hour.
    const now = T0 + 30_000;
    const seeds = new Map([['a', seedOf('a', { temperature: 10 }, iso(T0 - 3600_000))]]);
    const fleet = buildFleet(inputs({ seeds, since: now - 3600_000 }));
    expect(fleet.members[0].latest.temperature?.value).toBe(10);
    // Its minute out of range, it goes: the slack is the stamp's minute, and no more.
    const later = buildFleet(inputs({ seeds, since: now + 60_000 - 3600_000 }));
    expect(later.members[0].latest.temperature).toBeUndefined();
    // A live reading carries its own time, and is held to the exact instant.
    const exact = buildFleet(inputs({ live: live(s('a', 'temperature', 7, iso(T0 - 3600_000))), since: now - 3600_000 }));
    expect(exact.members[0].latest.temperature).toBeUndefined();
  });

  it('shows the same reading under a longer span (the range is honoured both ways)', () => {
    const seeds = new Map([['a', seedOf('a', { temperature: 10 }, iso(T0 - 7200_000))]]);
    const fleet = buildFleet(inputs({ seeds, since: T0 - 86400_000 }));
    expect(fleet.members[0].latest.temperature.value).toBe(10);
  });
});

describe('buildFleet: truncation and state', () => {
  it('flags a fleet larger than the members it holds', () => {
    const r: MemberRoster = { members: roster('a', 'b').members, total: 812, unbound: false };
    const fleet = buildFleet(inputs({ roster: r }));
    expect(fleet.truncated).toBe(true);
    expect(fleet.total).toBe(812);
    expect(fleet.members).toHaveLength(2);
  });

  it('does not flag a fleet that fits', () => {
    expect(buildFleet(inputs()).truncated).toBe(false);
  });

  it('is loading before the roster, unbound for an unbound slot, forbidden when refused', () => {
    expect(buildFleet(inputs({ roster: null })).status).toBe('loading');
    expect(buildFleet(inputs({ roster: { members: [], total: 0, unbound: true } })).status).toBe('unbound');
    expect(buildFleet(inputs({ forbidden: true })).status).toBe('forbidden');
    expect(buildFleet(inputs()).status).toBe('ready');
  });

  it('marks each member pending until its own seed settles, then ready or failed', () => {
    const seeds = new Map<string, MemberSeed | 'failed'>([['a', seedOf('a', {})]]);
    const [a, b] = buildFleet(inputs({ seeds })).members;
    expect([a.seed, b.seed]).toEqual(['ready', 'pending']);
    const failed = buildFleet(inputs({ seeds: new Map([['a', 'failed' as const]]) })).members[0];
    expect(failed.seed).toBe('failed');
  });
});

describe('cellFor: why a cell is empty', () => {
  const member = (over: Partial<FleetMember>): FleetMember => ({
    token: 'a',
    name: null,
    latest: {},
    seed: 'ready',
    observed: [],
    ...over,
  });

  it('is loading while the seed is pending', () => {
    expect(cellFor(member({ seed: 'pending' }), 'temperature').kind).toBe('loading');
  });

  it('is failed when the seed read failed and no live reading has arrived', () => {
    expect(cellFor(member({ seed: 'failed' }), 'temperature').kind).toBe('failed');
  });

  it('is "no data in range" for a device that reported nothing at all', () => {
    expect(cellFor(member({}), 'temperature').kind).toBe('no-data');
  });

  it('is "does not report this" for a device whose profile does not declare the measurement', () => {
    expect(cellFor(member({ observed: ['humidity'], declared: ['humidity'] }), 'temperature').kind).toBe('not-reported');
    expect(cellFor(member({ observed: [], declared: ['humidity'] }), 'temperature').kind).toBe('not-reported');
  });

  it('is "no data" for a declared measurement the device has sent other readings without: a dead probe, a live heartbeat', () => {
    expect(cellFor(member({ observed: ['battery'], declared: ['temperature', 'battery'] }), 'temperature').kind).toBe('no-data');
  });

  it('reads an unknown declaration as declared', () => {
    expect(cellFor(member({ observed: ['humidity'] }), 'temperature').kind).toBe('no-data');
  });

  it('shows a reading the profile does not declare: a reading is a reading', () => {
    const latest = { temperature: s('a', 'temperature', 3) };
    expect(cellFor(member({ latest, declared: ['humidity'] }), 'temperature').kind).toBe('value');
  });

  it('shows a live reading even while the seed is pending or failed', () => {
    const latest = { temperature: s('a', 'temperature', 3) };
    expect(cellFor(member({ seed: 'pending', latest }), 'temperature').kind).toBe('value');
    expect(cellFor(member({ seed: 'failed', latest }), 'temperature').kind).toBe('value');
  });

  it('treats a reading with no number as no reading', () => {
    expect(cellFor(member({ latest: { temperature: s('a', 'temperature', null) } }), 'temperature').kind).toBe('no-data');
  });

  it('treats a non-finite number as no reading', () => {
    expect(cellFor(member({ latest: { temperature: s('a', 'temperature', Number.NaN) } }), 'temperature').kind).toBe('no-data');
  });

  it('says a name the seed left unread is not loaded, never "no data", until a live reading gives it a value', () => {
    const seed: MemberSeed = { latest: { fuel: s('a', 'fuel', 40) }, observed: ['fuel', 'engine'], unread: ['engine'] };
    const fleet = buildFleet(inputs({ seeds: new Map([['a', seed]]) }));
    expect(cellFor(fleet.members[0], 'engine').kind).toBe('unread');
    expect(cellFor(fleet.members[0], 'fuel').kind).toBe('value');
    const heard = buildFleet(inputs({ seeds: new Map([['a', seed]]), live: live(s('a', 'engine', 61)) }));
    expect(cellFor(heard.members[0], 'engine')).toMatchObject({ kind: 'value', value: 61 });
  });
});

describe('columnNames', () => {
  const m = (observed: string[]): FleetMember => ({ token: 'x', name: null, latest: {}, seed: 'ready', observed });

  it('uses the measurements the datasource names, in that order, once each', () => {
    expect(columnNames(['b', 'a', 'b'], [m(['z'])])).toEqual(['b', 'a']);
  });

  it('with none named, uses every measurement any member reported, alphabetically', () => {
    expect(columnNames([], [m(['t', 'h']), m(['b', 't'])])).toEqual(['b', 'h', 't']);
  });
});

describe('sortMembers', () => {
  const members = buildFleet(
    inputs({
      roster: {
        members: [
          { token: 'a', name: 'Charlie' },
          { token: 'b', name: 'alpha' },
          { token: 'c', name: 'Bravo' },
          { token: 'd', name: 'Delta' },
        ],
        total: 4,
        unbound: false,
      },
      live: live(s('a', 'temperature', 30), s('b', 'temperature', 10), s('c', 'temperature', 20)),
      seeds: new Map([['d', seedOf('d', {})]]),
    }),
  ).members;
  const order = (key: Parameters<typeof sortMembers>[1]) => sortMembers(members, key).map((x) => x.token);

  it('orders by device name, ignoring case', () => {
    expect(order({ by: null, descending: false })).toEqual(['b', 'c', 'a', 'd']);
    expect(order({ by: null, descending: true })).toEqual(['d', 'a', 'c', 'b']);
  });

  it('orders by a measurement’s value', () => {
    expect(order({ by: 'temperature', descending: false }).slice(0, 3)).toEqual(['b', 'c', 'a']);
    expect(order({ by: 'temperature', descending: true }).slice(0, 3)).toEqual(['a', 'c', 'b']);
  });

  it('puts a device with no value LAST in either direction', () => {
    expect(order({ by: 'temperature', descending: false })[3]).toBe('d');
    expect(order({ by: 'temperature', descending: true })[3]).toBe('d');
  });

  it('breaks ties by device name, so the order never depends on arrival order', () => {
    const tied = buildFleet(inputs({ live: live(s('a', 'temperature', 5), s('b', 'temperature', 5)) })).members;
    expect(sortMembers(tied, { by: 'temperature', descending: false }).map((x) => x.token)).toEqual(['a', 'b']);
    expect(sortMembers([...tied].reverse(), { by: 'temperature', descending: false }).map((x) => x.token)).toEqual(['a', 'b']);
  });

  it('sorts numbers as numbers, not as text', () => {
    const nums = buildFleet(inputs({ live: live(s('a', 'temperature', 9), s('b', 'temperature', 10)) })).members;
    expect(sortMembers(nums, { by: 'temperature', descending: false }).map((x) => x.token)).toEqual(['a', 'b']);
  });

  it('does not mutate its input', () => {
    const before = members.map((x) => x.token);
    sortMembers(members, { by: null, descending: true });
    expect(members.map((x) => x.token)).toEqual(before);
  });
});

describe('paging', () => {
  it('counts pages, at least one', () => {
    expect(pageCount(0, 20)).toBe(1);
    expect(pageCount(20, 20)).toBe(1);
    expect(pageCount(21, 20)).toBe(2);
    expect(pageCount(500, 20)).toBe(25);
  });

  it('holds a page inside what exists', () => {
    expect(clampPage(9, 21, 20)).toBe(1);
    expect(clampPage(-3, 21, 20)).toBe(0);
    expect(clampPage(1, 0, 20)).toBe(0);
  });
});

describe('seedWindow', () => {
  it('is whole minutes ending just before the last minute boundary', () => {
    const at = Date.parse('2026-10-14T12:00:37.500Z');
    const w = seedWindow(3600, at);
    expect(w.startTime).toBe('2026-10-14T11:00:00.000Z');
    expect(w.endTime).toBe('2026-10-14T11:59:59.999Z');
  });

  it('reads in a whole number of minutes per bucket, so the store answers from its summaries', () => {
    for (const span of [900, 3600, 21600, 86400, 604800]) {
      const w = seedWindow(span, T0);
      expect(w.intervalSeconds % 60, `span ${span}`).toBe(0);
      expect(w.intervalSeconds).toBeGreaterThanOrEqual(60);
    }
  });

  it('is the same window for two widgets reading in the same minute (so the hub can share the reads)', () => {
    expect(seedWindow(3600, T0 + 1)).toEqual(seedWindow(3600, T0 + 40_000));
  });

  it('moves with the freeze instant while paused', () => {
    expect(seedWindow(3600, T0 - 7200_000)).not.toEqual(seedWindow(3600, T0));
  });
});

describe('deviceBars', () => {
  const members = buildFleet(
    inputs({
      roster: roster('a', 'b', 'c', 'd'),
      live: live(s('a', 'temperature', 30), s('b', 'temperature', 10), s('c', 'temperature', 20)),
      seeds: new Map([['d', seedOf('d', {})]]),
    }),
  ).members;
  const labels = (sort: 'none' | 'ascending' | 'descending') => deviceBars(members, 'temperature', sort).map((b) => b.token);

  it('is one bar per member in the order the roster gave', () => {
    expect(labels('none')).toEqual(['a', 'b', 'c', 'd']);
  });

  it('keeps the device’s own value: two devices with one measurement name are two bars', () => {
    const bars = deviceBars(members, 'temperature', 'none');
    expect(bars.map((b) => b.value)).toEqual([30, 10, 20, null]);
    expect(bars[0].label).toBe('Name a');
  });

  it('sorts, with a device that has no value last', () => {
    expect(labels('ascending')).toEqual(['b', 'c', 'a', 'd']);
    expect(labels('descending')).toEqual(['a', 'c', 'b', 'd']);
  });
});

describe('newestMemberTime', () => {
  it('is the newest reading anywhere in the fleet', () => {
    const fleet = buildFleet(inputs({ live: live(s('a', 'temperature', 1, iso(T0 - 30_000)), s('b', 'humidity', 2, iso(T0 - 4_000))) }));
    expect(newestMemberTime(fleet)).toBe(T0 - 4_000);
  });

  it('is undefined for an empty fleet', () => {
    expect(newestMemberTime(buildFleet(inputs()))).toBeUndefined();
  });
});
