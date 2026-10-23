// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The multi-device compare chart through the real ConnectedWidget and the real feed chunks: a line
// per member of the group, history read per DRAWN member only, the cap said on the chart, bucketed
// history labelled as such, and every way of having no chart a sentence.

import type {
  DashboardHub,
  DatasourceSelector,
  MeasurementBucket,
  MeasurementSample,
  MemberBucketCriteria,
  MemberRoster,
  MemberSeedPlan,
  MemberSink,
  TimeRangeState,
  WidgetInstance,
} from '@devicechain/dashboards';
import { WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The chart engine needs a canvas; the option it is handed is what these tests read.
const options: Array<Record<string, unknown>> = [];
vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    EChart: ({ option }: { option: Record<string, unknown> }) => {
      options.push(option);
      return <div data-testid="echart" />;
    },
  };
});

import { ConnectedWidget } from './connected-widget';

const NOW = Date.parse('2026-10-21T12:00:00Z');
const MIN = 60_000;
const at = (minAgo: number) => new Date(NOW - minAgo * MIN).toISOString();
const ds: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' },
  measurements: ['flow'],
};

const roster = (n: number, total = n): MemberRoster => ({
  members: Array.from({ length: n }, (_, i) => ({ token: `p${i + 1}`, name: `Pump ${i + 1}` })),
  total,
  unbound: false,
});

const live = (token: string, minAgo: number, value: number, name = 'flow'): MeasurementSample => ({
  id: `${token}-${name}-${minAgo}`,
  deviceToken: token,
  eventType: 0,
  occurredTime: at(minAgo),
  name,
  value,
  classifier: null,
});

const chart = (opts: Record<string, unknown> = {}, datasource: DatasourceSelector = ds, type = 'timeseries-chart'): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource,
    options: { groupBy: 'device', ...opts },
  }) as unknown as WidgetInstance;

interface Fake {
  hub: DashboardHub;
  sink: () => MemberSink;
  reads: Array<{ token: string; criteria: MemberBucketCriteria }>;
  asked: ReturnType<typeof vi.fn>;
}

// A hub that delivers the roster when told to and answers every history read from `history`.
function fakeHub(history: (token: string) => MeasurementBucket[] | Error = () => []): Fake {
  let sink: MemberSink | undefined;
  const reads: Fake['reads'] = [];
  const asked = vi.fn();
  const hub = {
    subscribeWidget: () => () => {},
    isDatasourceAvailable: async () => true,
    now: () => NOW,
    subscribeMembers: (d: DatasourceSelector, s: MemberSink) => {
      asked(d);
      sink = s;
      return () => {};
    },
    readMemberSeeds: async (
      tokens: readonly string[],
      plan: MemberSeedPlan,
      onSeed: (token: string, seed: unknown, failure?: 'failed' | 'forbidden') => void,
    ) => {
      await Promise.all(
        tokens.map(async (token) => {
          try {
            const seed = await plan.seed(token, async (criteria) => {
              reads.push({ token, criteria });
              const out = history(token);
              if (out instanceof Error) throw out;
              return out;
            });
            onSeed(token, seed);
          } catch {
            onSeed(token, null, 'failed');
          }
        }),
      );
    },
  } as unknown as DashboardHub;
  return { hub, sink: () => sink!, reads, asked };
}

const range = (spanSeconds = 3600, paused = false): TimeRangeState => (paused ? { spanSeconds, paused, pausedAt: NOW } : { spanSeconds, paused });

function mount(w: WidgetInstance, hub: DashboardHub, r: TimeRangeState = range()) {
  return render(
    <WidgetTimeRangeProvider range={r}>
      <ConnectedWidget widget={w} hub={hub} />
    </WidgetTimeRangeProvider>,
  );
}

interface Line {
  name: string;
  data: Array<[string, number]>;
}
const lastOption = () => options[options.length - 1] as { series: Line[] };
const lines = () => lastOption().series.map((s) => s.name);
const notes = () => document.querySelector('[data-compare-notes]')?.textContent ?? '';

