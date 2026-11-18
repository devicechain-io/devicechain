// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// What the provider does with the three second-factor session events:
//   verifyMfa       finishes a held sign-in and stores it exactly as login's result is;
//   adoptIdentity   swaps in the replacement sign-in turning two-factor on hands back (the one
//                   that enrolled has just ended), and renews the tenant session beside it;
//   forgetSessions  drops everything locally WITHOUT calling the server, which has just ended
//                   the sessions itself.

import { act, cleanup, render } from '@testing-library/react';
import { gql, GraphQLRequestError, resolveAuthToken, type TypedDocument } from '@devicechain/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  verifyMfa: vi.fn(),
  stepUp: vi.fn(),
  selectTenant: vi.fn(),
  logout: vi.fn(),
  refresh: vi.fn(),
}));

vi.mock('@/lib/api/user-management', () => ({
  login: vi.fn(),
  stepUp: (...a: unknown[]) => h.stepUp(...a),
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
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('HTTP 401 session recovery', () => {
  const query = 'query { test }' as unknown as TypedDocument<{ test: boolean }, Record<string, never>>;

  beforeEach(() => {
    window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: ACCESS, refreshToken: REFRESH }));
    window.localStorage.setItem('dc-identity', JSON.stringify({ identityToken: OLD_ID, superuser: false, memberships: [] }));
  });

  it('refreshes an unexpired tenant token once and replays without signing out', async () => {
    const next = { accessToken: jwt({ tenant: 'acme', exp: future, jti: 'fresh' }), refreshToken: REFRESH };
    h.refresh.mockResolvedValue(next);
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response('', { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { test: true } })));
    vi.stubGlobal('fetch', fetcher);
    mount();
    await act(async () => { expect(await gql('device-management', query)).toEqual({ test: true }); });
    expect(h.refresh).toHaveBeenCalledExactlyOnceWith(REFRESH);
    expect(auth.isAuthenticated).toBe(true);
    expect(stored('dc-auth')).toEqual(next);
  });

  it('clears the tenant session with the expired reason after the replay is refused', async () => {
    h.refresh.mockResolvedValue({ accessToken: ACCESS, refreshToken: REFRESH });
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })));
    mount();
    await act(async () => { await expect(gql('device-management', query)).rejects.toMatchObject({ status: 401 }); });
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(auth.isAuthenticated).toBe(false);
    expect(stored('dc-auth')).toBeNull();
    expect(consumeSessionExpired()).toBe('expired');
    expect(auth.isIdentityAuthenticated).toBe(true);
  });

  it('ends a rejected identity session without attempting a tenant refresh', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })));
    mount();
    await act(async () => { await expect(gql('user-management/admin', query)).rejects.toMatchObject({ status: 401 }); });
    expect(h.refresh).not.toHaveBeenCalled();
    expect(auth.isIdentityAuthenticated).toBe(false);
    expect(consumeSessionExpired()).toBe('expired');
    expect(auth.isAuthenticated).toBe(true);
  });
});

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
    ['a different tenant', jwt({ tenant: 'other', username: 'alice', exp: future }), false],
    ['a different username', jwt({ tenant: 'acme', username: 'bob', exp: future }), false],
    ['an undecodable access token', 'invalid', false],
    ['an undecodable own access token', freshAccess, true],
  ])('does not adopt a pair another tab stored for %s', async (_label, accessToken, invalidOwn) => {
    window.localStorage.setItem('dc-auth', JSON.stringify({
      accessToken: invalidOwn ? 'invalid' : jwt({ tenant: 'acme', username: 'alice', exp: past }),
      refreshToken: staleRefresh,
    }));
    const otherPair = JSON.stringify({ accessToken, refreshToken: freshRefresh });
    h.refresh.mockImplementation(async () => {
      window.localStorage.setItem('dc-auth', otherPair);
      throw coded('UNAUTHENTICATED');
    });
    mount();
    await act(async () => {
      await resolveAuthToken().catch(() => null);
    });
    expect(h.refresh).toHaveBeenCalledOnce();
    expect(auth.isAuthenticated).toBe(false);
    expect(auth.claims).toBeNull();
    expect(window.localStorage.getItem('dc-auth')).toBe(otherPair);
    expect(window.sessionStorage.getItem('dc-session-expired')).toBe('1');
  });

  it('adopts a refreshed pair for the same named user and tenant', async () => {
    window.localStorage.setItem('dc-auth', JSON.stringify({
      accessToken: jwt({ tenant: 'acme', username: 'alice', exp: past }),
      refreshToken: staleRefresh,
    }));
    const pair = {
      accessToken: jwt({ tenant: 'acme', username: 'alice', exp: future }),
      refreshToken: freshRefresh,
    };
    h.refresh.mockImplementation(async () => {
      window.localStorage.setItem('dc-auth', JSON.stringify(pair));
      throw coded('UNAUTHENTICATED');
    });
    mount();
    await act(async () => {
      await resolveAuthToken().catch(() => null);
    });
    expect(auth.isAuthenticated).toBe(true);
    expect(auth.claims).toMatchObject({ tenant: 'acme', username: 'alice' });
    expect(stored('dc-auth')).toEqual(pair);
    expect(window.sessionStorage.getItem('dc-session-expired')).toBeNull();
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

describe('in-session stepUp', () => {
  beforeEach(() => {
    window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: ACCESS, refreshToken: REFRESH }));
    window.localStorage.setItem('dc-identity', JSON.stringify({ identityToken: OLD_ID, superuser: false, memberships: [] }));
  });
  it('uses the stored refresh token and persists both new tokens without signing out', async () => {
    const pair = { accessToken: jwt({ tenant: 'acme', exp: future, mfa_time: future - 3600 }),
      refreshToken: jwt({ tenant: 'acme', exp: future, jti: 'rotated' }) };
    h.stepUp.mockResolvedValue(pair);
    mount();
    await act(async () => { await auth.stepUp('123456'); });
    expect(h.stepUp).toHaveBeenCalledExactlyOnceWith(REFRESH, '123456');
    expect(stored('dc-auth')).toEqual(pair);
    expect(stored('dc-identity').identityToken).toBe(OLD_ID);
    expect(await resolveAuthToken()).toBe(pair.accessToken);
    expect(h.logout).not.toHaveBeenCalled();
  });
  it('leaves the session in place when the code is refused', async () => {
    h.stepUp.mockRejectedValue(new Error('invalid code'));
    mount();
    await act(async () => { await expect(auth.stepUp('123456')).rejects.toThrow('invalid code'); });
    expect(stored('dc-auth')).toEqual({ accessToken: ACCESS, refreshToken: REFRESH });
    expect(auth.isAuthenticated).toBe(true);
    expect(h.logout).not.toHaveBeenCalled();
  });

  // 🔴 A 401 forces a refresh with refresh token R while the access token is still valid; the
  // user then submits a correct code. Refresh tokens rotate, and the server honours one use of
  // R. Step-up used to wait only for an ordinary refresh, so it spent R alongside the forced
  // one: the forced refresh was refused, which ended the session, and the step-up's pair then
  // arrived for a session that was gone. Signed out after a correct code.
  it('shares the refresh token with a forced refresh after a 401, and stays signed in', async () => {
    const live = new Set([REFRESH]);
    let n = 0;
    const issue = (rt: string) => {
      if (!live.delete(rt)) {
        throw new GraphQLRequestError('invalid refresh token', 200, [{ message: 'invalid refresh token' }]);
      }
      n++;
      const next = { accessToken: jwt({ tenant: 'acme', exp: future, jti: `a${n}` }),
        refreshToken: jwt({ tenant: 'acme', exp: future, jti: `r${n}` }) };
      live.add(next.refreshToken);
      return next;
    };
    let openRefresh!: () => void;
    const refreshGate = new Promise<void>((r) => (openRefresh = r));
    let openStepUp!: () => void;
    const stepUpGate = new Promise<void>((r) => (openStepUp = r));
    // The forced refresh is slow to reach the server; the step-up is claimed there at once and
    // slow to answer, so the refusal of whichever loses arrives first.
    h.refresh.mockImplementation(async (rt: string) => { await refreshGate; return issue(rt); });
    h.stepUp.mockImplementation(async (rt: string) => { const pair = issue(rt); await stepUpGate; return pair; });
    const query = 'query { test }' as unknown as TypedDocument<{ test: boolean }, Record<string, never>>;
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(new Response('', { status: 401 }))
      .mockResolvedValue(new Response(JSON.stringify({ data: { test: true } }))));
    mount();

    let request!: Promise<unknown>;
    let stepped!: Promise<unknown>;
    await act(async () => {
      request = gql('device-management', query);
      await vi.waitFor(() => expect(h.refresh).toHaveBeenCalledTimes(1));
      stepped = auth.stepUp('123456');
      await Promise.resolve();
      openRefresh();
      await vi.waitFor(() => expect(h.stepUp).toHaveBeenCalled());
      openStepUp();
      await Promise.allSettled([request, stepped]);
    });

    await expect(stepped).resolves.toBeUndefined();
    await expect(request).resolves.toEqual({ test: true });
    expect(h.refresh).toHaveBeenCalledExactlyOnceWith(REFRESH);
    // The step-up spent the token the forced refresh left, never R a second time.
    expect(h.stepUp).toHaveBeenCalledTimes(1);
    expect(h.stepUp.mock.calls[0][0]).not.toBe(REFRESH);
    expect(auth.isAuthenticated).toBe(true);
    expect(consumeSessionExpired()).toBeNull();
    expect(live.has(stored('dc-auth').refreshToken)).toBe(true);
    expect(stored('dc-auth').accessToken).toBe(jwt({ tenant: 'acme', exp: future, jti: 'a2' }));
  });
});
