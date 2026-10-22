// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The channel adapters honor a descriptor's binding policy by the option NAMES it declares —
// not by widget id. This is the behavioral half of the option-read evidence in
// options.test.ts: a policy reference counts as a read only because these prove it is one.

import { WIDGET_TYPES, type WidgetDescriptor, type WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { alarmSubscription, commandSubscription, measurementSpan, measurementWindow, membersMode } from './binding-policy';
import { builtinWidgetCatalog } from './builtin-descriptors';

const asDescriptor = (binding: Record<string, unknown>): WidgetDescriptor =>
  ({ binding }) as unknown as WidgetDescriptor;

const widget = (options: Record<string, unknown> | undefined, type = 'acme/x'): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid' as const, col: 0, row: 0, cols: 1, rows: 1 } },
    options,
  }) as unknown as WidgetInstance;

describe('binding policy adapters', () => {
  it('reads an arbitrary declared option key, with default and floor', () => {
    const d = asDescriptor({ pageSize: { option: 'rows', default: 20, min: 1 } });
    expect(commandSubscription(d, widget({ rows: 7 })).pageSize).toBe(7);
    expect(commandSubscription(d, widget({})).pageSize).toBe(20);
    expect(commandSubscription(d, widget({ rows: 0 })).pageSize).toBe(1);
    expect(commandSubscription(d, widget({ rows: 'many' })).pageSize).toBe(20);
  });

  it('a constant page size ignores options', () => {
    const d = asDescriptor({ pageSize: { const: 50 } });
    expect(alarmSubscription(d, widget({ pageSize: 3, maxRows: 3 })).pageSize).toBe(50);
  });

  it('maps the alarm filters through their declared keys', () => {
    const d = asDescriptor({
      pageSize: { const: 10 },
      state: { option: 'lifecycle' },
      severity: { option: 'sev', default: 'MAJOR' },
      acknowledged: { option: 'acked' },
    });
    expect(alarmSubscription(d, widget({ lifecycle: 'CLEARED', acked: 'false' }))).toMatchObject({
      state: 'CLEARED',
      severity: 'MAJOR',
      acknowledged: false,
      pageSize: 10,
    });
    expect(alarmSubscription(d, widget({ acked: 'true' })).acknowledged).toBe(true);
    expect(alarmSubscription(d, widget({ acked: 'maybe' })).acknowledged).toBeUndefined();
    // A key the descriptor does not name is not read, whatever the bag holds.
    expect(alarmSubscription(d, widget({ state: 'ACTIVE' })).state).toBeUndefined();
  });

  it('reads a measurement window by its declared key and leaves it unset otherwise', () => {
    const d = asDescriptor({ window: { option: 'samples', min: 1 } });
    expect(measurementWindow(d, widget({ samples: 90 }))).toBe(90);
    expect(measurementWindow(d, widget({ window: 90 }))).toBeUndefined();
    expect(measurementWindow(asDescriptor({}), widget({ window: 90 }))).toBeUndefined();
  });

  it('carries the datasource through to the subscription', () => {
    const w = { ...widget({}), datasource: { kind: 'device', deviceToken: 't', measurements: [] } } as WidgetInstance;
    expect(alarmSubscription(asDescriptor({ pageSize: { const: 1 } }), w).datasource).toEqual(w.datasource);
  });
});

