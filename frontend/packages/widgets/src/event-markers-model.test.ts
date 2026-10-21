// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The pure half of the chart's event markers: which alarms and commands count as inside the range,
// where each one is placed, what colour it takes, and the cap.

import type { AlarmRow, CommandRow } from '@devicechain/dashboards';
import type { ChartTheme } from '@devicechain/widget-sdk/echarts';
import { describe, expect, it } from 'vitest';

import {
  alarmEvents,
  alarmLevel,
  commandEvents,
  MAX_EVENT_MARKERS,
  planMarkers,
  type EventRange,
} from './event-markers-model';
import { EVENT_MARKER_STRINGS } from './event-markers-strings';

const words = EVENT_MARKER_STRINGS.en;
const T0 = Date.parse('2026-10-20T12:00:00Z');
const MIN = 60_000;
const range: EventRange = { from: T0, to: T0 + 60 * MIN };
const at = (min: number) => new Date(T0 + min * MIN).toISOString();
const fmt = (ms: number) => `t${(ms - T0) / MIN}`;

const SEVERITY = {
  warning: 'hsl(1, 1%, 1%)',
  minor: 'hsl(2, 2%, 2%)',
  major: 'hsl(3, 3%, 3%)',
  critical: 'hsl(4, 4%, 4%)',
};
const theme: ChartTheme = {
  foreground: 'fg',
  mutedForeground: 'muted',
  border: 'border',
  series: ['s1'],
  severity: SEVERITY,
};

const alarm = (over: Partial<AlarmRow> = {}): AlarmRow => ({
  token: 'a1',
  originatorType: 'device',
  originatorToken: 'd-1',
  alarmKey: 'high-temp',
  metricKey: 'temperature',
  state: 'CLEARED',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: at(10),
  clearedTime: at(20),
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
  queuedTime: at(29),
  sentTime: at(30),
  respondedTime: at(31),
  ...over,
});

interface Option {
  xAxis: { min: number; max: number };
  series: Array<{
    name?: string;
    markArea?: { data: Array<[{ name: string; xAxis: number; itemStyle: { color: string } }, { xAxis: number }]> };
    markLine?: { data: Array<{ name: string; xAxis: number; lineStyle: { color: string; type: string }; label: { show: boolean } }>; tooltip: { formatter: (p: { name: string }) => string } };
  }>;
}

const decorated = (items: ReturnType<typeof alarmEvents>, size?: { width: number; height: number }) =>
  planMarkers(items, range, theme).decorate({ series: [{ name: 'temperature', type: 'line' }], xAxis: { type: 'time' } }, size) as unknown as Option;
const helper = (o: Option) => o.series[o.series.length - 1];

describe('which alarms are inside the range', () => {
  it('keeps one raised or cleared inside, one spanning it, and one still active; drops the rest', () => {
    const rows = [
      alarm({ token: 'inside' }),
      alarm({ token: 'spans', raisedTime: at(-30), clearedTime: at(90) }),
      alarm({ token: 'active', state: 'ACTIVE', raisedTime: at(40), clearedTime: null }),
      alarm({ token: 'before', raisedTime: at(-50), clearedTime: at(-10) }),
      alarm({ token: 'after', raisedTime: at(70), clearedTime: null }),
      alarm({ token: 'unplaced', raisedTime: null }),
    ];
    expect(alarmEvents(rows, 'd-1', range, words, fmt).map((e) => e.id).sort()).toEqual(['active', 'inside', 'spans']);
  });

  it('keeps only the chart device’s alarms, whatever the source returned', () => {
    const rows = [alarm({ token: 'mine' }), alarm({ token: 'theirs', originatorToken: 'd-2' })];
    expect(alarmEvents(rows, 'd-1', range, words, fmt).map((e) => e.id)).toEqual(['mine']);
  });

  it('words the detail with severity, raise and clear, or "still active"', () => {
    const [cleared] = alarmEvents([alarm()], 'd-1', range, words, fmt);
    expect(cleared.detail).toBe('Critical · raised t10 · cleared t20');
    const [active] = alarmEvents([alarm({ clearedTime: null, state: 'ACTIVE' })], 'd-1', range, words, fmt);
    expect(active.detail).toBe('Critical · raised t10 · still active');
  });

  it('maps severities to levels; INDETERMINATE has none', () => {
    expect(['CRITICAL', 'MAJOR', 'MINOR', 'WARNING', 'INDETERMINATE'].map(alarmLevel)).toEqual(['critical', 'major', 'minor', 'warning', undefined]);
  });
});

