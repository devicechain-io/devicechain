// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Pure ECharts option builders. Kept free of React/DOM so the data→chart mapping
// is unit-testable without a canvas: a widget resolves a ChartTheme + samples and
// hands the result straight to <EChart>.

import type { MeasurementSample } from '@devicechain/dashboards';

import type { EChartOption } from './echart';
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
): EChartOption {
  const names = options.measurements?.length ? options.measurements : distinctNames(samples);

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
    textStyle: { color: theme.foreground },
    grid: { left: 8, right: 12, top: 24, bottom: 8, containLabel: true },
    tooltip: { trigger: 'axis' },
    legend: names.length > 1 ? { textStyle: { color: theme.mutedForeground }, top: 0 } : undefined,
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: theme.border } },
      axisLabel: { color: theme.mutedForeground },
    },
    yAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: theme.border } },
      axisLabel: { color: theme.mutedForeground },
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
// received yet) shows an em dash and rests the needle at the minimum.
export function buildGaugeOption(
  value: number | null,
  theme: ChartTheme,
  options: GaugeChartOptions = {},
): EChartOption {
  const min = options.min ?? 0;
  const max = options.max ?? 100;
  const accent = theme.series[0];
  const unitSuffix = options.unit ? ` ${options.unit}` : '';

  return {
    animation: false,
    series: [
      {
        type: 'gauge',
        min,
        max,
        progress: { show: true, itemStyle: { color: accent } },
        pointer: { itemStyle: { color: accent } },
        axisLine: { lineStyle: { color: [[1, theme.border]] } },
        axisTick: { lineStyle: { color: theme.border } },
        splitLine: { lineStyle: { color: theme.border } },
        axisLabel: { color: theme.mutedForeground },
        detail: {
          valueAnimation: false,
          color: theme.foreground,
          formatter: (v: number) => (value == null ? '—' : `${v}${unitSuffix}`),
        },
        data: [{ value: value ?? min }],
      },
    ],
  };
}
