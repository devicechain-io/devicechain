// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A tenant's roles list: loading, empty and error states, a populated row that opens the
// role, operator-defined roles sorting before the tenant's own, and the write gate: no
// create button without role:write.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { never, renderListPage } from '@/test/list-page';

const h = vi.hoisted(() => ({
  list: vi.fn(),
  claims: { authorities: [] as string[], scopeAnchors: [] as string[] },
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: h.claims }),
}));
vi.mock('@/lib/api/members', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/members')>()),
  listTenantRoles: (...a: unknown[]) => h.list(...a),
}));

import RolesPage from './RolesPage';

const t = (key: string) => i18n.t(key);

const tenantRole = { token: 'site-lead', name: 'Site lead', owner: 'TENANT', authorities: ['device:read'] };
const instanceRole = { token: 'viewer', name: 'Viewer', owner: 'INSTANCE', authorities: ['*:read'] };

describe('RolesPage (tenant)', () => {
  beforeEach(() => {
    h.list.mockReset();
    h.claims = { authorities: ['role:read', 'role:write'], scopeAnchors: [] };
  });
  afterEach(cleanup);

  it('shows the loading state while the list is read', () => {
    h.list.mockReturnValue(never());
    renderListPage(<RolesPage />);
    expect(screen.getByRole('status').textContent).toBe(t('members:rolesLoading'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the empty state, not an empty table, when there are no roles', async () => {
    h.list.mockResolvedValue([]);
    renderListPage(<RolesPage />);
    expect(await screen.findByText(t('members:rolesEmpty'))).toBeTruthy();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the error, not an empty table, when the read fails', async () => {
    h.list.mockImplementation(() => Promise.reject(new GraphQLRequestError('upstream exploded', 500)));
    renderListPage(<RolesPage />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('upstream exploded');
    expect(screen.queryByText(t('members:rolesEmpty'))).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('lists the operator roles first and opens a role on click', async () => {
    h.list.mockResolvedValue([tenantRole, instanceRole]);
    renderListPage(<RolesPage />);
    await screen.findByRole('table');
    const rows = screen.getAllByRole('button', { name: /Viewer|Site lead/ });
    expect(rows.map((r) => (r.textContent ?? '').includes('Viewer'))).toEqual([true, false]);
    fireEvent.click(rows[1]);
    expect(screen.getByTestId('where').textContent).toBe('/roles/site-lead');
  });

  it('offers the create button to a writer', async () => {
    h.list.mockResolvedValue([tenantRole]);
    renderListPage(<RolesPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('button', { name: new RegExp(t('members:newRole')) })).toBeTruthy();
  });

  it('offers no create button to a read-only member, who still sees the roles', async () => {
    h.claims = { authorities: ['role:read'], scopeAnchors: [] };
    h.list.mockResolvedValue([tenantRole]);
    renderListPage(<RolesPage />);
    await screen.findByRole('table');
    expect(screen.getByRole('button', { name: /Site lead/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: new RegExp(t('members:newRole')) })).toBeNull();
  });
});
