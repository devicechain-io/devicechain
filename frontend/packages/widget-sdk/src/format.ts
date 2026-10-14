// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Both formatters take an optional IANA `timeZone`. Omitted, they are exactly what they
// always were: the viewer's own zone and locale defaults, which is right for a live board.
// A host that plays a RECORDING pins one (the renderer's `timeZone` prop), because the
// recorded instants are the point and a viewer's zone would shift them across a date line;
// a pinned zone also prints its short name, so the reading says which clock it is.

// formatTimestamp renders an RFC3339 event time as a locale time string, leaving
// an unparseable value untouched and an absent one blank.
export function formatTimestamp(iso: string | null, timeZone?: string): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return timeZone ? date.toLocaleTimeString(undefined, { timeZone, timeZoneName: 'short' }) : date.toLocaleTimeString();
}

// formatDateTime renders an RFC3339 time as a locale date+time string. Unlike
// formatTimestamp (time-only, right for a live measurement tick) this keeps the date,
// because an alarm can have been raised days ago and a bare "09:14:32" would mislead.
export function formatDateTime(iso: string | null, timeZone?: string): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return timeZone ? date.toLocaleString(undefined, { timeZone, timeZoneName: 'short' }) : date.toLocaleString();
}

// With no precision configured a value is still not printed at full float width: a sensor's
// 30.203057033956792 is noise on a card, a gauge or an alarm row. So an unset precision shows
// at most DEFAULT_FRACTION_DIGITS decimals (trailing zeros dropped), and a value under 1 keeps
// DEFAULT_SMALL_SIGNIFICANT significant digits instead, so a 0.000123 reading is not rounded
// to nothing. An author who wants exact decimals sets `precision`.
export const DEFAULT_FRACTION_DIGITS = 3;
export const DEFAULT_SMALL_SIGNIFICANT = 4;

function defaultFormat(value: number): string {
  if (!Number.isFinite(value)) return String(value);
  const rounded =
    Math.abs(value) >= 1 ? Number(value.toFixed(DEFAULT_FRACTION_DIGITS)) : Number(value.toPrecision(DEFAULT_SMALL_SIGNIFICANT));
  return String(rounded);
}

// formatValue renders a measurement value, applying a fixed precision when given
// (else the default rounding above) and showing an em dash for a missing value.
//
// The precision is clamped to toFixed's domain because it arrives from an opaque stored
// definition: outside 0..100 toFixed throws a RangeError, which is an exception thrown
// during render — it unmounts the widget's whole subtree rather than degrading, and every
// other bad option value in this package degrades. The console's config panel can author
// a negative one today, and a hand-edited definition always could.
export function formatValue(value: number | null | undefined, precision?: number): string {
  if (value == null) return '—';
  if (precision == null) return defaultFormat(value);
  const places = Math.min(100, Math.max(0, Math.trunc(precision)));
  return value.toFixed(places);
}
