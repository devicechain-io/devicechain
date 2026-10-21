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

const roster = (tokens: string[], total = tokens.length, types: Record<string, string> = {}): MemberRoster => ({
  members: tokens.map((token) => ({ token, name: `Machine ${token}`, deviceType: types[token] ?? null })),
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
  // hub.declaredMetrics: by default every type declares nothing it can be asked about (null).
  declared: ReturnType<typeof vi.fn>;
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
  const declared = vi.fn(async (_type: string): Promise<readonly string[] | null> => null);
  const hub = {
    declaredMetrics: declared,
    subscribeWidget: () => () => {},
    isDatasourceAvailable: async () => true,
    subscribeMembers: subscribe,
    readMemberSeeds: seeds,
  } as unknown as DashboardHub;
  return {
    hub,
    subscribe,
    seeds,
    declared,
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

  // "Does not report this" is what the PROFILE declares, looked up once per device TYPE.
  describe('the profile declaration', () => {
    const TYPES = { fuel1: 'tank', fuel2: 'tank', fuel3: 'tank', plant: 'crusher' };
    const DECLARES: Record<string, string[]> = { tank: ['temperature', 'battery'], crusher: ['throughput'] };

    it('looks a device type up once, however many devices have it', async () => {
      const f = makeHub();
      f.declared.mockImplementation(async (type) => DECLARES[type] ?? null);
      mount(widget(), f.hub, live());
      act0(() => f.sink().roster(roster(['fuel1', 'fuel2', 'fuel3', 'plant'], 4, TYPES)));
      await flush();
      expect(f.declared.mock.calls.map((c) => c[0]).sort()).toEqual(['crusher', 'tank']);
      // A later roster of the same types (a membership change) does not ask again.
      act0(() => f.sink().roster(roster(['fuel1', 'fuel2', 'fuel3', 'plant'], 4, TYPES)));
      await flush();
      expect(f.declared).toHaveBeenCalledTimes(2);
    });

    it('says a dead probe with a live heartbeat is silent, and a device whose profile never declares it does not report it', async () => {
      const f = makeHub();
      f.declared.mockImplementation(async (type) => DECLARES[type] ?? null);
      mount(widget(), f.hub, live());
      act0(() => f.sink().roster(roster(['fuel1', 'plant'], 2, TYPES)));
      act0(() => {
        f.seed('fuel1', { latest: { battery: sample('fuel1', 'battery', 88) }, observed: ['battery'] });
        f.seed('plant', { latest: { throughput: sample('plant', 'throughput', 5) }, observed: ['throughput'] });
      });
      await flush();
      expect(cellText('fuel1', 'temperature')).toBe('no data in range');
      expect(cellText('plant', 'temperature')).toBe('does not report this');
    });

    it('treats a failed lookup as declared: the device is silent, never left out', async () => {
      const f = makeHub();
      f.declared.mockRejectedValue(new Error('profile read failed'));
      mount(widget(), f.hub, live());
      act0(() => f.sink().roster(roster(['plant'], 1, TYPES)));
      act0(() => f.seed('plant', { latest: { throughput: sample('plant', 'throughput', 5) }, observed: ['throughput'] }));
      await flush();
      expect(cellText('plant', 'temperature')).toBe('no data in range');
    });

    it('treats a lookup that has not landed as declared, then follows it when it does', async () => {
      const f = makeHub();
      let land: (names: string[]) => void = () => {};
      f.declared.mockImplementation(() => new Promise((resolve) => (land = resolve)));
      mount(widget(), f.hub, live());
      act0(() => f.sink().roster(roster(['plant'], 1, TYPES)));
      act0(() => f.seed('plant', { latest: { throughput: sample('plant', 'throughput', 5) }, observed: ['throughput'] }));
      await flush();
      expect(cellText('plant', 'temperature')).toBe('no data in range');
      land(DECLARES.crusher);
      await flush();
      expect(cellText('plant', 'temperature')).toBe('does not report this');
    });

    it('does not look up a device with no known type, and counts it as declared', async () => {
      const f = makeHub();
      mount(widget(), f.hub, live());
      act0(() => f.sink().roster(roster(['plant'])));
      act0(() => f.seed('plant', { latest: { throughput: sample('plant', 'throughput', 5) }, observed: ['throughput'] }));
      await flush();
      expect(f.declared).not.toHaveBeenCalled();
      expect(cellText('plant', 'temperature')).toBe('no data in range');
    });
  });

  it('explains an empty cell once the seed has settled', async () => {
    const f = makeHub();
    f.declared.mockImplementation(async (type) => (type === 'wide' ? ['temperature', 'humidity'] : ['humidity']));
    mount(widget(), f.hub, live());
    act0(() => f.sink().roster(roster(['quiet', 'partial', 'broken'], 3, { quiet: 'wide', partial: 'narrow', broken: 'wide' })));
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

describe('the live feed asks for what the seed asks for', () => {
  it('subscribes a per-device bar chart to the measurement it compares, even one its datasource does not name', async () => {
    const f = makeHub();
    mount(widget({ groupBy: 'device', measurement: 'pressure' }, { ...ds, measurements: ['temperature'] }, 'bar-chart'), f.hub, live());
    await flush();
    // The hub's live feed delivers only the names the selector lists: without 'pressure' here, the
    // bars would be seeded with it and then never move.
    expect(f.subscribe.mock.calls[0][0].measurements).toEqual(['temperature', 'pressure']);
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

// A member stream that resumes after an outage has missed whatever its device sent meanwhile. The
// table must not go on showing the reading from before the outage as the device's latest.
describe('after the live feed was interrupted', () => {
  const at = (deviceToken: string, value: number, ms: number): MeasurementSample => ({
    id: `${deviceToken}-${value}-${ms}`,
    deviceToken,
    eventType: 0,
    occurredTime: iso(ms),
    name: 'temperature',
    value,
    classifier: null,
  });

  it('reads the seeds again, so a reading sent during the outage is the one shown', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    act0(() => f.seed('a', { latest: { temperature: at('a', 11, NOW - 600_000) }, observed: ['temperature'] }));
    act0(() => f.sink().next(at('a', 21, NOW + 5_000)));
    await flush();
    expect(cellText('a', 'temperature')).toContain('21');

    // Down for five minutes; the device reported 30 two minutes in, and nothing since.
    vi.setSystemTime(NOW + 5 * 60_000 + 30_000);
    act0(() => f.sink().reconnected?.());
    await flush();
    // Answered through the read made after the reconnect only, as the hub answers it.
    const reread = f.seeds.mock.calls[1]?.[2] as ((t: string, s: MemberSeed | null) => void) | undefined;
    act0(() => reread?.('a', { latest: { temperature: at('a', 30, NOW + 2 * 60_000) }, observed: ['temperature'] }));
    await flush();
    expect(cellText('a', 'temperature')).toContain('30');
  });

  it('reads once at once and once more past the next minute boundary, however many members resumed', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a', 'b', 'c'])));
    await flush();
    expect(f.seeds).toHaveBeenCalledTimes(1);
    vi.setSystemTime(NOW + 5 * 60_000 + 30_000);
    act0(() => {
      for (let i = 0; i < 3; i++) f.sink().reconnected?.();
    });
    await flush();
    expect(f.seeds).toHaveBeenCalledTimes(2);
    // Nothing more before the boundary: a read then would end where the immediate one did. (Each
    // advance is its own act, so a read a timer asks for is made when it is asked for.)
    await act(async () => void (await vi.advanceTimersByTimeAsync(29_000)));
    expect(f.seeds).toHaveBeenCalledTimes(2);
    // The read that reaches the outage's last minute: its window ends on the boundary after it.
    await act(async () => void (await vi.advanceTimersByTimeAsync(3_000)));
    expect(f.seeds).toHaveBeenCalledTimes(3);
    expect(f.windows[2].endTime).toBe(iso(NOW + 6 * 60_000 - 1));
    await act(async () => void (await vi.advanceTimersByTimeAsync(5 * 60_000)));
    expect(f.seeds).toHaveBeenCalledTimes(3);
  });

  it('keeps the reading it had when the re-read fails', async () => {
    const f = makeHub();
    mount(widget(), f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    act0(() => f.seed('a', { latest: { temperature: at('a', 11, NOW - 600_000) }, observed: ['temperature'] }));
    await flush();
    act0(() => f.sink().reconnected?.());
    await flush();
    const reread = f.seeds.mock.calls[1]?.[2] as ((t: string, s: MemberSeed | null, x?: SeedFailure) => void) | undefined;
    act0(() => reread?.('a', null, 'failed'));
    await flush();
    expect(cellText('a', 'temperature')).toContain('11');
  });

  it('stops its boundary read when the widget goes away', async () => {
    const f = makeHub();
    const { unmount } = mount(widget(), f.hub, live(3600));
    act0(() => f.sink().roster(roster(['a'])));
    await flush();
    act0(() => f.sink().reconnected?.());
    await flush();
    const asked = f.seeds.mock.calls.length;
    unmount();
    await act(async () => void (await vi.advanceTimersByTimeAsync(2 * 60_000)));
    expect(f.seeds).toHaveBeenCalledTimes(asked);
  });
});
