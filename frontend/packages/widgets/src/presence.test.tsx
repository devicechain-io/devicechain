// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The device status card and the online doughnut, and the feed that reads device-state for them:
// the tally rules, every state in which there is no honest figure, the ISA-101 colouring (the
// normal state neutral, only offline drawing attention), and the routing through the connected
// widget (a card bound to a group refuses it; the feed polls once per widget).

import type {
  DashboardHub,
  DatasourceSelector,
  DevicePresence,
  FleetMember,
  FleetState,
  KnownWidgetInstance,
  MemberPresence,
  PresenceSink,
  TimeRangeState,
} from '@devicechain/dashboards';
import { WidgetTimeRangeProvider } from '@devicechain/widget-sdk';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { arcPath } from './category-geometry';
import { ConnectedWidget } from './connected-widget';
import { buildPresenceFleet } from './presence-feed';
import { lastActivityMs, presenceKind, tallyPresence } from './presence-model';
import { loadPresenceWidgets } from './widgets/presence-lazy';
import { DeviceStatus, formatLastSeen } from './widgets/device-status';
import { OnlineDoughnut } from './widgets/online-doughnut';

const NOW = Date.parse('2026-10-21T12:00:00Z');
const MIN = 60_000;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  document.documentElement.lang = 'en';
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const ready = (active: boolean, agoMs: number | null = 5 * MIN): MemberPresence => ({
  status: 'ready',
  active,
  lastActivityTime: agoMs === null ? null : new Date(NOW - agoMs).toISOString(),
  inactivityTimeout: 600,
});
const member = (token: string, presence?: MemberPresence): FleetMember => ({
  token,
  name: token,
  latest: {},
  seed: 'ready',
  observed: [],
  ...(presence ? { presence } : {}),
});
const fleet = (members: FleetMember[], over: Partial<FleetState> = {}): FleetState => ({
  status: 'ready',
  members,
  total: members.length,
  truncated: false,
  ...over,
});

const ONE: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: [] };
const GROUP: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' },
  measurements: [],
};

function instance(type: 'device-status' | 'online-doughnut', datasource: DatasourceSelector, options: Record<string, unknown> = {}): KnownWidgetInstance {
  return {
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 4, rows: 4 } },
    datasource,
    options,
  } as unknown as KnownWidgetInstance;
}

const dataOf = (members: FleetState | undefined) => ({ latest: {}, samples: [], error: null, members });

function card(state: FleetState | undefined) {
  const { container } = render(<DeviceStatus widget={instance('device-status', ONE)} data={dataOf(state)} />);
  const root = container.querySelector<HTMLElement>('[data-device-status]');
  return {
    container,
    kind: root?.getAttribute('data-device-status'),
    figure: container.querySelector('[data-level-readout]')?.textContent,
    figureColor: container.querySelector<HTMLElement>('[data-level-readout]')?.style.color,
    caption: container.querySelector('[data-device-status-caption]')?.textContent,
    empty: container.querySelector('[data-level-empty]')?.textContent,
  };
}

function doughnut(state: FleetState | undefined) {
  const { container } = render(<OnlineDoughnut widget={instance('online-doughnut', GROUP)} data={dataOf(state)} />);
  const root = container.querySelector<HTMLElement>('[data-online-doughnut]');
  const legend = (kind: string) => container.querySelector(`[data-legend="${kind}"]`)?.textContent ?? null;
  return {
    container,
    root,
    centre: container.querySelector('[data-doughnut-figure]')?.textContent,
    segments: [...container.querySelectorAll<SVGElement>('[data-segment]')].map((s) => [s.getAttribute('data-segment'), Number(s.getAttribute('data-count'))]),
    segment: (kind: string) => container.querySelector<SVGElement>(`[data-segment="${kind}"]`),
    legend,
    notes: [...container.querySelectorAll('[data-doughnut-note]')].map((n) => n.textContent),
    empty: container.querySelector('[data-level-empty]')?.textContent,
    label: container.querySelector('svg')?.getAttribute('aria-label'),
  };
}

