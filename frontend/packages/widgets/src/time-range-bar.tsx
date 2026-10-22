// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's time-range control: the presets, a pause, the "Paused" indicator, and a custom
// from-to window (picked in a lazily loaded panel, or opened from a `?from=&to=` link). One
// component for every host (the console viewer, the /dash app), so the two cannot disagree about
// what a frozen board looks like. It is a plain controlled bar -- the host owns the state through
// useBoardTimeRange and feeds it to the renderer as `timeRange` -- and it carries no copy of its
// own beyond English defaults: a host passes its translations as `labels`.

import {
  DEFAULT_TIME_RANGE_SECONDS,
  TIME_RANGE_PRESETS,
  absoluteRange,
  absoluteToParams,
  initialTimeRange,
  isValidSpanSeconds,
  leaveAbsolute,
  pauseRange,
  presetIdOf,
  resumeRange,
  spanLabel,
  withSpan,
  type AbsoluteLinkRejection,
  type AbsoluteRange,
  type AbsoluteRangeProblem,
  type TimeRangeState,
} from '@devicechain/dashboards';
import { useCallback, useEffect, useMemo, useRef, useState, type ComponentType, type CSSProperties } from 'react';
import type { AbsolutePickerLabels } from './absolute-range-picker';

export interface TimeRangeControls {
  setSpan(spanSeconds: number): void;
  pause(): void;
  resume(): void;
  // Show a fixed from-to window (epoch ms). Returns why it was refused, or undefined when taken.
  setAbsolute(fromMs: number, toMs: number): AbsoluteRangeProblem | undefined;
}

// useBoardTimeRange holds the board's range for a host: the definition's default span, or the
// deep link's (a preset span, a frozen instant, or an absolute window), and live unless the link
// says otherwise. `defaultSeconds` is read once, at mount; a host that swaps boards remounts (the
// console and /dash both do), which is when it should reset.
export function useBoardTimeRange(
  defaultSeconds: number | undefined,
  link: { spanSeconds?: number; paused?: boolean; absolute?: AbsoluteRange } = {},
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
      // A preset click while a fixed window is showing leaves it: back to live at that span.
      setSpan: (spanSeconds) => setState((s) => withSpan(leaveAbsolute(s), spanSeconds)),
      pause: () => setState((s) => pauseRange(s, Date.now())),
      resume: () => setState((s) => resumeRange(s)),
      setAbsolute: (fromMs, toMs) => {
        const made = absoluteRange(fromMs, toMs);
        if ('problem' in made) return made.problem;
        setState(made.state);
        return undefined;
      },
    }),
    [],
  );
  return [state, controls];
}

export interface AbsoluteLabels extends AbsolutePickerLabels {
  // "Fixed window" -- what the indicator says in place of "Paused".
  fixed: string;
  // The button that leaves a fixed window for the live board.
  backToLive: string;
  copyLink: string;
  linkCopied: string;
  // The picker's chunk could not be fetched.
  loadFailed: string;
  dismiss: string;
  // The note shown when a `?from=&to=` link was ignored. `reason` is the problem; `raw` the values.
  ignored: (reason: string, raw: string) => string;
  incomplete: string;
  // The link carried a date-time with no zone, or one that does not parse.
  unzoned: string;
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
  // The custom-window strings. Optional so a host written before the absolute range keeps compiling;
  // the English defaults fill any it leaves out.
  absolute?: Partial<AbsoluteLabels>;
}

export const DEFAULT_ABSOLUTE_LABELS: AbsoluteLabels = {
  custom: 'Custom range',
  from: 'From',
  to: 'To',
  apply: 'Apply',
  cancel: 'Cancel',
  zone: (zone, offset) => `Times are in ${zone} (${offset}). Shared links use UTC.`,
  problem: {
    unparseable: 'Enter both a start and an end.',
    order: 'The start must be before the end.',
    tooShort: 'The window must be at least one minute.',
    tooLong: 'The window can be at most 31 days.',
    future: 'The end cannot be in the future.',
    tooEarly: 'The start is too far in the past.',
  },
  fixed: 'Fixed window',
  backToLive: 'Back to live',
  copyLink: 'Copy link',
  linkCopied: 'Link copied',
  loadFailed: 'The range picker could not be loaded. Try again.',
  dismiss: 'Dismiss',
  ignored: (reason, raw) => `The link's time window was ignored: ${reason} (${raw}). Showing the live board.`,
  incomplete: 'both from and to are required',
  unzoned: 'times must be ISO 8601 with a zone, such as 2026-10-22T02:00:00Z or +02:00',
};

