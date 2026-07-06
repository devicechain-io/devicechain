// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Alarm-severity presentation shared by the alarm widgets (ADR-041 vocabulary:
// CRITICAL | MAJOR | MINOR | WARNING | INDETERMINATE). Severity is SEMANTIC color —
// deliberately fixed, not derived from the dashboard accent — so a critical alarm
// reads the same red on every tenant's theme. The warm-to-cool ramp mirrors the
// console's alarm badges (red → orange → amber → sky → slate) so an operator reads the
// same hue for the same severity on both surfaces; shades are tuned to hold contrast as
// a foreground color on both light and dark widget backgrounds.

const SEVERITY_COLORS: Record<string, string> = {
  CRITICAL: '#dc2626', // red-600
  MAJOR: '#f97316', // orange-500
  MINOR: '#d97706', // amber-600 (readable as foreground; console uses amber-400 as a fill)
  WARNING: '#0ea5e9', // sky-500 — cool end of the ramp, matching the console
  INDETERMINATE: '#64748b', // slate-500
};

const SEVERITY_ORDER: Record<string, number> = {
  CRITICAL: 0,
  MAJOR: 1,
  MINOR: 2,
  WARNING: 3,
  INDETERMINATE: 4,
};

const UNKNOWN_COLOR = '#64748b';

// severityColor maps a severity to its badge/stripe color, falling back to a muted
// slate for an unrecognized value (a hand-edited or future severity).
export function severityColor(severity: string): string {
  return SEVERITY_COLORS[severity] ?? UNKNOWN_COLOR;
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
