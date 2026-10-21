// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The event markers of a time-series chart as plain data: which alarms and commands fall in the
// chart's range, which of them are drawn, and the ECharts pieces that draw them. Pure (no React, no
// ECharts import), and part of the markers' lazy chunk (event-markers-feed.tsx).
//
//   - An ALARM is a span: a faint band from the raise to the clear (or to the end of the range while
//     it is still active), with a solid line at the raise. The span takes its severity's colour; an
//     INDETERMINATE alarm and the clear line are neutral, because colour means severity and
//     "back to normal" has none (the ISA-101 rule the threshold bands follow).
//   - A COMMAND is a dotted neutral line at the time it was sent (queued, when it never was), named
//     by the command, with its delivery outcome where it has one.
//   - At most MAX_EVENT_MARKERS events are drawn, the newest ones; the rest are COUNTED, never
//     dropped in silence (the feed prints the count).
//   - The markers sit on a helper series of their own, so the reading series are untouched.

import type { AlarmRow, CommandRow } from '@devicechain/dashboards';
import { levelColor, type AlarmLevel } from '@devicechain/widget-sdk';
import { severityFill, type ChartTheme, type EChartOption } from '@devicechain/widget-sdk/echarts';

import type { EventKind, EventMarkerStrings } from './event-markers-strings';

// The most events drawn on one chart. Past it the chart is a comb, not a timeline.
export const MAX_EVENT_MARKERS = 30;

// The opacity of an alarm's span behind the readings: a zone, not a wall.
const SPAN_ALPHA = 0.1;
// Labels are written on the canvas only on a roomy chart with few lines; the tooltip and the list
// carry the same text everywhere.
const LABEL_MIN_WIDTH = 480;
const LABEL_MAX_LINES = 8;

export interface EventRange {
  from: number; // epoch ms
  to: number;
}

export interface EventItem {
  id: string;
  kind: EventKind;
  // The raise time of an alarm; the send time of a command.
  at: number;
  // An alarm's clear time; null while it is active (and always null for a command).
  end: number | null;
  level: AlarmLevel | undefined;
  // The command's name, or the alarm's key.
  name: string;
  title: string;
  detail: string;
}

const LEVELS: Record<string, AlarmLevel> = { CRITICAL: 'critical', MAJOR: 'major', MINOR: 'minor', WARNING: 'warning' };

// INDETERMINATE (and any severity a newer platform adds) has no level: it draws neutral.
export function alarmLevel(severity: string): AlarmLevel | undefined {
  return LEVELS[severity.toUpperCase()];
}

function ms(iso: string | null): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t;
}

// The alarms that overlap the range, as events. A row with no raise time cannot be placed and is
// skipped; with `deviceToken`, only that device's rows are kept (the platform already limits a
// viewer to the devices they may see, and this keeps another device's alarm off this device's chart
// whatever a source returns).
export function alarmEvents(
  rows: readonly AlarmRow[],
  deviceToken: string | undefined,
  range: EventRange,
  words: EventMarkerStrings,
  fmt: (at: number) => string,
): EventItem[] {
  const out: EventItem[] = [];
  for (const r of rows) {
    if (deviceToken !== undefined && r.originatorToken !== deviceToken) continue;
    const at = ms(r.raisedTime);
    if (at === null) continue;
    const end = ms(r.clearedTime);
    if (at > range.to || (end ?? Infinity) < range.from) continue;
    const sev = words.severity[r.severity.toUpperCase()] ?? r.severity;
    out.push({
      id: r.token,
      kind: 'alarm',
      at,
      end,
      level: alarmLevel(r.severity),
      name: r.alarmKey,
      title: `${words.alarm}: ${r.alarmKey}`,
      detail: [sev, words.raised(fmt(at)), end === null ? words.stillActive : words.cleared(fmt(end))].join(' · '),
    });
  }
  return out;
}

// The commands sent inside the range, as events.
export function commandEvents(
  rows: readonly CommandRow[],
  range: EventRange,
  words: EventMarkerStrings,
  fmt: (at: number) => string,
): EventItem[] {
  const out: EventItem[] = [];
  for (const r of rows) {
    const at = ms(r.sentTime) ?? ms(r.queuedTime);
    if (at === null || at < range.from || at > range.to) continue;
    out.push({
      id: r.token,
      kind: 'command',
      at,
      end: null,
      level: undefined,
      name: r.name,
      title: `${words.command}: ${r.name}`,
      detail: [words.sent(fmt(at)), words.outcome[r.status] ?? r.status].join(' · '),
    });
  }
  return out;
}

