// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  buildCompassOption,
  buildGaugeOption,
  buildLineOption,
  GAUGE_AXIS_LABELS_MIN_SIDE,
  gaugeReadoutBox,
  severityFill,
  wrapHeading,
} from './chart-options';
import type { ChartTheme } from './chart-theme';

const theme: ChartTheme = {
  foreground: '#fff',
  mutedForeground: '#999',
  border: '#333',
  series: ['#c1', '#c2', '#c3', '#c4', '#c5'],
  severity: {
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

  it('a normal threshold keeps its (muted) line but gets no band fill', () => {
    const o = opt(
      buildLineOption(data, theme, {
        thresholds: [
          { value: 0, level: 'major' },
          { value: 15, level: 'normal' },
        ],
      }),
    );
    const marks = o.series[0].markLine.data;
    expect(marks.map((m: { yAxis: number }) => m.yAxis)).toEqual([0, 15]);
    expect(marks[1].lineStyle.color).toBe('#999'); // mutedForeground: the return-to-normal boundary
    expect(marks[1].label.color).toBe('#999');
    const areas = o.series[0].markArea.data;
    expect(areas).toHaveLength(1);
    expect(areas[0][0].yAxis).toBe(0);
    expect(areas[0][1].yAxis).toBe(15);
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
    expect(small.axisLabel.fontSize).toBeLessThanOrEqual(large.axisLabel.fontSize);
  });

  it('hides axis labels below the minimum short side instead of drawing them tiny', () => {
    const tiny = seriesOf(buildGaugeOption(20, theme, {}, { width: 200, height: GAUGE_AXIS_LABELS_MIN_SIDE - 1 }))[0];
    const ok = seriesOf(buildGaugeOption(20, theme, {}, { width: 200, height: GAUGE_AXIS_LABELS_MIN_SIDE }))[0];
    expect(tiny.axisLabel.show).toBe(false);
    expect(ok.axisLabel.show).toBe(true);
  });

  it('draws no readout of its own: the number is DOM text over the chart', () => {
    // Canvas text is invisible to the readability gate, so the gauge widget overlays a DOM readout
    // (gauge.tsx); the series must not ALSO print the value or it would show twice.
    for (const value of [21.456, null]) {
      const gauge = seriesOf(buildGaugeOption(value, theme, {}))[0];
      expect(gauge.detail.show).toBe(false);
    }
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
      [1, '#333'], // normal: the neutral track, not a colour
    ]);
  });

  it('a value in a normal band keeps the plain needle, and an arc with only normal bands keeps the progress arc', () => {
    const below = { thresholds: [{ value: 0, level: 'major' as const }, { value: 15, level: 'normal' as const }] };
    const healthy = seriesOf(buildGaugeOption(50, theme, { min: 0, max: 100, ...below }))[0];
    expect(healthy.pointer.itemStyle.color).toBe('#c1');
    expect(seriesOf(buildGaugeOption(5, theme, { min: 0, max: 100, ...below }))[0].pointer.itemStyle.color).toBe('hsl(4, 4%, 4%)');
    const onlyNormal = seriesOf(buildGaugeOption(50, theme, { min: 0, max: 100, thresholds: [{ value: 10, level: 'normal' }] }))[0];
    expect(onlyNormal.axisLine.lineStyle.color).toEqual([
      [0.1, '#333'],
      [1, '#333'],
    ]);
    expect(onlyNormal.progress.show).toBe(true);
  });

  it('places the DOM readout where the canvas detail used to be: under the hub, inside the dial', () => {
    const box = gaugeReadoutBox({ width: 200, height: 140 });
    // The dial is centred at 58% of the height and sized by the short side (140).
    expect(box.centerX).toBe(100);
    expect(box.centerY).toBeCloseTo(0.58 * 140 + 0.3 * 0.95 * 70, 5);
    expect(box.width).toBeCloseTo(140 * 0.7, 5);
    expect(box.height).toBeCloseTo(140 * 0.25, 5);
    // Unmeasured: the same nominal 200 px the builder assumes.
    expect(gaugeReadoutBox({ width: 0, height: 0 }).width).toBeCloseTo(200 * 0.7, 5);
  });
});

