// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The channel adapters honor a descriptor's binding policy by the option NAMES it declares —
// not by widget id. This is the behavioral half of the option-read evidence in
// options.test.ts: a policy reference counts as a read only because these prove it is one.

import { WIDGET_TYPES, type WidgetDescriptor, type WidgetInstance } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import { alarmSubscription, commandSubscription, measurementWindow } from './binding-policy';
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

  it('measurement widgets read window', () => {
    expect(measurementWindow(d('timeseries-chart'), widget({ window: 600 }, 'timeseries-chart'))).toBe(600);
    expect(measurementWindow(d('gauge'), widget({}, 'gauge'))).toBeUndefined();
  });
});
