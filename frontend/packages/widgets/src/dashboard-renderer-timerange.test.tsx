// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The window the renderer asks the backend for, per widget, as the board's time range moves:
// the span of a time-ranged widget, the fixed hour of one that is not, a pinned span, a change of
// range, and a frozen board. The widget itself is a recorder; the history fetch is held and
// recorded so the arguments it was called with can be read.

import type {
  DashboardDefinition,
  HistoryWindow,
  MeasurementSample,
  TimeRangeState,
  WidgetDataSource,
  WidgetInstance,
} from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  fetches: [] as Array<{ widgetId: string; window: HistoryWindow; resolve: (s: MeasurementSample[]) => void }>,
  renders: [] as Array<{ widgetId: string; seeded: number | undefined }>,
}));

vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  return {
    ...actual,
    fetchWidgetHistory: (widget: WidgetInstance, window: HistoryWindow) =>
      new Promise<MeasurementSample[]>((resolve) => {
        h.fetches.push({ widgetId: widget.id, window, resolve });
      }),
  };
});

vi.mock('./connected-widget', () => ({
  ConnectedWidget: ({ widget, initialSamples }: { widget: WidgetInstance; initialSamples?: MeasurementSample[] }) => {
    h.renders.push({ widgetId: widget.id, seeded: initialSamples?.length });
    return null;
  },
}));

import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);
beforeEach(() => {
  h.fetches.length = 0;
  h.renders.length = 0;
});

const source: WidgetDataSource = {
  subscribeWidget: () => () => {},
  subscribeAlarms: () => () => {},
  subscribeCommands: () => () => {},
  subscribeLocations: () => () => {},
  isDatasourceAvailable: async () => true,
};

const ds = { kind: 'device' as const, deviceToken: 'd-1', measurements: ['t'] };
const at = (row: number) => ({ base: { mode: 'grid' as const, col: 0, row, cols: 4, rows: 2 } });
const board = (over: Partial<DashboardDefinition> = {}, chartOptions: Record<string, unknown> = {}): DashboardDefinition => ({
  schemaVersion: 3,
  title: 'Range',
  canvas: { grid: { columns: 12, minCellSize: 56, gutter: 8, padding: 0 }, sizing: 'fill', breakpoints: { base: 0 } },
  widgets: [
    { id: 'chart', type: 'timeseries-chart', layout: at(0), datasource: ds, options: chartOptions },
    { id: 'card', type: 'latest-card', layout: at(2), datasource: ds },
  ] as WidgetInstance[],
  ...over,
});

const range = (spanSeconds: number, extra: Partial<TimeRangeState> = {}): TimeRangeState => ({ spanSeconds, paused: false, ...extra });
const flush = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const fetchesOf = (id: string) => h.fetches.filter((f) => f.widgetId === id);
const lastChartRender = () => {
  const all = h.renders.filter((r) => r.widgetId === 'chart');
  return all[all.length - 1];
};
const spanOf = (w: HistoryWindow) => (Date.parse(w.endTime) - Date.parse(w.startTime)) / 1000;

describe('the history window each widget asks for', () => {
  it('a time-ranged chart seeds the board span, bucketed so the read stays bounded', async () => {
    render(<DashboardRenderer definition={board()} hub={source} timeRange={range(7 * 86400)} />);
    await flush();
    const [chart] = fetchesOf('chart');
    expect(spanOf(chart.window)).toBe(7 * 86400);
    expect(chart.window.intervalSeconds).toBe(2520);
    // from = now - span: the window ends at the moment of the request, not at some earlier instant.
    expect(Math.abs(Date.parse(chart.window.endTime) - Date.now())).toBeLessThan(5000);
  });

  it('a widget that is not time-ranged keeps the fixed hour whatever the board says', async () => {
    render(<DashboardRenderer definition={board()} hub={source} timeRange={range(7 * 86400)} />);
    await flush();
    const [card] = fetchesOf('card');
    expect(spanOf(card.window)).toBe(3600);
    expect(card.window.intervalSeconds).toBe(60);
  });

  it('a chart that pins its own span ignores the board range', async () => {
    render(<DashboardRenderer definition={board({}, { windowSeconds: 300 })} hub={source} timeRange={range(86400)} />);
    await flush();
    expect(spanOf(fetchesOf('chart')[0].window)).toBe(300);
  });

  it('with no range supplied, follows the definition default, else the hour', async () => {
    const { unmount } = render(<DashboardRenderer definition={board({ timeRange: { defaultSeconds: 21600 } })} hub={source} />);
    await flush();
    expect(spanOf(fetchesOf('chart')[0].window)).toBe(21600);
    unmount();
    h.fetches.length = 0;
    render(<DashboardRenderer definition={board()} hub={source} />);
    await flush();
    expect(spanOf(fetchesOf('chart')[0].window)).toBe(3600);
  });
});

