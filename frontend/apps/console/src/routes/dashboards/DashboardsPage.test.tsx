// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Who is offered Clone on the dashboard list.
//
// Cloning reads the source's draft and creates a dashboard; both need dashboard:write. A
// member without it must not be shown a button whose only outcome is an error.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ authorities: [] as string[] }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { authorities: h.authorities } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn() }));
vi.mock('@/lib/api/dashboards', () => ({
  listDashboards: vi.fn(async () => ({
    results: [{ token: 'ops', name: 'Ops', description: null, updatedAt: '2026-10-10T00:00:00Z' }],
    pagination: { pageNumber: 1, pageSize: 25, totalRecords: 1 },
  })),
  createDashboard: vi.fn(),
  deleteDashboard: vi.fn(),
  getDashboard: vi.fn(),
}));

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
});
