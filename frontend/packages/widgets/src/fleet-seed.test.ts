// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet seed plan: what a fleet widget asks the event store for each device, and the seed it
// makes of the answer. These hold the plan to its cost (how many reads, over what, with which name)
// as well as to its answer, because a plan that answers correctly by reading a whole span of every
// measurement for every device is the query storm it exists to avoid.

import type { MeasurementBucket, MemberBucketCriteria } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { foldMemberSeed, MAX_NARROW_READS, memberSeedPlan, mergeSeeds, TAIL_SECONDS, tailWindow } from './fleet-seed';
import { seedWindow } from './fleet-model';

const b = (name: string, bucketStart: string, avg: number | null, count: number | undefined = 5): MeasurementBucket => ({
  bucketStart,
  name,
  avg,
  count,
});

describe('foldMemberSeed', () => {
  it('keeps the NEWEST bucket per measurement, whatever order the store returned them in', () => {
    const seed = foldMemberSeed('d-1', [
      b('t', '2026-10-14T11:02:00Z', 22),
      b('t', '2026-10-14T11:03:00Z', 23),
      b('t', '2026-10-14T11:01:00Z', 21),
    ]);
    expect(seed.latest.t.value).toBe(23);
    expect(seed.latest.t.occurredTime).toBe('2026-10-14T11:03:00Z');
    expect(seed.latest.t.deviceToken).toBe('d-1');
  });

  it('keeps each measurement apart, and records every name the device reported as observed', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T11:00:00Z', 20), b('h', '2026-10-14T11:00:00Z', 55)]);
    expect(Object.keys(seed.latest).sort()).toEqual(['h', 't']);
    expect(seed.observed.sort()).toEqual(['h', 't']);
  });

  it('skips a bucket with no mean when choosing the latest, yet still counts its name as observed', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T11:00:00Z', 20), b('t', '2026-10-14T11:05:00Z', null, 3)]);
    expect(seed.latest.t.value).toBe(20);
    expect(seed.observed).toEqual(['t']);
  });

  it('does not count an empty bucket as the device reporting that measurement', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T11:00:00Z', null, 0)]);
    expect(seed.observed).toEqual([]);
    expect(seed.latest).toEqual({});
  });

  it('is empty for a device with no buckets at all', () => {
    expect(foldMemberSeed('d-1', [])).toEqual({ latest: {}, observed: [] });
  });

  // The store's buckets sit on fixed instants, so the first one of a span can begin before the span
  // does. Its reading happened inside the span, so the stamp moves up to the span's start: still no
  // later than the reading (an age is overstated, never understated), and no longer out of range.
  it('stamps a bucket that began before the window at the window start, never earlier and never later', () => {
    const seed = foldMemberSeed('d-1', [b('t', '2026-10-14T10:42:00Z', 20)], '2026-10-14T11:00:00.000Z');
    expect(seed.latest.t.occurredTime).toBe('2026-10-14T11:00:00.000Z');
    const inside = foldMemberSeed('d-1', [b('t', '2026-10-14T11:07:00Z', 20)], '2026-10-14T11:00:00.000Z');
    expect(inside.latest.t.occurredTime).toBe('2026-10-14T11:07:00Z');
  });
});

describe('mergeSeeds', () => {
  it('lets the newer read win per measurement, keeps what only the older one has, and unions the observed names', () => {
    const newer = foldMemberSeed('d', [b('t', '2026-10-14T11:58:00Z', 30)]);
    const older = foldMemberSeed('d', [b('t', '2026-10-14T11:00:00Z', 10), b('h', '2026-10-14T11:00:00Z', 50), b('p', '2026-10-14T11:00:00Z', null, 2)]);
    const merged = mergeSeeds(newer, older);
    expect(merged.latest.t.value).toBe(30);
    expect(merged.latest.h.value).toBe(50);
    expect(merged.observed.sort()).toEqual(['h', 'p', 't']);
  });
});

// ---- The plan ----------------------------------------------------------------------------

const NOW = Date.parse('2026-10-14T12:00:30Z');
const HOUR = seedWindow(3600, NOW);
const iso = (ms: number) => new Date(ms).toISOString();
const TAIL_START = Date.parse('2026-10-14T12:00:00Z') - TAIL_SECONDS * 1000;

// A store that answers per device from a list of (name, minute-bucket start, mean), honouring the
// window and the name filter of each read, and records every read it was asked.
function store(readings: Array<[string, string, number]>) {
  const asked: MemberBucketCriteria[] = [];
  const read = async (c: MemberBucketCriteria): Promise<MeasurementBucket[]> => {
    asked.push(c);
    const lo = Date.parse(c.startTime);
    const hi = Date.parse(c.endTime);
    return readings
      .filter(([name, at]) => (c.name === undefined || c.name === name) && Date.parse(at) >= lo && Date.parse(at) <= hi)
      .map(([name, at, avg]) => b(name, at, avg));
  };
  return { asked, read };
}

