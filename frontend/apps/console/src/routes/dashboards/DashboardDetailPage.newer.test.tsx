// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A board saved by a newer release is REFUSED on the detail page -- the console's viewer door
// (read-only members) and its editor door (authors) alike. The workspace, which owns Save, is
// replaced by a recorder: the claim is that it is never mounted over a board this console
// cannot read, so there is no editor to offer saving it back over the newer one.

import i18n from '@/i18n/config';
import { CURRENT_SCHEMA_VERSION } from '@devicechain/dashboards';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ authorities: [] as string[], mounted: 0 }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities } }),
}));
vi.mock('./editor/DashboardWorkspace', () => ({
  DashboardWorkspace: () => {
    h.mounted++;
    return <div data-testid="workspace" />;
  },
}));
vi.mock('@/lib/api/dashboards', () => ({
  getDashboard: vi.fn(),
  getPublishedDashboard: vi.fn(),
  isNotPublishedError: () => false,
}));

import { getDashboard, getPublishedDashboard } from '@/lib/api/dashboards';
import DashboardDetailPage from './DashboardDetailPage';

const stored = (schemaVersion: number) => ({
  token: 'ops',
  name: 'Ops',
  description: null,
  definition: JSON.stringify({ schemaVersion, title: 'T', widgets: [] }),
  updatedAt: '2026-10-10T00:00:00Z',
  publishedVersion: 1,
});

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/dashboards/ops']}>
      <Routes>
        <Route path="/dashboards/:token" element={<DashboardDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  h.mounted = 0;
  await i18n.changeLanguage('en');
  vi.mocked(getDashboard).mockReset();
  vi.mocked(getPublishedDashboard).mockReset();
});
afterEach(cleanup);

describe('DashboardDetailPage with a board from a newer version', () => {
  it('refuses it for an author, and never mounts the editor', async () => {
    h.authorities = ['dashboard:read', 'dashboard:write'];
    vi.mocked(getDashboard).mockResolvedValue(stored(CURRENT_SCHEMA_VERSION + 1));
    renderPage();
    await screen.findByText(/saved by a newer version of DeviceChain\. Upgrade this console/);
    expect(screen.queryByTestId('workspace')).toBeNull();
    expect(h.mounted).toBe(0);
  });

  it('refuses it for a read-only member, in that member language', async () => {
    await i18n.changeLanguage('es');
    h.authorities = ['dashboard:read'];
    vi.mocked(getPublishedDashboard).mockResolvedValue({ ...stored(CURRENT_SCHEMA_VERSION + 1), version: 1, publishedAt: '2026-10-10T00:00:00Z' });
    renderPage();
    await screen.findByText(i18n.t('dashboards:schemaTooNew'));
    expect(i18n.t('dashboards:schemaTooNew')).toMatch(/versión más reciente/);
    expect(h.mounted).toBe(0);
  });

  it('still opens a board at the current version', async () => {
    h.authorities = ['dashboard:read', 'dashboard:write'];
    vi.mocked(getDashboard).mockResolvedValue(stored(CURRENT_SCHEMA_VERSION));
    renderPage();
    await screen.findByTestId('workspace');
  });
});
