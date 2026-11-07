// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The status grid as an operator meets it: a tile per device for one measurement, each placed in the
// threshold band of that device's OWN latest reading; a device with no number is hollow and counted
// apart, never green; the worst devices can lead; a fleet past what is drawn says so.

import type { FleetMember, FleetState, KnownWidgetInstance, MeasurementSample } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { MAX_TILES, buildTiles, groupOf, tally, tileSide } from './status-grid-model';
import { StatusGrid } from './widgets/status-grid';

const sample = (deviceToken: string, name: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-${name}`,
  deviceToken,
  eventType: 0,
  occurredTime: '2026-11-06T12:00:00Z',
  name,
  value,
  classifier: null,
});

function member(token: string, name: string | null, reading?: number, over: Partial<FleetMember> = {}): FleetMember {
  const latest: Record<string, MeasurementSample> = reading === undefined ? {} : { temp: sample(token, 'temp', reading) };
  return { token, name, latest, seed: 'ready', observed: Object.keys(latest), ...over };
}

const fleet = (members: FleetMember[], over: Partial<FleetState> = {}): FleetState => ({ status: 'ready', members, total: members.length, truncated: false, ...over });

const BANDS = [
  { value: 0, level: 'normal' as const, label: 'Nominal' },
  { value: 80, level: 'warning' as const, label: 'Hot' },
  { value: 95, level: 'critical' as const, label: 'Overheating' },
];

function widget(options: Record<string, unknown> = {}): KnownWidgetInstance {
  return {
    id: 'sg',
    type: 'status-grid',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource: { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' }, measurements: ['temp'] },
    options: { measurement: 'temp', thresholds: BANDS, unit: 'C', ...options },
  } as unknown as KnownWidgetInstance;
}

function show(state: FleetState | undefined, options: Record<string, unknown> = {}) {
  return render(<StatusGrid widget={widget(options)} data={{ latest: {}, samples: [], error: null, members: state }} />);
}

beforeEach(() => {
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

const tile = (c: HTMLElement, token: string) => c.querySelector(`[data-tile="${token}"]`) as HTMLElement;
const order = (c: HTMLElement) => [...c.querySelectorAll('[data-tile]')].map((t) => t.getAttribute('data-tile'));

describe('the model', () => {
  it('places each device in the band of its own reading', () => {
    const tiles = buildTiles([member('a', 'A', 20), member('b', 'B', 85), member('c', 'C', 99)], 'temp', BANDS, false);
    expect(tiles.map((t) => t.group)).toEqual(['ok', 'warning', 'critical']);
  });

  it('never counts a device with no number as OK', () => {
    const tiles = buildTiles([member('a', 'A'), member('b', 'B', 10)], 'temp', BANDS, false);
    expect(tiles[0].group).toBe('none');
    expect(tally(tiles).map((t) => [t.group, t.count])).toEqual([['ok', 1], ['none', 1]]);
  });

  it('treats a reading below the lowest threshold as OK, and a normal band as OK', () => {
    expect(groupOf({ kind: 'value', sample: sample('a', 'temp', -5), value: -5 }, undefined)).toBe('ok');
    expect(groupOf({ kind: 'value', sample: sample('a', 'temp', 5), value: 5 }, BANDS[0])).toBe('ok');
  });

  it('puts the worst first on request, keeping the fleet order among equals', () => {
    const tiles = buildTiles([member('a', 'A', 10), member('b', 'B', 99), member('c', 'C', 85), member('d', 'D', 12)], 'temp', BANDS, true);
    expect(tiles.map((t) => t.token)).toEqual(['b', 'c', 'a', 'd']);
  });

  it('sizes tiles to fit, within the floor and ceiling', () => {
    expect(tileSide(4, 400, 400, 64, 160, 4)).toBe(160);
    expect(tileSide(0, 400, 400, 64, 160, 4)).toBe(64);
    expect(tileSide(1000, 300, 200, 64, 160, 4)).toBe(64);
  });
});

describe('rendering and binding', () => {
  it('draws a tile per device with its name and value, banded by that device', () => {
    const { container } = show(fleet([member('a', 'Pump A', 20), member('b', 'Pump B', 85), member('c', 'Pump C', 99)]));
    expect(order(container)).toEqual(['a', 'b', 'c']);
    expect(tile(container, 'a').textContent).toContain('Pump A');
    expect(tile(container, 'a').textContent).toContain('20');
    expect(tile(container, 'b').getAttribute('data-threshold-level')).toBe('warning');
    expect(tile(container, 'c').getAttribute('data-threshold-level')).toBe('critical');
    expect(tile(container, 'b').getAttribute('aria-label')).toBe('Pump B: 85 C, Hot');
  });

  it('keeps two devices reporting the same name apart', () => {
    const { container } = show(fleet([member('a', 'A', 10), member('b', 'B', 99)]));
    expect(tile(container, 'a').textContent).not.toContain('99');
    expect(tile(container, 'b').textContent).not.toContain('10 ');
  });

  it('draws a device with no reading hollow, with its reason, and counts it apart in the legend', () => {
    const { container } = show(fleet([member('a', 'A', 10), member('b', 'B', undefined, { seed: 'ready' })]));
    expect(tile(container, 'b').getAttribute('data-tile-group')).toBe('none');
    expect(tile(container, 'b').textContent).toContain('—');
    expect(tile(container, 'b').getAttribute('aria-label')).toContain('no data');
    expect(container.querySelector('[data-legend-group="none"]')?.textContent).toContain('No reading');
    expect(container.querySelector('[data-legend-group="ok"]')?.textContent).toContain('1');
  });

  it('says a device does not report the measurement when its profile does not declare it', () => {
    const { container } = show(fleet([member('a', 'A', undefined, { declared: ['humidity'] })]));
    expect(tile(container, 'a').getAttribute('aria-label')).toContain('does not report this');
  });

  it('leads with the worst devices when asked', () => {
    const { container } = show(fleet([member('a', 'A', 10), member('b', 'B', 99)]), { worstFirst: true });
    expect(order(container)).toEqual(['b', 'a']);
  });

  it('says how much of the fleet it left out', () => {
    const many = Array.from({ length: MAX_TILES + 5 }, (_, i) => member(`d${i}`, `D${i}`, 10));
    const { container } = show(fleet(many));
    expect(container.querySelectorAll('[data-tile]').length).toBe(MAX_TILES);
    expect(container.querySelector('[data-status-truncated]')?.textContent).toBe(`Showing ${MAX_TILES} of ${MAX_TILES + 5} devices`);
  });

  it('says so in a fleet the resolver read only a page of', () => {
    const { container } = show(fleet([member('a', 'A', 10)], { total: 900, truncated: true }));
    expect(container.querySelector('[data-status-truncated]')?.textContent).toBe('Showing 1 of 900 devices');
  });

  it('explains every way of having no grid', () => {
    expect(show(undefined).container.textContent).toContain('Loading');
    cleanup();
    expect(show({ status: 'unbound', members: [], total: 0, truncated: false }).container.textContent).toContain('Choose');
    cleanup();
    expect(show(fleet([])).container.textContent).toContain('No devices');
    cleanup();
    const noMeasurement = render(
      <StatusGrid
        widget={{ ...widget(), datasource: { kind: 'anchor', anchor: { relationship: 'a', targetType: 'b', targetToken: 'c' }, measurements: [] }, options: {} } as unknown as KnownWidgetInstance}
        data={{ latest: {}, samples: [], error: null, members: fleet([member('a', 'A', 1)]) }}
      />,
    );
    expect(noMeasurement.container.textContent).toContain('Choose the measurement the tiles show');
  });

  it('words its legend in Spanish', () => {
    document.documentElement.lang = 'es';
    const { container } = show(fleet([member('a', 'A', undefined)]));
    expect(container.querySelector('[data-legend-group="none"]')?.textContent).toContain('Sin lectura');
  });
});
