// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

export type CelPreset = 'filter' | 'exists' | 'math';
export type CelContext = 'event' | 'detection';

const comparisons = ['>', '>=', '<', '<=', '==', '!='];
const arithmetic = ['+', '-', '*', '/'];

/** Generate only the small, reviewed vocabulary offered by the friendly forms. */
export function buildCelPreset(kind: CelPreset, context: CelContext, metric: string, op: string, number: string): string | null {
  if (context === 'event' && !metric.trim()) return null;
  const key = JSON.stringify(metric);
  const value = context === 'event' ? `m[${key}]` : 'value';
  const present = context === 'event' ? `${key} in m` : 'hasValue';
  if (kind === 'exists') return present;
  if (!number.trim() || !Number.isFinite(Number(number))) return null;
  const numeric = Number(number);
  // CEL distinguishes int and double; the telemetry vocabulary contains doubles.
  const literal = Number.isInteger(numeric) && !String(numeric).includes('e') ? `${numeric}.0` : String(numeric);
  if (kind === 'filter') return comparisons.includes(op) ? `${present} && ${value} ${op} ${literal}` : null;
  if (!arithmetic.includes(op) || (op === '/' && numeric === 0)) return null;
  // A missing scalar remains an evaluation error, rather than a plausible manufactured zero.
  return `${value} ${op} ${literal}`;
}
