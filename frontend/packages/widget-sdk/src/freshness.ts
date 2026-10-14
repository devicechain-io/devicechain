// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// How old a widget's newest sample is, whether that is "stale", and how to say so. Pure, so
// the precedence and the wording are testable without a DOM or a clock.

import type { MeasurementSample } from '@devicechain/dashboards';

import { optNumber } from './widget-options';

// resolveStaleAfterSeconds is the age, in seconds, past which a widget's value is called stale,
// or undefined when no honest threshold is known. Precedence:
//
//   1. the widget's own `staleAfterSeconds` option;
//   2. (no source today) the device profile's expected reporting interval x 3 -- a profile
//      carries no measurement reporting interval at present, only a location-fix interval that
//      is documented as descriptive, so nothing is read here and nothing is invented;
//   3. otherwise undefined: show the age, never style anything stale.
//
// 🔴 There is deliberately NO default. A fleet that reports hourly would be painted stale all
// day by any number chosen here, and "stale" painted on healthy data teaches viewers to ignore
// the badge. A non-positive value is not a threshold and counts as none.
export function resolveStaleAfterSeconds(options: Record<string, unknown> | undefined): number | undefined {
  const configured = optNumber(options, 'staleAfterSeconds');
  return configured !== undefined && configured > 0 ? configured : undefined;
}

// newestSampleTime is the latest occurredTime (epoch ms) among the named measurements' latest
// samples, or undefined when none carries a parseable time. `names` empty or omitted means
// every measurement the widget holds.
export function newestSampleTime(
  latest: Record<string, MeasurementSample>,
  names?: readonly string[],
): number | undefined {
  let newest: number | undefined;
  for (const [name, sample] of Object.entries(latest)) {
    if (names && names.length > 0 && !names.includes(name)) continue;
    if (!sample.occurredTime) continue;
    const at = Date.parse(sample.occurredTime);
    if (Number.isNaN(at)) continue;
    if (newest === undefined || at > newest) newest = at;
  }
  return newest;
}

// ageSeconds is whole seconds since `at`, never negative: a device clock slightly ahead of the
// viewer's would otherwise read "in 3 seconds".
export function ageSeconds(at: number, now: number): number {
  return Math.max(0, Math.floor((now - at) / 1000));
}

// isStale is true only when a threshold is known and the age has passed it.
export function isStale(age: number, staleAfter: number | undefined): boolean {
  return staleAfter !== undefined && age > staleAfter;
}

// The viewer's language, as the host app set it on <html lang>, else the browser's.
export function viewerLocale(): string | undefined {
  if (typeof document !== 'undefined' && document.documentElement.lang) return document.documentElement.lang;
  if (typeof navigator !== 'undefined' && navigator.language) return navigator.language;
  return undefined;
}

// One formatter per locale tag: every readout on a board re-formats once a second, and
// building an Intl formatter is far dearer than using one.
const formatters = new Map<string, Intl.RelativeTimeFormat>();

function relativeTimeFormat(locale: string | undefined): Intl.RelativeTimeFormat {
  const key = locale ?? '';
  let formatter = formatters.get(key);
  if (!formatter) {
    try {
      formatter = new Intl.RelativeTimeFormat(locale, { numeric: 'auto', style: 'short' });
    } catch {
      // An unparseable tag in <html lang> must not take a widget down: the runtime's default.
      formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto', style: 'short' });
    }
    formatters.set(key, formatter);
  }
  return formatter;
}

// formatAge renders an age as "12 sec. ago" / "hace 12 s" in the viewer's locale, stepping up
// to minutes, hours and days. Under a second it reads "now".
export function formatAge(age: number, locale: string | undefined = viewerLocale()): string {
  const formatter = relativeTimeFormat(locale);
  if (age < 60) return formatter.format(-age, 'second');
  if (age < 3600) return formatter.format(-Math.floor(age / 60), 'minute');
  if (age < 86400) return formatter.format(-Math.floor(age / 3600), 'hour');
  return formatter.format(-Math.floor(age / 86400), 'day');
}

// The SDK carries no i18n runtime, so the one word it adds to a board ships here, per
// language, falling back to English. A host that wants another language passes `staleLabel`.
export interface FreshnessStrings {
  stale: string;
  ageLabel: string;
}

export const FRESHNESS_STRINGS: Record<'en' | 'es', FreshnessStrings> = {
  en: { stale: 'stale', ageLabel: 'Last sample' },
  es: { stale: 'obsoleto', ageLabel: 'Última muestra' },
};

export function freshnessStrings(locale: string | undefined = viewerLocale()): FreshnessStrings {
  const base = locale?.toLowerCase().split('-')[0];
  return base === 'es' ? FRESHNESS_STRINGS.es : FRESHNESS_STRINGS.en;
}
