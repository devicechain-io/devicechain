// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The history table through the real ConnectedWidget: the window it reads (the board's span, or its own
// pin), what it says about its figures (bucket averages, never readings), how it pages, every way of
// having no table, and the CSV its frame exports -- the same span and the same bucket as on screen.

import type {
  DashboardHub,
  DatasourceSelector,
  HistoryWindow,
  MeasurementBucket,
  TimeRangeState,
  WidgetInstance,
} from '@devicechain/dashboards';
import { WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { ConnectedWidget } from './connected-widget';
import { historyRefreshMs } from './history-model';
import { loadHistoryWidgets } from './widgets/history-lazy';

const NOW = Date.parse('2026-10-22T14:05:42Z');
const ds: DatasourceSelector = { kind: 'device', deviceToken: 'pump-3', measurements: ['temperature', 'humidity'] };

const bucket = (start: string, name: string, avg: number | null): MeasurementBucket => ({
  bucketStart: start,
  name,
  avg,
  min: avg,
  max: avg,
  sum: avg,
  count: avg === null ? 0 : 3,
});

// An hour of one-minute buckets ending at 14:04, both measurements.
const HOUR: MeasurementBucket[] = Array.from({ length: 60 }, (_, i) => {
  const start = new Date(Date.parse('2026-10-22T13:05:00Z') + i * 60_000).toISOString();
  return [bucket(start, 'temperature', 20 + i / 10), bucket(start, 'humidity', 50 + i)];
}).flat();

const widget = (options: Record<string, unknown> = {}, datasource: DatasourceSelector | null = ds): WidgetInstance =>
  ({
    id: 'w',
    type: 'history-table',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 5 } },
    ...(datasource ? { datasource } : {}),
    options: { title: 'Pump 3 history', ...options },
  }) as unknown as WidgetInstance;

interface Hub {
  hub: DashboardHub;
  read: ReturnType<typeof vi.fn>;
  subscribed: ReturnType<typeof vi.fn>;
}

function makeHub(read: (d: DatasourceSelector, w: HistoryWindow) => Promise<MeasurementBucket[]> | MeasurementBucket[], withBuckets = true): Hub {
  const fn = vi.fn(async (d: DatasourceSelector, w: HistoryWindow) => read(d, w));
  const subscribed = vi.fn(() => () => {});
  const hub = {
    subscribeWidget: subscribed,
    isDatasourceAvailable: async () => true,
    ...(withBuckets ? { bucketMeasurements: fn } : {}),
  } as unknown as DashboardHub;
  return { hub, read: fn, subscribed };
}

const live = (spanSeconds = 3600): TimeRangeState => ({ spanSeconds, paused: false });
const frozenAt = (pausedAt: number, spanSeconds = 3600): TimeRangeState => ({ spanSeconds, paused: true, pausedAt });

function mount(w: WidgetInstance, hub: DashboardHub, range: TimeRangeState | undefined = live()) {
  const tree = (r: TimeRangeState | undefined) => (
    <WidgetTimeRangeProvider range={r}>
      <ConnectedWidget widget={w} hub={hub} />
    </WidgetTimeRangeProvider>
  );
  const view = render(tree(range));
  return { ...view, setRange: (r: TimeRangeState | undefined) => view.rerender(tree(r)) };
}

// The table and its feed arrive as one chunk, and a dynamic import resolves on the module runner's own
// timers, which the fake clock below would never advance: load it once, on the real clock.
beforeAll(async () => {
  await loadHistoryWidgets();
  const { hub } = makeHub(() => [], false);
  render(<ConnectedWidget widget={widget()} hub={hub} />);
  await screen.findByText('This data source cannot read stored history', {}, { timeout: 20_000 });
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
  vi.restoreAllMocks();
});

const settle = () => act(async () => void (await vi.advanceTimersByTimeAsync(0)));
const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

const rows = () => [...document.querySelectorAll<HTMLElement>('tbody tr')].map((tr) => [...tr.children].map((c) => c.textContent));
const heads = () => [...document.querySelectorAll('thead th')].map((th) => th.textContent);
const footer = () => document.querySelector('[data-history-footer]')?.textContent;
const basis = () => document.querySelector('[data-history-bucket]')?.textContent;
const empty = () => document.querySelector('[data-level-empty]')?.textContent;

