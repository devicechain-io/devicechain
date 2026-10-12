// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The sign-out call: it names the console as the app to come back to, hands back the
// provider address the server answered, and never hands back something a browser should
// not be sent to.

import { beforeEach, describe, expect, it, vi } from 'vitest';

const { gqlMock } = vi.hoisted(() => ({ gqlMock: vi.fn() }));
vi.mock('@devicechain/client', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  gql: gqlMock,
}));

import { logout, providerSignOutTarget } from './user-management';

beforeEach(() => gqlMock.mockReset());

describe('logout', () => {
  it('returns the provider address the server answered', async () => {
    const at = 'https://idp.example.com/logout?client_id=dc';
    gqlMock.mockResolvedValue({ logout: { providerSignOutUrl: at } });
    await expect(logout('tok')).resolves.toBe(at);
    expect(gqlMock).toHaveBeenCalledWith('user-management', expect.anything(), { token: 'tok' }, { anonymous: true });
  });

  it('returns null when there is nothing more to do', async () => {
    gqlMock.mockResolvedValue({ logout: { providerSignOutUrl: null } });
    await expect(logout('tok')).resolves.toBeNull();
  });

  it('fails when the server did not confirm', async () => {
    gqlMock.mockResolvedValue({ logout: null });
    await expect(logout('tok')).rejects.toThrow();
  });
});

describe('providerSignOutTarget', () => {
  it('keeps an absolute http(s) address', () => {
    expect(providerSignOutTarget('https://idp.example.com/logout?a=1')).toBe('https://idp.example.com/logout?a=1');
    expect(providerSignOutTarget('http://127.0.0.1:5556/logout')).toBe('http://127.0.0.1:5556/logout');
  });

  it.each(['javascript:alert(1)', 'data:text/html,x', '/login', '//evil.example/x', 'not a url', '', null])(
    'drops %s',
    (raw) => {
      expect(providerSignOutTarget(raw)).toBeNull();
    },
  );
});
