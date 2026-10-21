// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The stat card's trend strip: off it is the card as it was; on it draws the buckets the card was
// folded from, neutral, with a gap where buckets are missing; empty and single-point series are
// explained by drawing nothing / a dot; and it keeps out of the way at 390 px and at the smallest size.

import type { AggregateResult, AggregateStreamState, AggregateTrendPoint, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { aggregateSeries } from './aggregate-fold';
import { sparklineGeometry, trendPoints } from './sparkline';
import { StatCard } from './widgets/stat-card';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
beforeAll(() => {
  document.documentElement.lang = 'en';
});

function sizedAt(width: number, height: number) {
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

const point = (minute: number, avg: number, over: Partial<AggregateTrendPoint> = {}): AggregateTrendPoint => ({
  bucketStart: new Date(Date.parse('2026-10-14T11:00:00Z') + minute * 60_000).toISOString(),
  count: 10,
  min: avg - 1,
  max: avg + 1,
  sum: avg * 10,
  avg,
  ...over,
});

const ready = (series?: AggregateTrendPoint[]): AggregateResult => ({
  status: 'ready',
  startTime: '2026-10-14T11:00:00Z',
  endTime: '2026-10-14T12:00:00Z',
  spanSeconds: 3600,
  byName: { temperature: { count: 40, min: 12, max: 31, sum: 800, avg: 20 } },
  ...(series ? { series: { temperature: series } } : {}),
});

const state = (aggregates: AggregateResult): AggregateStreamState => ({ aggregates, latest: {}, error: null });

const widget = (options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'w',
    type: 'stat-card',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    datasource: { kind: 'device', deviceToken: 'd', measurements: ['temperature'] },
    options,
  }) as unknown as WidgetInstance;

const SERIES = [point(0, 10), point(10, 14), point(20, 12), point(30, 18)];

function show(options: Record<string, unknown>, aggregates: AggregateResult) {
  const { container } = render(<StatCard widget={widget(options)} data={state(aggregates)} />);
  return {
    container,
    strip: container.querySelector('[data-sparkline]'),
    path: container.querySelector('[data-sparkline] path')?.getAttribute('d'),
    figure: container.querySelector('[data-level-readout]')?.textContent,
  };
}

describe('a stat card with the sparkline off', () => {
  it('is the card as it was: no strip, even when the data carries a series', () => {
    sizedAt(300, 300);
    const off = show({ stat: 'avg' }, ready(SERIES));
    expect(off.strip).toBeNull();
    const html = off.container.innerHTML;
    cleanup();
    const without = show({ stat: 'avg' }, ready());
    expect(html).toBe(without.container.innerHTML);
  });

  it('is unchanged by an explicit false', () => {
    sizedAt(300, 300);
    expect(show({ stat: 'avg', sparkline: false }, ready(SERIES)).strip).toBeNull();
  });
});

describe('a stat card with the sparkline on', () => {
  it('draws the series it was given, neutral, beside the same figure', () => {
    sizedAt(300, 300);
    const on = show({ stat: 'avg', sparkline: true }, ready(SERIES));
    expect(on.strip?.getAttribute('data-sparkline')).toBe('4');
    expect(on.path?.match(/[ML]/g)).toEqual(['M', 'L', 'L', 'L']);
    expect(on.container.querySelector('[data-sparkline] path')?.getAttribute('stroke')).toBe('hsl(var(--muted-foreground))');
    expect(on.figure).toBe('20');
    expect(on.strip?.getAttribute('aria-label')).toBe('Average trend over the last 1h');
  });

  it('stays neutral when the figure is in an abnormal band', () => {
    sizedAt(300, 300);
    const on = show({ stat: 'avg', sparkline: true, thresholds: [{ value: 0, level: 'critical' }] }, ready(SERIES));
    expect(on.container.querySelector('[data-sparkline] path')?.getAttribute('stroke')).toBe('hsl(var(--muted-foreground))');
    expect(on.container.querySelector('[data-stat]')?.getAttribute('data-threshold-level')).toBe('critical');
  });

  it('plots the statistic the card shows', () => {
    const p = point(0, 10);
    expect(trendPoints([p], 'max')[0].value).toBe(11);
    expect(trendPoints([p], 'min')[0].value).toBe(9);
    expect(trendPoints([p], 'avg')[0].value).toBe(10);
    expect(trendPoints([p], 'last')[0].value).toBe(10);
    expect(trendPoints([p], 'sum')[0].value).toBe(100);
    expect(trendPoints([p], 'count')[0].value).toBe(10);
    expect(trendPoints(undefined, 'avg')).toEqual([]);
  });

  it('draws nothing for an empty or missing series', () => {
    sizedAt(300, 300);
    expect(show({ stat: 'avg', sparkline: true }, ready([])).strip).toBeNull();
    cleanup();
    expect(show({ stat: 'avg', sparkline: true }, ready()).strip).toBeNull();
  });

  it('draws a single point as a dot, with no line', () => {
    sizedAt(300, 300);
    const one = show({ stat: 'avg', sparkline: true }, ready([point(5, 12)]));
    expect(one.strip?.getAttribute('data-sparkline')).toBe('1');
    expect(one.container.querySelector('[data-sparkline] path')).toBeNull();
    expect(one.container.querySelector('[data-sparkline-end]')).not.toBeNull();
  });

  it('is left out where it would take the figure below its smallest readable size', () => {
    sizedAt(300, 60);
    const small = show({ stat: 'avg', sparkline: true }, ready(SERIES));
    expect(small.strip).toBeNull();
    expect(small.figure).toBe('20');
  });

  it('fits at 390 px: the full width of the card, never wider', () => {
    sizedAt(358, 200);
    const on = show({ stat: 'avg', sparkline: true }, ready(SERIES));
    const strip = on.strip as HTMLElement;
    expect(strip).not.toBeNull();
    expect(strip.style.width).toBe('100%');
    expect(on.container.querySelector('[data-sparkline] svg')?.getAttribute('viewBox')).toMatch(/^0 0 100 \d+$/);
  });
});

describe('sparklineGeometry', () => {
  const at = (minute: number, value: number) => ({ at: Date.parse('2026-10-14T11:00:00Z') + minute * 60_000, value });

  it('puts the lowest value at the bottom and the highest at the top, time left to right', () => {
    const g = sparklineGeometry([at(0, 5), at(10, 15)], 28, 600);
    const [x0, y0, x1, y1] = g.d.match(/-?[0-9]+(\.[0-9]+)?/g)!.map(Number);
    expect(x1).toBeGreaterThan(x0);
    expect(y1).toBeLessThan(y0);
  });

  it('centres a flat series rather than pinning it to an edge', () => {
    const g = sparklineGeometry([at(0, 7), at(10, 7)], 28, 600);
    expect(g.d).toContain(' 14.00');
  });

  it('breaks the line where whole buckets are missing', () => {
    const g = sparklineGeometry([at(0, 1), at(10, 2), at(60, 3), at(70, 4)], 28, 600);
    expect(g.d.match(/M/g)).toHaveLength(2);
  });

  it('has nothing to draw for no points', () => {
    expect(sparklineGeometry([], 28, 600)).toEqual({ d: '', end: undefined, points: 0 });
  });
});

describe('aggregateSeries', () => {
  const bucket = (start: string, over: Record<string, unknown> = {}) => ({
    bucketStart: start,
    name: 'temperature',
    avg: 20,
    min: 10,
    max: 30,
    sum: 200,
    count: 10,
    ...over,
  });

  it('lays buckets out oldest first and skips the empty ones', () => {
    const s = aggregateSeries([
      bucket('2026-10-14T11:10:00Z'),
      bucket('2026-10-14T11:00:00Z'),
      bucket('2026-10-14T11:05:00Z', { count: 0, min: null, max: null, sum: null, avg: null }),
    ]);
    expect(s.temperature.map((p) => p.bucketStart)).toEqual(['2026-10-14T11:00:00Z', '2026-10-14T11:10:00Z']);
  });

  it('merges buckets of one instant exactly as the totals merge', () => {
    const s = aggregateSeries([bucket('2026-10-14T11:00:00Z'), bucket('2026-10-14T11:00:00Z', { min: 5, max: 50, sum: 400, count: 20 })]);
    expect(s.temperature).toEqual([{ bucketStart: '2026-10-14T11:00:00Z', count: 30, min: 5, max: 50, sum: 600, avg: 20 }]);
  });
});