describe('a change of range', () => {
  it('re-fetches the time-ranged chart at the new span and leaves the fixed-hour card alone', async () => {
    const def = board();
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={range(3600)} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(1);
    expect(fetchesOf('card')).toHaveLength(1);

    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(86400)} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(2);
    expect(spanOf(fetchesOf('chart')[1].window)).toBe(86400);
    expect(fetchesOf('card')).toHaveLength(1);
  });

  it('keeps the seed on screen while the new span loads (same device, so no blank flash)', async () => {
    const def = board();
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={range(3600)} />);
    await flush();
    await act(async () => fetchesOf('chart')[0].resolve([{ id: 's', deviceToken: 'd-1', eventType: 0, occurredTime: null, name: 't', value: 1, classifier: null }]));
    expect(lastChartRender()?.seeded).toBe(1);

    const before = h.renders.length;
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(86400)} />);
    await flush();
    const during = h.renders.slice(before).filter((r) => r.widgetId === 'chart');
    expect(during.length).toBeGreaterThan(0);
    for (const r of during) expect(r.seeded).toBe(1);
  });

  it('drops a slow answer for the span it replaced', async () => {
    const def = board();
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={range(3600)} />);
    await flush();
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(86400)} />);
    await flush();
    const [stale, fresh] = fetchesOf('chart');
    const mk = (v: number): MeasurementSample => ({ id: `s${v}`, deviceToken: 'd-1', eventType: 0, occurredTime: null, name: 't', value: v, classifier: null });
    await act(async () => fresh.resolve([mk(2), mk(3)]));
    await act(async () => stale.resolve([mk(1)]));
    expect(lastChartRender()?.seeded).toBe(2);
  });
});

describe('a frozen board', () => {
  it('reads up to the freeze instant, not up to now', async () => {
    const pausedAt = Date.now() - 600_000;
    render(<DashboardRenderer definition={board()} hub={source} timeRange={range(3600, { paused: true, pausedAt })} />);
    await flush();
    const [chart] = fetchesOf('chart');
    expect(Date.parse(chart.window.endTime)).toBe(pausedAt);
    expect(spanOf(chart.window)).toBe(3600);
  });

  it('a range change while frozen re-reads the same moment at the new span', async () => {
    const pausedAt = Date.now() - 600_000;
    const def = board();
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={range(3600, { paused: true, pausedAt })} />);
    await flush();
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(21600, { paused: true, pausedAt })} />);
    await flush();
    const second = fetchesOf('chart')[1];
    expect(Date.parse(second.window.endTime)).toBe(pausedAt);
    expect(spanOf(second.window)).toBe(21600);
  });

  it('a fixed window re-reads at the window, even one of the same length as the live span', async () => {
    const def = board();
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={range(3600)} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(1);
    const toMs = Math.floor((Date.now() - 86_400_000) / 1000) * 1000;
    const fixed = (to: number) => range(3600, { paused: true, pausedAt: to, absolute: { fromMs: to - 3_600_000, toMs: to } });

    rerender(<DashboardRenderer definition={def} hub={source} timeRange={fixed(toMs)} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(2);
    expect(Date.parse(fetchesOf('chart')[1].window.endTime)).toBe(toMs);
    expect(spanOf(fetchesOf('chart')[1].window)).toBe(3600);
    // The card is not time-ranged: its fixed hour ends at the window's end, so it shows the value AS OF then.
    expect(fetchesOf('card')).toHaveLength(2);
    expect(Date.parse(fetchesOf('card')[1].window.endTime)).toBe(toMs);

    // Another window of the same length is another read.
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={fixed(toMs - 7_200_000)} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(3);
    expect(Date.parse(fetchesOf('chart')[2].window.endTime)).toBe(toMs - 7_200_000);

    // Back to live reads up to now again.
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(3600)} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(4);
    expect(Math.abs(Date.parse(fetchesOf('chart')[3].window.endTime) - Date.now())).toBeLessThan(5000);
  });

  it('never hands a seed read for the live board to a fixed window, nor the other way round', async () => {
    const def = board();
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={range(3600)} />);
    await flush();
    await act(async () => fetchesOf('chart')[0].resolve([{ id: 's', deviceToken: 'd-1', eventType: 0, occurredTime: null, name: 't', value: 1, classifier: null }]));
    expect(lastChartRender()?.seeded).toBe(1);
    const to = Math.floor((Date.now() - 86_400_000) / 1000) * 1000;
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(3600, { paused: true, pausedAt: to, absolute: { fromMs: to - 3_600_000, toMs: to } })} />);
    await flush();
    expect(lastChartRender()?.seeded).toBeUndefined();
  });

  it('does not re-fetch on pause or resume: the stream kept buffering, so there is no gap to read', async () => {
    const def = board();
    const live = range(3600);
    const { rerender } = render(<DashboardRenderer definition={def} hub={source} timeRange={live} />);
    await flush();
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={range(3600, { paused: true, pausedAt: Date.now() })} />);
    rerender(<DashboardRenderer definition={def} hub={source} timeRange={live} />);
    await flush();
    expect(fetchesOf('chart')).toHaveLength(1);
  });

  it('says so on the board root, for a test, a style or a screenshot to find', () => {
    const { container, rerender } = render(<DashboardRenderer definition={board()} hub={source} timeRange={range(3600, { paused: true, pausedAt: 1 })} />);
    expect(container.querySelector('[data-time-paused]')).not.toBeNull();
    rerender(<DashboardRenderer definition={board()} hub={source} timeRange={range(3600)} />);
    expect(container.querySelector('[data-time-paused]')).toBeNull();
  });
});
