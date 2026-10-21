// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The event-marker feed of a time-series chart: the alarms and commands of the chart's device inside
// the chart's range, drawn as markers (event-markers-model.ts) and listed under the chart. Loaded on
// first use (connected-widget.tsx imports it dynamically), so a board whose charts have the markers
// off never downloads it, and the ECharts pieces it draws with load only when a marker is drawn.
//
// Where the events come from: the hub's own alarm and command channels (the queries the alarm table
// and the command button already read), scoped to the chart's ONE device. Neither query takes a time
// range, so the newest page of each is read and filtered here. That is why:
//
//   - a page that comes back full and still reaches into the range is NOT passed off as the whole
//     range: the note says "up to N more not shown";
//   - a change of range reads again (the subscription is keyed by the span), so the note and the
//     markers are never those of the previous range;
//   - a chart with no device reads nothing. It is NEVER given an unscoped subscription: on the alarm
//     channel "no datasource" means every alarm in the tenant;
//   - a paused board holds the events it showed at the pause (useHeld), as its readings are held;
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
  spaceVar,
  useWidgetTimeRange,
  useWidgetTimeZone,
} from '@devicechain/widget-sdk';
import { useChartTheme } from '@devicechain/widget-sdk/echarts';
import { useEffect, useMemo, useRef, useState, type ComponentType, type ReactNode } from 'react';

import { measurementSpan } from './binding-policy';
import {
  alarmEvents,
  commandEvents,
  EVENT_FETCH_ROWS,
  planMarkers,
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

// The oldest time among rows, or Infinity when none has one.
function oldest(times: Array<number | null>): number {
  let min = Infinity;
  for (const t of times) if (t !== null && t < min) min = t;
  return min;
}

const when = (iso: string | null): number | null => {
  const t = iso ? Date.parse(iso) : Number.NaN;
  return Number.isNaN(t) ? null : t;
};

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
  const wantCommands = bound && optBoolean(widget.options, 'markCommands') === true;

  // The span is part of the key: a new range reads again, and the answer for the old one is never
  // shown under the new one.
  const key = JSON.stringify([ds ?? null, spanSeconds]);
  const alarms = useChannel<AlarmSnapshot>(
    wantAlarms ? (sink) => hub.subscribeAlarms({ datasource: ds, pageSize: EVENT_FETCH_ROWS }, sink) : null,
    `a${key}`,
    paused,
  );
  const commands = useChannel<CommandSnapshot>(
    wantCommands ? (sink) => hub.subscribeCommands({ datasource: ds, pageSize: EVENT_FETCH_ROWS }, sink) : null,
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

    const deviceToken = ds?.kind === 'device' ? ds.deviceToken : undefined;
    const items: EventItem[] = [];
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
        items.push(...alarmEvents(snap.alarms, deviceToken, span, words, fmt));
        // A full page that still reaches into the range may have older events behind it.
        if (snap.alarms.length >= EVENT_FETCH_ROWS && snap.total > snap.alarms.length) {
          if (oldest(snap.alarms.map((r) => when(r.raisedTime))) >= span.from) unloaded += snap.total - snap.alarms.length;
        }
      }
    }
    if (wantCommands) {
      channel('command', commands);
      const snap = commands.value;
      if (snap) {
        items.push(...commandEvents(snap.commands, span, words, fmt));
        if (snap.commands.length >= EVENT_FETCH_ROWS && snap.total > snap.commands.length) {
          if (oldest(snap.commands.map((r) => when(r.sentTime) ?? when(r.queuedTime))) >= span.from) {
            unloaded += snap.total - snap.commands.length;
          }
        }
      }
    }

    const plan = planMarkers(items, span, theme);
    const more = plan.hidden + unloaded;
    if (more > 0) notes.push(unloaded > 0 ? words.moreUpTo(more) : words.more(more));
    return { decorate: plan.decorate, footer: footerOf(plan.shown, notes, theme.severity) };
    // `ds` is read through the keys (value identity). `alarms`/`commands` are the channels' values.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantAlarms, wantCommands, alarms, commands, data.samples, theme, words, spanSeconds, pausedAt, now, timeZone, key]);

  return <Component widget={widget} data={data} actions={actions} markers={view} />;
}

const FOOTER_STYLE = {
  flex: 'none',
  fontSize: fontVar('xs'),
  color: css('muted-foreground'),
  padding: `0 ${spaceVar(2)}`,
} as const;

function footerOf(shown: readonly EventItem[], notes: readonly string[], severity: Record<string, string>): ReactNode {
  if (shown.length === 0 && notes.length === 0) return null;
  const words = eventMarkerStrings();
  return (
    <div data-chart-events="" style={FOOTER_STYLE}>
      {notes.map((note) => (
        <div key={note} data-chart-events-note="">
          {note}
        </div>
      ))}
      {shown.length > 0 && (
        // A native disclosure: reachable and operable from the keyboard, with the canvas's tooltips
        // as its pointer-and-touch counterpart.
        <details>
          <summary style={{ cursor: 'pointer' }}>{words.events(shown.length)}</summary>
          <ul style={{ margin: 0, padding: 0, listStyle: 'none', maxHeight: 96, overflowY: 'auto' }}>
            {[...shown].reverse().map((e) => (
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
      )}
    </div>
  );
}
