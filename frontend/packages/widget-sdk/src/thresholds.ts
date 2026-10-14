// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Thresholds: where a measurement crosses from one severity to the next.
//
// A threshold STARTS a band: it holds from its `value` up to the next threshold's value (or
// without end, for the last). Below the lowest threshold there is no band, so a value there
// reads in the widget's own neutral colour, as does a value in a `normal` band (normal has no
// colour; see severity.ts), so a healthy value looks the same under either kind of rule.
// This one rule covers both directions:
//   - "overheat above 105"      -> [{ value: 105, level: 'critical' }]
//   - "low fuel below 15"       -> [{ value: 0, level: 'critical' }, { value: 15, level: 'normal' }]
// A value ON a threshold belongs to the band that threshold starts (>=), never the one below.

import { isSeverityLevel, type SeverityLevel } from './severity';

export interface Threshold {
  value: number;
  level: SeverityLevel;
  label?: string;
}

// A band resolved over a numeric range. `level` is undefined for the neutral stretch below the
// lowest threshold. `to` is Infinity for the last band.
export interface ThresholdBand {
  from: number;
  to: number;
  level: SeverityLevel | undefined;
  label?: string;
}

// parseThresholds reads the stored option defensively: a hand-edited definition may carry
// anything, and a threshold that cannot be read must drop out rather than throw during render
// (or, worse, colour a value by a guess). Only a finite `value` and a known `level` survive --
// so a free colour in the `level` slot is dropped, and an extra `color` key is never read.
// The result is sorted ascending by value (stable, so equal values keep their authored order
// and the later one wins).
export function parseThresholds(raw: unknown): Threshold[] {
  if (!Array.isArray(raw)) return [];
  const out: Threshold[] = [];
  for (const item of raw) {
    if (typeof item !== 'object' || item === null) continue;
    const { value, level, label } = item as Record<string, unknown>;
    if (typeof value !== 'number' || !Number.isFinite(value) || !isSeverityLevel(level)) continue;
    out.push(typeof label === 'string' && label !== '' ? { value, level, label } : { value, level });
  }
  return out.map((t, i) => ({ t, i })).sort((a, b) => a.t.value - b.t.value || a.i - b.i).map(({ t }) => t);
}

export function optThresholds(options: Record<string, unknown> | undefined, key = 'thresholds'): Threshold[] {
  return parseThresholds(options?.[key]);
}

// bandAt returns the threshold whose band holds `value`, or undefined below the lowest.
// `thresholds` must be sorted (parseThresholds does). A null value has no band.
export function bandAt(thresholds: readonly Threshold[], value: number | null | undefined): Threshold | undefined {
  if (value == null || !Number.isFinite(value)) return undefined;
  let hit: Threshold | undefined;
  for (const t of thresholds) {
    if (value >= t.value) hit = t;
    else break;
  }
  return hit;
}

// thresholdBands lays the bands over [min, max]: a neutral lead-in up to the first threshold,
// then one band per threshold, each clipped to the range. Bands that fall wholly outside the
// range are dropped, and the band in effect AT `min` is carried in from a threshold below it.
// The result tiles [min, max] exactly, which is what a gauge arc needs.
export function thresholdBands(thresholds: readonly Threshold[], min: number, max: number): ThresholdBand[] {
  if (!(max > min)) return [];
  const bands: ThresholdBand[] = [];
  const push = (from: number, to: number, level: SeverityLevel | undefined, label?: string) => {
    const a = Math.max(from, min);
    const b = Math.min(to, max);
    if (b > a) bands.push(label === undefined ? { from: a, to: b, level } : { from: a, to: b, level, label });
  };
  if (thresholds.length === 0) return [];
  push(-Infinity, thresholds[0].value, undefined);
  thresholds.forEach((t, i) => push(t.value, thresholds[i + 1]?.value ?? Infinity, t.level, t.label));
  return bands;
}
