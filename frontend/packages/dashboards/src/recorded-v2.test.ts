// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Format version 2 (positions), re-timing, and aggregates computed from the recording.

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import type { AlarmSnapshot, LocationSnapshot } from './hub';
import type { HistoryWindow } from './history';
import type { MeasurementBucket } from './queries';
import { NotInRecordingError, RecordedClock, RecordedDataSource, retimeShiftMs } from './recorded';
import { RecordingFormatError, parseBoardRecording, type BoardRecording } from './recording';
import { settle } from './testing/data-source-contract';
import {
  FIXTURE_DURATION_MS,
  FIXTURE_START_MS,
  SITE,
  fixtureJson,
  fixtureJsonV2,
  fixtureRecording,
  fixtureRecordingV2,
} from './testing/recording-fixture';
import { installNetworkTrap, type NetworkTrap } from './testing/network-trap';
import type { DatasourceSelector, MeasurementSample } from './types';

type Json = Record<string, any>;

const idleTicker = { start: () => () => {} };

let trap: NetworkTrap;
beforeEach(() => {
  trap = installNetworkTrap();
});
afterEach(() => {
  const calls = [...trap.calls];
  trap.restore();
  expect(calls).toEqual([]);
});

function v2Refusal(mutate: (doc: Json) => void): RecordingFormatError {
  const doc = JSON.parse(JSON.stringify(fixtureJsonV2())) as Json;
  mutate(doc);
  try {
    parseBoardRecording(doc);
  } catch (err) {
    expect(err).toBeInstanceOf(RecordingFormatError);
    return err as RecordingFormatError;
  }
  throw new Error('the corrupted recording was accepted');
}

function sourceAt(rec: BoardRecording, cursorMs: number, timeShiftMs?: number) {
  const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker, startMs: cursorMs });
  const source = new RecordedDataSource(rec, clock, { siteAnchors: [SITE], timeShiftMs });
  return { clock, source };
}

const site = (measurements: string[] = []): DatasourceSelector => ({ kind: 'anchor', anchor: SITE, measurements, location: { series: 'latest' } });

