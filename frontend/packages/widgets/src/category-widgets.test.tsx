// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The bar and pie charts as components: the bars in the order the options ask for, labelled and
// valued, a pie's wedges and legend, and every case where there is nothing honest to draw.

import type { MeasurementSample, MeasurementStreamState, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { aggregateStrings } from './aggregate-strings';
import { BarChart, fitLabel } from './widgets/bar-chart';
import { PieChart } from './widgets/pie-chart';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.documentElement.lang = 'en';
});

// Every observed element reports this content-box size, so a chart has a real box to lay out in.
function stubElementSize(width: number, height: number) {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(private cb: (entries: unknown[]) => void) {}
      observe() {
        this.cb([{ contentRect: { width, height } }]);
      }
      disconnect() {}
    },
  );
}

const sample = (name: string, value: number | null): MeasurementSample => ({
  id: name,
  deviceToken: 'd',
  eventType: 0,
  occurredTime: '2026-10-14T12:00:00Z',
  name,
  value,
  classifier: null,
});
const stream = (pairs: Record<string, number | null>): MeasurementStreamState => ({
  latest: Object.fromEntries(Object.entries(pairs).map(([n, v]) => [n, sample(n, v)])),
  samples: [],
  error: null,
});
const widget = (type: string, measurements: string[], options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource: { kind: 'device', deviceToken: 'd', measurements },
    options,
  }) as unknown as WidgetInstance;

const bars = (c: HTMLElement) =>
  [...c.querySelectorAll('[data-bar]')].map((g) => ({
    label: g.getAttribute('data-bar'),
    value: g.getAttribute('data-value'),
    drawn: g.querySelector('rect') !== null,
  }));

describe('bar-chart', () => {
  const data = stream({ temperature: 20, humidity: 55, battery: 90 });
  const names = ['temperature', 'humidity', 'battery'];

  it('draws one bar per measurement, in the order the datasource names them, each carrying its value', () => {
    stubElementSize(360, 200);
    const { container } = render(<BarChart widget={widget('bar-chart', names)} data={data} />);
    expect(bars(container)).toEqual([
      { label: 'temperature', value: '20', drawn: true },
      { label: 'humidity', value: '55', drawn: true },
      { label: 'battery', value: '90', drawn: true },
    ]);
  });

  it('orders the bars by value when asked, largest or smallest first', () => {
    stubElementSize(360, 200);
    const desc = render(<BarChart widget={widget('bar-chart', names, { sort: 'descending' })} data={data} />);
    expect(bars(desc.container).map((b) => b.label)).toEqual(['battery', 'humidity', 'temperature']);
    cleanup();
    const asc = render(<BarChart widget={widget('bar-chart', names, { sort: 'ascending' })} data={data} />);
    expect(bars(asc.container).map((b) => b.label)).toEqual(['temperature', 'humidity', 'battery']);
  });

  it('makes a bar as tall as its value: a bigger value is a taller bar, on one scale', () => {
    stubElementSize(360, 200);
    const { container } = render(<BarChart widget={widget('bar-chart', names)} data={data} />);
    const heights = [...container.querySelectorAll('rect')].map((r) => Number(r.getAttribute('height')));
    expect(heights[0]).toBeLessThan(heights[1]);
    expect(heights[1]).toBeLessThan(heights[2]);
    // 90 against 20 on a shared scale from zero.
    expect(heights[2] / heights[0]).toBeCloseTo(90 / 20, 1);
  });

  it('prints each category label and value as text, with the unit and precision applied', () => {
    stubElementSize(360, 200);
    const { container } = render(
      <BarChart widget={widget('bar-chart', names, { unit: 'C', precision: 1 })} data={stream({ temperature: 20.25, humidity: 55, battery: 90 })} />,
    );
    const text = [...container.querySelectorAll('text')].map((t) => t.textContent);
    for (const label of ['temperature', 'humidity', 'battery']) expect(text).toContain(label);
    expect(text).toContain('20.3'); // the value above its bar, precision applied
    expect(text).toContain('C'); // the unit, once
    expect(container.querySelector('[data-bar="temperature"] title')?.textContent).toBe('temperature: 20.3 C');
    expect(container.querySelector('svg')?.getAttribute('aria-label')).toContain('humidity: 55.0 C');
  });

  it('keeps a measurement that has not reported as a label with no bar', () => {
    stubElementSize(360, 200);
    const { container } = render(<BarChart widget={widget('bar-chart', ['temperature', 'humidity'])} data={stream({ temperature: 20 })} />);
    expect(bars(container)).toEqual([
      { label: 'temperature', value: '20', drawn: true },
      { label: 'humidity', value: null, drawn: false },
    ]);
  });

  it('lays out as columns in a wide box and as rows in a tall one', () => {
    stubElementSize(360, 200);
    expect(render(<BarChart widget={widget('bar-chart', names)} data={data} />).container.querySelector('svg')?.getAttribute('data-bar-chart')).toBe('vertical');
    cleanup();
    stubElementSize(200, 360);
    const tall = render(<BarChart widget={widget('bar-chart', names)} data={data} />).container;
    expect(tall.querySelector('svg')?.getAttribute('data-bar-chart')).toBe('horizontal');
    // Rows: the first category is the top row, and a bigger value is a longer bar.
    const rects = [...tall.querySelectorAll('rect')];
    const ys = rects.map((r) => Number(r.getAttribute('y')));
    expect(ys).toEqual([...ys].sort((a, b) => a - b));
    const widths = rects.map((r) => Number(r.getAttribute('width')));
    expect(widths[0]).toBeLessThan(widths[2]);
  });

  it('draws a negative value as a bar below the zero line, not a positive one', () => {
    stubElementSize(360, 200);
    const { container } = render(<BarChart widget={widget('bar-chart', ['up', 'down'])} data={stream({ up: 10, down: -10 })} />);
    const [up, down] = [...container.querySelectorAll('rect')];
    expect(Number(down.getAttribute('y'))).toBeGreaterThanOrEqual(Number(up.getAttribute('y')) + Number(up.getAttribute('height')) - 0.001);
  });

  it('trims a label that will not fit, keeping the whole name on the bar title', () => {
    stubElementSize(200, 160);
    const long = 'a-very-long-measurement-name-indeed';
    const { container } = render(<BarChart widget={widget('bar-chart', [long, 'b', 'c', 'd'])} data={stream({ [long]: 1, b: 2, c: 3, d: 4 })} />);
    const label = [...container.querySelectorAll('text')].find((t) => t.textContent?.endsWith('…'));
    expect(label, 'a long label should be cut with an ellipsis').toBeTruthy();
    expect(container.querySelector(`[data-bar="${long}"] title`)?.textContent).toContain(long);
  });

  it('says what to do when no measurement is selected and none has reported', () => {
    const { container } = render(<BarChart widget={widget('bar-chart', [])} data={stream({})} />);
    expect(container.querySelector('[data-level-empty]')?.textContent).toBe(aggregateStrings('en').noCategories);
    expect(container.querySelector('svg')).toBeNull();
  });

  it('titles itself from the options', () => {
    expect(render(<BarChart widget={widget('bar-chart', names, { title: 'Latest' })} data={data} />).container.textContent).toContain('Latest');
  });
});