describe('what the table reads', () => {
  it('is the board’s span, in the buckets of its chart, as whole minutes ending at the last boundary', async () => {
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub);
    await settle();
    expect(f.read).toHaveBeenCalledTimes(1);
    const [selector, window] = f.read.mock.calls[0];
    expect(selector).toEqual(ds);
    expect(window).toEqual({ startTime: '2026-10-22T13:05:00.000Z', endTime: '2026-10-22T14:04:59.999Z', intervalSeconds: 60 });
  });

  it('is the widget’s own pin when it has one, whatever the viewer picks', async () => {
    const f = makeHub(() => []);
    mount(widget({ windowSeconds: 21_600 }), f.hub, live(900));
    await settle();
    const window = f.read.mock.calls[0][1];
    expect(Date.parse(window.endTime) + 1 - Date.parse(window.startTime)).toBe(21_600_000);
    expect(window.intervalSeconds).toBe(120);
  });

  it('follows the viewer: a new range reads again over the new span, in that span’s bucket', async () => {
    const f = makeHub(() => []);
    const { setRange } = mount(widget(), f.hub, live(3600));
    await settle();
    setRange(live(86_400));
    await settle();
    expect(f.read).toHaveBeenCalledTimes(2);
    expect(f.read.mock.calls[1][1].intervalSeconds).toBe(360);
    expect(basis()).toBe('avg per 6m · newest first');
  });

  it('is read once while the board is paused, over the window ending at the freeze instant', async () => {
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub, frozenAt(Date.parse('2026-10-22T10:00:30Z')));
    await settle();
    expect(f.read.mock.calls[0][1].endTime).toBe('2026-10-22T09:59:59.999Z');
    await advance(10 * historyRefreshMs(3600));
    expect(f.read).toHaveBeenCalledTimes(1);
  });

  it('is read again on the boundary of each bucket while live, and not while the tab is hidden', async () => {
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub);
    await settle();
    await advance(60_000);
    expect(f.read).toHaveBeenCalledTimes(2);
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
    await advance(180_000);
    expect(f.read).toHaveBeenCalledTimes(2);
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false);
    document.dispatchEvent(new Event('visibilitychange'));
    await settle();
    expect(f.read).toHaveBeenCalledTimes(3);
  });

  it('has no live tail: nothing subscribes to the device’s stream', async () => {
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub);
    await settle();
    expect(f.subscribed).not.toHaveBeenCalled();
  });
});