describe('formatVersion 2: positions', () => {
  it('parses the position columns and the producer name', () => {
    const rec = fixtureRecordingV2();
    expect(rec.formatVersion).toBe(2);
    expect(rec.channels.locations).toBe(true);
    expect(rec.converter).toEqual({ version: 1, name: 'fixture' });
    expect(rec.locations.t).toEqual([0, 5_000, 10_000, 30_000]);
    expect(rec.locations.hdg).toEqual([90, null, 95.5, 180]);
  });

  it('still reads version 1, as a recording with no positions', () => {
    const rec = fixtureRecording();
    expect(rec.formatVersion).toBe(1);
    expect(rec.channels.locations).toBe(false);
    expect(rec.locations).toEqual({ d: [], t: [], lat: [], lon: [], hdg: [], spd: [] });
  });

  it('refuses positions it cannot honour', () => {
    expect(v2Refusal((d) => (d.channels.locations = false)).path).toBe('$.locations');
    expect(v2Refusal((d) => (d.locations = [])).path).toBe('$.locations');
    expect(v2Refusal((d) => (d.locations.extra = [])).path).toBe('$.locations.extra');
    expect(v2Refusal((d) => (d.locations.lat[1] = 91)).path).toBe('$.locations.lat[1]');
    expect(v2Refusal((d) => (d.locations.lon[0] = -181)).path).toBe('$.locations.lon[0]');
    expect(v2Refusal((d) => (d.locations.hdg[0] = 361)).path).toBe('$.locations.hdg[0]');
    expect(v2Refusal((d) => (d.locations.spd[0] = -1)).path).toBe('$.locations.spd[0]');
    expect(v2Refusal((d) => (d.locations.d[2] = 3)).path).toBe('$.locations.d[2]');
    expect(v2Refusal((d) => d.locations.lat.pop()).path).toBe('$.locations.lat');
    expect(v2Refusal((d) => (d.locations.t[2] = 1_000)).path).toBe('$.locations.t[2]');
    expect(v2Refusal((d) => (d.locations.t[3] = FIXTURE_DURATION_MS + 1)).path).toBe('$.locations.t[3]');
    expect(v2Refusal((d) => (d.channels.commands = true)).path).toBe('$.channels.commands');
  });

  it('keeps version 1 exactly as strict as it was', () => {
    const v1 = fixtureJson();
    v1.converter = { version: 1, name: 'x' };
    expect(() => parseBoardRecording(v1)).toThrow(RecordingFormatError);
  });

  it('places every bound device at its last position at the cursor, and none it has not reached', async () => {
    const { source } = sourceAt(fixtureRecordingV2(), 12_000);
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: site() }, { next: (s) => snaps.push(s) });
    await settle();
    expect(snaps).toHaveLength(1);
    const snap = snaps[0];
    if (snap.kind !== 'positions') throw new Error('expected positions');
    expect(snap.deviceTokens).toEqual(['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01']);
    // The plant was never located: absent, as from device-state.
    expect(snap.locations.map((l) => [l.deviceToken, l.latitude, l.longitude, l.heading, l.speed])).toEqual([
      ['sp-hauler-01', 39.0002, -117.0002, 95.5, 4.2],
      ['sp-hauler-02', 39.0005, -117.0005, null, null],
    ]);
    expect(snap.locations[0].occurredTime).toBe(new Date(FIXTURE_START_MS + 10_000).toISOString());
  });

  it('moves a device on when the cursor passes its next fix, and only then', async () => {
    const { clock, source } = sourceAt(fixtureRecordingV2(), 12_000);
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: site() }, { next: (s) => snaps.push(s) });
    await settle();
    clock.play();
    clock.advance(10_000); // 22 s: nobody moved
    expect(snaps).toHaveLength(1);
    clock.advance(8_000); // 30 s: hauler-01's last fix
    expect(snaps).toHaveLength(2);
    const last = snaps[1];
    if (last.kind !== 'positions') throw new Error('expected positions');
    expect(last.locations.find((l) => l.deviceToken === 'sp-hauler-01')?.latitude).toBe(39.0003);
  });
});

