// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The timeseries chart's alarm and command markers through the real ConnectedWidget and the real
// feed chunk: off is today's chart, on reads the hub's alarm and command channels for the chart's
// ONE device and range, and every way of showing less than everything says so.

import {
  NotInRecordingError,
  type AlarmRow,
  type AlarmSnapshot,
  type AlarmStreamSink,
  type CommandRow,
  type CommandSnapshot,
  type CommandStreamSink,
  type DashboardHub,
  type DatasourceSelector,
  type MeasurementSample,
  type TimeRangeState,
  type WidgetInstance,
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
import { MAX_EVENT_MARKERS } from './event-markers-model';

const NOW = Date.parse('2026-10-20T12:00:00Z');
const MIN = 60_000;
const at = (minAgo: number) => new Date(NOW - minAgo * MIN).toISOString();
const ds: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };

const sample = (): MeasurementSample => ({
  id: 's',
  deviceToken: 'd-1',
  eventType: 0,
  occurredTime: at(1),
  name: 'temperature',
  value: 20,
  classifier: null,
});

const alarm = (over: Partial<AlarmRow> = {}): AlarmRow => ({
  token: 'a1',
  originatorType: 'device',
  originatorToken: 'd-1',
  alarmKey: 'high-temp',
  metricKey: 'temperature',
  state: 'CLEARED',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: at(30),
  clearedTime: at(10),
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 90,
  ...over,
});

const command = (over: Partial<CommandRow> = {}): CommandRow => ({
  token: 'c1',
  name: 'reboot',
  status: 'SUCCESSFUL',
  payload: null,
  responsePayload: null,
  error: null,
  queuedTime: at(21),
  sentTime: at(20),
  respondedTime: at(19),
  ...over,
});

interface Fake {
  hub: DashboardHub;
  alarmSubs: ReturnType<typeof vi.fn>;
  commandSubs: ReturnType<typeof vi.fn>;
  alarmDisposed: ReturnType<typeof vi.fn>;
  alarms: () => AlarmStreamSink | undefined;
  commands: () => CommandStreamSink | undefined;
}

// A hub whose channels answer when told to, and count what they are asked.
function fakeHub(first: { alarms?: AlarmSnapshot | Error; commands?: CommandSnapshot | Error } = {}): Fake {
  let alarmSink: AlarmStreamSink | undefined;
  let commandSink: CommandStreamSink | undefined;
  const alarmDisposed = vi.fn();
  const alarmSubs = vi.fn((_sub: unknown, sink: AlarmStreamSink) => {
    alarmSink = sink;
    if (first.alarms instanceof Error) sink.error?.(first.alarms);
    else if (first.alarms) sink.next(first.alarms);
    return alarmDisposed;
  });
  const commandSubs = vi.fn((_sub: unknown, sink: CommandStreamSink) => {
    commandSink = sink;
    if (first.commands instanceof Error) sink.error?.(first.commands);
    else if (first.commands) sink.next(first.commands);
    return () => {};
  });
  const hub = {
    subscribeWidget: (_d: DatasourceSelector, sink: { next: (s: MeasurementSample) => void }) => {
      sink.next(sample());
      return () => {};
    },
    subscribeAlarms: alarmSubs,
    subscribeCommands: commandSubs,
    isDatasourceAvailable: async () => true,
    now: () => NOW,
  } as unknown as DashboardHub;
  return { hub, alarmSubs, commandSubs, alarmDisposed, alarms: () => alarmSink, commands: () => commandSink };
}

const chart = (opts: Record<string, unknown> = {}, datasource: DatasourceSelector = ds): WidgetInstance =>
  ({
    id: 'w',
    type: 'timeseries-chart',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource,
    options: opts,
  }) as unknown as WidgetInstance;

const live = (spanSeconds = 3600): TimeRangeState => ({ spanSeconds, paused: false });
const frozen = (spanSeconds = 3600): TimeRangeState => ({ spanSeconds, paused: true, pausedAt: NOW });

function mount(w: WidgetInstance, hub: DashboardHub, range: TimeRangeState = live()) {
  const tree = (r: TimeRangeState) => (
    <WidgetTimeRangeProvider range={r}>
      <ConnectedWidget widget={w} hub={hub} />
    </WidgetTimeRangeProvider>
  );
  const view = render(tree(range));
  return { ...view, setRange: (r: TimeRangeState) => view.rerender(tree(r)) };
}

