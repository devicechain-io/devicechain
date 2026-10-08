// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type {
  AlarmRow,
  AlarmSnapshot,
  AlarmStreamSink,
  AlarmSubscription,
  CommandSnapshot,
  CommandStreamSink,
  CommandSubscription,
  DashboardHub,
  DatasourceSelector,
  LocationSample,
  LocationSnapshot,
  LocationStreamSink,
  LocationSubscription,
  MeasurementSample,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import type { DashboardDefinition, MemberResolver, SlotBinding } from '@devicechain/dashboards';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(cleanup);

import {
  useAlarmStream,
  useCommandStream,
  useDatasourceAvailability,
  useLocationStream,
  useMeasurementStream,
  useResolvedBindings,
} from './hooks';

function fakeHub() {
  let sink: WidgetStreamSink | null = null;
  const unsub = vi.fn();
  const hub = {
    subscribeWidget: (_datasource: DatasourceSelector, s: WidgetStreamSink) => {
      sink = s;
      return unsub;
    },
  } as unknown as DashboardHub;
  return {
    hub,
    unsub,
    push: (m: MeasurementSample) => act(() => sink?.next(m)),
  };
}

const ds: DatasourceSelector = { kind: 'device', deviceToken: 'therm-001', measurements: ['temperature'] };

const sample = (name: string, value: number, time: string): MeasurementSample => ({
  id: `${name}-${time}`,
  deviceToken: 'therm-001',
  eventType: 0,
  occurredTime: time,
  name,
  value,
  classifier: null,
});

describe('useMeasurementStream', () => {
  it('tracks the latest value per measurement name and appends to the window', () => {
    const f = fakeHub();
    const { result } = renderHook(() => useMeasurementStream(f.hub, ds));

    f.push(sample('temperature', 20, 't1'));
    f.push(sample('humidity', 55, 't2'));
    f.push(sample('temperature', 21, 't3'));

    expect(result.current.latest.temperature.value).toBe(21);
    expect(result.current.latest.humidity.value).toBe(55);
    expect(result.current.samples).toHaveLength(3);
  });

  it('bounds the rolling window to the configured size', () => {
    const f = fakeHub();
    const { result } = renderHook(() => useMeasurementStream(f.hub, ds, { window: 2 }));

    f.push(sample('t', 1, 'a'));
    f.push(sample('t', 2, 'b'));
    f.push(sample('t', 3, 'c'));

    expect(result.current.samples.map((s) => s.value)).toEqual([2, 3]);
  });

  it('unsubscribes on unmount', () => {
    const f = fakeHub();
    const { unmount } = renderHook(() => useMeasurementStream(f.hub, ds));
    unmount();
    expect(f.unsub).toHaveBeenCalledTimes(1);
  });

  it('seeds the window with initialSamples ahead of the live tail', () => {
    const f = fakeHub();
    const history = [sample('temperature', 18, 'h1'), sample('temperature', 19, 'h2')];
    const { result } = renderHook(() =>
      useMeasurementStream(f.hub, ds, { initialSamples: history }),
    );

    // History is visible before any live sample arrives.
    expect(result.current.samples.map((s) => s.value)).toEqual([18, 19]);
    expect(result.current.latest.temperature.value).toBe(19);

    // A live sample appends after the seed, and live wins for `latest`.
    f.push(sample('temperature', 22, 't1'));
    expect(result.current.samples.map((s) => s.value)).toEqual([18, 19, 22]);
    expect(result.current.latest.temperature.value).toBe(22);
  });

  it('caps the merged history+live window to the configured size', () => {
    const f = fakeHub();
    const history = [sample('t', 1, 'h1'), sample('t', 2, 'h2')];
    const { result } = renderHook(() =>
      useMeasurementStream(f.hub, ds, { window: 3, initialSamples: history }),
    );

    f.push(sample('t', 3, 'a'));
    f.push(sample('t', 4, 'b'));

    // 2 history + 2 live = 4, capped to the newest 3.
    expect(result.current.samples.map((s) => s.value)).toEqual([2, 3, 4]);
  });
});