describe('re-timing', () => {
  it('computes the shift that makes the cursor read as now', () => {
    const rec = fixtureRecording();
    const now = Date.UTC(2030, 5, 1, 12, 0, 0);
    expect(FIXTURE_START_MS + 20_000 + retimeShiftMs(rec, 20_000, now)).toBe(now);
  });

  it('moves every instant it hands out by the one shift, so ages and order are those of the run', async () => {
    const rec = fixtureRecordingV2();
    const shift = retimeShiftMs(rec, 45_000, Date.UTC(2030, 5, 1, 12, 0, 0));
    const plain = sourceAt(rec, 45_000).source;
    const moved = sourceAt(rec, 45_000, shift).source;
    expect(moved.now()).toBe(Date.UTC(2030, 5, 1, 12, 0, 0));
    expect(moved.now() - plain.now()).toBe(shift);

    const collect = (s: RecordedDataSource) => {
      const got: MeasurementSample[] = [];
      const alarms: AlarmSnapshot[] = [];
      const places: LocationSnapshot[] = [];
      s.subscribeWidget(site(['fuel_pct']), { next: (x) => got.push(x) });
      s.subscribeAlarms({ datasource: site(), pageSize: 10 }, { next: (x) => alarms.push(x) });
      s.subscribeLocations({ datasource: site() }, { next: (x) => places.push(x) });
      return { got, alarms, places };
    };
    const a = collect(plain);
    const b = collect(moved);
    await settle();

    // Measurements: the same rows, each moved by exactly the shift, so every age is unchanged.
    expect(b.got.map((x) => x.value)).toEqual(a.got.map((x) => x.value));
    a.got.forEach((x, i) => {
      expect(Date.parse(b.got[i].occurredTime!) - Date.parse(x.occurredTime!)).toBe(shift);
      expect(moved.now() - Date.parse(b.got[i].occurredTime!)).toBe(plain.now() - Date.parse(x.occurredTime!));
    });
    // Alarms: raise and clear times move by the shift; the order and states do not change.
    const rows = (s: AlarmSnapshot[]) => s[s.length - 1].alarms;
    expect(rows(b.alarms).map((r) => [r.token, r.state])).toEqual(rows(a.alarms).map((r) => [r.token, r.state]));
    rows(a.alarms).forEach((r, i) => {
      const m = rows(b.alarms)[i];
      for (const k of ['raisedTime', 'clearedTime'] as const) {
        if (r[k] === null) expect(m[k]).toBeNull();
        else expect(Date.parse(m[k]!) - Date.parse(r[k]!)).toBe(shift);
      }
    });
    // Positions: the same fixes, moved the same way.
    const fixes = (s: LocationSnapshot[]) => {
      const last = s[s.length - 1];
      if (last.kind !== 'positions') throw new Error('expected positions');
      return last.locations;
    };
    fixes(a.places).forEach((l, i) => {
      expect(fixes(b.places)[i].latitude).toBe(l.latitude);
      expect(Date.parse(fixes(b.places)[i].occurredTime!) - Date.parse(l.occurredTime!)).toBe(shift);
    });
  });

  it('keeps the recording own alarm strings untouched when there is no shift', async () => {
    const { source } = sourceAt(fixtureRecording(), 45_000);
    const got: AlarmSnapshot[] = [];
    source.subscribeAlarms({ pageSize: 10 }, { next: (x) => got.push(x) });
    await settle();
    expect(got[0].alarms.map((a) => a.raisedTime)).toContain(fixtureJson().alarms.events[3].occ);
  });

  it('refuses a shift that is not a number', () => {
    expect(() => sourceAt(fixtureRecording(), 0, Number.NaN)).toThrow(RangeError);
  });
});

// The oracle: the recording's own rows, folded by hand (no source code involved) into
// buckets `widthMs` wide aligned on the epoch, for one device and measurement, over the
// rows in [fromMs, toMs] (offsets) that the cursor has reached.
function directBuckets(doc: Json, device: string, name: string, fromMs: number, toMs: number, cursorMs: number, widthMs: number, shiftMs = 0) {
  const d = doc.devices.findIndex((x: Json) => x.token === device);
  const n = doc.channels.measurements.indexOf(name);
  const out = new Map<number, number[]>();
  const m = doc.measurements;
  for (let i = 0; i < m.t.length; i++) {
    if (m.d[i] !== d || m.n[i] !== n || m.t[i] < fromMs || m.t[i] > toMs || m.t[i] > cursorMs) continue;
    const at = FIXTURE_START_MS + shiftMs + m.t[i];
    const start = at - (((at % widthMs) + widthMs) % widthMs);
    out.set(start, [...(out.get(start) ?? []), m.v[i]]);
  }
  return [...out.entries()].map(([start, values]) => ({
    bucketStart: new Date(start).toISOString(),
    name,
    count: values.length,
    min: Math.min(...values),
    max: Math.max(...values),
    sum: values.reduce((a, b) => a + b, 0),
  }));
}

const windowOf = (fromMs: number, toMs: number, intervalSeconds: number, shiftMs = 0): HistoryWindow => ({
  startTime: new Date(FIXTURE_START_MS + shiftMs + fromMs).toISOString(),
  endTime: new Date(FIXTURE_START_MS + shiftMs + toMs).toISOString(),
  intervalSeconds,
});

const strip = (b: MeasurementBucket) => ({ bucketStart: b.bucketStart, name: b.name, count: b.count, min: b.min, max: b.max, sum: b.sum });