describe('the presence model', () => {
  it('reads active as online and inactive as offline', () => {
    expect(presenceKind(ready(true))).toBe('online');
    expect(presenceKind(ready(false))).toBe('offline');
  });

  it('keeps pending, failed and unknown apart from both', () => {
    expect(presenceKind(undefined)).toBe('pending');
    expect(presenceKind({ status: 'pending' })).toBe('pending');
    expect(presenceKind({ status: 'failed' })).toBe('failed');
    expect(presenceKind({ status: 'unknown' })).toBe('unknown');
  });

  it('takes last seen from the last activity, and nothing from a device with none or an unparseable one', () => {
    expect(lastActivityMs(ready(true, MIN))).toBe(NOW - MIN);
    expect(lastActivityMs(ready(false, null))).toBeUndefined();
    expect(lastActivityMs({ status: 'ready', active: true, lastActivityTime: 'not a time', inactivityTimeout: 600 })).toBeUndefined();
    expect(lastActivityMs({ status: 'unknown' })).toBeUndefined();
  });

  it('tallies every state and finds the newest activity', () => {
    const t = tallyPresence([
      member('a', ready(true, 2 * MIN)),
      member('b', ready(true, MIN)),
      member('c', ready(false, 90 * MIN)),
      member('d', { status: 'unknown' }),
      member('e', { status: 'failed' }),
      member('f'),
    ]);
    expect(t).toMatchObject({ online: 2, offline: 1, unknown: 1, failed: 1, pending: 1, newest: NOW - MIN });
  });
});

describe('arcPath', () => {
  it('has no path for an empty span or one that fills the circle: the caller draws a circle', () => {
    expect(arcPath(50, 50, 38, 0.2, 0.2)).toBeUndefined();
    expect(arcPath(50, 50, 38, 0, 1)).toBeUndefined();
  });
  it('takes the long way round past half a turn', () => {
    expect(arcPath(50, 50, 38, 0, 0.75)).toContain(' 0 1 1 ');
    expect(arcPath(50, 50, 38, 0, 0.25)).toContain(' 0 0 1 ');
  });
});

describe('the device status card', () => {
  it('says Online in the foreground colour: the normal state is neutral', () => {
    const s = card(fleet([member('d-1', ready(true, 5 * MIN))]));
    expect(s.kind).toBe('online');
    expect(s.figure).toBe('Online');
    expect(s.figureColor).toBe('hsl(var(--foreground))');
  });

  it('says Offline in a severity colour, and in words', () => {
    const s = card(fleet([member('d-1', ready(false, 3 * 3600_000))]));
    expect(s.kind).toBe('offline');
    expect(s.figure).toBe('Offline');
    expect(s.figureColor).not.toBe('hsl(var(--foreground))');
    expect(s.figureColor).not.toBe('');
  });

  it('shows when it was last seen', () => {
    const s = card(fleet([member('d-1', ready(true, 5 * MIN))]));
    expect(s.caption).toBe(`Last seen ${formatLastSeen(NOW - 5 * MIN, 'en')}`);
  });

  it('reads a device with no state row as "No status yet", muted, and never seen: not an outage', () => {
    const s = card(fleet([member('d-1', { status: 'unknown' })]));
    expect(s.kind).toBe('unknown');
    expect(s.figure).toBe('No status yet');
    expect(s.figureColor).toBe('hsl(var(--muted-foreground))');
    expect(s.caption).toBe('Never seen');
  });

  it('says never seen for a device that has a state but no activity', () => {
    expect(card(fleet([member('d-1', ready(false, null))])).caption).toBe('Never seen');
  });

  it('has no figure while the read is pending, and says so when it failed', () => {
    expect(card(fleet([member('d-1', { status: 'pending' })])).empty).toBe('Loading');
    cleanup();
    expect(card(fleet([member('d-1', { status: 'failed' })])).empty).toBe('Status could not be read');
  });

  it.each([
    ['loading', undefined, 'Loading'],
    ['unbound', fleet([], { status: 'unbound' }), 'Choose a device for this widget'],
    ['forbidden', fleet([], { status: 'forbidden' }), 'You do not have access to this data'],
    ['unsupported', fleet([], { status: 'unsupported' }), 'This data source cannot read device status'],
    ['empty', fleet([]), 'No devices in this group'],
  ])('explains the %s state', (_name, state, message) => {
    expect(card(state as FleetState | undefined).empty).toBe(message);
  });

  it('refuses a group in words rather than showing whichever device came first', () => {
    const s = card(fleet([member('a', ready(true)), member('b', ready(false))]));
    expect(s.empty).toContain('shows one device, and it is bound to a group');
    expect(s.empty).toContain('online / offline doughnut');
    expect(s.kind).toBeUndefined();
  });

  it('speaks Spanish when the viewer does', () => {
    document.documentElement.lang = 'es';
    const s = card(fleet([member('d-1', ready(false, 5 * MIN))]));
    expect(s.figure).toBe('Sin conexión');
  });
});