beforeEach(() => {
  options.length = 0;
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

// First in the file on purpose: the feed chunks are fetched once per module graph, so only the
// first mount sees them in flight.
describe('while the feed chunks load', () => {
  it('draws the widget frame, busy, never an empty box', async () => {
    const f = fakeHub();
    mount(chart({ title: 'Pump flow' }), f.hub);
    const busy = document.querySelector('[data-chunk-loading]');
    expect(busy).not.toBeNull();
    expect(busy!.getAttribute('aria-busy')).toBe('true');
    expect(screen.getByText('Pump flow')).toBeTruthy();
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    expect(document.querySelector('[data-chunk-loading]')).toBeNull();
  });
});

describe('one line per device', () => {
  it('draws a line per member, named by the device, from history and the live tail', async () => {
    const f = fakeHub((token) => [{ bucketStart: at(30), name: 'flow', avg: token === 'p1' ? 10 : 20 }]);
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    // The live feed is asked for the one measurement, however many the datasource lists.
    expect((f.asked.mock.calls[0][0] as DatasourceSelector).measurements).toEqual(['flow']);
    act(() => {
      f.sink().roster(roster(2));
      f.sink().next(live('p1', 0, 12));
    });
    await vi.waitFor(() => expect(lines()).toEqual(['Pump 1', 'Pump 2']));
    await vi.waitFor(() => expect(lastOption().series[0].data.map((d) => d[1])).toEqual([10, 12]));
    expect(lastOption().series[1].data.map((d) => d[1])).toEqual([20]);
  });

  it('reads the history of the measurement alone, once per member, over the board span', async () => {
    const f = fakeHub();
    mount(chart({ measurement: 'flow' }, { ...ds, measurements: ['flow', 'pressure'] }), f.hub, range(21600));
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(3)));
    await vi.waitFor(() => expect(f.reads).toHaveLength(3));
    expect(f.reads.map((r) => r.token).sort()).toEqual(['p1', 'p2', 'p3']);
    for (const r of f.reads) {
      expect(r.criteria.name).toBe('flow');
      expect(r.criteria.intervalSeconds).toBe(120);
      expect(Date.parse(r.criteria.endTime) - Date.parse(r.criteria.startTime)).toBeGreaterThan(21500 * 1000);
    }
  });

  it('caps the lines, reads history for the drawn ones only, and says how many it left out', async () => {
    const f = fakeHub();
    mount(chart({ maxSeries: 3 }), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(8)));
    await vi.waitFor(() => expect(lines()).toEqual(['Pump 1', 'Pump 2', 'Pump 3']));
    expect(f.reads).toHaveLength(3);
    expect(notes()).toContain('Showing 3 of 8 devices');
    // A reading of a device that is not drawn is not kept.
    act(() => f.sink().next(live('p7', 0, 1)));
    expect(lines()).toEqual(['Pump 1', 'Pump 2', 'Pump 3']);
  });

  it('counts a group the source read only a page of against the whole group', async () => {
    const f = fakeHub();
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(6, 812)));
    await vi.waitFor(() => expect(notes()).toContain('Showing 5 of 812 devices'));
  });

  it('shows no note for a group that fits', async () => {
    const f = fakeHub();
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(2)));
    await vi.waitFor(() => expect(lines()).toHaveLength(2));
    expect(document.querySelector('[data-compare-notes]')).toBeNull();
  });

  it('labels bucketed history as an average per bucket', async () => {
    const f = fakeHub();
    mount(chart(), f.hub, range(86400));
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(2)));
    const badge = await vi.waitFor(() => {
      const el = document.querySelector('[data-compare-bucket]');
      expect(el).not.toBeNull();
      return el!;
    });
    expect(badge.getAttribute('data-compare-bucket')).toBe('6m');
    expect(badge.textContent).toBe('avg per 6m');
    expect(badge.getAttribute('title')).toContain('average');
  });

  it('says that a threshold is a reference line over averages, and only when there is one', async () => {
    const f = fakeHub();
    mount(chart({ thresholds: [{ value: 50, level: 'critical' }] }), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(2)));
    await vi.waitFor(() => expect(notes()).toContain('Thresholds are reference lines'));
  });

  it('says so when a device history could not be read, and still draws its live readings', async () => {
    const f = fakeHub((token) => (token === 'p2' ? new Error('boom') : []));
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => {
      f.sink().roster(roster(2));
      f.sink().next(live('p2', 0, 4));
    });
    await vi.waitFor(() => expect(notes()).toContain('history could not be read'));
    expect(lastOption().series[1].data.map((d) => d[1])).toEqual([4]);
  });

  it('keeps the newer history when an older read lands last (the span changed mid-read)', async () => {
    // Each read waits until the test answers it, so the two spans' reads can land in either order.
    const pending: Array<{ span: number; answer: (b: MeasurementBucket[]) => void }> = [];
    const f = fakeHub();
    const hub = {
      ...(f.hub as object),
      readMemberSeeds: async (tokens: readonly string[], plan: MemberSeedPlan, onSeed: (t: string, seed: unknown) => void) => {
        await Promise.all(
          tokens.map(async (token) => {
            const seed = await plan.seed(token, (criteria) => {
              const span = (Date.parse(criteria.endTime) - Date.parse(criteria.startTime)) / 1000;
              return new Promise<MeasurementBucket[]>((answer) => pending.push({ span, answer }));
            });
            onSeed(token, seed);
          }),
        );
      },
    } as unknown as DashboardHub;
    const view = mount(chart(), hub, range(3600));
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(1)));
    await vi.waitFor(() => expect(pending).toHaveLength(1));
    view.rerender(
      <WidgetTimeRangeProvider range={range(21600)}>
        <ConnectedWidget widget={chart()} hub={hub} />
      </WidgetTimeRangeProvider>,
    );
    await vi.waitFor(() => expect(pending).toHaveLength(2));
    expect(pending[0].span).toBeLessThan(pending[1].span);
    await act(async () => pending[1].answer([{ bucketStart: at(30), name: 'flow', avg: 2 }]));
    await vi.waitFor(() => expect(lastOption()?.series?.[0]?.data.map((d) => d[1])).toEqual([2]));
    // The 1-hour read, superseded, answers last: its average must not replace the 6-hour one.
    await act(async () => pending[0].answer([{ bucketStart: at(30), name: 'flow', avg: 1 }]));
    // Any reading redraws from the store (this one is older than the history's end, so not drawn).
    act(() => f.sink().next(live('p1', 5, 9)));
    await act(async () => new Promise((r) => setTimeout(r, 300)));
    expect(lastOption().series[0].data.map((d) => d[1])).toEqual([2]);
  });

  it('follows the roster: a member that joins gets its history read, one that leaves loses its line', async () => {
    const f = fakeHub((token) => [{ bucketStart: at(30), name: 'flow', avg: Number(token.slice(1)) }]);
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(2)));
    await vi.waitFor(() => expect(lines()).toEqual(['Pump 1', 'Pump 2']));
    act(() =>
      f.sink().roster({
        members: [
          { token: 'p1', name: 'Pump 1' },
          { token: 'p3', name: 'Pump 3' },
        ],
        total: 2,
        unbound: false,
      }),
    );
    await vi.waitFor(() => expect(lines()).toEqual(['Pump 1', 'Pump 3']));
    expect(f.reads.map((r) => r.token)).toContain('p3');
    await vi.waitFor(() => expect(lastOption().series[1].data.map((d) => d[1])).toEqual([3]));
  });

  it('labels the averaging in a box too small for the sentences, too', async () => {
    const f = fakeHub();
    // jsdom measures nothing; a ResizeObserver reporting a small box is what makes it compact.
    const RO = globalThis.ResizeObserver;
    class Small {
      constructor(private cb: ResizeObserverCallback) {}
      observe(el: Element) {
        this.cb([{ target: el, contentRect: { width: 240, height: 150 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver);
      }
      unobserve() {}
      disconnect() {}
    }
    globalThis.ResizeObserver = Small as unknown as typeof ResizeObserver;
    try {
      mount(chart({ maxSeries: 1 }), f.hub);
      await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
      act(() => f.sink().roster(roster(3)));
      const line = await vi.waitFor(() => {
        const el = document.querySelector('[data-compare-compact]');
        expect(el).not.toBeNull();
        return el!;
      });
      expect(line.textContent).toContain('avg per');
      expect(line.textContent).toContain('1 of 3 devices');
    } finally {
      globalThis.ResizeObserver = RO;
    }
  });

  it('cuts the chart at the freeze instant while paused: nothing newer than the freeze is drawn', async () => {
    const f = fakeHub(() => [
      { bucketStart: at(10), name: 'flow', avg: 1 },
      { bucketStart: at(2), name: 'flow', avg: 2 },
    ]);
    mount(chart(), f.hub, range(3600, true));
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => {
      f.sink().roster(roster(1));
      // Arrived after the board was frozen: held back, not drawn.
      f.sink().next(live('p1', -5, 9));
    });
    await vi.waitFor(() => expect(lastOption()?.series?.[0]?.data.map((d) => d[1])).toEqual([1, 2]));
  });
});

