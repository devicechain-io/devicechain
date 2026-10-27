// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The page an invitation link lands on. The code arrives in the URL fragment; the page
// takes it out of the address bar at once, accepts with whichever proof the person gives,
// and enters the tenant. Only the auth provider is faked.

import '@/i18n/config';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { GraphQLRequestError } from '@devicechain/client';

const h = vi.hoisted(() => ({
  acceptInvitation: vi.fn(),
  login: vi.fn(),
  verifyMfa: vi.fn(),
  selectTenant: vi.fn(),
  identityToken: null as string | null,
  logout: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({
    acceptInvitation: h.acceptInvitation,
    login: h.login,
    verifyMfa: h.verifyMfa,
    selectTenant: h.selectTenant,
    logout: h.logout,
    identityToken: h.identityToken,
  }),
}));
vi.mock('@/lib/sso', () => ({ startSso: vi.fn() }));

import AcceptInvitationPage from './AcceptInvitation';
import { heldInvitation } from '@/lib/invitation';
import { startSso } from '@/lib/sso';

function Where() {
  const loc = useLocation();
  return <div data-testid="at">{loc.pathname + JSON.stringify(loc.state)}</div>;
}

function renderAt() {
  return render(
    <MemoryRouter initialEntries={['/invitation']}>
      <Routes>
        <Route path="/invitation" element={<AcceptInvitationPage />} />
        <Route path="/" element={<div>home</div>} />
        <Route path="/security" element={<Where />} />
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
  h.verifyMfa.mockReset();
  h.selectTenant.mockReset().mockResolvedValue(undefined);
  h.logout.mockReset().mockResolvedValue(undefined);
  vi.mocked(startSso).mockReset();
  h.identityToken = null;
});
afterEach(cleanup);

