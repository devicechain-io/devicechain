// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A board whose live feed was interrupted, through the real renderer, the real stream hook and
// the real chart builder. The readings sent during the outage never arrive on the stream, so the
// only place they exist is stored history: the widget must read it again when the feed resumes,
// and until that read lands it must draw the outage as a gap -- never a straight line from the
// last reading before to the first one after, which is what a device that reported nothing would
// look like. The hub is a fake that hands the widget's sink to the test; the history read is held
// so its timing against the reconnect can be chosen.

import type {
  DashboardDefinition,
  DatasourceSelector,
  HistoryWindow,
  MeasurementSample,
  TimeRangeState,
  WidgetDataSource,
  WidgetInstance,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  fetches: [] as Array<{
    window: HistoryWindow;
    rethrow: boolean;
    resolve: (s: MeasurementSample[]) => void;
    reject: (e: unknown) => void;
  }>,
}));

vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  return {
    ...actual,
    fetchWidgetHistory: (_w: WidgetInstance, window: HistoryWindow, options?: { rethrow?: boolean }) =>
      new Promise<MeasurementSample[]>((resolve, reject) => {
        h.fetches.push({ window, rethrow: options?.rethrow === true, resolve, reject });
      }),
  };
});

vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    EChart: ({ option }: { option: any }) => <div data-testid="echart" data-series={JSON.stringify(option.series)} />,
  };
});

import { DashboardRenderer, RESEED_RETRY_MS } from './dashboard-renderer';

const T0 = Date.parse('2026-10-15T12:00:00Z');
const iso = (ms: number) => new Date(ms).toISOString();
const MIN = 60_000;

const sample = (value: number, at: number): MeasurementSample => ({
  id: `t-${value}-${at}`,
  deviceToken: 'd-1',
  eventType: 0,
  occurredTime: iso(at),
  name: 't',
  value,
  classifier: null,
});

// Stored history as the bucket read returns it: one sample per minute bucket.
const history = (from: number, to: number, value: (at: number) => number): MeasurementSample[] => {
  const out: MeasurementSample[] = [];
  for (let at = from; at <= to; at += MIN) out.push(sample(value(at), at));
  return out;
};

const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['t'] };
const board: DashboardDefinition = {
  schemaVersion: 3,
  title: 'Reconnect',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [
    { id: 'chart', type: 'timeseries-chart', layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } }, datasource: ds, options: {} },
  ] as WidgetInstance[],
};

function fakeHub() {
  const sinks: WidgetStreamSink[] = [];
  const hub: WidgetDataSource = {
    subscribeWidget: (_d, sink) => {
      sinks.push(sink);
      return () => {
        sinks.splice(sinks.indexOf(sink), 1);
      };
    },
    subscribeAlarms: () => () => {},
    subscribeCommands: () => () => {},
    subscribeLocations: () => () => {},
    isDatasourceAvailable: async () => true,
  };
  return {
    hub,
    push: (s: MeasurementSample) => act(() => sinks.forEach((k) => k.next(s))),
    reconnect: () => act(() => sinks.forEach((k) => k.reconnected?.())),
    sinks,
    get subscribed() {
      return sinks.length;
    },
  };
}

const live: TimeRangeState = { spanSeconds: 3600, paused: false };
const flush = () => act(async () => void (await vi.advanceTimersByTimeAsync(0)));

// The chart's one series, as [time ms, value | null] points.
function points(container: HTMLElement): Array<[number, number | null]> {
  const raw = container.querySelector('[data-testid="echart"]')?.getAttribute('data-series') ?? '[]';
  const [series] = JSON.parse(raw) as Array<{ data: Array<[string, number | null]> }>;
  return (series?.data ?? []).map(([t, v]) => [Date.parse(t), v]);
}

