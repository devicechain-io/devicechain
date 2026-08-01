// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// formatTimestamp renders an RFC3339 event time as a locale time string, leaving
// an unparseable value untouched and an absent one blank.
export function formatTimestamp(iso: string | null): string {
  if (!iso) return '';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleTimeString();
}

// formatDateTime renders an RFC3339 time as a locale date+time string. Unlike
// formatTimestamp (time-only, right for a live measurement tick) this keeps the date,
// because an alarm can have been raised days ago and a bare "09:14:32" would mislead.
export function formatDateTime(iso: string | null): string {
  if (!iso) return '';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString();
}

// formatValue renders a measurement value, applying a fixed precision when given
// and showing an em dash for a missing value.
//
// The precision is clamped to toFixed's domain because it arrives from an opaque stored
// definition: outside 0..100 toFixed throws a RangeError, which is an exception thrown
// during render — it unmounts the widget's whole subtree rather than degrading, and every
// other bad option value in this package degrades. The console's config panel can author
// a negative one today, and a hand-edited definition always could.
export function formatValue(value: number | null | undefined, precision?: number): string {
  if (value == null) return '—';
  if (precision == null) return String(value);
  const places = Math.min(100, Math.max(0, Math.trunc(precision)));
  return value.toFixed(places);
}
