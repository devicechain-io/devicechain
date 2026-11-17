// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import i18n from 'i18next';

type Timestamp = string | Date | null | undefined;

function formatTimestamp(value: Timestamp, fallback: string, dateOnly: boolean): string {
  if (!value) return fallback;
  const date = typeof value === 'string' ? new Date(value) : value;
  if (Number.isNaN(date.getTime())) return fallback;
  // Read on every call so a language switch also changes formatting. Use the
  // resolved catalog language, including when a requested catalog failed to load.
  const locale = i18n.resolvedLanguage ?? 'en';
  return dateOnly ? date.toLocaleDateString(locale) : date.toLocaleString(locale);
}

/** Display a timestamp in the console language and the viewer's local time zone. */
export function formatDateTime(value: Timestamp, fallback = '—'): string {
  return formatTimestamp(value, fallback, false);
}

/** Preserve date-only displays while using the same console language. */
export function formatDate(value: Timestamp): string {
  return formatTimestamp(value, '—', true);
}

/** Display a number in the same language as the surrounding console text. */
export function formatNumber(value: number): string {
  return value.toLocaleString(i18n.resolvedLanguage ?? 'en');
}
