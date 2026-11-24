// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Why a single-device widget has nothing to show, said through the connected widget: the slot is
// unbound, the device's profile does not declare the measurement, or it does and nothing arrived.

import type { DashboardHub, DatasourceSelector, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

// The chart engine draws nothing in jsdom; the chart's empty state is what is under test.
vi.mock('@devicechain/widget-sdk/echarts', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@devicechain/widget-sdk/echarts')>()),
  EChart: () => <div data-testid="echart" />,
}));

// Deliver the real empty-state chunk late: a fixed macrotask is not import readiness.
vi.mock('./no-reading', async (importOriginal) => {
  await new Promise((resolve) => setTimeout(resolve, 200));
  return importOriginal();
});

import { ConnectedWidget } from './connected-widget';
import { ASK_DELAY_MS, emptyKindFor, lastReadingHint, SETTLE_MS } from './no-reading-core';
import { loadLevelWidgets } from './widgets/level-lazy';
import { NoReading as LazyNoReading } from './widgets/no-reading-lazy';

// Import/transform work is real I/O, independent of the empty-state clock. Await the actual
// lazy component's preload (not just import('../no-reading'), which leaves lazyChunk unloaded)
// and the level family before enabling fake timers. The delayed-import control above makes
// a missing readiness wait fail even on a fast machine.
beforeAll(async () => {
  await Promise.all([LazyNoReading.preload(), loadLevelWidgets()]);
});

beforeEach(() => {
  document.documentElement.lang = 'en';
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const flush = () => act(async () => undefined);
const advance = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));
const asked = () => advance(ASK_DELAY_MS);
const settled = () => advance(SETTLE_MS - ASK_DELAY_MS);

const widgetOf = (type: string, datasource: DatasourceSelector, options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 3, rows: 3 } },
    datasource,
    options,
  }) as unknown as WidgetInstance;

const device = (m = 'fuel_level'): DatasourceSelector => ({ kind: 'device', deviceToken: 'd-1', measurements: [m] });

function hubOf(declared: readonly string[] | null | 'absent', samples: MeasurementSample[] = []) {
  const declaredMetricsForDevice = vi.fn(async () => (declared === 'absent' ? null : declared));
  const hub = {
    subscribeWidget: (_d: DatasourceSelector, sink: { next: (s: MeasurementSample) => void }) => {
      for (const s of samples) sink.next(s);
      return () => {};
    },
    isDatasourceAvailable: async () => true,
    ...(declared === 'absent' ? {} : { declaredMetricsForDevice }),
  } as unknown as DashboardHub;
  return { hub, declaredMetricsForDevice };
}

const state = (container: Element) => container.querySelector('[data-empty-state]')?.getAttribute('data-empty-state');

describe('which empty state', () => {
  it('unbound outranks everything, and a measurement the profile omits is not-reported', () => {
    expect(emptyKindFor({ unbound: true, measurement: 'x', declared: ['y'] })).toBe('unbound');
    expect(emptyKindFor({ unbound: false, measurement: 'x', declared: ['y'] })).toBe('not-reported');
    expect(emptyKindFor({ unbound: false, measurement: 'x', declared: ['x'] })).toBe('no-data');
  });

  it('reads an unknown declaration as declared, never as absent', () => {
    expect(emptyKindFor({ unbound: false, measurement: 'x', declared: undefined })).toBe('no-data');
    expect(emptyKindFor({ unbound: false, measurement: 'x', declared: null })).toBe('no-data');
    expect(emptyKindFor({ unbound: false, measurement: undefined, declared: ['y'] })).toBe('no-data');
  });

  it('a reading the widget holds proves the device reports it, whatever the profile declares', () => {
    expect(emptyKindFor({ unbound: false, measurement: 'x', declared: ['y'], reported: true })).toBe('no-data');
    expect(emptyKindFor({ unbound: true, measurement: 'x', declared: ['y'], reported: true })).toBe('unbound');
  });
});

