// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet feed through the real ConnectedWidget: the roster, each member's own readings, the
// seed window (the board's span, or the widget's pin, frozen while paused), the pause, and every
// way of having no table. The hub is a fake that records what it was asked, so these hold the
// feed to what it asks of the data source as well as to what it draws.

import type {
  DashboardHub,
  DatasourceSelector,
  MeasurementSample,
  MemberBucketCriteria,
  MemberRoster,
  MemberSeed,
  MemberSeedPlan,
  MemberSink,
  SeedFailure,
  TimeRangeState,
  WidgetInstance,
} from '@devicechain/dashboards';
import { WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { ConnectedWidget } from './connected-widget';
import { FLUSH_MS } from './fleet-feed';
import { loadAggregateWidgets } from './widgets/aggregate-lazy';

const NOW = Date.parse('2026-10-14T12:00:00Z');
const ds: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' },
  measurements: ['temperature', 'humidity'],
};

const iso = (ms: number) => new Date(ms).toISOString();
const sample = (deviceToken: string, name: string, value: number, ageSeconds = 5): MeasurementSample => ({
  id: `${deviceToken}-${name}-${ageSeconds}`,
  deviceToken,
  eventType: 0,
  occurredTime: iso(NOW - ageSeconds * 1000),
  name,
  value,
  classifier: null,
});

const roster = (tokens: string[], total = tokens.length): MemberRoster => ({
  members: tokens.map((token) => ({ token, name: `Machine ${token}` })),
  total,
  unbound: false,
});

const widget = (options: Record<string, unknown> = {}, datasource: DatasourceSelector = ds, type = 'fleet-table'): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 12, rows: 6 } },
    datasource,
    options,
  }) as unknown as WidgetInstance;

interface Fake {
  hub: DashboardHub;
  subscribe: ReturnType<typeof vi.fn>;
  seeds: ReturnType<typeof vi.fn>;
  disposed: ReturnType<typeof vi.fn>;
  sink: () => MemberSink;
  seed: (token: string, seed: MemberSeed | null, failure?: SeedFailure) => void;
  // The span each seed plan reads, found by running the plan against a store that holds nothing:
  // it asks for the last few minutes, finds nothing, and then asks for the whole span.
  windows: MemberBucketCriteria[];
  plans: MemberSeedPlan[];
}

// A hub whose roster, live samples and seeds are delivered by the test, when the test chooses.
function makeHub(): Fake {
  let sink: MemberSink | null = null;
  const onSeeds: Array<(token: string, seed: MemberSeed | null, failure?: SeedFailure) => void> = [];
  const windows: MemberBucketCriteria[] = [];
  const plans: MemberSeedPlan[] = [];
  const disposed = vi.fn();
  const subscribe = vi.fn((_d: DatasourceSelector, s: MemberSink) => {
    sink = s;
    return disposed;
  });
  const seeds = vi.fn(async (_tokens: readonly string[], plan: MemberSeedPlan, onSeed: (typeof onSeeds)[number]) => {
    onSeeds.push(onSeed);
    plans.push(plan);
    const asked: MemberBucketCriteria[] = [];
    await plan.seed('probe', async (c) => {
      asked.push(c);
      return [];
    });
    windows.push(asked[asked.length - 1]);
  });
  const hub = {
    subscribeWidget: () => () => {},
    isDatasourceAvailable: async () => true,
    subscribeMembers: subscribe,
    readMemberSeeds: seeds,
  } as unknown as DashboardHub;
  return {
    hub,
    subscribe,
    seeds,
    disposed,
    sink: () => sink!,
    seed: (token, seed, failure) => onSeeds.forEach((f) => f(token, seed, failure)),
    windows,
    plans,
  };
}