describe('the online doughnut', () => {
  const mixed = () =>
    fleet([
      member('a', ready(true)),
      member('b', ready(true)),
      member('c', ready(true)),
      member('d', ready(false, 4 * 3600_000)),
      member('e', { status: 'unknown' }),
    ]);

  it('rings online, offline and no status yet in proportion, with every count in a legend', () => {
    const s = doughnut(mixed());
    expect(s.segments).toEqual([
      ['online', 3],
      ['offline', 1],
      ['unknown', 1],
    ]);
    expect(s.centre).toBe('3of 5 online');
    expect(s.legend('online')).toBe('Online3');
    expect(s.legend('offline')).toBe('Offline1');
    expect(s.legend('unknown')).toBe('No status yet1');
    expect(s.label).toBe('3 online, 1 offline, 1 with no status yet');
  });

  it('draws the online slice neutral and only the offline slice in a colour', () => {
    const s = doughnut(mixed());
    expect(s.segment('online')?.style.stroke).toBe('hsl(var(--muted-foreground))');
    expect(s.segment('offline')?.style.stroke).not.toBe('hsl(var(--muted-foreground))');
    // The no-status slice is the same neutral, pale: less than a connected device, and not an outage.
    expect(s.segment('unknown')?.style.stroke).toBe('hsl(var(--muted-foreground))');
    expect(Number(s.segment('unknown')?.style.strokeOpacity)).toBeLessThan(1);
    expect(Number(s.segment('online')?.style.strokeOpacity)).toBe(1);
  });

  it('draws no colour at all for a group with nothing offline, but still lists the zero', () => {
    const s = doughnut(fleet([member('a', ready(true)), member('b', ready(true))]));
    expect(s.segments).toEqual([['online', 2]]);
    expect(s.legend('offline')).toBe('Offline0');
    expect(s.legend('unknown')).toBeNull();
  });

  it('draws a ring that is entirely one state as a circle', () => {
    const s = doughnut(fleet([member('a', ready(false))]));
    expect(s.segment('offline')?.tagName.toLowerCase()).toBe('circle');
  });

  it('has no ring while any member is still being read', () => {
    expect(doughnut(fleet([member('a', ready(true)), member('b', { status: 'pending' })])).empty).toBe('Loading');
  });

  it('notes the devices that could not be read, apart from the ring', () => {
    const s = doughnut(fleet([member('a', ready(true)), member('b', { status: 'failed' })]));
    expect(s.centre).toBe('1of 1 online');
    expect(s.notes).toEqual(['1 could not be read']);
  });

  it('says so when every read failed', () => {
    expect(doughnut(fleet([member('a', { status: 'failed' })])).empty).toBe('Status could not be read');
  });

  it('says it shows part of a group larger than the page', () => {
    const s = doughnut(fleet([member('a', ready(true))], { total: 812, truncated: true }));
    expect(s.notes).toEqual(['Showing 1 of 812 devices']);
  });

  it.each([
    ['loading', undefined, 'Loading'],
    ['unbound', fleet([], { status: 'unbound' }), 'Choose a device group for this widget'],
    ['forbidden', fleet([], { status: 'forbidden' }), 'You do not have access to this data'],
    ['unsupported', fleet([], { status: 'unsupported' }), 'This data source cannot read device status'],
    ['empty', fleet([]), 'No devices in this group'],
  ])('explains the %s state', (_name, state, message) => {
    expect(doughnut(state as FleetState | undefined).empty).toBe(message);
  });
});

describe('buildPresenceFleet', () => {
  const roster = { members: [{ token: 'a', name: 'A' }, { token: 'b', name: null }], total: 2, unbound: false };
  const state = (token: string, active: boolean): DevicePresence => ({ deviceToken: token, active, lastActivityTime: null, inactivityTimeout: 600 });

  it('is loading until the roster lands, and pending per member until the first snapshot', () => {
    expect(buildPresenceFleet({ roster: null, states: null, forbidden: false, error: null }).status).toBe('loading');
    const f = buildPresenceFleet({ roster, states: null, forbidden: false, error: null });
    expect(f.members.map((m) => m.presence?.status)).toEqual(['pending', 'pending']);
  });

  it('reads a member the snapshot omits as unknown: a settled answer, not a failure', () => {
    const f = buildPresenceFleet({ roster, states: new Map([['a', state('a', true)]]), forbidden: false, error: null });
    expect(f.members.map((m) => m.presence?.status)).toEqual(['ready', 'unknown']);
  });

  it('fails every member on an error before any snapshot, and keeps the last good snapshot after one', () => {
    const failed = buildPresenceFleet({ roster, states: null, forbidden: false, error: new Error('x') });
    expect(failed.members.map((m) => m.presence?.status)).toEqual(['failed', 'failed']);
    const kept = buildPresenceFleet({ roster, states: new Map([['a', state('a', false)]]), forbidden: false, error: new Error('x') });
    expect(kept.members[0].presence).toMatchObject({ status: 'ready', active: false });
    // The member the kept snapshot carried no row for stays a settled 'unknown', not a failure.
    expect(kept.members[1].presence).toEqual({ status: 'unknown' });
  });

  it('reports a refusal as forbidden, and an unbound slot as unbound', () => {
    expect(buildPresenceFleet({ roster, states: null, forbidden: true, error: null }).status).toBe('forbidden');
    expect(buildPresenceFleet({ roster: { ...roster, unbound: true }, states: null, forbidden: false, error: null }).status).toBe('unbound');
  });

  it('marks a roster past the page as truncated', () => {
    expect(buildPresenceFleet({ roster: { ...roster, total: 9 }, states: null, forbidden: false, error: null }).truncated).toBe(true);
  });
});

