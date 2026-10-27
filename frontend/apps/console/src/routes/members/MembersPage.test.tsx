// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant's Members page: what a row shows, which rows offer actions, and the
// invitation link an admin is shown exactly once. The transport is faked; the page, the
// access sheet and the error mapping are real.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  claims: { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] as string[] },
  members: [] as unknown[],
  invite: vi.fn(),
  setScope: vi.fn(),
  setRoles: vi.fn(),
  setAccess: vi.fn(),
  toast: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({ useAuth: () => ({ claims: h.claims }) }));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: h.toast }) }));
vi.mock('@/components/ui/confirm-dialog', () => ({ useConfirm: () => vi.fn(async () => true) }));
vi.mock('@/lib/api/customers', () => ({
  listCustomers: vi.fn(async () => ({
    results: [
      { token: 'acme', name: 'Acme Corp' },
      { token: 'globex', name: 'Globex' },
    ],
    pagination: { pageStart: 1, pageEnd: 2, totalRecords: 2 },
  })),
  getCustomerNames: vi.fn(async (tokens: string[]) =>
    Object.fromEntries(tokens.filter((t) => t === 'acme').map((t) => [t, 'Acme Corp'])),
  ),
}));
vi.mock('@/lib/api/device-management', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/device-management')>()),
  // The access-scope groups the picker offers: only these, never every group.
  listAccessScopeGroups: vi.fn(async () => [{ token: 'north', name: 'North wing' }]),
  getGroupNames: vi.fn(async (tokens: string[]) =>
    Object.fromEntries(tokens.filter((t) => t === 'north').map((t) => [t, 'North wing'])),
  ),
}));
vi.mock('@/lib/api/members', async (orig) => ({
  ...(await orig<typeof import('@/lib/api/members')>()),
  listMembers: vi.fn(async () => ({
    results: h.members,
    pagination: { pageStart: 1, pageEnd: h.members.length, totalRecords: h.members.length },
  })),
  listInvitations: vi.fn(async () => ({ results: [], pagination: { pageStart: null, pageEnd: null, totalRecords: 0 } })),
  listTenantRoles: vi.fn(async () => [
    { token: 'viewer', name: 'Viewer', description: null, authorities: ['device:read'], owner: 'INSTANCE' },
    { token: 'acme-operator', name: 'Acme operator', description: null, authorities: ['device:read'], owner: 'TENANT' },
  ]),
  inviteMember: h.invite,
  setMemberScope: h.setScope,
  setMemberRoles: h.setRoles,
  setMemberAccess: h.setAccess,
}));

import MembersPage from './MembersPage';

const member = (over: Record<string, unknown>) => ({
  email: 'x@example.com',
  firstName: null,
  lastName: null,
  enabled: true,
  roles: [{ token: 'viewer', name: 'Viewer', owner: 'INSTANCE' }],
  scope: [],
  managedByIdentityProvider: false,
  invitationPending: false,
  mfaEnabled: false,
  ...over,
});

function renderPage() {
  return render(
    <MemoryRouter>
      <MembersPage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  h.claims = { username: 'boss@oem.example', authorities: ['*'], scopeAnchors: [] };
  h.invite.mockReset();
  h.setScope.mockReset();
  h.setRoles.mockReset();
  h.setAccess.mockReset();
  h.toast.mockReset();
});
afterEach(cleanup);

