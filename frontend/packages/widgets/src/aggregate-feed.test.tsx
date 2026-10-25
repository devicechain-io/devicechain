// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The aggregate channel through the real ConnectedWidget: the span a stat card reads (the board's,
// or its own pin), that a paused board reads one frozen window and never again, that a live one
// keeps reading, and that every way of having no figure is an explained state.

import type {
  DashboardHub,
  DatasourceSelector,
  HistoryWindow,
  MeasurementBucket,
  MeasurementSample,
  TimeRangeState,
  WidgetInstance,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { aggregateRefreshMs } from './aggregate-fold';
import { ConnectedWidget } from './connected-widget';
import { loadAggregateWidgets } from './widgets/aggregate-lazy';

const NOW = Date.parse('2026-10-14T12:00:00Z');
// How often a live card over the default hour reads again: a minute.
const AGGREGATE_REFRESH_MS = aggregateRefreshMs(3600);
const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };

// The stat card and the feed both arrive as chunks, and a dynamic import resolves on the module
// runner's own timers, which the fake clock below would never advance. So both are loaded ONCE,
// here, on the real clock: the card's chunk by loading it, the feed's by mounting a card and
// waiting for it. React.lazy then holds the resolved module, and every mount below is synchronous.
beforeAll(async () => {
  await loadAggregateWidgets();
  const hub = { subscribeWidget: () => () => {}, isDatasourceAvailable: async () => true } as unknown as DashboardHub;
  render(<ConnectedWidget widget={widget()} hub={hub} />);
  await screen.findByText('This data source cannot compute aggregates', {}, { timeout: 20_000 });
  cleanup();
}, 30_000);
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  document.documentElement.lang = 'en';
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const bucket = (over: Partial<MeasurementBucket> = {}): MeasurementBucket => ({
  bucketStart: '2026-10-14T11:00:00Z',
  name: 'temperature',
  avg: 20,
  min: 10,
  max: 40,
  sum: 400,
  count: 20,
  ...over,
});

const widget = (options: Record<string, unknown> = {}, datasource: DatasourceSelector | null = ds): WidgetInstance =>
  ({
    id: 'w',
    type: 'stat-card',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    ...(datasource ? { datasource } : {}),
    options: { stat: 'max', ...options },
  }) as unknown as WidgetInstance;

interface Hub {
  hub: DashboardHub;
  read: ReturnType<typeof vi.fn>;
  push: (m: MeasurementSample) => void;
}

function makeHub(read: (d: DatasourceSelector, w: HistoryWindow) => Promise<MeasurementBucket[]> | MeasurementBucket[]): Hub {
  let sink: WidgetStreamSink | null = null;
  const fn = vi.fn(async (d: DatasourceSelector, w: HistoryWindow) => read(d, w));
  const hub = {
    subscribeWidget: (_d: DatasourceSelector, s: WidgetStreamSink) => {
      sink = s;
      return () => {};
    },
    isDatasourceAvailable: async () => true,
    bucketMeasurements: fn,
  } as unknown as DashboardHub;
  return { hub, read: fn, push: (m) => act(() => sink?.next(m)) };
}

const live = (spanSeconds = 3600): TimeRangeState => ({ spanSeconds, paused: false });
const frozenAt = (pausedAt: number, spanSeconds = 3600): TimeRangeState => ({ spanSeconds, paused: true, pausedAt });

function mount(w: WidgetInstance, hub: DashboardHub, range: TimeRangeState | undefined) {
  const tree = (r: TimeRangeState | undefined) => (
    <WidgetTimeRangeProvider range={r}>
      <ConnectedWidget widget={w} hub={hub} />
    </WidgetTimeRangeProvider>
  );
  const view = render(tree(range));
  return { ...view, setRange: (r: TimeRangeState | undefined) => view.rerender(tree(r)) };
}

// Let the feed's read, and the state it sets, land.
const settle = () => act(async () => void (await vi.advanceTimersByTimeAsync(0)));
const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

