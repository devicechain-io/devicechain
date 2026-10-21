// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet table as an operator meets it: one row per device, one column per measurement, each cell
// that device's own reading with its band, age and (against a threshold) staleness; every way a cell
// can hold no number says why; the columns sort from the keyboard; the device name drills; and a
// fleet that was cut says how much.

import type { FleetMember, FleetState, KnownWidgetInstance, MeasurementSample, TimeRangeState } from '@devicechain/dashboards';
import { WidgetSelectProvider, WidgetTimeRangeProvider, type WidgetSelect } from '@devicechain/widget-sdk';
import { act, cleanup, fireEvent, render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { FleetTable, columnBands, columnFormats } from './widgets/fleet-table';

const NOW = Date.parse('2026-10-14T12:00:00Z');
const ago = (seconds: number) => new Date(NOW - seconds * 1000).toISOString();

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  document.documentElement.lang = 'en';
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const sample = (deviceToken: string, name: string, value: number, seconds = 10): MeasurementSample => ({
  id: `${deviceToken}-${name}`,
  deviceToken,
  eventType: 0,
  occurredTime: ago(seconds),
  name,
  value,
  classifier: null,
});

function member(token: string, name: string | null, readings: Record<string, number | [number, number]> = {}, over: Partial<FleetMember> = {}): FleetMember {
  const latest: Record<string, MeasurementSample> = {};
  for (const [n, r] of Object.entries(readings)) {
    latest[n] = Array.isArray(r) ? sample(token, n, r[0], r[1]) : sample(token, n, r);
  }
  return { token, name, latest, seed: 'ready', observed: Object.keys(latest), ...over };
}

const fleet = (members: FleetMember[], over: Partial<FleetState> = {}): FleetState => ({
  status: 'ready',
  members,
  total: members.length,
  truncated: false,
  ...over,
});

function widget(options: Record<string, unknown> = {}, measurements = ['temperature', 'humidity']): KnownWidgetInstance {
  return {
    id: 'ft',
    type: 'fleet-table',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 12, rows: 6 } },
    datasource: { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' }, measurements },
    options,
  } as unknown as KnownWidgetInstance;
}

function show(state: FleetState | undefined, options: Record<string, unknown> = {}, measurements?: string[], extra?: ReactElement) {
  const data = { latest: {}, samples: [], error: null, members: state };
  const view = render(
    <WidgetTimeRangeProvider range={undefined}>
      {extra}
      <FleetTable widget={widget(options, measurements)} data={data} />
    </WidgetTimeRangeProvider>,
  );
  return view;
}

const rows = (c: HTMLElement) => [...c.querySelectorAll('tbody tr')].map((tr) => tr.getAttribute('data-device'));
const cell = (c: HTMLElement, device: string, measurement: string) =>
  c.querySelector(`tr[data-device="${device}"] td[data-measurement="${measurement}"]`) as HTMLElement;
const headers = (c: HTMLElement) => [...c.querySelectorAll('thead th')].map((th) => th.textContent?.replace(/[▲▼]/g, '').trim());

describe('one row per device, one column per measurement', () => {
  it('keeps two devices that report the same measurement name as two cells with two values', () => {
    const { container } = show(fleet([member('a', 'Pump A', { temperature: 20 }), member('b', 'Pump B', { temperature: 90 })]));
    expect(cell(container, 'a', 'temperature').textContent).toContain('20');
    expect(cell(container, 'b', 'temperature').textContent).toContain('90');
    expect(cell(container, 'a', 'temperature').textContent).not.toContain('90');
  });

  it('draws a column for each measurement the datasource names, in that order, headed by its name', () => {
    const { container } = show(fleet([member('a', 'Pump A', { temperature: 1, humidity: 2 })]), {}, ['humidity', 'temperature']);
    expect(headers(container)).toEqual(['Device', 'humidity', 'temperature']);
  });

  it('labels a row with the device name, falling back to its token', () => {
    const { container } = show(fleet([member('tok-1', 'Pump A', { temperature: 1 }), member('tok-2', null, { temperature: 2 })]));
    const labels = [...container.querySelectorAll('tbody th')].map((th) => th.textContent);
    expect(labels).toEqual(['Pump A', 'tok-2']);
  });

  it('applies the column heading, unit and precision from the columns option', () => {
    const { container } = show(
      fleet([member('a', 'Pump A', { temperature: 21.456 })]),
      { columns: [{ measurement: 'temperature', label: 'Temp', unit: 'C', precision: 1 }] },
      ['temperature'],
    );
    expect(headers(container)).toEqual(['Device', 'Temp (C)']);
    expect(cell(container, 'a', 'temperature').textContent).toContain('21.5 C');
  });
});

