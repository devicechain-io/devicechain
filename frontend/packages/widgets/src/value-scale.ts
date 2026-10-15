// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The arithmetic the level-and-indicator widgets share: reading a [min, max] scale off the
// options, turning a value into a fill fraction, and laying threshold bands over the scale.
// Pure and React-free, so every rule is asserted by value in value-scale.test.ts.
//
// 🔑 No range is ever invented. A widget that needs a scale either gets BOTH ends from its
// options or from a fallback its descriptor documents (the battery's 0..100 percent); with
// neither it reports why, and the widget renders that reason instead of a plausible fill.

import { thresholdBands, type SeverityLevel, type Threshold } from '@devicechain/widget-sdk';

export interface ScaleRange {
  min: number;
  max: number;
}

export type RangeResult = { ok: true; range: ScaleRange } | { ok: false; reason: 'missing' | 'invalid' };

// The scale from the two configured ends. With a `fallback`, an end left unset (undefined) takes
// the fallback's (the documented default); without one, an unset end is `missing`. An inverted
// or empty scale (max <= min) is `invalid`: a descriptor constraint says the same at authoring
// time, but a hand-edited definition reaches the renderer without it.
export function readRange(configuredMin: number | undefined, configuredMax: number | undefined, fallback?: ScaleRange): RangeResult {
  const min = configuredMin ?? fallback?.min;
  const max = configuredMax ?? fallback?.max;
  if (min === undefined || max === undefined) return { ok: false, reason: 'missing' };
  if (!(max > min)) return { ok: false, reason: 'invalid' };
  return { ok: true, range: { min, max } };
}

export type ScalePosition = 'below' | 'within' | 'above';

export interface Fill {
  // 0..1, clamped: what is drawn.
  fraction: number;
  // Where the value really is. Anything but `within` is marked on the widget, so a clamped fill
  // is never mistaken for a value that sits exactly on the end.
  position: ScalePosition;
  // The unclamped (value - min) / (max - min), for a percent readout.
  ratio: number;
}

// The fill for a value on a scale, or undefined with no usable value (null, NaN, +-Infinity).
export function fillOf(value: number | null | undefined, range: ScaleRange): Fill | undefined {
  if (value == null || !Number.isFinite(value)) return undefined;
  const ratio = (value - range.min) / (range.max - range.min);
  const position: ScalePosition = value < range.min ? 'below' : value > range.max ? 'above' : 'within';
  return { fraction: Math.min(1, Math.max(0, ratio)), position, ratio };
}

export interface BandSpan {
  // Fractions of the scale, 0..1.
  from: number;
  to: number;
  level: SeverityLevel | undefined;
}

// The threshold bands as fractions of the scale: what a track draws behind the fill.
export function bandSpans(thresholds: readonly Threshold[], range: ScaleRange): BandSpan[] {
  const width = range.max - range.min;
  return thresholdBands(thresholds, range.min, range.max).map((b) => ({
    from: (b.from - range.min) / width,
    to: (b.to - range.min) / width,
    level: b.level,
  }));
}

// A signal strength as bars: one bar per threshold, lit for every threshold the value has
// reached. Below the lowest threshold none is lit, which is a truthful "no signal", not a gap.
export function litBars(thresholds: readonly Threshold[], value: number | null | undefined): { total: number; lit: number } | undefined {
  if (value == null || !Number.isFinite(value)) return undefined;
  return { total: thresholds.length, lit: thresholds.filter((t) => value >= t.value).length };
}
