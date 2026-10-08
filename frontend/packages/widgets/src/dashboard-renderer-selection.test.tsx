// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A selection re-points ONE slot. These tests pin what that must cost on a board whose
// data source stays the same object: only the widgets bound to the re-pointed slot
// re-subscribe (on every channel), only they re-fetch history, and none of them shows the
// previous device's value while the new one is on its way.

import type {
  AlarmStreamSink,
  AlarmSubscription,
  CommandStreamSink,
  CommandSubscription,
  DashboardDefinition,
  DatasourceSelector,
  LocationStreamSink,
  LocationSubscription,
  MeasurementSample,
  SlotBinding,
  WidgetDataSource,
  WidgetInstance,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  // Each history fetch is held until the test settles it, so "the refetch is still
  // pending" is a state the test can stand in.
  fetches: [] as Array<{
    widgetId: string;
    datasource: unknown;
    resolve: (samples: MeasurementSample[]) => void;
  }>,
}));

vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  return {
    ...actual,
    fetchWidgetHistory: (widget: WidgetInstance) =>
      new Promise<MeasurementSample[]>((resolve) => {
        h.fetches.push({ widgetId: widget.id, datasource: widget.datasource, resolve });
      }),
  };
});

vi.mock('./echart', () => ({ EChart: () => <div data-testid="echart" /> }));

import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);

// ── A data source that records every subscription it is asked for ────────────────────

type Channel = 'measurement' | 'alarm' | 'command' | 'location';

interface Opened {
  channel: Channel;
  datasource: DatasourceSelector | undefined;
  disposed: boolean;
}

class RecordingSource implements WidgetDataSource {
  readonly opened: Opened[] = [];
  readonly availability: Array<DatasourceSelector | undefined> = [];

  private open(channel: Channel, datasource: DatasourceSelector | undefined): () => void {
    const entry: Opened = { channel, datasource, disposed: false };
    this.opened.push(entry);
    return () => {
      entry.disposed = true;
    };
  }

  subscribeWidget(datasource: DatasourceSelector, _sink: WidgetStreamSink) {
    return this.open('measurement', datasource);
  }
  subscribeAlarms(sub: AlarmSubscription, sink: AlarmStreamSink) {
    sink.next({ alarms: [], total: 0 });
    return this.open('alarm', sub.datasource);
  }
  subscribeCommands(sub: CommandSubscription, sink: CommandStreamSink) {
    sink.next({ deviceToken: null, commands: [], total: 0 });
    return this.open('command', sub.datasource);
  }
  subscribeLocations(sub: LocationSubscription, sink: LocationStreamSink) {
    sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
    return this.open('location', sub.datasource);
  }
  async isDatasourceAvailable(datasource: DatasourceSelector | undefined) {
    this.availability.push(datasource);
    return true;
  }

  // The live subscriptions on a channel whose concrete selector names `token`.
  live(channel: Channel, token: string): Opened[] {
    return this.opened.filter((o) => o.channel === channel && !o.disposed && tokenOf(o.datasource) === token);
  }
  // Every subscription ever opened on a channel for `token` (live or not).
  ever(channel: Channel, token: string): Opened[] {
    return this.opened.filter((o) => o.channel === channel && tokenOf(o.datasource) === token);
  }
}

function tokenOf(ds: DatasourceSelector | undefined): string | undefined {
  if (!ds) return undefined;
  if (ds.kind === 'device') return ds.deviceToken;
  if (ds.kind === 'slot') return `slot:${ds.slot}`;
  if (ds.kind === 'unbound') return 'unbound';
  return undefined;
}

// ── The board: one widget per channel on the re-pointed slot, and one on a slot that stays ─

const box = (row: number) => ({ base: { col: 0, colSpan: 4, row, rowSpan: 2, z: 0 } });

function widgetsFor(slot: string, prefix: string, row: number): WidgetInstance[] {
  return [
    {
      id: `${prefix}-card`,
      type: 'latest-card',
      layout: box(row),
      datasource: { kind: 'slot', slot, measurements: ['fuel'] },
      options: { title: `${prefix} fuel` },
    },
    {
      id: `${prefix}-alarms`,
      type: 'alarm-count',
      layout: box(row + 2),
      datasource: { kind: 'slot', slot, measurements: [] },
    },
    {
      id: `${prefix}-cmd`,
      type: 'command-button',
      layout: box(row + 4),
      datasource: { kind: 'slot', slot, measurements: [] },
    },
    {
      id: `${prefix}-map`,
      type: 'map',
      layout: box(row + 6),
      datasource: { kind: 'slot', slot, measurements: [], location: { series: 'latest' } },
    },
  ];
}

const definition: DashboardDefinition = {
  schemaVersion: 1,
  title: 'Selection',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: {
    machine: { type: 'device' },
    plant: { type: 'device' },
  },
  widgets: [...widgetsFor('machine', 'm', 0), ...widgetsFor('plant', 'p', 8)],
};

const dev = (deviceToken: string): SlotBinding => ({ kind: 'device', deviceToken });

const flush = () => act(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
});

const sample = (deviceToken: string, value: number): MeasurementSample => ({
  id: `${deviceToken}-fuel`,
  deviceToken,
  eventType: 0,
  occurredTime: null,
  name: 'fuel',
  value,
  classifier: null,
});

beforeEach(() => {
  h.fetches.length = 0;
});

const CHANNELS: Channel[] = ['measurement', 'alarm', 'command', 'location'];