describe('the CSV export of a compare chart', () => {
  it('holds the points on screen, a row each, the device as the series and the basis of each point', async () => {
    const saved: Blob[] = [];
    const names: string[] = [];
    vi.spyOn(URL, 'createObjectURL').mockImplementation((b) => (saved.push(b as Blob), 'blob:csv'));
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined);
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });
    const f = fakeHub((token) => [{ bucketStart: at(30), name: 'flow', avg: token === 'p1' ? 10 : 20 }]);
    mount(chart({ title: 'Pump flow', unit: 'L/s', precision: 1 }), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => {
      f.sink().roster(roster(2));
      f.sink().next(live('p1', 0, 12.345));
    });
    await vi.waitFor(() => expect(lines()).toEqual(['Pump 1', 'Pump 2']));
    await vi.waitFor(() => expect(lastOption().series[0].data).toHaveLength(2));

    fireEvent.click(await screen.findByRole('button', { name: 'Export data' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Download CSV' }));
    await vi.waitFor(() => expect(saved).toHaveLength(1));
    const csv = new TextDecoder().decode(new Uint8Array(await saved[0].arrayBuffer()).slice(3));
    expect(csv.trimEnd().split('\r\n')).toEqual([
      'Time (UTC),Series,Value (L/s),Basis',
      '2026-10-21T11:30:00.000Z,Pump 1,10,avg per 1m',
      '2026-10-21T11:30:00.000Z,Pump 2,20,avg per 1m',
      '2026-10-21T12:00:00.000Z,Pump 1,12.3,reading',
    ]);
    expect(names[0]).toBe('Pump-flow-1h-20261021T1130Z-20261021T1200Z-avg-per-1m-reading.csv');
  });
});

