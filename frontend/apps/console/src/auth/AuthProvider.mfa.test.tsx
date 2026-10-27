// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the provider does with the three second-factor session events:
//   verifyMfa       finishes a held sign-in and stores it exactly as login's result is;
//   adoptIdentity   swaps in the replacement sign-in turning two-factor on hands back (the one
//                   that enrolled has just ended), and renews the tenant session beside it;
//   forgetSessions  drops everything locally WITHOUT calling the server, which has just ended
//                   the sessions itself.

import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  verifyMfa: vi.fn(),
  selectTenant: vi.fn(),
  logout: vi.fn(),
}));

vi.mock('@/lib/api/user-management', () => ({
  login: vi.fn(),
  verifyMfa: (...a: unknown[]) => h.verifyMfa(...a),
  selectTenant: (...a: unknown[]) => h.selectTenant(...a),
  refresh: vi.fn(),
  getIdentityMemberships: vi.fn(),
  logout: (...a: unknown[]) => h.logout(...a),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));

import { AuthProvider, useAuth } from './AuthProvider';

const jwt = (claims: Record<string, unknown>) => `h.${btoa(JSON.stringify(claims)).replace(/=+$/, '')}.s`;
const future = Math.floor(Date.now() / 1000) + 3600;
const ACCESS = jwt({ tenant: 'acme', exp: future });
const REFRESH = jwt({ tenant: 'acme', exp: future, jti: 'r' });
const OLD_ID = jwt({ exp: future, jti: 'old' });
const NEW_ID = jwt({ exp: future, jti: 'new' });

let auth: ReturnType<typeof useAuth>;
function Grab() {
  auth = useAuth();
  return null;
}
const mount = () =>
  render(
    <AuthProvider>
      <Grab />
    </AuthProvider>,
  );

const stored = (key: string) => JSON.parse(window.localStorage.getItem(key) ?? 'null');

beforeEach(() => {
  Object.values(h).forEach((m) => m.mockReset());
  window.localStorage.clear();
});
afterEach(cleanup);

describe('verifyMfa', () => {
  it('starts the identity session the way a password sign-in does', async () => {
    h.verifyMfa.mockResolvedValue({
      identityToken: NEW_ID,
      superuser: false,
      memberships: [{ tenant: 'acme', roles: [], ssoRequired: false }],
    });
    mount();
    await act(async () => {
      await auth.verifyMfa('chal', '123456');
    });
    expect(h.verifyMfa).toHaveBeenCalledWith('chal', '123456');
    expect(auth.isIdentityAuthenticated).toBe(true);
    expect(auth.signInMethod).toBe('password');
    expect(stored('dc-identity').identityToken).toBe(NEW_ID);
    // The challenge handle is not retained anywhere.
    expect(JSON.stringify({ ...window.localStorage })).not.toContain('chal');
  });
});

describe('adoptIdentity', () => {
  beforeEach(() => {
    window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: ACCESS, refreshToken: REFRESH }));
    window.localStorage.setItem(
      'dc-identity',
      JSON.stringify({ identityToken: OLD_ID, superuser: false, memberships: [], method: 'password' }),
    );
  });

  it('replaces the identity token and re-enters the tenant the person is in', async () => {
    const fresh = jwt({ tenant: 'acme', exp: future, jti: 'fresh' });
    h.selectTenant.mockResolvedValue({ accessToken: fresh, refreshToken: fresh });
    mount();
    await act(async () => {
      await auth.adoptIdentity({ identityToken: NEW_ID, expiresAt: '', superuser: false, memberships: [] });
    });
    expect(stored('dc-identity').identityToken).toBe(NEW_ID);
    expect(h.selectTenant).toHaveBeenCalledWith(NEW_ID, 'acme');
    expect(stored('dc-auth').accessToken).toBe(fresh);
  });

  it('keeps the new identity even when re-entering the tenant fails', async () => {
    h.selectTenant.mockRejectedValue(new Error('nope'));
    mount();
    await act(async () => {
      await auth.adoptIdentity({ identityToken: NEW_ID, expiresAt: '', superuser: false, memberships: [] });
    });
    expect(stored('dc-identity').identityToken).toBe(NEW_ID);
  });

  it('does not start a tenant session for a person who was not in one', async () => {
    window.localStorage.removeItem('dc-auth');
    mount();
    await act(async () => {
      await auth.adoptIdentity({ identityToken: NEW_ID, expiresAt: '', superuser: false, memberships: [] });
    });
    expect(h.selectTenant).not.toHaveBeenCalled();
    expect(stored('dc-auth')).toBeNull();
  });
});

describe('forgetSessions', () => {
  it('clears every local session without asking the server to end them', async () => {
    window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: ACCESS, refreshToken: REFRESH }));
    window.localStorage.setItem('dc-identity', JSON.stringify({ identityToken: OLD_ID, superuser: false, memberships: [] }));
    mount();
    expect(auth.isAuthenticated).toBe(true);
    act(() => auth.forgetSessions());
    expect(auth.isAuthenticated).toBe(false);
    expect(auth.isIdentityAuthenticated).toBe(false);
    expect(stored('dc-auth')).toBeNull();
    expect(stored('dc-identity')).toBeNull();
    expect(h.logout).not.toHaveBeenCalled();
  });
});