// The longest stretch the chart draws as a line with no point in it: a straight line across an
// outage shows up here as a segment as long as the outage.
function longestSegment(pts: Array<[number, number | null]>): number {
  let longest = 0;
  for (let i = 1; i < pts.length; i++) {
    if (pts[i][1] === null || pts[i - 1][1] === null) continue;
    longest = Math.max(longest, pts[i][0] - pts[i - 1][0]);
  }
  return longest;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(T0);
  h.fetches.length = 0;
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

// The scenario every test starts from: a board opened at 12:00 with an hour of history, two live
// readings, and then the feed goes down for five minutes while the device keeps reporting.
async function openAndLoseTheFeed(timeRange: TimeRangeState = live) {
  const f = fakeHub();
  const view = render(<DashboardRenderer definition={board} hub={f.hub} timeRange={timeRange} />);
  await flush();
  const first = h.fetches[h.fetches.length - 1];
  await act(async () => first.resolve(history(T0 - 30 * MIN, T0 - MIN, () => 1)));
  await f.push(sample(10, T0 + 10_000));
  await f.push(sample(11, T0 + 20_000));
  // Down from 12:00:20 to 12:05:20. Nothing reaches the board meanwhile.
  vi.setSystemTime(T0 + 5 * MIN + 20_000);
  return { f, view };
}

// What the store holds after the outage: the hour before, and a reading a minute through it.
const storedAfterOutage = () => history(T0 - 30 * MIN, T0 + 5 * MIN, (at) => (at > T0 ? 50 : 1));

describe('a line chart after its live feed was interrupted', () => {
  it('reads its history again and draws the outage from it, not as a straight line', async () => {
    const { f, view } = await openAndLoseTheFeed();
    await f.reconnect();
    await flush();
    expect(h.fetches).toHaveLength(2);
    await act(async () => h.fetches[1].resolve(storedAfterOutage()));
    await f.push(sample(20, T0 + 5 * MIN + 30_000));

    const pts = points(view.container);
    // The readings sent during the outage are on the chart...
    expect(pts.filter(([t, v]) => t > T0 + 20_000 && t < T0 + 5 * MIN + 20_000 && v === 50).length).toBeGreaterThanOrEqual(4);
    // ...nothing is drawn across it as one stretch of line...
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
    // ...and the live readings from before it, which the history now covers, are not drawn twice.
    expect(pts.some(([, v]) => v === 10 || v === 11)).toBe(false);
    expect(pts[pts.length - 1]).toEqual([T0 + 5 * MIN + 30_000, 20]);
  });

  it('draws a gap across the outage while the history is still being read', async () => {
    const { f, view } = await openAndLoseTheFeed();
    await f.reconnect();
    await f.push(sample(20, T0 + 5 * MIN + 30_000));

    const pts = points(view.container);
    // Not one line from the last reading before the outage to the first after it.
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
    const gap = pts.findIndex(([, v]) => v === null);
    expect(gap).toBeGreaterThan(0);
    expect(pts[gap - 1]).toEqual([T0 + 20_000, 11]);
    expect(pts[gap + 1]).toEqual([T0 + 5 * MIN + 30_000, 20]);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
  });

  it('keeps the gap and what it held when the re-read fails, and tries again later', async () => {
    const { f, view } = await openAndLoseTheFeed();
    await f.reconnect();
    await flush();
    expect(h.fetches[1].rethrow).toBe(true);
    await act(async () => h.fetches[1].reject(new Error('event-management unreachable')));
    await f.push(sample(20, T0 + 5 * MIN + 30_000));

    const pts = points(view.container);
    expect(pts.some(([, v]) => v === null)).toBe(true);
    expect(pts.filter(([, v]) => v === 1).length).toBe(30);
    expect(pts.some(([, v]) => v === 11)).toBe(true);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);

    await act(async () => void (await vi.advanceTimersByTimeAsync(RESEED_RETRY_MS)));
    expect(h.fetches).toHaveLength(3);
    await act(async () => h.fetches[2].resolve(storedAfterOutage()));
    const after = points(view.container);
    expect(after.some(([, v]) => v === null)).toBe(false);
    expect(after.filter(([, v]) => v === 50).length).toBeGreaterThanOrEqual(4);
  });
});

