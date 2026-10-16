// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The page an invitation link lands on. The code arrives in the URL fragment; the page
// takes it out of the address bar at once, accepts with whichever proof the person gives,
// and enters the tenant. Only the auth provider is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  acceptInvitation: vi.fn(),
  login: vi.fn(),
  selectTenant: vi.fn(),
  identityToken: null as string | null,
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    acceptInvitation: h.acceptInvitation,
    login: h.login,
    selectTenant: h.selectTenant,
    identityToken: h.identityToken,
  }),
}));
vi.mock('@/lib/sso', () => ({ startSso: vi.fn() }));

import AcceptInvitationPage from './AcceptInvitation';
import { heldInvitation } from '@/lib/invitation';

function renderAt() {
  return render(
    <MemoryRouter initialEntries={['/invitation']}>
      <Routes>
        <Route path="/invitation" element={<AcceptInvitationPage />} />
        <Route path="/" element={<div>home</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

const auth = (tenants: string[]) => ({
  identityToken: 'id-token',
  expiresAt: '',
  superuser: false,
  memberships: tenants.map((tenant) => ({ tenant, roles: ['viewer'], ssoRequired: false })),
});

beforeEach(() => {
  window.sessionStorage.clear();
  h.acceptInvitation.mockReset();
  h.login.mockReset();
  h.selectTenant.mockReset().mockResolvedValue(undefined);
  h.identityToken = null;
});
afterEach(cleanup);

describe('accepting an invitation', () => {
  it('takes the code out of the address bar and accepts with a new password', async () => {
    window.history.replaceState(null, '', '/invitation#code=abc123');
    h.acceptInvitation.mockResolvedValue(auth(['oem']));
    renderAt();

    // The fragment is gone from the address bar as soon as the page has read it.
    expect(window.location.hash).toBe('');
    fireEvent.change(screen.getByLabelText('Choose a password'), { target: { value: 's3cret-pass' } });
    fireEvent.change(screen.getByLabelText('Repeat the password'), { target: { value: 's3cret-pass' } });
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));

    await waitFor(() => expect(h.acceptInvitation).toHaveBeenCalledWith('abc123', { password: 's3cret-pass' }));
    // One tenant to enter: it is entered, and the held code is dropped.
    await waitFor(() => expect(h.selectTenant).toHaveBeenCalledWith('id-token', 'oem'));
    expect(await screen.findByText('home')).toBeTruthy();
    expect(heldInvitation()).toBeNull();
  });

  it('accepts as an existing account by signing in first', async () => {
    window.history.replaceState(null, '', '/invitation#code=abc123');
    h.login.mockResolvedValue(auth(['other']));
    h.acceptInvitation.mockResolvedValue(auth(['other', 'oem']));
    renderAt();

    fireEvent.click(screen.getByRole('radio', { name: 'I have an account' }));
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'ann@acme.example' } });
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pw' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in and accept' }));

    await waitFor(() => expect(h.acceptInvitation).toHaveBeenCalledWith('abc123', { identityToken: 'id-token' }));
    // Two tenants now: the person chooses.
    fireEvent.click(await screen.findByRole('button', { name: 'oem' }));
    await waitFor(() => expect(h.selectTenant).toHaveBeenCalledWith('id-token', 'oem'));
  });

  it('accepts as the signed-in account when one is held (back from single sign-on)', async () => {
    window.sessionStorage.setItem('dc-pending-invitation', 'held-code');
    window.history.replaceState(null, '', '/invitation');
    h.identityToken = 'sso-identity';
    h.acceptInvitation.mockResolvedValue(auth(['oem']));
    renderAt();
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));
    await waitFor(() => expect(h.acceptInvitation).toHaveBeenCalledWith('held-code', { identityToken: 'sso-identity' }));
  });

  it('gives one answer for any unusable code, and forgets it', async () => {
    window.history.replaceState(null, '', '/invitation#code=stale');
    h.acceptInvitation.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'invalid invitation', extensions: { code: 'INVALID_INVITATION' } }]),
    );
    renderAt();
    fireEvent.change(screen.getByLabelText('Choose a password'), { target: { value: 'x' } });
    fireEvent.change(screen.getByLabelText('Repeat the password'), { target: { value: 'x' } });
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));
    expect(await screen.findByText(/This invitation cannot be used/)).toBeTruthy();
    expect(heldInvitation()).toBeNull();
    expect(h.selectTenant).not.toHaveBeenCalled();
  });

  it('refuses two different passwords without asking the server', async () => {
    window.history.replaceState(null, '', '/invitation#code=abc');
    renderAt();
    fireEvent.change(screen.getByLabelText('Choose a password'), { target: { value: 'one' } });
    fireEvent.change(screen.getByLabelText('Repeat the password'), { target: { value: 'two' } });
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));
    expect(await screen.findByText('The two passwords are different.')).toBeTruthy();
    expect(h.acceptInvitation).not.toHaveBeenCalled();
  });

  it('says the link is incomplete when it carries no code', () => {
    window.history.replaceState(null, '', '/invitation');
    renderAt();
    expect(screen.getByText(/This link is incomplete/)).toBeTruthy();
  });
});
