// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The identity detail page. Two things are pinned here:
//   - the email in the address is read exactly once: the router already decodes the param, so
//     decoding it again turned an address holding a '%' into another address, or threw.
//   - a membership the server refuses to edit with MANAGED_BY_IDP stops offering the edits the
//     server refuses, and says what to do instead, rather than toasting the raw refusal.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { ConfirmProvider } from '@/components/ui/confirm-dialog';
import { ToastProvider } from '@/components/ui/toast';

const h = vi.hoisted(() => ({
  identities: [] as unknown[],
  setRoles: vi.fn(),
  setEnabled: vi.fn(),
  remove: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ refreshMemberships: () => Promise.resolve() }),
}));

vi.mock('@/lib/api/admin', () => ({
  listIdentities: () => Promise.resolve(h.identities),
  listTenants: () => Promise.resolve([]),
  listRoles: () => Promise.resolve([]),
  setIdentityEnabled: vi.fn(),
  setSystemRoles: vi.fn(),
  setPassword: vi.fn(),
  deleteIdentity: vi.fn(),
  addMembership: vi.fn(),
  setMembershipRoles: (...a: unknown[]) => h.setRoles(...a),
  setMembershipEnabled: (...a: unknown[]) => h.setEnabled(...a),
  removeMembership: (...a: unknown[]) => h.remove(...a),
}));

vi.mock('@/routes/admin/identities/IdentityMfaPanel', () => ({
  IdentityMfaPanel: () => null,
}));

import IdentityDetailPage from './IdentityDetailPage';

const t = (key: string) => i18n.t(key);

function identity(email: string, memberships: { tenant: string; enabled: boolean; roles: string[] }[] = []) {
  return {
    id: `id-${email}`,
    email,
    firstName: null,
    lastName: null,
    enabled: true,
    systemRoles: [],
    mfaEnabled: false,
    memberships,
    createdAt: '2026-08-01T00:00:00Z',
    updatedAt: '2026-08-01T00:00:00Z',
  };
}

function renderAt(email: string) {
  render(
    <MemoryRouter initialEntries={['/admin/identities/' + encodeURIComponent(email)]}>
      <ToastProvider>
        <ConfirmProvider>
          <Routes>
            <Route path="/admin/identities/:email" element={<IdentityDetailPage />} />
          </Routes>
        </ConfirmProvider>
      </ToastProvider>
    </MemoryRouter>,
  );
}

const managed = new GraphQLRequestError('managed', 200, [
  { message: 'this membership is managed by an identity provider', extensions: { code: 'MANAGED_BY_IDP' } },
]);

// The membership row's own controls: the page header carries an identity-wide Disable too.
const button = (name: string) =>
  within(screen.getByText('acme').parentElement!).getByRole('button', { name }) as HTMLButtonElement;

beforeEach(async () => {
  h.setRoles.mockReset();
  h.setEnabled.mockReset();
  h.remove.mockReset();
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('the email in the address', () => {
  it.each([
    ['a percent sign that decodes to something else', 'a%41b@example.com'],
    ['a percent sign that does not decode at all', 'odd%zz@example.com'],
    ['an encoded-looking sequence', 'x%2540y@example.com'],
  ])('finds the identity whose email holds %s', async (_, email) => {
    // A second identity whose email is what a second decode would produce, so a double decode
    // shows up as the WRONG identity rather than only as "not found".
    h.identities = [identity(email), identity('aAb@example.com'), identity('x%40y@example.com')];
    renderAt(email);
    expect(await screen.findByRole('heading', { name: email })).toBeTruthy();
  });
});

describe('a membership the identity provider manages', () => {
  beforeEach(() => {
    h.identities = [identity('sso@example.com', [{ tenant: 'acme', enabled: true, roles: [] }])];
  });

  it('is refused once, then stops offering the edits and says what to do instead', async () => {
    h.remove.mockRejectedValue(managed);
    renderAt('sso@example.com');
    await screen.findByText('acme');
    expect(screen.queryByText(t('identities:managedByIdpNotice'))).toBeNull();

    fireEvent.click(button(t('identities:removeButton')));
    await screen.findByText(t('identities:managedByIdpNotice'));
    expect(h.remove).toHaveBeenCalledTimes(1);

    // The server's own words are not toasted on top of the explanation.
    expect(screen.queryByText('this membership is managed by an identity provider')).toBeNull();
    expect(button(t('identities:removeButton')).disabled).toBe(true);
    expect(button(t('identities:saveRolesButton')).disabled).toBe(true);
    expect(button(t('identities:disable')).disabled).toBe(true);
  });

  it('keeps any other refusal as it was, and leaves the row editable', async () => {
    h.setEnabled.mockRejectedValue(new GraphQLRequestError('nope', 200, [{ message: 'nope', extensions: { code: 'FORBIDDEN' } }]));
    renderAt('sso@example.com');
    await screen.findByText('acme');
    fireEvent.click(button(t('identities:disable')));
    await screen.findByText('nope');
    expect(screen.queryByText(t('identities:managedByIdpNotice'))).toBeNull();
    expect(button(t('identities:removeButton')).disabled).toBe(false);
  });

  it('still offers enabling a disabled one, which the server does not refuse for this reason', async () => {
    h.identities = [identity('sso@example.com', [{ tenant: 'acme', enabled: false, roles: [] }])];
    h.setRoles.mockRejectedValue(managed);
    renderAt('sso@example.com');
    await screen.findByText('acme');
    fireEvent.click(button(t('identities:saveRolesButton')));
    await screen.findByText(t('identities:managedByIdpNotice'));
    expect(button(t('identities:enable')).disabled).toBe(false);
    expect(button(t('identities:removeButton')).disabled).toBe(true);
  });
});