describe('wrapHeading', () => {
  it.each([
    [0, 0],
    [10, 10],
    [359, 359],
    [360, 0],
    [370, 10],
    [720, 0],
    [725.5, 5.5],
    [-90, 270],
    [-1, 359],
    [-360, 0],
    [-450, 270],
  ])('%s degrees wraps to %s', (input, wrapped) => {
    expect(wrapHeading(input)).toBeCloseTo(wrapped, 9);
  });

  it('never returns 360 or a negative zero', () => {
    expect(Object.is(wrapHeading(-360), 0)).toBe(true);
    expect(Object.is(wrapHeading(-0), 0)).toBe(true);
    // A tiny negative would round up to 360 under naive modulo arithmetic.
    expect(wrapHeading(-1e-14)).toBeLessThan(360);
  });

  it('has no heading for a value that is not a finite number', () => {
    for (const bad of [Number.NaN, Infinity, -Infinity, null, undefined]) expect(wrapHeading(bad)).toBeUndefined();
  });
});

describe('buildCompassOption', () => {
  const letters = ['N', 'E', 'S', 'W'] as const;
  const compass = (value: number | null, opts = {}) => seriesOf(buildCompassOption(value, theme, { cardinals: letters, ...opts }))[0];

  it('is a full circle from north, clockwise: 0 at the top, 90 to the east', () => {
    const c = compass(10);
    expect(c.type).toBe('gauge');
    expect(c.min).toBe(0);
    expect(c.max).toBe(360);
    expect(c.startAngle).toBe(90);
    expect(c.endAngle).toBe(-270);
    expect(c.clockwise).not.toBe(false);
    expect(c.splitNumber).toBe(4);
  });

  it.each([
    [10, 10],
    [370, 10],
    [-90, 270],
    [360, 0],
    [0, 0],
  ])('points the needle at heading %s as %s degrees', (value, needle) => {
    expect(compass(value).data[0].value).toBeCloseTo(needle, 9);
  });

  it('rests the needle at north with no reading, and draws no value text', () => {
    const c = compass(null);
    expect(c.data[0].value).toBe(0);
    expect(c.detail.show).toBe(false);
  });

  it('labels the four points of the compass and leaves the 360 tick blank', () => {
    const fmt = compass(10).axisLabel.formatter as (v: number) => string;
    expect([0, 90, 180, 270, 360].map(fmt)).toEqual(['N', 'E', 'S', 'W', '']);
    const es = compass(10, { cardinals: ['N', 'E', 'S', 'O'] }).axisLabel.formatter as (v: number) => string;
    expect(es(270)).toBe('O');
    // Any other tick is left unlabelled rather than printed as a number.
    expect(fmt(45)).toBe('');
  });

  it('takes the needle colour of the band the heading is in, and the accent in a normal band', () => {
    const thresholds = [
      { value: 0, level: 'normal' as const },
      { value: 90, level: 'critical' as const },
    ];
    expect(compass(45, { thresholds }).pointer.itemStyle.color).toBe('#c1');
    expect(compass(100, { thresholds }).pointer.itemStyle.color).toBe('hsl(5, 5%, 5%)');
    // The band is read at the WRAPPED heading: 460 is 100 degrees.
    expect(compass(460, { thresholds }).pointer.itemStyle.color).toBe('hsl(5, 5%, 5%)');
    expect(compass(100, { thresholds }).axisLine.lineStyle.color).toEqual([
      [0.25, '#333'],
      [1, 'hsl(5, 5%, 5%)'],
    ]);
    expect(compass(10).pointer.itemStyle.color).toBe('#c1');
  });
});
