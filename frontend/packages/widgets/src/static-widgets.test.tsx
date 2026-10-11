// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The static widgets (label, image) sit on the `none` channel: they open no data channel at
// all. Rendered through the REAL renderer against a source that records every call, so a
// regression to "static widget quietly subscribes" is a failing count, not a silent leak.

import type { DashboardDefinition, WidgetDataSource } from '@devicechain/dashboards';
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ historyFetches: [] as string[] }));

// History backfill is the one other thing a widget could trigger; record it.
vi.mock('@devicechain/dashboards', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/dashboards')>();
  return {
    ...actual,
    fetchWidgetHistory: (widget: { id: string }) => {
      h.historyFetches.push(widget.id);
      return Promise.resolve([]);
    },
  };
});

import { DashboardRenderer } from './dashboard-renderer';

afterEach(() => {
  cleanup();
  h.historyFetches.length = 0;
});

function recordingSource() {
  const calls: string[] = [];
  const source: WidgetDataSource = {
    subscribeWidget: () => (calls.push('measurement'), () => {}),
    subscribeAlarms: () => (calls.push('alarm'), () => {}),
    subscribeCommands: () => (calls.push('command'), () => {}),
    subscribeLocations: () => (calls.push('location'), () => {}),
    isDatasourceAvailable: async () => (calls.push('availability'), true),
  };
  return { source, calls };
}

const layout = (col: number) => ({ base: { col, colSpan: 4, row: 0, rowSpan: 2, z: 0 } });

const staticOnly: DashboardDefinition = {
  schemaVersion: 1,
  title: 'Static',
  canvas: { grid: { columns: 12, gap: 8, rowHeight: 40 }, sizing: 'fill', breakpoints: { base: 0 } },
  slots: {},
  widgets: [
    { id: 'note', type: 'label', layout: layout(0), options: { text: 'Zone A' } },
    { id: 'logo', type: 'image', layout: layout(4), options: { url: 'http://x/y.png', alt: 'plan' } },
  ],
};

describe('static widgets on the none channel', () => {
  it('render their content and open zero hub subscriptions or availability queries', async () => {
    const { source, calls } = recordingSource();
    render(<DashboardRenderer definition={staticOnly} hub={source} />);

    // Positive control: both rendered, so the zero count below is not an empty board.
    expect(screen.getByText('Zone A')).toBeTruthy();
    expect(screen.getByAltText('plan')).toBeTruthy();

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(calls).toEqual([]);
    expect(h.historyFetches).toEqual([]);
  });

  // A board authored while label/image sat on the measurement channel can still carry a
  // datasource on them, and the parser keeps it. The channel, not the stored JSON, decides:
  // a `none` widget reads nothing even when handed a selector, so it can neither subscribe
  // nor be replaced by an "unavailable" pane for a device it never displayed.
  it('read nothing even when the stored widget still carries a datasource', async () => {
    const { source, calls } = recordingSource();
    const ds = { kind: 'device' as const, deviceToken: 'gone', measurements: ['t'] };
    const definition: DashboardDefinition = {
      ...staticOnly,
      widgets: [
        { id: 'note', type: 'label', layout: layout(0), datasource: ds, options: { text: 'Zone A' } },
        { id: 'logo', type: 'image', layout: layout(4), datasource: ds, options: { url: 'http://x/y.png', alt: 'plan' } },
      ],
    };
    source.isDatasourceAvailable = async () => (calls.push('availability'), false);
    render(<DashboardRenderer definition={definition} hub={source} />);

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(calls).toEqual([]);
    expect(h.historyFetches).toEqual([]);
    expect(screen.getByText('Zone A')).toBeTruthy();
    expect(screen.getByAltText('plan')).toBeTruthy();
  });

  it('control: a measurement widget on the same board DOES subscribe', async () => {
    const { source, calls } = recordingSource();
    const definition: DashboardDefinition = {
      ...staticOnly,
      widgets: [
        ...staticOnly.widgets,
        {
          id: 'card',
          type: 'latest-card',
          layout: layout(8),
          datasource: { kind: 'device', deviceToken: 'd1', measurements: ['t'] },
        },
      ],
    };
    render(<DashboardRenderer definition={definition} hub={source} />);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(calls).toContain('measurement');
    expect(h.historyFetches).toEqual(['card']);
  });
});
