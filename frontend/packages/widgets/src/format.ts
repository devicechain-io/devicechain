// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// formatTimestamp renders an RFC3339 event time as a locale time string, leaving
// an unparseable value untouched and an absent one blank.
export function formatTimestamp(iso: string | null): string {
  if (!iso) return '';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleTimeString();
}

// formatValue renders a measurement value, applying a fixed precision when given
// and showing an em dash for a missing value.
export function formatValue(value: number | null | undefined, precision?: number): string {
  if (value == null) return '—';
  return precision != null ? value.toFixed(precision) : String(value);
}