function fakeAlarmHub() {
  let sink: AlarmStreamSink | null = null;
  const unsub = vi.fn();
  const hub = {
    // Only the two methods the alarm hook path touches; the measurement one is a no-op.
    subscribeWidget: () => () => {},
    subscribeAlarms: (_subscription: AlarmSubscription, s: AlarmStreamSink) => {
      sink = s;
      return unsub;
    },
  } as unknown as DashboardHub;
  return {
    hub,
    unsub,
    push: (snapshot: AlarmSnapshot) => act(() => sink?.next(snapshot)),
    fail: (err: unknown) => act(() => sink?.error?.(err)),
  };
}

const alarmSub: AlarmSubscription = { pageSize: 50 };

const alarm = (over: Partial<AlarmRow> = {}): AlarmRow => ({
  token: 'a-1',
  originatorType: 'device',
  originatorToken: 'thermostat-01',
  alarmKey: 'over-temperature',
  metricKey: 'temperature',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: '2026-07-05T12:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 87.4,
  ...over,
});

// A selection moves a widget from one device to another on the SAME hub. The live buffer
// is reset by an effect, and an effect runs after the render that carried the new
// selector — so the hook must not let that one render show the old device's values.
describe('useMeasurementStream across a selector change', () => {
  it("never renders the previous device's live value under the new selector", () => {
    const f = fakeHub();
    const deviceA: DatasourceSelector = { kind: 'device', deviceToken: 'dev-a', measurements: ['temperature'] };
    const deviceB: DatasourceSelector = { kind: 'device', deviceToken: 'dev-b', measurements: ['temperature'] };
    // Every render's view, recorded DURING render (before any effect of that commit runs).
    const seen: Array<{ token: string; value: unknown }> = [];
    const { rerender } = renderHook(
      ({ datasource }) => {
        const state = useMeasurementStream(f.hub, datasource);
        seen.push({
          token: datasource.kind === 'device' ? datasource.deviceToken : '',
          value: state.latest.temperature?.value,
        });
        return state;
      },
      { initialProps: { datasource: deviceA } },
    );
    f.push(sample('temperature', 71, '2026-01-01T00:00:00Z'));
    expect(seen[seen.length - 1]).toEqual({ token: 'dev-a', value: 71 });

    seen.length = 0;
    rerender({ datasource: deviceB });
    expect(seen.length).toBeGreaterThan(0);
    for (const view of seen) expect(view).toEqual({ token: 'dev-b', value: undefined });
  });

  it("ignores a late value from the previous selector's released subscription", () => {
    // A data source that keeps every sink it was handed, released or not.
    const sinks = new Map<string, WidgetStreamSink>();
    const hub = {
      subscribeWidget: (datasource: DatasourceSelector, s: WidgetStreamSink) => {
        sinks.set(datasource.kind === 'device' ? datasource.deviceToken : '', s);
        return () => {};
      },
    } as unknown as DashboardHub;
    const deviceA: DatasourceSelector = { kind: 'device', deviceToken: 'dev-a', measurements: ['temperature'] };
    const deviceB: DatasourceSelector = { kind: 'device', deviceToken: 'dev-b', measurements: ['temperature'] };
    const { result, rerender } = renderHook(({ datasource }) => useMeasurementStream(hub, datasource), {
      initialProps: { datasource: deviceA },
    });

    rerender({ datasource: deviceB });
    act(() => sinks.get('dev-b')?.next({ ...sample('temperature', 12, '2026-01-01T00:00:01Z'), deviceToken: 'dev-b' }));
    expect(result.current.latest.temperature?.value).toBe(12);

    // dev-a's sink answers after it was released: dev-b's value stays.
    act(() => sinks.get('dev-a')?.next(sample('temperature', 71, '2026-01-01T00:00:02Z')));
    expect(result.current.latest.temperature?.value).toBe(12);
    expect(result.current.samples.map((s) => s.value)).toEqual([12]);
  });
});

