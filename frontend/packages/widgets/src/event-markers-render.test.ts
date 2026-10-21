// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment node

// The decorated option through a REAL ECharts instance (SVG, no canvas): the markers are drawn in
// their colours, at the right place on a time axis, on a series that holds no readings -- and the
// chart's own series are still there, unmoved.

import type { AlarmRow } from '@devicechain/dashboards';
import { buildLineOption, type ChartTheme } from '@devicechain/widget-sdk/echarts';
import { LineChart } from 'echarts/charts';
import { GridComponent, LegendPlainComponent, MarkAreaComponent, MarkLineComponent, TooltipComponent } from 'echarts/components';
import * as echarts from 'echarts/core';
import { SVGRenderer } from 'echarts/renderers';
import { afterEach, describe, expect, it } from 'vitest';

import { alarmEvents, commandEvents, planMarkers } from './event-markers-model';
import { EVENT_MARKER_STRINGS } from './event-markers-strings';

echarts.use([LineChart, GridComponent, TooltipComponent, LegendPlainComponent, MarkLineComponent, MarkAreaComponent, SVGRenderer]);

const words = EVENT_MARKER_STRINGS.en;
const T0 = Date.parse('2026-10-20T12:00:00Z');
const MIN = 60_000;
const range = { from: T0, to: T0 + 60 * MIN };
const at = (min: number) => new Date(T0 + min * MIN).toISOString();
const theme: ChartTheme = {
  foreground: '#fff',
  mutedForeground: '#999',
  border: '#333',
  series: ['#c1', '#c2'],
  severity: { warning: 'hsl(1, 1%, 1%)', minor: 'hsl(2, 2%, 2%)', major: 'hsl(3, 3%, 3%)', critical: 'hsl(4, 4%, 4%)' },
};

const sample = (name: string, value: number, min: number) => ({
  id: `${name}${min}`,
  deviceToken: 'd',
  eventType: 0,
  occurredTime: at(min),
  name,
  value,
  classifier: null,
});
const samples = [sample('temperature', 20, 0), sample('temperature', 25, 30), sample('humidity', 50, 0), sample('humidity', 55, 60)];

const row: AlarmRow = {
  token: 'a1',
  originatorType: 'device',
  originatorToken: 'd',
  alarmKey: 'high-temp',
  metricKey: 'temperature',
  state: 'CLEARED',
  acknowledged: false,
  severity: 'CRITICAL',
  raisedTime: at(10),
  clearedTime: at(20),
  acknowledgedTime: null,
  acknowledgedBy: null,
  lastValue: 90,
};

let chart: echarts.ECharts | undefined;
afterEach(() => {
  chart?.dispose();
  chart = undefined;
});

function draw(option: unknown) {
  chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 390, height: 240 });
  chart.setOption(option as echarts.EChartsCoreOption);
  return chart.renderToSVGString();
}

describe('the decorated chart through a real ECharts', () => {
  const items = [...alarmEvents([row], 'd', range, words, (n) => String(n)), ...commandEvents([{ token: 'c', name: 'reboot', status: 'SUCCESSFUL', payload: null, responsePayload: null, error: null, queuedTime: null, sentTime: at(40), respondedTime: null }], range, words, (n) => String(n))];
  const base = buildLineOption(samples, theme, {});
  const decorated = planMarkers(items, range, theme).decorate(base, { width: 390, height: 240 });

  it('draws the severity line and band, and the neutral command line', () => {
    const svg = draw(decorated);
    expect(svg).toContain('stroke="hsl(4, 4%, 4%)"'); // the raise line: critical
    expect(svg).toContain('fill="hsla(4, 4%, 4%, 0.05)"'); // the band, faint
    expect(svg).toContain('fill="hsla(4, 4%, 4%, 0.85)"'); // the strip along its top edge
    expect(svg).toContain('stroke="#999"'); // neutral: the clear line and the command line
    expect(svg).not.toMatch(/Infinity|NaN/);
  });

  it('draws nothing of the kind without the markers: the same chart, undecorated', () => {
    const svg = draw(base);
    expect(svg).not.toContain('stroke="hsl(4, 4%, 4%)"');
    expect(svg).not.toContain('hsla(4, 4%, 4%, 0.05)');
  });

  it('keeps every reading series, and the legend lists the readings alone', () => {
    const c = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 390, height: 240 });
    chart = c;
    c.setOption(decorated as echarts.EChartsCoreOption);
    const series = (c.getOption() as { series: Array<{ name?: string; data?: unknown[] }> }).series;
    expect(series.map((s) => s.name)).toEqual(['temperature', 'humidity', '']);
    expect(series.slice(0, 2).map((s) => s.data?.length)).toEqual([2, 2]);
    const svg = c.renderToSVGString();
    expect(svg).toContain('temperature');
    expect(svg).toContain('humidity');
  });

  it('puts the raise where the time axis says it is', () => {
    const c = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 600, height: 240 });
    chart = c;
    c.setOption(decorated as echarts.EChartsCoreOption);
    const x = (ms: number) => (c.convertToPixel({ xAxisIndex: 0 }, ms) as unknown as number);
    // Ten minutes in is a sixth of the way across the plot: the axis is the range, not the data.
    const left = x(range.from);
    const right = x(range.to);
    expect((x(T0 + 10 * MIN) - left) / (right - left)).toBeCloseTo(1 / 6, 3);
  });
});