describe('the members list', () => {
  it('names what each member can see: the whole tenant, or the customers and groups they are limited to', async () => {
    h.members = [
      member({ email: 'staff@oem.example' }),
      member({ email: 'ann@acme.example', scope: [{ kind: 'CUSTOMER', token: 'acme' }] }),
      member({ email: 'gus@globex.example', scope: [{ kind: 'CUSTOMER', token: 'globex' }] }),
      member({
        email: 'nora@oem.example',
        scope: [
          { kind: 'GROUP', token: 'north' },
          { kind: 'GROUP', token: 'retired' },
        ],
      }),
    ];
    renderPage();
    const staff = (await screen.findByText('staff@oem.example')).closest('tr')!;
    expect(within(staff).getByText('Whole tenant')).toBeTruthy();
    const ann = screen.getByText('ann@acme.example').closest('tr')!;
    await waitFor(() => expect(within(ann).getByText('Acme Corp')).toBeTruthy());
    // A customer whose name could not be read is shown by its token, never as "whole tenant".
    const gus = screen.getByText('gus@globex.example').closest('tr')!;
    expect(within(gus).getByText('globex')).toBeTruthy();
    expect(within(gus).queryByText('Whole tenant')).toBeNull();
    // A group is named like a customer, and marked as a group, not a customer.
    const nora = screen.getByText('nora@oem.example').closest('tr')!;
    await waitFor(() => expect(within(nora).getByText('North wing')).toBeTruthy());
    expect(within(nora).getByText('retired')).toBeTruthy();
    expect(within(nora).getAllByTitle('Group of devices')).toHaveLength(2);
    expect(within(nora).queryByText('Whole tenant')).toBeNull();
  });

  it('shows whether each member has two-factor authentication on, and nothing for a pending invitation', async () => {
    h.members = [
      member({ email: 'on@oem.example', mfaEnabled: true }),
      member({ email: 'off@oem.example' }),
      member({ email: 'new@oem.example', invitationPending: true }),
    ];
    renderPage();
    const on = (await screen.findByText('on@oem.example')).closest('tr')!;
    expect(within(on).getByText('On')).toBeTruthy();
    expect(within(screen.getByText('off@oem.example').closest('tr')!).getByText('Off')).toBeTruthy();
    const pending = screen.getByText('new@oem.example').closest('tr')!;
    expect(within(pending).queryByText('On')).toBeNull();
    expect(within(pending).queryByText('Off')).toBeNull();
  });

  it('offers no actions on your own row or on a membership the identity provider manages', async () => {
    h.members = [
      member({ email: 'boss@oem.example' }),
      member({ email: 'idp@oem.example', managedByIdentityProvider: true }),
      member({ email: 'ann@acme.example' }),
    ];
    renderPage();
    await screen.findByText('ann@acme.example');
    expect(screen.queryByRole('button', { name: 'Actions for boss@oem.example' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Actions for idp@oem.example' })).toBeNull();
    expect(screen.getByText('Managed by the identity provider')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Actions for ann@acme.example' })).toBeTruthy();
  });

  it('offers no invite and no actions to a member without member:write', async () => {
    h.claims = { username: 'reader@oem.example', authorities: ['member:read'], scopeAnchors: [] };
    h.members = [member({ email: 'ann@acme.example' })];
    renderPage();
    await screen.findByText('ann@acme.example');
    expect(screen.queryByRole('button', { name: 'Invite member' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Actions for ann@acme.example' })).toBeNull();
  });
});

describe('inviting a member', () => {
  it('sends the chosen scope and shows the one-time link with the code in its fragment', async () => {
    h.members = [];
    h.invite.mockResolvedValue({ member: { email: 'new@acme.example' }, invitationCode: 'c0de+/=' });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Invite member' }));
    fireEvent.change(await screen.findByLabelText('Email'), { target: { value: ' new@acme.example ' } });
    // Roles are picked through the kit's multi-select; open it and choose one.
    fireEvent.click(screen.getByLabelText('Roles'));
    fireEvent.click(await screen.findByText('Acme operator'));
    fireEvent.click(screen.getByRole('radio', { name: 'Specific customers or groups' }));
    fireEvent.click(await screen.findByLabelText('Customers'));
    fireEvent.click(await screen.findByText('Acme Corp'));
    fireEvent.click(screen.getByRole('button', { name: 'Invite member', hidden: false }));

    await waitFor(() =>
      expect(h.invite).toHaveBeenCalledWith({
        email: 'new@acme.example',
        roleTokens: ['acme-operator'],
        scope: [{ kind: 'CUSTOMER', token: 'acme' }],
      }),
    );
    const link = (await screen.findByLabelText('Invitation link')) as HTMLInputElement;
    // The code rides the fragment, which a browser never sends to a server.
    expect(link.value).toBe(`${window.location.origin}/invitation#code=${encodeURIComponent('c0de+/=')}`);
    expect(screen.getByText(/shown only once/)).toBeTruthy();
  });

  it('limits a member to an access-scope group, beside a customer, and says where a missing group is enabled', async () => {
    h.members = [];
    h.invite.mockResolvedValue({ member: { email: 'nora@oem.example' }, invitationCode: 'c0de' });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Invite member' }));
    fireEvent.change(await screen.findByLabelText('Email'), { target: { value: 'nora@oem.example' } });
    fireEvent.click(screen.getByLabelText('Roles'));
    fireEvent.click(await screen.findByText('Viewer'));
    fireEvent.click(screen.getByRole('radio', { name: 'Specific customers or groups' }));
    expect(screen.getByText(/enable it as an access scope on the group's page/)).toBeTruthy();
    fireEvent.click(await screen.findByLabelText('Customers'));
    fireEvent.click(await screen.findByText('Acme Corp'));
    fireEvent.click(screen.getByLabelText('Groups of devices'));
    fireEvent.click(await screen.findByText('North wing'));
    fireEvent.click(screen.getByRole('button', { name: 'Invite member', hidden: false }));

    await waitFor(() =>
      expect(h.invite).toHaveBeenCalledWith({
        email: 'nora@oem.example',
        roleTokens: ['viewer'],
        scope: [
          { kind: 'CUSTOMER', token: 'acme' },
          { kind: 'GROUP', token: 'north' },
        ],
      }),
    );
  });

  it('keeps a group already in the scope when it is edited, even once it is no longer an access scope', async () => {
    h.members = [member({ email: 'nora@oem.example', scope: [{ kind: 'GROUP', token: 'retired' }] })];
    h.setRoles.mockResolvedValue(undefined);
    renderPage();
    fireEvent.pointerDown(await screen.findByRole('button', { name: 'Actions for nora@oem.example' }), {
      button: 0,
      ctrlKey: false,
      pointerType: 'mouse',
    });
    fireEvent.click(await screen.findByText('Edit access'));
    // The sheet opens LIMITED with the group still picked, though no access scope offers it:
    // dropping it here would narrow the member's scope on the next save, or show the member
    // as seeing the whole tenant.
    const groupsField = await screen.findByLabelText('Groups of devices');
    expect(within(groupsField).getByText('retired')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Remove retired' })).toBeTruthy();
    fireEvent.click(await screen.findByLabelText('Roles'));
    fireEvent.click(await screen.findByText('Acme operator'));
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(h.setRoles).toHaveBeenCalled());
    // Only the roles changed: the scope, naming the group, is not rewritten.
    expect(h.setScope).not.toHaveBeenCalled();
  });

  // Roles and scope changed together are one write: if the server refuses it, neither half is
  // saved, so the banner's "not saved" is true. Two calls would have saved the roles first.
  it('saves roles and scope changed together in one call', async () => {
    h.members = [member({ email: 'ann@acme.example', scope: [{ kind: 'CUSTOMER', token: 'acme' }] })];
    h.setAccess.mockResolvedValue(undefined);
    renderPage();
    fireEvent.pointerDown(await screen.findByRole('button', { name: 'Actions for ann@acme.example' }), {
      button: 0,
      ctrlKey: false,
      pointerType: 'mouse',
    });
    fireEvent.click(await screen.findByText('Edit access'));
    fireEvent.click(await screen.findByLabelText('Roles'));
    fireEvent.click(await screen.findByText('Acme operator'));
    fireEvent.click(await screen.findByLabelText('Groups of devices'));
    fireEvent.click(await screen.findByText('North wing'));
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() =>
      expect(h.setAccess).toHaveBeenCalledWith('ann@acme.example', ['viewer', 'acme-operator'], [
        { kind: 'CUSTOMER', token: 'acme' },
        { kind: 'GROUP', token: 'north' },
      ]),
    );
    expect(h.setRoles).not.toHaveBeenCalled();
    expect(h.setScope).not.toHaveBeenCalled();
  });

  it('maps a refusal code to words that say what to do', async () => {
    h.members = [];
    h.invite.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'already a member', extensions: { code: 'CONFLICT' } }]),
    );
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Invite member' }));
    fireEvent.change(await screen.findByLabelText('Email'), { target: { value: 'dup@oem.example' } });
    fireEvent.click(screen.getByLabelText('Roles'));
    fireEvent.click(await screen.findByText('Viewer'));
    fireEvent.click(screen.getByRole('button', { name: 'Invite member', hidden: false }));
    expect(await screen.findByText('That person is already a member of this tenant.')).toBeTruthy();
  });
});