// ---- Through the connected widget ----------------------------------------------

interface Fake {
  hub: DashboardHub;
  subscribe: ReturnType<typeof vi.fn>;
  subscribeWidget: ReturnType<typeof vi.fn>;
  subscribeMembers: ReturnType<typeof vi.fn>;
  disposed: () => number;
  sink: () => PresenceSink;
}

function fakeHub(withPresence = true): Fake {
  let sink: PresenceSink | undefined;
  let disposed = 0;
  const subscribe = vi.fn((_ds: DatasourceSelector, s: PresenceSink) => {
    sink = s;
    return () => {
      disposed++;
    };
  });
  const subscribeWidget = vi.fn(() => () => {});
  const subscribeMembers = vi.fn(() => () => {});
  const hub = {
    ...(withPresence ? { subscribePresence: subscribe } : {}),
    subscribeWidget,
    subscribeMembers,
    isDatasourceAvailable: async () => true,
  } as unknown as DashboardHub;
  return { hub, subscribe, subscribeWidget, subscribeMembers, disposed: () => disposed, sink: () => sink! };
}

// The feed arrives as a chunk, and a dynamic import resolves on the module runner's own timers,
// which the fake clock would never advance: load the chunks once, on the real clock.
beforeAll(async () => {
  vi.useRealTimers();
  await loadPresenceWidgets();
  const f = fakeHub();
  render(<ConnectedWidget widget={instance('device-status', ONE)} hub={f.hub} />);
  await screen.findByText('Loading', {}, { timeout: 20_000 });
  cleanup();
}, 30_000);

const flush = () => act(async () => void (await vi.advanceTimersByTimeAsync(1)));
const roster = (tokens: string[]) => ({ members: tokens.map((token) => ({ token, name: null })), total: tokens.length, unbound: false });
const poll = (...states: Array<[string, boolean, number | null]>): DevicePresence[] =>
  states.map(([deviceToken, active, ago]) => ({
    deviceToken,
    active,
    lastActivityTime: ago === null ? null : new Date(NOW - ago).toISOString(),
    inactivityTimeout: 600,
  }));

