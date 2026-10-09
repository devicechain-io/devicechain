// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import type { AlarmSnapshot, LocationSnapshot } from './hub';
import {
  NotInRecordingError,
  RECORDED_HISTORY_WINDOW_MS,
  RecordedClock,
  RecordedDataSource,
  createRecordedClock,
  createRecordedLister,
  createRecordedResolver,
} from './recorded';
import { parseBoardRecording } from './recording';
import { settle } from './testing/data-source-contract';
import {
  FIXTURE_DURATION_MS,
  FIXTURE_START_MS,
  SITE,
  fixtureInstant,
  fixtureJson,
  fixtureRecording,
} from './testing/recording-fixture';
import { installNetworkTrap, type NetworkTrap } from './testing/network-trap';
import type { DatasourceSelector, MeasurementSample } from './types';

type Json = Record<string, any>;

const idleTicker = { start: () => () => {} };

function playerAt(startMs: number) {
  const rec = fixtureRecording();
  const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker, startMs });
  const source = new RecordedDataSource(rec, clock, { siteAnchors: [SITE] });
  return { rec, clock, source };
}

const fuel = (device: string): DatasourceSelector => ({ kind: 'device', deviceToken: device, measurements: ['fuel_pct'] });

function samples(source: RecordedDataSource, ds: DatasourceSelector): MeasurementSample[] {
  const got: MeasurementSample[] = [];
  source.subscribeWidget(ds, { next: (s) => got.push(s) });
  return got;
}

let trap: NetworkTrap;
beforeEach(() => {
  trap = installNetworkTrap();
});
afterEach(() => {
  const calls = [...trap.calls];
  trap.restore();
  // A swallowed exception must still fail the test.
  expect(calls).toEqual([]);
});

