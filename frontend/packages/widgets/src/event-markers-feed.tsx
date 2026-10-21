// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The event-marker feed of a time-series chart: the alarms and commands of the chart's device inside
// the chart's range, drawn as markers (event-markers-model.ts) and listed under the chart. Loaded on
// first use (connected-widget.tsx imports it dynamically), so a board whose charts have the markers
// off never downloads it, and the ECharts pieces it draws with load only when a marker is drawn.
//
// Where the events come from: the hub's own alarm and command channels (the queries the alarm table
// and the command button already read), scoped to the chart's ONE device and to the chart's range,
// which device-management and command-delivery apply before paging. So:
//
//   - one page of MAX_EVENT_MARKERS is read per channel, and each page's total is the exact count in
//     range, so the note says exactly how many were left out, for a range of any age;
//   - a change of range, and a pause, read again (the subscription is keyed by the span and the
//     pause instant), so the note and the markers are never those of another range;
//   - a chart with no device reads nothing. It is NEVER given an unscoped subscription: on the alarm
//     channel "no datasource" means every alarm in the tenant;
//   - a paused board holds the events it showed at the pause (useHeld), as its readings are held;
//   - the alarm scope ('series', or 'device' when unset) picks which of the device's alarms are DRAWN:
//     'series' keeps those about a measurement the chart plots, and the others are listed apart
//     ("other alarms on this device"), never drawn and never dropped;
//   - a source that cannot supply a channel (a recording without alarms or commands) says so in a
//     note instead of drawing none and implying there were none.

import {
  DEFAULT_TIME_RANGE_SECONDS,
  type AlarmSnapshot,
  type CommandSnapshot,
  type MeasurementStreamState,
  type WidgetActions,
  type WidgetDataSource,
  type WidgetDescriptor,
  type WidgetInstance,
} from '@devicechain/dashboards';
import {
  css,
  fontVar,
  formatDateTime,
  formatTimestamp,
  optBoolean,
  optString,
  spaceVar,
  useWidgetTimeRange,
  useWidgetTimeZone,
} from '@devicechain/widget-sdk';
import { useChartTheme } from '@devicechain/widget-sdk/echarts';
import { useEffect, useMemo, useRef, useState, type ComponentType, type ReactNode } from 'react';

import { measurementSpan } from './binding-policy';
import {
  alarmEvents,
  alarmScope,
  commandEvents,
  MAX_EVENT_MARKERS,
  planMarkers,
  splitByScope,
  type EventItem,
  type EventRange,
} from './event-markers-model';
import { eventMarkerStrings, type EventKind } from './event-markers-strings';
import type { EventMarkersView } from './event-markers-types';
import { useHeld } from './use-held';

const DAY_MS = 86_400_000;

export interface EventMarkersFeedProps {
  hub: WidgetDataSource;
  widget: WidgetInstance;
  descriptor: WidgetDescriptor;
  data: MeasurementStreamState;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: ComponentType<any>;
  actions: WidgetActions | undefined;
}

// One channel's latest answer, tagged with the subscription it answers: a render that carries a new
// key must not show the previous key's rows, so a mismatch reads as loading.
interface Channel<S> {
  key: string;
  value?: S;
  error?: unknown;
}

interface Sink<S> {
  next: (value: S) => void;
  error?: (err: unknown) => void;
}

function useChannel<S>(subscribe: ((sink: Sink<S>) => () => void) | null, key: string, paused: boolean): Channel<S> {
  const [state, setState] = useState<Channel<S>>({ key: '' });
  const subscribeRef = useRef(subscribe);
  subscribeRef.current = subscribe;
  const enabled = subscribe !== null;
  useEffect(() => {
    const open = subscribeRef.current;
    if (!open) return;
    return open({
      next: (value) => setState({ key, value }),
      // A later error keeps the rows already read: the channel polls and heals.
      error: (error) => setState((prev) => ({ key, value: prev.key === key ? prev.value : undefined, error })),
    });
  }, [key, enabled]);
  const live: Channel<S> = enabled && state.key === key ? state : { key };
  return useHeld(live, paused, (c) => c.value !== undefined || c.error !== undefined);
}

const isNotInRecording = (err: unknown): boolean => err instanceof Error && err.name === 'NotInRecordingError';

