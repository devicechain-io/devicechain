// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure ECharts option builders. Kept free of React/DOM so the data→chart mapping
// is unit-testable without a canvas: a widget resolves a ChartTheme + samples and
// hands the result straight to <EChart>.

import type { MeasurementSample } from '@devicechain/dashboards';

import { formatValue } from '../format';
import type { ElementSize } from '../hooks';
import { isAlarmLevel, levelColor, type AlarmLevel } from '../severity';
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
export function severityFill(theme: ChartTheme, level: AlarmLevel, alpha: number): string {
  return theme.severity[level].replace(/^hsl\(/, 'hsla(').replace(/\)$/, `, ${alpha})`);
}

// Opacity of a threshold band behind a line: enough to read as a zone, faint enough that the
// line and its grid stay the subject. A `normal` band has no fill at all.
const BAND_ALPHA = 0.14;

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
  // The board's density scale (useWidgetScale().scale): multiplies the axis-font clamp. Default 1.
  scale?: number;
}

// The markLine / markArea for a chart's thresholds, attached to the first series (the markers
// belong to a series in ECharts, and every series shares the one y-axis).
function thresholdMarks(theme: ChartTheme, thresholds: readonly Threshold[], unit: string | undefined, precision: number | undefined, fontSize: number) {
  const suffix = unit ? ` ${unit}` : '';
  // A `normal` threshold is still drawn: it is where the value RETURNS to normal, which an
  // operator wants to see. It is drawn in the muted text colour, the same ink as the axis
  // labels, which reads in light and dark and says "boundary" without saying "alarm".
  const ink = (t: Threshold) => levelColor(theme.severity, t.level) ?? theme.mutedForeground;
  const markLine = {
    silent: true,
    symbol: 'none',
    animation: false,
    data: thresholds.map((t) => ({
      yAxis: t.value,
      name: t.label ?? String(t.value),
      lineStyle: { color: ink(t), type: 'dashed' as const, width: 1.5 },
      label: {
        show: true,
        position: 'insideEndTop' as const,
        color: ink(t),
        fontSize,
        formatter: () => t.label ?? `${formatValue(t.value, precision)}${suffix}`,
      },
    })),
  };
  const markArea = {
    silent: true,
    animation: false,
    data: thresholdBands(thresholds, -Infinity, Infinity)
      .filter((b) => isAlarmLevel(b.level))
      .map((b) => [
        { yAxis: b.from, itemStyle: { color: severityFill(theme, b.level as AlarmLevel, BAND_ALPHA) } },
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
  const scale = options.scale ?? 1;
  const axisFont = clamp(Math.round(shortSide(size) / 22), Math.round(8 * scale), Math.round(12 * scale));

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
  // Severity thresholds, drawn as coloured bands on the arc (any order).
  thresholds?: readonly Threshold[];
  // The board's density scale: multiplies the label-font clamp. Default 1.
  scale?: number;
}

// The dial's geometry as fractions of its box: where its centre sits and how much of the short
// side's half it fills. The DOM readout is placed from the same numbers (gaugeReadoutBox).
const GAUGE_CENTER_Y = 0.58;
const GAUGE_RADIUS = 0.95;
// How far below the hub the readout sits, as a fraction of the radius.
const GAUGE_READOUT_OFFSET = 0.3;

// The box (px, relative to the chart) the gauge's DOM readout is centred in and fitted to: under
// the hub and inside the dial, where the canvas detail used to be drawn. A zero size (before the
// first measure) takes the nominal one the builder assumes.
export function gaugeReadoutBox(size?: ElementSize): { centerX: number; centerY: number; width: number; height: number } {
  const s = shortSide(size);
  const w = (size?.width ?? 0) > 0 ? size!.width : s;
  const h = (size?.height ?? 0) > 0 ? size!.height : s;
  return {
    centerX: w / 2,
    centerY: GAUGE_CENTER_Y * h + GAUGE_READOUT_OFFSET * GAUGE_RADIUS * (s / 2),
    width: s * 0.7,
    height: s * 0.25,
  };
}

// Below this short side (px) the gauge drops its axis labels: at that size they would be
// drawn at the 8 px floor, which is noise rather than a scale.
export const GAUGE_AXIS_LABELS_MIN_SIDE = 120;

// The arc's colour stops: ECharts wants [[endFraction, colour], ...] ending at 1. A neutral
// stretch (below the lowest threshold, or a `normal` band) takes the track colour. With no bands: one track.
function gaugeArcColors(bands: ReturnType<typeof thresholdBands>, min: number, max: number, theme: ChartTheme): [number, string][] {
  if (bands.length === 0) return [[1, theme.border]];
  return bands.map((b, i): [number, string] => [
    i === bands.length - 1 ? 1 : (b.to - min) / (max - min),
    levelColor(theme.severity, b.level) ?? theme.border,
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
  // Banded only when a COLOURED band actually lands on the arc: thresholds that all sit above
  // `max`, or only `normal` bands, leave a plain neutral track, which keeps the progress arc
  // like any other gauge.
  const banded = bands.some((b) => isAlarmLevel(b.level));
  // The needle takes the colour of the band the value sits in; with no bands, below the
  // lowest, or in a `normal` band it keeps the chart accent.
  const accent = levelColor(theme.severity, bandAt(thresholds, value)?.level) ?? theme.series[0];

  const s = shortSide(size);
  const arcWidth = clamp(Math.round(s / 26), 4, 12);
  const scale = options.scale ?? 1;
  const labelFont = clamp(Math.round(s / 20), Math.round(8 * scale), Math.round(13 * scale));
  const showAxisLabels = s >= GAUGE_AXIS_LABELS_MIN_SIDE;
  return {
    animation: false,
    series: [
      {
        type: 'gauge',
        min,
        max,
        splitNumber: s < 160 ? 4 : 5, // fewer ticks than the default 10 → no cram
        radius: `${GAUGE_RADIUS * 100}%`,
        center: ['50%', `${GAUGE_CENTER_Y * 100}%`],
        // The progress arc would paint over the bands, so with bands the needle alone marks the value.
        progress: { show: !banded, width: arcWidth, itemStyle: { color: accent } },
        pointer: { itemStyle: { color: accent }, length: '55%', width: clamp(Math.round(s / 50), 2, 6) },
        anchor: { show: false },
        axisLine: { lineStyle: { width: arcWidth, color: gaugeArcColors(bands, min, max, theme) } },
        axisTick: { show: false },
        splitLine: { length: arcWidth, lineStyle: { color: theme.border, width: 1 } },
        axisLabel: { show: showAxisLabels, color: theme.mutedForeground, fontSize: labelFont, distance: arcWidth + 2 },
        // The number is DOM text over the chart (gaugeReadoutBox), not canvas text: the readability
        // gate measures DOM only, and a canvas readout was invisible to it.
        detail: { show: false },
        data: [{ value: value ?? min }],
      },
    ],
  };
}

// A heading in degrees folded onto [0, 360): 370 is 10 and -90 is 270, because a compass has no
// "beyond a full turn" and a bearing sensor may well report either. 360 is 0, never 360, and a
// value that is not a finite number has no heading at all (undefined), not a plausible one.
export function wrapHeading(value: number | null | undefined): number | undefined {
  if (value == null || !Number.isFinite(value)) return undefined;
  return ((value % 360) + 360) % 360;
}

export interface CompassChartOptions {
  // The four point labels in N, E, S, W order (a locale writes West as "O", so the caller owns them).
  cardinals: readonly [string, string, string, string];
  thresholds?: readonly Threshold[];
  // The board's density scale; multiplies the label-font clamp. Default 1.
  scale?: number;
}

// buildCompassOption draws a full turn on the registered gauge series: north at the top, the
// angle running clockwise, so 90 is east. The needle takes the colour of the band the (wrapped)
// heading is in and the chart accent in a `normal` band or none. The degrees themselves are DOM
// text beside it (compass.tsx). With no reading the needle rests at north.
export function buildCompassOption(
  value: number | null,
  theme: ChartTheme,
  options: CompassChartOptions,
  size?: ElementSize,
): EChartOption {
  const heading = wrapHeading(value);
  const thresholds = parseThresholds(options.thresholds ?? []);
  const bands = thresholdBands(thresholds, 0, 360);
  const accent = levelColor(theme.severity, bandAt(thresholds, heading ?? null)?.level) ?? theme.series[0];
  const s = shortSide(size);
  const scale = options.scale ?? 1;
  const arcWidth = clamp(Math.round(s / 30), 3, 10);
  const labelFont = clamp(Math.round(s / 11), Math.round(10 * scale), Math.round(20 * scale));
  const [north, east, south, west] = options.cardinals;
  const points: Record<number, string> = { 0: north, 90: east, 180: south, 270: west };
  return {
    animation: false,
    series: [
      {
        type: 'gauge',
        min: 0,
        max: 360,
        startAngle: 90,
        endAngle: -270,
        splitNumber: 4,
        radius: '92%',
        center: ['50%', '50%'],
        progress: { show: false },
        pointer: { itemStyle: { color: accent }, length: '62%', width: clamp(Math.round(s / 36), 3, 8) },
        anchor: { show: true, size: clamp(Math.round(s / 24), 5, 12), itemStyle: { color: accent } },
        axisLine: { lineStyle: { width: arcWidth, color: gaugeArcColors(bands, 0, 360, theme) } },
        axisTick: { show: s >= GAUGE_AXIS_LABELS_MIN_SIDE, splitNumber: 3, length: arcWidth, lineStyle: { color: theme.border, width: 1 } },
        splitLine: { length: arcWidth * 1.6, lineStyle: { color: theme.mutedForeground, width: 2 } },
        // Only the four points are labelled; 360 is the same tick as 0, so it stays blank.
        axisLabel: {
          show: true,
          color: theme.foreground,
          fontSize: labelFont,
          distance: arcWidth * 1.6 + 6,
          formatter: (v: number) => points[v] ?? '',
        },
        detail: { show: false },
        data: [{ value: heading ?? 0 }],
      },
    ],
  };
}
