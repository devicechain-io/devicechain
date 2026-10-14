// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Thresholds on the three widgets that honour them, plus the option's schema (free colours are
// turned away). The pure band/boundary rules are tested where they live (widget-sdk
// thresholds.test.ts); these prove each widget WIRES them.

import type { MeasurementSample, WidgetInstance, WidgetType } from '@devicechain/dashboards';
import { type MeasurementStreamState } from '@devicechain/widget-sdk';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(cleanup);

vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>();
  return {
    ...actual,
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    EChart: ({ option }: { option: any }) => (
      <div data-testid="echart" data-series={JSON.stringify(option.series)} />
    ),
  };
});

import { validateWidgetOptions } from './options';
import { Gauge } from './widgets/gauge';
import { LatestCard } from './widgets/latest-card';
import { TimeSeriesChart } from './widgets/time-series-chart';

const widget = (type: WidgetType, options: Record<string, unknown>): WidgetInstance => ({
  id: 'w',
  type,
  layout: { base: { col: 0, colSpan: 2, row: 0, rowSpan: 2, z: 1 } },
  datasource: { kind: 'device', deviceToken: 'd', measurements: ['temperature'] },
  options,
});

const sample = (value: number, time = '2026-07-02T10:00:00Z'): MeasurementSample => ({
  id: time,
  deviceToken: 'd',
  eventType: 0,
  occurredTime: time,
  name: 'temperature',
  value,
  classifier: null,
});

const data = (...samples: MeasurementSample[]): MeasurementStreamState => ({
  latest: { temperature: samples[samples.length - 1] },
  samples,
  error: null,
});

const thresholds = [
  { value: 0, level: 'critical' },
  { value: 15, level: 'normal' },
  { value: 80, level: 'major' },
];

const levelOf = () => document.querySelector('[data-threshold-level]')?.getAttribute('data-threshold-level');

describe('latest-card colours its value by the band it falls in', () => {
  it.each([
    [-1, undefined], // below the lowest threshold: the card's own colour
    [0, 'critical'], // ON a threshold: the band it starts
    [14.9, 'critical'],
    [15, 'normal'],
    [79.9, 'normal'],
    [80, 'major'],
  ])('value %s -> %s', (value, level) => {
    render(<LatestCard widget={widget('latest-card', { thresholds })} data={data(sample(value))} />);
    expect(levelOf()).toBe(level);
  });

  it('paints the resolved severity colour, not the foreground, and the foreground with no band', () => {
    const { rerender } = render(<LatestCard widget={widget('latest-card', { thresholds })} data={data(sample(90))} />);
    const banded = document.querySelector<HTMLElement>('[data-threshold-level]')!;
    // jsdom resolves no CSS variables, so the SDK's own light value for `major` is what paints.
    const expected = document.createElement('div');
    expected.style.color = 'hsl(25, 95%, 48%)'; // SEVERITY_TOKENS.major.light, normalised by the same parser
    expect(banded.style.color).toBe(expected.style.color);
    expect(banded.style.color).not.toContain('var(--foreground)');
    rerender(<LatestCard widget={widget('latest-card', {})} data={data(sample(90))} />);
    expect(document.querySelector('[data-threshold-level]')).toBeNull();
    expect(screen.getByText('90').style.color).toContain('var(--foreground)');
  });

  it('ignores a malformed list rather than crashing', () => {
    render(<LatestCard widget={widget('latest-card', { thresholds: 'nope' })} data={data(sample(5))} />);
    expect(screen.getByText('5')).toBeTruthy();
    expect(levelOf()).toBeUndefined();
  });

  it('never uses a free colour a hand-edited definition smuggles in', () => {
    render(
      <LatestCard
        widget={widget('latest-card', { thresholds: [{ value: 0, level: 'critical', color: '#00ff00' }, { value: 1, level: '#ff00ff' }] })}
        data={data(sample(5))}
      />,
    );
    const html = document.body.innerHTML.toLowerCase();
    expect(html).not.toContain('#00ff00');
    expect(html).not.toContain('#ff00ff');
    expect(html).not.toContain('rgb(0, 255, 0)');
  });
});

describe('gauge and chart draw their thresholds', () => {
  it('the gauge arc is banded from the stored thresholds', () => {
    render(<Gauge widget={widget('gauge', { min: 0, max: 100, thresholds })} data={data(sample(50))} />);
    const series = JSON.parse(screen.getByTestId('echart').getAttribute('data-series') as string);
    // [end fraction, colour] stops, ending at 1: critical to 15, normal to 80, major to 100.
    const stops = series[0].axisLine.lineStyle.color as [number, string][];
    expect(stops.map(([end]) => end)).toEqual([0.15, 0.8, 1]);
    expect(new Set(stops.map(([, colour]) => colour)).size).toBe(3);
  });

  it('the gauge arc is one neutral track without thresholds', () => {
    render(<Gauge widget={widget('gauge', { min: 0, max: 100 })} data={data(sample(50))} />);
    const series = JSON.parse(screen.getByTestId('echart').getAttribute('data-series') as string);
    expect(series[0].axisLine.lineStyle.color).toHaveLength(1);
  });

  it('the chart gets a markLine and markArea per stored threshold', () => {
    render(
      <TimeSeriesChart
        widget={widget('timeseries-chart', { thresholds: [{ value: 21, level: 'warning' }, { value: 25, level: 'critical' }], unit: 'C' })}
        data={data(sample(20), sample(22, '2026-07-02T10:05:00Z'))}
      />,
    );
    const series = JSON.parse(screen.getByTestId('echart').getAttribute('data-series') as string);
    expect(series[0].markLine.data.map((m: { yAxis: number }) => m.yAxis)).toEqual([21, 25]);
    expect(series[0].markArea.data).toHaveLength(2);
  });
});

describe('the thresholds option schema', () => {
  const issues = (options: Record<string, unknown>) =>
    validateWidgetOptions('gauge', options).map((i) => `${i.code}:${i.key}`);

  it('accepts well-formed thresholds', () => {
    expect(issues({ thresholds: [{ value: 1, level: 'critical', label: 'Trip' }] })).toEqual([]);
  });

  it('REJECTS a free colour as a level, and a colour field the entry does not declare', () => {
    expect(issues({ thresholds: [{ value: 1, level: '#ff0000' }] })).toEqual(['enum:thresholds[0].level']);
    expect(issues({ thresholds: [{ value: 1, level: 'critical', color: '#ff0000' }] })).toEqual([
      'unknown:thresholds[0].color',
    ]);
  });

  it('rejects an entry with a missing or mistyped value or level, a non-list, and too many entries', () => {
    expect(issues({ thresholds: [{ level: 'critical' }] })).toEqual(['missing:thresholds[0].value']);
    expect(issues({ thresholds: [{ value: '1', level: 'critical' }] })).toEqual(['type:thresholds[0].value']);
    expect(issues({ thresholds: [{ value: 1 }] })).toEqual(['missing:thresholds[0].level']);
    expect(issues({ thresholds: 'x' })).toEqual(['type:thresholds']);
    expect(issues({ thresholds: Array.from({ length: 13 }, (_, i) => ({ value: i, level: 'normal' })) })).toEqual([
      'range:thresholds',
    ]);
  });

  it('is declared on exactly the three widgets that draw it', () => {
    const declares = (type: WidgetType) => validateWidgetOptions(type, { thresholds: [] }).every((i) => i.code !== 'unknown');
    expect(['gauge', 'latest-card', 'timeseries-chart'].every((t) => declares(t as WidgetType))).toBe(true);
    expect(declares('table')).toBe(false);
  });
});
