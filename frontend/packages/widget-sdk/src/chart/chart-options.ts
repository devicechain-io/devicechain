// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure ECharts option builders. Kept free of React/DOM so the data→chart mapping
// is unit-testable without a canvas: a widget resolves a ChartTheme + samples and
// hands the result straight to <EChart>.

import type { MeasurementSample } from '@devicechain/dashboards';

import { fitValueFont } from '../fit-text';
import { formatValue } from '../format';
import type { ElementSize } from '../hooks';
import type { SeverityLevel } from '../severity';
import { bandAt, parseThresholds, thresholdBands, type Threshold } from '../thresholds';

import type { ChartTheme } from './chart-theme';
import type { EChartOption } from './echart';

// distinctNames returns the measurement names present in `samples`, in first-seen
// order — used when a chart's datasource doesn't pin an explicit series list.
function distinctNames(samples: MeasurementSample[]): string[] {
  const seen: string[] = [];
  for (const s of samples) {
    if (!seen.includes(s.name)) seen.push(s.name);
  }
  return seen;
}

// A severity colour with an alpha, for a band drawn UNDER the data (markArea). The theme's
// severity colours are comma-form hsl(), so the alpha form is a string edit.
export function severityFill(theme: ChartTheme, level: SeverityLevel, alpha: number): string {
  return theme.severity[level].replace(/^hsl\(/, 'hsla(').replace(/\)$/, `, ${alpha})`);
}

// Opacity of a threshold band behind a line: enough to read as a zone, faint enough that the
// line and its grid stay the subject. `normal` is fainter still -- green everywhere is noise.
const BAND_ALPHA = 0.14;
const NORMAL_BAND_ALPHA = 0.06;

const clamp = (v: number, lo: number, hi: number): number => Math.max(lo, Math.min(hi, v));

// gauge/line fonts and tick counts scale with the smaller container dimension so a
// widget stays legible when resized. A zero/absent size (before first measure, or
// in a non-DOM test) falls back to a nominal medium size.
function shortSide(size?: ElementSize): number {
  const s = Math.min(size?.width ?? 0, size?.height ?? 0);
  return s > 0 ? s : 200;
}

export interface LineChartOptions {
  // Measurement names to plot, one series each. Empty → every name seen in the
  // window (first-seen order).
  measurements?: string[];
  // Unit suffix: named on the y-axis and appended to every tooltip value.
  unit?: string;
  // Decimal places for axis labels and tooltip values; unset uses the default rounding.
  precision?: number;
  // Severity thresholds, drawn as a labelled line each plus a faint band between them.
  // Any order: the builder sorts them.
  thresholds?: readonly Threshold[];
}

// The markLine / markArea for a chart's thresholds, attached to the first series (the markers
// belong to a series in ECharts, and every series shares the one y-axis).
function thresholdMarks(theme: ChartTheme, thresholds: readonly Threshold[], unit: string | undefined, precision: number | undefined, fontSize: number) {
  const suffix = unit ? ` ${unit}` : '';
  const markLine = {
    silent: true,
    symbol: 'none',
    animation: false,
    data: thresholds.map((t) => ({
      yAxis: t.value,
      name: t.label ?? String(t.value),
      lineStyle: { color: theme.severity[t.level], type: 'dashed' as const, width: 1.5 },
      label: {
        show: true,
        position: 'insideEndTop' as const,
        color: theme.severity[t.level],
        fontSize,
        formatter: () => t.label ?? `${formatValue(t.value, precision)}${suffix}`,
      },
    })),
  };
  const markArea = {
    silent: true,
    animation: false,
    data: thresholdBands(thresholds, -Infinity, Infinity)
      .filter((b) => b.level !== undefined)
      .map((b) => [
        { yAxis: b.from, itemStyle: { color: severityFill(theme, b.level as SeverityLevel, b.level === 'normal' ? NORMAL_BAND_ALPHA : BAND_ALPHA) } },
        { yAxis: b.to },
      ]),
  };
  return { markLine, markArea };
}

// buildLineOption maps a rolling window of samples to a multi-series time chart,
// one line per measurement name, coloured from the theme's chart palette.
export function buildLineOption(
  samples: MeasurementSample[],
  theme: ChartTheme,
  options: LineChartOptions = {},
  size?: ElementSize,
): EChartOption {
  const names = options.measurements?.length ? options.measurements : distinctNames(samples);
  const axisFont = clamp(Math.round(shortSide(size) / 22), 8, 12);

  // Re-parsed (validated + sorted): the builder is public, and a caller may hand it a list
  // that never went through parseThresholds. Every rule below assumes ascending order.
  const thresholds = parseThresholds(options.thresholds ?? []);
  const lowest = thresholds.length > 0 ? thresholds[0].value : 0;
  const highest = thresholds.length > 0 ? thresholds[thresholds.length - 1].value : 0;
  const marks = thresholds.length > 0 ? thresholdMarks(theme, thresholds, options.unit, options.precision, axisFont) : undefined;
  const suffix = options.unit ? ` ${options.unit}` : '';
  const precision = options.precision;

  const series = names.map((name, i) => {
    const color = theme.series[i % theme.series.length];
    return {
      ...(i === 0 ? marks : undefined),
      name,
      type: 'line' as const,
      showSymbol: false,
      lineStyle: { color },
      itemStyle: { color },
      data: samples
        .filter((s) => s.name === name && s.value != null && s.occurredTime != null)
        .map((s) => [s.occurredTime as string, s.value as number]),
    };
  });

  return {
    animation: false,
    textStyle: { color: theme.foreground, fontSize: axisFont },
    grid: { left: 8, right: 12, top: names.length > 1 || options.unit ? 28 : 12, bottom: 8, containLabel: true },
    tooltip: {
      trigger: 'axis',
      valueFormatter: (v: unknown) => (typeof v === 'number' ? `${formatValue(v, precision)}${suffix}` : String(v ?? '—')),
    },
    legend:
      names.length > 1 ? { textStyle: { color: theme.mutedForeground, fontSize: axisFont }, top: 0 } : undefined,
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: theme.border } },
      axisLabel: { color: theme.mutedForeground, fontSize: axisFont, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      // The unit rides the axis once, at its head, rather than on every tick.
      name: options.unit,
      nameTextStyle: { color: theme.mutedForeground, fontSize: axisFont, align: 'left' as const },
      splitLine: { lineStyle: { color: theme.border } },
      axisLabel: {
        color: theme.mutedForeground,
        fontSize: axisFont,
        // Rounded only when the author asked; otherwise ECharts' own tick rounding applies.
        ...(precision != null ? { formatter: (v: number) => formatValue(v, precision) } : {}),
      },
      // A threshold outside the data's range must still be on the chart: extend the range to
      // take it in. Only then -- a returned number FIXES that end of the axis (no nice
      // rounding, no zero baseline), so a threshold inside the data returns null and leaves
      // ECharts' own auto range exactly as it is without thresholds.
      ...(thresholds.length > 0
        ? {
            min: (e: { min: number }) => (lowest < e.min ? lowest : null),
            max: (e: { max: number }) => (highest > e.max ? highest : null),
          }
        : {}),
    },
    series,
  };
}