describe('threshold colours', () => {
  const bands = [
    { measurement: 'temperature', value: 0, level: 'normal' },
    { measurement: 'temperature', value: 80, level: 'major' },
    { measurement: 'humidity', value: 50, level: 'critical' },
  ];
  const mixed = fleet([member('a', 'A', { temperature: 70, humidity: 60 }), member('b', 'B', { temperature: 90, humidity: 10 })]);

  const level = (c: HTMLElement, d: string, m: string) => cell(c, d, m).querySelector('[data-threshold-level]')?.getAttribute('data-threshold-level');
  const painted = (c: HTMLElement, d: string, m: string) => /color/.test(cell(c, d, m).querySelector('[data-threshold-level]')?.getAttribute('style') ?? '');

  it('colours each cell by ITS column’s bands and its own value', () => {
    const { container } = show(mixed, { thresholds: bands });
    expect(level(container, 'a', 'temperature')).toBe('normal');
    expect(level(container, 'b', 'temperature')).toBe('major');
    expect(level(container, 'a', 'humidity')).toBe('critical');
    // humidity 10 is below its only band: no band at all.
    expect(level(container, 'b', 'humidity')).toBeUndefined();
  });

  it('paints a real band and leaves the normal band, and no band, neutral', () => {
    const { container } = show(mixed, { thresholds: bands });
    expect(painted(container, 'b', 'temperature')).toBe(true);
    expect(painted(container, 'a', 'temperature')).toBe(false);
    expect(painted(container, 'b', 'humidity')).toBe(false);
  });

  it('does not apply one column’s bands to another', () => {
    const { container } = show(mixed, { thresholds: [{ measurement: 'temperature', value: 0, level: 'critical' }] });
    expect(level(container, 'a', 'humidity')).toBeUndefined();
  });

  it('puts the band on a value exactly at a threshold in the band that threshold starts', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 80 })]), { thresholds: bands }, ['temperature']);
    expect(level(container, 'a', 'temperature')).toBe('major');
  });

  it('reads the list defensively: an entry without a measurement or a usable level drops out', () => {
    const grouped = columnBands([{ value: 1, level: 'major' }, { measurement: 't', value: 1, level: 'purple' }, { measurement: 't', value: 5, level: 'major' }, 'junk', null]);
    expect([...grouped.keys()]).toEqual(['t']);
    expect(grouped.get('t')).toEqual([{ value: 5, level: 'major' }]);
  });
});

describe('freshness per cell', () => {
  const stale = fleet([member('a', 'A', { temperature: [20, 600], humidity: [50, 5] })]);
  const age = (c: HTMLElement, d: string, m: string) => cell(c, d, m).querySelector('[data-cell-age]')?.textContent ?? '';

  it('shows each cell’s own age', () => {
    const { container } = show(stale);
    expect(age(container, 'a', 'temperature')).toMatch(/10 min/);
    expect(age(container, 'a', 'humidity')).toMatch(/5 sec/);
  });

  it('marks a cell stale only against a configured threshold, and only the cell that passed it', () => {
    const { container } = show(stale, { staleAfterSeconds: 300 });
    expect(cell(container, 'a', 'temperature').querySelector('[data-stale]')).not.toBeNull();
    expect(age(container, 'a', 'temperature')).toContain('stale');
    expect(cell(container, 'a', 'humidity').querySelector('[data-stale]')).toBeNull();
  });

  it('never marks anything stale without a threshold, however old', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: [20, 86400 * 30] })]));
    expect(container.querySelector('[data-stale]')).toBeNull();
    expect(container.textContent).not.toContain('stale');
  });

  it('ages from the freeze instant while the board is paused', () => {
    const range: TimeRangeState = { spanSeconds: 3600, paused: true, pausedAt: NOW - 590_000 };
    const data = { latest: {}, samples: [], error: null, members: stale };
    const { container } = render(
      <WidgetTimeRangeProvider range={range}>
        <FleetTable widget={widget({ staleAfterSeconds: 300 })} data={data} />
      </WidgetTimeRangeProvider>,
    );
    // The reading is 600 s old by the wall clock but only 10 s old at the freeze: neither old nor stale.
    expect(age(container, 'a', 'temperature')).toMatch(/10 sec/);
    expect(container.querySelector('[data-stale]')).toBeNull();
  });

  it('moves the ages on with the clock', () => {
    const { container } = show(stale);
    const before = age(container, 'a', 'humidity');
    act(() => void vi.advanceTimersByTime(60_000));
    expect(age(container, 'a', 'humidity')).not.toBe(before);
  });
});