const figure = () => document.querySelector('[data-level-readout]')?.textContent;
const caption = () => document.querySelector('[data-stat-caption]')?.textContent;
// A widget's own sentence, else the explained state's explanation (data-empty-state).
const empty = () => (document.querySelector('[data-level-empty]') ?? document.querySelector('[data-empty-detail]'))?.textContent;

describe('the span a stat card reads', () => {
  it('is the board range: a window of that span ending at the last minute, read as one bucket', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, live(3600));
    await settle();
    expect(f.read).toHaveBeenCalledTimes(1);
    const [selector, window] = f.read.mock.calls[0];
    expect(selector).toEqual(ds);
    // The store's end is inclusive, so the window ends the instant before its minute boundary.
    expect(window).toEqual({
      startTime: new Date(NOW - 3600_000).toISOString(),
      endTime: new Date(NOW - 1).toISOString(),
      intervalSeconds: 3600,
    });
    expect(figure()).toBe('40');
    expect(caption()).toBe('Maximum · 1h');
  });

  it('follows the viewer: a new range reads again over the new span, and says so', async () => {
    const f = makeHub(() => [bucket()]);
    const { setRange } = mount(widget(), f.hub, live(3600));
    await settle();
    setRange(live(86400));
    await settle();
    expect(f.read).toHaveBeenCalledTimes(2);
    const window = f.read.mock.calls[1][1];
    expect(Date.parse(window.endTime) + 1 - Date.parse(window.startTime)).toBe(86400_000);
    expect(window.intervalSeconds).toBe(86400); // the totals only: one bucket, not a chart's worth
    expect(caption()).toBe('Maximum · 1d');
  });

  it('is its own pin when it has one, whatever the viewer picks', async () => {
    const f = makeHub(() => [bucket()]);
    const { setRange } = mount(widget({ windowSeconds: 300 }), f.hub, live(86400));
    await settle();
    expect(caption()).toBe('Maximum · 5m');
    const window = f.read.mock.calls[0][1];
    expect(Date.parse(window.endTime) + 1 - Date.parse(window.startTime)).toBe(300_000);
    setRange(live(3600));
    await settle();
    expect(f.read).toHaveBeenCalledTimes(1); // nothing about this card changed
    expect(caption()).toBe('Maximum · 5m');
  });

  it('ignores a pin that is not a usable span, and follows the board', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget({ windowSeconds: 5 }), f.hub, live(900));
    await settle();
    expect(caption()).toBe('Maximum · 15m');
  });

  it('is an hour outside any board, which is the span every chart seeded with before it was configurable', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, undefined);
    await settle();
    expect(caption()).toBe('Maximum · 1h');
  });

  it('never shows the figure of one span under the label of another', async () => {
    let release: (b: MeasurementBucket[]) => void = () => {};
    const slow = new Promise<MeasurementBucket[]>((r) => (release = r));
    let call = 0;
    const f = makeHub(() => (call++ === 0 ? [bucket({ max: 11 })] : slow));
    const { setRange } = mount(widget(), f.hub, live(3600));
    await settle();
    expect(figure()).toBe('11');
    setRange(live(86400));
    await settle();
    // The 1h figure is gone the moment the span changes; the card is waiting for the 24h one.
    expect(figure()).toBeUndefined();
    expect(empty()).toBe('Loading');
    await act(async () => release([bucket({ max: 99 })]));
    expect(figure()).toBe('99');
    expect(caption()).toBe('Maximum · 1d');
  });

  it('discards a slow answer for a span the viewer has already left', async () => {
    let releaseOld: (b: MeasurementBucket[]) => void = () => {};
    const old = new Promise<MeasurementBucket[]>((r) => (releaseOld = r));
    let call = 0;
    const f = makeHub(() => (call++ === 0 ? old : [bucket({ max: 24 })]));
    const { setRange } = mount(widget(), f.hub, live(3600));
    setRange(live(86400));
    await settle();
    expect(figure()).toBe('24');
    await act(async () => releaseOld([bucket({ max: 1 })]));
    expect(figure()).toBe('24');
    expect(caption()).toBe('Maximum · 1d');
  });
});

