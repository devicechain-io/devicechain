// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A paused board through the real ConnectedWidget: the value on screen does not move, the age
// beside it keeps counting from the sample it shows (so nothing looks live), and resuming
// catches up at once. Measurement, alarm and location channels all freeze; the command
// channel (an action surface) does not.

import type {
  AlarmSnapshot,
  AlarmStreamSink,
  DashboardHub,
  DatasourceSelector,
  MeasurementSample,
  TimeRangeState,
  WidgetInstance,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    EChart: ({ option }: { option: any }) => <div data-testid="echart" data-series={JSON.stringify(option.series)} />,
  };
});

import { ConnectedWidget } from './connected-widget';
import { loadAlarmWidgets } from './widgets/alarm-lazy';

const NOW = Date.parse('2026-10-14T12:00:00Z');
const at = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();
const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  document.documentElement.lang = 'en';
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const sample = (value: number, time: string): MeasurementSample => ({
  id: `t-${value}`,
  deviceToken: 'd-1',
  eventType: 0,
  occurredTime: time,
  name: 'temperature',
  value,
  classifier: null,
});

const widget = (type: string, options: Record<string, unknown> = {}, datasource: DatasourceSelector | undefined = ds): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 3 } },
    ...(datasource ? { datasource } : {}),
    options,
  }) as unknown as WidgetInstance;

const live: TimeRangeState = { spanSeconds: 3600, paused: false };
const frozen: TimeRangeState = { spanSeconds: 3600, paused: true, pausedAt: NOW };

function mount(w: WidgetInstance, hub: DashboardHub, range: TimeRangeState) {
  const tree = (r: TimeRangeState) => (
    <WidgetTimeRangeProvider range={r}>
      <ConnectedWidget widget={w} hub={hub} />
    </WidgetTimeRangeProvider>
  );
  const view = render(tree(range));
  return { ...view, setRange: (r: TimeRangeState) => view.rerender(tree(r)) };
}

describe('a paused measurement widget', () => {
  function measurementHub() {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_d: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    return { hub, push: (m: MeasurementSample) => act(() => sink?.next(m)) };
  }

  it('shows no new value while paused, and its age keeps counting from the sample it shows', () => {
    const f = measurementHub();
    const { setRange } = mount(widget('latest-card', { precision: 0 }), f.hub, live);
    f.push(sample(42, at(10)));
    expect(screen.getByText('42')).toBeTruthy();
    expect(screen.getByText(/10 sec/)).toBeTruthy();

    setRange(frozen);
    f.push(sample(99, at(0))); // arrives while frozen
    expect(screen.queryByText('99')).toBeNull();
    expect(screen.getByText('42')).toBeTruthy();

    // Frozen on screen, so it must AGE on screen: 10 s old at the pause, 70 s old a minute later.
    act(() => void vi.advanceTimersByTime(60_000));
    expect(screen.getByText('42')).toBeTruthy();
    expect(screen.getByText(/1 min/)).toBeTruthy();
  });

  it('catches up on resume: the buffered value appears and the age resets to it', () => {
    const f = measurementHub();
    const { setRange } = mount(widget('latest-card', { precision: 0 }), f.hub, live);
    f.push(sample(42, at(10)));
    setRange(frozen);
    f.push(sample(99, at(2)));
    act(() => void vi.advanceTimersByTime(30_000));
    setRange(live);
    expect(screen.getByText('99')).toBeTruthy();
    expect(screen.queryByText('42')).toBeNull();
  });

  it('freezes the chart too, and draws only the span: older samples are not drawn', () => {
    const f = measurementHub();
    const { setRange, container } = mount(widget('timeseries-chart', {}), f.hub, live);
    const points = () => {
      const series = JSON.parse(container.querySelector('[data-testid="echart"]')?.getAttribute('data-series') ?? '[]');
      return series.reduce((n: number, s: { data?: unknown[] }) => n + (s.data?.length ?? 0), 0);
    };
    f.push(sample(1, at(7200))); // outside the hour
    f.push(sample(2, at(60)));
    expect(points()).toBe(1);

    setRange(frozen);
    f.push(sample(3, at(5)));
    expect(points()).toBe(1);
    setRange(live);
    expect(points()).toBe(2);
  });

  it('under a fixed window a measurement widget carries no current-state badge: it shows the window', () => {
    const f = measurementHub();
    const window: TimeRangeState = { spanSeconds: 3600, paused: true, pausedAt: NOW, absolute: { fromMs: NOW - 3_600_000, toMs: NOW } };
    const { container } = mount(widget('latest-card', { precision: 0 }), f.hub, window);
    expect(container.querySelector('[data-current-state-badge]')).toBeNull();
  });

  it('a widget outside any board (no range provided) is unaffected', () => {
    const f = measurementHub();
    render(<ConnectedWidget widget={widget('latest-card', { precision: 0 })} hub={f.hub} />);
    f.push(sample(7, at(1)));
    expect(screen.getByText('7')).toBeTruthy();
  });
});