export interface GaugeChartOptions {
  min?: number;
  max?: number;
  unit?: string;
  // Decimal places for the readout; unset uses the default rounding.
  precision?: number;
  // Severity thresholds, drawn as coloured bands on the arc (any order).
  thresholds?: readonly Threshold[];
}

// Below this short side (px) the gauge drops its axis labels: at that size they would be
// drawn at the 8 px floor, which is noise rather than a scale.
export const GAUGE_AXIS_LABELS_MIN_SIDE = 120;

// The arc's colour stops: ECharts wants [[endFraction, colour], ...] ending at 1. A neutral
// stretch (below the lowest threshold) takes the track colour. With no bands: one track.
function gaugeArcColors(bands: ReturnType<typeof thresholdBands>, min: number, max: number, theme: ChartTheme): [number, string][] {
  if (bands.length === 0) return [[1, theme.border]];
  return bands.map((b, i): [number, string] => [
    i === bands.length - 1 ? 1 : (b.to - min) / (max - min),
    b.level ? theme.severity[b.level] : theme.border,
  ]);
}

// buildGaugeOption maps a single latest value to a gauge. A null value (nothing
// received yet) shows an em dash and rests the needle at the minimum. Ticks, fonts,
// and the arc width scale with the widget size so it stays legible when resized
// (ECharts' default gauge crams ~10 labels at fixed fonts on a small canvas).
export function buildGaugeOption(
  value: number | null,
  theme: ChartTheme,
  options: GaugeChartOptions = {},
  size?: ElementSize,
): EChartOption {
  const min = options.min ?? 0;
  const max = options.max ?? 100;
  const thresholds = parseThresholds(options.thresholds ?? []); // validated + sorted, as above
  const bands = thresholdBands(thresholds, min, max);
  // Banded only when a severity band actually lands on the arc: thresholds that all sit above
  // `max` leave a plain neutral track, which keeps the progress arc like any other gauge.
  const banded = bands.some((b) => b.level !== undefined);
  // The needle takes the colour of the band the value sits in; with no bands (or below the
  // lowest) it keeps the chart accent.
  const hit = bandAt(thresholds, value);
  const accent = hit ? theme.severity[hit.level] : theme.series[0];
  const unitSuffix = options.unit ? ` ${options.unit}` : '';

  const s = shortSide(size);
  const arcWidth = clamp(Math.round(s / 26), 4, 12);
  const labelFont = clamp(Math.round(s / 20), 8, 13);
  const showAxisLabels = s >= GAUGE_AXIS_LABELS_MIN_SIDE;
  // The readout is fitted to the dial's inner box and its own length, like every big number.
  const readout = value == null ? '—' : `${formatValue(value, options.precision)}${unitSuffix}`;
  const valueFont = fitValueFont({ width: s * 0.7, height: s * 0.25 }, readout.length);

  return {
    animation: false,
    series: [
      {
        type: 'gauge',
        min,
        max,
        splitNumber: s < 160 ? 4 : 5, // fewer ticks than the default 10 → no cram
        radius: '95%',
        center: ['50%', '58%'],
        // The progress arc would paint over the bands, so with bands the needle alone marks the value.
        progress: { show: !banded, width: arcWidth, itemStyle: { color: accent } },
        pointer: { itemStyle: { color: accent }, length: '55%', width: clamp(Math.round(s / 50), 2, 6) },
        anchor: { show: false },
        axisLine: { lineStyle: { width: arcWidth, color: gaugeArcColors(bands, min, max, theme) } },
        axisTick: { show: false },
        splitLine: { length: arcWidth, lineStyle: { color: theme.border, width: 1 } },
        axisLabel: { show: showAxisLabels, color: theme.mutedForeground, fontSize: labelFont, distance: arcWidth + 2 },
        detail: {
          valueAnimation: false,
          color: theme.foreground,
          fontSize: valueFont,
          offsetCenter: [0, '30%'],
          formatter: (v: number) => (value == null ? '—' : `${formatValue(v, options.precision)}${unitSuffix}`),
        },
        data: [{ value: value ?? min }],
      },
    ],
  };
}