// The built-in policies reproduce the subscriptions the old by-id code produced.
describe('the built-in policies', () => {
  const d = (id: (typeof WIDGET_TYPES)[number]) => builtinWidgetCatalog.get(id) as WidgetDescriptor;

  it('alarm-table reads maxRows (default 50, floor 1); alarm-count always reads 50', () => {
    expect(alarmSubscription(d('alarm-table'), widget({ maxRows: 12 }, 'alarm-table')).pageSize).toBe(12);
    expect(alarmSubscription(d('alarm-table'), widget({}, 'alarm-table')).pageSize).toBe(50);
    expect(alarmSubscription(d('alarm-table'), widget({ maxRows: -4 }, 'alarm-table')).pageSize).toBe(1);
    expect(alarmSubscription(d('alarm-count'), widget({ maxRows: 12 }, 'alarm-count')).pageSize).toBe(50);
  });

  it('command-button reads maxRows (default 20)', () => {
    expect(commandSubscription(d('command-button'), widget({}, 'command-button')).pageSize).toBe(20);
    expect(commandSubscription(d('command-button'), widget({ maxRows: 5 }, 'command-button')).pageSize).toBe(5);
  });

  it('alarm widgets map state, severity and acknowledged', () => {
    const sub = alarmSubscription(
      d('alarm-table'),
      widget({ state: 'ACTIVE', severity: 'CRITICAL', acknowledged: 'false' }, 'alarm-table'),
    );
    expect(sub).toMatchObject({ state: 'ACTIVE', severity: 'CRITICAL', acknowledged: false });
  });

  it('measurement widgets read window, and the timeseries chart a span instead', () => {
    expect(measurementWindow(d('latest-card'), widget({ window: 600 }, 'latest-card'))).toBe(600);
    expect(measurementWindow(d('gauge'), widget({}, 'gauge'))).toBeUndefined();
    // The chart draws a span, so it carries no sample count at all.
    expect(measurementWindow(d('timeseries-chart'), widget({ window: 600 }, 'timeseries-chart'))).toBeUndefined();
  });

  describe('measurementSpan', () => {
    const BOARD = 6 * 3600;

    it('follows the board span when the widget pins none', () => {
      expect(measurementSpan(d('timeseries-chart'), widget({}, 'timeseries-chart'), BOARD)).toBe(BOARD);
    });

    it('is pinned by its own windowSeconds, whatever the board says', () => {
      expect(measurementSpan(d('timeseries-chart'), widget({ windowSeconds: 300 }, 'timeseries-chart'), BOARD)).toBe(300);
    });

    it('ignores a pinned span that is not a usable one, rather than clamping it', () => {
      for (const bad of [0, 59, 604801, 90.5, -60, Number.NaN]) {
        expect(measurementSpan(d('timeseries-chart'), widget({ windowSeconds: bad }, 'timeseries-chart'), BOARD), String(bad)).toBe(BOARD);
      }
    });

    it('is undefined for a widget that is not time-ranged, pinned or not', () => {
      expect(measurementSpan(d('latest-card'), widget({ windowSeconds: 300 }, 'latest-card'), BOARD)).toBeUndefined();
      expect(measurementSpan(d('gauge'), widget({}, 'gauge'), BOARD)).toBeUndefined();
    });

    it('is undefined outside a board (no range imposed) unless the widget pins one', () => {
      expect(measurementSpan(d('timeseries-chart'), widget({}, 'timeseries-chart'), undefined)).toBeUndefined();
      expect(measurementSpan(d('timeseries-chart'), widget({ windowSeconds: 300 }, 'timeseries-chart'), undefined)).toBe(300);
    });
  });
});

describe('membersMode', () => {
  it('is off for a widget whose descriptor declares no members policy', () => {
    expect(membersMode(asDescriptor({}), widget({ groupBy: 'device' }))).toBe(false);
  });

  it('is always on for a constant policy, whatever the options hold', () => {
    expect(membersMode(asDescriptor({ members: { const: true } }), widget(undefined))).toBe(true);
    expect(membersMode(asDescriptor({ members: { const: true } }), widget({}))).toBe(true);
  });

  it('follows a declared option: on only while it holds the named value', () => {
    const d = asDescriptor({ members: { option: 'groupBy', equals: 'device' } });
    expect(membersMode(d, widget({ groupBy: 'device' }))).toBe(true);
    expect(membersMode(d, widget({ groupBy: 'measurement' }))).toBe(false);
    expect(membersMode(d, widget({}))).toBe(false);
    expect(membersMode(d, widget(undefined))).toBe(false);
    expect(membersMode(d, widget({ groupBy: 7 }))).toBe(false);
  });

  it('is declared by the fleet table and the fleet count (always) and the bar chart and time-series chart (per device), and by no other built-in', () => {
    const on = (type: string, options?: Record<string, unknown>) =>
      membersMode(builtinWidgetCatalog.get(type as never)!, widget(options, type));
    expect(on('fleet-table', {})).toBe(true);
    expect(on('fleet-count', {})).toBe(true);
    expect(on('bar-chart', {})).toBe(false);
    expect(on('bar-chart', { groupBy: 'device' })).toBe(true);
    expect(on('timeseries-chart', {})).toBe(false);
    expect(on('timeseries-chart', { groupBy: 'device' })).toBe(true);
    for (const type of WIDGET_TYPES) {
      if (type === 'fleet-table' || type === 'fleet-count' || type === 'bar-chart' || type === 'timeseries-chart') continue;
      expect(on(type, { groupBy: 'device' }), type).toBe(false);
    }
  });
});