describe('tailWindow', () => {
  it('is the last minutes of the window, on whole minutes, in one-minute buckets', () => {
    expect(tailWindow(HOUR)).toEqual({ startTime: iso(TAIL_START), endTime: HOUR.endTime, intervalSeconds: 60 });
  });

  it('is nothing for a window no longer than the tail: the window itself is then the cheap read', () => {
    expect(tailWindow(seedWindow(TAIL_SECONDS, NOW))).toBeUndefined();
    expect(tailWindow(seedWindow(60, NOW))).toBeUndefined();
  });
});

describe('memberSeedPlan', () => {
  it('reads a reporting device ONCE, over the last minutes only, in minute buckets and with no name filter', async () => {
    const s = store([
      ['temperature', '2026-10-14T11:58:00Z', 21],
      ['humidity', '2026-10-14T11:59:00Z', 40],
    ]);
    const seed = await memberSeedPlan(HOUR, ['temperature', 'humidity']).seed('d', s.read);
    expect(s.asked).toEqual([{ startTime: iso(TAIL_START), endTime: HOUR.endTime, intervalSeconds: 60 }]);
    expect(seed.latest.temperature.value).toBe(21);
    expect(seed.latest.humidity.value).toBe(40);
  });

  it('looks back over the span only for a column the tail left empty, narrowed to that name', async () => {
    const s = store([
      ['temperature', '2026-10-14T11:58:00Z', 21],
      ['humidity', '2026-10-14T11:20:00Z', 44],
    ]);
    const seed = await memberSeedPlan(HOUR, ['temperature', 'humidity']).seed('d', s.read);
    expect(s.asked).toHaveLength(2);
    expect(s.asked[1]).toEqual({ ...HOUR, name: 'humidity' });
    expect(seed.latest.humidity.value).toBe(44);
    expect(seed.latest.temperature.value).toBe(21);
  });

  it('tells "does not report this" apart after a narrowed read: the device was seen, the name never was', async () => {
    const s = store([['temperature', '2026-10-14T11:58:00Z', 21]]);
    const seed = await memberSeedPlan(HOUR, ['temperature', 'pressure']).seed('d', s.read);
    expect(s.asked[1]).toEqual({ ...HOUR, name: 'pressure' });
    expect(seed.latest.pressure).toBeUndefined();
    expect(seed.observed).toEqual(['temperature']);
  });

  it('reads every measurement over the span for a device quiet in the tail (its other names are what "not reported" needs)', async () => {
    const s = store([
      ['temperature', '2026-10-14T11:10:00Z', 19],
      ['vibration', '2026-10-14T11:12:00Z', 3],
    ]);
    const seed = await memberSeedPlan(HOUR, ['temperature', 'humidity']).seed('d', s.read);
    expect(s.asked).toHaveLength(2);
    expect(s.asked[1]).toEqual(HOUR);
    expect(seed.latest.temperature.value).toBe(19);
    expect(seed.observed.sort()).toEqual(['temperature', 'vibration']);
  });

  it('reads every measurement once rather than more than MAX_NARROW_READS narrowed reads', async () => {
    const names = Array.from({ length: MAX_NARROW_READS + 2 }, (_, i) => `m${i}`);
    const s = store([['temperature', '2026-10-14T11:58:00Z', 21]]);
    await memberSeedPlan(HOUR, ['temperature', ...names]).seed('d', s.read);
    expect(s.asked).toHaveLength(2);
    expect(s.asked[1]).toEqual(HOUR);
  });

  it('makes exactly MAX_NARROW_READS narrowed reads when that many columns are missing', async () => {
    const names = Array.from({ length: MAX_NARROW_READS }, (_, i) => `m${i}`);
    const s = store([['temperature', '2026-10-14T11:58:00Z', 21]]);
    await memberSeedPlan(HOUR, ['temperature', ...names]).seed('d', s.read);
    expect(s.asked.slice(1).map((c) => c.name)).toEqual(names);
  });

  it('reads the whole span once for a table that names no columns: only that read can list them', async () => {
    const s = store([['temperature', '2026-10-14T11:58:00Z', 21]]);
    await memberSeedPlan(HOUR, []).seed('d', s.read);
    expect(s.asked).toEqual([HOUR]);
  });

  it('reads a window no longer than the tail once, as it is', async () => {
    const short = seedWindow(TAIL_SECONDS, NOW);
    const s = store([]);
    await memberSeedPlan(short, ['temperature']).seed('d', s.read);
    expect(s.asked).toEqual([short]);
  });

  it('keeps a tail reading over an older span bucket for the same name', async () => {
    const s = store([
      ['temperature', '2026-10-14T11:58:00Z', 21],
      ['humidity', '2026-10-14T11:20:00Z', 44],
    ]);
    // Asked of a 7-day span, the span read's buckets are wide; the tail's minute is what is shown.
    const week = seedWindow(7 * 86400, NOW);
    const seed = await memberSeedPlan(week, ['temperature', 'humidity']).seed('d', s.read);
    expect(seed.latest.temperature.occurredTime).toBe('2026-10-14T11:58:00Z');
  });
});

