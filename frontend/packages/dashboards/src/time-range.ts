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
export interface TimeRangeState {
  spanSeconds: number;
  paused: boolean;
  pausedAt?: number;
}

export function initialTimeRange(
  defaultSeconds: number | undefined,
  link: { spanSeconds?: number; paused?: boolean } = {},
  now: number = Date.now(),
): TimeRangeState {
  const spanSeconds = link.spanSeconds ?? (isValidSpanSeconds(defaultSeconds) ? defaultSeconds : DEFAULT_TIME_RANGE_SECONDS);
  return link.paused ? { spanSeconds, paused: true, pausedAt: now } : { spanSeconds, paused: false };
}

// Freezing keeps the span and stamps the instant; unfreezing drops the stamp. Both are no-ops
// when already in the asked state, so a double click cannot move `pausedAt` forward.
export function pauseRange(state: TimeRangeState, now: number): TimeRangeState {
  return state.paused ? state : { spanSeconds: state.spanSeconds, paused: true, pausedAt: now };
}

export function resumeRange(state: TimeRangeState): TimeRangeState {
  return state.paused ? { spanSeconds: state.spanSeconds, paused: false } : state;
}

export function withSpan(state: TimeRangeState, spanSeconds: number): TimeRangeState {
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

// timeRangeFromSearch reads both parameters from a query string. Never throws.
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
