// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Moving straight from one role to another on the same detail route. The role is found in a
// list that does not reload when only the token changes, and RoleForm seeds its fields from
// the role it is first handed — so without a remount the second role's page showed the FIRST
// role's name and description, and Save wrote them to the second role.
import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { updateRole, listRoles } = vi.hoisted(() => ({ updateRole: vi.fn(), listRoles: vi.fn() }));

function role(scope: string, token: string, name: string) {
  return {
    id: `id-${scope}-${token}`,
    scope,
    token,
    name,
    description: `${name} description`,
    authorities: [],
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
  };
}

vi.mock('@/lib/api/admin', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/admin')>();
  return {
    ...actual,
    listRoles: (...args: unknown[]) => listRoles(...args),
    listAuthorities: () => Promise.resolve([]),
    updateRole: (...args: unknown[]) => {
      updateRole(...args);
      return Promise.resolve({});
    },
  };
});

import RoleDetailPage from './RoleDetailPage';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';

function GoTo({ to }: { to: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(to)}>
      go-{to}
    </button>
  );
}

function renderAt(initial: string, links: string[]) {
  render(
    <MemoryRouter initialEntries={[initial]}>
      <ConfirmProvider>
        {links.map((to) => (
          <GoTo key={to} to={to} />
        ))}
        <Routes>
          <Route path="/admin/roles/:scope/:token" element={<RoleDetailPage />} />
        </Routes>
      </ConfirmProvider>
    </MemoryRouter>,
  );
}

const nameValue = async () => ((await screen.findByLabelText('Name')) as HTMLInputElement).value;

beforeEach(() => {
  updateRole.mockReset();
  listRoles.mockReset();
  listRoles.mockImplementation((scope: string) =>
    Promise.resolve(
      scope === 'tenant'
        ? [role('tenant', 'viewer', 'Viewer Role'), role('tenant', 'editor', 'Editor Role')]
        : [role('system', 'ops', 'Ops Role')],
    ),
  );
});
afterEach(cleanup);

describe('RoleDetailPage across a direct role-to-role navigation', () => {
  it('the form shows the NEW role, and Save writes the new role’s values', async () => {
    renderAt('/admin/roles/tenant/viewer', ['/admin/roles/tenant/editor']);
    await waitFor(async () => expect(await nameValue()).toBe('Viewer Role'));

    fireEvent.click(screen.getByRole('button', { name: 'go-/admin/roles/tenant/editor' }));
    await waitFor(async () => expect(await nameValue()).toBe('Editor Role'));

    fireEvent.click(await screen.findByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(updateRole).toHaveBeenCalledTimes(1));
    expect(updateRole.mock.calls[0][0]).toBe('tenant');
    expect(updateRole.mock.calls[0][1]).toBe('editor');
    const sent = updateRole.mock.calls[0][2] as { name: string; description: string };
    expect(sent.name).toBe('Editor Role');
    expect(sent.description).toBe('Editor Role description');
  });

  it('a switch of scope finds the role in the NEW scope’s list', async () => {
    renderAt('/admin/roles/tenant/viewer', ['/admin/roles/system/ops']);
    await waitFor(async () => expect(await nameValue()).toBe('Viewer Role'));

    fireEvent.click(screen.getByRole('button', { name: 'go-/admin/roles/system/ops' }));
    await waitFor(async () => expect(await nameValue()).toBe('Ops Role'));
    expect(listRoles).toHaveBeenCalledWith('system');
  });
});