describe('where an alarm is drawn', () => {
  it('is a band from the raise to the clear, a line at each, and the axis is fixed to the range', () => {
    const o = decorated(alarmEvents([alarm()], 'd-1', range, words, fmt));
    expect(o.xAxis).toMatchObject({ min: range.from, max: range.to });
    const [start, stop] = helper(o).markArea!.data[0];
    expect(start.xAxis).toBe(T0 + 10 * MIN);
    expect(stop.xAxis).toBe(T0 + 20 * MIN);
    const lines = helper(o).markLine!.data;
    expect(lines.map((l) => [l.name.split(':')[1], l.xAxis])).toEqual([
      ['raised', T0 + 10 * MIN],
      ['cleared', T0 + 20 * MIN],
    ]);
  });

  it('runs a band that began before the range from its start, and an active one to its end', () => {
    const o = decorated(
      alarmEvents(
        [alarm({ token: 'x', raisedTime: at(-30), clearedTime: at(15) }), alarm({ token: 'y', state: 'ACTIVE', raisedTime: at(40), clearedTime: null })],
        'd-1',
        range,
        words,
        fmt,
      ),
    );
    const bands = helper(o).markArea!.data.map(([a, b]) => [a.xAxis, b.xAxis]);
    expect(bands).toEqual([
      [range.from, T0 + 15 * MIN],
      [T0 + 40 * MIN, range.to],
    ]);
    // No raise line for the one raised before the range, none for the active one's clear.
    expect(helper(o).markLine!.data.map((l) => l.name)).toEqual(['x:cleared', 'y:raised']);
  });

  it('puts the markers on a helper series of their own and leaves the readings untouched', () => {
    const o = decorated(alarmEvents([alarm()], 'd-1', range, words, fmt));
    expect(o.series).toHaveLength(2);
    expect(o.series[0]).toEqual({ name: 'temperature', type: 'line' });
    expect((helper(o) as { data: unknown[] }).data).toEqual([]);
  });

  it('leaves the option alone when there is nothing to draw', () => {
    const option = { series: [], xAxis: {} };
    expect(planMarkers([], range, theme).decorate(option)).toBe(option);
  });
});

describe('severity colours', () => {
  it.each(['critical', 'major', 'minor', 'warning'] as const)('a %s alarm takes its severity token', (level) => {
    const [e] = alarmEvents([alarm({ severity: level.toUpperCase() })], 'd-1', range, words, fmt);
    const o = decorated([e]);
    expect(helper(o).markLine!.data[0].lineStyle.color).toBe(SEVERITY[level]);
    expect(helper(o).markArea!.data[0][0].itemStyle.color).toBe(SEVERITY[level].replace(/^hsl\(/, 'hsla(').replace(/\)$/, ', 0.1)'));
  });

  it('an INDETERMINATE alarm, and every clear line, are neutral: no severity colour at all', () => {
    const o = decorated(alarmEvents([alarm({ severity: 'INDETERMINATE' }), alarm({ token: 'b' })], 'd-1', range, words, fmt));
    const severityInks = Object.values(SEVERITY);
    const lines = helper(o).markLine!.data;
    const indeterminateRaise = lines.find((l) => l.name === 'a1:raised')!;
    expect(indeterminateRaise.lineStyle.color).toBe('muted');
    expect(helper(o).markArea!.data.find(([a]) => a.name === 'a1:span')![0].itemStyle.color).toBe('border');
    for (const l of lines.filter((x) => x.name.endsWith(':cleared'))) {
      expect(l.lineStyle.color).toBe('muted');
      expect(severityInks).not.toContain(l.lineStyle.color);
    }
  });
});