describe('ages only where a reading lags the fleet (ageLagSeconds)', () => {
  const ageOf = (c: HTMLElement, d: string, m: string) => cell(c, d, m).querySelector('[data-cell-age]')?.textContent ?? null;
  // Two machines reporting every few seconds, one that fell silent four minutes ago, and the whole
  // fleet eight days old: the lag is between readings, never against the viewer's clock.
  const days = 8 * 86400;
  const site = fleet([
    member('a', 'A', { temperature: [20, days + 1], humidity: [50, days + 3] }),
    member('b', 'B', { temperature: [21, days + 2] }),
    member('c', 'C', { temperature: [22, days + 240] }),
  ]);

  it('drops the age line under a reading that keeps up with the newest, however old the fleet', () => {
    const { container } = show(site, { ageLagSeconds: 30 });
    expect(ageOf(container, 'a', 'temperature')).toBeNull();
    expect(ageOf(container, 'a', 'humidity')).toBeNull();
    expect(ageOf(container, 'b', 'temperature')).toBeNull();
  });

  it('shows the age of the reading that fell behind by the lag, and only that one', () => {
    const { container } = show(site, { ageLagSeconds: 30 });
    expect(ageOf(container, 'c', 'temperature')).toMatch(/8 days/);
    expect(container.querySelectorAll('[data-cell-age]')).toHaveLength(1);
  });

  it('counts a reading exactly the lag behind as behind', () => {
    const { container } = show(site, { ageLagSeconds: 239 });
    expect(ageOf(container, 'c', 'temperature')).not.toBeNull();
    const at = show(site, { ageLagSeconds: 240 });
    expect(ageOf(at.container, 'c', 'temperature')).toBeNull();
  });

  it('measures the lag from the newest SHOWN column, not from a measurement the table leaves out', () => {
    const withHidden = fleet([member('a', 'A', { temperature: [20, 100], pressure: [1, 0] }), member('b', 'B', { temperature: [21, 101] })]);
    const { container } = show(withHidden, { ageLagSeconds: 30 }, ['temperature']);
    expect(container.querySelectorAll('[data-cell-age]')).toHaveLength(0);
  });

  it('still shows the age of a stale cell that keeps up with the fleet', () => {
    const { container } = show(site, { ageLagSeconds: 30, staleAfterSeconds: 60 });
    expect(ageOf(container, 'a', 'temperature')).toContain('stale');
  });

  it('shows every age when the option is absent or not a positive number', () => {
    for (const options of [{}, { ageLagSeconds: 0 }, { ageLagSeconds: -5 }, { ageLagSeconds: 'x' }]) {
      const { container } = show(site, options);
      expect(container.querySelectorAll('[data-cell-age]'), JSON.stringify(options)).toHaveLength(4);
      cleanup();
    }
  });
});