describe('accepting an invitation', () => {
  it('takes the code out of the address bar and accepts with a new password', async () => {
    window.history.replaceState(null, '', '/invitation#code=abc123');
    h.acceptInvitation.mockResolvedValue(auth(['oem']));
    renderAt();

    // The fragment is gone from the address bar as soon as the page has read it, and the
    // code is held in session storage instead: that is what lets it survive the round trip
    // through an identity provider.
    expect(window.location.hash).toBe('');
    expect(heldInvitation()).toBe('abc123');
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

  it('forgets the held code when the signed-in account is refused, and "Not you?" holds it again', async () => {
    window.sessionStorage.setItem('dc-pending-invitation', 'held-code');
    window.history.replaceState(null, '', '/invitation');
    h.identityToken = 'wrong-identity';
    h.acceptInvitation.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'invalid invitation', extensions: { code: 'INVALID_INVITATION' } }]),
    );
    renderAt();
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));
    expect(await screen.findByText(/This invitation cannot be used/)).toBeTruthy();
    // A held code would send every later single sign-on in this tab back to this page,
    // to be refused again, so the refusal drops it...
    expect(heldInvitation()).toBeNull();
    // ...and choosing to switch accounts is what holds it again.
    fireEvent.click(screen.getByRole('button', { name: 'Not you? Use a different account' }));
    await waitFor(() => expect(h.logout).toHaveBeenCalledTimes(1));
    expect(heldInvitation()).toBe('held-code');
  });

  it('holds the code again when leaving for single sign-on after a refusal', async () => {
    // Signing in with a password as the wrong account: the refusal drops the held copy,
    // the page keeps the code, and single sign-on must take it along or the person comes
    // back from the provider with nothing to accept.
    window.history.replaceState(null, '', '/invitation#code=abc123');
    h.login.mockResolvedValue(auth(['other']));
    h.acceptInvitation.mockRejectedValue(
      new GraphQLRequestError('GraphQL error', 200, [{ message: 'invalid invitation', extensions: { code: 'INVALID_INVITATION' } }]),
    );
    let heldWhenLeaving: string | null = 'never called';
    vi.mocked(startSso).mockImplementation(async () => {
      heldWhenLeaving = heldInvitation();
      return null;
    });
    renderAt();
    fireEvent.click(screen.getByRole('radio', { name: 'I have an account' }));
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'ann@acme.example' } });
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pw' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in and accept' }));
    expect(await screen.findByText(/This invitation cannot be used/)).toBeTruthy();
    expect(heldInvitation()).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Sign in with SSO and accept' }));
    await waitFor(() => expect(startSso).toHaveBeenCalledTimes(1));
    expect(heldWhenLeaving).toBe('abc123');
  });

  it('"Not you?" signs the account out and keeps the code', async () => {
    window.sessionStorage.setItem('dc-pending-invitation', 'held-code');
    window.history.replaceState(null, '', '/invitation');
    h.identityToken = 'wrong-identity';
    // The real provider clears the identity; the fake re-renders the page signed out.
    let view!: ReturnType<typeof renderAt>;
    h.logout.mockImplementation(async () => {
      h.identityToken = null;
      view.rerender(
        <MemoryRouter initialEntries={['/invitation']}>
          <Routes>
            <Route path="/invitation" element={<AcceptInvitationPage />} />
          </Routes>
        </MemoryRouter>,
      );
    });
    view = renderAt();
    fireEvent.click(screen.getByRole('button', { name: 'Not you? Use a different account' }));
    await waitFor(() => expect(h.logout).toHaveBeenCalledTimes(1));
    expect(heldInvitation()).toBe('held-code');
    expect(await screen.findByRole('radio', { name: 'I have an account' })).toBeTruthy();
    expect(screen.getByRole('radio', { name: 'New account' })).toBeTruthy();
    expect(h.acceptInvitation).not.toHaveBeenCalled();
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

// An existing account with two-factor authentication on: its password is right, so login
// holds the sign-in for a code, and the invitation is accepted as that account only after the
// code passes. The tenant it joins may itself require a factor the account lacks.
describe('accepting as an existing account with two-factor authentication', () => {
  const challenge = (expiresAt = new Date(Date.now() + 300_000).toISOString()) =>
    new GraphQLRequestError('mfa', 200, [
      { message: 'mfa', extensions: { code: 'MFA_CHALLENGE', challenge: 'chal-9', expiresAt, methods: ['TOTP', 'RECOVERY_CODE'] } },
    ]);
  const enrollRequired = () =>
    new GraphQLRequestError('x', 200, [{ message: 'x', extensions: { code: 'MFA_ENROLLMENT_REQUIRED' } }]);

  const signInExisting = async () => {
    window.history.replaceState(null, '', '/invitation#code=abc123');
    renderAt();
    fireEvent.click(screen.getByRole('radio', { name: 'I have an account' }));
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'ann@acme.example' } });
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pw' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in and accept' }));
    await screen.findByLabelText('Authenticator code');
  };
  const enterCode = () => {
    fireEvent.change(screen.getByLabelText('Authenticator code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: 'Verify' }));
  };

  it('asks for the code instead of "invalid credentials", then accepts as that account', async () => {
    h.login.mockRejectedValue(challenge());
    h.verifyMfa.mockResolvedValue(auth(['other']));
    h.acceptInvitation.mockResolvedValue(auth(['oem']));
    await signInExisting();
    expect(screen.queryByText('Invalid email or password.')).toBeNull();
    expect(h.acceptInvitation).not.toHaveBeenCalled();

    enterCode();
    await waitFor(() => expect(h.verifyMfa).toHaveBeenCalledWith('chal-9', '123456'));
    await waitFor(() => expect(h.acceptInvitation).toHaveBeenCalledWith('abc123', { identityToken: 'id-token' }));
    await waitFor(() => expect(h.selectTenant).toHaveBeenCalledWith('id-token', 'oem'));
  });

  it('does not accept the invitation when the code is refused', async () => {
    h.login.mockRejectedValue(challenge());
    h.verifyMfa.mockRejectedValue(new GraphQLRequestError('invalid code', 200, [{ message: 'invalid code' }]));
    await signInExisting();
    enterCode();
    await screen.findByText('That code did not work. Check it and try again.');
    expect(h.acceptInvitation).not.toHaveBeenCalled();
  });

  it('sends a member to set up a second factor when the tenant they joined requires one', async () => {
    h.login.mockRejectedValue(challenge());
    h.verifyMfa.mockResolvedValue(auth(['other']));
    h.acceptInvitation.mockResolvedValue(auth(['oem']));
    h.selectTenant.mockRejectedValue(enrollRequired());
    await signInExisting();
    enterCode();
    expect((await screen.findByTestId('at')).textContent).toBe('/security{"enrollForTenant":"oem"}');
  });

  it('routes MFA_ENROLLMENT_REQUIRED to enrolment for a new account too', async () => {
    window.history.replaceState(null, '', '/invitation#code=abc123');
    h.acceptInvitation.mockResolvedValue(auth(['oem']));
    h.selectTenant.mockRejectedValue(enrollRequired());
    renderAt();
    fireEvent.change(screen.getByLabelText('Choose a password'), { target: { value: 's3cret-pass' } });
    fireEvent.change(screen.getByLabelText('Repeat the password'), { target: { value: 's3cret-pass' } });
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));
    expect((await screen.findByTestId('at')).textContent).toBe('/security{"enrollForTenant":"oem"}');
  });
});
