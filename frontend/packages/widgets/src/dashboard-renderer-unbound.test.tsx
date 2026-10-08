// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// An alarm-count bound to a slot that has no binding must show an empty state, never the
// tenant's alarm total. Tested end to end through the REAL hub (only the SDK's network
// calls are faked), because the hazard lives in the hand-off between the two: an absent
// datasource is tenant-wide on the alarm channel, so a renderer that resolved "unbound"
// to `undefined` would produce a plausible, wrong number under a one-machine title.

import { DashboardHub, type DashboardDefinition, type WidgetInstance } from '@devicechain/dashboards';
import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const TENANT_TOTAL = 4242;

vi.mock('@devicechain/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/client')>();
  return {
    ...actual,
    subscribe: () => () => {},
    // Every alarm query answers with a tenant-sized total, so a leak shows as that number.
    gql: async () => ({ alarms: { results: [], pagination: { totalRecords: TENANT_TOTAL } } }),
  };
});

import { DashboardRenderer } from './dashboard-renderer';

afterEach(cleanup);

const alarmCount = (id: string, datasource: WidgetInstance['datasource']): WidgetInstance => ({
  id,
  type: 'alarm-count',
  layout: { base: { col: 0, colSpan: 4, row: 0, rowSpan: 2, z: 0 } },
  datasource,
  options: { title: id },
});

const board = (widgets: WidgetInstance[]): DashboardDefinition => ({
  schemaVersion: 1,
  title: 'Unbound',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: { machine: { type: 'device' } },
  widgets,
});

const resolver = { devicesForAnchor: async () => [], deviceExists: async () => true };

const flush = () => act(async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
});

describe('an alarm-count on an unbound slot', () => {
  it('renders an empty state, not the tenant total', async () => {
    const hub = new DashboardHub({ resolver });
    render(
      <DashboardRenderer
        definition={board([alarmCount('machine-alarms', { kind: 'slot', slot: 'machine', measurements: [] })])}
        hub={hub}
        seedHistory={false}
        bindings={{}}
      />,
    );
    await flush();

    expect(screen.queryByText(String(TENANT_TOTAL))).toBeNull();
    expect(screen.getByText('0')).toBeTruthy();
    hub.disposeAll();
  });

  it('the counterweight: a tile with no datasource does show the tenant total', async () => {
    // Proves the fake reaches the hub, so the test above cannot pass by never querying.
    const hub = new DashboardHub({ resolver });
    render(
      <DashboardRenderer definition={board([alarmCount('all-alarms', undefined)])} hub={hub} seedHistory={false} bindings={{}} />,
    );
    await flush();

    expect(screen.getByText(String(TENANT_TOTAL))).toBeTruthy();
    hub.disposeAll();
  });
});
