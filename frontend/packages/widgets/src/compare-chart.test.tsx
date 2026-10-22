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
import { act, cleanup, render, screen } from '@testing-library/react';
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
    expect(await screen.findByText(/access/i)).toBeTruthy();
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

  it('a group bound without the compare mode is refused in words that point at the compare mode', async () => {
    const f = fakeHub();
    mount(chart({ groupBy: 'measurement' }), f.hub);
    expect(await screen.findByText(/one line per device/)).toBeTruthy();
    expect(f.asked).not.toHaveBeenCalled();
  });
});
