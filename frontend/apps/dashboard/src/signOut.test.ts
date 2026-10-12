// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// A plain recording function rather than vi.fn(): vitest reports a spy whose last call
// threw as a failure of the test, which is exactly what these tests provoke on purpose.
const h = vi.hoisted(() => ({
  calls: [] as unknown[][],
  impl: (async () => undefined) as (...args: unknown[]) => Promise<unknown>,
}));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: (...args: unknown[]) => {
    h.calls.push(args);
    return h.impl(...args);
  },
}));

const { endServerSession } = await import('./signOut');
const { LOGOUT, SELECT_TENANT } = await import('./queries');

beforeEach(() => {
  h.calls = [];
});
afterEach(() => vi.restoreAllMocks());

describe('endServerSession', () => {
  it('sends the refresh token to the logout mutation, anonymously', async () => {
    h.impl = async () => ({ logout: { providerSignOutUrl: null } });
    expect(await endServerSession('rt-1')).toEqual({ ended: true, providerSignOut: null });
    expect(h.calls).toHaveLength(1);
    const [area, doc, vars, opts] = h.calls[0];
    expect(area).toBe('user-management');
    expect(doc).toBe(LOGOUT);
    expect(vars).toEqual({ token: 'rt-1' });
    expect(opts).toEqual({ anonymous: true });
  });

  it('reports a failure, and logs the cause rather than swallowing it', async () => {
    const log = vi.spyOn(console, 'error').mockImplementation(() => {});
    h.impl = async () => {
      throw new Error('Failed to fetch');
    };
    expect(await endServerSession('rt-1')).toEqual({ ended: false, providerSignOut: null });
    expect(log).toHaveBeenCalledTimes(1);
  });

  it('reports a server that answers without confirming', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    h.impl = async () => ({ logout: null });
    expect(await endServerSession('rt-1')).toEqual({ ended: false, providerSignOut: null });
  });

  it('hands back the provider sign-out address the server answered', async () => {
    const at = 'https://idp.example.com/logout?client_id=dc&post_logout_redirect_uri=https%3A%2F%2Fdc.example.com%2Fdash%2F';
    h.impl = async () => ({ logout: { providerSignOutUrl: at } });
    expect(await endServerSession('rt-1')).toEqual({ ended: true, providerSignOut: at });
  });

  it('drops a provider address no browser should be sent to', async () => {
    h.impl = async () => ({ logout: { providerSignOutUrl: 'javascript:alert(1)' } });
    expect(await endServerSession('rt-1')).toEqual({ ended: true, providerSignOut: null });
  });

  it('has nothing to end without a token, and does not call the server', async () => {
    expect(await endServerSession(null)).toEqual({ ended: true, providerSignOut: null });
    expect(h.calls).toHaveLength(0);
  });
});

describe('the documents', () => {
  // The viewer can only sign out server-side if sign-in handed it the refresh token.
  it('selectTenant asks for the refresh token', () => {
    expect(String(SELECT_TENANT)).toContain('refreshToken');
  });
  it('logout takes the token and names this app to come back to', () => {
    expect(String(LOGOUT)).toMatch(/logout\(token: \$token, returnTo: DASHBOARD\)/);
    expect(String(LOGOUT)).toContain('providerSignOutUrl');
  });
});