interface Series {
  name?: string;
  markArea?: { data: Array<[{ xAxis: number }, { xAxis: number }]> };
  markLine?: { data: Array<{ name: string; xAxis: number }> };
}
const lastOption = () => options[options.length - 1] as { series: Series[]; xAxis: { min?: number; max?: number } };
const helper = () => lastOption().series.find((s) => s.markLine || s.markArea);
const footer = () => document.querySelector('[data-chart-events]');
const notes = () => [...document.querySelectorAll('[data-chart-events-note]')].map((n) => n.textContent);
const listed = () => [...document.querySelectorAll('[data-chart-event]')];

beforeEach(() => {
  options.length = 0;
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

describe('with the markers off', () => {
  it('the chart is exactly what it was: no channel is read, no helper series, no footer', async () => {
    const f = fakeHub();
    mount(chart(), f.hub);
    await screen.findByTestId('echart');
    expect(f.alarmSubs).not.toHaveBeenCalled();
    expect(f.commandSubs).not.toHaveBeenCalled();
    expect(lastOption().series).toHaveLength(1);
    expect(lastOption().xAxis.min).toBeUndefined();
    expect(footer()).toBeNull();
  });

  it('an explicit false is off too', async () => {
    const f = fakeHub();
    mount(chart({ markAlarms: false, markCommands: false }), f.hub);
    await screen.findByTestId('echart');
    expect(f.alarmSubs).not.toHaveBeenCalled();
    expect(f.commandSubs).not.toHaveBeenCalled();
  });
});

describe('alarm markers', () => {
  it('read the alarm channel for the chart device only, and place the span at the raise and the clear', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm()], total: 1 } });
    mount(chart({ markAlarms: true }), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(f.alarmSubs).toHaveBeenCalledTimes(1);
    // The chart's range goes to the server, which pages and counts inside it: one page of the cap.
    expect(f.alarmSubs.mock.calls[0][0]).toEqual({ datasource: ds, pageSize: MAX_EVENT_MARKERS, window: { spanSeconds: 3600 } });
    expect(f.commandSubs).not.toHaveBeenCalled();
    const [from, to] = helper()!.markArea!.data[0];
    expect([from.xAxis, to.xAxis]).toEqual([NOW - 30 * MIN, NOW - 10 * MIN]);
    expect(helper()!.markLine!.data.map((l) => [l.name, l.xAxis])).toEqual([
      ['a1:raised', NOW - 30 * MIN],
      ['a1:cleared', NOW - 10 * MIN],
    ]);
    // The axis is the range the markers were placed in.
    expect(lastOption().xAxis).toMatchObject({ min: NOW - 3600_000, max: NOW });
  });

  it('are listed under the chart behind a disclosure a keyboard can open', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm()], total: 1 } });
    mount(chart({ markAlarms: true }), f.hub);
    await vi.waitFor(() => expect(footer()).not.toBeNull());
    const summary = footer()!.querySelector('details > summary');
    expect(summary?.textContent).toBe('Events in this range (1)');
    expect(listed()).toHaveLength(1);
    expect(listed()[0].textContent).toMatch(/^Alarm: high-temp · Critical · raised .+ · cleared .+$/);
    // The list scrolls, so a keyboard must be able to reach it to scroll it.
    const list = footer()!.querySelector('details ul');
    expect(list?.getAttribute('tabindex')).toBe('0');
    expect(list?.getAttribute('aria-label')).toBe('Events in this range (1)');
  });
});

