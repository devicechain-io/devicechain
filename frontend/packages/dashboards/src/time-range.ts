// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { DashboardDefinition } from './types';

// The board's time range: a SPAN (seconds back from "now"), never a sample count. A time-series
// widget draws the last `spanSeconds`; the viewer may pick another span from the presets, and may
// PAUSE, which freezes what the board shows at that instant. Everything here is pure -- the
// clock is an argument -- so the window arithmetic, the bucket choice and the deep-link parsing
// are testable without a DOM.

// The presets a viewer picks from, shortest first. The id is also the deep-link token
// (`?range=1h`), so it is a contract: renaming one breaks every saved link.
export const TIME_RANGE_PRESETS = [
  { id: '15m', seconds: 15 * 60 },
  { id: '1h', seconds: 60 * 60 },
  { id: '6h', seconds: 6 * 60 * 60 },
  { id: '24h', seconds: 24 * 60 * 60 },
  { id: '7d', seconds: 7 * 24 * 60 * 60 },
] as const;

export type TimeRangePresetId = (typeof TIME_RANGE_PRESETS)[number]['id'];

// The span a board uses when its definition names none. One hour is what every chart seeded
// before the span was configurable, so a board that never set one reads as it always did.
export const DEFAULT_TIME_RANGE_SECONDS = 60 * 60;

// The bounds of a span a definition may author: a minute to the longest preset. Outside them
// the value is not a usable span (a zero-length window draws nothing; a year would ask the
// backend for an unbounded read), so it is ignored, not clamped -- clamping would silently
// turn a typo into a different window than anyone wrote.
export const MIN_TIME_RANGE_SECONDS = 60;
export const MAX_TIME_RANGE_SECONDS = 7 * 24 * 60 * 60;

export function isValidSpanSeconds(v: unknown): v is number {
  return (
    typeof v === 'number' &&
    Number.isInteger(v) &&
    v >= MIN_TIME_RANGE_SECONDS &&
    v <= MAX_TIME_RANGE_SECONDS
  );
}

// The board's range state, owned by the host (the viewer header) and read by the renderer.
// `pausedAt` is the instant (epoch ms) the board froze at; it is set exactly while `paused`.
//
// ABSOLUTE ranges (a from-to window, for incident review and a shareable "02:00 to 04:00" link) are
// an ADDITIVE variant of this same shape, not a new one: `absolute` is present only for them, and
// when present the rolling fields are a faithful projection of the window as a FROZEN rolling
// range -- `paused` is true, `pausedAt` is the window's end and `spanSeconds` its length. A widget
// that only understands rolling ranges (every third-party widget written before this variant)
// therefore computes exactly the absolute window, with its live tail already off, and needs no
// change. A widget that wants to say "this is a fixed window" reads `absolute`.
export interface AbsoluteRange {
  // Epoch ms, whole seconds. fromMs < toMs.
  fromMs: number;
  toMs: number;
}

export interface TimeRangeState {
  spanSeconds: number;
  paused: boolean;
  pausedAt?: number;
  absolute?: AbsoluteRange;
}

// Bounds of an ABSOLUTE window. A minute to 31 days: shorter draws nothing, longer is a bulk read
// the bucketed history does not need to serve on a board. `from` may not predate 2000 (a typo in
// the year, an epoch in seconds read as ms) and `to` may not lie in the future beyond a minute of
// clock skew -- a window that has not happened yet has no history to show.
export const MIN_ABSOLUTE_SPAN_SECONDS = 60;
export const MAX_ABSOLUTE_SPAN_SECONDS = 31 * 24 * 60 * 60;
export const EARLIEST_ABSOLUTE_MS = Date.UTC(2000, 0, 1);
export const ABSOLUTE_FUTURE_SKEW_MS = 60 * 1000;

export type AbsoluteRangeProblem = 'unparseable' | 'order' | 'tooShort' | 'tooLong' | 'future' | 'tooEarly';

// absoluteRangeProblem says why a from-to pair is not a usable window, or undefined when it is.
export function absoluteRangeProblem(fromMs: number, toMs: number, now: number): AbsoluteRangeProblem | undefined {
  if (!Number.isFinite(fromMs) || !Number.isFinite(toMs)) return 'unparseable';
  if (fromMs >= toMs) return 'order';
  if (fromMs < EARLIEST_ABSOLUTE_MS) return 'tooEarly';
  if (toMs > now + ABSOLUTE_FUTURE_SKEW_MS) return 'future';
  const seconds = (Math.floor(toMs / 1000) * 1000 - Math.floor(fromMs / 1000) * 1000) / 1000;
  if (seconds < MIN_ABSOLUTE_SPAN_SECONDS) return 'tooShort';
  if (seconds > MAX_ABSOLUTE_SPAN_SECONDS) return 'tooLong';
  return undefined;
}

export function isAbsolute(state: TimeRangeState): boolean {
  return state.absolute !== undefined;
}

