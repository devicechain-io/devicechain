// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console parses a stored dashboard WITH the deployment's catalog, and hands the
// workspace the same registry it parsed with. The page is the production entry point (the
// route), so this runs the real page over a faked API and a recorder in place of the
// workspace, and asks what the workspace was given.
//
// Before the catalog was threaded here, every installed extension widget came back as a
// placeholder and a built-in's settings version was never checked.

import '@/i18n/config';
import { UNKNOWN_WIDGET_TYPE, type DashboardDefinition } from '@devicechain/dashboards';
import { createHostWidgetRegistry } from '@devicechain/host-widgets';
import { ACME_TANK_WIDGET_JSON, acmeTankWidget } from '@devicechain/host-widgets/fixtures/acme-tank';
import { cleanup, render, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  definition: '',
  workspace: undefined as { loaded: DashboardDefinition; registry: unknown } | undefined,
  registry: undefined as unknown,
}));

// The deployment's composition, with the fixture package installed in it.
vi.mock('@devicechain/host-widgets', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@devicechain/host-widgets')>();
  return {
    ...actual,
    hostWidgetRegistry: () => (h.registry ??= actual.createHostWidgetRegistry([])),
  };
});

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: ['dashboard:read', 'dashboard:write'] } }),
}));
vi.mock('@/lib/api/dashboards', () => ({
  getDashboard: async () => ({
    token: 'plant',
    name: 'Plant',
    description: null,
    definition: h.definition,
    updatedAt: '2026-10-11T09:00:00Z',
  }),
  getPublishedDashboard: async () => null,
  isNotPublishedError: () => false,
}));
vi.mock('./editor/DashboardWorkspace', () => ({
  DashboardWorkspace: (props: { loaded: DashboardDefinition; registry: unknown }) => {
    h.workspace = props;
    return null;
  },
}));

import DashboardDetailPage from './DashboardDetailPage';

afterEach(() => {
  cleanup();
  h.workspace = undefined;
  h.registry = undefined;
});

const box = { base: { mode: 'grid' as const, col: 0, row: 0, cols: 4, rows: 2 } };

async function openPage(widgets: unknown[], installed: boolean) {
  h.registry = installed ? createHostWidgetRegistry([acmeTankWidget]) : createHostWidgetRegistry([]);
  h.definition = JSON.stringify({ title: 'Plant', slots: { tank: { type: 'device' } }, widgets });
  render(
    <MemoryRouter initialEntries={['/dashboards/plant']}>
      <Routes>
        <Route path="/dashboards/:token" element={<DashboardDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
  // The page awaits preloadWidgets first, and for a board holding a gauge that fetches the chart
  // engine, whose first import in this file's module graph can outlast the 1 s default.
  await waitFor(() => expect(h.workspace).toBeTruthy(), { timeout: 15_000 });
  return h.workspace!;
}

describe('the console dashboard page parses with the deployment catalog', { timeout: 30_000 }, () => {
  it('reads an installed extension widget as that widget, and gives the workspace the same registry', async () => {
    const workspace = await openPage([{ ...ACME_TANK_WIDGET_JSON, layout: box }], true);
    expect(workspace.loaded.widgets[0].type).toBe(ACME_TANK_WIDGET_JSON.type);
    expect(workspace.registry).toBe(h.registry);
  });

  it('keeps a widget whose package is not installed as an unresolved placeholder with its reason', async () => {
    const workspace = await openPage([{ ...ACME_TANK_WIDGET_JSON, layout: box }], false);
    const widget = workspace.loaded.widgets[0];
    expect(widget.type).toBe(UNKNOWN_WIDGET_TYPE);
    expect(widget.reason?.code).toBe('unregistered');
  });

  it("checks a built-in's settings version against the installed one", async () => {
    const stale = { id: 'g', type: 'gauge', widget: { version: '1.0.0', optionsVersion: 9 }, layout: box, options: {} };
    const workspace = await openPage([stale], true);
    expect(workspace.loaded.widgets[0].type).toBe(UNKNOWN_WIDGET_TYPE);
    expect(workspace.loaded.widgets[0].reason?.code).toBe('options-version');
  });
});
