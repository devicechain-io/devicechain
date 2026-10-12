// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// /dash parses with the deployment's catalog and renders with the registry that parsed.
//
// The board is the SAME document the console's end-to-end test authors (the shared fixture
// from @devicechain/host-widgets), exported as a template -- slots with no default binding --
// so this is the viewing half of "authored in the console, viewed in /dash": an installed
// extension is drawn, not shown as a placeholder, and the host's manifest rebinds its slot.

import type {
  AlarmStreamSink,
  AlarmSubscription,
  CommandStreamSink,
  CommandSubscription,
  DatasourceSelector,
  LocationStreamSink,
  LocationSubscription,
  WidgetDataSource,
  WidgetStreamSink,
} from '@devicechain/dashboards';
import { UNKNOWN_WIDGET_TYPE } from '@devicechain/dashboards';
import { createHostWidgetRegistry } from '@devicechain/host-widgets';
import { ACME_TANK_WIDGET_JSON, acmeTankWidget } from '@devicechain/host-widgets/fixtures/acme-tank';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  seen: [] as Array<{ channel: string; datasource: DatasourceSelector | undefined }>,
  source: undefined as WidgetDataSource | undefined,
  registry: undefined as unknown,
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

// The deployment's composition, with the fixture package installed in it.
vi.mock('@devicechain/host-widgets', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/host-widgets')>();
  return { ...actual, hostWidgetRegistry: () => h.registry };
});
vi.mock('@devicechain/widgets', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useDashboardHub: () => (h.source ??= new RecordingSource()),
  useSlotCandidates: () => undefined,
}));
vi.mock('@devicechain/dashboards', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createDeviceResolver: () => ({}),
  createEntityLister: () => ({}),
}));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: () => Promise.reject(new Error('offline')),
}));

import './i18n/config';
import { View } from './App';
import { loadDashboard } from './load';

afterEach(() => {
  cleanup();
  h.seen.length = 0;
});

const box = { base: { col: 0, colSpan: 8, row: 0, rowSpan: 4, z: 1 } };

// The console's template export of the shared board: the slot is declared, with no default.
const template = (widgets: unknown[] = [{ ...ACME_TANK_WIDGET_JSON }]) =>
  JSON.stringify({ title: 'Plant', slots: { tank: { type: 'device' } }, widgets });

function load(definition: string, manifest: string) {
  h.registry = createHostWidgetRegistry([acmeTankWidget]);
  const result = loadDashboard(definition, manifest);
  if (!('loaded' in result)) throw new Error(`load failed: ${JSON.stringify(result.error)}`);
  return result.loaded;
}

async function view(definition: string, manifest: string) {
  render(<View loaded={load(definition, manifest)} authorities={[]} onChange={() => {}} onSignOut={() => {}} />);
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe('/dash with an installed extension package', () => {
  it('parses an installed extension as that widget and renders with the registry that parsed it', () => {
    const loaded = load(template(), '');
    expect(loaded.definition.widgets[0].type).toBe(ACME_TANK_WIDGET_JSON.type);
    expect(loaded.registry).toBe(h.registry);
  });

  it("checks a built-in's settings version, like every host", () => {
    const stale = { id: 'g', type: 'gauge', widget: { version: '1.0.0', optionsVersion: 9 }, layout: box, options: {} };
    const loaded = load(template([stale]), '');
    expect(loaded.definition.widgets[0].type).toBe(UNKNOWN_WIDGET_TYPE);
    expect(loaded.definition.widgets[0].reason?.code).toBe('options-version');
  });

  it("draws the extension (not a placeholder) and rebinds its slot from the host's manifest", async () => {
    await view(template(), '{"tank":{"kind":"device","deviceToken":"tank-77"}}');
    expect(screen.getByText('Main tank')).toBeTruthy();
    expect(screen.queryByText(/needs a newer viewer/)).toBeNull();
    expect(h.seen).toContainEqual({
      channel: 'measurement',
      datasource: { kind: 'device', deviceToken: 'tank-77', measurements: ['level'] },
    });
    cleanup();
    h.seen.length = 0;

    // Same document, another host manifest: the slot follows it.
    await view(template(), '{"tank":{"kind":"device","deviceToken":"tank-88"}}');
    expect(h.seen).toContainEqual({
      channel: 'measurement',
      datasource: { kind: 'device', deviceToken: 'tank-88', measurements: ['level'] },
    });
    expect(h.seen.some((s) => s.datasource?.kind === 'device' && s.datasource.deviceToken === 'tank-77')).toBe(false);
  });

  it('shows a labelled placeholder, and opens no stream, where the package is not installed', async () => {
    h.registry = createHostWidgetRegistry([]);
    const result = loadDashboard(template(), '{"tank":{"kind":"device","deviceToken":"tank-77"}}');
    if (!('loaded' in result)) throw new Error('load failed');
    render(<View loaded={result.loaded} authorities={[]} onChange={() => {}} onSignOut={() => {}} />);
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(screen.getByText(/needs a newer viewer/)).toBeTruthy();
    expect(h.seen).toEqual([]);
  });
});
