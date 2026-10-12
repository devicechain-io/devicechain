// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The Identity providers routes sit behind the superuser guard.
//
// This renders the REAL route table (App), so it proves the routes are registered under
// AdminProtectedRoute and not merely that the guard works in isolation: a page added next
// to the guard instead of inside it would pass a test of the guard alone.

import '@/i18n/config';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { auth, listIdpConnections } = vi.hoisted(() => ({
  auth: {
    value: {
      isLoading: false,
      isAuthenticated: false,
      isIdentityAuthenticated: false,
      superuser: false,
    },
  },
  listIdpConnections: vi.fn(),
}));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ ...auth.value, login: vi.fn(), selectTenant: vi.fn(), claims: null }),
  consumeSessionExpired: () => false,
}));
vi.mock('@/lib/api/identity-providers', () => ({
  listIdpConnections: (...a: unknown[]) => listIdpConnections(...a),
  listSsoLinkRequests: () => Promise.resolve([]),
}));

import App from '@/App';

beforeEach(() => {
  listIdpConnections.mockReset();
  listIdpConnections.mockResolvedValue([]);
});
afterEach(cleanup);

function visit(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

describe('the identity-providers routes', () => {
  it.each([
    ['a signed-out visitor', { isIdentityAuthenticated: false, superuser: false }],
    ['an identity that is not a superuser', { isIdentityAuthenticated: true, superuser: false }],
  ])('send %s to the login page without loading anything', async (_who, state) => {
    auth.value = { ...auth.value, ...state };
    visit('/admin/identity-providers');
    // The login page has a password "Sign in" and a "Sign in with SSO" button; match the first exactly.
    expect(await screen.findByRole('button', { name: /^sign in$/i })).toBeTruthy();
    expect(screen.queryByText('Identity providers')).toBeNull();
    expect(listIdpConnections).not.toHaveBeenCalled();
  });

  it('guards the detail route the same way', async () => {
    auth.value = { ...auth.value, isIdentityAuthenticated: true, superuser: false };
    visit('/admin/identity-providers/acme-sso');
    expect(await screen.findByRole('button', { name: /^sign in$/i })).toBeTruthy();
    expect(listIdpConnections).not.toHaveBeenCalled();
  });
});