describe('RecordedDataSource: value at the cursor', () => {
  it('delivers, at the cursor, the last row at or before it and nothing after', async () => {
    const { source } = playerAt(25_000);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    // Rows at t = 0, 10 000, 20 000 have been applied by 25 000; the one at 30 000 has not.
    expect(got.map((s) => s.value)).toEqual([90, 80, 70]);
    expect(got[got.length - 1].value).toBe(70);
  });

  it('includes a row exactly at the cursor', async () => {
    const { source } = playerAt(20_000);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    expect(got.map((s) => s.value)).toEqual([90, 80, 70]);
  });

  it('stamps each sample with the time the viewer applied it, as wall time from the run start', async () => {
    const { source } = playerAt(10_000);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    const last = got[got.length - 1];
    expect(Date.parse(last.occurredTime as string)).toBe(FIXTURE_START_MS + 10_000);
    expect(last.deviceToken).toBe('sp-hauler-01');
    expect(last.name).toBe('fuel_pct');
  });

  it('delivers a recorded zero as zero, not as a skipped or missing row', async () => {
    const { source } = playerAt(10_000);
    const got = samples(source, { kind: 'device', deviceToken: 'sp-plant-01', measurements: ['throughput_tph'] });
    await settle();
    expect(got.map((s) => s.value)).toEqual([410, 0, 415]);
  });

  it('emits nothing for a declared series the device has no rows for', async () => {
    // The tyre series exists only for hauler-01; hauler-02 never reported it.
    const { source } = playerAt(FIXTURE_DURATION_MS);
    const errors: unknown[] = [];
    const got: MeasurementSample[] = [];
    source.subscribeWidget(
      { kind: 'device', deviceToken: 'sp-hauler-02', measurements: ['tyre_pressure_kpa'] },
      { next: (s) => got.push(s), error: (e) => errors.push(e) },
    );
    await settle();
    expect(got).toEqual([]);
    expect(errors).toEqual([]);
  });

  it('returns every recorded name for an empty measurement list', async () => {
    const { source } = playerAt(FIXTURE_DURATION_MS);
    const got = samples(source, { kind: 'device', deviceToken: 'sp-hauler-01', measurements: [] });
    await settle();
    expect(new Set(got.map((s) => s.name))).toEqual(new Set(['fuel_pct', 'tyre_pressure_kpa']));
  });

  it('back-fills only the preceding window, then continues from the cursor', async () => {
    const W = RECORDED_HISTORY_WINDOW_MS;
    const doc = fixtureJson();
    doc.durationMs = 4 * W;
    doc.measurements = { d: [0, 0, 0, 0], n: [0, 0, 0, 0], t: [0, W, 2 * W, 3 * W], v: [1, 2, 3, 4], s: [0, 0, 0, 0] };
    doc.alarms = { snapshots: [], events: [] };
    doc.chapters = [];
    const rec = parseBoardRecording(doc);
    const clock = new RecordedClock(rec.durationMs, { ticker: idleTicker, startMs: 2 * W });
    const source = new RecordedDataSource(rec, clock);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    // t = 0 is two hours behind the cursor; t = 1 h is exactly one hour back, inside the window.
    expect(got.map((s) => s.value)).toEqual([2, 3]);
    clock.play();
    clock.advance(W);
    expect(got.map((s) => s.value)).toEqual([2, 3, 4]);
  });

  it('delivers rows incrementally as the cursor passes them, each exactly once', async () => {
    const { source, clock } = playerAt(0);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    expect(got.map((s) => s.value)).toEqual([90]);
    clock.play();
    clock.advance(9_999);
    expect(got.map((s) => s.value)).toEqual([90]);
    clock.advance(1);
    expect(got.map((s) => s.value)).toEqual([90, 80]);
    clock.advance(25_000);
    expect(got.map((s) => s.value)).toEqual([90, 80, 70, 60]);
  });

  it('interleaves the series of one subscription chronologically', async () => {
    const { source } = playerAt(20_000);
    const got = samples(source, { kind: 'device', deviceToken: 'sp-hauler-01', measurements: [] });
    await settle();
    const times = got.map((s) => Date.parse(s.occurredTime as string));
    expect([...times].sort((a, b) => a - b)).toEqual(times);
  });

  it('expands a declared site anchor to the devices the run recorded', async () => {
    const { source } = playerAt(0);
    const got = samples(source, { kind: 'anchor', anchor: SITE, measurements: ['fuel_pct'] });
    await settle();
    expect(got.map((s) => s.deviceToken).sort()).toEqual(['sp-hauler-01', 'sp-hauler-02']);
  });

  it('refuses an anchor the host did not declare, through the sink, not as an empty set', async () => {
    const { source } = playerAt(0);
    const errors: unknown[] = [];
    const got: MeasurementSample[] = [];
    source.subscribeWidget(
      { kind: 'anchor', anchor: { ...SITE, targetToken: 'someone-else' }, measurements: [] },
      { next: (s) => got.push(s), error: (e) => errors.push(e) },
    );
    await settle();
    expect(got).toEqual([]);
    expect(String(errors[0])).toMatch(/not in this recording/);
  });
});

describe('RecordedDataSource: seeking', () => {
  it('a backwards seek through a new view delivers no later value', async () => {
    const rec = fixtureRecording();
    const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker });
    const first = new RecordedDataSource(rec, clock);
    const before = samples(first, fuel('sp-hauler-01'));
    clock.play();
    await settle();
    clock.advance(35_000);
    expect(before.map((s) => s.value)).toEqual([90, 80, 70, 60]);

    clock.seek(12_000);
    const second = new RecordedDataSource(rec, clock);
    const after = samples(second, fuel('sp-hauler-01'));
    await settle();
    // The new view knows nothing at or beyond 20 000.
    expect(after.map((s) => s.value)).toEqual([90, 80]);
  });

  it('retires a view on seek: it emits nothing further in either direction', async () => {
    const rec = fixtureRecording();
    const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker });
    const view = new RecordedDataSource(rec, clock);
    const got = samples(view, fuel('sp-hauler-01'));
    await settle();
    clock.play();
    clock.advance(10_000);
    const seen = got.length;
    expect(view.isStale).toBe(false);

    clock.seek(50_000);
    expect(view.isStale).toBe(true);
    clock.advance(1_000);
    clock.seek(2_000);
    clock.advance(30_000);
    expect(got).toHaveLength(seen);
  });

  it('a view that is seeked before its subscription starts emits nothing', async () => {
    const { source, clock } = playerAt(10_000);
    const got = samples(source, fuel('sp-hauler-01'));
    clock.seek(30_000);
    await settle();
    expect(got).toEqual([]);
  });

  it('a new view after a forward seek back-fills up to the new cursor', async () => {
    const rec = fixtureRecording();
    const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker });
    const old = new RecordedDataSource(rec, clock);
    old.disposeAll();
    clock.seek(41_000);
    const got = samples(new RecordedDataSource(rec, clock), fuel('sp-hauler-01'));
    await settle();
    expect(got.map((s) => s.value)).toEqual([90, 80, 70, 60, 50]);
  });
});