describe('aggregates from the recording', () => {
  it('equal a direct fold of the recorded rows, bucket by bucket', async () => {
    const doc = fixtureJson();
    const { source } = sourceAt(fixtureRecording(), 45_000);
    for (const [device, name] of [
      ['sp-hauler-01', 'fuel_pct'],
      ['sp-hauler-02', 'fuel_pct'],
      ['sp-hauler-01', 'tyre_pressure_kpa'],
      ['sp-plant-01', 'throughput_tph'],
    ] as const) {
      for (const width of [10, 60]) {
        const got = await source.bucketMeasurements({ kind: 'device', deviceToken: device, measurements: [name] }, windowOf(0, 60_000, width));
        const want = directBuckets(doc, device, name, 0, 60_000, 45_000, width * 1000);
        expect(got.map(strip), `${device} ${name} @${width}s`).toEqual(want);
        for (const b of got) expect(b.avg).toBe(b.sum! / b.count!);
      }
    }
  });

  it('never counts a row the cursor has not reached, whatever window is asked for', async () => {
    const { source } = sourceAt(fixtureRecording(), 25_000);
    const got = await source.bucketMeasurements({ kind: 'device', deviceToken: 'sp-hauler-02', measurements: ['fuel_pct'] }, windowOf(0, 60_000, 3600));
    // Rows at 0, 10, 20 s (95, 94, 93); the ones at 30, 40, 50 s are in the future.
    expect(got.map((b) => [b.count, b.min, b.max, b.sum])).toEqual([[3, 93, 95, 282]]);
  });

  it('honours both edges of the window, inclusive', async () => {
    const { source } = sourceAt(fixtureRecording(), 60_000);
    const got = await source.bucketMeasurements({ kind: 'device', deviceToken: 'sp-hauler-01', measurements: ['fuel_pct'] }, windowOf(10_000, 30_000, 3600));
    expect(got.reduce((n, b) => n + b.count!, 0)).toBe(3); // 10, 20 and 30 s
  });

  it('folds every device of an anchor, one series each', async () => {
    const { source } = sourceAt(fixtureRecording(), 60_000);
    const got = await source.bucketMeasurements({ kind: 'anchor', anchor: SITE, measurements: ['fuel_pct'] }, windowOf(0, 60_000, 3600));
    const doc = fixtureJson();
    const want = [
      ...directBuckets(doc, 'sp-hauler-01', 'fuel_pct', 0, 60_000, 60_000, 3_600_000),
      ...directBuckets(doc, 'sp-hauler-02', 'fuel_pct', 0, 60_000, 60_000, 3_600_000),
    ];
    expect(got.map(strip).sort((a, b) => a.sum! - b.sum!)).toEqual(want.sort((a, b) => a.sum - b.sum));
  });

  it('reads the same rows on a re-timed board, from the window as the board shows it', async () => {
    const doc = fixtureJson();
    const shift = 123_456_789;
    const { source } = sourceAt(fixtureRecording(), 45_000, shift);
    const got = await source.bucketMeasurements({ kind: 'device', deviceToken: 'sp-hauler-01', measurements: ['fuel_pct'] }, windowOf(0, 60_000, 10, shift));
    expect(got.map(strip)).toEqual(directBuckets(doc, 'sp-hauler-01', 'fuel_pct', 0, 60_000, 45_000, 10_000, shift));
    expect(got.reduce((n, b) => n + b.count!, 0)).toBe(5);
  });

  it('refuses a measurement the run never recorded, and answers an unbound slot with nothing', async () => {
    const { source } = sourceAt(fixtureRecording(), 45_000);
    await expect(
      source.bucketMeasurements({ kind: 'device', deviceToken: 'sp-hauler-01', measurements: ['engine_hours'] }, windowOf(0, 60_000, 60)),
    ).rejects.toBeInstanceOf(NotInRecordingError);
    expect(await source.bucketMeasurements({ kind: 'unbound', slot: 's', measurements: ['fuel_pct'] } as DatasourceSelector, windowOf(0, 60_000, 60))).toEqual([]);
  });
});
