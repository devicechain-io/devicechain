// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { buildGaugeOption, buildLineOption, GAUGE_AXIS_LABELS_MIN_SIDE, severityFill } from './chart-options';
import type { ChartTheme } from './chart-theme';

const theme: ChartTheme = {
  foreground: '#fff',
  mutedForeground: '#999',
  border: '#333',
  series: ['#c1', '#c2', '#c3', '#c4', '#c5'],
  severity: {
    normal: 'hsl(1, 1%, 1%)',
    warning: 'hsl(2, 2%, 2%)',
    minor: 'hsl(3, 3%, 3%)',
    major: 'hsl(4, 4%, 4%)',
    critical: 'hsl(5, 5%, 5%)',
  },
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

describe('buildLineOption units, precision and thresholds', () => {
  const data = [sample('temperature', 20.123456, 't1'), sample('temperature', 22, 't2')];
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const opt = (o: unknown) => o as any;

  it('names the unit on the y-axis and appends it to tooltip values', () => {
    const o = opt(buildLineOption(data, theme, { unit: 'degC', precision: 1 }));
    expect(o.yAxis.name).toBe('degC');
    expect(o.tooltip.valueFormatter(20.123456)).toBe('20.1 degC');
  });

  it('rounds axis labels to the precision, and leaves ECharts ticks alone when unset', () => {
    const o = opt(buildLineOption(data, theme, { precision: 0 }));
    expect(o.yAxis.axisLabel.formatter(20.6)).toBe('21');
    expect(opt(buildLineOption(data, theme)).yAxis.axisLabel.formatter).toBeUndefined();
  });

  it('formats tooltip values with the default rounding when precision is unset, never at full float width', () => {
    const o = opt(buildLineOption(data, theme));
    expect(o.tooltip.valueFormatter(30.203057033956792)).toBe('30.203');
  });

  it('draws a labelled dashed markLine per threshold on the first series, in its severity colour', () => {
    const o = opt(
      buildLineOption(data, theme, {
        unit: 'C',
        precision: 1,
        thresholds: [
          { value: 21, level: 'warning', label: 'Hot' },
          { value: 25, level: 'critical' },
        ],
      }),
    );
    const marks = o.series[0].markLine.data;
    expect(marks.map((m: { yAxis: number }) => m.yAxis)).toEqual([21, 25]);
    expect(marks[0].lineStyle.color).toBe('hsl(2, 2%, 2%)');
    expect(marks[1].lineStyle.color).toBe('hsl(5, 5%, 5%)');
    expect(marks[0].label.formatter()).toBe('Hot');
    expect(marks[1].label.formatter()).toBe('25.0 C'); // unlabelled: the value, with precision and unit
  });

  it('draws a band per threshold (none for the neutral lead-in), open-ended at the top', () => {
    const o = opt(
      buildLineOption(data, theme, {
        thresholds: [
          { value: 21, level: 'warning' },
          { value: 25, level: 'critical' },
        ],
      }),
    );
    const areas = o.series[0].markArea.data;
    expect(areas).toHaveLength(2);
    expect(areas[0][0].yAxis).toBe(21);
    expect(areas[0][1].yAxis).toBe(25);
    expect(areas[0][0].itemStyle.color).toBe(severityFill(theme, 'warning', 0.14));
    expect(areas[1][1].yAxis).toBe(Infinity);
  });

  it('extends the auto y-range to include thresholds outside the data', () => {
    const o = opt(
      buildLineOption(data, theme, {
        thresholds: [
          { value: 5, level: 'minor' },
          { value: 90, level: 'major' },
        ],
      }),
    );
    expect(o.yAxis.min({ min: 20, max: 22 })).toBe(5);
    expect(o.yAxis.max({ min: 20, max: 22 })).toBe(90);
    expect(opt(buildLineOption(data, theme)).yAxis.min).toBeUndefined();
  });

  it('leaves the auto range alone (null) for a threshold inside the data: a number would FIX the axis end', () => {
    const o = opt(buildLineOption(data, theme, { thresholds: [{ value: 21, level: 'warning' }] }));
    expect(o.yAxis.min({ min: 20, max: 22 })).toBeNull();
    expect(o.yAxis.max({ min: 20, max: 22 })).toBeNull();
  });

  it('accepts thresholds in any order (the builder is public and sorts them itself)', () => {
    const o = opt(
      buildLineOption(data, theme, {
        thresholds: [
          { value: 90, level: 'major' },
          { value: 5, level: 'minor' },
        ],
      }),
    );
    expect(o.yAxis.min({ min: 20, max: 22 })).toBe(5);
    expect(o.yAxis.max({ min: 20, max: 22 })).toBe(90);
    expect(o.series[0].markArea.data.map((a: [{ yAxis: number }, { yAxis: number }]) => [a[0].yAxis, a[1].yAxis])).toEqual([
      [5, 90],
      [90, Infinity],
    ]);
  });

  it('adds no markers without thresholds', () => {
    expect(opt(buildLineOption(data, theme)).series[0].markLine).toBeUndefined();
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

  it('hides axis labels below the minimum short side instead of drawing them tiny', () => {
    const tiny = seriesOf(buildGaugeOption(20, theme, {}, { width: 200, height: GAUGE_AXIS_LABELS_MIN_SIDE - 1 }))[0];
    const ok = seriesOf(buildGaugeOption(20, theme, {}, { width: 200, height: GAUGE_AXIS_LABELS_MIN_SIDE }))[0];
    expect(tiny.axisLabel.show).toBe(false);
    expect(ok.axisLabel.show).toBe(true);
  });

  it('applies precision to the readout, and shows the raw value when unset', () => {
    const withP = seriesOf(buildGaugeOption(21.456, theme, { precision: 1, unit: 'C' }))[0];
    expect(withP.detail.formatter(21.456)).toBe('21.5 C');
    const raw = seriesOf(buildGaugeOption(21.456, theme, {}))[0];
    expect(raw.detail.formatter(21.456)).toBe('21.456');
    // Unset is not full float width either.
    expect(raw.detail.formatter(30.203057033956792)).toBe('30.203');
  });

  it('paints the arc in the severity bands, ending at 1, with the neutral track below the first', () => {
    const gauge = seriesOf(
      buildGaugeOption(50, theme, {
        min: 0,
        max: 200,
        thresholds: [
          { value: 50, level: 'warning' },
          { value: 150, level: 'critical' },
        ],
      }),
    )[0];
    expect(gauge.axisLine.lineStyle.color).toEqual([
      [0.25, '#333'], // 0..50: below the lowest threshold, the neutral track
      [0.75, 'hsl(2, 2%, 2%)'], // 50..150 warning
      [1, 'hsl(5, 5%, 5%)'], // 150..200 critical
    ]);
    // The needle takes the band of the value (50 is ON the warning threshold: the upper band).
    expect(gauge.pointer.itemStyle.color).toBe('hsl(2, 2%, 2%)');
    // The progress arc would hide the bands, so it is off when bands are drawn.
    expect(gauge.progress.show).toBe(false);
  });

  it('keeps the single neutral track and the progress arc when there are no thresholds', () => {
    const gauge = seriesOf(buildGaugeOption(50, theme, {}))[0];
    expect(gauge.axisLine.lineStyle.color).toEqual([[1, '#333']]);
    expect(gauge.progress.show).toBe(true);
    expect(gauge.pointer.itemStyle.color).toBe('#c1');
  });

  it('keeps the progress arc when every threshold sits above max (no band lands on the arc)', () => {
    const gauge = seriesOf(buildGaugeOption(50, theme, { min: 0, max: 100, thresholds: [{ value: 150, level: 'critical' }] }))[0];
    expect(gauge.axisLine.lineStyle.color).toEqual([[1, '#333']]);
    expect(gauge.progress.show).toBe(true);
  });

  it('carries the band in effect at min in from a threshold below the scale', () => {
    const gauge = seriesOf(
      buildGaugeOption(5, theme, {
        min: 10,
        max: 20,
        thresholds: [
          { value: 0, level: 'major' },
          { value: 15, level: 'normal' },
        ],
      }),
    )[0];
    expect(gauge.axisLine.lineStyle.color).toEqual([
      [0.5, 'hsl(4, 4%, 4%)'],
      [1, 'hsl(1, 1%, 1%)'],
    ]);
  });

  it('fits the readout font to the string length', () => {
    const size = { width: 140, height: 140 };
    const short = seriesOf(buildGaugeOption(5, theme, {}, size))[0];
    const long = seriesOf(buildGaugeOption(123456.789, theme, {}, size))[0];
    expect(long.detail.fontSize).toBeLessThan(short.detail.fontSize);
  });
});
