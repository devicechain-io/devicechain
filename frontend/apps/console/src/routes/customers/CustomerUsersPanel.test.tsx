// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A customer's Users tab lists exactly the members limited to that customer — not the
// tenant's staff, and not another customer's users — and invites with the scope preset.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ invite: vi.fn(), truncated: { value: false } }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] } }),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn() }));
vi.mock('@/lib/api/customers', () => ({
  listCustomers: vi.fn(async () => ({
    results: [{ token: 'acme', name: 'Acme Corp' }],
    pagination: { pageStart: 1, pageEnd: 1, totalRecords: 1 },
  })),
  getCustomerNames: vi.fn(async () => ({ acme: 'Acme Corp' })),
}));
const member = (email: string, scope: string[]) => ({
  email,
  firstName: null,
  lastName: null,
  enabled: true,
  roles: [{ token: 'viewer', name: 'Viewer', owner: 'INSTANCE' }],
  scope: scope.map((token) => ({ kind: 'CUSTOMER', token })),
  managedByIdentityProvider: false,
  invitationPending: false,
});
vi.mock('@/lib/api/members', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/members')>()),
  // The server makes the cut by scope anchor; the fake does the same so the panel is shown
  // only what a real answer would hold.
  listAllMembers: vi.fn(async (filter?: { scopeAnchor?: { kind: string; token: string } }) => ({
    truncated: h.truncated.value,
    members: [
      member('staff@oem.example', []),
      member('ann@acme.example', ['acme']),
      member('both@example.com', ['globex', 'acme']),
      member('gus@globex.example', ['globex']),
    ].filter((m) => m.scope.some((a) => a.kind === filter?.scopeAnchor?.kind && a.token === filter?.scopeAnchor?.token)),
  })),
  listTenantRoles: vi.fn(async () => [
    { token: 'viewer', name: 'Viewer', description: null, authorities: ['device:read'], owner: 'INSTANCE' },
  ]),
  inviteMember: h.invite,
}));

import * as members from '@/lib/api/members';
import { CustomerUsersPanel } from './CustomerUsersPanel';

afterEach(cleanup);

describe("a customer's Users tab", () => {
  it('says so, and still lists what it read, when more users matched than the console reads', async () => {
    h.truncated.value = true;
    try {
      render(
        <MemoryRouter>
          <CustomerUsersPanel customerToken="acme" />
        </MemoryRouter>,
      );
      expect(await screen.findByText('ann@acme.example')).toBeTruthy();
      expect(screen.getByTestId('users-truncated').textContent).toMatch(/first 2 users/);
    } finally {
      h.truncated.value = false;
    }
  });

  it('lists the members limited to this customer and nobody else', async () => {
    render(
      <MemoryRouter>
        <CustomerUsersPanel customerToken="acme" />
      </MemoryRouter>,
    );
    expect(await screen.findByText('ann@acme.example')).toBeTruthy();
    expect(screen.getByText('both@example.com')).toBeTruthy();
    expect(screen.queryByText('staff@oem.example')).toBeNull();
    expect(screen.queryByText('gus@globex.example')).toBeNull();
    expect(members.listAllMembers).toHaveBeenCalledWith({ scopeAnchor: { kind: 'CUSTOMER', token: 'acme' } });
  });

  it('invites with the scope already set to this customer', async () => {
    h.invite.mockResolvedValue({ member: { email: 'new@acme.example' }, invitationCode: 'c' });
    render(
      <MemoryRouter>
        <CustomerUsersPanel customerToken="acme" />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Invite user' }));
    fireEvent.change(await screen.findByLabelText('Email'), { target: { value: 'new@acme.example' } });
    fireEvent.click(screen.getByLabelText('Roles'));
    // "Viewer" is also a role badge in the table; the option is the one the opened list added last.
    const viewer = await screen.findAllByText('Viewer');
    fireEvent.click(viewer[viewer.length - 1]);
    fireEvent.click(screen.getByRole('button', { name: 'Invite member' }));
    await waitFor(() =>
      expect(h.invite).toHaveBeenCalledWith({
        email: 'new@acme.example',
        roleTokens: ['viewer'],
        scope: [{ kind: 'CUSTOMER', token: 'acme' }],
      }),
    );
  });
});
