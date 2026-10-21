// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// On a narrow board the fleet table becomes a card per device: a column cut off at the right edge is
// unread on a wall display, where nobody scrolls sideways (the readability gate fails it), so the
// narrow layout carries every device, measurement, reason and control the table does, and the same
// sorting and drill, without a row wider than the widget.

import type { FleetMember, FleetState, KnownWidgetInstance, MeasurementSample } from '@devicechain/dashboards';
import { WidgetSelectProvider, type WidgetSelect } from '@devicechain/widget-sdk';
import { cleanup, fireEvent, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ width: 390 }));

vi.mock('@devicechain/widget-sdk', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/widget-sdk')>();
  return { ...actual, useWidgetScale: () => ({ size: { width: h.width, height: 300 } }) };
});

import { FleetTable, STACK_BELOW_PX } from './widgets/fleet-table';

beforeEach(() => {
  h.width = 390;
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

const sample = (deviceToken: string, name: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-${name}`,
  deviceToken,
  eventType: 0,
  occurredTime: new Date().toISOString(),
  name,
  value,
  classifier: null,
});

const member = (token: string, name: string, readings: Record<string, number>, over: Partial<FleetMember> = {}): FleetMember => ({
  token,
  name,
  latest: Object.fromEntries(Object.entries(readings).map(([n, v]) => [n, sample(token, n, v)])),
  seed: 'ready',
  observed: Object.keys(readings),
  ...over,
});

const state: FleetState = {
  status: 'ready',
  total: 3,
  truncated: false,
  members: [
    member('a', 'Pump A', { temperature: 30, humidity: 40 }),
    member('b', 'Pump B', { temperature: 10 }, { declared: ['temperature'] }),
    member('c', 'Pump C', {}, { observed: [] }),
  ],
};

const widget = (options: Record<string, unknown> = {}): KnownWidgetInstance =>
  ({
    id: 'ft',
    type: 'fleet-table',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource: { kind: 'anchor', anchor: { relationship: 'r', targetType: 'area', targetToken: 'z' }, measurements: ['temperature', 'humidity'] },
    options,
  }) as unknown as KnownWidgetInstance;

const show = (options: Record<string, unknown> = {}, extra?: { select: WidgetSelect }) => {
  const ui = <FleetTable widget={widget(options)} data={{ latest: {}, samples: [], error: null, members: state }} />;
  return render(extra ? <WidgetSelectProvider select={extra.select}>{ui}</WidgetSelectProvider> : ui);
};

const devices = (c: HTMLElement) => [...c.querySelectorAll('[data-fleet-stacked] > [data-device]')].map((e) => e.getAttribute('data-device'));

describe('the narrow layout', () => {
  it('drops the age line under readings that keep up with the fleet, as the table does', () => {
    // Every reading here is stamped together, so none lags the newest: with the option, no ages.
    expect(show().container.querySelectorAll('[data-cell-age]').length).toBeGreaterThan(0);
    cleanup();
    expect(show({ ageLagSeconds: 30 }).container.querySelectorAll('[data-cell-age]')).toHaveLength(0);
  });

  it('is cards, not a table, below the breakpoint', () => {
    const { container } = show();
    expect(container.querySelector('table')).toBeNull();
    expect(container.querySelector('[data-fleet-stacked]')).not.toBeNull();
    expect(devices(container)).toEqual(['a', 'b', 'c']);
  });

  it('is the table at and above the breakpoint, and when the body has not been measured', () => {
    h.width = STACK_BELOW_PX;
    expect(show().container.querySelector('table')).not.toBeNull();
    cleanup();
    h.width = 0;
    expect(show().container.querySelector('table')).not.toBeNull();
    cleanup();
    h.width = STACK_BELOW_PX - 1;
    expect(show().container.querySelector('table')).toBeNull();
  });

  it('gives each device its own value per measurement, and says why a cell is empty', () => {
    const { container } = show();
    const cell = (d: string, m: string) => container.querySelector(`[data-device="${d}"] [data-measurement="${m}"]`)?.textContent ?? '';
    expect(cell('a', 'temperature')).toContain('30');
    expect(cell('b', 'temperature')).toContain('10');
    expect(cell('a', 'humidity')).toContain('40');
    expect(cell('b', 'humidity')).toContain('does not report this');
    expect(cell('c', 'temperature')).toContain('no data in range');
  });

  it('sorts from buttons that say which order is on', () => {
    const { container } = show();
    const buttons = [...container.querySelectorAll('[data-fleet-stacked] > div:first-child button')] as HTMLButtonElement[];
    expect(buttons.map((b) => b.textContent?.replace(/[▲▼]/g, '').trim())).toEqual(['Device', 'temperature', 'humidity']);
    expect(buttons[0].getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(buttons[1]);
    expect(devices(container)).toEqual(['b', 'a', 'c']);
    expect(buttons[1].getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(buttons[1]);
    expect(devices(container)).toEqual(['a', 'b', 'c']);
  });

  it('drills from the device name, as the table does', () => {
    const select = vi.fn<WidgetSelect>();
    const { container } = show({ selectionTarget: 'machine' }, { select });
    fireEvent.click(container.querySelector('[data-device="b"] button')!);
    expect(select).toHaveBeenCalledWith({ slot: 'machine', binding: { kind: 'device', deviceToken: 'b' } });
  });

  it('lets a long value or heading break instead of running past the card', () => {
    const { container } = show({ columns: [{ measurement: 'temperature', label: 'An extremely long heading for a narrow board' }] });
    const label = [...container.querySelectorAll('[data-device="a"] [data-measurement="temperature"] span')][0] as HTMLElement;
    expect(label.style.overflowWrap).toBe('anywhere');
    expect((container.querySelector('[data-device="a"] [data-cell-empty], [data-device="a"] [data-cell-age]') as HTMLElement).style.overflowWrap).toBe('anywhere');
  });
});