describe('explained empty cells', () => {
  const state = fleet([
    member('quiet', 'Quiet', {}, { observed: [] }),
    member('partial', 'Partial', { humidity: 40 }, { declared: ['humidity'] }),
    member('loading', 'Loading', {}, { seed: 'pending' }),
    member('failed', 'Failed', {}, { seed: 'failed' }),
  ]);

  it('says "no data in range" for a device that reported nothing', () => {
    const { container } = show(state);
    expect(cell(container, 'quiet', 'temperature').textContent).toBe('no data in range');
  });

  it('says "does not report this" for a device whose profile does not declare the measurement', () => {
    const { container } = show(state);
    expect(cell(container, 'partial', 'temperature').textContent).toBe('does not report this');
    expect(cell(container, 'partial', 'humidity').textContent).toContain('40');
  });

  it('says "no data in range", not "does not report this", for a declared measurement a live device has sent nothing of', () => {
    // A dead temperature probe beside a heartbeat that keeps arriving: the profile declares both.
    const probe = member('probe', 'Probe', { humidity: 40 }, { declared: ['temperature', 'humidity'] });
    const { container } = show(fleet([probe]));
    expect(cell(container, 'probe', 'temperature').textContent).toBe('no data in range');
  });

  it('says "no data in range" for every device whose declaration is not known, whatever it has sent', () => {
    const { container } = show(fleet([member('unknown', 'Unknown', { humidity: 40 })]));
    expect(cell(container, 'unknown', 'temperature').textContent).toBe('no data in range');
  });

  it('says "not loaded", never "no data in range", for a reported measurement whose newest reading was not read', () => {
    const { container } = show(fleet([member('unread', 'Unread', {}, { observed: ['temperature'], unread: ['temperature'] })]));
    expect(cell(container, 'unread', 'temperature').textContent).toBe('not loaded');
  });

  it('shows a pending seed as loading and a failed one as unreadable, never as an absence', () => {
    const { container } = show(state);
    expect(cell(container, 'loading', 'temperature').textContent).toBe('…');
    expect(cell(container, 'loading', 'temperature').querySelector('[aria-label]')?.getAttribute('aria-label')).toBe('Loading');
    expect(cell(container, 'failed', 'temperature').textContent).toBe('could not be read');
  });

  it('speaks Spanish when the viewer does', () => {
    document.documentElement.lang = 'es';
    const { container } = show(state);
    expect(cell(container, 'quiet', 'temperature').textContent).toBe('sin datos en el rango');
    expect(cell(container, 'partial', 'temperature').textContent).toBe('no informa esta medición');
  });

  it('explains every whole-widget state in words', () => {
    const text = (s: FleetState | undefined) => {
      const { container } = show(s);
      const out = container.textContent;
      cleanup();
      return out;
    };
    expect(text(undefined)).toContain('Loading');
    expect(text({ status: 'loading', members: [], total: 0, truncated: false })).toContain('Loading');
    expect(text({ status: 'unbound', members: [], total: 0, truncated: false })).toContain('Choose a device group');
    expect(text({ status: 'forbidden', members: [], total: 0, truncated: false })).toContain('do not have access');
    expect(text({ status: 'unsupported', members: [], total: 0, truncated: false })).toContain('cannot list a fleet');
    expect(text(fleet([]))).toContain('No devices in this group');
  });
});

describe('truncation', () => {
  it('says how many of how many devices it shows when the fleet was cut', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 1 })], { total: 812, truncated: true }));
    expect(container.querySelector('[data-fleet-truncated]')?.textContent).toBe('Showing 1 of 812 devices');
  });

  it('says nothing when the fleet is whole', () => {
    const { container } = show(fleet([member('a', 'A', { temperature: 1 })]));
    expect(container.querySelector('[data-fleet-truncated]')).toBeNull();
    expect(container.textContent).not.toMatch(/Showing/);
  });
});

describe('order', () => {
  const state = fleet([
    member('a', 'Charlie', { temperature: 30 }),
    member('b', 'alpha', { temperature: 10 }),
    member('c', 'Bravo', { temperature: 20 }),
    member('d', 'Delta', {}, { observed: [] }),
  ]);

  it('starts by device name', () => {
    expect(rows(show(state).container)).toEqual(['b', 'c', 'a', 'd']);
  });

  it('starts as the author set it', () => {
    expect(rows(show(state, { sortBy: 'temperature', sortDescending: true }).container)).toEqual(['a', 'c', 'b', 'd']);
  });

  it('sorts from the heading button: first ascending, then descending, with aria-sort to match', () => {
    const { container } = show(state);
    const th = () => container.querySelectorAll('thead th')[1];
    const press = () => fireEvent.click(th().querySelector('button')!);
    expect(th().getAttribute('aria-sort')).toBe('none');
    press();
    expect(rows(container)).toEqual(['b', 'c', 'a', 'd']);
    expect(th().getAttribute('aria-sort')).toBe('ascending');
    press();
    expect(rows(container)).toEqual(['a', 'c', 'b', 'd']);
    expect(th().getAttribute('aria-sort')).toBe('descending');
  });

  it('puts devices with no value last both ways', () => {
    const { container } = show(state, {}, ['temperature']);
    const btn = container.querySelectorAll('thead th')[1].querySelector('button')!;
    fireEvent.click(btn);
    expect(rows(container)[rows(container).length - 1]).toBe('d');
    fireEvent.click(btn);
    expect(rows(container)[rows(container).length - 1]).toBe('d');
  });

  it('is operable from the keyboard: every heading is a real button, focusable and labelled', () => {
    const { container } = show(state);
    for (const th of container.querySelectorAll('thead th')) {
      const button = th.querySelector('button');
      expect(button, 'a heading with no button cannot be reached by keyboard').not.toBeNull();
      expect(button!.getAttribute('type')).toBe('button');
      expect(button!.getAttribute('title')).toMatch(/^Sort by /);
      expect(button!.tabIndex).toBeGreaterThanOrEqual(0);
    }
  });

  it('makes the scroll region focusable so a wide table can be scrolled sideways', () => {
    const region = show(state).container.querySelector('[data-fleet-scroll]')!;
    expect(region.getAttribute('tabindex')).toBe('0');
    expect(region.getAttribute('role')).toBe('region');
    expect((region as HTMLElement).style.overflow).toBe('auto');
  });
});

