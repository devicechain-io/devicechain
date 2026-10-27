// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the provider does with the three second-factor session events:
//   verifyMfa       finishes a held sign-in and stores it exactly as login's result is;
//   adoptIdentity   swaps in the replacement sign-in turning two-factor on hands back (the one
//                   that enrolled has just ended), and renews the tenant session beside it;
//   forgetSessions  drops everything locally WITHOUT calling the server, which has just ended
//                   the sessions itself.

import { act, cleanup, render } from '@testing-library/react';
import { GraphQLRequestError, resolveAuthToken } from '@devicechain/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  verifyMfa: vi.fn(),
  selectTenant: vi.fn(),
  logout: vi.fn(),
  refresh: vi.fn(),
}));

vi.mock('@/lib/api/user-management', () => ({
  login: vi.fn(),
  verifyMfa: (...a: unknown[]) => h.verifyMfa(...a),
  selectTenant: (...a: unknown[]) => h.selectTenant(...a),
  refresh: (...a: unknown[]) => h.refresh(...a),
  getIdentityMemberships: vi.fn(),
  logout: (...a: unknown[]) => h.logout(...a),
}));
vi.mock('@/components/ui/toast', () => ({ useToast: () => ({ toast: vi.fn() }) }));

import { AuthProvider, consumeSessionExpired, useAuth } from './AuthProvider';

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

// Two tabs share one localStorage. Turning two-factor on in tab A ends every other session and
// swaps in a fresh pair; tab B, still holding the OLD pair, then fails its refresh. That failure
// must not delete tab A's fresh pair, and the reason a refresh was refused must reach the sign-in
// screen.
describe('a session that can no longer be refreshed', () => {
  const past = Math.floor(Date.now() / 1000) - 600;
  const staleAccess = jwt({ tenant: 'acme', exp: past, iat: past - 900 });
  const staleRefresh = jwt({ tenant: 'acme', exp: future, jti: 'old' });
  const freshAccess = jwt({ tenant: 'acme', exp: future, iat: future - 900, jti: 'fresh-a' });
  const freshRefresh = jwt({ tenant: 'acme', exp: future, jti: 'fresh-r' });
  const coded = (code: string) => new GraphQLRequestError(code, 200, [{ message: code, extensions: { code } }]);

  beforeEach(() => {
    window.sessionStorage.clear();
    window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: staleAccess, refreshToken: staleRefresh }));
  });

  it('drops the stored pair when it is still the one that failed', async () => {
    h.refresh.mockRejectedValue(coded('SOMETHING'));
    mount();
    await act(async () => {
      await resolveAuthToken().catch(() => null);
    });
    expect(stored('dc-auth')).toBeNull();
    expect(auth.isAuthenticated).toBe(false);
  });

  it('does not delete a newer pair another tab stored, and adopts it', async () => {
    h.refresh.mockImplementation(async () => {
      // Tab A finishes enrolling while this tab's refresh is in flight.
      window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: freshAccess, refreshToken: freshRefresh }));
      throw coded('UNAUTHENTICATED');
    });
    mount();
    await act(async () => {
      await resolveAuthToken().catch(() => null);
    });
    expect(stored('dc-auth')).toEqual({ accessToken: freshAccess, refreshToken: freshRefresh });
    expect(auth.isAuthenticated).toBe(true);
  });

  it.each([
    ['MFA_REQUIRED', 'mfaRequired'],
    ['MFA_ENROLLMENT_REQUIRED', 'mfaEnrollmentRequired'],
    ['SSO_REQUIRED', 'ssoRequired'],
    ['OTHER', 'expired'],
  ])('tells the sign-in screen why: %s', async (code, reason) => {
    h.refresh.mockRejectedValue(coded(code));
    mount();
    await act(async () => {
      await resolveAuthToken().catch(() => null);
    });
    expect(consumeSessionExpired()).toBe(reason);
  });
});