describe('RecordedDataSource: alarms', () => {
  const subscribe = (source: RecordedDataSource, over: Partial<Parameters<RecordedDataSource['subscribeAlarms']>[0]> = {}) => {
    const snaps: AlarmSnapshot[] = [];
    source.subscribeAlarms({ pageSize: 10, ...over }, { next: (s) => snaps.push(s) });
    return snaps;
  };

  it('folds the alarm history to the latest state per alarm at the cursor', async () => {
    const { source } = playerAt(35_000);
    const snaps = subscribe(source);
    await settle();
    const last = snaps[snaps.length - 1];
    expect(last.total).toBe(2);
    const byToken = new Map(last.alarms.map((a) => [a.token, a]));
    expect(byToken.get('alarm-1')?.state).toBe('CLEARED');
    expect(byToken.get('alarm-2')?.state).toBe('ACTIVE');
    expect(byToken.has('alarm-3')).toBe(false);
  });

  it('is empty before the first alarm event', async () => {
    const { source } = playerAt(5_000);
    const snaps = subscribe(source);
    await settle();
    expect(snaps).toEqual([{ alarms: [], total: 0 }]);
  });

  it('applies the subscription filters and reports the matching total', async () => {
    const { source } = playerAt(FIXTURE_DURATION_MS);
    const active = subscribe(source, { state: 'ACTIVE' });
    const major = subscribe(source, { severity: 'MAJOR' });
    const acked = subscribe(source, { acknowledged: true });
    await settle();
    expect(active[active.length - 1].total).toBe(2);
    expect(major[major.length - 1].alarms.map((a) => a.token)).toEqual(['alarm-2']);
    expect(acked[acked.length - 1].alarms.map((a) => a.token)).toEqual(['alarm-3']);
  });

  it('scopes to the devices a selector names', async () => {
    const { source } = playerAt(FIXTURE_DURATION_MS);
    const snaps = subscribe(source, { datasource: { kind: 'device', deviceToken: 'sp-hauler-01', measurements: [] } });
    await settle();
    expect(snaps[snaps.length - 1].alarms.map((a) => a.alarmKey)).toEqual(['tyre-pressure-low']);
  });

  it('re-emits only when the fold changes', async () => {
    const { source, clock } = playerAt(0);
    const snaps = subscribe(source);
    await settle();
    clock.play();
    clock.advance(1_000);
    clock.advance(1_000);
    expect(snaps).toHaveLength(1);
    clock.advance(8_000);
    expect(snaps).toHaveLength(2);
    expect(snaps[1].total).toBe(1);
  });

  it('maps an alarm to the widget shape: recorded times, no last value, no acknowledger', async () => {
    const { source } = playerAt(FIXTURE_DURATION_MS);
    const snaps = subscribe(source);
    await settle();
    const rows = new Map(snaps[snaps.length - 1].alarms.map((a) => [a.token, a]));
    const cleared = rows.get('alarm-1');
    expect(cleared?.originatorToken).toBe('sp-hauler-02');
    // A cleared alarm keeps the raise time its ACTIVE row gave it; the CLEARED row's own
    // time is the clear time.
    expect(cleared?.raisedTime).toBe(fixtureInstant(9_700));
    expect(cleared?.clearedTime).toBe(fixtureInstant(29_700));
    const acked = rows.get('alarm-3');
    expect(acked?.acknowledged).toBe(true);
    expect(acked?.clearedTime).toBeNull();
    // The recording says THAT it was acknowledged, not when.
    expect(acked?.acknowledgedTime).toBeNull();
    for (const row of rows.values()) {
      expect(row.lastValue).toBeNull();
      expect(row.acknowledgedBy).toBeNull();
    }
  });
});

