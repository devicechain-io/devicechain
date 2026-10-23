// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Why a single-device widget has nothing to show, said through the connected widget: the slot is
// unbound, the device's profile does not declare the measurement, or it does and nothing arrived.

import type { DashboardHub, DatasourceSelector, MeasurementSample, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ConnectedWidget } from './connected-widget';
import { ASK_DELAY_MS, emptyKindFor, lastReadingHint } from './no-reading';
import { loadLevelWidgets } from './widgets/level-lazy';

beforeEach(() => {
  document.documentElement.lang = 'en';
});
afterEach(cleanup);

const flush = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
// Past the moment an empty widget waits before it asks what the device declares.
const asked = () => act(async () => void (await new Promise((r) => setTimeout(r, ASK_DELAY_MS + 60))));

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
});

describe('through the connected widget', () => {
  it('a card for a measurement the device profile does not declare says it will never report it', async () => {
    const f = hubOf(['temperature']);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    expect(f.declaredMetricsForDevice).toHaveBeenCalledWith('d-1');
    expect(state(container)).toBe('not-reported');
    expect(container.textContent).toContain('"fuel_level"');
    expect(container.textContent).not.toContain('—');
  });

  it('a declared measurement with nothing in range says no data in range', async () => {
    const f = hubOf(['fuel_level']);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    expect(state(container)).toBe('no-data');
  });

  it('a host that cannot say what is declared never claims the device does not report it', async () => {
    const f = hubOf('absent');
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    expect(state(container)).toBe('no-data');
  });

  it('a slot with no device bound says so, and asks the source nothing', async () => {
    const f = hubOf(['temperature']);
    const unbound = { kind: 'unbound', measurements: ['fuel_level'] } as DatasourceSelector;
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', unbound)} hub={f.hub} />);
    await flush();
    await asked();
    expect(state(container)).toBe('unbound');
    expect(f.declaredMetricsForDevice).not.toHaveBeenCalled();
  });

  it('a widget with a reading asks nothing and draws no empty state', async () => {
    const sample = { id: 's', deviceToken: 'd-1', eventType: 0, occurredTime: new Date().toISOString(), name: 'fuel_level', value: 42, classifier: null };
    const f = hubOf(['temperature'], [sample]);
    const { container } = render(<ConnectedWidget widget={widgetOf('latest-card', device())} hub={f.hub} />);
    await flush();
    await asked();
    expect(state(container)).toBeUndefined();
    expect(f.declaredMetricsForDevice).not.toHaveBeenCalled();
  });

  it('the level widgets explain themselves the same way', async () => {
    await loadLevelWidgets();
    for (const type of ['battery', 'progress-ring', 'level-gauge']) {
      const f = hubOf(['temperature']);
      const { container, unmount } = render(<ConnectedWidget widget={widgetOf(type, device(), { min: 0, max: 100 })} hub={f.hub} />);
      await flush();
      await asked();
      await vi.waitFor(() => expect(state(container), type).toBe('not-reported'), { timeout: 4000 });
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
