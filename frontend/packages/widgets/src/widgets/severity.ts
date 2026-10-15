// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Alarm-severity presentation shared by the alarm widgets (ADR-041 vocabulary:
// CRITICAL | MAJOR | MINOR | WARNING | INDETERMINATE). Severity is SEMANTIC color --
// deliberately fixed, not derived from the dashboard accent -- so a critical alarm reads the
// same red on every tenant's theme.
//
// 🔑 ONE PALETTE. The hues are the SDK's severity tokens (`--sev-*`, widget-sdk/severity.ts):
// the same colour a threshold band uses is the colour an alarm row uses, in light and dark, and
// a host that re-themes a token re-themes both. This file keeps only the VOCABULARY mapping
// (the backend's UPPER_CASE severities onto the token levels). INDETERMINATE -- and anything
// unrecognized -- has no alarm hue: it renders in the muted foreground, the same neutral an
// absent alarm gets.

import { css, type AlarmLevel } from '@devicechain/widget-sdk';

const LEVEL_OF: Record<string, AlarmLevel> = {
  CRITICAL: 'critical',
  MAJOR: 'major',
  MINOR: 'minor',
  WARNING: 'warning',
};

const SEVERITY_ORDER: Record<string, number> = {
  CRITICAL: 0,
  MAJOR: 1,
  MINOR: 2,
  WARNING: 3,
  INDETERMINATE: 4,
};

// severityColor maps a severity to its badge/stripe color. `colors` is useSeverityColors() --
// the concrete token colours in scope -- so the result follows the host's theme and dark mode.
// An INDETERMINATE or unrecognized severity (a hand-edited or future one) takes the muted
// foreground rather than a hue it has not earned.
export function severityColor(colors: Readonly<Record<AlarmLevel, string>>, severity: string): string {
  const level = LEVEL_OF[severity];
  return level ? colors[level] : css('muted-foreground');
}

// severityRank orders severities most-severe-first; unknown severities sort last.
export function severityRank(severity: string): number {
  return SEVERITY_ORDER[severity] ?? 99;
}

// severityLabel renders a severity in Title Case ('CRITICAL' → 'Critical').
export function severityLabel(severity: string): string {
  if (!severity) return '';
  return severity.charAt(0).toUpperCase() + severity.slice(1).toLowerCase();
}

// highestSeverity returns the most severe severity present in a list, or undefined for
// an empty list — the accent an alarm-count uses to signal "how bad is the worst one".
export function highestSeverity(severities: string[]): string | undefined {
  let best: string | undefined;
  for (const s of severities) {
    if (best === undefined || severityRank(s) < severityRank(best)) best = s;
  }
  return best;
}