describe('RecordedDataSource: alarm snapshots', () => {
  const alarm = (id: string, state: string, occ: number) => ({
    id, dev: 'sp-hauler-01', key: 'k', metric: 'fuel_pct', state, sev: 'MAJOR', occ: fixtureInstant(occ),
  });

  async function folded(mutate: (doc: Json) => void, cursor: number) {
    const doc = fixtureJson();
    mutate(doc);
    const rec = parseBoardRecording(doc);
    const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker, startMs: cursor });
    const snaps: AlarmSnapshot[] = [];
    new RecordedDataSource(rec, clock).subscribeAlarms({ pageSize: 10 }, { next: (x) => snaps.push(x) });
    await settle();
    return snaps[snaps.length - 1];
  }

  it('a later snapshot replaces the whole set, then events apply on top of it', async () => {
    const mutate = (d: Json) => {
      d.alarms.snapshots.push({ tMs: 35_000, requestedAtMs: 35_000, total: 1, alarms: [alarm('alarm-9', 'ACTIVE', 34_000)] });
      d.alarms.events.push({ ...alarm('alarm-9', 'CLEARED', 36_000), tMs: 36_000 });
      d.alarms.events.sort((a: Json, b: Json) => a.tMs - b.tMs);
    };
    const atSnapshot = await folded(mutate, 35_500);
    expect(atSnapshot.alarms.map((a) => a.token)).toEqual(['alarm-9']);
    expect(atSnapshot.total).toBe(1);
    const afterEvent = await folded(mutate, 36_000);
    expect(afterEvent.alarms.map((a) => [a.token, a.state, a.raisedTime, a.clearedTime])).toEqual([
      ['alarm-9', 'CLEARED', fixtureInstant(34_000), fixtureInstant(36_000)],
    ]);
  });

  it('a snapshot listing a cleared alarm shows it cleared, with no raise time it never saw', async () => {
    const last = await folded((d) => {
      d.alarms.snapshots[0] = { tMs: 0, requestedAtMs: 0, total: 1, alarms: [alarm('alarm-9', 'CLEARED', 100)] };
    }, 5_000);
    expect(last.alarms).toHaveLength(1);
    expect(last.alarms[0]).toMatchObject({ raisedTime: null, clearedTime: fixtureInstant(100), state: 'CLEARED' });
  });
});

