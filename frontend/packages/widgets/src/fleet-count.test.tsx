// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The fleet count card: how many devices of a group are in an abnormal band of one measurement
// ("3 of 21"), the plain device count, and every state in which there is no honest figure.

import type { FleetMember, FleetState, KnownWidgetInstance, MeasurementSample } from '@devicechain/dashboards';
import { cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { attentionCount } from './fleet-count-model';
import { FleetCount } from './widgets/fleet-count';

const NOW = Date.parse('2026-10-14T12:00:00Z');

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  document.documentElement.lang = 'en';
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
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

const sample = (deviceToken: string, name: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-${name}`,
  deviceToken,
  eventType: 0,
  occurredTime: new Date(NOW - 10_000).toISOString(),
  name,
  value,
  classifier: null,
});

function member(token: string, fuel?: number, over: Partial<FleetMember> = {}): FleetMember {
  const latest: Record<string, MeasurementSample> = fuel === undefined ? {} : { fuel: sample(token, 'fuel', fuel) };
  return { token, name: token, latest, seed: 'ready', observed: Object.keys(latest), ...over };
}

const fleet = (members: FleetMember[], over: Partial<FleetState> = {}): FleetState => ({
  status: 'ready',
  members,
  total: members.length,
  truncated: false,
  ...over,
});

// "Low fuel": critical from 0 up to 15, normal from 15.
const LOW = [
  { value: 0, level: 'critical', label: 'Low' },
  { value: 15, level: 'normal' },
];

function widget(options: Record<string, unknown> = {}): KnownWidgetInstance {
  return {
    id: 'fc',
    type: 'fleet-count',
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    datasource: { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' }, measurements: ['fuel'] },
    options,
  } as unknown as KnownWidgetInstance;
}

function show(state: FleetState | undefined, options: Record<string, unknown> = {}) {
  const data = { latest: {}, samples: [], error: null, members: state };
  const { container } = render(<FleetCount widget={widget({ measurement: 'fuel', thresholds: LOW, ...options })} data={data} />);
  const card = container.querySelector<HTMLElement>('[data-fleet-count]');
  return {
    container,
    card,
    figure: container.querySelector('[data-level-readout]')?.textContent,
    caption: container.querySelector('[data-fleet-count-caption]')?.textContent,
    notes: [...container.querySelectorAll('[data-fleet-count-note]')].map((n) => n.textContent),
    empty: container.querySelector('[data-level-empty]')?.textContent,
    figureColor: container.querySelector<HTMLElement>('[data-level-readout]')?.style.color,
  };
}

describe('attentionCount', () => {
  const t = [
    { value: 0, level: 'critical' as const, label: 'Low' },
    { value: 15, level: 'normal' as const },
    { value: 80, level: 'warning' as const, label: 'High' },
  ];

  it('counts the devices whose newest reading is in an abnormal band', () => {
    const c = attentionCount([member('a', 5), member('b', 50), member('c', 10), member('d', 90)], 'fuel', t);
    expect(c).toMatchObject({ members: 4, flagged: 3, reporting: 4, worst: 'critical' });
  });

  it('puts a value on a threshold in the band that threshold starts', () => {
    expect(attentionCount([member('a', 15)], 'fuel', t).flagged).toBe(0);
    expect(attentionCount([member('a', 80)], 'fuel', t).flagged).toBe(1);
  });

  it('does not flag a value below the lowest threshold', () => {
    expect(attentionCount([member('a', -3)], 'fuel', t).flagged).toBe(0);
  });

  it('counts a device with no reading apart: neither healthy nor flagged', () => {
    const c = attentionCount([member('a', 5), member('b'), member('c', 50)], 'fuel', t);
    expect(c).toMatchObject({ members: 3, flagged: 1, reporting: 2 });
  });

  it('leaves a device whose profile does not declare the measurement out of the "of N"', () => {
    const plant = member('plant', undefined, { observed: ['throughput'], declared: ['throughput'] });
    const c = attentionCount([member('a', 5), member('b'), plant, member('c', 50)], 'fuel', t);
    expect(c).toMatchObject({ members: 3, notMeasured: 1, flagged: 1, reporting: 2 });
  });

  // The device's PROFILE decides whether it is outside the question, never what it has sent.
  it('counts a dead probe with a live heartbeat as not reporting: it declares the measurement, so it stays in N', () => {
    const deadProbe = member('probe', undefined, { observed: ['battery', 'rssi'], declared: ['fuel', 'battery', 'rssi'] });
    const c = attentionCount([member('a', 5), member('b', 50), deadProbe], 'fuel', t);
    expect(c).toMatchObject({ members: 3, notMeasured: 0, flagged: 1, reporting: 2 });
    const s = show(fleet([member('a', 5), member('b', 50), deadProbe]));
    expect(s.figure).toBe('1of 3');
    expect(s.notes).toEqual(['1 not reporting']);
  });

  it('counts a device whose profile declares the measurement and which sent nothing at all as not reporting', () => {
    const c = attentionCount([member('a', 5), member('quiet', undefined, { observed: [], declared: ['fuel'] })], 'fuel', t);
    expect(c).toMatchObject({ members: 2, notMeasured: 0, reporting: 1 });
  });

  it('leaves out a device whose profile does not declare the measurement even though it sent nothing at all', () => {
    const c = attentionCount([member('a', 5), member('quiet', undefined, { observed: [], declared: ['throughput'] })], 'fuel', t);
    expect(c).toMatchObject({ members: 1, notMeasured: 1 });
  });

  it('counts a device whose declaration is not known as declared, not left out: a failed or pending lookup never excludes', () => {
    // No `declared` on the member: the profile was not read (yet), or its read failed.
    const unknown = member('u', undefined, { observed: ['battery'] });
    const c = attentionCount([member('a', 5), unknown], 'fuel', t);
    expect(c).toMatchObject({ members: 2, notMeasured: 0, reporting: 1 });
    expect(show(fleet([member('a', 5), unknown])).notes).toEqual(['1 not reporting']);
  });

  it('counts a calculated metric the profile declares like any other', () => {
    // A calculated metric is in the profile's metric list beside the sensed ones.
    const calc = member('c', undefined, { observed: [], declared: ['fuel_pct'] });
    expect(attentionCount([calc], 'fuel_pct', t)).toMatchObject({ members: 1, notMeasured: 0 });
  });

  it('names the band only when every flagged device shares it', () => {
    expect(attentionCount([member('a', 5), member('b', 6)], 'fuel', t).label).toBe('Low');
    expect(attentionCount([member('a', 5), member('b', 90)], 'fuel', t).label).toBeUndefined();
    expect(attentionCount([member('a', 5), member('b', 90)], 'fuel', t).worst).toBe('critical');
  });

  it('reads the worst level among several', () => {
    const levels = [
      { value: 0, level: 'warning' as const },
      { value: 50, level: 'major' as const },
    ];
    expect(attentionCount([member('a', 10), member('b', 60)], 'fuel', levels).worst).toBe('major');
  });
});

describe('the attention count', () => {
  it('says how many of the fleet are in an abnormal band, and names the band', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5), member('b', 8), member('c', 50), member('d', 60)]));
    expect(s.figure).toBe('2of 4');
    expect(s.caption).toBe('Low');
    expect(s.card?.getAttribute('data-flagged')).toBe('2');
    expect(s.card?.getAttribute('data-of')).toBe('4');
    expect(s.card?.getAttribute('data-threshold-level')).toBe('critical');
    expect(s.figureColor).not.toBe('');
  });

  it('falls back to "need attention" when the flagged devices are in a band with no label', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5), member('b', 50)]), { thresholds: [{ value: 0, level: 'major' }, { value: 15, level: 'normal' }] });
    expect(s.caption).toBe('need attention');
  });

  it('stays neutral when nothing needs attention: 0 of N is calm, in the foreground colour', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 50), member('b', 60), member('c', 70)]));
    expect(s.figure).toBe('0of 3');
    expect(s.card?.getAttribute('data-threshold-level')).toBeNull();
    expect(s.figureColor).toBe('hsl(var(--foreground))');
    expect(s.caption).toBe('need attention');
  });

  it('notes the devices that report nothing, and does not count them as healthy or flagged', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5), member('b'), member('c', 50)]));
    expect(s.figure).toBe('1of 3');
    expect(s.notes).toEqual(['1 not reporting']);
  });

  it('says a device that does not report the measurement is outside the count, not silent', () => {
    sizedAt(300, 300);
    const plant = member('plant', undefined, { observed: ['throughput'], declared: ['throughput'] });
    const s = show(fleet([member('a', 5), member('b', 50), plant]));
    expect(s.figure).toBe('1of 2');
    expect(s.card?.getAttribute('data-of')).toBe('2');
    expect(s.notes).toEqual(['1 does not report this']);
  });

  it('keeps a silent device and a device that does not report the measurement apart', () => {
    sizedAt(300, 300);
    const other = (token: string) => member(token, undefined, { observed: ['throughput'], declared: ['throughput'] });
    const s = show(fleet([member('a', 5), member('b'), other('p'), other('q')]));
    expect(s.figure).toBe('1of 2');
    expect(s.notes).toEqual(['1 not reporting', '2 do not report this']);
  });

  it('has no "of N" when no device of the group reports the measurement', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('p', undefined, { observed: ['throughput'], declared: ['throughput'] })]));
    expect(s.empty).toBe('No device in this group reports this');
    expect(s.figure).toBeUndefined();
  });

  it('does not speak for a whole truncated group when none of the devices read reports the measurement', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('p', undefined, { observed: ['throughput'], declared: ['throughput'] })], { total: 812, truncated: true }));
    expect(s.empty).toBe('None of the 1 of 812 devices read reports this');
    expect(s.figure).toBeUndefined();
  });

  it('has no figure while any member is still loading: a partial count is an undercount', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5), member('b', undefined, { seed: 'pending' }), member('c', 50)]));
    expect(s.empty).toBe('Loading');
    expect(s.figure).toBeUndefined();
  });

  it('says a device whose read failed or did not load could not be read, not that it is silent', () => {
    sizedAt(300, 300);
    const s = show(
      fleet([
        member('a', 5),
        member('b', undefined, { seed: 'failed' }),
        member('c', undefined, { observed: ['fuel'], unread: ['fuel'] }),
        member('d'),
      ]),
    );
    expect(s.figure).toBe('1of 4');
    expect(s.notes).toEqual(['1 not reporting', '2 could not be read']);
  });

  it('leaves the figure room for every note it shows', () => {
    sizedAt(300, 120);
    const one = show(fleet([member('a', 5), member('b')]));
    const oneFit = one.container.querySelector<HTMLElement>('[data-level-readout]')?.style.fontSize;
    cleanup();
    const two = show(fleet([member('a', 5), member('b')], { total: 812, truncated: true }));
    expect(two.notes).toHaveLength(2);
    const twoFit = two.container.querySelector<HTMLElement>('[data-level-readout]')?.style.fontSize;
    expect(parseFloat(twoFit ?? '0')).toBeLessThan(parseFloat(oneFit ?? '0'));
  });

  it('says so when the group is larger than the feed reads, and counts what it has', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5), member('b', 50)], { total: 812, truncated: true }));
    expect(s.figure).toBe('1of 2');
    expect(s.notes).toEqual(['Showing 2 of 812 devices']);
  });

  it('explains a card with no measurement or no bands rather than printing a zero', () => {
    sizedAt(300, 300);
    const members = fleet([member('a', 5)]);
    const data = { latest: {}, samples: [], error: null, members };
    const noMeasurement = render(<FleetCount widget={{ ...widget({ thresholds: LOW }), datasource: { kind: 'anchor', anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'p' }, measurements: [] } } as never} data={data} />);
    expect(noMeasurement.container.querySelector('[data-level-empty]')?.textContent).toBe('Choose the measurement to watch');
    cleanup();
    expect(show(members, { thresholds: [] }).empty).toBe('Add thresholds to say what needs attention');
  });

  it('explains each state in which the fleet cannot be counted', () => {
    sizedAt(300, 300);
    expect(show(undefined).empty).toBe('Loading');
    cleanup();
    expect(show(fleet([], { status: 'loading' })).empty).toBe('Loading');
    cleanup();
    expect(show(fleet([], { status: 'unbound' })).empty).toBe('Choose a device group for this widget');
    cleanup();
    expect(show(fleet([], { status: 'unsupported' })).empty).toBe('This data source cannot list a fleet');
    cleanup();
    expect(show(fleet([])).empty).toBe('No devices in this group');
  });

  it('speaks Spanish for a Spanish viewer', () => {
    sizedAt(300, 300);
    document.documentElement.lang = 'es';
    const s = show(fleet([member('a', 5), member('b'), member('c', 50)]), { thresholds: [{ value: 0, level: 'major' }, { value: 15, level: 'normal' }] });
    expect(s.caption).toBe('requieren atención');
    expect(s.notes).toEqual(['1 sin informar']);
  });
});

describe('the device count', () => {
  it('is the size of the group, whatever the readings say', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5), member('b'), member('c', 50)]), { show: 'devices', thresholds: undefined, measurement: undefined });
    expect(s.figure).toBe('3');
    expect(s.caption).toBe('devices');
    expect(s.card?.getAttribute('data-fleet-count')).toBe('devices');
    expect(s.figureColor).toBe('hsl(var(--foreground))');
  });

  it('counts the whole group when the feed read only part of it', () => {
    sizedAt(300, 300);
    const s = show(fleet([member('a', 5)], { total: 812, truncated: true }), { show: 'devices' });
    expect(s.figure).toBe('812');
    // The figure is exact past the page, so nothing says it covers part of the fleet.
    expect(s.notes).toEqual([]);
  });

  it('is 0 for an empty group: a count, not an empty state', () => {
    sizedAt(300, 300);
    expect(show(fleet([]), { show: 'devices' }).figure).toBe('0');
  });
});

describe('at 390 px', () => {
  it('lays out in the one column the card has, nothing wider than it', () => {
    sizedAt(358, 160);
    const s = show(fleet([member('a', 5), member('b'), member('c', 50)], { total: 812, truncated: true }));
    const card = s.card as HTMLElement;
    expect(card.style.overflow).toBe('hidden');
    expect(card.style.flexDirection).toBe('column');
    expect(s.figure).toBe('1of 3');
  });
});
