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
  MeasurementSample,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(cleanup);

import { useAlarmStream, useCommandStream, useDatasourceAvailability, useMeasurementStream } from './hooks';

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
  message: null,
  ...over,
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
      commands: [{ token: 'c-1', name: 'reboot', status: 'SENT', payload: null, responsePayload: null, error: null, queuedTime: null, sentTime: null, deliveredTime: null, respondedTime: null }],
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
});