describe('RecordedDataSource: alarm ordering and acknowledgement', () => {
  const alarm = (id: string, state: string, occ: string, extra: Json = {}) => ({
    id, dev: 'sp-hauler-01', key: 'k', metric: 'fuel_pct', state, sev: 'MAJOR', occ, ...extra,
  });

  async function folded(mutate: (doc: Json) => void, cursor: number) {
    const doc = fixtureJson();
    mutate(doc);
    const rec = parseBoardRecording(doc);
    const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker, startMs: cursor });
    const snaps: AlarmSnapshot[] = [];
    new RecordedDataSource(rec, clock).subscribeAlarms({ pageSize: 10 }, { next: (x) => snaps.push(x) });
    await settle();
    return snaps[snaps.length - 1];
  }

  const sorted = (d: Json) => d.alarms.events.sort((a: Json, b: Json) => a.tMs - b.tMs);

  it('each record decides its own ack: acknowledgement does not carry over to a later record', async () => {
    const mutate = (d: Json) => {
      d.alarms.events.push({ ...alarm('alarm-9', 'ACTIVE', '2026-01-02T03:04:11Z', { ack: true }), tMs: 11_000 });
      d.alarms.events.push({ ...alarm('alarm-9', 'ACTIVE', '2026-01-02T03:04:11Z'), tMs: 12_000 });
      sorted(d);
    };
    const acked = await folded(mutate, 11_500);
    expect(acked.alarms.find((a) => a.token === 'alarm-9')?.acknowledged).toBe(true);
    const later = await folded(mutate, 12_500);
    expect(later.alarms.find((a) => a.token === 'alarm-9')?.acknowledged).toBe(false);
  });

  it('orders alarms by the instant they were raised, not by the text of the timestamp', async () => {
    const last = await folded((d) => {
      // 03:04:15Z is earlier than 03:04:15.5Z although it sorts after it as text.
      d.alarms.events.push({ ...alarm('alarm-5', 'ACTIVE', '2026-01-02T03:04:15Z'), tMs: 11_000 });
      d.alarms.events.push({ ...alarm('alarm-6', 'ACTIVE', '2026-01-02T03:04:15.5Z'), tMs: 11_000 });
      sorted(d);
    }, 12_000);
    expect(last.alarms.map((a) => a.token)).toEqual(['alarm-6', 'alarm-5', 'alarm-1']);
  });

  it('at an equal time an event applies after the snapshot, so the event wins', async () => {
    const last = await folded((d) => {
      d.alarms.snapshots.push({
        tMs: 35_000,
        requestedAtMs: 35_000,
        total: 1,
        alarms: [alarm('alarm-9', 'ACTIVE', '2026-01-02T03:04:34Z')],
      });
      d.alarms.events.push({ ...alarm('alarm-9', 'CLEARED', '2026-01-02T03:04:40Z'), tMs: 35_000 });
      sorted(d);
    }, 35_000);
    expect(last.alarms.map((a) => [a.token, a.state])).toEqual([['alarm-9', 'CLEARED']]);
  });

  it('a snapshot never erases an alarm that changed after its query was sent', async () => {
    const mutate = (requestedAtMs: number) => (d: Json) => {
      d.alarms.events.push({ ...alarm('alarm-7', 'ACTIVE', '2026-01-02T03:04:34Z'), tMs: 34_000 });
      d.alarms.snapshots.push({ tMs: 35_000, requestedAtMs, total: 0, alarms: [] });
      sorted(d);
    };
    // Query sent at 33 000, before the event at 34 000: the answer cannot speak to it.
    const kept = await folded(mutate(33_000), 35_500);
    expect(kept.alarms.map((a) => a.token)).toEqual(['alarm-7']);
    // Query sent at 34 500, after the event: the answer is authoritative and omits it.
    const erased = await folded(mutate(34_500), 35_500);
    expect(erased.alarms).toEqual([]);
  });

  it('keeps the newer event over a contradicting snapshot entry', async () => {
    const last = await folded((d) => {
      d.alarms.events.push({ ...alarm('alarm-7', 'CLEARED', '2026-01-02T03:04:39Z'), tMs: 34_000 });
      d.alarms.snapshots.push({
        tMs: 35_000,
        requestedAtMs: 33_000,
        total: 1,
        alarms: [alarm('alarm-7', 'ACTIVE', '2026-01-02T03:04:30Z')],
      });
      sorted(d);
    }, 35_500);
    expect(last.alarms.map((a) => [a.token, a.state])).toEqual([['alarm-7', 'CLEARED']]);
  });
});

describe('RecordedDataSource: excerpts', () => {
  it('plays only the excerpt window, with the alarm history up to its end intact', async () => {
    const doc = fixtureJson();
    // Keep only the measurement rows of [25 000, 50 000].
    const m = doc.measurements;
    const keep = m.t.map((t: number, i: number) => (t >= 25_000 && t <= 50_000 ? i : -1)).filter((i: number) => i >= 0);
    for (const k of ['d', 'n', 't', 'v', 's']) m[k] = keep.map((i: number) => m[k][i]);
    doc.excerpt = { fromMs: 25_000, toMs: 50_000 };
    doc.chapters = [];
    const rec = parseBoardRecording(doc);

    const clock = createRecordedClock(rec, { ticker: idleTicker });
    expect([clock.minMs, clock.timeMs, clock.durationMs]).toEqual([25_000, 25_000, 50_000]);
    clock.seek(0);
    expect(clock.timeMs).toBe(25_000);
    clock.seek(60_000);
    expect(clock.timeMs).toBe(50_000);
    clock.seek(25_000);

    const view = new RecordedDataSource(rec, clock);
    const alarms: AlarmSnapshot[] = [];
    view.subscribeAlarms({ pageSize: 10 }, { next: (x) => alarms.push(x) });
    const fuelNow = samples(view, fuel('sp-hauler-01'));
    await settle();
    // Both alarms were raised before the window opened, and still fold into the table.
    expect(alarms[alarms.length - 1].alarms.map((a) => [a.token, a.state])).toEqual([
      ['alarm-2', 'ACTIVE'],
      ['alarm-1', 'ACTIVE'],
    ]);
    // The 10 000 and 20 000 rows lie outside the window, so there is nothing to back-fill;
    // playback then delivers the first row inside it.
    expect(fuelNow).toEqual([]);
    clock.play();
    clock.advance(5_000);
    expect(fuelNow.map((s) => s.value)).toEqual([60]);
  });
});