describe('the re-read racing the feed', () => {
  it('a reconnect while the first history read is in flight: the stale answer is not taken as covering the outage', async () => {
    const f = fakeHub();
    const view = render(<DashboardRenderer definition={board} hub={f.hub} timeRange={live} />);
    await flush();
    await f.push(sample(10, T0 + 10_000));
    vi.setSystemTime(T0 + 5 * MIN);
    await f.reconnect();
    await flush();
    expect(h.fetches).toHaveLength(2);
    await f.push(sample(20, T0 + 5 * MIN + 10_000));

    // The read asked for before the outage lands last-but-not-least: it is discarded.
    await act(async () => h.fetches[0].resolve(history(T0 - 30 * MIN, T0 - MIN, () => 1)));
    let pts = points(view.container);
    expect(pts.some(([, v]) => v === 1)).toBe(false);
    expect(pts.some(([, v]) => v === null)).toBe(true);

    await act(async () => h.fetches[1].resolve(storedAfterOutage()));
    pts = points(view.container);
    expect(pts.some(([, v]) => v === null)).toBe(false);
    expect(pts.filter(([, v]) => v === 50).length).toBeGreaterThanOrEqual(4);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
  });

  it('a read asked for before the outage that lands as the feed resumes is kept, but does not close the gap', async () => {
    const f = fakeHub();
    const view = render(<DashboardRenderer definition={board} hub={f.hub} timeRange={live} />);
    await flush();
    await f.push(sample(10, T0 + 10_000));
    vi.setSystemTime(T0 + 5 * MIN);
    // The first read answers in the same turn the feed resumes, before the re-read is asked for:
    // it is still the current ask, so it is taken -- as history up to when it was asked.
    await act(async () => {
      f.sinks.forEach((k) => k.reconnected?.());
      h.fetches[0].resolve(history(T0 - 30 * MIN, T0 - MIN, () => 1));
      await Promise.resolve();
    });
    await f.push(sample(20, T0 + 5 * MIN + 10_000));
    const pts = points(view.container);
    expect(pts.filter(([, v]) => v === 1)).toHaveLength(30);
    expect(pts.some(([, v]) => v === null)).toBe(true);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
  });

  it('two reconnects back to back: only the read asked for after the second is taken', async () => {
    const { f, view } = await openAndLoseTheFeed();
    await f.reconnect();
    await flush();
    await f.push(sample(30, T0 + 5 * MIN + 25_000));
    vi.setSystemTime(T0 + 8 * MIN);
    await f.reconnect();
    await flush();
    expect(h.fetches).toHaveLength(3);
    await f.push(sample(40, T0 + 8 * MIN + 10_000));

    // The first re-read lands after the second reconnect: it cannot cover the second outage.
    await act(async () => h.fetches[1].resolve(storedAfterOutage()));
    let pts = points(view.container);
    expect(pts.some(([, v]) => v === null)).toBe(true);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);

    await act(async () => h.fetches[2].resolve(history(T0 - 30 * MIN, T0 + 8 * MIN, (at) => (at > T0 ? 60 : 1))));
    pts = points(view.container);
    expect(pts.some(([, v]) => v === null)).toBe(false);
    expect(pts.filter(([, v]) => v === 60).length).toBeGreaterThanOrEqual(7);
    expect(pts[pts.length - 1]).toEqual([T0 + 8 * MIN + 10_000, 40]);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
  });

  it('a failed re-read that a newer one superseded is not tried again', async () => {
    const { f } = await openAndLoseTheFeed();
    await f.reconnect();
    await flush();
    await f.reconnect();
    await flush();
    expect(h.fetches).toHaveLength(3);
    await act(async () => h.fetches[1].reject(new Error('down')));
    await act(async () => h.fetches[2].resolve(storedAfterOutage()));
    await act(async () => void (await vi.advanceTimersByTimeAsync(RESEED_RETRY_MS * 2)));
    expect(h.fetches).toHaveLength(3);
  });

  it('unmounting while the re-read is in flight, or waiting to be retried, does nothing after', async () => {
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      const { f, view } = await openAndLoseTheFeed();
      await f.reconnect();
      await flush();
      view.unmount();
      expect(f.subscribed).toBe(0);
      await act(async () => h.fetches[1].resolve(storedAfterOutage()));

      vi.setSystemTime(T0);
      const again = await openAndLoseTheFeed();
      await again.f.reconnect();
      await flush();
      const asked = h.fetches.length;
      await act(async () => h.fetches[asked - 1].reject(new Error('down')));
      again.view.unmount();
      await act(async () => void (await vi.advanceTimersByTimeAsync(RESEED_RETRY_MS * 3)));
      expect(h.fetches).toHaveLength(asked);
      expect(errors).not.toHaveBeenCalled();
    } finally {
      errors.mockRestore();
    }
  });

  it('a frozen board waits to resume before it reads the outage back', async () => {
    const paused: TimeRangeState = { spanSeconds: 3600, paused: true, pausedAt: T0 + 20_000 };
    const f = fakeHub();
    const view = render(<DashboardRenderer definition={board} hub={f.hub} timeRange={live} />);
    await flush();
    await act(async () => h.fetches[0].resolve(history(T0 - 30 * MIN, T0 - MIN, () => 1)));
    await f.push(sample(11, T0 + 20_000));
    view.rerender(<DashboardRenderer definition={board} hub={f.hub} timeRange={paused} />);
    vi.setSystemTime(T0 + 5 * MIN);
    await f.reconnect();
    await flush();
    expect(h.fetches).toHaveLength(1);

    view.rerender(<DashboardRenderer definition={board} hub={f.hub} timeRange={live} />);
    await flush();
    expect(h.fetches).toHaveLength(2);
    expect(Date.parse(h.fetches[1].window.endTime)).toBeGreaterThanOrEqual(T0 + 5 * MIN);
  });
});

