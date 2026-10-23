// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The radial family: compass, progress-ring, and the gauge's readout now being DOM text.
// The pure arithmetic (heading wrap, option geometry) is in chart-options.test.ts; these prove
// each widget WIRES it and renders what it computes. The canvas wrapper is stubbed (jsdom has no
// canvas) and hands back the option it was given.

import type { MeasurementSample, WidgetInstance, WidgetType } from '@devicechain/dashboards';
import type { MeasurementStreamState } from '@devicechain/widget-sdk';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    EChart: ({ option }: { option: any }) => <div data-testid="echart" data-option={JSON.stringify(option)} />,
  };
});

import { Compass } from './widgets/compass';
import { Gauge } from './widgets/gauge';
import { ProgressRing, RING_CIRCUMFERENCE } from './widgets/progress-ring';

afterEach(cleanup);

const widget = (type: WidgetType, options: Record<string, unknown>): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 3, rows: 3 } },
  datasource: { kind: 'device', deviceToken: 'd', measurements: ['m'] },
  options,
});

const sample = (value: number | null): MeasurementSample => ({
  id: 's',
  deviceToken: 'd',
  eventType: 0,
  occurredTime: '2026-07-02T10:00:00Z',
  name: 'm',
  value,
  classifier: null,
});

const data = (value: number | null | 'none'): MeasurementStreamState =>
  value === 'none'
    ? { latest: {}, samples: [], error: null }
    : { latest: { m: sample(value) }, samples: [sample(value)], error: null };

const q = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const styleOf = (sel: string) => q(sel)?.getAttribute('style') ?? '';
const optionOf = () => JSON.parse(q('[data-testid="echart"]')?.getAttribute('data-option') ?? '{}');
const readout = () => q('[data-level-readout]');

const ACCENT = 'var(--chart-1)';
// jsdom normalises a colour to rgb() when it is set on an element, so compare through the same parser.
const paint = (colour: string) => {
  const el = document.createElement('div');
  el.style.color = colour;
  return el.style.color;
};
const WARNING = paint('hsl(199, 89%, 44%)');
const CRITICAL = paint('hsl(0, 72%, 46%)');

describe('compass', () => {
  const show = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<Compass widget={widget('compass', options)} data={data(value)} />);

  it.each([
    [10, '10', 'N'],
    [370, '10', 'N'],
    [-90, '270', 'W'],
    [450, '90', 'E'],
    [180, '180', 'S'],
    [225, '225', 'SW'],
    // Between the axes: the NEAREST point, so 30 is NE (not N) and 350 is N (not NW).
    [30, '30', 'NE'],
    [350, '350', 'N'],
    [-20, '340', 'N'],
  ])('heading %s reads %s degrees, %s, and points the needle there', (value, degrees, point) => {
    show({}, value);
    expect(readout()?.firstChild?.textContent).toBe(`${degrees}°`);
    expect(readout()?.textContent).toBe(`${degrees}°${point}`);
    expect(optionOf().series[0].data[0].value).toBe(Number(degrees));
    expect(optionOf().series[0].startAngle).toBe(90);
  });

  it('is named by its heading, not by colour alone', () => {
    show({ title: 'Wind' }, -90);
    const dial = screen.getByRole('img', { name: /270°.*W/ });
    expect(dial).toBeTruthy();
    expect(dial.getAttribute('aria-label')).toContain('Wind');
  });

  it('rounds to the precision BEFORE it wraps, so a heading never reads 360', () => {
    show({}, 359.6);
    expect(readout()?.firstChild?.textContent).toBe('0°');
    expect(optionOf().series[0].data[0].value).toBe(0);
    cleanup();
    show({ precision: 1 }, 359.6);
    expect(readout()?.firstChild?.textContent).toBe('359.6°');
  });

  it('says No data, not a heading, when nothing has arrived or the value is not a number', () => {
    show({}, 'none');
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(readout()).toBeNull();
    cleanup();
    show({}, null);
    expect(screen.getByText('No data in this range')).toBeTruthy();
    cleanup();
    show({}, Number.NaN);
    expect(screen.getByText('No data in this range')).toBeTruthy();
  });

  const thresholds = [
    { value: 0, level: 'normal' },
    { value: 90, level: 'warning' },
    { value: 270, level: 'critical' },
  ];

  it('colours the readout by band at the wrapped heading and keeps it neutral in the normal band', () => {
    show({ thresholds }, 100);
    expect(styleOf('[data-level-readout]')).toContain(WARNING);
    cleanup();
    show({ thresholds }, 460); // 100 degrees
    expect(styleOf('[data-level-readout]')).toContain(WARNING);
    cleanup();
    show({ thresholds }, -45); // 315 degrees
    expect(styleOf('[data-level-readout]')).toContain(CRITICAL);
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('critical');
    cleanup();
    show({ thresholds }, 45);
    const normal = styleOf('[data-level-readout]');
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('normal');
    cleanup();
    show({}, 45);
    // The normal band paints exactly what no thresholds at all paint, and no severity hue.
    expect(normal).toBe(styleOf('[data-level-readout]'));
    expect(normal).not.toContain(WARNING);
    expect(normal).not.toContain(CRITICAL);
    // ...and that neutral is the foreground text colour, not the chart accent: equal to "no
    // thresholds" alone would also hold if both painted the accent.
    expect(normal).toContain('var(--foreground)');
    expect(normal).not.toContain(ACCENT);
  });
});

