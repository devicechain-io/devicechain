// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure ECharts option builders. Kept free of React/DOM so the data→chart mapping
// is unit-testable without a canvas: a widget resolves a ChartTheme + samples and
// hands the result straight to <EChart>.

import type { MeasurementSample } from '@devicechain/dashboards';

import type { EChartOption } from './echart';
import type { ElementSize } from './hooks';
import type { ChartTheme } from './theme';

// distinctNames returns the measurement names present in `samples`, in first-seen
// order — used when a chart's datasource doesn't pin an explicit series list.
function distinctNames(samples: MeasurementSample[]): string[] {
  const seen: string[] = [];
  for (const s of samples) {
    if (!seen.includes(s.name)) seen.push(s.name);
  }
  return seen;
}

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

  const series = names.map((name, i) => {
    const color = theme.series[i % theme.series.length];
    return {
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
    grid: { left: 8, right: 12, top: names.length > 1 ? 28 : 12, bottom: 8, containLabel: true },
    tooltip: { trigger: 'axis' },
    legend:
      names.length > 1 ? { textStyle: { color: theme.mutedForeground, fontSize: axisFont }, top: 0 } : undefined,
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: theme.border } },
      axisLabel: { color: theme.mutedForeground, fontSize: axisFont, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: theme.border } },
      axisLabel: { color: theme.mutedForeground, fontSize: axisFont },
    },
    series,
  };
}

export interface GaugeChartOptions {
  min?: number;
  max?: number;
  unit?: string;
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
  const accent = theme.series[0];
  const unitSuffix = options.unit ? ` ${options.unit}` : '';

  const s = shortSide(size);
  const arcWidth = clamp(Math.round(s / 26), 4, 12);
  const labelFont = clamp(Math.round(s / 20), 8, 13);
  const valueFont = clamp(Math.round(s / 7), 14, 40);

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
        progress: { show: true, width: arcWidth, itemStyle: { color: accent } },
        pointer: { itemStyle: { color: accent }, length: '55%', width: clamp(Math.round(s / 50), 2, 6) },
        anchor: { show: false },
        axisLine: { lineStyle: { width: arcWidth, color: [[1, theme.border]] } },
        axisTick: { show: false },
        splitLine: { length: arcWidth, lineStyle: { color: theme.border, width: 1 } },
        axisLabel: { color: theme.mutedForeground, fontSize: labelFont, distance: arcWidth + 2 },
        detail: {
          valueAnimation: false,
          color: theme.foreground,
          fontSize: valueFont,
          offsetCenter: [0, '42%'],
          formatter: (v: number) => (value == null ? '—' : `${v}${unitSuffix}`),
        },
        data: [{ value: value ?? min }],
      },
    ],
  };
}