export default function EventMarkersFeed({ hub, widget, descriptor, data, Component, actions }: EventMarkersFeedProps) {
  const range = useWidgetTimeRange();
  const timeZone = useWidgetTimeZone();
  const theme = useChartTheme();
  const words = eventMarkerStrings();
  const now = useMemo(() => (hub.now ? () => hub.now!() : Date.now), [hub]);
  const spanSeconds = measurementSpan(descriptor, widget, range?.spanSeconds) ?? DEFAULT_TIME_RANGE_SECONDS;
  const paused = range?.paused === true;
  const pausedAt = paused ? range?.pausedAt : undefined;
  const ds = widget.datasource;
  const bound = ds !== undefined && ds.kind !== 'unbound';
  const wantAlarms = bound && optBoolean(widget.options, 'markAlarms') === true;
  const scope = alarmScope(optString(widget.options, 'markAlarmsScope'));
  const wantCommands = bound && optBoolean(widget.options, 'markCommands') === true;

  // The range is part of the key: a new span, or a pause (which fixes the range's end), reads
  // again, and the answer for one range is never shown under another.
  const key = JSON.stringify([ds ?? null, spanSeconds, pausedAt ?? null]);
  const window = { spanSeconds, ...(pausedAt !== undefined ? { endAt: pausedAt } : {}) };
  const alarms = useChannel<AlarmSnapshot>(
    wantAlarms ? (sink) => hub.subscribeAlarms({ datasource: ds, pageSize: MAX_EVENT_MARKERS, window }, sink) : null,
    `a${key}`,
    paused,
  );
  const commands = useChannel<CommandSnapshot>(
    wantCommands ? (sink) => hub.subscribeCommands({ datasource: ds, pageSize: MAX_EVENT_MARKERS, window }, sink) : null,
    `c${key}`,
    paused,
  );

  const view = useMemo((): EventMarkersView | undefined => {
    if (!wantAlarms && !wantCommands) return undefined;
    const to = pausedAt ?? now();
    const span: EventRange = { from: to - spanSeconds * 1000, to };
    const fmt = (at: number): string =>
      spanSeconds * 1000 > DAY_MS
        ? formatDateTime(new Date(at).toISOString(), timeZone)
        : formatTimestamp(new Date(at).toISOString(), timeZone);

    // What the chart plots: the measurements it names, else those its readings carry.
    const named = ds?.measurements ?? [];
    const charted = new Set<string>(named.length > 0 ? named : data.samples.map((s) => s.name));

    const deviceToken = ds?.kind === 'device' ? ds.deviceToken : undefined;
    const found: EventItem[] = [];
    const notes: string[] = [];
    let unloaded = 0;

    const channel = (kind: EventKind, c: Channel<unknown>): void => {
      if (c.value === undefined && c.error !== undefined) {
        notes.push(isNotInRecording(c.error) ? words.notInRecording[kind] : words.failed[kind]);
      }
    };
    if (wantAlarms) {
      channel('alarm', alarms);
      const snap = alarms.value;
      if (snap) {
        found.push(...alarmEvents(snap.alarms, deviceToken, span, words, fmt));
        // The source counted the range before paging, so the rows past the page are exact.
        unloaded += Math.max(0, snap.total - snap.alarms.length);
      }
    }
    if (wantCommands) {
      channel('command', commands);
      const snap = commands.value;
      if (snap) {
        found.push(...commandEvents(snap.commands, span, words, fmt));
        unloaded += Math.max(0, snap.total - snap.commands.length);
      }
    }

    const { drawn, other } = splitByScope(found, scope, charted);
    const plan = planMarkers(drawn, span, theme);
    const more = plan.hidden + unloaded;
    if (more > 0) notes.push(words.more(more));
    const listed = other.map((e) => ({
      ...e,
      detail: `${e.detail} · ${e.metric ? words.aboutMeasurement(e.metric) : words.noMeasurement}`,
    }));
    return { decorate: plan.decorate, footer: footerOf(plan.shown, listed, notes, theme.severity) };
    // `ds` is read through the keys (value identity). `alarms`/`commands` are the channels' values.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantAlarms, wantCommands, scope, alarms, commands, data.samples, theme, words, spanSeconds, pausedAt, now, timeZone, key]);

  return <Component widget={widget} data={data} actions={actions} markers={view} />;
}

const FOOTER_STYLE = {
  flex: 'none',
  fontSize: fontVar('xs'),
  color: css('muted-foreground'),
  padding: `0 ${spaceVar(2)}`,
} as const;

function footerOf(
  shown: readonly EventItem[],
  other: readonly EventItem[],
  notes: readonly string[],
  severity: Record<string, string>,
): ReactNode {
  if (shown.length === 0 && other.length === 0 && notes.length === 0) return null;
  const words = eventMarkerStrings();
  return (
    <div data-chart-events="" style={FOOTER_STYLE}>
      {notes.map((note) => (
        <div key={note} data-chart-events-note="">
          {note}
        </div>
      ))}
      {shown.length > 0 && <EventList events={shown} label={words.events(shown.length)} severity={severity} />}
      {other.length > 0 && <EventList events={other} label={words.otherAlarms(other.length)} severity={severity} other />}
    </div>
  );
}

function EventList({
  events,
  label,
  severity,
  other,
}: {
  events: readonly EventItem[];
  label: string;
  severity: Record<string, string>;
  other?: boolean;
}) {
  return (
    // A native disclosure: reachable and operable from the keyboard, with the canvas's tooltips
    // as its pointer-and-touch counterpart.
    <details data-chart-events-other={other ? '' : undefined}>
      <summary style={{ cursor: 'pointer' }}>{label}</summary>
      {/* The list scrolls past a few rows, so it takes focus: a keyboard can only scroll what it can reach. */}
      <ul tabIndex={0} aria-label={label} style={{ margin: 0, padding: 0, listStyle: 'none', maxHeight: 96, overflowY: 'auto' }}>
        {[...events].reverse().map((e) => (
          <li key={e.id} data-chart-event={e.kind} style={{ display: 'flex', gap: spaceVar(1), alignItems: 'baseline' }}>
            <span
              aria-hidden="true"
              style={{
                flex: 'none',
                width: 8,
                height: 8,
                background: e.level ? severity[e.level] : css('muted-foreground'),
                borderRadius: e.kind === 'command' ? 4 : 0,
              }}
            />
            <span>
              {e.title} · {e.detail}
            </span>
          </li>
        ))}
      </ul>
    </details>
  );
}