describe('a live stat card', () => {
  it('reads again every refresh interval, and shows the new figure', async () => {
    let max = 40;
    const f = makeHub(() => [bucket({ max })]);
    mount(widget(), f.hub, live());
    await settle();
    expect(figure()).toBe('40');
    max = 55;
    await advance(AGGREGATE_REFRESH_MS);
    expect(f.read).toHaveBeenCalledTimes(2);
    expect(figure()).toBe('55');
    await advance(AGGREGATE_REFRESH_MS);
    expect(f.read).toHaveBeenCalledTimes(3);
  });

  it('moves the window forward with the clock', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, live());
    await settle();
    await advance(AGGREGATE_REFRESH_MS);
    const second = f.read.mock.calls[1][1];
    expect(second.endTime).toBe(new Date(NOW + AGGREGATE_REFRESH_MS - 1).toISOString());
  });

  it('stops reading when it unmounts', async () => {
    const f = makeHub(() => [bucket()]);
    const view = mount(widget(), f.hub, live());
    await settle();
    view.unmount();
    await advance(AGGREGATE_REFRESH_MS * 3);
    expect(f.read).toHaveBeenCalledTimes(1);
  });
});

describe('the window a stat card reads', () => {
  const at = (ms: number) => new Date(ms).toISOString();

  it('starts and ends on whole minutes, so no summary from before the span is read', async () => {
    vi.setSystemTime(NOW + 30_000);
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, live(3600));
    await settle();
    const window = f.read.mock.calls[0][1];
    // The minute in progress is not read: its summary would be read whole, and the hour before it
    // starts on the boundary, so the window is exactly an hour.
    expect(window.startTime).toBe(at(NOW - 3600_000));
    expect(window.endTime).toBe(at(NOW - 1));
    expect(caption()).toBe('Maximum · 1h');
  });

  it('covers less, never more, when its span is not whole minutes, and says what it covers', async () => {
    vi.setSystemTime(NOW + 30_000);
    const f = makeHub(() => [bucket()]);
    mount(widget({ windowSeconds: 90 }), f.hub, live());
    await settle();
    const window = f.read.mock.calls[0][1];
    expect(window.startTime).toBe(at(NOW - 60_000));
    expect(window.endTime).toBe(at(NOW - 1));
    expect(caption()).toBe('Maximum · 1m');
  });

  it('a week is read again once a chart bucket (42 minutes), not every few seconds', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, live(604800));
    await settle();
    expect(aggregateRefreshMs(604800)).toBe(42 * 60_000);
    await advance(15_000);
    expect(f.read).toHaveBeenCalledTimes(1);
    // 12:00 is not on a 42-minute boundary: the next read is on the next one, whichever card asks.
    const next = Math.ceil((NOW + 1) / (42 * 60_000)) * 42 * 60_000;
    await advance(next - NOW - 15_000 - 1);
    expect(f.read).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(f.read).toHaveBeenCalledTimes(2);
    expect(f.read.mock.calls[1][1].endTime).toBe(at(next - 1));
  });

  it('cards mounted at different moments read again at the same moment, asking the same question', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, live());
    await settle();
    await advance(25_000);
    mount(widget({ stat: 'min' }), f.hub, live());
    await settle();
    expect(f.read).toHaveBeenCalledTimes(2);
    await advance(35_000);
    expect(f.read).toHaveBeenCalledTimes(4);
    expect(f.read.mock.calls[2][1]).toEqual(f.read.mock.calls[3][1]);
  });

  it('does not read while the tab is hidden, and reads once when it is shown again', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, live());
    await settle();
    let hidden = true;
    const spy = vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden);
    try {
      await advance(AGGREGATE_REFRESH_MS * 5);
      expect(f.read).toHaveBeenCalledTimes(1);
      hidden = false;
      await act(async () => void document.dispatchEvent(new Event('visibilitychange')));
      expect(f.read).toHaveBeenCalledTimes(2);
      await advance(AGGREGATE_REFRESH_MS);
      expect(f.read).toHaveBeenCalledTimes(3);
    } finally {
      spy.mockRestore();
    }
  });
});