describe('paging', () => {
  const many = fleet(Array.from({ length: 45 }, (_, i) => member(`d${String(i).padStart(2, '0')}`, `Dev ${String(i).padStart(2, '0')}`, { temperature: i })));

  it('shows one page of rows and a pager with the range', () => {
    const { container } = show(many, { pageSize: 20 });
    expect(rows(container)).toHaveLength(20);
    expect(container.querySelector('[data-fleet-footer]')?.textContent).toContain('1–20 of 45');
  });

  it('moves between pages, and the buttons disable at the ends', () => {
    const { container } = show(many, { pageSize: 20 });
    const next = container.querySelector('button[aria-label="Next page"]') as HTMLButtonElement;
    const prev = container.querySelector('button[aria-label="Previous page"]') as HTMLButtonElement;
    expect(prev.disabled).toBe(true);
    fireEvent.click(next);
    expect(rows(container)[0]).toBe('d20');
    fireEvent.click(next);
    expect(rows(container)).toHaveLength(5);
    expect(next.disabled).toBe(true);
    expect(container.querySelector('[data-fleet-footer]')?.textContent).toContain('41–45 of 45');
  });

  it('returns to the first page when the order changes', () => {
    const { container } = show(many, { pageSize: 20 });
    fireEvent.click(container.querySelector('button[aria-label="Next page"]')!);
    fireEvent.click(container.querySelectorAll('thead th')[1].querySelector('button')!);
    expect(container.querySelector('[data-fleet-footer]')?.textContent).toContain('1–20 of 45');
  });

  it('has no pager for a fleet that fits one page', () => {
    expect(show(fleet([member('a', 'A', { temperature: 1 })])).container.querySelector('[data-fleet-footer]')).toBeNull();
  });

  it('never renders more rows than a page, however big the fleet', () => {
    const huge = fleet(Array.from({ length: 500 }, (_, i) => member(`d${i}`, `Dev ${i}`, { temperature: i })));
    expect(rows(show(huge, { pageSize: 100 }).container)).toHaveLength(100);
  });
});

describe('the device column', () => {
  const state = fleet([member('tok-1', 'Pump A', { temperature: 1 })]);

  it('drills: pressing the name re-points the target slot to that device', () => {
    const select = vi.fn<WidgetSelect>();
    const data = { latest: {}, samples: [], error: null, members: state };
    const { container } = render(
      <WidgetSelectProvider select={select}>
        <FleetTable widget={widget({ selectionTarget: 'machine' })} data={data} />
      </WidgetSelectProvider>,
    );
    const button = container.querySelector('tbody th button') as HTMLButtonElement;
    expect(button.textContent).toBe('Pump A');
    expect(button.title).toBe('Show Pump A in the linked widgets');
    fireEvent.click(button);
    expect(select).toHaveBeenCalledWith({ slot: 'machine', binding: { kind: 'device', deviceToken: 'tok-1' } });
  });

  it('is plain text when no target slot is configured', () => {
    const select = vi.fn<WidgetSelect>();
    const data = { latest: {}, samples: [], error: null, members: state };
    const { container } = render(
      <WidgetSelectProvider select={select}>
        <FleetTable widget={widget()} data={data} />
      </WidgetSelectProvider>,
    );
    expect(container.querySelector('tbody th button')).toBeNull();
  });

  it('is plain text when the host wires no selection', () => {
    const { container } = show(state, { selectionTarget: 'machine' });
    expect(container.querySelector('tbody th button')).toBeNull();
  });
});

describe('option parsing', () => {
  it('reads column formats defensively', () => {
    const formats = columnFormats([
      { measurement: 't', label: 'T', unit: 'C', precision: 2 },
      { measurement: 'h', precision: -1 },
      { label: 'orphan' },
      'junk',
    ]);
    expect(formats.get('t')).toEqual({ label: 'T', unit: 'C', precision: 2 });
    expect(formats.get('h')).toEqual({});
    expect(formats.size).toBe(2);
    expect(columnFormats(undefined).size).toBe(0);
  });
});
