// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { AlarmRow, CommandRow } from './types';

// The time range an event read covers, for a widget that draws events on a time axis: the
// `spanSeconds` that end at `endAt` (epoch ms), or, without `endAt`, at the source's now() when
// each read is made, so a live range keeps sliding. Every source applies it BEFORE paging, so a
// snapshot's `total` counts the range and nothing outside it.
export interface EventWindow {
  spanSeconds: number;
  endAt?: number;
}

// The instants a window covers at `now`. `to` is undefined for a live window: the read is open
// at the top, since nothing has happened after now.
export function eventWindowBounds(window: EventWindow, now: number): { from: number; to: number | undefined } {
  const end = window.endAt ?? now;
  return { from: end - window.spanSeconds * 1000, to: window.endAt };
}

// The range criteria of the alarms and commands queries (RFC 3339; null = unbounded), at `now`.
export function windowCriteria(window: EventWindow | undefined, now: number): { startTime: string | null; endTime: string | null } {
  if (!window) return { startTime: null, endTime: null };
  const { from, to } = eventWindowBounds(window, now);
  return { startTime: new Date(from).toISOString(), endTime: to === undefined ? null : new Date(to).toISOString() };
}

const instant = (iso: string | null | undefined): number | null => {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t;
};

// The platform's alarm range, for a source that answers in memory: the alarm's span (raise to
// clear, or still active) overlaps the window. A row with no raise time cannot be placed.
export function alarmOverlaps(row: Pick<AlarmRow, 'raisedTime' | 'clearedTime'>, window: EventWindow, now: number): boolean {
  const { from, to } = eventWindowBounds(window, now);
  const raised = instant(row.raisedTime);
  if (raised === null || (to !== undefined && raised > to)) return false;
  const cleared = instant(row.clearedTime);
  return cleared === null || cleared >= from;
}

// The platform's command range, for a source that answers in memory: sent (or queued, when
// never sent) inside the window.
export function commandInRange(row: Pick<CommandRow, 'sentTime' | 'queuedTime'>, window: EventWindow, now: number): boolean {
  const { from, to } = eventWindowBounds(window, now);
  const at = instant(row.sentTime) ?? instant(row.queuedTime);
  return at !== null && at >= from && (to === undefined || at <= to);
}