// ---- A span bucket's average is not a latest reading -------------------------------------------

// A store that AVERAGES each read's readings into buckets of the read's own width (aligned to the
// epoch, as the store's buckets are), so a wide span bucket answers with a mean like the real one.
function bucketing(readings: Array<[string, string, number]>) {
  const asked: MemberBucketCriteria[] = [];
  const read = async (c: MemberBucketCriteria): Promise<MeasurementBucket[]> => {
    asked.push(c);
    const lo = Date.parse(c.startTime);
    const hi = Date.parse(c.endTime);
    const width = c.intervalSeconds * 1000;
    const sums = new Map<string, { name: string; start: number; sum: number; n: number }>();
    for (const [name, at, value] of readings) {
      const t = Date.parse(at);
      if ((c.name !== undefined && c.name !== name) || t < lo || t > hi) continue;
      const start = Math.floor(t / width) * width;
      const k = `${name}@${start}`;
      const acc = sums.get(k) ?? { name, start, sum: 0, n: 0 };
      acc.sum += value;
      acc.n += 1;
      sums.set(k, acc);
    }
    return [...sums.values()].map((a) => b(a.name, iso(a.start), a.sum / a.n, a.n));
  };
  return { asked, read };
}

describe('memberSeedPlan on a long span: the latest reading, not a bucket average', () => {
  const WEEK = seedWindow(7 * 86400, NOW);

  it('shows a quiet device’s last reading, not the average of the 42-minute bucket it fell in', async () => {
    // Fuel 14, refuelled to 40 eleven minutes later, inside one 42-minute bucket; then parked.
    const s = bucketing([
      ['fuel', '2026-10-14T09:15:00Z', 14],
      ['fuel', '2026-10-14T09:26:00Z', 40],
    ]);
    expect(WEEK.intervalSeconds).toBe(42 * 60);
    const seed = await memberSeedPlan(WEEK, ['fuel']).seed('d', s.read);
    expect(seed.latest.fuel.value).toBe(40);
    expect(seed.latest.fuel.occurredTime).toBe('2026-10-14T09:26:00.000Z');
    // The re-read is one bucket wide, in minutes, narrowed to the one name.
    const last = s.asked[s.asked.length - 1];
    expect(last).toMatchObject({ intervalSeconds: 60, name: 'fuel' });
    expect(Date.parse(last.endTime) + 1 - Date.parse(last.startTime)).toBe(42 * 60_000);
  });

  it('re-reads names whose newest bucket is the same one together, once', async () => {
    const s = bucketing([
      ['fuel', '2026-10-14T09:15:00Z', 14],
      ['fuel', '2026-10-14T09:26:00Z', 40],
      ['engine', '2026-10-14T09:16:00Z', 80],
      ['engine', '2026-10-14T09:25:00Z', 60],
    ]);
    const seed = await memberSeedPlan(WEEK, ['fuel', 'engine']).seed('d', s.read);
    expect([seed.latest.fuel.value, seed.latest.engine.value]).toEqual([40, 60]);
    // tail, span, and ONE re-read of every name over the shared bucket.
    expect(s.asked).toHaveLength(3);
    expect(s.asked[2].name).toBeUndefined();
  });

  it('shows no value, rather than an average, for a name past the re-read bound', async () => {
    const names = Array.from({ length: MAX_NARROW_READS + 1 }, (_, i) => `m${i}`);
    // Each name's newest reading in a different bucket, newest first.
    const s = bucketing(names.map((n, i): [string, string, number] => [n, iso(Date.parse('2026-10-14T09:00:00Z') - i * 3 * 3600_000), i]));
    const seed = await memberSeedPlan(WEEK, names).seed('d', s.read);
    expect(s.asked).toHaveLength(2 + MAX_NARROW_READS);
    expect(Object.keys(seed.latest).sort()).toEqual(names.slice(0, MAX_NARROW_READS));
    expect(seed.observed.sort()).toEqual(names);
  });
});