export const DEFAULT_TIME_RANGE_LABELS: TimeRangeLabels = {
  range: 'Time range',
  pause: 'Pause',
  resume: 'Resume',
  paused: 'Paused',
  pausedAt: (time) => `Paused at ${time}`,
  actionsLive: 'commands still send',
};

const absoluteLabels = (labels: TimeRangeLabels): AbsoluteLabels => ({ ...DEFAULT_ABSOLUTE_LABELS, ...labels.absolute });

// The note text for a deep link whose `from`/`to` were unusable.
export function rejectedLinkNote(rejected: AbsoluteLinkRejection, labels: TimeRangeLabels = DEFAULT_TIME_RANGE_LABELS): string {
  const l = absoluteLabels(labels);
  const raw = `from=${rejected.from || '-'} to=${rejected.to || '-'}`;
  const reason =
    rejected.reason === 'incomplete' ? l.incomplete : rejected.reason === 'unparseable' ? l.unzoned : l.problem[rejected.reason];
  return l.ignored(reason, raw);
}

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

// A window as text, each end WITH its zone: "Oct 22, 2026, 2:00 AM CEST - Oct 22, 2026, 4:00 AM CEST".
export function formatAbsoluteRange(a: AbsoluteRange): string {
  const f = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' });
  return `${f.format(a.fromMs)} – ${f.format(a.toMs)}`;
}

// What stands in for the Paused pill while a fixed window shows. Same loudness, different words:
// "Paused" would be wrong (nothing was paused, a window was chosen), and the live tail is off for
// the same reason either way. Exported for the kiosk, like PausedIndicator.
export function AbsoluteIndicator({
  absolute,
  labels = DEFAULT_TIME_RANGE_LABELS,
}: {
  absolute: AbsoluteRange;
  labels?: TimeRangeLabels;
}) {
  const l = absoluteLabels(labels);
  const utc = absoluteToParams(absolute);
  return (
    <span
      role="status"
      data-time-range-absolute=""
      title={`${utc.from} – ${utc.to} (UTC)`}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 4,
        padding: '2px 8px',
        fontSize: 12,
        fontWeight: 700,
        color: 'hsl(var(--background))',
        background: 'hsl(var(--foreground))',
        border: '2px solid hsl(var(--foreground))',
        borderRadius: 4,
      }}
    >
      <span style={{ letterSpacing: 0.4, textTransform: 'uppercase' }}>{l.fixed}</span>
      <span data-time-range-window="" style={{ fontWeight: 400 }}>
        {formatAbsoluteRange(absolute)}
      </span>
      <span data-time-range-actions-live="" style={{ fontWeight: 400 }}>
        {`· ${labels.actionsLive}`}
      </span>
    </span>
  );
}

// The shareable address of the window: the current page with `from`/`to` in UTC, and the rolling
// parameters dropped (an absolute window wins over them, so leaving them would only mislead).
export function absoluteLinkFor(href: string, a: AbsoluteRange): string {
  const url = new URL(href);
  const { from, to } = absoluteToParams(a);
  url.searchParams.delete('range');
  url.searchParams.delete('paused');
  url.searchParams.set('from', from);
  url.searchParams.set('to', to);
  return url.toString();
}

type PickerProps = {
  initial?: AbsoluteRange;
  labels: AbsolutePickerLabels;
  onApply: (fromMs: number, toMs: number) => AbsoluteRangeProblem | undefined;
  onCancel: () => void;
};

