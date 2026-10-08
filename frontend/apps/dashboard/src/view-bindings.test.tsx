// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's data source holds no bindings: every widget's slot is resolved by the
// renderer, through the manifest the viewer hands it. Leave that manifest out and every
// slot-bound widget on every embedded board resolves to "unbound" and renders empty —
// no build, typecheck or other test in this app notices. So this mounts the view with a
// recording data source and asserts the widgets subscribe to the devices their slots
// are bound to.

import type {
  AlarmStreamSink,
  AlarmSubscription,
  CommandStreamSink,
  CommandSubscription,
  DashboardDefinition,
  DatasourceSelector,
  LocationStreamSink,
  LocationSubscription,
  WidgetDataSource,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { act, cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  seen: [] as Array<{ channel: string; datasource: DatasourceSelector | undefined }>,
  source: undefined as WidgetDataSource | undefined,
}));

class RecordingSource implements WidgetDataSource {
  subscribeWidget(datasource: DatasourceSelector, _sink: WidgetStreamSink) {
    h.seen.push({ channel: 'measurement', datasource });
    return () => {};
  }
  subscribeAlarms(sub: AlarmSubscription, sink: AlarmStreamSink) {
    h.seen.push({ channel: 'alarm', datasource: sub.datasource });
    sink.next({ alarms: [], total: 0 });
    return () => {};
  }
  subscribeCommands(_sub: CommandSubscription, sink: CommandStreamSink) {
    sink.next({ deviceToken: null, commands: [], total: 0 });
    return () => {};
  }
  subscribeLocations(_sub: LocationSubscription, sink: LocationStreamSink) {
    sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
    return () => {};
  }
  async isDatasourceAvailable() {
    return true;
  }
}

vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  // One stable source, made on first use (the class is defined below the hoisted mock).
  useDashboardHub: () => (h.source ??= new RecordingSource()),
  useSlotCandidates: () => undefined,
}));
vi.mock('@devicechain/dashboards', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  // A scope-free board never asks either of these anything; they only must not dial out.
  createDeviceResolver: () => ({}),
  createEntityLister: () => ({}),
}));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  // The tenant basemap lookup: decoration, and it degrades to none.
  gql: () => Promise.reject(new Error('offline')),
}));

import './i18n/config';
import { View } from './App';

afterEach(() => {
  cleanup();
  h.seen.length = 0;
});

const box = (row: number) => ({ base: { col: 0, colSpan: 4, row, rowSpan: 2, z: 0 } });

const definition: DashboardDefinition = {
  schemaVersion: 1,
  title: 'Embedded',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: {
    machine: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'hauler-1' } },
    plant: { type: 'device', defaultBinding: { kind: 'device', deviceToken: 'plant-1' } },
  },
  widgets: [
    { id: 'card', type: 'latest-card', layout: box(0), datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] } },
    { id: 'alarms', type: 'alarm-count', layout: box(2), datasource: { kind: 'slot', slot: 'plant', measurements: [] } },
  ],
};

async function mount(manifest: Record<string, { kind: 'device'; deviceToken: string }>) {
  render(<View loaded={{ definition, manifest }} authorities={[]} onChange={() => {}} onSignOut={() => {}} />);
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe('the /dash view', () => {
  it("subscribes each slot-bound widget to its slot's default device", async () => {
    await mount({});
    expect(h.seen).toContainEqual({
      channel: 'measurement',
      datasource: { kind: 'device', deviceToken: 'hauler-1', measurements: ['fuel'] },
    });
    expect(h.seen).toContainEqual({
      channel: 'alarm',
      datasource: { kind: 'device', deviceToken: 'plant-1', measurements: [] },
    });
    expect(h.seen.filter((s) => s.datasource?.kind !== 'device')).toEqual([]);
  });

  it("lets the pasted manifest override a slot's default", async () => {
    await mount({ machine: { kind: 'device', deviceToken: 'hauler-9' } });
    expect(h.seen).toContainEqual({
      channel: 'measurement',
      datasource: { kind: 'device', deviceToken: 'hauler-9', measurements: ['fuel'] },
    });
    expect(h.seen.some((s) => s.datasource?.kind === 'device' && s.datasource.deviceToken === 'hauler-1')).toBe(false);
  });
});