describe('through the connected widget', () => {
  it('a card for a measurement the device profile does not declare says it will never report it', async () => {
    const f = hubOf(['temperature']);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    await settled();
    expect(f.declaredMetricsForDevice).toHaveBeenCalledWith('d-1');
    expect(state(container)).toBe('not-reported');
    expect(container.textContent).toContain('"fuel_level"');
    expect(container.textContent).not.toContain('—');
  });

  it('a board still loading makes no claim for the first moment, then says why', async () => {
    const f = hubOf(['temperature']);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    expect(container.querySelector('[data-empty-pending]')).not.toBeNull();
    expect(state(container)).toBeUndefined();
    await advance(ASK_DELAY_MS - 1);
    expect(f.declaredMetricsForDevice).not.toHaveBeenCalled();
    await advance(1);
    // The lookup has answered, but the reading may still be on its way.
    expect(f.declaredMetricsForDevice).toHaveBeenCalledExactlyOnceWith('d-1');
    expect(state(container)).toBeUndefined();
    await advance(SETTLE_MS - ASK_DELAY_MS - 1);
    expect(state(container)).toBeUndefined();
    expect(container.querySelector('[data-empty-pending]')).not.toBeNull();
    await advance(1);
    expect(state(container)).toBe('not-reported');
  });

  it('a chart of several measurements never says the device does not report them', async () => {
    const f = hubOf(['temperature']);
    const two: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['fuel_level', 'temperature'] };
    const { container } = render(<ConnectedWidget widget={widgetOf('timeseries-chart', two)} hub={f.hub} />);
    await flush();
    await advance(SETTLE_MS - 1);
    expect(state(container)).toBeUndefined();
    await advance(1);
    expect(state(container)).toBe('no-data');
    await asked();
    expect(f.declaredMetricsForDevice).not.toHaveBeenCalled();
    expect(state(container)).toBe('no-data');
  });

  it('an answer about the device it was bound to is never read as the answer about the next one', async () => {
    // d-1's profile omits fuel_level; d-2's answer is slow to come.
    const declaredMetricsForDevice = vi.fn((token: string) => (token === 'd-1' ? Promise.resolve(['temperature']) : new Promise<never>(() => {})));
    const hub = {
      subscribeWidget: () => () => {},
      isDatasourceAvailable: async () => true,
      declaredMetricsForDevice,
    } as unknown as DashboardHub;
    const { container, rerender } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={hub} />);
    await flush();
    await asked();
    await settled();
    expect(state(container)).toBe('not-reported');
    rerender(<ConnectedWidget widget={widgetOf('latest-card', { kind: 'device', deviceToken: 'd-2', measurements: ['fuel_level'] })} hub={hub} />);
    await flush();
    // The previous device's declaration must disappear as soon as the binding changes.
    expect(state(container)).toBe('no-data');
    await asked();
    await settled();
    expect(declaredMetricsForDevice).toHaveBeenCalledWith('d-2');
    expect(state(container)).toBe('no-data');
  });

  it('a declared measurement with nothing in range says no data in range', async () => {
    const f = hubOf(['fuel_level']);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    await settled();
    expect(state(container)).toBe('no-data');
  });

  it('a host that cannot say what is declared never claims the device does not report it', async () => {
    const f = hubOf('absent');
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    await settled();
    expect(state(container)).toBe('no-data');
  });

  it('a slot with no device bound says so, and asks the source nothing', async () => {
    const f = hubOf(['temperature']);
    const unbound = { kind: 'unbound', measurements: ['fuel_level'] } as DatasourceSelector;
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', unbound)} hub={f.hub} />);
    await flush();
    expect(state(container)).toBe('unbound');
    await asked();
    await settled();
    expect(state(container)).toBe('unbound');
    expect(f.declaredMetricsForDevice).not.toHaveBeenCalled();
  });

  it('a widget with a reading asks nothing and draws no empty state', async () => {
    const sample = { id: 's', deviceToken: 'd-1', eventType: 0, occurredTime: new Date().toISOString(), name: 'fuel_level', value: 42, classifier: null };
    const f = hubOf(['temperature'], [sample]);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    await settled();
    expect(state(container)).toBeUndefined();
    expect(f.declaredMetricsForDevice).not.toHaveBeenCalled();
  });

  it('the level widgets explain themselves the same way', async () => {
    for (const type of ['battery', 'progress-ring', 'level-gauge']) {
      const f = hubOf(['temperature']);
      const { container, unmount } = render(<ConnectedWidget widget={widgetOf(type, device(), { min: 0, max: 100 })} hub={f.hub} />);
      await flush();
      await asked();
      expect(state(container), type).toBeUndefined();
      await settled();
      expect(state(container), type).toBe('not-reported');
      unmount();
    }
  });

  it('a source error stays an explained, neutral state and names the failure', async () => {
    const hub = {
      subscribeWidget: (_d: DatasourceSelector, sink: { error?: (e: unknown) => void }) => {
        sink.error?.(new Error('socket closed'));
        return () => {};
      },
      isDatasourceAvailable: async () => true,
    } as unknown as DashboardHub;
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={hub} />);
    await flush();
    expect(state(container)).toBe('error');
    await advance(SETTLE_MS);
    expect(state(container)).toBe('error');
    expect(container.textContent).toContain('socket closed');
    expect(container.textContent).not.toContain('⚠');
  });
});

describe('the last-value hint', () => {
  const NOW = Date.parse('2026-10-21T12:00:00Z');
  const sample = (value: number | null, at = '2026-10-18T12:00:00Z') =>
    ({ id: 's', deviceToken: 'd', eventType: 0, occurredTime: at, name: 'm', value, classifier: null }) as MeasurementSample;

  it('gives the value with its unit and how long ago', () => {
    expect(lastReadingHint(sample(21.456), { unit: '°C', precision: 1, now: NOW })).toBe('Last value 21.5 °C · 3 days ago');
  });

  it('gives nothing for no reading, a null value, or a time it cannot read', () => {
    expect(lastReadingHint(undefined)).toBeUndefined();
    expect(lastReadingHint(sample(null), { now: NOW })).toBeUndefined();
    expect(lastReadingHint(sample(1, 'not a time'), { now: NOW })).toBeUndefined();
  });
});