describe('through the connected widget', () => {
  it('opens ONE presence subscription for the widget, hands it the datasource, and reads no readings', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    expect(f.subscribe).toHaveBeenCalledTimes(1);
    expect(f.subscribe.mock.calls[0][0]).toEqual(GROUP);
    expect(f.subscribeWidget).not.toHaveBeenCalled();
    expect(f.subscribeMembers).not.toHaveBeenCalled();
  });

  it('shows Loading until the roster lands, then draws the ring from the first snapshot', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    expect(screen.getByText('Loading')).toBeTruthy();
    act(() => f.sink().roster(roster(['a', 'b', 'c'])));
    await flush();
    // Roster known, no snapshot yet: the members are pending, so still no ring.
    expect(screen.getByText('Loading')).toBeTruthy();
    act(() => f.sink().next(poll(['a', true, MIN], ['b', false, 3 * 3600_000])));
    await flush();
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-online')).toBe('1');
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-offline')).toBe('1');
    // c has no state row.
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-no-status')).toBe('1');
  });

  it('replaces the snapshot on the next poll', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    act(() => {
      f.sink().roster(roster(['a', 'b']));
      f.sink().next(poll(['a', true, MIN], ['b', true, MIN]));
    });
    await flush();
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-offline')).toBe('0');
    act(() => f.sink().next(poll(['a', true, MIN], ['b', false, 20 * MIN])));
    await flush();
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-offline')).toBe('1');
  });

  it('ages the frame from the last READ, not the device: a silent offline device is not stale', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('device-status', ONE, { staleAfterSeconds: 60 })} hub={f.hub} />);
    await flush();
    act(() => {
      f.sink().roster(roster(['d-1']));
      f.sink().next(poll(['d-1', false, 10 * MIN]));
    });
    await flush();
    expect(document.querySelector('[data-device-status]')?.getAttribute('data-device-status')).toBe('offline');
    // 10 minutes silent against a 1-minute threshold, but the status was read just now: an offline
    // device is silent by definition, and greying it out would strip the one colour an outage takes.
    expect(document.querySelector('[data-stale-badge]')).toBeNull();
    // The reads then keep failing for two minutes: now the status on screen is old, and says so.
    act(() => f.sink().error?.(new Error('blip')));
    await act(async () => void (await vi.advanceTimersByTimeAsync(2 * MIN)));
    expect(document.querySelector('[data-device-status]')?.getAttribute('data-device-status')).toBe('offline');
    expect(document.querySelector('[data-stale-badge]')).toBeTruthy();
    // A read that lands again makes it current.
    act(() => f.sink().next(poll(['d-1', false, 12 * MIN])));
    await flush();
    expect(document.querySelector('[data-stale-badge]')).toBeNull();
  });

  it('does not mark the card stale when no threshold is set', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('device-status', ONE)} hub={f.hub} />);
    await flush();
    act(() => {
      f.sink().roster(roster(['d-1']));
      f.sink().next(poll(['d-1', true, 10 * MIN]));
    });
    await flush();
    expect(document.querySelector('[data-freshness]')).toBeTruthy();
    expect(document.querySelector('[data-stale-badge]')).toBeNull();
  });

  it('refuses a card bound to a group in words', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('device-status', GROUP)} hub={f.hub} />);
    await flush();
    act(() => {
      f.sink().roster(roster(['a', 'b']));
      f.sink().next(poll(['a', true, MIN], ['b', true, MIN]));
    });
    await flush();
    expect(document.body.textContent).toContain('shows one device, and it is bound to a group');
  });

  it('says a refusal in words rather than showing an error', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    act(() => f.sink().forbidden?.());
    await flush();
    expect(screen.getByText('You do not have access to this data')).toBeTruthy();
  });

  it('keeps the last good snapshot through a later failed poll', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    act(() => {
      f.sink().roster(roster(['a']));
      f.sink().next(poll(['a', true, MIN]));
    });
    await flush();
    act(() => f.sink().error?.(new Error('blip')));
    await flush();
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-online')).toBe('1');
  });

  it('fails the members, not the board, when the first read fails', async () => {
    const f = fakeHub();
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    act(() => {
      f.sink().roster(roster(['a']));
      f.sink().error?.(new Error('down'));
    });
    await flush();
    expect(screen.getByText('Status could not be read')).toBeTruthy();
  });

  it('says so when the data source cannot read device status', async () => {
    const f = fakeHub(false);
    render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    expect(screen.getByText('This data source cannot read device status')).toBeTruthy();
  });

  it('disposes its subscription when the widget goes away, and re-subscribes for a new selector', async () => {
    const f = fakeHub();
    const view = render(<ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />);
    await flush();
    view.rerender(<ConnectedWidget widget={instance('online-doughnut', { ...GROUP, anchor: { ...(GROUP as { anchor: object }).anchor, targetToken: 'other' } } as DatasourceSelector)} hub={f.hub} />);
    await flush();
    expect(f.subscribe).toHaveBeenCalledTimes(2);
    expect(f.disposed()).toBe(1);
    view.unmount();
    expect(f.disposed()).toBe(2);
  });

  it('holds the snapshot on screen while the board is paused', async () => {
    const f = fakeHub();
    const range = (r: TimeRangeState) => (
      <WidgetTimeRangeProvider range={r}>
        <ConnectedWidget widget={instance('online-doughnut', GROUP)} hub={f.hub} />
      </WidgetTimeRangeProvider>
    );
    const view = render(range({ spanSeconds: 3600, paused: false }));
    await flush();
    act(() => {
      f.sink().roster(roster(['a', 'b']));
      f.sink().next(poll(['a', true, MIN], ['b', true, MIN]));
    });
    await flush();
    view.rerender(range({ spanSeconds: 3600, paused: true, pausedAt: NOW }));
    await flush();
    act(() => f.sink().next(poll(['a', true, MIN], ['b', false, 30 * MIN])));
    await flush();
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-offline')).toBe('0');
    view.rerender(range({ spanSeconds: 3600, paused: false }));
    await flush();
    expect(document.querySelector('[data-online-doughnut]')?.getAttribute('data-offline')).toBe('1');
  });
});
