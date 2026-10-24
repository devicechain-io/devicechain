// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What every level-and-indicator widget reads off its options and data, once: the sample it
// shows, how it formats, the threshold band the value is in and that band's colour, the scale
// and the fill on it. A widget adds its own drawing on top.

import {
  bandAt,
  css,
  formatValue,
  levelColor,
  pickSample,
  primaryMeasurementName,
  useSeverityColors,
  type MeasurementStreamState,
  type SeverityLevel,
  type Threshold,
} from '@devicechain/widget-sdk';
import type { MeasurementSample, WidgetInstance } from '@devicechain/dashboards';

import { levelStrings, type LevelStrings } from './level-strings';
import { fillOf, type Fill, type RangeResult } from './value-scale';

export interface LevelReading {
  // The measurement shown (the title's fallback).
  name: string | undefined;
  // The widget's slot has no device bound: an empty reading then means "choose one".
  unbound: boolean;
  sample: MeasurementSample | undefined;
  value: number | undefined;
  unit: string | undefined;
  precision: number | undefined;
  display: string;
  thresholds: Threshold[];
  band: Threshold | undefined;
  // The band's severity colour; undefined for no band and for `normal`, so the widget draws its
  // OWN neutral. `fillColor` below is that choice made for the common case.
  levelColor: string | undefined;
  // The colour a given level resolves to (undefined for `normal`), for drawing the bands themselves.
  colorOf: (level: SeverityLevel | undefined) => string | undefined;
  // The colour of a lit or filled part: the band's, else the chart accent (never a hue of its own).
  fillColor: string;
  range: RangeResult | undefined;
  fill: Fill | undefined;
  strings: LevelStrings;
}

// What a widget reads off its own options and hands in: the hook reads none itself, so each
// widget's source states every option it honors (options.test.ts holds that to the schema).
// `range` is the widget's scale, read with readRange; an indicator with no scale omits it.
export interface LevelInput {
  thresholds: Threshold[];
  unit: string | undefined;
  precision: number | undefined;
  range?: RangeResult;
  // Maps the sample's raw value to the one the widget shows (a compass folds a heading onto
  // 0..360); undefined means "no usable value". Band, display and fill all read the mapped value.
  normalize?: (raw: number | undefined) => number | undefined;
}

export function useLevelReading(widget: WidgetInstance, data: MeasurementStreamState, input: LevelInput): LevelReading {
  const severity = useSeverityColors();
  const name = primaryMeasurementName(widget);
  const sample = pickSample(data.latest, name);
  const { thresholds, unit, precision, range, normalize } = input;
  const raw = sample?.value ?? undefined;
  const value = normalize ? normalize(raw) : raw;
  const band = bandAt(thresholds, value);
  const bandColor = levelColor(severity, band?.level);
  return {
    name,
    unbound: widget.datasource?.kind === 'unbound',
    sample,
    value,
    unit,
    precision,
    display: formatValue(value, precision),
    thresholds,
    band,
    levelColor: bandColor,
    colorOf: (level) => levelColor(severity, level),
    fillColor: bandColor ?? css('chart-1'),
    range,
    fill: range?.ok ? fillOf(value, range.range) : undefined,
    strings: levelStrings(),
  };
}

// The CONFIGURATION reason a scale widget has nothing to draw, or undefined when its setup is fine.
// The author's fix (the scale) is named before the absence of data, which resolves itself and is
// explained apart (LevelEmptyFor).
export function emptyReason(r: LevelReading): string | undefined {
  if (r.range && !r.range.ok) return r.range.reason === 'missing' ? r.strings.setRange : r.strings.badRange;
  return undefined;
}