// The feed and the table both arrive as chunks, and a dynamic import resolves on the module runner's
// own timers, which the fake clock would never advance: load both once, on the real clock.
beforeAll(async () => {
  await loadAggregateWidgets();
  const f = makeHub();
  render(<ConnectedWidget widget={widget()} hub={f.hub} />);
  await screen.findByText('Loading', {}, { timeout: 20_000 });
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

const flush = () => act(async () => void (await vi.advanceTimersByTimeAsync(FLUSH_MS + 1)));
const act0 = (fn: () => void) => act(fn);
const cellText = (device: string, measurement: string) =>
  document.querySelector(`tr[data-device="${device}"] td[data-measurement="${measurement}"]`)?.textContent ?? null;

describe('the data it asks for', () => {
  it('opens ONE member subscription for the widget and hands it the datasource', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    await flush();
    expect(f.subscribe).toHaveBeenCalledTimes(1);
    expect(f.subscribe.mock.calls[0][0]).toEqual(ds);
  });

  it('reads the seeds for the roster over the board span, ending at the last whole minute', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a', 'b'])));
    await flush();
    expect(f.seeds).toHaveBeenCalledTimes(1);
    expect(f.seeds.mock.calls[0][0]).toEqual(['a', 'b']);
    expect(f.windows[0]).toMatchObject({ startTime: iso(NOW - 3600_000), endTime: iso(NOW - 1) });
    expect(f.windows[0].intervalSeconds % 60).toBe(0);
  });

  it('follows the viewer’s range: a new span reads the seeds again over the new window', async () => {
    const f = makeHub();
    const { setRange } = mount(widget(), f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    setRange(live(86400));
    await flush();
    expect(f.seeds).toHaveBeenCalledTimes(2);
    expect(Date.parse(f.windows[1].endTime) + 1 - Date.parse(f.windows[1].startTime)).toBe(86400_000);
    // The subscription itself is not torn down for a range change: no live reading is lost.
    expect(f.subscribe).toHaveBeenCalledTimes(1);
    expect(f.disposed).not.toHaveBeenCalled();
  });

  it('pins to the widget’s own span when it sets one, whatever the board shows', async () => {
    const f = makeHub();
    mount(widget({ windowSeconds: 900 }), f.hub, live(86400));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    expect(Date.parse(f.windows[0].endTime) + 1 - Date.parse(f.windows[0].startTime)).toBe(900_000);
  });

  it('reads the frozen window while paused, and does not read again as time passes', async () => {
    const f = makeHub();
    const at = NOW - 1800_000;
    mount(widget(), f.hub, frozenAt(at));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    expect(f.windows[0].endTime).toBe(iso(at - (at % 60_000) - 1));
    await act(async () => void (await vi.advanceTimersByTimeAsync(10 * 60_000)));
    expect(f.seeds).toHaveBeenCalledTimes(1);
  });

  it('asks nothing of an empty roster', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().roster({ members: [], total: 0, unbound: false }));
    await flush();
    expect(f.seeds).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain('No devices in this group');
  });

  it('closes the subscription when the widget goes away, and opens a new one for a new datasource', async () => {
    const f = makeHub();
    const { rerender, unmount } = mount(widget(), f.hub, live());
    await flush();
    expect(f.subscribe).toHaveBeenCalledTimes(1);
    rerender(
      <WidgetTimeRangeProvider range={live()}>
        <ConnectedWidget widget={widget({}, { ...ds, measurements: ['pressure'] })} hub={f.hub} />
      </WidgetTimeRangeProvider>,
    );
    await flush();
    expect(f.subscribe).toHaveBeenCalledTimes(2);
    expect(f.disposed).toHaveBeenCalledTimes(1);
    unmount();
    expect(f.disposed).toHaveBeenCalledTimes(2);
  });
});

describe('what it shows', () => {
  it('shows each device’s own reading for a measurement that two devices both report', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().roster(roster(['a', 'b'])));
    act0(() => {
      f.sink().next(sample('a', 'temperature', 20));
      f.sink().next(sample('b', 'temperature', 90));
    });
    await flush();
    expect(cellText('a', 'temperature')).toContain('20');
    expect(cellText('b', 'temperature')).toContain('90');
    expect(cellText('a', 'temperature')).not.toContain('90');
  });

  it('fills a cell from the seed, and lets a newer live reading replace it', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    expect(cellText('a', 'temperature')).toBe('…'); // seed pending
    act0(() => f.seed('a', { latest: { temperature: sample('a', 'temperature', 11, 600) }, observed: ['temperature'] }));
    await flush();
    expect(cellText('a', 'temperature')).toContain('11');
    act0(() => f.sink().next(sample('a', 'temperature', 12, 2)));
    await flush();
    expect(cellText('a', 'temperature')).toContain('12');
  });

  it('explains an empty cell once the seed has settled', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().roster(roster(['quiet', 'partial', 'broken'])));
    act0(() => {
      f.seed('quiet', { latest: {}, observed: [] });
      f.seed('partial', { latest: { humidity: sample('partial', 'humidity', 40) }, observed: ['humidity'] });
      f.seed('broken', null, 'failed');
    });
    await flush();
    expect(cellText('quiet', 'temperature')).toBe('no data in range');
    expect(cellText('partial', 'temperature')).toBe('does not report this');
    expect(cellText('broken', 'temperature')).toBe('could not be read');
  });

  it('says it is showing part of a fleet that is larger than the page read', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().roster(roster(['a', 'b'], 812)));
    await flush();
    expect(document.querySelector('[data-fleet-truncated]')?.textContent).toBe('Showing 2 of 812 devices');
  });

  it('says so when the viewer may not read the data, and when the source cannot list a fleet', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().forbidden?.());
    await flush();
    expect(document.body.textContent).toContain('do not have access');
    cleanup();

    const plain = { subscribeWidget: () => () => {}, isDatasourceAvailable: async () => true } as unknown as DashboardHub;
    mount(widget(), plain, live());
    await flush();
    expect(document.body.textContent).toContain('cannot list a fleet');
  });

  it('shows the error pane for a real failure, not for a refusal', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live());
    act0(() => f.sink().error?.(new Error('socket exploded')));
    await flush();
    expect(document.body.textContent).toContain('Data unavailable');
    expect(document.body.textContent).toContain('socket exploded');
  });

  it('a stale reading outside a shorter span is no longer shown, and returns with a longer one', async () => {
    const f = makeHub();
    const { setRange } = mount(widget(), f.hub, live(86400));
    act0(() => f.sink().roster(roster(['a'])));
    act0(() => f.seed('a', { latest: { temperature: sample('a', 'temperature', 33, 7200) }, observed: ['temperature'] }));
    await flush();
    expect(cellText('a', 'temperature')).toContain('33');
    setRange(live(3600));
    await flush();
    expect(cellText('a', 'temperature')).toBe('no data in range');
    setRange(live(86400));
    await flush();
    expect(cellText('a', 'temperature')).toContain('33');
  });
});

