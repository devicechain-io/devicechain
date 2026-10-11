// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Signing out must end the sign-in on the SERVER, not only forget the tokens here: the
// refresh token a browser has stored is good for days, so clearing localStorage alone
// leaves anyone who copied it able to keep minting access tokens. These drive the real
// AuthProvider and press the real logout the menus call.

import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({
  logoutCalls: [] as string[],
  logoutImpl: (async (_t: string) => {}) as (t: string) => Promise<void>,
  toasts: [] as Array<[string, string | undefined]>,
}));

vi.mock('@/lib/api/user-management', () => ({
  login: vi.fn(),
  selectTenant: vi.fn(),
  refresh: vi.fn(),
  getIdentityMemberships: vi.fn(),
  logout: (t: string) => {
    h.logoutCalls.push(t);
    return h.logoutImpl(t);
  },
}));
vi.mock('@/components/ui/toast', () => ({
  useToast: () => ({ toast: (m: string, v?: string) => h.toasts.push([m, v]) }),
}));

import { AuthProvider, useAuth } from './AuthProvider';

// An unsigned JWT-shaped string: the provider only decodes it, and logout never verifies.
const jwt = (claims: Record<string, unknown>) =>
  `h.${btoa(JSON.stringify(claims)).replace(/=+$/, '')}.s`;
const future = Math.floor(Date.now() / 1000) + 3600;
const ACCESS = jwt({ tenant: 'acme', exp: future });
const REFRESH = jwt({ tenant: 'acme', exp: future, jti: 'r' });
const IDENTITY = jwt({ exp: future, jti: 'i' });

let auth: ReturnType<typeof useAuth>;
function Grab() {
  auth = useAuth();
  return null;
}

function mount() {
  render(
    <AuthProvider>
      <Grab />
    </AuthProvider>,
  );
}

beforeEach(() => {
  h.logoutCalls = [];
  h.toasts = [];
  h.logoutImpl = async () => {};
  window.localStorage.clear();
  window.localStorage.setItem('dc-auth', JSON.stringify({ accessToken: ACCESS, refreshToken: REFRESH }));
  window.localStorage.setItem(
    'dc-identity',
    JSON.stringify({ identityToken: IDENTITY, superuser: false, memberships: [] }),
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('AuthProvider.logout', () => {
  it('calls the logout mutation with the refresh token and the identity token, then clears local state', async () => {
    mount();
    expect(auth.isAuthenticated).toBe(true);

    await act(async () => {
      await auth.logout();
    });

    expect(h.logoutCalls.sort()).toEqual([IDENTITY, REFRESH].sort());
    expect(auth.isAuthenticated).toBe(false);
    expect(window.localStorage.getItem('dc-auth')).toBeNull();
    expect(window.localStorage.getItem('dc-identity')).toBeNull();
    expect(h.toasts).toEqual([]);
  });

  it('asks the server BEFORE it clears local state', async () => {
    mount();
    let storedWhenCalled: string | null = 'unset';
    h.logoutImpl = async () => {
      storedWhenCalled = window.localStorage.getItem('dc-auth');
    };
    await act(async () => {
      await auth.logout();
    });
    expect(storedWhenCalled).not.toBeNull();
    expect(window.localStorage.getItem('dc-auth')).toBeNull();
  });

  it('still clears local state when the server cannot be reached, and says so', async () => {
    const log = vi.spyOn(console, 'error').mockImplementation(() => {});
    mount();
    h.logoutImpl = async () => {
      throw new Error('Failed to fetch');
    };

    await act(async () => {
      await auth.logout();
    });

    expect(auth.isAuthenticated).toBe(false);
    expect(window.localStorage.getItem('dc-auth')).toBeNull();
    expect(window.localStorage.getItem('dc-identity')).toBeNull();
    expect(log).toHaveBeenCalled();
    expect(h.toasts).toHaveLength(1);
    expect(h.toasts[0][1]).toBe('error');
  });

  it('with nothing held it does not call the server', async () => {
    window.localStorage.clear();
    mount();
    await act(async () => {
      await auth.logout();
    });
    expect(h.logoutCalls).toEqual([]);
  });
});
