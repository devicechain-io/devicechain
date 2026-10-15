// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Who is offered Clone on the dashboard list.
//
// Cloning reads the source's draft and creates a dashboard; both need dashboard:write. A
// member without it must not be shown a button whose only outcome is an error.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ authorities: [] as string[], toast: vi.fn(), getDashboard: vi.fn() }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn() }));
vi.mock('@/lib/api/dashboards', () => ({
  listDashboards: vi.fn(async () => ({
    results: [{ token: 'ops', name: 'Ops', description: null, updatedAt: '2026-10-10T00:00:00Z' }],
    pagination: { pageNumber: 1, pageSize: 25, totalRecords: 1 },
  })),
  createDashboard: vi.fn(),
  deleteDashboard: vi.fn(),
  getDashboard: h.getDashboard,
}));

import { CURRENT_SCHEMA_VERSION } from '@devicechain/dashboards';

import DashboardsPage from './DashboardsPage';

function renderPage() {
  return render(
    <MemoryRouter>
      <DashboardsPage />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('DashboardsPage clone', () => {
  it('offers Clone to a member who can write dashboards', async () => {
    h.authorities = ['dashboard:read', 'dashboard:write'];
    renderPage();
    await screen.findByText('Ops');
    expect(screen.getByRole('button', { name: 'Clone' })).toBeTruthy();
  });

  it('does not offer Clone to a read-only member', async () => {
    h.authorities = ['dashboard:read'];
    renderPage();
    await screen.findByText('Ops');
    expect(screen.queryByRole('button', { name: 'Clone' })).toBeNull();
  });

  it('refuses to clone a board saved by a newer version, and opens no import', async () => {
    h.authorities = ['dashboard:read', 'dashboard:write'];
    h.toast.mockClear();
    h.getDashboard.mockResolvedValue({
      token: 'ops',
      name: 'Ops',
      description: null,
      definition: JSON.stringify({ schemaVersion: CURRENT_SCHEMA_VERSION + 1, title: 'Ops', widgets: [] }),
    });
    renderPage();
    await screen.findByText('Ops');
    fireEvent.click(screen.getByRole('button', { name: 'Clone' }));
    await waitFor(() =>
      expect(h.toast).toHaveBeenCalledWith(
        'This dashboard was saved by a newer version of DeviceChain. Upgrade this console to open it.',
        'error',
      ),
    );
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