describe('a selection on a board whose data source stays the same', () => {
  it('re-subscribes only the widgets bound to the re-pointed slot, on every channel', async () => {
    const source = new RecordingSource();
    const { rerender } = render(
      <DashboardRenderer
        definition={definition}
        hub={source}
        seedHistory={false}
        bindings={{ machine: dev('hauler-1'), plant: dev('plant-1') }}
      />,
    );
    await flush();

    for (const channel of CHANNELS) {
      expect(source.live(channel, 'hauler-1'), channel).toHaveLength(1);
      expect(source.live(channel, 'plant-1'), channel).toHaveLength(1);
    }

    rerender(
      <DashboardRenderer
        definition={definition}
        hub={source}
        seedHistory={false}
        bindings={{ machine: dev('hauler-2'), plant: dev('plant-1') }}
      />,
    );
    await flush();

    for (const channel of CHANNELS) {
      // The re-pointed widget follows the selection: the old device is released and the
      // new one subscribed, exactly once.
      expect(source.live(channel, 'hauler-1'), `${channel}: old device released`).toHaveLength(0);
      expect(source.ever(channel, 'hauler-2'), `${channel}: new device subscribed once`).toHaveLength(1);
      expect(source.live(channel, 'hauler-2'), channel).toHaveLength(1);
      // The untouched slot's widget is not re-subscribed at all.
      expect(source.ever(channel, 'plant-1'), `${channel}: untouched slot kept`).toHaveLength(1);
      expect(source.live(channel, 'plant-1'), channel).toHaveLength(1);
    }

    // Availability: the re-pointed widgets ask about the new device; the untouched slot's
    // four widgets asked once each, at mount, and never again.
    const asked = (token: string) => source.availability.filter((ds) => tokenOf(ds) === token).length;
    expect(asked('plant-1')).toBe(4);
    expect(asked('hauler-2')).toBe(4);
  });

  it('hands the data source an explicit unbound selector for a slot with no binding', async () => {
    const source = new RecordingSource();
    render(
      <DashboardRenderer definition={definition} hub={source} seedHistory={false} bindings={{ plant: dev('plant-1') }} />,
    );
    await flush();

    // 🔴 Never `undefined`: an absent datasource is tenant-wide on the alarm channel.
    const machineAlarms = source.opened.find((o) => o.channel === 'alarm' && tokenOf(o.datasource) !== 'plant-1');
    expect(machineAlarms?.datasource).toEqual({ kind: 'unbound', measurements: [] });
    for (const channel of CHANNELS) {
      expect(source.opened.filter((o) => o.channel === channel && o.datasource === undefined), channel).toHaveLength(0);
    }
  });

  it('fetches history again only for the widget whose selector changed', async () => {
    const source = new RecordingSource();
    const { rerender } = render(
      <DashboardRenderer
        definition={definition}
        hub={source}
        bindings={{ machine: dev('hauler-1'), plant: dev('plant-1') }}
      />,
    );
    await flush();

    // Only the two measurement widgets are seeded.
    expect(h.fetches.map((f) => f.widgetId).sort()).toEqual(['m-card', 'p-card']);
    await act(async () => {
      for (const f of h.fetches) f.resolve([]);
    });

    rerender(
      <DashboardRenderer
        definition={definition}
        hub={source}
        bindings={{ machine: dev('hauler-2'), plant: dev('plant-1') }}
      />,
    );
    await flush();

    expect(h.fetches.slice(2).map((f) => [f.widgetId, f.datasource])).toEqual([
      ['m-card', { kind: 'device', deviceToken: 'hauler-2', measurements: ['fuel'] }],
    ]);
  });

  it("never shows the previous device's seeded value while the new device's history is pending", async () => {
    const source = new RecordingSource();
    const { rerender } = render(
      <DashboardRenderer
        definition={definition}
        hub={source}
        bindings={{ machine: dev('hauler-1'), plant: dev('plant-1') }}
      />,
    );
    await flush();

    await act(async () => {
      for (const f of h.fetches) f.resolve(f.widgetId === 'm-card' ? [sample('hauler-1', 71)] : [sample('plant-1', 33)]);
    });
    expect(screen.getByText('71')).toBeTruthy();
    expect(screen.getByText('33')).toBeTruthy();

    rerender(
      <DashboardRenderer
        definition={definition}
        hub={source}
        bindings={{ machine: dev('hauler-2'), plant: dev('plant-1') }}
      />,
    );
    // hauler-2's history is held pending: the card must not keep hauler-1's 71.
    expect(screen.queryByText('71')).toBeNull();
    await flush();
    expect(screen.queryByText('71')).toBeNull();
    // The untouched slot keeps its seed — dropping it would blank a widget that did not move.
    expect(screen.getByText('33')).toBeTruthy();

    // When hauler-2's history lands, it shows.
    await act(async () => {
      h.fetches[h.fetches.length - 1]?.resolve([sample('hauler-2', 12)]);
    });
    expect(screen.getByText('12')).toBeTruthy();
  });

  it('ignores a slow history answer for a device the widget has already moved away from', async () => {
    const source = new RecordingSource();
    const { rerender } = render(
      <DashboardRenderer definition={definition} hub={source} bindings={{ machine: dev('hauler-1') }} />,
    );
    await flush();
    const first = h.fetches.find((f) => f.widgetId === 'm-card');

    rerender(<DashboardRenderer definition={definition} hub={source} bindings={{ machine: dev('hauler-2') }} />);
    await flush();

    // hauler-1's history arrives late, after the selection moved on.
    await act(async () => {
      first?.resolve([sample('hauler-1', 71)]);
    });
    expect(screen.queryByText('71')).toBeNull();
  });
});