describe('every way of having no chart is a sentence', () => {
  it('no measurement', async () => {
    const f = fakeHub();
    mount(chart({}, { ...ds, measurements: [] }), f.hub);
    expect(await screen.findByText('Choose the measurement to compare')).toBeTruthy();
    expect(f.asked).not.toHaveBeenCalled();
  });

  it('no members', async () => {
    const f = fakeHub();
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().roster(roster(0)));
    expect(await screen.findByText('No devices in this group')).toBeTruthy();
  });

  it('a refusal', async () => {
    const f = fakeHub();
    mount(chart(), f.hub);
    await vi.waitFor(() => expect(f.asked).toHaveBeenCalled());
    act(() => f.sink().forbidden?.());
    expect(await screen.findByText(/do not have access/i)).toBeTruthy();
  });

  it('a source that cannot list a fleet', async () => {
    const hub = { subscribeWidget: () => () => {}, isDatasourceAvailable: async () => true } as unknown as DashboardHub;
    mount(chart(), hub);
    expect(await screen.findByText('This data source cannot list a fleet')).toBeTruthy();
  });
});

describe('the default mode is the chart it always was', () => {
  it('one device, one line per measurement, no history per member', async () => {
    const f = fakeHub();
    const one: DatasourceSelector = { kind: 'device', deviceToken: 'p1', measurements: ['flow'] };
    const hub = {
      ...(f.hub as object),
      subscribeWidget: (_d: DatasourceSelector, sink: { next: (s: MeasurementSample) => void }) => {
        sink.next(live('p1', 1, 3));
        return () => {};
      },
    } as unknown as DashboardHub;
    mount(chart({ groupBy: undefined }, one), hub);
    await screen.findByTestId('echart');
    expect(f.asked).not.toHaveBeenCalled();
    expect(document.querySelector('[data-compare-chart]')).toBeNull();
  });

  it('a measurement stored for the compare mode does not narrow a one-device chart: its age is the newest line', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(NOW);
    try {
      const one: DatasourceSelector = { kind: 'device', deviceToken: 'p1', measurements: ['flow', 'pressure'] };
      const hub = {
        subscribeWidget: (_d: DatasourceSelector, sink: { next: (s: MeasurementSample) => void }) => {
          sink.next(live('p1', 10, 3, 'flow'));
          sink.next(live('p1', 0, 7, 'pressure'));
          return () => {};
        },
        isDatasourceAvailable: async () => true,
        now: () => NOW,
      } as unknown as DashboardHub;
      mount(chart({ groupBy: undefined, measurement: 'flow', staleAfterSeconds: 60 }, one), hub);
      await screen.findByTestId('echart');
      await vi.waitFor(() => expect(lines()).toEqual(['flow', 'pressure']));
      expect(document.querySelector('[data-stale="true"]')).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('a group bound without the compare mode is refused in words that point at the compare mode', async () => {
    const f = fakeHub();
    mount(chart({ groupBy: 'measurement' }), f.hub);
    expect(await screen.findByText(/one line per device/)).toBeTruthy();
    expect(f.asked).not.toHaveBeenCalled();
  });
});