describe('what the table says', () => {
  it('lists the bucket averages newest first, with the unit in the heading and the bucket in the footer', async () => {
    const f = makeHub(() => HOUR);
    mount(widget({ columns: [{ measurement: 'temperature', label: 'Temp', unit: '°C', precision: 1 }, { measurement: 'humidity', unit: '%', precision: 0 }] }), f.hub);
    await settle();
    expect(heads()).toEqual(['Time', 'Temp (°C)', 'humidity (%)']);
    const first = rows()[0];
    expect(first.slice(1)).toEqual(['25.9', '109']);
    expect(basis()).toBe('avg per 1m · newest first');
    // The sentence is in the footer's title: a figure here is an average, not a reading.
    expect(document.querySelector('[data-history-bucket]')?.getAttribute('title')).toMatch(/average of the readings in a 1m bucket, not a reading/);
    expect(document.querySelector('[data-history-bucket]')?.getAttribute('data-history-bucket')).toBe('1m');
  });

  it('lists the oldest first on request, and says so', async () => {
    const f = makeHub(() => HOUR);
    mount(widget({ oldestFirst: true }), f.hub);
    await settle();
    expect(rows()[0][1]).toBe('20');
    expect(basis()).toBe('avg per 1m · oldest first');
  });

  it('marks a measurement with no readings in a bucket, and does not carry the last value over', async () => {
    const f = makeHub(() => [bucket('2026-10-22T14:00:00Z', 'temperature', 21), bucket('2026-10-22T14:01:00Z', 'humidity', 55)]);
    mount(widget(), f.hub);
    await settle();
    const cells = document.querySelectorAll('tbody td');
    expect([...cells].map((c) => c.textContent)).toEqual(['—', '55', '21', '—']);
    expect(document.querySelectorAll('[data-cell-empty]')).toHaveLength(2);
    expect(document.querySelector('[data-cell-empty]')?.getAttribute('title')).toBe('no readings in this bucket');
  });

  it('pages a long history, the last page holding what is left', async () => {
    const f = makeHub(() => HOUR);
    mount(widget({ pageSize: 25 }), f.hub);
    await settle();
    expect(rows()).toHaveLength(25);
    expect(footer()).toContain('1–25 of 60');
    fireEvent.click(screen.getByLabelText('Next page'));
    fireEvent.click(screen.getByLabelText('Next page'));
    expect(rows()).toHaveLength(10);
    expect(footer()).toContain('51–60 of 60');
    expect((screen.getByLabelText('Next page') as HTMLButtonElement).disabled).toBe(true);
  });

  it('prints times alone for a day’s history and dates for a longer one', async () => {
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub);
    await settle();
    expect(rows()[0][0]).not.toMatch(/2026/);
    cleanup();
    const g = makeHub(() => [bucket('2026-10-20T10:00:00Z', 'temperature', 20), bucket('2026-10-22T10:00:00Z', 'temperature', 21)]);
    mount(widget(), g.hub, live(604_800));
    await settle();
    expect(rows()[0][0]).toMatch(/2026/);
  });

  it('says the range is empty rather than draw a table of nothing', async () => {
    const f = makeHub(() => []);
    mount(widget(), f.hub);
    await settle();
    expect(empty()).toBe('No readings in this range');
    expect(basis()).toBe('avg per 1m · newest first');
  });

  it('lists what the device reported when neither the author nor the datasource names a column', async () => {
    const f = makeHub(() => HOUR.slice(0, 4));
    mount(widget({}, { kind: 'device', deviceToken: 'pump-3', measurements: [] }), f.hub);
    await settle();
    expect(heads()).toEqual(['Time', 'humidity', 'temperature']);
  });
});

describe('when there is no table', () => {
  it('says it is loading until the first read lands', async () => {
    let release: (b: MeasurementBucket[]) => void = () => {};
    const f = makeHub(() => new Promise<MeasurementBucket[]>((r) => (release = r)));
    mount(widget(), f.hub);
    await settle();
    expect(empty()).toBe('Loading');
    release(HOUR);
    await settle();
    expect(rows().length).toBeGreaterThan(0);
  });

  it('says so when the history cannot be read, and reads it again at the next bucket', async () => {
    let fail = true;
    const f = makeHub(() => (fail ? Promise.reject(new Error('boom')) : HOUR));
    mount(widget(), f.hub);
    await settle();
    expect(empty()).toBe('The history could not be read');
    fail = false;
    await advance(60_000);
    expect(rows().length).toBeGreaterThan(0);
  });

  it('keeps the table on screen when a later read fails, and lets the frame age from the last good one', async () => {
    let fail = false;
    const f = makeHub(() => (fail ? Promise.reject(new Error('boom')) : HOUR));
    mount(widget({ staleAfterSeconds: 90 }), f.hub);
    await settle();
    const before = rows();
    fail = true;
    await advance(60_000);
    expect(rows()).toEqual(before);
    expect(empty()).toBeUndefined();
    await advance(120_000);
    expect(document.querySelector('[data-stale="true"]')).not.toBeNull();
  });

  it('says it is refused, not broken, when the viewer may not read the history', async () => {
    const f = makeHub(() => Promise.reject([{ message: 'forbidden: missing required authority', extensions: { code: 'FORBIDDEN' } }]));
    mount(widget(), f.hub);
    await settle();
    expect(empty()).toBe('You do not have access to this data');
  });

  it('asks for a device when the slot is unbound, and reads nothing', async () => {
    const f = makeHub(() => HOUR);
    mount(widget({}, { kind: 'unbound', measurements: [] } as DatasourceSelector), f.hub);
    await settle();
    expect(empty()).toBe('Choose a device for this widget');
    expect(f.read).not.toHaveBeenCalled();
  });

  it('says its source cannot read stored history rather than show an empty table', async () => {
    const f = makeHub(() => HOUR, false);
    mount(widget(), f.hub);
    await settle();
    expect(empty()).toBe('This data source cannot read stored history');
  });

  it('shows no table under the previous range while a new one is read', async () => {
    let release: (b: MeasurementBucket[]) => void = () => {};
    let first = true;
    const f = makeHub(() => {
      if (first) {
        first = false;
        return HOUR;
      }
      return new Promise<MeasurementBucket[]>((r) => (release = r));
    });
    const { setRange } = mount(widget(), f.hub, live(3600));
    await settle();
    expect(rows().length).toBeGreaterThan(0);
    setRange(live(86_400));
    await settle();
    expect(empty()).toBe('Loading');
    expect(rows()).toHaveLength(0);
    release([]);
    await settle();
  });

  it('speaks Spanish to a Spanish viewer', async () => {
    document.documentElement.lang = 'es';
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub);
    await settle();
    expect(heads()[0]).toBe('Hora');
    expect(basis()).toBe('prom. por 1m · más recientes primero');
  });
});