describe('progress-ring', () => {
  const opts = { min: 0, max: 200 };
  const show = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<ProgressRing widget={widget('progress-ring', options)} data={data(value)} />);
  const dash = () => q('[data-ring-fill]')?.getAttribute('stroke-dasharray');

  it.each([
    [0, 0],
    [50, 0.25],
    [100, 0.5],
    [200, 1],
  ])('value %s fills %s of the ring and reads its percent', (value, fraction) => {
    show(opts, value);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe(String(fraction));
    expect(dash()).toBe(`${fraction * RING_CIRCUMFERENCE} ${RING_CIRCUMFERENCE}`);
    expect(readout()?.textContent).toBe(`${fraction * 100}%`);
    expect(q('[data-out-of-range]')).toBeNull();
  });

  it('pins a value off the scale to the end, marks it, and still prints the true percent', () => {
    show(opts, 300);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe('1');
    expect(dash()).toBe(`${RING_CIRCUMFERENCE} ${RING_CIRCUMFERENCE}`);
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('above');
    expect(screen.getByRole('img', { name: 'Above scale' })).toBeTruthy();
    expect(readout()?.textContent).toContain('150');
    cleanup();
    show(opts, -50);
    expect(q('[data-fill]')?.getAttribute('data-fill')).toBe('0');
    expect(q('[data-out-of-range]')?.getAttribute('data-out-of-range')).toBe('below');
    expect(screen.getByRole('img', { name: 'Below scale' })).toBeTruthy();
    expect(readout()?.textContent).toContain('-25');
  });

  it('reads the value itself, with its unit, when asked', () => {
    show({ ...opts, readout: 'value', unit: 'L', precision: 1 }, 50);
    expect(readout()?.firstChild?.textContent).toBe('50.0');
    expect(readout()?.textContent).toBe('50.0L');
  });

  it('refuses to draw without both ends of the scale, and says so', () => {
    show({ min: 0 }, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
    expect(q('[data-ring-fill]')).toBeNull();
    cleanup();
    show({ max: 100 }, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
    cleanup();
    show({}, 50);
    expect(screen.getByText('Set min and max')).toBeTruthy();
    cleanup();
    show({ min: 10, max: 10 }, 10);
    expect(screen.getByText('Max must be above min')).toBeTruthy();
    expect(q('[data-ring-fill]')).toBeNull();
  });

  it('says No data with a scale but no sample', () => {
    show(opts, 'none');
    expect(screen.getByText('No data in this range')).toBeTruthy();
    expect(q('[data-ring-fill]')).toBeNull();
  });

  it('is a meter with its value in words, so colour is never the only cue', () => {
    show({ ...opts, unit: 'L' }, 50);
    const meter = screen.getByRole('meter');
    expect(meter.getAttribute('aria-valuemin')).toBe('0');
    expect(meter.getAttribute('aria-valuemax')).toBe('200');
    expect(meter.getAttribute('aria-valuenow')).toBe('50');
    expect(meter.getAttribute('aria-valuetext')).toContain('50');
    expect(meter.getAttribute('aria-valuetext')).toContain('L');
  });

  it('clamps the meter value to the scale off the end, and keeps the true value in words', () => {
    show({ ...opts, unit: 'L' }, 300);
    const meter = screen.getByRole('meter');
    expect(meter.getAttribute('aria-valuenow')).toBe('200');
    expect(meter.getAttribute('aria-valuetext')).toBe('300 L');
  });

  const thresholds = [
    { value: 0, level: 'critical' },
    { value: 30, level: 'normal' },
    { value: 160, level: 'warning' },
  ];

  it('colours the arc by the coloured band and keeps the neutral accent for normal', () => {
    show({ ...opts, thresholds }, 180);
    expect(styleOf('[data-ring-fill]')).toContain(WARNING);
    cleanup();
    show({ ...opts, thresholds }, 10);
    expect(styleOf('[data-ring-fill]')).toContain(CRITICAL);
    cleanup();
    show({ ...opts, thresholds }, 100);
    expect(styleOf('[data-ring-fill]')).toContain(ACCENT);
    expect(q('[data-threshold-level]')?.getAttribute('data-threshold-level')).toBe('normal');
    cleanup();
    show(opts, 100);
    expect(styleOf('[data-ring-fill]')).toContain(ACCENT);
    // The arc takes the accent; the number stays in the foreground colour, as on every level widget.
    expect(styleOf('[data-level-readout]')).toContain('var(--foreground)');
  });
});

describe('gauge readout', () => {
  const show = (options: Record<string, unknown>, value: number | null | 'none') =>
    render(<Gauge widget={widget('gauge', options)} data={data(value)} />);

  it('is a DOM element carrying the formatted value and unit, over the chart', () => {
    show({ min: 0, max: 50, unit: 'C', precision: 1 }, 21.456);
    expect(readout()).not.toBeNull();
    expect(readout()?.firstChild?.textContent).toBe('21.5');
    expect(readout()?.textContent).toBe('21.5C');
    // The chart carries the needle but no text of its own.
    expect(optionOf().series[0].data[0].value).toBe(21.456);
    expect(optionOf().series[0].detail.show).toBe(false);
  });

  it('explains the absence and draws no needle with no sample', async () => {
    // A needle resting on the minimum would read as a reading of the minimum.
    show({ min: 10, max: 50 }, 'none');
    // The explanation is its own chunk, fetched on first use.
    expect(await screen.findByText('No data in this range', {}, { timeout: 4000 })).toBeTruthy();
    expect(readout()).toBeNull();
    cleanup();
    show({ min: 10, max: 50 }, 10);
    expect(optionOf().series[0].pointer.show).toBe(true);
  });

  it('keeps the default 0..100 scale and the default rounding of an unset precision', () => {
    show({}, 30.203057033956792);
    expect(readout()?.textContent).toBe('30.203');
    expect(optionOf().series[0].max).toBe(100);
  });

  it('is a meter whose reading is exposed as text', () => {
    show({ min: 0, max: 50, unit: 'C' }, 30);
    const meter = screen.getByRole('meter');
    expect(meter.getAttribute('aria-valuenow')).toBe('30');
    expect(meter.getAttribute('aria-valuetext')).toBe('30 C');
  });

  it('clamps the meter value off the scale like the level gauges, with the true reading in words', () => {
    show({ min: 0, max: 50, unit: 'C' }, 80);
    const meter = screen.getByRole('meter');
    expect(meter.getAttribute('aria-valuenow')).toBe('50');
    expect(meter.getAttribute('aria-valuetext')).toBe('80 C');
  });

  it('with no sample, is no meter and prints no bare dash or unit with no number', async () => {
    show({ min: 0, max: 50, unit: 'C' }, 'none');
    expect(await screen.findByText('No data in this range', {}, { timeout: 4000 })).toBeTruthy();
    expect(screen.queryByRole('meter')).toBeNull();
    expect(readout()).toBeNull();
  });
});
