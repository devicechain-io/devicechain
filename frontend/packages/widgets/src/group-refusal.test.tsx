// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A widget that shows ONE device's readings keyed by measurement name, bound to a group of devices.
// Read as one stream, every device reporting "temperature" would overwrite the others and the widget
// would show whichever spoke last as THE temperature: a wrong number that looks right. Each such
// widget must refuse the group in words and read nothing, whatever brought the group to it (the
// editor no longer offers one; a host binding or an older definition still can).

import type { DashboardHub, DatasourceSelector, WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { builtinWidgetDescriptors } from './builtin-descriptors';
import { ConnectedWidget } from './connected-widget';
import { loadAggregateWidgets } from './widgets/aggregate-lazy';

const GROUP: DatasourceSelector = {
  kind: 'anchor',
  anchor: { relationship: 'assigned-to', targetType: 'area', targetToken: 'plant' },
  measurements: ['temperature'],
};
const ONE: DatasourceSelector = { kind: 'device', deviceToken: 'd-1', measurements: ['temperature'] };

const widget = (type: string, datasource: DatasourceSelector, options: Record<string, unknown> = {}): WidgetInstance =>
  ({
    id: 'w',
    type,
    layout: { base: { mode: 'grid', col: 0, row: 0, cols: 6, rows: 4 } },
    datasource,
    options: { title: 'T', measurement: 'temperature', ...options },
  }) as unknown as WidgetInstance;

function hub() {
  const reads = {
    subscribeWidget: vi.fn(() => () => {}),
    subscribeMembers: vi.fn(() => () => {}),
    bucketMeasurements: vi.fn(async () => []),
    readMemberSeeds: vi.fn(async () => {}),
  };
  return { reads, hub: { ...reads, isDatasourceAvailable: async () => true } as unknown as DashboardHub };
}

// The widgets and the refusal arrive as chunks: load them once up front, so each case below settles
// in a few ticks and a regression fails on an assertion rather than on a timeout.
beforeAll(async () => {
  await loadAggregateWidgets();
  await import('./fleet-feed');
}, 30_000);

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 50))));
afterEach(cleanup);

// Every built-in that reads measurements by name or aggregates them: the ones that must refuse.
const ONE_DEVICE_WIDGETS = Object.values(builtinWidgetDescriptors)
  // (the presence widgets read device-state, and have their own cases in presence.test.tsx)
  .filter((d) => (d.channel === 'measurement' && !('members' in d.binding) && !('presence' in d.binding)) || d.channel === 'aggregate')
  .map((d) => d.id);

describe('a widget that shows one device, bound to a group', () => {
  it('covers the widgets the brief names, so an empty list cannot pass for a clean one', () => {
    expect(ONE_DEVICE_WIDGETS).toEqual(expect.arrayContaining(['latest-card', 'gauge', 'table', 'pie-chart', 'stat-card']));
  });

  it.each(ONE_DEVICE_WIDGETS)('%s refuses it in words and reads nothing', async (type) => {
    const f = hub();
    render(<ConnectedWidget widget={widget(type, GROUP)} hub={f.hub} />);
    await settle();
    expect(f.reads.subscribeWidget).not.toHaveBeenCalled();
    expect(f.reads.subscribeMembers).not.toHaveBeenCalled();
    expect(f.reads.bucketMeasurements).not.toHaveBeenCalled();
    expect(await screen.findByText(/bound to a group of devices/)).toBeTruthy();
    expect(document.body.textContent).toContain('show the group in a fleet table');
  });

  it.each(ONE_DEVICE_WIDGETS)('%s bound to one device reads it as before', async (type) => {
    const f = hub();
    render(<ConnectedWidget widget={widget(type, ONE)} hub={f.hub} />);
    await vi.waitFor(() => expect(f.reads.subscribeWidget.mock.calls.length + f.reads.bucketMeasurements.mock.calls.length).toBeGreaterThan(0), { timeout: 20_000 });
    expect(document.body.textContent).not.toContain('bound to a group of devices');
    // The test's own budget has to outlast the wait inside it, or the 20 s above is a 5 s.
  }, 30_000);

  it('points a bar chart at its own per-device mode, and lets that mode read the group', async () => {
    const f = hub();
    render(<ConnectedWidget widget={widget('bar-chart', GROUP)} hub={f.hub} />);
    expect(await screen.findByText(/one bar per device/, {}, { timeout: 20_000 })).toBeTruthy();
    expect(f.reads.subscribeWidget).not.toHaveBeenCalled();
    cleanup();

    render(<ConnectedWidget widget={widget('bar-chart', GROUP, { groupBy: 'device' })} hub={f.hub} />);
    await vi.waitFor(() => expect(f.reads.subscribeMembers).toHaveBeenCalledTimes(1), { timeout: 20_000 });
    expect(document.body.textContent).not.toContain('bound to a group of devices');
  }, 30_000);

  it('points a time-series chart at its own one-line-per-device mode, and lets that mode read the group', async () => {
    const f = hub();
    render(<ConnectedWidget widget={widget('timeseries-chart', GROUP)} hub={f.hub} />);
    expect(await screen.findByText(/one line per device/, {}, { timeout: 20_000 })).toBeTruthy();
    expect(f.reads.subscribeWidget).not.toHaveBeenCalled();
    expect(f.reads.subscribeMembers).not.toHaveBeenCalled();
    cleanup();

    render(<ConnectedWidget widget={widget('timeseries-chart', GROUP, { groupBy: 'device' })} hub={f.hub} />);
    await vi.waitFor(() => expect(f.reads.subscribeMembers).toHaveBeenCalledTimes(1), { timeout: 20_000 });
    expect(document.body.textContent).not.toContain('bound to a group of devices');
  }, 30_000);

  it('a time-series chart bound to one device reads it as before', async () => {
    const f = hub();
    render(<ConnectedWidget widget={widget('timeseries-chart', ONE)} hub={f.hub} />);
    await vi.waitFor(() => expect(f.reads.subscribeWidget).toHaveBeenCalled(), { timeout: 20_000 });
    expect(f.reads.subscribeMembers).not.toHaveBeenCalled();
  }, 30_000);

  it('is offered no group in the editor: only the fleet widgets and the alarm and map widgets take one', () => {
    const takesGroup = Object.values(builtinWidgetDescriptors)
      .filter((d) => d.datasource.mode !== 'none' && (d.datasource.selectors as readonly string[]).includes('anchor'))
      .map((d) => d.id)
      .sort();
    expect(takesGroup).toEqual(['alarm-count', 'alarm-severity', 'alarm-table', 'bar-chart', 'fleet-count', 'fleet-table', 'map', 'online-doughnut', 'timeseries-chart']);
  });
});
