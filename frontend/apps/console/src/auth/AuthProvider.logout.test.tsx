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
  logoutImpl: (async (_t: string) => null) as (t: string) => Promise<string | null>,
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
  h.logoutImpl = async () => null;
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

  // A tab closed straight after the click must not leave the tokens behind: storage is
  // cleared before the server call starts, not after it settles.
  it('clears local state BEFORE it waits on the server', async () => {
    mount();
    let storedWhenCalled: Array<string | null> = ['unset'];
    const releases: Array<() => void> = [];
    h.logoutImpl = () => {
      storedWhenCalled = [
        window.localStorage.getItem('dc-auth'),
        window.localStorage.getItem('dc-identity'),
      ];
      return new Promise<string | null>((r) => releases.push(() => r(null)));
    };
    let done: Promise<void> = Promise.resolve();
    act(() => {
      done = auth.logout();
    });
    expect(storedWhenCalled).toEqual([null, null]);
    expect(h.logoutCalls.sort()).toEqual([IDENTITY, REFRESH].sort());
    await act(async () => {
      releases.forEach((r) => r());
      await done;
    });
    expect(auth.isAuthenticated).toBe(false);
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

  // Signing out at the identity provider: only after the server confirmed the sign-out
  // here, and only to the address the server answered.
  describe('at the identity provider', () => {
    const realLocation = window.location;
    const assign = vi.fn();
    beforeEach(() => {
      assign.mockReset();
      Object.defineProperty(window, 'location', {
        configurable: true,
        value: { ...realLocation, assign, origin: realLocation.origin },
      });
    });
    afterEach(() => {
      Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
    });

    it('sends the browser to the provider address the server answered, after clearing local state', async () => {
      const at = 'https://idp.example.com/logout?client_id=dc&post_logout_redirect_uri=https%3A%2F%2Fdc.example.com%2Flogin';
      // One of the two calls reached the sign-in first and answered; the other found it gone.
      h.logoutImpl = async (t) => (t === REFRESH ? at : null);
      mount();
      await act(async () => {
        await auth.logout();
      });
      expect(window.localStorage.getItem('dc-auth')).toBeNull();
      expect(assign).toHaveBeenCalledTimes(1);
      expect(assign).toHaveBeenCalledWith(at);
    });

    it('stays here when the server answers no provider address', async () => {
      mount();
      await act(async () => {
        await auth.logout();
      });
      expect(assign).not.toHaveBeenCalled();
    });

    it('does not leave for the provider when the server did not confirm the sign-out', async () => {
      vi.spyOn(console, 'error').mockImplementation(() => {});
      h.logoutImpl = async (t) => {
        if (t === REFRESH) throw new Error('Failed to fetch');
        return 'https://idp.example.com/logout';
      };
      mount();
      await act(async () => {
        await auth.logout();
      });
      expect(assign).not.toHaveBeenCalled();
      expect(h.toasts).toHaveLength(1);
    });
  });
});