describe('the pause', () => {
  it('holds the table still while paused, and merges what arrived when the board resumes', async () => {
    const f = makeHub();
    const { setRange } = mount(widget(), f.hub, live());
    act0(() => f.sink().roster(roster(['a'])));
    act0(() => f.sink().next(sample('a', 'temperature', 20, 30)));
    await flush();
    expect(cellText('a', 'temperature')).toContain('20');

    setRange(frozenAt(NOW));
    await flush();
    act0(() => f.sink().next(sample('a', 'temperature', 99, 1)));
    // Something else rebuilds the table while it is paused (a seed settling): the queued reading
    // must still be held back, not just unscheduled.
    act0(() => f.seed('a', { latest: {}, observed: ['temperature'] }));
    await flush();
    expect(cellText('a', 'temperature'), 'a paused board moved').toContain('20');
    expect(cellText('a', 'temperature')).not.toContain('99');

    setRange(live());
    await flush();
    expect(cellText('a', 'temperature')).toContain('99');
  });

  it('keeps only the newest of the readings queued during a pause, per device and measurement', async () => {
    const f = makeHub();
    const { setRange } = mount(widget(), f.hub, frozenAt(NOW));
    act0(() => f.sink().roster(roster(['a', 'b'])));
    act0(() => {
      f.sink().next(sample('a', 'temperature', 1, 30));
      f.sink().next(sample('a', 'temperature', 2, 20));
      f.sink().next(sample('a', 'temperature', 3, 10));
      f.sink().next(sample('b', 'temperature', 7, 10));
    });
    setRange(live());
    await flush();
    expect(cellText('a', 'temperature')).toContain('3');
    expect(cellText('b', 'temperature')).toContain('7');
  });
});

describe('what the seed plan asks for', () => {
  // A device that reported only temperature in the last few minutes: what else does the plan read?
  async function followUps(w: WidgetInstance): Promise<MemberBucketCriteria[]> {
    const f = makeHub();
    mount(w, f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    const asked: MemberBucketCriteria[] = [];
    await f.plans[0].seed('a', async (c) => {
      asked.push(c);
      return asked.length === 1 ? [{ bucketStart: iso(NOW - 120_000), name: 'temperature', avg: 20, count: 4 }] : [];
    });
    return asked.slice(1);
  }

  it('looks further back only for the columns the table shows, one narrowed read each', async () => {
    const more = await followUps(widget());
    expect(more.map((c) => c.name)).toEqual(['humidity']);
    expect(Date.parse(more[0].endTime) + 1 - Date.parse(more[0].startTime)).toBe(3600_000);
  });

  it('counts the measurement a per-device bar chart compares as shown, whatever the datasource names', async () => {
    const more = await followUps(widget({ groupBy: 'device', measurement: 'pressure' }, { ...ds, measurements: ['temperature'] }, 'bar-chart'));
    expect(more.map((c) => c.name)).toEqual(['pressure']);
  });
});

describe('which feed a widget gets', () => {
  it('reads a bar chart member by member only when it is set to one bar per device', async () => {
    const f = makeHub();
    const stream = vi.fn(() => () => {});
    (f.hub as unknown as { subscribeWidget: typeof stream }).subscribeWidget = stream;
    mount(widget({ groupBy: 'device', measurement: 'temperature' }, ds, 'bar-chart'), f.hub, live());
    await flush();
    expect(f.subscribe).toHaveBeenCalledTimes(1);
    expect(stream).not.toHaveBeenCalled();
    cleanup();

    // One bar per measurement keys its readings by name: bound to a group it is refused, in words
    // that point at the per-device mode, and reads nothing at all.
    mount(widget({ measurement: 'temperature' }, ds, 'bar-chart'), f.hub, live());
    await flush();
    expect(stream).not.toHaveBeenCalled();
    expect(f.subscribe).toHaveBeenCalledTimes(1); // no second member subscription
    expect(document.body.textContent).toContain('Set it to one bar per device to compare them');
    cleanup();

    mount(widget({ measurement: 'temperature' }, { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] }, 'bar-chart'), f.hub, live());
    await flush();
    expect(stream).toHaveBeenCalledTimes(1);
  });
});
