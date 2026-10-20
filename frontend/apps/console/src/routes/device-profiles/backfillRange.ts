// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The range a backfill defaults to, and the bounds the server holds it to. Plain functions so the
// dialog's defaults are testable without rendering it.

/** The longest range one job may cover. The server refuses a longer one; the dialog says so first. */
export const BACKFILL_MAX_DAYS = 90;

/** How far back the range starts by default. */
export const BACKFILL_DEFAULT_DAYS = 30;

/** The most devices one job may name. */
export const BACKFILL_MAX_DEVICES = 1000;

const DAY_MS = 86_400_000;

/**
 * The default [from, to): `to` is when the version became active (what the live path already
 * covers from then on), or now for a version that is not active; `from` is 30 days before it.
 */
export function defaultBackfillRange(activeSince: string | null | undefined, now: Date): { from: Date; to: Date } {
  const since = activeSince ? new Date(activeSince) : null;
  const to = since && !isNaN(since.getTime()) && since.getTime() <= now.getTime() ? since : now;
  return { from: new Date(to.getTime() - BACKFILL_DEFAULT_DAYS * DAY_MS), to };
}

/** What is wrong with a range, as a stable code the dialog translates; null when it is fine. */
export function backfillRangeProblem(from: Date, to: Date, now: Date): 'invalid' | 'order' | 'future' | 'span' | null {
  if (isNaN(from.getTime()) || isNaN(to.getTime())) return 'invalid';
  if (from.getTime() >= to.getTime()) return 'order';
  // A minute of slack: the inputs have minute resolution and "now" moves while the form is open.
  if (to.getTime() > now.getTime() + 60_000) return 'future';
  if (to.getTime() - from.getTime() > BACKFILL_MAX_DAYS * DAY_MS) return 'span';
  return null;
}

/** A date as the local `YYYY-MM-DDTHH:mm` a datetime-local input holds. */
export function toLocalInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Splits a pasted list of device tokens on commas, whitespace and newlines; drops blanks and repeats. */
export function parseDeviceTokens(text: string): string[] {
  return Array.from(new Set(text.split(/[\s,]+/).filter(Boolean)));
}
