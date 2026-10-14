// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The option builders emit components (legend, markLine, markArea); ECharts silently ignores
// a key whose component was never registered. These tests render the builders' output through
// a REAL ECharts instance after importing the wrapper (whose module body is what registers
// the components), and read back what the instance actually built -- so removing a component
// from the registration, or a builder dropping the key, fails here.

import type { MeasurementSample } from '@devicechain/dashboards';
import * as echarts from 'echarts/core';
import { SVGRenderer } from 'echarts/renderers';
import { afterEach, describe, expect, it } from 'vitest';

import { buildGaugeOption, buildLineOption } from './chart-options';
import type { ChartTheme } from './chart-theme';
import './echart'; // registers the chart types and components under test

echarts.use([SVGRenderer]); // test-only: jsdom has no canvas, SSR+SVG is the real engine without one

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

const s = (name: string, value: number, time: string): MeasurementSample => ({
  id: `${name}-${time}`,
  deviceToken: 'd',
  eventType: 0,
  occurredTime: time,
  name,
  value,
  classifier: null,
});

const samples = [
  s('temperature', 20, '2026-07-02T10:00:00Z'),
  s('humidity', 50, '2026-07-02T10:00:00Z'),
  s('temperature', 22, '2026-07-02T10:05:00Z'),
  s('humidity', 52, '2026-07-02T10:05:00Z'),
];

let chart: echarts.ECharts | undefined;
afterEach(() => {
  chart?.dispose();
  chart = undefined;
});

function render(option: unknown) {
  chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 600, height: 300 });
  chart.setOption(option as echarts.EChartsCoreOption);
  return { chart, svg: chart.renderToSVGString() };
}

describe('ECharts component registration', () => {
  it('draws the legend of a multi-series chart', () => {
    const { chart: c, svg } = render(buildLineOption(samples, theme));
    expect((c as unknown as { getModel(): { getComponent(t: string): unknown } }).getModel().getComponent('legend'), 'the legend component was never created').toBeTruthy();
    // The legend is the only place the series names are drawn as text.
    expect(svg).toContain('temperature');
    expect(svg).toContain('humidity');
  });

  it('the builder sets a legend for several series and none for one', () => {
    expect((buildLineOption(samples, theme) as { legend?: unknown }).legend).toBeTruthy();
    expect((buildLineOption(samples.slice(0, 1), theme) as { legend?: unknown }).legend).toBeUndefined();
  });

  it('draws a threshold markLine in its severity colour', () => {
    const { svg } = render(
      buildLineOption(samples, theme, {
        measurements: ['temperature'],
        thresholds: [{ value: 21, level: 'critical' }],
      }),
    );
    expect(svg).toContain('stroke="hsl(5, 5%, 5%)"');
    expect(svg).toContain('stroke-dasharray');
  });

  it('draws the threshold band (markArea) and keeps the axis finite', () => {
    const { svg } = render(
      buildLineOption(samples, theme, {
        measurements: ['temperature'],
        thresholds: [{ value: 21, level: 'major' }],
      }),
    );
    expect(svg).toContain('fill="hsla(4, 4%, 4%, 0.14)"');
    expect(svg).not.toMatch(/Infinity|NaN/);
  });

  it('draws the gauge', () => {
    const { svg } = render(buildGaugeOption(42, theme, { thresholds: [{ value: 50, level: 'minor' }] }));
    expect(svg.length).toBeGreaterThan(200);
  });
});