describe('RecordedDataSource: what the recording does not hold', () => {
  function errorOf(subscribe: (sink: { next: (x: any) => void; error: (e: unknown) => void }) => unknown): Promise<unknown[]> {
    const errors: unknown[] = [];
    const nexts: unknown[] = [];
    subscribe({ next: (x) => nexts.push(x), error: (e) => errors.push(e) });
    return settle().then(() => {
      // Never an empty answer in place of an error.
      expect(nexts).toEqual([]);
      return errors;
    });
  }

  it('raises the typed error for commands, never an empty snapshot', async () => {
    const { source } = playerAt(0);
    const errors = await errorOf((sink) => source.subscribeCommands({ datasource: fuel('sp-hauler-01'), pageSize: 5 }, sink));
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(NotInRecordingError);
    expect((errors[0] as NotInRecordingError).channel).toBe('commands');
  });

  it('raises the typed error for the map location channel, which this format never records', async () => {
    const { source } = playerAt(0);
    const errors = await errorOf((sink) =>
      source.subscribeLocations(
        { datasource: { kind: 'device', deviceToken: 'sp-hauler-01', measurements: [], location: { series: 'latest' } } },
        sink,
      ),
    );
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(NotInRecordingError);
    expect((errors[0] as NotInRecordingError).channel).toBe('locations');
  });

  it('answers a selector that names no location series with the empty snapshot, as the hub does', async () => {
    const { source } = playerAt(0);
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: fuel('sp-hauler-01') }, { next: (s) => snaps.push(s) });
    await settle();
    expect(snaps).toEqual([{ kind: 'positions', deviceTokens: [], locations: [] }]);
  });

  it('raises the typed error for an alarm channel the run did not record', async () => {
    const doc = fixtureJson();
    doc.channels.alarms = false;
    doc.alarms = { snapshots: [], events: [] };
    const source = new RecordedDataSource(
      parseBoardRecording(doc),
      new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker }),
    );
    const errors = await errorOf((sink) => source.subscribeAlarms({ pageSize: 5 }, sink));
    expect(errors[0]).toBeInstanceOf(NotInRecordingError);
    expect((errors[0] as NotInRecordingError).channel).toBe('alarms');
  });

  it('raises the typed error for a measurement name that was never recorded', async () => {
    const { source } = playerAt(0);
    const errors = await errorOf((sink) =>
      source.subscribeWidget({ kind: 'device', deviceToken: 'sp-hauler-01', measurements: ['engine_temp_c'] }, sink),
    );
    expect(errors[0]).toBeInstanceOf(NotInRecordingError);
    expect((errors[0] as NotInRecordingError).channel).toBe('measurements');
  });

  it('prints as the bare message, which is what the widget error frame shows', () => {
    const err = new NotInRecordingError('locations', 'no positions were recorded');
    expect(String(err)).toBe('Not in this recording: no positions were recorded');
    expect(String(new NotInRecordingError('commands'))).toBe('Not in this recording');
  });

  it('is not a WidgetActions: nothing that writes can be reached through it', () => {
    const { source } = playerAt(0);
    for (const method of ['can', 'acknowledgeAlarm', 'clearAlarm', 'sendCommand']) {
      expect((source as unknown as Record<string, unknown>)[method]).toBeUndefined();
    }
  });
});

describe('RecordedDataSource: availability', () => {
  it('is available for a device the run has, unavailable for one it does not', async () => {
    const { source } = playerAt(0);
    expect(await source.isDatasourceAvailable(fuel('sp-hauler-01'))).toBe(true);
    expect(await source.isDatasourceAvailable(fuel('sp-ghost-99'))).toBe(false);
  });
});