export function TimeRangeBar({
  state,
  controls,
  labels = DEFAULT_TIME_RANGE_LABELS,
  notice,
}: {
  state: TimeRangeState;
  controls: TimeRangeControls;
  labels?: TimeRangeLabels;
  // A host's note about its deep link (an ignored `?from=&to=`), shown dismissibly under the bar.
  notice?: string;
}) {
  const abs = state.absolute;
  const preset = abs ? undefined : presetIdOf(state.spanSeconds);
  const onPause = useCallback(() => (state.paused ? controls.resume() : controls.pause()), [state.paused, controls]);
  const l = absoluteLabels(labels);

  // The picker is fetched on the first click, as its own chunk.
  const [Picker, setPicker] = useState<ComponentType<PickerProps> | undefined>();
  const [open, setOpen] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [copied, setCopied] = useState(false);
  const [noticeShown, setNoticeShown] = useState(true);
  useEffect(() => setNoticeShown(true), [notice]);

  const openPicker = () => {
    if (open) {
      setOpen(false);
      return;
    }
    setLoadFailed(false);
    if (Picker) {
      setOpen(true);
      return;
    }
    import('./absolute-range-picker').then(
      (m) => {
        setPicker(() => m.default);
        setOpen(true);
      },
      () => setLoadFailed(true),
    );
  };

  const copyLink = () => {
    if (!abs) return;
    navigator.clipboard?.writeText(absoluteLinkFor(window.location.href, abs)).then(
      () => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 2000);
      },
      () => undefined,
    );
  };

  return (
    <div style={{ position: 'relative', display: 'inline-block', maxWidth: '100%' }}>
      <div
        data-time-range-bar=""
        role="group"
        aria-label={labels.range}
        style={{ display: 'inline-flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}
      >
        {/* A board default that is none of the presets (two hours, say) is still the selected span,
          shown under its own label rather than silently looking like another preset. */}
        {!abs && preset === undefined ? (
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
        <button
          type="button"
          aria-pressed={abs !== undefined}
          aria-expanded={open}
          data-time-range-custom=""
          onClick={openPicker}
          style={button(abs !== undefined)}
        >
          {l.custom}
        </button>
        {abs ? (
          <>
            <button type="button" data-time-range-live="" onClick={() => controls.resume()} style={button(false)}>
              {l.backToLive}
            </button>
            <button type="button" data-time-range-copy="" onClick={copyLink} style={button(false)}>
              {copied ? l.linkCopied : l.copyLink}
            </button>
            <AbsoluteIndicator absolute={abs} labels={labels} />
          </>
        ) : (
          <>
            <button type="button" aria-pressed={state.paused} data-time-range-pause="" onClick={onPause} style={button(state.paused)}>
              {state.paused ? labels.resume : labels.pause}
            </button>
            {state.paused ? <PausedIndicator pausedAt={state.pausedAt} labels={labels} /> : null}
          </>
        )}
      </div>
      {open && Picker ? (
        <div style={{ position: 'absolute', right: 0, top: '100%', marginTop: 6, zIndex: 30 }}>
          <Picker
            initial={abs}
            labels={l}
            onApply={(from, to) => {
              const problem = controls.setAbsolute(from, to);
              if (!problem) setOpen(false);
              return problem;
            }}
            onCancel={() => setOpen(false)}
          />
        </div>
      ) : null}
      {loadFailed ? (
        <div role="alert" data-time-range-load-failed="" style={{ fontSize: 12, color: 'hsl(var(--destructive))' }}>
          {l.loadFailed}
        </div>
      ) : null}
      {notice && noticeShown ? (
        <div
          role="status"
          data-time-range-notice=""
          style={{ display: 'flex', gap: 8, alignItems: 'baseline', marginTop: 4, fontSize: 12, color: 'hsl(var(--muted-foreground))' }}
        >
          <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{notice}</span>
          <button type="button" onClick={() => setNoticeShown(false)} style={{ ...button(false), flex: '0 0 auto' }}>
            {l.dismiss}
          </button>
        </div>
      ) : null}
    </div>
  );
}