// absoluteRange builds the state for a window, or the problem that makes it unusable. Ends are cut
// to whole seconds so the projected span is exact.
export function absoluteRange(
  fromMs: number,
  toMs: number,
  now: number = Date.now(),
): { state: TimeRangeState } | { problem: AbsoluteRangeProblem } {
  const problem = absoluteRangeProblem(fromMs, toMs, now);
  if (problem) return { problem };
  const from = Math.floor(fromMs / 1000) * 1000;
  const to = Math.floor(toMs / 1000) * 1000;
  return {
    state: { spanSeconds: (to - from) / 1000, paused: true, pausedAt: to, absolute: { fromMs: from, toMs: to } },
  };
}

// leaveAbsolute returns to a LIVE rolling range of the same length (capped at the longest preset).
export function leaveAbsolute(state: TimeRangeState): TimeRangeState {
  if (!state.absolute) return state;
  return { spanSeconds: Math.min(state.spanSeconds, MAX_TIME_RANGE_SECONDS), paused: false };
}

export function initialTimeRange(
  defaultSeconds: number | undefined,
  link: { spanSeconds?: number; paused?: boolean; absolute?: AbsoluteRange } = {},
  now: number = Date.now(),
): TimeRangeState {
  if (link.absolute) {
    const made = absoluteRange(link.absolute.fromMs, link.absolute.toMs, now);
    if ('state' in made) return made.state;
  }
  const spanSeconds = link.spanSeconds ?? (isValidSpanSeconds(defaultSeconds) ? defaultSeconds : DEFAULT_TIME_RANGE_SECONDS);
  return link.paused ? { spanSeconds, paused: true, pausedAt: now } : { spanSeconds, paused: false };
}

// Freezing keeps the span and stamps the instant; unfreezing drops the stamp. Both are no-ops
// when already in the asked state, so a double click cannot move `pausedAt` forward.
export function pauseRange(state: TimeRangeState, now: number): TimeRangeState {
  return state.paused ? state : { spanSeconds: state.spanSeconds, paused: true, pausedAt: now };
}

export function resumeRange(state: TimeRangeState): TimeRangeState {
  if (state.absolute) return leaveAbsolute(state);
  return state.paused ? { spanSeconds: state.spanSeconds, paused: false } : state;
}

// A fixed window keeps its length: picking a span is done through leaveAbsolute (a preset click),
// never by quietly rewriting a window someone chose.
export function withSpan(state: TimeRangeState, spanSeconds: number): TimeRangeState {
  if (state.absolute) return state;
  return isValidSpanSeconds(spanSeconds) ? { ...state, spanSeconds } : state;
}

// The instant the board's window ENDS at: now while live, the pause instant while frozen.
export function rangeEnd(state: TimeRangeState, now: number): number {
  return state.paused && state.pausedAt !== undefined ? state.pausedAt : now;
}

export interface RangeWindow {
  startTime: string;
  endTime: string;
}

// from = end - span, to = end, as RFC 3339 instants.
export function rangeWindow(state: TimeRangeState, now: number): RangeWindow {
  const end = rangeEnd(state, now);
  return {
    startTime: new Date(end - state.spanSeconds * 1000).toISOString(),
    endTime: new Date(end).toISOString(),
  };
}

// The most buckets one history seed asks for per measurement. The backend refuses a read past
// 10 000 buckets; 240 is far inside that, and it is what a chart can show: past a few hundred
// points a line is smoothed by the pixels anyway, so asking for more only costs the read.
export const MAX_SEED_BUCKETS = 240;

// The finest bucket a seed uses, and the base bucket of the pre-aggregated one-minute summaries.
const BASE_BUCKET_SECONDS = 60;

// bucketSecondsFor picks the bucket width for a span: the smallest whole number of minutes that
// keeps the read within MAX_SEED_BUCKETS. A multiple of 60 matters beyond tidiness -- the backend
// serves it from the one-minute summaries instead of scanning raw readings, which is the
// difference between a 7-day read being cheap and being a table scan. The result: 15m and 1h
// stay at one minute; 6h is 2 min; 24h is 6 min; 7d is 42 min.
export function bucketSecondsFor(spanSeconds: number): number {
  const wanted = Math.ceil(spanSeconds / MAX_SEED_BUCKETS);
  return Math.max(BASE_BUCKET_SECONDS, Math.ceil(wanted / BASE_BUCKET_SECONDS) * BASE_BUCKET_SECONDS);
}

// The number of buckets a seed over `spanSeconds` reads, at the width bucketSecondsFor picks.
export function seedBucketCount(spanSeconds: number): number {
  return Math.ceil(spanSeconds / bucketSecondsFor(spanSeconds));
}

// ---- Deep link --------------------------------------------------------------

// parseRangeParam reads `?range=`: a preset id, exactly. Anything else (an unknown id, an empty
// value, `1H`, `3600`, a span in another unit) is undefined, so the caller keeps the board's
// default. A link must never be able to ask for a window the control cannot show.
export function parseRangeParam(value: string | null | undefined): number | undefined {
  if (!value) return undefined;
  return TIME_RANGE_PRESETS.find((p) => p.id === value)?.seconds;
}