describe('createRecordedResolver', () => {
  const rec = fixtureRecording();

  it('expands a declared site anchor to the run devices', async () => {
    expect(await createRecordedResolver(rec, { siteAnchors: [SITE] }).devicesForAnchor(SITE)).toEqual([
      'sp-hauler-01',
      'sp-hauler-02',
      'sp-plant-01',
    ]);
  });

  it('throws for an anchor that was not declared instead of answering with no devices', async () => {
    const resolver = createRecordedResolver(rec, { siteAnchors: [SITE] });
    await expect(resolver.devicesForAnchor({ ...SITE, targetToken: 'someone-else' })).rejects.toThrow(/not in this recording/);
    await expect(resolver.devicesForAnchor({ ...SITE, relationship: 'located-in' })).rejects.toThrow();
    // With no anchor declared at all, even the site is unknown rather than empty.
    await expect(createRecordedResolver(rec).devicesForAnchor(SITE)).rejects.toThrow(/not in this recording/);
  });

  it('knows exactly the devices in the run', async () => {
    const resolver = createRecordedResolver(rec);
    expect(await resolver.deviceExists('sp-plant-01')).toBe(true);
    expect(await resolver.deviceExists('sp-ghost-99')).toBe(false);
  });
});

describe('declared site anchors', () => {
  const rec = fixtureRecording();
  const clock = () => new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker });
  const area = { relationship: 'assigned', targetType: 'area', targetToken: 'north-pit' } as const;

  it('accept exactly one anchor, and only a customer, wherever options are taken', () => {
    const builders = [
      (o: { siteAnchors: any }) => createRecordedResolver(rec, o),
      (o: { siteAnchors: any }) => createRecordedLister(rec, o),
      (o: { siteAnchors: any }) => new RecordedDataSource(rec, clock(), o),
    ];
    for (const build of builders) {
      expect(() => build({ siteAnchors: [] })).toThrow(/exactly one/);
      expect(() => build({ siteAnchors: [SITE, { ...SITE, targetToken: 'second' }] })).toThrow(/exactly one/);
      expect(() => build({ siteAnchors: [area] })).toThrow(/customer/);
      expect(() => build({ siteAnchors: [SITE] })).not.toThrow();
    }
  });
});

describe('createRecordedLister', () => {
  const rec = fixtureRecording();

  it('lists the run devices for a device selector, so selection is not offered as live-only', async () => {
    const rows = await createRecordedLister(rec)('device');
    expect(rows.map((r) => r.token)).toEqual(['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01']);
  });

  it('lists the declared site anchors by kind, and nothing for a kind with none', async () => {
    const lister = createRecordedLister(rec, { siteAnchors: [SITE] });
    expect((await lister('customer')).map((r) => r.token)).toEqual(['acme-earthworks']);
    expect(await lister('area')).toEqual([]);
    expect(await lister('asset')).toEqual([]);
  });
});

// The converter's real excerpt of the first recorded run. The file arrives with the
// converter change; until then this suite reports itself skipped, by name, rather than
// passing on nothing.
const EXCERPT = fileURLToPath(new URL('../../../testdata/board-replay/take2-tyre-excerpt.json', import.meta.url));

describe.skipIf(!existsSync(EXCERPT))('the converter excerpt of the first recorded run', () => {
  const load = () => parseBoardRecording(JSON.parse(readFileSync(EXCERPT, 'utf-8')));

  it('parses, and declares no positions or commands', () => {
    const r = load();
    expect(r.excerpt).toBeDefined();
    expect(r.channels).toMatchObject({ alarms: true, locations: false, commands: false });
  });

  it('shows the tyre alarm active and the hauler tyre pressure at 441.2 s', async () => {
    const r = load();
    const clock = new RecordedClock(r.excerpt!.toMs, { minMs: r.excerpt!.fromMs, startMs: 441_200, ticker: idleTicker });
    const source = new RecordedDataSource(r, clock, { siteAnchors: [SITE] });
    const alarms: AlarmSnapshot[] = [];
    source.subscribeAlarms({ pageSize: 50, state: 'ACTIVE' }, { next: (x) => alarms.push(x) });
    const tyre = samples(source, { kind: 'device', deviceToken: 'sp-hauler-03', measurements: ['tyre_pressure_kpa'] });
    await settle();
    const last = alarms[alarms.length - 1];
    expect(last.alarms.map((a) => [a.alarmKey, a.originatorToken, a.severity])).toEqual([
      ['tyre-pressure-low', 'sp-hauler-03', 'MAJOR'],
    ]);
    expect(tyre[tyre.length - 1].value).toBe(599.6);
  });
});