// ---- The export -----------------------------------------------------------------------------

describe('the CSV export', () => {
  let saved: Blob[];
  let names: string[];
  beforeEach(() => {
    saved = [];
    names = [];
    vi.spyOn(URL, 'createObjectURL').mockImplementation((b) => {
      saved.push(b as Blob);
      return 'blob:csv';
    });
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined);
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });
  });

  // The frame's menu loads as a chunk and the file is built from a Blob: both need the real clock.
  async function exportCsv(): Promise<string> {
    vi.useRealTimers();
    fireEvent.click(await screen.findByRole('button', { name: 'Export data' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Download CSV' }));
    await vi.waitFor(() => expect(saved).toHaveLength(1));
    const bytes = new Uint8Array(await saved[0].arrayBuffer());
    return new TextDecoder().decode(bytes.slice(3));
  }

  it('holds every row, not the page, over the span and bucket on screen, and says so in the file name', async () => {
    const f = makeHub(() => HOUR);
    mount(widget({ pageSize: 10, columns: [{ measurement: 'temperature', label: 'Temp', unit: '°C', precision: 1 }, { measurement: 'humidity', unit: '%' }] }), f.hub);
    await settle();
    expect(rows()).toHaveLength(10);
    const csv = await exportCsv();
    const lines = csv.trimEnd().split('\r\n');
    expect(lines[0]).toBe('Bucket start (UTC),Temp (°C) avg per 1m,humidity (%) avg per 1m');
    expect(lines).toHaveLength(61);
    // Newest first, as on screen; ISO-8601 with the zone; rounded as displayed (temperature to one place).
    expect(lines[1]).toBe('2026-10-22T14:04:00.000Z,25.9,109');
    expect(lines[60]).toBe('2026-10-22T13:05:00.000Z,20,50');
    expect(names).toEqual(['Pump-3-history-pump-3-avg-per-1m-20261022T1305Z-20261022T1405Z.csv']);
  });

  it('leaves a cell with no readings empty', async () => {
    const f = makeHub(() => [bucket('2026-10-22T14:00:00Z', 'temperature', 21)]);
    mount(widget(), f.hub);
    await settle();
    const csv = await exportCsv();
    expect(csv.trimEnd().split('\r\n')[1]).toBe('2026-10-22T14:00:00.000Z,21,');
  });

  it('follows the range: a longer span exports its own, wider bucket', async () => {
    const f = makeHub(() => HOUR);
    mount(widget(), f.hub, live(86_400));
    await settle();
    await exportCsv();
    expect(names[0]).toMatch(/-avg-per-6m-20261021T1405Z-20261022T1405Z\.csv$/);
  });

  it('guards a heading an author could make a formula', async () => {
    const f = makeHub(() => HOUR);
    mount(widget({ columns: [{ measurement: 'temperature', label: '=HYPERLINK("http://example.test")' }] }), f.hub);
    await settle();
    const csv = await exportCsv();
    expect(csv.split('\r\n')[0]).toBe(`Bucket start (UTC),"'=HYPERLINK(""http://example.test"") avg per 1m"`);
  });

  it('is not offered before there is a table to export', async () => {
    const f = makeHub(() => new Promise<MeasurementBucket[]>(() => {}));
    mount(widget(), f.hub);
    await settle();
    expect(screen.queryByRole('button', { name: 'Export data' })).toBeNull();
  });
});
