// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import type { AlarmSnapshot, LocationSnapshot } from './hub';
import {
  NotInRecordingError,
  RECORDED_HISTORY_WINDOW_MS,
  RecordedClock,
  RecordedDataSource,
  createRecordedLister,
  createRecordedResolver,
} from './recorded';
import { parseBoardRecording } from './recording';
import { settle } from './testing/data-source-contract';
import {
  FIXTURE_DURATION_MS,
  FIXTURE_OCC_LAG_MS,
  FIXTURE_START_MS,
  fixtureJson,
  fixtureRecording,
} from './testing/recording-fixture';
import { installNetworkTrap, type NetworkTrap } from './testing/network-trap';
import type { DatasourceSelector, MeasurementSample } from './types';

const idleTicker = { start: () => () => {} };

function playerAt(startMs: number, options: { locations?: boolean } = {}) {
  const rec = fixtureRecording(options);
  const clock = new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker, startMs });
  const source = new RecordedDataSource(rec, clock);
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

  it('stamps each sample with the platform occurred time, not the applied time', async () => {
    const { source } = playerAt(10_000);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    const last = got[got.length - 1];
    expect(Date.parse(last.occurredTime as string)).toBe(FIXTURE_START_MS + 10_000 - FIXTURE_OCC_LAG_MS);
    expect(last.deviceToken).toBe('sp-hauler-01');
    expect(last.name).toBe('fuel_pct');
  });

  it('delivers a recorded null as a null value, not a skipped row', async () => {
    const { source } = playerAt(10_000);
    const got = samples(source, { kind: 'device', deviceToken: 'sp-plant-01', measurements: ['throughput_tph'] });
    await settle();
    expect(got.map((s) => s.value)).toEqual([410, null, 415]);
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
    const rec = parseBoardRecording({
      ...fixtureJson(),
      durationMs: 4 * RECORDED_HISTORY_WINDOW_MS,
      measurements: [
        {
          device: 'sp-hauler-01',
          name: 'fuel_pct',
          t: [0, RECORDED_HISTORY_WINDOW_MS, 2 * RECORDED_HISTORY_WINDOW_MS, 3 * RECORDED_HISTORY_WINDOW_MS],
          occ: [0, 0, 0, 0],
          v: [1, 2, 3, 4],
        },
      ],
      alarms: [],
      chapters: [],
    });
    const clock = new RecordedClock(rec.durationMs, { ticker: idleTicker, startMs: 2 * RECORDED_HISTORY_WINDOW_MS });
    const source = new RecordedDataSource(rec, clock);
    const got = samples(source, fuel('sp-hauler-01'));
    await settle();
    // t = 0 is two hours behind the cursor; t = 1 h is exactly one hour back, inside the window.
    expect(got.map((s) => s.value)).toEqual([2, 3]);
    clock.play();
    clock.advance(RECORDED_HISTORY_WINDOW_MS);
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

  it('expands an anchor to the members the run recorded', async () => {
    const { source } = playerAt(0);
    const got = samples(source, {
      kind: 'anchor',
      anchor: { relationship: 'assigned', targetType: 'customer', targetToken: 'acme-earthworks' },
      measurements: ['fuel_pct'],
    });
    await settle();
    expect(got.map((s) => s.deviceToken).sort()).toEqual(['sp-hauler-01', 'sp-hauler-02']);
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

  it('folds the alarm history to the latest state per token at the cursor', async () => {
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

  it('is empty before the first alarm row', async () => {
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

  it('maps a row to the widget shape: recorded times, no last value, no acknowledger', async () => {
    const { source } = playerAt(FIXTURE_DURATION_MS);
    const snaps = subscribe(source);
    await settle();
    const rows = new Map(snaps[snaps.length - 1].alarms.map((a) => [a.token, a]));
    const cleared = rows.get('alarm-1');
    expect(cleared?.originatorToken).toBe('sp-hauler-02');
    expect(cleared?.raisedTime).toBe(new Date(FIXTURE_START_MS + 9_700).toISOString());
    expect(cleared?.clearedTime).toBe(new Date(FIXTURE_START_MS + 29_700).toISOString());
    const acked = rows.get('alarm-3');
    expect(acked?.acknowledgedTime).toBe(new Date(FIXTURE_START_MS + 44_900).toISOString());
    for (const row of rows.values()) {
      expect(row.lastValue).toBeNull();
      expect(row.acknowledgedBy).toBeNull();
    }
  });
});

describe('RecordedDataSource: locations', () => {
  const fleet: DatasourceSelector = {
    kind: 'anchor',
    anchor: { relationship: 'assigned', targetType: 'customer', targetToken: 'acme-earthworks' },
    measurements: [],
    location: { series: 'latest' },
  };
  const machine = (deviceToken: string): DatasourceSelector => ({
    kind: 'device',
    deviceToken,
    measurements: [],
    location: { series: 'latest' },
  });

  it('steps positions: the last row at or before the cursor, never interpolated', async () => {
    const { source, clock } = playerAt(10_000, { locations: true });
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: machine('sp-hauler-01') }, { next: (s) => snaps.push(s) });
    await settle();
    const first = snaps[0];
    expect(first.kind === 'positions' && first.locations[0].latitude).toBe(40);
    clock.play();
    clock.advance(10_000);
    const second = snaps[snaps.length - 1];
    expect(second.kind === 'positions' && second.locations[0].latitude).toBe(40.001);
    expect(snaps).toHaveLength(2);
  });

  it('keeps absent optionals absent rather than zero, and records no accuracy', async () => {
    const { source } = playerAt(20_000, { locations: true });
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: machine('sp-hauler-01') }, { next: (s) => snaps.push(s) });
    await settle();
    const snap = snaps[snaps.length - 1];
    if (snap.kind !== 'positions') throw new Error('expected positions');
    expect(snap.locations[0]).toMatchObject({ elevation: null, heading: null, speed: 3.5, accuracy: null });
  });

  it('lists every device the selector resolved, located or not', async () => {
    const { source } = playerAt(0, { locations: true });
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: fleet }, { next: (s) => snaps.push(s) });
    await settle();
    const snap = snaps[0];
    if (snap.kind !== 'positions') throw new Error('expected positions');
    expect(snap.deviceTokens).toEqual(['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01']);
    expect(snap.locations.map((l) => l.deviceToken)).toEqual(['sp-hauler-01', 'sp-hauler-02']);
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

  it('raises the typed error for a map location channel the run did not record', async () => {
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

  it('raises the typed error for an alarm channel the run did not record', async () => {
    const rec = parseBoardRecording({
      ...fixtureJson(),
      channels: { ...(fixtureJson().channels as object), alarms: false },
      alarms: [],
    });
    const source = new RecordedDataSource(rec, new RecordedClock(FIXTURE_DURATION_MS, { ticker: idleTicker }));
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

  it('an empty location request for a selector with no location series is still the empty snapshot', async () => {
    const { source } = playerAt(0);
    const snaps: LocationSnapshot[] = [];
    source.subscribeLocations({ datasource: fuel('sp-hauler-01') }, { next: (s) => snaps.push(s) });
    await settle();
    expect(snaps).toEqual([{ kind: 'positions', deviceTokens: [], locations: [] }]);
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
  const site = { relationship: 'assigned', targetType: 'customer', targetToken: 'acme-earthworks' } as const;

  it('expands the recorded anchor to its members', async () => {
    expect(await createRecordedResolver(rec).devicesForAnchor(site)).toEqual([
      'sp-hauler-01',
      'sp-hauler-02',
      'sp-plant-01',
    ]);
  });

  it('throws for an anchor the run does not know instead of answering with no devices', async () => {
    const resolver = createRecordedResolver(rec);
    await expect(resolver.devicesForAnchor({ ...site, targetToken: 'someone-else' })).rejects.toThrow(/not in this recording/);
    await expect(resolver.devicesForAnchor({ ...site, relationship: 'located-in' })).rejects.toThrow();
  });

  it('knows exactly the devices in the run', async () => {
    const resolver = createRecordedResolver(rec);
    expect(await resolver.deviceExists('sp-plant-01')).toBe(true);
    expect(await resolver.deviceExists('sp-ghost-99')).toBe(false);
  });
});

describe('createRecordedLister', () => {
  const rec = fixtureRecording();

  it('lists the run devices for a device selector, so selection is not offered as live-only', async () => {
    const rows = await createRecordedLister(rec)('device');
    expect(rows.map((r) => r.token)).toEqual(['sp-hauler-01', 'sp-hauler-02', 'sp-plant-01']);
  });

  it('lists the recorded anchor targets by kind, and nothing for a kind the run never had', async () => {
    const lister = createRecordedLister(rec);
    expect((await lister('customer')).map((r) => r.token)).toEqual(['acme-earthworks']);
    expect(await lister('area')).toEqual([]);
    expect(await lister('asset')).toEqual([]);
  });
});
