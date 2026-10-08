// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The edit canvas renders each widget live, and the data source it hands them holds no
// bindings: a widget's `slot` selector must be resolved through the canvas's manifest
// before the widget subscribes. These tests pin that the data source only ever sees a
// concrete selector — the bound device, or an explicit `unbound` — and never a raw slot.

import '@/i18n/config';
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
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

import { DashboardCanvas } from './DashboardCanvas';

afterEach(cleanup);

// jsdom has no ResizeObserver; the canvas measures its width with one.
beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
});

class RecordingSource implements WidgetDataSource {
  readonly seen: Array<{ channel: string; datasource: DatasourceSelector | undefined }> = [];
  subscribeWidget(datasource: DatasourceSelector, _sink: WidgetStreamSink) {
    this.seen.push({ channel: 'measurement', datasource });
    return () => {};
  }
  subscribeAlarms(sub: AlarmSubscription, sink: AlarmStreamSink) {
    this.seen.push({ channel: 'alarm', datasource: sub.datasource });
    sink.next({ alarms: [], total: 0 });
    return () => {};
  }
  subscribeCommands(sub: CommandSubscription, sink: CommandStreamSink) {
    this.seen.push({ channel: 'command', datasource: sub.datasource });
    sink.next({ deviceToken: null, commands: [], total: 0 });
    return () => {};
  }
  subscribeLocations(sub: LocationSubscription, sink: LocationStreamSink) {
    this.seen.push({ channel: 'location', datasource: sub.datasource });
    sink.next({ kind: 'positions', deviceTokens: [], locations: [] });
    return () => {};
  }
  async isDatasourceAvailable() {
    return true;
  }
}

const box = (row: number) => ({ base: { col: 0, colSpan: 4, row, rowSpan: 2, z: 0 } });

const definition: DashboardDefinition = {
  schemaVersion: 1,
  title: 'Canvas',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device' }, spare: { type: 'device' } },
  widgets: [
    {
      id: 'bound-card',
      type: 'latest-card',
      layout: box(0),
      datasource: { kind: 'slot', slot: 'machine', measurements: ['fuel'] },
    },
    {
      id: 'bound-alarms',
      type: 'alarm-count',
      layout: box(2),
      datasource: { kind: 'slot', slot: 'machine', measurements: [] },
    },
    {
      id: 'unbound-card',
      type: 'latest-card',
      layout: box(4),
      datasource: { kind: 'slot', slot: 'spare', measurements: ['fuel'] },
    },
  ],
};

describe('the edit canvas', () => {
  it('hands its data source each widget with its slot resolved, never a raw slot', async () => {
    const source = new RecordingSource();
    render(
      <DashboardCanvas
        definition={definition}
        onChange={vi.fn()}
        hub={source}
        bindings={{ machine: { kind: 'device', deviceToken: 'hauler-1' } }}
        selectedId={null}
        onSelect={vi.fn()}
      />,
    );
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    // Positive control: the widgets did subscribe, so the absence check is not vacuous.
    expect(source.seen.length).toBeGreaterThanOrEqual(3);
    expect(source.seen.filter((s) => s.datasource?.kind === 'slot')).toEqual([]);

    expect(source.seen).toContainEqual({
      channel: 'measurement',
      datasource: { kind: 'device', deviceToken: 'hauler-1', measurements: ['fuel'] },
    });
    expect(source.seen).toContainEqual({
      channel: 'alarm',
      datasource: { kind: 'device', deviceToken: 'hauler-1', measurements: [] },
    });
    // A slot with no binding reaches the source as an explicit unbound selector.
    expect(source.seen).toContainEqual({
      channel: 'measurement',
      datasource: { kind: 'unbound', measurements: ['fuel'] },
    });
  });
});
