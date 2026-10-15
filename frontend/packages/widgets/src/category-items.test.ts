// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { categoryItems, parseSort, pieProblem } from './category-items';

const sample = (name: string, value: number | null): MeasurementSample => ({
  id: name,
  deviceToken: 'd',
  eventType: 0,
  occurredTime: '2026-10-14T12:00:00Z',
  name,
  value,
  classifier: null,
});
const latest = (pairs: Record<string, number | null>) => ({
  latest: Object.fromEntries(Object.entries(pairs).map(([n, v]) => [n, sample(n, v)])),
});
const widget = (measurements: string[]): WidgetInstance =>
  ({ id: 'w', type: 'bar-chart', datasource: { kind: 'device', deviceToken: 'd', measurements } }) as unknown as WidgetInstance;

describe('categoryItems', () => {
  it('has one category per measurement the datasource names, in that order, labelled by name', () => {
    const items = categoryItems(
      widget(['humidity', 'temperature', 'battery']),
      latest({ temperature: 20, humidity: 55, battery: 90 }),
      'none',
    );
    expect(items).toEqual([
      { label: 'humidity', value: 55 },
      { label: 'temperature', value: 20 },
      { label: 'battery', value: 90 },
    ]);
  });

  it('keeps a measurement that has not reported, with no value, instead of dropping it or reading zero', () => {
    const items = categoryItems(widget(['temperature', 'humidity']), latest({ temperature: 20 }), 'none');
    expect(items).toEqual([
      { label: 'temperature', value: 20 },
      { label: 'humidity', value: null },
    ]);
  });

  it('with none named, shows every measurement that has reported, as first seen', () => {
    expect(categoryItems(widget([]), latest({ b: 2, a: 1 }), 'none').map((i) => i.label)).toEqual(['b', 'a']);
  });

  it('names a measurement once even if the datasource lists it twice', () => {
    expect(categoryItems(widget(['a', 'a', 'b']), latest({ a: 1, b: 2 }), 'none').map((i) => i.label)).toEqual(['a', 'b']);
  });

  it('sorts ascending and descending by value, with the unreported last either way', () => {
    const data = latest({ a: 30, b: 10, c: 20 });
    const w = widget(['a', 'missing', 'b', 'c']);
    expect(categoryItems(w, data, 'ascending').map((i) => i.label)).toEqual(['b', 'c', 'a', 'missing']);
    expect(categoryItems(w, data, 'descending').map((i) => i.label)).toEqual(['a', 'c', 'b', 'missing']);
  });

  it('keeps the datasource order among equal values', () => {
    const data = latest({ a: 5, b: 5, c: 5 });
    expect(categoryItems(widget(['c', 'a', 'b']), data, 'descending').map((i) => i.label)).toEqual(['c', 'a', 'b']);
    expect(categoryItems(widget(['c', 'a', 'b']), data, 'ascending').map((i) => i.label)).toEqual(['c', 'a', 'b']);
  });
});

describe('parseSort', () => {
  it('reads the three orders and keeps the datasource order for anything else', () => {
    expect(parseSort('ascending')).toBe('ascending');
    expect(parseSort('descending')).toBe('descending');
    expect(parseSort('none')).toBe('none');
    expect(parseSort(undefined)).toBe('none');
    expect(parseSort('largest')).toBe('none');
  });
});

describe('pieProblem', () => {
  it('refuses a negative value: it has no share of a whole', () => {
    expect(
      pieProblem([
        { label: 'a', value: 5 },
        { label: 'b', value: -1 },
      ]),
    ).toBe('negative');
  });
  it('refuses all zeros: there is nothing to divide', () => {
    expect(
      pieProblem([
        { label: 'a', value: 0 },
        { label: 'b', value: 0 },
      ]),
    ).toBe('noShare');
  });
  it('accepts positive values, a lone zero among them, and categories that have not reported', () => {
    expect(
      pieProblem([
        { label: 'a', value: 5 },
        { label: 'b', value: 0 },
        { label: 'c', value: null },
      ]),
    ).toBeUndefined();
  });
  it('names nothing reported yet as no data, not as a pie to draw', () => {
    expect(pieProblem([{ label: 'a', value: null }])).toBe('noData');
  });
});