describe('command markers', () => {
  it('read the command channel, and mark each send with its name and outcome', async () => {
    const f = fakeHub({ commands: { deviceToken: 'd-1', commands: [command(), command({ token: 'c2', name: 'ping', status: 'FAILED', sentTime: at(5) })], total: 2 } });
    mount(chart({ markCommands: true }), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(f.commandSubs.mock.calls[0][0]).toEqual({ datasource: ds, pageSize: MAX_EVENT_MARKERS, window: { spanSeconds: 3600 } });
    expect(f.alarmSubs).not.toHaveBeenCalled();
    expect(helper()!.markLine!.data.map((l) => [l.name, l.xAxis])).toEqual([
      ['c1', NOW - 20 * MIN],
      ['c2', NOW - 5 * MIN],
    ]);
    const text = listed().map((e) => e.textContent);
    expect(text.some((t) => t?.includes('Command: ping') && t.includes('failed'))).toBe(true);
    expect(text.some((t) => t?.includes('Command: reboot') && t.includes('succeeded'))).toBe(true);
  });

  it('alarms and commands together share one chart and one list', async () => {
    const f = fakeHub({
      alarms: { alarms: [alarm()], total: 1 },
      commands: { deviceToken: 'd-1', commands: [command()], total: 1 },
    });
    mount(chart({ markAlarms: true, markCommands: true }), f.hub);
    await vi.waitFor(() => expect(listed()).toHaveLength(2));
    expect(lastOption().series).toHaveLength(2); // the readings and ONE helper series
  });
});

describe('what is NOT shown is said', () => {
  it('counts the events beyond the cap: "N more not shown"', async () => {
    const rows = Array.from({ length: MAX_EVENT_MARKERS + 5 }, (_, i) => alarm({ token: `a${i}`, raisedTime: at(50 - i), clearedTime: null, state: 'ACTIVE' }));
    const f = fakeHub({ alarms: { alarms: rows, total: rows.length } });
    mount(chart({ markAlarms: true }), f.hub);
    await vi.waitFor(() => expect(notes()).toEqual(['5 more not shown']));
    expect(listed()).toHaveLength(MAX_EVENT_MARKERS);
    // A band and a strip each.
    expect(helper()!.markArea!.data).toHaveLength(MAX_EVENT_MARKERS * 2);
  });

  it('counts the range past the page exactly, from the source total for the range', async () => {
    const rows = Array.from({ length: MAX_EVENT_MARKERS }, (_, i) => alarm({ token: `a${i}`, raisedTime: at(55 - i), clearedTime: null, state: 'ACTIVE' }));
    const f = fakeHub({ alarms: { alarms: rows, total: 250 } });
    mount(chart({ markAlarms: true }), f.hub);
    // 250 in range, 30 read and drawn: exactly 220 more, never "up to".
    await vi.waitFor(() => expect(notes()).toEqual(['220 more not shown']));
  });

  it('adds the commands past their page to the alarms past the cap', async () => {
    const alarms = Array.from({ length: MAX_EVENT_MARKERS }, (_, i) => alarm({ token: `a${i}`, raisedTime: at(55 - i), clearedTime: null, state: 'ACTIVE' }));
    const commands = Array.from({ length: 4 }, (_, i) => command({ token: `c${i}`, sentTime: at(2 + i) }));
    const f = fakeHub({ alarms: { alarms, total: 40 }, commands: { deviceToken: 'd-1', commands, total: 9 } });
    mount(chart({ markAlarms: true, markCommands: true }), f.hub);
    // 34 read, 30 drawn (4 over the cap), and 10 + 5 never read.
    await vi.waitFor(() => expect(notes()).toEqual(['19 more not shown']));
  });

  it('says so in Spanish for a Spanish viewer', async () => {
    document.documentElement.lang = 'es';
    const rows = Array.from({ length: MAX_EVENT_MARKERS + 2 }, (_, i) => alarm({ token: `a${i}`, raisedTime: at(50 - i), clearedTime: null, state: 'ACTIVE' }));
    const f = fakeHub({ alarms: { alarms: rows, total: rows.length } });
    mount(chart({ markAlarms: true }), f.hub);
    await vi.waitFor(() => expect(notes()).toEqual(['2 más sin mostrar']));
    expect(document.querySelector('summary')?.textContent).toBe('Eventos en este intervalo (30)');
  });

  it('a recording without the channel says it is not in the recording, and draws no marker', async () => {
    const f = fakeHub({ alarms: new NotInRecordingError('alarms'), commands: new NotInRecordingError('commands') });
    mount(chart({ markAlarms: true, markCommands: true }), f.hub);
    await vi.waitFor(() => expect(notes()).toHaveLength(2));
    expect(notes()).toEqual(['Alarm markers: not in this recording', 'Command markers: not in this recording']);
    expect(helper()).toBeUndefined();
  });

  it('a recording WITH alarms draws them and still says the commands are missing', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm()], total: 1 }, commands: new NotInRecordingError('commands') });
    mount(chart({ markAlarms: true, markCommands: true }), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(notes()).toEqual(['Command markers: not in this recording']);
  });

  it('a failed read says it could not be loaded, rather than drawing nothing in silence', async () => {
    const f = fakeHub({ alarms: new Error('boom') });
    mount(chart({ markAlarms: true }), f.hub);
    await vi.waitFor(() => expect(notes()).toEqual(['Alarm markers could not be loaded']));
  });
});