// parsePausedParam reads `?paused=`: only the literal `1` pauses. `true`, `yes`, `0`, an empty
// value and garbage all mean "not paused", so a malformed link opens live rather than frozen
// on an unannounced instant.
export function parsePausedParam(value: string | null | undefined): boolean {
  return value === '1';
}

// RFC 3339 with an EXPLICIT zone (`Z` or `+02:00`). A date-time with no zone is ambiguous -- whose
// 02:00? -- and a shared link must mean the same window to everyone who opens it, so it is refused
// rather than read in the opener's zone.
const RFC3339_WITH_ZONE = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?([Zz]|[+-]\d{2}:\d{2})$/;

export function parseInstantParam(value: string | null | undefined): number | undefined {
  if (!value || !RFC3339_WITH_ZONE.test(value)) return undefined;
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? undefined : ms;
}

export interface AbsoluteLinkRejection {
  reason: AbsoluteRangeProblem | 'incomplete';
  from: string;
  to: string;
}

// What a deep link's `from`/`to` came to: a usable window, or the reason it was ignored (with the
// raw values, so the host can show them). Neither parameter present is neither.
export function parseAbsoluteParams(
  from: string | null | undefined,
  to: string | null | undefined,
  now: number = Date.now(),
): { absolute?: AbsoluteRange; rejected?: AbsoluteLinkRejection } {
  if (from == null && to == null) return {};
  const raw = { from: from ?? '', to: to ?? '' };
  if (!from || !to) return { rejected: { reason: 'incomplete', ...raw } };
  const fromMs = parseInstantParam(from);
  const toMs = parseInstantParam(to);
  if (fromMs === undefined || toMs === undefined) return { rejected: { reason: 'unparseable', ...raw } };
  const made = absoluteRange(fromMs, toMs, now);
  if ('problem' in made) return { rejected: { reason: made.problem, ...raw } };
  return { absolute: made.state.absolute };
}

// The query values that reproduce a window: UTC instants, so the link means one thing everywhere.
export function absoluteToParams(a: AbsoluteRange): { from: string; to: string } {
  return { from: new Date(a.fromMs).toISOString(), to: new Date(a.toMs).toISOString() };
}

export interface TimeRangeLink {
  spanSeconds?: number;
  paused?: boolean;
  absolute?: AbsoluteRange;
  // Present when `from`/`to` were given but unusable; the host shows a note and opens rolling.
  rejected?: AbsoluteLinkRejection;
}

// timeRangeFromSearch reads the rolling parameters, `range` and `paused`, from a query string.
// Never throws. It ignores `from`/`to`, so what it returns for any address is what it returned
// before fixed windows existed; a host that opens fixed windows reads timeRangeLinkFromSearch.
export function timeRangeFromSearch(search: string): { spanSeconds?: number; paused?: boolean } {
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return {};
  }
  const link: { spanSeconds?: number; paused?: boolean } = {};
  const spanSeconds = parseRangeParam(params.get('range'));
  if (spanSeconds !== undefined) link.spanSeconds = spanSeconds;
  if (parsePausedParam(params.get('paused'))) link.paused = true;
  return link;
}

// timeRangeLinkFromSearch reads every time-range parameter from a query string. Never throws. An
// absolute window (`from`+`to`) wins over `range` and `paused`: it is the more specific ask.
export function timeRangeLinkFromSearch(search: string, now: number = Date.now()): TimeRangeLink {
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return {};
  }
  const abs = parseAbsoluteParams(params.get('from'), params.get('to'), now);
  if (abs.absolute) return { absolute: abs.absolute };
  const link: TimeRangeLink = timeRangeFromSearch(search);
  if (abs.rejected) link.rejected = abs.rejected;
  return link;
}

// The preset id for a span, or undefined when the span is none of them (a board default of
// two hours, say): the control then shows the span's own label with no preset highlighted.
export function presetIdOf(spanSeconds: number): TimeRangePresetId | undefined {
  return TIME_RANGE_PRESETS.find((p) => p.seconds === spanSeconds)?.id;
}

// A compact label for any span: `15m`, `2h`, `3d`. A span that is not a whole number of the
// largest unit that divides it falls to the next smaller one, so the label is always exact.
export function spanLabel(spanSeconds: number): string {
  if (spanSeconds % 86400 === 0) return `${spanSeconds / 86400}d`;
  if (spanSeconds % 3600 === 0) return `${spanSeconds / 3600}h`;
  if (spanSeconds % 60 === 0) return `${spanSeconds / 60}m`;
  return `${spanSeconds}s`;
}

// ---- Authoring ---------------------------------------------------------------

// setBoardTimeRange writes the board's default span. The default (one hour) and an unusable value
// are stored as NO range at all, so a board that never chose one stays byte-identical on save and
// reads as it always did.
export function setBoardTimeRange(def: DashboardDefinition, defaultSeconds: number | undefined): DashboardDefinition {
  const { timeRange: _previous, ...rest } = def;
  if (!isValidSpanSeconds(defaultSeconds) || defaultSeconds === DEFAULT_TIME_RANGE_SECONDS) return rest;
  return { ...rest, timeRange: { defaultSeconds } };
}
