// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The alarm table grouped by device type: a tab strip around the table. The table half is tested
// with a hand-built `typeTabs`; the connected half (what each tab reads, and that a refresh keeps the
// selection) through the real ConnectedWidget with a fake alarm channel.

import type {
  AlarmRow,
  AlarmSnapshot,
  AlarmStreamSink,
  AlarmSubscription,
  AlarmTypeCount,
  AlarmTypeTab,
  DashboardHub,
  WidgetInstance,
} from '@devicechain/dashboards';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ width: 0 }));

vi.mock('@devicechain/widget-sdk', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk')>();
  // 0 = unmeasured (jsdom), the table's default; a test sets a width to exercise the phone layout.
  return { ...actual, useElementSize: () => [() => {}, { width: h.width, height: 300 }] };
});

import { type AlarmStreamState } from '@devicechain/widget-sdk';
import { alarmStrings } from './alarm-strings';
import { ConnectedWidget } from './connected-widget';
import { loadAlarmWidgets } from './widgets/alarm-lazy';
import { AlarmTable, shownTabs } from './widgets/alarm-table';

beforeAll(async () => {
  await loadAlarmWidgets();
});
beforeEach(() => {
  h.width = 0;
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

const widget = (options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'w',
    type: 'alarm-table',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    options: { groupBy: 'deviceType', ...options },
  }) as unknown as WidgetInstance;

const alarm = (over: Partial<AlarmRow> = {}): AlarmRow => ({
  token: 'a-1',
  originatorType: 'device',
  originatorToken: 'thermostat-01',
  alarmKey: 'over-temperature',
  metricKey: 'temperature',
  state: 'ACTIVE',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: '2026-07-05T12:00:00Z',
  clearedTime: null,
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 87.4,
  deviceType: { token: 'thermostat', name: 'Thermostat' },
  ...over,
});

const tab = (token: string, name: string | null, active: number, worstSeverity: string | null): AlarmTypeTab => ({
  token,
  name,
  active,
  worstSeverity,
});

function stateWith(
  tabs: AlarmTypeTab[],
  over: Partial<AlarmStreamState> = {},
  selected: string | null = null,
  select: (t: string | null) => void = () => {},
): AlarmStreamState {
  return {
    alarms: [alarm()],
    total: 1,
    loading: false,
    error: null,
    typeTabs: { tabs, selected, select, loading: false },
    ...over,
  };
}

const TABS = [
  tab('thermostat', 'Thermostat', 3, 'CRITICAL'),
  tab('sensor', 'Sensor', 2, 'INDETERMINATE'),
  tab('gateway', 'Gateway', 0, null),
];

const stripe = (el: Element) => (el as HTMLElement).style.background;

describe('with grouping off', () => {
  it('renders the table with no tab strip and no type column, as it always has', () => {
    const data: AlarmStreamState = { alarms: [alarm()], total: 1, loading: false, error: null };
    render(<AlarmTable widget={widget({ groupBy: undefined })} data={data} />);
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(screen.queryByRole('tabpanel')).toBeNull();
    expect(screen.getAllByRole('columnheader').map((c) => c.textContent)).toEqual([
      'Severity',
      'Status',
      'Originator',
      'Alarm',
      'Value',
      'Raised',
    ]);
  });
});

describe('the tab strip', () => {
  it('shows All first, then a tab per device type labelled by NAME', () => {
    render(<AlarmTable widget={widget()} data={stateWith(TABS)} />);
    const tabs = within(screen.getByRole('tablist')).getAllByRole('tab');
    expect(tabs.map((t) => t.textContent?.replace(/\d+/g, '').trim())).toEqual(['All', 'Thermostat', 'Sensor', 'Gateway']);
    expect(tabs[0].getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('tablist').getAttribute('aria-label')).toBe('Alarms by device type');
  });

  it('falls back to the token for a type with no name', () => {
    render(<AlarmTable widget={widget()} data={stateWith([tab('acme-x1', null, 1, 'MAJOR')])} />);
    expect(screen.getByRole('tab', { name: /acme-x1/ })).toBeTruthy();
  });

  it('counts ACTIVE alarms in a badge coloured by the worst severity, and shows none at zero', () => {
    render(<AlarmTable widget={widget()} data={stateWith(TABS)} />);
    const badges = screen.getAllByTestId('alarm-tab-badge');
    // Thermostat and Sensor carry one; Gateway (zero active) does not.
    expect(badges).toHaveLength(2);
    expect(badges[0].getAttribute('aria-label')).toBe('3 active alarms, worst Critical');
    expect(badges[0].textContent).toBe('3');
    const reference = document.createElement('span');
    reference.style.background = 'hsl(0, 72%, 46%)';
    expect(stripe(within(badges[0]).getByTestId('alarm-tab-badge-dot'))).toBe(reference.style.background);
    expect(within(screen.getByRole('tab', { name: /Gateway/ })).queryByTestId('alarm-tab-badge')).toBeNull();
  });

  it('keeps a tab with only normal-band (indeterminate) alarms NEUTRAL', () => {
    render(<AlarmTable widget={widget()} data={stateWith(TABS)} />);
    const badge = within(screen.getByRole('tab', { name: /Sensor/ })).getByTestId('alarm-tab-badge');
    expect(badge.getAttribute('aria-label')).toBe('2 active alarms, worst Indeterminate');
    expect(stripe(within(badge).getByTestId('alarm-tab-badge-dot'))).toBe('hsl(var(--muted-foreground))');
  });

  it('wires tabs to the panel with ARIA, and a roving tabindex', () => {
    render(<AlarmTable widget={widget()} data={stateWith(TABS, {}, 'sensor')} />);
    const tabs = screen.getAllByRole('tab');
    const panel = screen.getByRole('tabpanel');
    const selected = tabs.find((t) => t.getAttribute('aria-selected') === 'true')!;
    expect(selected.textContent).toContain('Sensor');
    expect(panel.getAttribute('aria-labelledby')).toBe(selected.id);
    for (const t of tabs) expect(t.getAttribute('aria-controls')).toBe(panel.id);
    expect(tabs.map((t) => t.tabIndex)).toEqual([-1, -1, 0, -1]);
  });

  it('hides the device-type column inside a type tab, and shows it on All', () => {
    const { rerender } = render(<AlarmTable widget={widget()} data={stateWith(TABS)} />);
    expect(screen.getByRole('columnheader', { name: 'Device type' })).toBeTruthy();
    expect(screen.getByRole('cell', { name: 'Thermostat' })).toBeTruthy();
    rerender(<AlarmTable widget={widget()} data={stateWith(TABS, {}, 'thermostat')} />);
    expect(screen.queryByRole('columnheader', { name: 'Device type' })).toBeNull();
  });

  it('selects on click, and on Enter or Space via the native button', () => {
    const select = vi.fn();
    render(<AlarmTable widget={widget()} data={stateWith(TABS, {}, null, select)} />);
    fireEvent.click(screen.getByRole('tab', { name: /Sensor/ }));
    expect(select).toHaveBeenCalledWith('sensor');
    fireEvent.click(screen.getByRole('tab', { name: 'All' }));
    expect(select).toHaveBeenLastCalledWith(null);
  });

  it('moves focus with the arrow keys and Home/End, wrapping, without selecting', () => {
    const select = vi.fn();
    render(<AlarmTable widget={widget()} data={stateWith(TABS, {}, null, select)} />);
    const [all, thermostat, , gateway] = screen.getAllByRole('tab');
    all.focus();
    fireEvent.keyDown(all, { key: 'ArrowRight' });
    expect(document.activeElement).toBe(thermostat);
    fireEvent.keyDown(thermostat, { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(all);
    fireEvent.keyDown(all, { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(gateway);
    fireEvent.keyDown(gateway, { key: 'ArrowRight' });
    expect(document.activeElement).toBe(all);
    fireEvent.keyDown(all, { key: 'End' });
    expect(document.activeElement).toBe(gateway);
    fireEvent.keyDown(gateway, { key: 'Home' });
    expect(document.activeElement).toBe(all);
    expect(select).not.toHaveBeenCalled();
  });

  it('is readable at 390 px: the strip scrolls sideways, the phone layout drops the type column, nothing overflows', () => {
    h.width = 390;
    render(<AlarmTable widget={widget()} data={stateWith(TABS)} />);
    const strip = screen.getByRole('tablist');
    expect(strip.style.overflowX).toBe('auto');
    expect(strip.style.flexWrap).toBe('');
    for (const t of screen.getAllByRole('tab')) expect(t.style.whiteSpace).toBe('nowrap');
    expect(screen.queryByRole('columnheader', { name: 'Device type' })).toBeNull();
    expect(screen.getByRole('columnheader', { name: 'Severity' })).toBeTruthy();
  });

  it('speaks Spanish for the viewer whose locale is es', () => {
    document.documentElement.lang = 'es';
    render(<AlarmTable widget={widget()} data={stateWith(TABS)} />);
    expect(screen.getByRole('tablist').getAttribute('aria-label')).toBe(alarmStrings('es').tabsLabel);
    expect(screen.getByRole('tab', { name: /Todas/ })).toBeTruthy();
    expect(screen.getAllByTestId('alarm-tab-badge')[0].getAttribute('aria-label')).toBe('3 alarmas activas, la peor: Crítica');
  });

  it('shows no strip when no type could be told (nothing to group by)', () => {
    render(<AlarmTable widget={widget()} data={stateWith([])} />);
    expect(screen.queryByRole('tablist')).toBeNull();
  });
});

describe('pinned device types', () => {
  it('shows only the listed types, in the listed order; the rest are under All only', () => {
    const pinned = [{ token: 'sensor' }, { token: 'thermostat' }];
    render(<AlarmTable widget={widget({ deviceTypes: pinned })} data={stateWith(TABS)} />);
    expect(screen.getAllByRole('tab').map((t) => t.textContent?.replace(/\d+/g, '').trim())).toEqual(['All', 'Sensor', 'Thermostat']);
  });

  it('gives a pinned type with no active alarm a tab without a badge, named by its token', () => {
    expect(shownTabs([], ['pump'])).toEqual([tab('pump', null, 0, null)]);
    render(<AlarmTable widget={widget({ deviceTypes: [{ token: 'pump' }] })} data={stateWith(TABS)} />);
    const pump = screen.getByRole('tab', { name: 'pump' });
    expect(within(pump).queryByTestId('alarm-tab-badge')).toBeNull();
  });

  it('returns a selection the pin dropped to All', () => {
    const select = vi.fn();
    render(<AlarmTable widget={widget({ deviceTypes: [{ token: 'sensor' }] })} data={stateWith(TABS, {}, 'gateway', select)} />);
    expect(select).toHaveBeenCalledWith(null);
  });

  it('ignores malformed entries and duplicates', () => {
    expect(shownTabs(TABS, ['sensor', 'sensor']).map((t) => t.token)).toEqual(['sensor']);
    render(<AlarmTable widget={widget({ deviceTypes: [{ token: '' }, 7, null, { token: 'sensor' }] })} data={stateWith(TABS)} />);
    expect(screen.getAllByRole('tab')).toHaveLength(2);
  });
});

// ── Connected: what each tab reads, and that a refresh keeps the selection ──────────────────────

const row = (token: string, typeToken: string, typeName: string, severity: string, state = 'ACTIVE'): AlarmRow =>
  alarm({ token, originatorToken: `${typeToken}-${token}`, alarmKey: `key-${token}`, severity, state, deviceType: { token: typeToken, name: typeName } });

const ALL_ACTIVE = [
  row('t1', 'thermostat', 'Thermostat', 'CRITICAL'),
  row('t2', 'thermostat', 'Thermostat', 'MINOR'),
  row('s1', 'sensor', 'Sensor', 'INDETERMINATE'),
];

// The count read's answer for a set of rows: one entry per (type, severity).
function countsOf(rows: AlarmRow[]): AlarmSnapshot {
  const m = new Map<string, AlarmTypeCount>();
  for (const r of rows) {
    const key = `${r.deviceType!.token}/${r.severity}`;
    m.set(key, { deviceType: r.deviceType!, severity: r.severity, count: (m.get(key)?.count ?? 0) + 1 });
  }
  return { alarms: [], total: rows.length, typeCounts: [...m.values()] };
}

function fakeHub() {
  const subs: Array<{ sub: AlarmSubscription; sink: AlarmStreamSink; disposed: boolean }> = [];
  const hub = {
    subscribeAlarms: (sub: AlarmSubscription, sink: AlarmStreamSink) => {
      const entry = { sub, sink, disposed: false };
      subs.push(entry);
      return () => {
        entry.disposed = true;
      };
    },
    isDatasourceAvailable: async () => true,
    isDatasourceSupported: async () => true,
  } as unknown as DashboardHub;
  const live = () => subs.filter((s) => !s.disposed);
  const summary = () => live().find((s) => s.sub.countByDeviceType === true)!;
  const rows = () => live().find((s) => s.sub.countByDeviceType !== true)!;
  const emit = (entry: { sink: AlarmStreamSink }, snapshot: AlarmSnapshot) => act(() => entry.sink.next(snapshot));
  return { hub, subs, live, summary, rows, emit };
}

async function mount(options: Record<string, unknown> = {}) {
  const fake = fakeHub();
  render(<ConnectedWidget widget={widget(options)} hub={fake.hub} />);
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
  return fake;
}

describe('connected', () => {
  it('reads ONE count of the active alarms plus the selected tab’s rows, never a stream per tab', async () => {
    const f = await mount();
    expect(f.live()).toHaveLength(2);
    expect(f.summary().sub).toMatchObject({ state: 'ACTIVE', countByDeviceType: true });
    expect(f.rows().sub.deviceType).toBeUndefined();
    expect(f.rows().sub.enrichDeviceType).toBe(true);
    await f.emit(f.summary(), countsOf(ALL_ACTIVE));
    await f.emit(f.rows(), { alarms: ALL_ACTIVE, total: 3 });
    expect(screen.getAllByRole('tab')).toHaveLength(3);
    // Most alarming type first: Thermostat (critical) before Sensor (indeterminate).
    expect(screen.getAllByRole('tab').map((t) => t.textContent?.replace(/\d+/g, '').trim())).toEqual(['All', 'Thermostat', 'Sensor']);
  });

  it('loads only the chosen tab’s rows, when it is chosen', async () => {
    const f = await mount();
    await f.emit(f.summary(), countsOf(ALL_ACTIVE));
    await f.emit(f.rows(), { alarms: ALL_ACTIVE, total: 3 });
    const before = f.subs.length;

    fireEvent.click(screen.getByRole('tab', { name: /Sensor/ }));
    // Exactly one new subscription (the Sensor rows); the All rows were released; the summary stayed.
    expect(f.subs.length).toBe(before + 1);
    expect(f.rows().sub.deviceType).toBe('sensor');
    expect(f.summary().disposed).toBe(false);
    expect(f.subs.filter((s) => s.sub.deviceType === 'thermostat')).toHaveLength(0);

    await f.emit(f.rows(), { alarms: [ALL_ACTIVE[2]], total: 1 });
    expect(screen.getByText('key-s1')).toBeTruthy();
    expect(screen.queryByText('key-t1')).toBeNull();
  });

  it('keeps the selected tab when the data refreshes', async () => {
    const f = await mount();
    await f.emit(f.summary(), countsOf(ALL_ACTIVE));
    fireEvent.click(screen.getByRole('tab', { name: /Sensor/ }));
    await f.emit(f.rows(), { alarms: [ALL_ACTIVE[2]], total: 1 });

    // A poll lands on both streams, and the sensor's last alarm clears.
    await f.emit(f.summary(), countsOf(ALL_ACTIVE.slice(0, 2)));
    await f.emit(f.rows(), { alarms: [], total: 0 });

    const selected = screen.getAllByRole('tab').find((t) => t.getAttribute('aria-selected') === 'true')!;
    expect(selected.textContent).toContain('Sensor');
    // Still there at a zero count (no badge), under its name rather than its token.
    expect(within(selected).queryByTestId('alarm-tab-badge')).toBeNull();
  });

  it('badges each tab with the exact server count and the worst severity across its severities', async () => {
    const f = await mount();
    await f.emit(f.summary(), {
      alarms: [],
      total: 2405,
      typeCounts: [
        { deviceType: { token: 'thermostat', name: 'Thermostat' }, severity: 'MINOR', count: 2400 },
        { deviceType: { token: 'thermostat', name: 'Thermostat' }, severity: 'MAJOR', count: 5 },
      ],
    });
    const badge = within(screen.getByRole('tab', { name: /Thermostat/ })).getByTestId('alarm-tab-badge');
    expect(badge.textContent).toBe('2405');
    expect(badge.getAttribute('aria-label')).toBe('2405 active alarms, worst Major');
  });

  it('draws no strip and filters nothing when the source cannot count by type', async () => {
    const f = await mount();
    // A source that ignored countByDeviceType: rows, no typeCounts.
    await f.emit(f.summary(), { alarms: ALL_ACTIVE, total: 3 });
    await f.emit(f.rows(), { alarms: ALL_ACTIVE, total: 3 });
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(f.rows().sub.deviceType).toBeUndefined();
    expect(screen.getByText('key-t1')).toBeTruthy();
  });

  it('drops a selected tab’s filter when the counts stop arriving, rather than filter to a hidden tab', async () => {
    const f = await mount();
    await f.emit(f.summary(), countsOf(ALL_ACTIVE));
    fireEvent.click(screen.getByRole('tab', { name: /Sensor/ }));
    expect(f.rows().sub.deviceType).toBe('sensor');
    // The source's next answer carries no counts.
    await f.emit(f.summary(), { alarms: [], total: 0 });
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(f.rows().sub.deviceType).toBeUndefined();
  });

  it('draws no strip when the count read fails (a recording cannot count by type)', async () => {
    const f = await mount();
    await act(() => f.summary().sink.error?.(new Error('Not in this recording: device types')));
    await f.emit(f.rows(), { alarms: ALL_ACTIVE, total: 3 });
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(screen.getByText('key-s1')).toBeTruthy();
  });

  it('renders no strip, and one stream, when the option is off', async () => {
    const f = await mount({ groupBy: undefined });
    expect(f.live()).toHaveLength(1);
    expect(f.live()[0].sub.enrichDeviceType).toBeUndefined();
    await f.emit(f.live()[0], { alarms: ALL_ACTIVE, total: 3 });
    expect(screen.queryByRole('tablist')).toBeNull();
  });
});