describe('the board range', () => {
  it('a change of range reads again, for the new span, and the old subscription is released', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm({ raisedTime: at(30), clearedTime: at(10) })], total: 1 } });
    const { setRange } = mount(chart({ markAlarms: true }), f.hub, live(3600));
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(f.alarmSubs).toHaveBeenCalledTimes(1);
    setRange(live(900)); // 15 minutes: the alarm began before it
    await vi.waitFor(() => expect(f.alarmSubs).toHaveBeenCalledTimes(2));
    expect(f.alarmDisposed).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(lastOption().xAxis).toMatchObject({ min: NOW - 900_000, max: NOW }));
    // Clipped to the new range: the band starts at its left edge and no raise line is drawn.
    expect(helper()!.markArea!.data[0][0].xAxis).toBe(NOW - 900_000);
    expect(helper()!.markLine!.data.map((l) => l.name)).toEqual(['a1:cleared']);
  });

  it('a pause reads the range it fixes, holds it, and a resume reads the live range again', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm({ token: 'first' })], total: 1 } });
    const { setRange } = mount(chart({ markAlarms: true }), f.hub, live());
    await vi.waitFor(() => expect(listed()).toHaveLength(1));
    setRange(frozen());
    // The pause fixes the range's end, so the server is asked for exactly that range.
    await vi.waitFor(() => expect(f.alarmSubs).toHaveBeenCalledTimes(2));
    expect(f.alarmSubs.mock.calls[1][0]).toEqual({ datasource: ds, pageSize: MAX_EVENT_MARKERS, window: { spanSeconds: 3600, endAt: NOW } });
    expect(f.alarmDisposed).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => expect(listed()).toHaveLength(1));
    // While paused, a later answer for the same range is held back.
    act(() => f.alarms()!.next({ alarms: [alarm({ token: 'first' }), alarm({ token: 'second', raisedTime: at(5), clearedTime: null, state: 'ACTIVE' })], total: 2 }));
    await act(async () => {});
    expect(listed()).toHaveLength(1);
    setRange(live());
    await vi.waitFor(() => expect(f.alarmSubs).toHaveBeenCalledTimes(3));
    expect(f.alarmSubs.mock.calls[2][0]).toEqual({ datasource: ds, pageSize: MAX_EVENT_MARKERS, window: { spanSeconds: 3600 } });
    await vi.waitFor(() => expect(listed()).toHaveLength(1));
    act(() => f.alarms()!.next({ alarms: [alarm({ token: 'first' }), alarm({ token: 'second', raisedTime: at(5), clearedTime: null, state: 'ACTIVE' })], total: 2 }));
    await vi.waitFor(() => expect(listed()).toHaveLength(2));
  });

  it('a range a month back asks the server for that month, and draws what it answers', async () => {
    const MONTH = 30 * 86_400;
    const old = at(20 * 24 * 60); // 20 days ago
    const f = fakeHub({ commands: { deviceToken: 'd-1', commands: [command({ token: 'old', sentTime: old, queuedTime: old })], total: 1 } });
    mount(chart({ markCommands: true }), f.hub, live(MONTH));
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(f.commandSubs.mock.calls[0][0]).toMatchObject({ window: { spanSeconds: MONTH } });
    expect(helper()!.markLine!.data.map((l) => l.name)).toEqual(['old']);
    expect(notes()).toEqual([]);
  });
});

describe('who sees which devices', () => {
  it('only the chart device’s markers, even if the source hands back another device’s rows', async () => {
    // A member limited to some customers is refused other devices by the platform; this is the
    // second line: a row for a device the chart is not about never reaches it.
    const f = fakeHub({
      alarms: { alarms: [alarm({ token: 'mine' }), alarm({ token: 'theirs', originatorToken: 'd-2', alarmKey: 'other' })], total: 2 },
    });
    mount(chart({ markAlarms: true }), f.hub);
    await vi.waitFor(() => expect(listed()).toHaveLength(1));
    expect(listed()[0].textContent).toContain('high-temp');
    expect(document.body.textContent).not.toContain('other');
  });

  it('a chart bound to no device reads NOTHING: an unscoped alarm subscription is tenant-wide', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm({ originatorToken: 'd-9' })], total: 1 } });
    mount(chart({ markAlarms: true, markCommands: true }, { kind: 'unbound', measurements: ['temperature'] }), f.hub);
    await screen.findByTestId('echart');
    expect(f.alarmSubs).not.toHaveBeenCalled();
    expect(f.commandSubs).not.toHaveBeenCalled();
    expect(helper()).toBeUndefined();
  });
});

