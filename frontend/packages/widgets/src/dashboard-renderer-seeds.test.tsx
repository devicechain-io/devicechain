// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What a measurement widget is HANDED as its history seed, render by render. The widget
// itself is replaced by a recorder, so every render the renderer commits is observed —
// including the one between a selection's commit and the effects that follow it, which a
// DOM assertion after `rerender` never sees (rerender runs inside act, which flushes
// effects before it returns).

import type {
  DashboardDefinition,
  DatasourceSelector,
  MeasurementSample,
  SlotBinding,
  WidgetDataSource,
  WidgetInstance,
} from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  // Each history fetch is held until the test settles it.
  fetches: [] as Array<{
    widgetId: string;
    deviceToken: string | undefined;
    resolve: (samples: MeasurementSample[]) => void;
  }>,
  // Every render of every widget: the device its selector names, and the devices its
  // seed samples came from (undefined = no seed handed over).
  renders: [] as Array<{ widgetId: string; device: string | undefined; seeded: string[] | undefined }>,
}));

vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  return {
    ...actual,
    fetchWidgetHistory: (widget: WidgetInstance) =>
      new Promise<MeasurementSample[]>((resolve) => {
        const ds = widget.datasource as DatasourceSelector | undefined;
        h.fetches.push({
          widgetId: widget.id,
          deviceToken: ds?.kind === 'device' ? ds.deviceToken : undefined,
          resolve,
        });
      }),
  };
});

vi.mock('./connected-widget', () => ({
  ConnectedWidget: ({ widget, initialSamples }: { widget: WidgetInstance; initialSamples?: MeasurementSample[] }) => {
    const ds = widget.datasource as DatasourceSelector | undefined;
    h.renders.push({
      widgetId: widget.id,
      device: ds?.kind === 'device' ? ds.deviceToken : undefined,
      seeded: initialSamples?.map((s) => s.deviceToken),
    });
    return null;
  },
}));

import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);
beforeEach(() => {
  h.fetches.length = 0;
  h.renders.length = 0;
});

// Never subscribed to: the widget is a recorder.
const source: WidgetDataSource = {
  subscribeWidget: () => () => {},
  subscribeAlarms: () => () => {},
  subscribeCommands: () => () => {},
  subscribeLocations: () => () => {},
  isDatasourceAvailable: async () => true,
};

const definition: DashboardDefinition = {
  schemaVersion: 1,
  title: 'Seeds',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device' } },
  widgets: [
    {
      id: 'card',
      type: 'latest-card',
      layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 2, z: 0 } },
      datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] },
    },
  ],
};

const dev = (deviceToken: string): Record<string, SlotBinding> => ({ machine: { kind: 'device', deviceToken } });

const sample = (deviceToken: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-fuel-${value}`,
  deviceToken,
  eventType: 0,
  occurredTime: null,
  name: 'fuel',
  value,
  classifier: null,
});

const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

const lastRender = () => {
  const card = h.renders.filter((r) => r.widgetId === 'card');
  return card[card.length - 1];
};
const fetchFor = (device: string) => h.fetches.find((f) => f.widgetId === 'card' && f.deviceToken === device);

describe('the history seed a measurement widget is handed', () => {
  it('is never, on any render, a seed fetched for a device other than the one the widget names', async () => {
    const { rerender } = render(<DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-1')} />);
    await flush();
    await act(async () => {
      fetchFor('hauler-1')?.resolve([sample('hauler-1', 71)]);
    });
    // Positive control: the seed did reach the widget, so the check below is not vacuous.
    expect(lastRender()).toEqual({ widgetId: 'card', device: 'hauler-1', seeded: ['hauler-1'] });

    const before = h.renders.length;
    rerender(<DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-2')} />);
    await flush();

    const after = h.renders.slice(before).filter((r) => r.widgetId === 'card');
    expect(after.length).toBeGreaterThan(0);
    for (const r of after) {
      expect(r.device).toBe('hauler-2');
      // Every render after the selection: no seed yet, or a seed of hauler-2 only.
      expect(r.seeded === undefined || r.seeded.every((d) => d === 'hauler-2'), JSON.stringify(r)).toBe(true);
    }
  });

  it("keeps the new device's history when the previous device's answer lands after it", async () => {
    const { rerender } = render(<DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-1')} />);
    await flush();
    const first = fetchFor('hauler-1');
    expect(first).toBeDefined();

    // hauler-1 is still pending when the selection moves to hauler-2, and hauler-2 lands first.
    rerender(<DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-2')} />);
    await flush();
    await act(async () => {
      fetchFor('hauler-2')?.resolve([sample('hauler-2', 12)]);
    });
    expect(lastRender()?.seeded).toEqual(['hauler-2']);

    // Then hauler-1's answer arrives late. It must not displace hauler-2's seed: nothing
    // would fetch hauler-2 again, so the widget would stay without history.
    await act(async () => {
      first?.resolve([sample('hauler-1', 71)]);
    });
    await flush();
    expect(lastRender()).toEqual({ widgetId: 'card', device: 'hauler-2', seeded: ['hauler-2'] });
  });

  it('fetches history again after a round trip through preview', async () => {
    const { rerender } = render(<DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-1')} />);
    await flush();
    await act(async () => {
      fetchFor('hauler-1')?.resolve([sample('hauler-1', 71)]);
    });
    expect(lastRender()?.seeded).toEqual(['hauler-1']);

    rerender(
      <DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-1')} seedHistory={false} />,
    );
    await flush();
    expect(lastRender()?.seeded).toBeUndefined();

    rerender(<DashboardRenderer definition={definition} hub={source} bindings={dev('hauler-1')} />);
    await flush();
    const refetches = h.fetches.filter((f) => f.widgetId === 'card' && f.deviceToken === 'hauler-1');
    expect(refetches).toHaveLength(2);
    await act(async () => {
      refetches[1]?.resolve([sample('hauler-1', 72)]);
    });
    expect(lastRender()?.seeded).toEqual(['hauler-1']);
  });
});
