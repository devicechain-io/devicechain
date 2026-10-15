// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The bar chart set to one bar per device: a bar for each member of the anchor, for one measurement,
// from the same per-device feed as the fleet table. The existing one-bar-per-measurement chart is
// untouched by the option (category-widgets.test.tsx holds that), so what is pinned here is the new
// mode: the device stays beside the value, the order is the chosen one, the value-label rule is the
// chart's own, and a fleet cut short says so inside the chart.

import type { FleetMember, FleetState, KnownWidgetInstance, MeasurementSample } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { BarChart, MAX_DEVICE_BARS, MAX_LABELLED_BARS } from './widgets/bar-chart';

beforeEach(() => {
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

const sample = (deviceToken: string, name: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-${name}`,
  deviceToken,
  eventType: 0,
  occurredTime: '2026-10-14T12:00:00Z',
  name,
  value,
  classifier: null,
});

const member = (token: string, name: string | null, readings: Record<string, number> = {}, over: Partial<FleetMember> = {}): FleetMember => ({
  token,
  name,
  latest: Object.fromEntries(Object.entries(readings).map(([n, v]) => [n, sample(token, n, v)])),
  seed: 'ready',
  observed: Object.keys(readings),
  ...over,
});

const fleet = (members: FleetMember[], over: Partial<FleetState> = {}): FleetState => ({
  status: 'ready',
  members,
  total: members.length,
  truncated: false,
  ...over,
});

function widget(options: Record<string, unknown> = {}): KnownWidgetInstance {
  return {
    id: 'bar',
    type: 'bar-chart',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource: { kind: 'anchor', anchor: { relationship: 'r', targetType: 'area', targetToken: 'z' }, measurements: ['temperature', 'humidity'] },
    options: { groupBy: 'device', measurement: 'temperature', ...options },
  } as unknown as KnownWidgetInstance;
}

function show(members: FleetState | undefined, options: Record<string, unknown> = {}) {
  return render(<BarChart widget={widget(options)} data={{ latest: {}, samples: [], error: null, members }} />);
}

const bars = (c: HTMLElement) => [...c.querySelectorAll('[data-bar]')].map((g) => [g.getAttribute('data-bar'), g.getAttribute('data-value')]);

describe('one bar per device', () => {
  it('draws a bar for each member, valued by that device’s own reading', () => {
    const { container } = show(fleet([member('a', 'Pump A', { temperature: 20 }), member('b', 'Pump B', { temperature: 90 })]));
    expect(bars(container)).toEqual([
      ['Pump A', '20'],
      ['Pump B', '90'],
    ]);
  });

  it('keeps two devices that share a display name as two bars', () => {
    const { container } = show(fleet([member('a', 'Pump', { temperature: 1 }), member('b', 'Pump', { temperature: 2 })]));
    expect(container.querySelectorAll('[data-bar]')).toHaveLength(2);
    expect(bars(container).map((b) => b[1])).toEqual(['1', '2']);
  });

  it('reads the measurement named by the option, not the datasource’s first', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 1, humidity: 77 })]), { measurement: 'humidity' });
    expect(bars(container)).toEqual([['A', '77']]);
  });

  it('falls back to the datasource’s first measurement', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 5, humidity: 77 })]), { measurement: undefined });
    expect(bars(container)).toEqual([['A', '5']]);
  });

  it('sorts the bars, with a device that has no reading last and drawn without a bar', () => {
    const members = fleet([member('a', 'A', { temperature: 20 }), member('b', 'B', {}, { observed: [] }), member('c', 'C', { temperature: 90 }), member('d', 'D', { temperature: 5 })]);
    expect(bars(show(members, { sort: 'descending' }).container).map((b) => b[0])).toEqual(['C', 'A', 'D', 'B']);
    cleanup();
    expect(bars(show(members, { sort: 'ascending' }).container).map((b) => b[0])).toEqual(['D', 'A', 'C', 'B']);
    cleanup();
    const { container } = show(members, { sort: 'none' });
    expect(bars(container).map((b) => b[0])).toEqual(['A', 'B', 'C', 'D']);
    expect(container.querySelector('[data-bar="B"] rect')).toBeNull();
  });

  it('prints each bar’s value while there are few enough, and drops the printed values past the chart’s limit', () => {
    const few = fleet(Array.from({ length: MAX_LABELLED_BARS }, (_, i) => member(`d${i}`, `D${i}`, { temperature: i + 1 })));
    const { container } = show(few);
    expect(container.querySelectorAll('[data-bar] text')).toHaveLength(MAX_LABELLED_BARS * 2); // value + label
    cleanup();
    const many = fleet(Array.from({ length: MAX_LABELLED_BARS + 1 }, (_, i) => member(`d${i}`, `D${i}`, { temperature: i + 1 })));
    const crowded = show(many).container;
    expect(crowded.querySelectorAll('[data-bar] text')).toHaveLength(MAX_LABELLED_BARS + 1); // labels only
    // Every value is still in the accessible description.
    expect(crowded.querySelector('svg')!.getAttribute('aria-label')).toContain('D12: 13');
  });
});

describe('a fleet that is cut', () => {
  it('draws at most MAX_DEVICE_BARS and says how many devices there are', () => {
    const big = fleet(Array.from({ length: MAX_DEVICE_BARS + 40 }, (_, i) => member(`d${i}`, `D${i}`, { temperature: i })));
    const { container } = show(big);
    expect(container.querySelectorAll('[data-bar]')).toHaveLength(MAX_DEVICE_BARS);
    expect(container.querySelector('[data-bar-note]')?.textContent).toBe(`Showing ${MAX_DEVICE_BARS} of ${MAX_DEVICE_BARS + 40} devices`);
  });

  it('keeps the largest when sorted, so the cut falls on the smallest', () => {
    const big = fleet(Array.from({ length: MAX_DEVICE_BARS + 5 }, (_, i) => member(`d${i}`, `D${i}`, { temperature: i })));
    const { container } = show(big, { sort: 'descending' });
    const names = bars(container).map((b) => b[0]);
    expect(names[0]).toBe(`D${MAX_DEVICE_BARS + 4}`);
    expect(names).not.toContain('D0');
  });

  it('counts against the anchor, not only the chart: a page the resolver cut is just as cut off', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 1 })], { total: 812, truncated: true }));
    expect(container.querySelector('[data-bar-note]')?.textContent).toBe('Showing 1 of 812 devices');
  });

  it('says nothing about a fleet that is whole', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 1 })]));
    expect(container.querySelector('[data-bar-note]')).toBeNull();
  });
});

describe('explained states', () => {
  const text = (s: FleetState | undefined, options: Record<string, unknown> = {}) => {
    const out = show(s, options).container.textContent;
    cleanup();
    return out;
  };

  it('says why there is no chart', () => {
    expect(text(undefined)).toContain('Loading');
    expect(text({ status: 'unbound', members: [], total: 0, truncated: false })).toContain('Choose a device group');
    expect(text({ status: 'forbidden', members: [], total: 0, truncated: false })).toContain('do not have access');
    expect(text({ status: 'unsupported', members: [], total: 0, truncated: false })).toContain('cannot list a fleet');
    expect(text(fleet([]))).toContain('No devices in this group');
  });

  it('asks for a measurement when there is none to compare', () => {
    const w = widget({ measurement: undefined });
    (w as unknown as { datasource: { measurements: string[] } }).datasource.measurements = [];
    const { container } = render(<BarChart widget={w} data={{ latest: {}, samples: [], error: null, members: fleet([member('a', 'A')]) }} />);
    expect(container.textContent).toContain('Choose the measurement to compare');
  });
});