describe('alarm scope', () => {
  const engine = alarm({ token: 'engine', alarmKey: 'engine-overheat', metricKey: 'engine_temp_c' });
  const tyre = alarm({ token: 'tyre', alarmKey: 'tyre-pressure-low', metricKey: 'tyre_pressure_kpa', severity: 'MAJOR' });
  const unknown = alarm({ token: 'unknown', alarmKey: 'raw-cel', metricKey: '' });
  const rows = { alarms: [engine, tyre, unknown], total: 3 };
  const engineChart = { kind: 'device', deviceToken: 'd-1', measurements: ['engine_temp_c'] } as DatasourceSelector;
  const bandNames = () =>
    helper()!
      .markArea!.data.map(([a]) => (a as unknown as { name: string }).name)
      .filter((n) => n.endsWith(':span'));
  const others = () => [...document.querySelectorAll('[data-chart-events-other] [data-chart-event]')].map((n) => n.textContent);
  const drawnList = () => [...document.querySelectorAll('[data-chart-events] details:not([data-chart-events-other]) [data-chart-event]')];

  it('series: only the alarms about the charted measurement are drawn and listed as events', async () => {
    const f = fakeHub({ alarms: rows });
    mount(chart({ markAlarms: true, markAlarmsScope: 'series' }, engineChart), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(bandNames()).toEqual(['engine:span']);
    expect(drawnList().map((n) => n.textContent)).toEqual([expect.stringContaining('engine-overheat')]);
  });

  it('series: an alarm about another measurement, or none known, is listed apart and never drawn', async () => {
    const f = fakeHub({ alarms: rows });
    mount(chart({ markAlarms: true, markAlarmsScope: 'series' }, engineChart), f.hub);
    await vi.waitFor(() => expect(others()).toHaveLength(2));
    const summary = document.querySelector('[data-chart-events-other] summary');
    expect(summary?.textContent).toBe('Other alarms on this device (2)');
    expect(others().find((t) => t?.includes('tyre-pressure-low'))).toContain('about tyre_pressure_kpa');
    expect(others().find((t) => t?.includes('raw-cel'))).toContain('no measurement known');
    expect(bandNames()).not.toContain('tyre:span');
    expect(bandNames()).not.toContain('unknown:span');
  });

  it('series: with every alarm about something else, nothing is drawn but all are listed', async () => {
    const f = fakeHub({ alarms: { alarms: [tyre, unknown], total: 2 } });
    mount(chart({ markAlarms: true, markAlarmsScope: 'series' }, engineChart), f.hub);
    await vi.waitFor(() => expect(others()).toHaveLength(2));
    expect(helper()).toBeUndefined();
  });

  it('series: a chart that names no measurement takes the ones its readings carry', async () => {
    const f = fakeHub({ alarms: { alarms: [alarm({ token: 'temp', metricKey: 'temperature' }), tyre], total: 2 } });
    mount(chart({ markAlarms: true, markAlarmsScope: 'series' }, { kind: 'device', deviceToken: 'd-1' } as DatasourceSelector), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(bandNames()).toEqual(['temp:span']);
    expect(others()).toHaveLength(1);
  });

  it('series: commands are still drawn', async () => {
    const f = fakeHub({ alarms: { alarms: [tyre], total: 1 }, commands: { deviceToken: 'd-1', commands: [command()], total: 1 } });
    mount(chart({ markAlarms: true, markCommands: true, markAlarmsScope: 'series' }, engineChart), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(helper()!.markLine!.data.map((l) => l.name)).toEqual(['c1']);
    expect(others()).toHaveLength(1);
  });

  it('device: every alarm on the device is drawn, as before, and nothing is listed apart', async () => {
    const f = fakeHub({ alarms: rows });
    mount(chart({ markAlarms: true, markAlarmsScope: 'device' }, engineChart), f.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(bandNames().sort()).toEqual(['engine:span', 'tyre:span', 'unknown:span']);
    expect(document.querySelector('[data-chart-events-other]')).toBeNull();
  });

  it('an old definition with no scope draws exactly what the device scope does', async () => {
    const old = fakeHub({ alarms: rows });
    mount(chart({ markAlarms: true }, engineChart), old.hub);
    await vi.waitFor(() => expect(helper()).toBeDefined());
    expect(bandNames().sort()).toEqual(['engine:span', 'tyre:span', 'unknown:span']);
    expect(document.querySelector('[data-chart-events-other]')).toBeNull();
  });
});