describe('a paused board', () => {
  const pausedAt = NOW - 10 * 60_000;

  it('reads the window that ENDS at the freeze instant, not now', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget(), f.hub, frozenAt(pausedAt));
    await settle();
    const window = f.read.mock.calls[0][1];
    expect(window.endTime).toBe(new Date(pausedAt - 1).toISOString());
    expect(window.startTime).toBe(new Date(pausedAt - 3600_000).toISOString());
  });

  it('reads it once and never again: the figure on screen does not move', async () => {
    let max = 40;
    const f = makeHub(() => [bucket({ max })]);
    mount(widget(), f.hub, frozenAt(pausedAt));
    await settle();
    max = 99;
    await advance(AGGREGATE_REFRESH_MS * 4);
    expect(f.read).toHaveBeenCalledTimes(1);
    expect(figure()).toBe('40');
  });

  it('freezes a live card: pausing stops its polling, resuming starts it again', async () => {
    let max = 40;
    const f = makeHub(() => [bucket({ max })]);
    const { setRange } = mount(widget(), f.hub, live());
    await settle();
    setRange(frozenAt(NOW));
    await settle();
    const afterPause = f.read.mock.calls.length;
    max = 77;
    await advance(AGGREGATE_REFRESH_MS * 3);
    expect(f.read).toHaveBeenCalledTimes(afterPause);
    expect(figure()).toBe('40');
    setRange(live());
    await settle();
    expect(figure()).toBe('77');
  });

  it('holds the latest reading too: a reading that arrives while frozen is not shown', async () => {
    const f = makeHub(() => [bucket()]);
    const { setRange } = mount(widget({ stat: 'last' }), f.hub, live());
    await settle();
    f.push({ id: '1', deviceToken: 'd-1', eventType: 0, occurredTime: new Date(NOW - 5000).toISOString(), name: 'temperature', value: 21, classifier: null });
    expect(figure()).toBe('21');
    setRange(frozenAt(NOW));
    f.push({ id: '2', deviceToken: 'd-1', eventType: 0, occurredTime: new Date(NOW).toISOString(), name: 'temperature', value: 30, classifier: null });
    expect(figure()).toBe('21');
    setRange(live());
    expect(figure()).toBe('30');
  });
});