describe('a paused alarm widget', () => {
  // The alarm components load from a lazy chunk; loaded once here, every mount renders at once.
  beforeAll(() => loadAlarmWidgets());

  function alarmHub() {
    let sink: AlarmStreamSink | null = null;
    const hub = {
      subscribeAlarms: (_s: unknown, k: AlarmStreamSink) => {
        sink = k;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    const snap = (total: number): AlarmSnapshot => ({ alarms: [], total } as unknown as AlarmSnapshot);
    return {
      hub,
      push: (total: number) => act(() => sink?.next(snap(total))),
      fail: () => act(() => sink?.error?.(new Error('socket closed'))),
    };
  }

  // The hub's reconnect path: a query fails while the socket is down, then the socket comes back and
  // re-queries -- the same subscription answering again, with newer rows. Nothing was re-pointed, so
  // the pause holds exactly.
  it('a dropped and re-established stream while paused leaves the held count alone', () => {
    const f = alarmHub();
    const w = widget('alarm-count', { state: 'ACTIVE' }, undefined);
    const { setRange } = mount(w, f.hub, live);
    f.push(3);
    setRange(frozen);
    f.fail();
    f.push(8);
    f.push(9);
    expect(screen.getByText('3')).toBeTruthy();
    expect(screen.queryByText('8')).toBeNull();
    expect(screen.queryByText('9')).toBeNull();
    setRange(live);
    expect(screen.getByText('9')).toBeTruthy();
  });

  it('holds the count on screen while paused and shows the live one on resume', () => {
    const f = alarmHub();
    const w = widget('alarm-count', { state: 'ACTIVE' }, undefined);
    const { setRange } = mount(w, f.hub, live);
    f.push(3);
    expect(screen.getByText('3')).toBeTruthy();

    setRange(frozen);
    f.push(8);
    expect(screen.getByText('3')).toBeTruthy();
    expect(screen.queryByText('8')).toBeNull();

    setRange(live);
    expect(screen.getByText('8')).toBeTruthy();
  });

  it('under a fixed window an alarm widget says it shows the current state; a paused board does not need to', () => {
    const f = alarmHub();
    const w = widget('alarm-count', { state: 'ACTIVE' }, undefined);
    const window: TimeRangeState = {
      spanSeconds: 3600,
      paused: true,
      pausedAt: NOW - 86_400_000,
      absolute: { fromMs: NOW - 86_400_000 - 3_600_000, toMs: NOW - 86_400_000 },
    };
    const { container, setRange } = mount(w, f.hub, window);
    f.push(4);
    expect(screen.getByText('4')).toBeTruthy();
    const badge = container.querySelector('[data-current-state-badge]');
    expect(badge?.textContent).toBe('Now');
    expect(badge?.getAttribute('title')).toMatch(/fixed time window does not apply/);
    setRange(frozen);
    expect(container.querySelector('[data-current-state-badge]')).toBeNull();
    setRange(live);
    expect(container.querySelector('[data-current-state-badge]')).toBeNull();
  });

  it('opening paused freezes on the FIRST loaded snapshot, not on the loading state', () => {
    const f = alarmHub();
    const w = widget('alarm-count', { state: 'ACTIVE' }, undefined);
    const { setRange } = mount(w, f.hub, frozen);
    f.push(5); // the first snapshot to land
    expect(screen.getByText('5')).toBeTruthy();
    f.push(6);
    expect(screen.queryByText('6')).toBeNull();
    setRange(live);
    expect(screen.getByText('6')).toBeTruthy();
  });
});

describe('a slot re-pointed while the board is paused', () => {
  // One alarm stream per subscription, each fed by the test: what the hub would send for that device.
  // `answered` maps a device to the count a source that already holds its snapshot answers with AT
  // ONCE, inside subscribe (a cache, a recording): no loading state ever reaches the widget then.
  function alarmHub(answered: Record<string, number> = {}) {
    const sinks = new Map<string, AlarmStreamSink>();
    const snap = (total: number): AlarmSnapshot => ({ alarms: [], total }) as unknown as AlarmSnapshot;
    const hub = {
      subscribeAlarms: (s: { datasource?: { deviceToken?: string } }, k: AlarmStreamSink) => {
        const device = s.datasource?.deviceToken ?? '';
        sinks.set(device, k);
        if (answered[device] !== undefined) k.next(snap(answered[device]));
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    return { hub, push: (device: string, total: number) => act(() => sinks.get(device)?.next(snap(total))) };
  }
  const on = (device: string) => widget('alarm-count', { state: 'ACTIVE' }, { kind: 'device', deviceToken: device, measurements: [] });
  const mountOn = (hub: DashboardHub) => {
    const tree = (w: WidgetInstance, r: TimeRangeState) => (
      <WidgetTimeRangeProvider range={r}>
        <ConnectedWidget widget={w} hub={hub} />
      </WidgetTimeRangeProvider>
    );
    const view = render(tree(on('hauler-1'), live));
    return { tree, view };
  };

  it('holds the new device’s first snapshot when its source answers at once', () => {
    const f = alarmHub({ 'hauler-2': 8 });
    const { tree, view } = mountOn(f.hub);
    f.push('hauler-1', 3);
    view.rerender(tree(on('hauler-1'), frozen));
    view.rerender(tree(on('hauler-2'), frozen));
    expect(screen.queryByText('3')).toBeNull();
    expect(screen.getByText('8')).toBeTruthy();
  });

  it('never shows the previous device’s alarms under the new one, and holds the new one’s first snapshot', () => {
    const f = alarmHub();
    const { tree, view } = mountOn(f.hub);
    f.push('hauler-1', 3);
    view.rerender(tree(on('hauler-1'), frozen));
    expect(screen.getByText('3')).toBeTruthy();

    // The operator picks hauler-2 while paused: hauler-1's count must go at once.
    view.rerender(tree(on('hauler-2'), frozen));
    expect(screen.queryByText('3')).toBeNull();
    f.push('hauler-2', 8);
    expect(screen.getByText('8')).toBeTruthy();
    // ...and that first snapshot is what the pause now holds.
    f.push('hauler-2', 9);
    expect(screen.getByText('8')).toBeTruthy();
    expect(screen.queryByText('9')).toBeNull();
    view.rerender(tree(on('hauler-2'), live));
    expect(screen.getByText('9')).toBeTruthy();
  });
});

describe('a chart says what span it draws whenever the header would say otherwise', () => {
  function measurementHub() {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_d: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    return {
      hub,
      push: (m: MeasurementSample) => act(() => sink?.next(m)),
      // Many samples in ONE act, so a flood renders once instead of once per sample.
      pushAll: (ms: MeasurementSample[]) => act(() => ms.forEach((m) => sink?.next(m))),
    };
  }
  const badge = () => document.querySelector<HTMLElement>('[data-chart-span]');
  const day: TimeRangeState = { spanSeconds: 86400, paused: false };

  it('a chart PINNED to an hour under a 24h board range is labelled with its own span', () => {
    const f = measurementHub();
    mount(widget('timeseries-chart', { windowSeconds: 3600 }), f.hub, day);
    expect(badge()?.textContent).toBe('1h');
    expect(badge()?.getAttribute('data-chart-span')).toBe('1h');
    expect(badge()?.title).toMatch(/Pinned to 1h/);
  });

  it('the label follows the viewer: it goes when the board range matches the pin, and returns when it moves away', () => {
    const f = measurementHub();
    const { setRange } = mount(widget('timeseries-chart', { windowSeconds: 3600 }), f.hub, live);
    expect(badge()).toBeNull();
    setRange(day);
    expect(badge()?.textContent).toBe('1h');
    setRange({ spanSeconds: 900, paused: false });
    expect(badge()?.textContent).toBe('1h');
  });

  it('a chart that follows the board range carries no label (the header already says it)', () => {
    const f = measurementHub();
    mount(widget('timeseries-chart', {}), f.hub, day);
    expect(badge()).toBeNull();
  });

  it('an unusable pin (under a minute) follows the board, so it carries no label either', () => {
    const f = measurementHub();
    mount(widget('timeseries-chart', { windowSeconds: 5 }), f.hub, day);
    expect(badge()).toBeNull();
  });

  it('a pinned chart outside any board has no header to contradict, so no label', () => {
    const f = measurementHub();
    render(<ConnectedWidget widget={widget('timeseries-chart', { windowSeconds: 3600 })} hub={f.hub} />);
    expect(badge()).toBeNull();
  });

  it('a window the retention ceiling cut is marked with where it starts, never passed off as the whole range', () => {
    const f = measurementHub();
    mount(widget('timeseries-chart', {}), f.hub, { spanSeconds: 604800, paused: false });
    expect(badge()).toBeNull();
    f.pushAll(Array.from({ length: 2010 }, (_, i) => sample(i, at(2010 - i))));
    const b = badge();
    expect(b).not.toBeNull();
    expect(b?.hasAttribute('data-chart-truncated')).toBe(true);
    expect(b?.textContent).toMatch(/^from /);
    expect(b?.title).toMatch(/not the whole 7d/);
  });
});