describe('useAlarmStream', () => {
  it('starts in a loading state before any snapshot arrives', () => {
    const f = fakeAlarmHub();
    const { result } = renderHook(() => useAlarmStream(f.hub, alarmSub));
    expect(result.current.loading).toBe(true);
    expect(result.current.alarms).toEqual([]);
    expect(result.current.total).toBe(0);
  });

  it('holds the latest snapshot and clears loading', () => {
    const f = fakeAlarmHub();
    const { result } = renderHook(() => useAlarmStream(f.hub, alarmSub));

    f.push({ alarms: [alarm({ token: 'a-1' }), alarm({ token: 'a-2' })], total: 9 });

    expect(result.current.alarms.map((a) => a.token)).toEqual(['a-1', 'a-2']);
    expect(result.current.total).toBe(9);
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it('records a sink error and stops loading', () => {
    const f = fakeAlarmHub();
    const { result } = renderHook(() => useAlarmStream(f.hub, alarmSub));

    const err = new Error('alarms query failed');
    f.fail(err);

    expect(result.current.error).toBe(err);
    expect(result.current.loading).toBe(false);
  });

  it('unsubscribes on unmount', () => {
    const f = fakeAlarmHub();
    const { unmount } = renderHook(() => useAlarmStream(f.hub, alarmSub));
    unmount();
    expect(f.unsub).toHaveBeenCalledTimes(1);
  });
});

function fakeCommandHub() {
  let sink: CommandStreamSink | null = null;
  const unsub = vi.fn();
  const hub = {
    subscribeWidget: () => () => {},
    subscribeCommands: (_subscription: CommandSubscription, s: CommandStreamSink) => {
      sink = s;
      return unsub;
    },
  } as unknown as DashboardHub;
  return {
    hub,
    unsub,
    push: (snapshot: CommandSnapshot) => act(() => sink?.next(snapshot)),
    fail: (err: unknown) => act(() => sink?.error?.(err)),
  };
}

const commandSub: CommandSubscription = { datasource: { kind: 'device', deviceToken: 'd1', measurements: [] }, pageSize: 20 };

describe('useCommandStream', () => {
  it('starts loading before any snapshot', () => {
    const f = fakeCommandHub();
    const { result } = renderHook(() => useCommandStream(f.hub, commandSub));
    expect(result.current.loading).toBe(true);
    expect(result.current.deviceToken).toBeNull();
    expect(result.current.commands).toEqual([]);
  });

  it('holds the latest command snapshot and clears loading', () => {
    const f = fakeCommandHub();
    const { result } = renderHook(() => useCommandStream(f.hub, commandSub));

    f.push({
      deviceToken: 'd1',
      commands: [{ token: 'c-1', name: 'reboot', status: 'SENT', payload: null, responsePayload: null, error: null, queuedTime: null, sentTime: null, respondedTime: null }],
      total: 1,
    });

    expect(result.current.deviceToken).toBe('d1');
    expect(result.current.commands.map((c) => c.token)).toEqual(['c-1']);
    expect(result.current.total).toBe(1);
    expect(result.current.loading).toBe(false);
  });

  it('records a sink error and stops loading', () => {
    const f = fakeCommandHub();
    const { result } = renderHook(() => useCommandStream(f.hub, commandSub));
    const err = new Error('commands query failed');
    f.fail(err);
    expect(result.current.error).toBe(err);
    expect(result.current.loading).toBe(false);
  });

  it('unsubscribes on unmount', () => {
    const f = fakeCommandHub();
    const { unmount } = renderHook(() => useCommandStream(f.hub, commandSub));
    unmount();
    expect(f.unsub).toHaveBeenCalledTimes(1);
  });
});

function availabilityHub(result: boolean | Promise<boolean> | (() => Promise<boolean>)) {
  const isDatasourceAvailable = vi.fn(() =>
    typeof result === 'function' ? result() : Promise.resolve(result),
  );
  return { hub: { isDatasourceAvailable } as unknown as DashboardHub, isDatasourceAvailable };
}

const deviceDs: DatasourceSelector = { kind: 'device', deviceToken: 'd1', measurements: [] };

describe('useDatasourceAvailability', () => {
  it('starts unknown (optimistic), then resolves available', async () => {
    const f = availabilityHub(true);
    const { result } = renderHook(() => useDatasourceAvailability(f.hub, deviceDs));
    expect(result.current).toBe('unknown');
    await act(async () => {});
    expect(result.current).toBe('available');
  });

  it('flips to unavailable when the device is gone', async () => {
    const f = availabilityHub(false);
    const { result } = renderHook(() => useDatasourceAvailability(f.hub, deviceDs));
    await act(async () => {});
    expect(result.current).toBe('unavailable');
  });

  it('fails open (available) when the check rejects', async () => {
    const f = availabilityHub(() => Promise.reject(new Error('down')));
    const { result } = renderHook(() => useDatasourceAvailability(f.hub, deviceDs));
    await act(async () => {});
    expect(result.current).toBe('available');
  });

  it('re-checks when the datasource changes', async () => {
    const f = availabilityHub(true);
    const { rerender } = renderHook(({ ds }) => useDatasourceAvailability(f.hub, ds), {
      initialProps: { ds: deviceDs },
    });
    await act(async () => {});
    rerender({ ds: { kind: 'device', deviceToken: 'd2', measurements: [] } });
    await act(async () => {});
    expect(f.isDatasourceAvailable).toHaveBeenCalledTimes(2);
  });

  it('is available immediately with no datasource — never queries', async () => {
    const f = availabilityHub(false); // would report unavailable IF it were asked
    const { result } = renderHook(() => useDatasourceAvailability(f.hub, undefined));
    await act(async () => {});
    expect(result.current).toBe('available');
    expect(f.isDatasourceAvailable).not.toHaveBeenCalled();
  });

  it('re-validates while unavailable so a recreated device recovers (then stops polling)', async () => {
    vi.useFakeTimers();
    try {
      let exists = false;
      const isDatasourceAvailable = vi.fn(() => Promise.resolve(exists));
      const hub = { isDatasourceAvailable } as unknown as DashboardHub;
      const { result } = renderHook(() => useDatasourceAvailability(hub, deviceDs));

      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
      expect(result.current).toBe('unavailable');

      exists = true; // device recreated with the same token (ADR-042 frees tokens)
      await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
      expect(result.current).toBe('available');
      expect(isDatasourceAvailable).toHaveBeenCalledTimes(2);

      // Recovered → polling stops (no further checks).
      await act(async () => { await vi.advanceTimersByTimeAsync(120_000); });
      expect(isDatasourceAvailable).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('useResolvedBindings', () => {
  const dashboard = (slots: DashboardDefinition['slots']): DashboardDefinition => ({
    schemaVersion: 1,
    title: '',
    canvas: { grid: { columns: 24, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
    widgets: [],
    slots,
  });
  const areaBinding: SlotBinding = {
    kind: 'anchor',
    anchor: { relationship: 'assigned', targetType: 'area', targetToken: 'b1' },
  };

  it('returns base+selection synchronously for a scope-free dashboard (no resolver call)', () => {
    const def = dashboard({ s1: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'd1' } } });
    const base = { s1: { kind: 'device', deviceToken: 'd1' } as SlotBinding };
    const resolver: MemberResolver = { devicesForAnchor: vi.fn() };
    const { result } = renderHook(() => useResolvedBindings(def, base, { s1: { kind: 'device', deviceToken: 'picked' } }, resolver));
    expect(result.current).toEqual({ s1: { kind: 'device', deviceToken: 'picked' } }); // selection wins
    expect(resolver.devicesForAnchor).not.toHaveBeenCalled();
  });

  it('runs the cascade for a scoped dashboard: first → the parent first member (by token)', async () => {
    const def = dashboard({
      building: { type: 'anchor', defaultBinding: areaBinding },
      therm: { type: 'device', scope: { parent: 'building', strategy: 'first' } },
    });
    const base = { building: areaBinding };
    const resolver: MemberResolver = { devicesForAnchor: vi.fn(async () => ['t2', 't1']) };
    const { result } = renderHook(() => useResolvedBindings(def, base, {}, resolver));
    await waitFor(() => expect(result.current.therm).toEqual({ kind: 'device', deviceToken: 't1' }));
    expect(result.current.building).toEqual(areaBinding);
  });
});

// ---- useLocationStream ------------------------------------------------------

function fakeLocationHub() {
  let sink: LocationStreamSink | null = null;
  const unsub = vi.fn();
  const hub = {
    // Only the method the location hook path touches; the others are inert no-ops.
    subscribeWidget: () => () => {},
    subscribeLocations: (_subscription: LocationSubscription, s: LocationStreamSink) => {
      sink = s;
      return unsub;
    },
  } as unknown as DashboardHub;
  return {
    hub,
    unsub,
    push: (snapshot: LocationSnapshot) => act(() => sink?.next(snapshot)),
    fail: (err: unknown) => act(() => sink?.error?.(err)),
  };
}

const locationSub: LocationSubscription = {
  datasource: { kind: 'device', deviceToken: 'dozer-1', measurements: [], location: { series: 'latest' } },
};

const position = (over: Partial<LocationSample> = {}): LocationSample => ({
  id: 'loc-1',
  deviceToken: 'dozer-1',
  latitude: 33.749,
  longitude: -84.388,
  elevation: 320.5,
  accuracy: 4.2,
  speed: 0,
  heading: 271.5,
  occurredTime: '2026-08-09T12:00:00Z',
  ...over,
});

describe('useLocationStream', () => {
  it('starts loading, with no positions and no refusal claimed', () => {
    const f = fakeLocationHub();
    const { result } = renderHook(() => useLocationStream(f.hub, locationSub));
    expect(result.current.loading).toBe(true);
    expect(result.current.locations).toEqual([]);
    expect(result.current.forbidden).toBe(false);
  });

  it('holds the latest snapshot and clears loading', () => {
    const f = fakeLocationHub();
    const { result } = renderHook(() => useLocationStream(f.hub, locationSub));

    f.push({ kind: 'positions', deviceTokens: ['dozer-1'], locations: [position()] });
    expect(result.current.loading).toBe(false);
    expect(result.current.deviceTokens).toEqual(['dozer-1']);
    expect(result.current.locations.map((l) => l.deviceToken)).toEqual(['dozer-1']);
    expect(result.current.forbidden).toBe(false);
  });

  // 🔴 A refusal sets a FLAG, never an error — the widget's permission copy is keyed on
  // it, and an `error` would take it through the "Data unavailable" pane instead.
  it('turns a forbidden snapshot into the flag, not an error', () => {
    const f = fakeLocationHub();
    const { result } = renderHook(() => useLocationStream(f.hub, locationSub));

    f.push({ kind: 'forbidden' });
    expect(result.current.forbidden).toBe(true);
    expect(result.current.error).toBeNull();
    expect(result.current.loading).toBe(false);
  });

  // An authority can be revoked mid-session. Keeping the last-seen coordinates on screen
  // under a permission banner would leave a refused viewer looking at the very data they
  // were just refused.
  it('clears the positions it was holding when a refusal arrives', () => {
    const f = fakeLocationHub();
    const { result } = renderHook(() => useLocationStream(f.hub, locationSub));

    f.push({ kind: 'positions', deviceTokens: ['dozer-1'], locations: [position()] });
    expect(result.current.locations).toHaveLength(1); // the control

    f.push({ kind: 'forbidden' });
    expect(result.current.locations).toEqual([]);
    expect(result.current.deviceTokens).toEqual([]);
  });

  it('surfaces a stream error while keeping the last-known positions', () => {
    const f = fakeLocationHub();
    const { result } = renderHook(() => useLocationStream(f.hub, locationSub));

    f.push({ kind: 'positions', deviceTokens: ['dozer-1'], locations: [position()] });
    f.fail(new Error('boom'));

    expect((result.current.error as Error).message).toBe('boom');
    expect(result.current.locations).toHaveLength(1); // not blanked on one blip
    expect(result.current.forbidden).toBe(false);
  });

  it('disposes on unmount', () => {
    const f = fakeLocationHub();
    const { unmount } = renderHook(() => useLocationStream(f.hub, locationSub));
    unmount();
    expect(f.unsub).toHaveBeenCalledTimes(1);
  });

  it('does not resubscribe for an equal-but-new subscription object', () => {
    const f = fakeLocationHub();
    const { rerender } = renderHook(({ sub }) => useLocationStream(f.hub, sub), {
      initialProps: { sub: { ...locationSub } },
    });
    rerender({ sub: { ...locationSub } });
    expect(f.unsub).not.toHaveBeenCalled();
  });
});