describe('a stat card with nothing to show, says why', () => {
  it('names a source that cannot produce buckets, and shows no figure', async () => {
    const hub = {
      subscribeWidget: () => () => {},
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    mount(widget(), hub, live());
    await settle();
    expect(empty()).toBe('This data source cannot compute aggregates');
    expect(figure()).toBeUndefined();
  });

  it('still shows the latest reading from a source that cannot produce buckets', async () => {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_d: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    mount(widget({ stat: 'last' }), hub, live());
    await settle();
    act(() =>
      sink?.next({ id: '1', deviceToken: 'd-1', eventType: 0, occurredTime: new Date(NOW).toISOString(), name: 'temperature', value: 19.5, classifier: null }),
    );
    expect(figure()).toBe('19.5');
  });

  it('shows a failed read as unavailable, with the reason, never as a figure', async () => {
    const f = makeHub(() => Promise.reject(new Error('event-management is down')));
    mount(widget(), f.hub, live());
    await settle();
    expect(empty()).toBe('Aggregates unavailable: event-management is down');
    expect(figure()).toBeUndefined();
  });

  it('shows buckets that hold readings but no maximum as a failure, not as a maximum of the averages', async () => {
    const f = makeHub(() => [bucket({ max: null, avg: 30, count: 3 })]);
    mount(widget(), f.hub, live());
    await settle();
    expect(empty()).toMatch(/holds 3 readings but no max/);
    expect(figure()).toBeUndefined();
    expect(document.body.textContent).not.toContain('30');
  });

  it('recovers when the next read succeeds', async () => {
    let fail = true;
    const f = makeHub(() => (fail ? Promise.reject(new Error('blip')) : [bucket({ max: 61 })]));
    mount(widget(), f.hub, live());
    await settle();
    expect(empty()).toMatch(/blip/);
    fail = false;
    await advance(AGGREGATE_REFRESH_MS);
    expect(figure()).toBe('61');
  });

  it('says there were no readings when the span holds none', async () => {
    const f = makeHub(() => []);
    mount(widget(), f.hub, live());
    await settle();
    expect(empty()).toBe('No readings in the last 1h');
  });

  it('says there were none for a card with no datasource, without asking the source', async () => {
    const f = makeHub(() => [bucket()]);
    mount(widget({}, null), f.hub, live());
    await settle();
    expect(f.read).not.toHaveBeenCalled();
    expect(empty()).toMatch(/No readings/);
  });

  it('shows the host error pane when the live stream itself fails', async () => {
    let sink: WidgetStreamSink | null = null;
    const hub = {
      subscribeWidget: (_d: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
      bucketMeasurements: async () => [bucket()],
    } as unknown as DashboardHub;
    mount(widget(), hub, live());
    await settle();
    act(() => sink?.error?.(new Error('socket closed')));
    expect(screen.getByText('Data unavailable')).toBeTruthy();
    expect(screen.getByText(/socket closed/)).toBeTruthy();
  });
});

// A live feed that resumes after an outage: the readings sent meanwhile are in the store, and the
// figure must take them in now -- not when the next scheduled read comes round, which on a week's
// span is 42 minutes later.
describe('after the live feed was interrupted', () => {
  it('reads the figure again at once', async () => {
    let sink: WidgetStreamSink | null = null;
    let max = 40;
    const read = vi.fn(async () => [bucket({ max })]);
    const hub = {
      subscribeWidget: (_d: DatasourceSelector, s: WidgetStreamSink) => {
        sink = s;
        return () => {};
      },
      isDatasourceAvailable: async () => true,
      bucketMeasurements: read,
    } as unknown as DashboardHub;
    mount(widget(), hub, live(7 * 86400));
    await settle();
    expect(figure()).toContain('40');
    expect(read).toHaveBeenCalledTimes(1);

    // The device peaked at 90 during the outage.
    max = 90;
    await advance(5 * 60_000);
    expect(read).toHaveBeenCalledTimes(1);
    act(() => sink!.reconnected?.());
    await settle();
    expect(read).toHaveBeenCalledTimes(2);
    expect(figure()).toContain('90');
  });
});

describe('a stat card set to draw its trend', () => {
  const buckets = [
    bucket({ bucketStart: '2026-10-14T11:00:00Z', min: 10, max: 20, sum: 150, count: 10 }),
    bucket({ bucketStart: '2026-10-14T11:10:00Z', min: 12, max: 40, sum: 250, count: 10 }),
  ];

  it('reads the same window in the span’s chart buckets, and the figure is the same number', async () => {
    const plain = makeHub(() => buckets);
    mount(widget(), plain.hub, live(3600));
    await settle();
    const plainFigure = figure();
    cleanup();

    const f = makeHub(() => buckets);
    mount(widget({ sparkline: true }), f.hub, live(3600));
    await settle();
    expect(f.read).toHaveBeenCalledTimes(1);
    const [, window] = f.read.mock.calls[0];
    const [, plainWindow] = plain.read.mock.calls[0];
    expect({ ...window, intervalSeconds: 0 }).toEqual({ ...plainWindow, intervalSeconds: 0 });
    expect(plainWindow.intervalSeconds).toBe(3600);
    expect(window.intervalSeconds).toBe(60);
    expect(figure()).toBe(plainFigure);
    expect(figure()).toBe('40');
  });

  it('asks for no finer buckets when the sparkline is off', async () => {
    const f = makeHub(() => buckets);
    mount(widget({ sparkline: false }), f.hub, live(3600));
    await settle();
    expect(f.read.mock.calls[0][1].intervalSeconds).toBe(3600);
  });

  it('reads again when the sparkline is switched on or off, rather than showing the other read', async () => {
    const f = makeHub(() => buckets);
    const view = mount(widget(), f.hub, live(3600));
    await settle();
    view.rerender(
      <WidgetTimeRangeProvider range={live(3600)}>
        <ConnectedWidget widget={widget({ sparkline: true })} hub={f.hub} />
      </WidgetTimeRangeProvider>,
    );
    await settle();
    expect(f.read).toHaveBeenCalledTimes(2);
    expect(f.read.mock.calls[1][1].intervalSeconds).toBe(60);
  });
});
