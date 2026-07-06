// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { buildGaugeOption, buildLineOption } from './chart-options';
import type { ChartTheme } from './theme';

const theme: ChartTheme = {
  foreground: '#fff',
  mutedForeground: '#999',
  border: '#333',
  series: ['#c1', '#c2', '#c3', '#c4', '#c5'],
};

const sample = (name: string, value: number | null, time: string): MeasurementSample => ({
  id: `${name}-${time}`,
  deviceToken: 'therm-001',
  eventType: 0,
  occurredTime: time,
  name,
  value,
  classifier: null,
});

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const seriesOf = (opt: unknown): any[] => (opt as { series: any[] }).series;

describe('buildLineOption', () => {
  it('builds one series per distinct measurement name in first-seen order', () => {
    const option = buildLineOption(
      [
        sample('temperature', 20, '2026-07-02T10:00:00Z'),
        sample('humidity', 50, '2026-07-02T10:00:00Z'),
        sample('temperature', 21, '2026-07-02T10:01:00Z'),
      ],
      theme,
    );
    const series = seriesOf(option);
    expect(series.map((s) => s.name)).toEqual(['temperature', 'humidity']);
    expect(series[0].data).toEqual([
      ['2026-07-02T10:00:00Z', 20],
      ['2026-07-02T10:01:00Z', 21],
    ]);
    expect(series[0].lineStyle.color).toBe('#c1');
    expect(series[1].itemStyle.color).toBe('#c2');
  });

  it('honors an explicit measurements list and drops null/timeless points', () => {
    const option = buildLineOption(
      [sample('temperature', null, 't1'), sample('temperature', 22, 't2'), sample('humidity', 9, 't3')],
      theme,
      { measurements: ['temperature'] },
    );
    const series = seriesOf(option);
    expect(series).toHaveLength(1);
    expect(series[0].data).toEqual([['t2', 22]]);
  });
});

describe('buildGaugeOption', () => {
  it('maps value with min/max', () => {
    const gauge = seriesOf(buildGaugeOption(42, theme, { min: 0, max: 100 }))[0];
    expect(gauge.type).toBe('gauge');
    expect(gauge.min).toBe(0);
    expect(gauge.max).toBe(100);
    expect(gauge.data[0].value).toBe(42);
  });

  it('rests the needle at min for a null value', () => {
    const gauge = seriesOf(buildGaugeOption(null, theme, { min: 10, max: 50 }))[0];
    expect(gauge.data[0].value).toBe(10);
  });

  it('scales ticks and fonts down for a small widget', () => {
    const small = seriesOf(buildGaugeOption(20, theme, { min: 0, max: 50 }, { width: 120, height: 100 }))[0];
    const large = seriesOf(buildGaugeOption(20, theme, { min: 0, max: 50 }, { width: 600, height: 400 }))[0];
    expect(small.splitNumber).toBeLessThan(large.splitNumber);
    expect(small.detail.fontSize).toBeLessThan(large.detail.fontSize);
    expect(small.axisLabel.fontSize).toBeLessThanOrEqual(large.axisLabel.fontSize);
  });
});