describe('fitLabel', () => {
  it('keeps a label that fits, cuts one that does not, and drops one with no room at all', () => {
    expect(fitLabel('abc', 5)).toBe('abc');
    expect(fitLabel('abcdefgh', 5)).toBe('abcd…');
    expect(fitLabel('abcdefgh', 1)).toBe('');
  });
});

describe('pie-chart', () => {
  const names = ['temperature', 'humidity', 'battery'];
  const wedges = (c: HTMLElement) =>
    [...c.querySelectorAll('[data-slice]')].map((p) => ({ label: p.getAttribute('data-slice'), fraction: Number(p.getAttribute('data-fraction')) }));

  it('draws each measurement as a wedge sized by its share of the total, clockwise in the order given', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', names)} data={stream({ temperature: 10, humidity: 30, battery: 60 })} />);
    expect(wedges(container)).toEqual([
      { label: 'temperature', fraction: 0.1 },
      { label: 'humidity', fraction: 0.3 },
      { label: 'battery', fraction: 0.6 },
    ]);
  });

  it('orders the wedges by value when asked', () => {
    const { container } = render(
      <PieChart widget={widget('pie-chart', names, { sort: 'descending' })} data={stream({ temperature: 10, humidity: 30, battery: 60 })} />,
    );
    expect(wedges(container).map((w) => w.label)).toEqual(['battery', 'humidity', 'temperature']);
  });

  it('names every category and its share in the legend, as text', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', names)} data={stream({ temperature: 10, humidity: 30, battery: 60 })} />);
    const legend = [...container.querySelectorAll('[data-pie-legend] li')].map((li) => li.textContent);
    expect(legend).toEqual(['temperature 10%', 'humidity 30%', 'battery 60%']);
  });

  it('gives each wedge a title with its value, unit and share', () => {
    const { container } = render(
      <PieChart widget={widget('pie-chart', names, { unit: 'L', precision: 0 })} data={stream({ temperature: 10, humidity: 30, battery: 60 })} />,
    );
    expect(container.querySelector('[data-slice="humidity"] title')?.textContent).toBe('humidity: 30 L (30%)');
  });

  it('draws a single category as a whole disc', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', ['temperature'])} data={stream({ temperature: 5 })} />);
    expect(wedges(container)).toEqual([{ label: 'temperature', fraction: 1 }]);
    expect(container.querySelector('[data-slice] ')?.getAttribute('d')).toMatch(/^M50,2A48,48 0 1 1 50,98/);
  });

  it('lists a category with no reading in the legend at zero, and gives it no wedge', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', ['a', 'b'])} data={stream({ a: 4 })} />);
    expect(wedges(container).map((w) => w.label)).toEqual(['a']);
    expect([...container.querySelectorAll('[data-pie-legend] li')].map((li) => li.textContent)).toEqual(['a 100%', 'b 0%']);
  });

  it('refuses a negative value: shares of a whole cannot include one', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', ['a', 'b'])} data={stream({ a: 5, b: -1 })} />);
    expect(container.querySelector('[data-level-empty]')?.textContent).toBe(aggregateStrings('en').pieNegative);
    expect(container.querySelector('[data-slice]')).toBeNull();
  });

  it('refuses all zeros: there is no share to show', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', ['a', 'b'])} data={stream({ a: 0, b: 0 })} />);
    expect(container.querySelector('[data-level-empty]')?.textContent).toBe(aggregateStrings('en').pieNoShare);
  });

  it('says what to do when no measurement is selected and none has reported', () => {
    const { container } = render(<PieChart widget={widget('pie-chart', [])} data={stream({})} />);
    expect(container.querySelector('[data-level-empty]')?.textContent).toBe(aggregateStrings('en').noCategories);
  });

  it('says its refusals in Spanish when the document is Spanish', () => {
    document.documentElement.lang = 'es';
    const { container } = render(<PieChart widget={widget('pie-chart', ['a', 'b'])} data={stream({ a: 5, b: -1 })} />);
    expect(container.querySelector('[data-level-empty]')?.textContent).toBe(aggregateStrings('es').pieNegative);
    expect(aggregateStrings('es').pieNegative).not.toBe(aggregateStrings('en').pieNegative);
  });
});
