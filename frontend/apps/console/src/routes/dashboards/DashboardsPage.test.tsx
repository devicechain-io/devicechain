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

const h = vi.hoisted(() => ({
  authorities: [] as string[],
  scope: [] as string[],
  toast: vi.fn(),
  getDashboard: vi.fn(),
  shareDashboard: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities, scopeAnchors: h.scope } }),
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
  getDashboardSharing: vi.fn(async () => ['acme']),
  shareDashboard: h.shareDashboard,
}));
vi.mock('@/lib/api/customers', () => ({
  listCustomers: vi.fn(async () => ({
    results: [
      { token: 'acme', name: 'Acme' },
      { token: 'globex', name: 'Globex' },
    ],
    pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 },
  })),
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

afterEach(() => {
  cleanup();
  h.scope = [];
});

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

describe('DashboardsPage for a customer user', () => {
  // A member limited to part of the tenant is served only the published dashboards shared
  // with their customers and may change none of them: no create, import, clone, share or
  // delete, whatever their role names.
  it('offers no authoring at all to a scoped member, even one holding dashboard:write', async () => {
    h.authorities = ['*'];
    h.scope = ['customer:acme'];
    renderPage();
    await screen.findByText('Ops');
    for (const name of ['New dashboard', 'Import dashboard', 'Clone', 'Share', 'Delete']) {
      expect(screen.queryByRole('button', { name }), name).toBeNull();
    }
    expect(screen.getByText('The dashboards shared with you.')).toBeTruthy();
  });

  // The negative control: the same authorities unscoped are offered all of it.
  it('offers Share, Clone and Delete to the same authorities without a scope', async () => {
    h.authorities = ['*'];
    renderPage();
    await screen.findByText('Ops');
    for (const name of ['New dashboard', 'Import dashboard', 'Share', 'Clone', 'Delete']) {
      expect(screen.getByRole('button', { name }), name).toBeTruthy();
    }
  });

  it('shares with the customers picked, replacing the list the dashboard had', async () => {
    h.authorities = ['dashboard:read', 'dashboard:write'];
    h.toast.mockClear();
    h.shareDashboard.mockResolvedValue(['acme']);
    renderPage();
    await screen.findByText('Ops');
    fireEvent.click(screen.getByRole('button', { name: 'Share' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Save sharing' }));
    await waitFor(() => expect(h.shareDashboard).toHaveBeenCalledWith('ops', ['acme']));
    await waitFor(() => expect(h.toast).toHaveBeenCalledWith('Ops is shared with 1 customer.'));
  });
});