describe('commands', () => {
  it('are a dotted neutral line at the send time (the queue time when never sent), inside the range only', () => {
    const rows = [
      command(),
      command({ token: 'q', sentTime: null, queuedTime: at(5), status: 'HELD' }),
      command({ token: 'old', sentTime: at(-5) }),
    ];
    const items = commandEvents(rows, range, words, fmt);
    expect(items.map((e) => e.id)).toEqual(['c1', 'q']);
    const lines = helper(decorated(items)).markLine!.data;
    expect(lines.map((l) => [l.name, l.xAxis, l.lineStyle.type, l.lineStyle.color])).toEqual([
      ['q', T0 + 5 * MIN, 'dotted', 'muted'],
      ['c1', T0 + 30 * MIN, 'dotted', 'muted'],
    ]);
  });

  it('carry the name and the outcome where known, in the tooltip and the list text', () => {
    const items = commandEvents([command(), command({ token: 'f', status: 'FAILED' }), command({ token: 'n', status: 'NEW_STATUS' })], range, words, fmt);
    expect(items.map((e) => e.detail)).toEqual(['sent t30 · succeeded', 'sent t30 · failed', 'sent t30 · NEW_STATUS']);
    const tip = helper(decorated(items)).markLine!.tooltip.formatter({ name: 'c1' });
    expect(tip).toContain('Command: reboot');
    expect(tip).toContain('succeeded');
  });

  it('escape the name in the tooltip, which ECharts renders as HTML', () => {
    const items = commandEvents([command({ name: '<img src=x onerror=alert(1)>' })], range, words, fmt);
    const tip = helper(decorated(items)).markLine!.tooltip.formatter({ name: 'c1' });
    expect(tip).not.toContain('<img');
    expect(tip).toContain('&lt;img');
  });

  it('write their name on the canvas only on a roomy chart with few lines', () => {
    const items = commandEvents([command()], range, words, fmt);
    expect(helper(decorated(items, { width: 390, height: 240 })).markLine!.data[0].label.show).toBe(false);
    expect(helper(decorated(items, { width: 640, height: 240 })).markLine!.data[0].label.show).toBe(true);
    const many = commandEvents(Array.from({ length: 9 }, (_, i) => command({ token: `c${i}`, sentTime: at(i + 1) })), range, words, fmt);
    expect(helper(decorated(many, { width: 640, height: 240 })).markLine!.data.every((l) => !l.label.show)).toBe(true);
  });
});

describe('the cap', () => {
  const many = (n: number) =>
    alarmEvents(
      Array.from({ length: n }, (_, i) => alarm({ token: `a${String(i).padStart(3, '0')}`, raisedTime: at(i), clearedTime: null, state: 'ACTIVE' })),
      'd-1',
      range,
      words,
      fmt,
    );

  it('draws the newest MAX events and COUNTS the rest', () => {
    const plan = planMarkers(many(MAX_EVENT_MARKERS + 7), range, theme);
    expect(plan.shown).toHaveLength(MAX_EVENT_MARKERS);
    expect(plan.hidden).toBe(7);
    // The oldest seven are the ones left out; what is shown is oldest first.
    expect(plan.shown[0].id).toBe('a007');
    expect(plan.shown[plan.shown.length - 1].id).toBe(`a${String(MAX_EVENT_MARKERS + 6).padStart(3, '0')}`);
  });

  it('hides nothing at the cap exactly', () => {
    expect(planMarkers(many(MAX_EVENT_MARKERS), range, theme).hidden).toBe(0);
  });
});
