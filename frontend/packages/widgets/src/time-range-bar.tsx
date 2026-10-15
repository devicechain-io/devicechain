// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's time-range control: the presets, a pause, and the "Paused" indicator. One component
// for every host (the console viewer, the /dash app), so the two cannot disagree about what a
// frozen board looks like. It is a plain controlled bar -- the host owns the state through
// useBoardTimeRange and feeds it to the renderer as `timeRange` -- and it carries no copy of its
// own beyond English defaults: a host passes its translations as `labels`.

import {
  DEFAULT_TIME_RANGE_SECONDS,
  TIME_RANGE_PRESETS,
  initialTimeRange,
  isValidSpanSeconds,
  pauseRange,
  presetIdOf,
  resumeRange,
  spanLabel,
  withSpan,
  type TimeRangeState,
} from '@devicechain/dashboards';
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';

export interface TimeRangeControls {
  setSpan(spanSeconds: number): void;
  pause(): void;
  resume(): void;
}

// useBoardTimeRange holds the board's range for a host: the definition's default span, or the
// deep link's, and live unless the link says paused. `defaultSeconds` is read once, at mount; a
// host that swaps boards remounts (the console and /dash both do), which is when it should reset.
export function useBoardTimeRange(
  defaultSeconds: number | undefined,
  link: { spanSeconds?: number; paused?: boolean } = {},
): [TimeRangeState, TimeRangeControls] {
  const [state, setState] = useState<TimeRangeState>(() => initialTimeRange(defaultSeconds, link));
  // An author who changes the board's default while the viewer is open expects the board to follow;
  // a viewer's own pick is only overridden when the DEFAULT moves, never by re-rendering.
  const lastDefault = useRef(defaultSeconds);
  useEffect(() => {
    if (lastDefault.current === defaultSeconds) return;
    lastDefault.current = defaultSeconds;
    setState((s) => withSpan(s, isValidSpanSeconds(defaultSeconds) ? defaultSeconds : DEFAULT_TIME_RANGE_SECONDS));
  }, [defaultSeconds]);
  const controls = useMemo<TimeRangeControls>(
    () => ({
      setSpan: (spanSeconds) => setState((s) => withSpan(s, spanSeconds)),
      pause: () => setState((s) => pauseRange(s, Date.now())),
      resume: () => setState((s) => resumeRange(s)),
    }),
    [],
  );
  return [state, controls];
}

export interface TimeRangeLabels {
  range: string;
  pause: string;
  resume: string;
  paused: string;
  // "Paused at 14:03:22".
  pausedAt: (time: string) => string;
  // Printed inside the pill: a pause freezes what the board SHOWS, never what it DOES. Commands
  // (and alarm acknowledge / clear) still go out at once, and their status keeps updating, so an
  // operator who acts on a frozen board is told the action is live.
  actionsLive: string;
}

export const DEFAULT_TIME_RANGE_LABELS: TimeRangeLabels = {
  range: 'Time range',
  pause: 'Pause',
  resume: 'Resume',
  paused: 'Paused',
  pausedAt: (time) => `Paused at ${time}`,
  actionsLive: 'commands still send',
};

const button = (active: boolean): CSSProperties => ({
  padding: '2px 8px',
  fontSize: 12,
  lineHeight: '18px',
  cursor: 'pointer',
  color: active ? 'hsl(var(--background))' : 'hsl(var(--foreground))',
  background: active ? 'hsl(var(--foreground))' : 'transparent',
  border: '1px solid hsl(var(--border))',
  borderRadius: 4,
});

// The pill is deliberately loud: filled, bordered and spelled out, because a frozen board that
// reads as live is the failure this slice exists to prevent. Exported so a kiosk wall (which has
// no bar) can show it alone.
export function PausedIndicator({
  pausedAt,
  labels = DEFAULT_TIME_RANGE_LABELS,
}: {
  pausedAt: number | undefined;
  labels?: TimeRangeLabels;
}) {
  const time = pausedAt === undefined ? undefined : new Date(pausedAt).toLocaleTimeString();
  return (
    <span
      role="status"
      data-time-range-paused=""
      title={time ? labels.pausedAt(time) : labels.paused}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        padding: '2px 8px',
        fontSize: 12,
        fontWeight: 700,
        letterSpacing: 0.4,
        textTransform: 'uppercase',
        color: 'hsl(var(--background))',
        background: 'hsl(var(--foreground))',
        border: '2px solid hsl(var(--foreground))',
        borderRadius: 4,
      }}
    >
      <span aria-hidden="true">{'⏸'}</span>
      {labels.paused}
      {time ? <span style={{ fontWeight: 400, textTransform: 'none' }}>{` ${time}`}</span> : null}
      <span data-time-range-actions-live="" style={{ fontWeight: 400, textTransform: 'none' }}>
        {`· ${labels.actionsLive}`}
      </span>
    </span>
  );
}

export function TimeRangeBar({
  state,
  controls,
  labels = DEFAULT_TIME_RANGE_LABELS,
}: {
  state: TimeRangeState;
  controls: TimeRangeControls;
  labels?: TimeRangeLabels;
}) {
  const preset = presetIdOf(state.spanSeconds);
  const onPause = useCallback(() => (state.paused ? controls.resume() : controls.pause()), [state.paused, controls]);
  return (
    <div
      data-time-range-bar=""
      role="group"
      aria-label={labels.range}
      style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}
    >
      {/* A board default that is none of the presets (two hours, say) is still the selected span,
          shown under its own label rather than silently looking like another preset. */}
      {preset === undefined ? (
        <button type="button" aria-pressed="true" data-range={spanLabel(state.spanSeconds)} style={button(true)}>
          {spanLabel(state.spanSeconds)}
        </button>
      ) : null}
      {TIME_RANGE_PRESETS.map((p) => (
        <button
          key={p.id}
          type="button"
          aria-pressed={p.id === preset}
          data-range={p.id}
          onClick={() => controls.setSpan(p.seconds)}
          style={button(p.id === preset)}
        >
          {p.id}
        </button>
      ))}
      <button type="button" aria-pressed={state.paused} data-time-range-pause="" onClick={onPause} style={button(state.paused)}>
        {state.paused ? labels.resume : labels.pause}
      </button>
      {state.paused ? <PausedIndicator pausedAt={state.pausedAt} labels={labels} /> : null}
    </div>
  );
}