export interface MarkerPlan {
  // The events drawn, oldest first.
  shown: EventItem[];
  // Events in the range that were not drawn because of the cap.
  hidden: number;
  decorate: (option: EChartOption, size?: { width: number; height: number }) => EChartOption;
}

const HTML_ESCAPES: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const escapeHtml = (s: string): string => s.replace(/[&<>"']/g, (c) => HTML_ESCAPES[c]);

export function planMarkers(items: readonly EventItem[], range: EventRange, theme: ChartTheme): MarkerPlan {
  // Newest first, so the cap keeps what just happened; drawn and listed oldest first.
  const byNewest = [...items].sort((a, b) => b.at - a.at || (a.id < b.id ? -1 : 1));
  const shown = byNewest.slice(0, MAX_EVENT_MARKERS).reverse();
  const hidden = byNewest.length - shown.length;

  const neutral = theme.mutedForeground;
  const tips: Record<string, string> = {};
  const lines: Record<string, unknown>[] = [];
  const areas: unknown[] = [];
  for (const e of shown) {
    const tip = `<b>${escapeHtml(e.title)}</b><br/>${escapeHtml(e.detail)}`;
    if (e.kind === 'command') {
      tips[e.id] = tip;
      lines.push({ name: e.id, xAxis: e.at, lineStyle: { color: neutral, type: 'dotted', width: 2 }, labelText: e.name });
      continue;
    }
    const ink = levelColor(theme.severity, e.level) ?? neutral;
    const start = Math.max(e.at, range.from);
    const stop = Math.min(e.end ?? range.to, range.to);
    tips[`${e.id}:span`] = tip;
    areas.push([
      { name: `${e.id}:span`, xAxis: start, itemStyle: { color: e.level ? severityFill(theme, e.level, SPAN_ALPHA) : theme.border } },
      { xAxis: stop },
    ]);
    if (e.at >= range.from) {
      tips[`${e.id}:raised`] = tip;
      lines.push({ name: `${e.id}:raised`, xAxis: e.at, lineStyle: { color: ink, type: 'solid', width: 2 } });
    }
    if (e.end !== null && e.end >= range.from && e.end <= range.to) {
      tips[`${e.id}:cleared`] = tip;
      lines.push({ name: `${e.id}:cleared`, xAxis: e.end, lineStyle: { color: neutral, type: 'dashed', width: 1 } });
    }
  }

  const tooltip = { trigger: 'item', formatter: (p: { name?: string }) => tips[p.name ?? ''] ?? '' };

  const decorate: MarkerPlan['decorate'] = (option, size) => {
    if (lines.length === 0 && areas.length === 0) return option;
    const base = option as { series?: unknown; xAxis?: object; legend?: object; textStyle?: { fontSize?: number } };
    const series = Array.isArray(base.series) ? base.series : base.series ? [base.series] : [];
    const labelled = (size?.width ?? 0) >= LABEL_MIN_WIDTH && lines.length <= LABEL_MAX_LINES;
    const fontSize = base.textStyle?.fontSize ?? 10;
    const lineData = lines.map(({ labelText, ...line }) =>
      labelled && typeof labelText === 'string'
        ? { ...line, label: { show: true, position: 'insideEndTop', rotate: 90, color: neutral, fontSize, formatter: labelText } }
        : { ...line, label: { show: false } },
    );
    return {
      ...option,
      xAxis: { ...base.xAxis, min: range.from, max: range.to },
      // The legend lists the readings, not the helper series that carries the markers.
      ...(base.legend ? { legend: { ...base.legend, data: series.map((s: { name?: string }) => s.name) } } : {}),
      series: [
        ...series,
        {
          type: 'line',
          name: '',
          data: [],
          silent: false,
          z: 1,
          markArea: { silent: false, animation: false, label: { show: false }, tooltip, data: areas },
          markLine: { silent: false, animation: false, symbol: 'none', tooltip, data: lineData },
        },
      ],
    };
  };

  return { shown, hidden, decorate };
}