describe('a line chart with no stored history to read back', () => {
  // A chart over several devices (an anchor, a device list) has no stored history to read: the
  // read would answer with nothing at once. That empty answer is not history across the outage,
  // so it must not replace what the chart drew before it -- the outage stays a gap.
  const fleet: DatasourceSelector = { kind: 'devices', deviceTokens: ['d-1', 'd-2'], measurements: ['t'] };
  const fleetBoard: DashboardDefinition = { ...board, widgets: [{ ...board.widgets[0], datasource: fleet } as WidgetInstance] };

  it('keeps the readings from before the outage and draws the outage as a gap', async () => {
    const f = fakeHub();
    const view = render(<DashboardRenderer definition={fleetBoard} hub={f.hub} timeRange={live} />);
    await flush();
    await f.push(sample(10, T0 + 10_000));
    await f.push(sample(11, T0 + 20_000));
    vi.setSystemTime(T0 + 5 * MIN + 20_000);
    await f.reconnect();
    await flush();
    // Whatever a read of this selector would answer, it is not taken as covering the outage.
    for (const read of h.fetches) await act(async () => read.resolve([]));
    await f.push(sample(20, T0 + 5 * MIN + 30_000));

    const pts = points(view.container);
    expect(pts.filter(([, v]) => v === 10 || v === 11)).toHaveLength(2);
    expect(pts.some(([, v]) => v === null)).toBe(true);
    expect(longestSegment(pts)).toBeLessThanOrEqual(2 * MIN);
  });
});

describe('a frozen board whose span changes after a reconnect', () => {
  it('reads the outage back once it resumes: the read made at the freeze instant cannot cover it', async () => {
    const paused: TimeRangeState = { spanSeconds: 3600, paused: true, pausedAt: T0 + 20_000 };
    const f = fakeHub();
    const view = render(<DashboardRenderer definition={board} hub={f.hub} timeRange={live} />);
    await flush();
    await act(async () => h.fetches[0].resolve(history(T0 - 30 * MIN, T0 - MIN, () => 1)));
    await f.push(sample(11, T0 + 20_000));
    view.rerender(<DashboardRenderer definition={board} hub={f.hub} timeRange={paused} />);
    vi.setSystemTime(T0 + 5 * MIN);
    await f.reconnect();
    await flush();
    // A new span while still frozen: read at the freeze instant, before the outage.
    view.rerender(<DashboardRenderer definition={board} hub={f.hub} timeRange={{ ...paused, spanSeconds: 6 * 3600 }} />);
    await flush();
    await act(async () => h.fetches[h.fetches.length - 1].resolve(history(T0 - 30 * MIN, T0, () => 1)));
    const asked = h.fetches.length;

    view.rerender(<DashboardRenderer definition={board} hub={f.hub} timeRange={{ spanSeconds: 6 * 3600, paused: false }} />);
    await flush();
    expect(h.fetches).toHaveLength(asked + 1);
    expect(Date.parse(h.fetches[asked].window.endTime)).toBeGreaterThanOrEqual(T0 + 5 * MIN);
    await act(async () => h.fetches[asked].resolve(storedAfterOutage()));
    expect(points(view.container).some(([, v]) => v === null)).toBe(false);
  });
});
