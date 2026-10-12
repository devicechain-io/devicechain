// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The page the identity provider returns the browser to. Driven for real, StrictMode and
// all: the one-time code must come out of the address bar, be redeemed once, and a failure
// must end on a message rather than a retry.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { resetSsoStateForTests } from '@/lib/sso';
import LoginSsoCompletePage from './LoginSsoComplete';

const completeSso = vi.fn();
const selectTenant = vi.fn();

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, completeSso, selectTenant }),
}));

// What the address bar held while the page's first child rendered. Effects run AFTER the
// render, so this is the one observation that tells "stripped before anything renders"
// from "stripped by the redeem effect a moment later" (RTL flushes both inside render()).
const seenAtRender: string[] = [];
vi.mock('@/components/ui/loading-state', () => ({
  LoadingState: ({ description }: { description?: string }) => {
    seenAtRender.push(window.location.href);
    return <div>{description}</div>;
  },
}));

const t = (key: string) => i18n.t(key);

function seedPending(returnTo = '/alarms') {
  window.sessionStorage.setItem('dc-sso-pending', JSON.stringify({ verifier: 'VERIFIER', returnTo }));
}

function renderPage() {
  render(
    <StrictMode>
      <MemoryRouter initialEntries={['/login/sso/complete']}>
        <Routes>
          <Route path="/login/sso/complete" element={<LoginSsoCompletePage />} />
          <Route path="/login" element={<div>login screen</div>} />
          <Route path="/admin" element={<div>admin home</div>} />
          <Route path="/alarms" element={<div>alarms page</div>} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

beforeEach(async () => {
  completeSso.mockReset();
  selectTenant.mockReset();
  resetSsoStateForTests();
  seenAtRender.length = 0;
  window.sessionStorage.clear();
  document.head.querySelectorAll('meta[name="referrer"]').forEach((m) => m.remove());
  window.history.replaceState(null, '', '/login/sso/complete?code=ONETIME');
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  window.history.replaceState(null, '', '/');
});

describe('LoginSsoCompletePage', () => {
  it('strips the code, sends no Referer, redeems once under StrictMode, and routes to returnTo', async () => {
    seedPending('/alarms');
    completeSso.mockResolvedValue({
      identityToken: 'id',
      superuser: false,
      memberships: [{ tenant: 'acme', roles: [] }],
    });
    selectTenant.mockResolvedValue(undefined);

    renderPage();
    // Gone from the address bar as soon as the page has rendered, before the redeem returns.
    expect(window.location.search).toBe('');
    expect(window.location.href).not.toContain('ONETIME');
    expect(document.head.querySelector('meta[name="referrer"]')?.getAttribute('content')).toBe(
      'no-referrer',
    );

    expect(await screen.findByText('alarms page')).toBeTruthy();
    expect(seenAtRender.length).toBeGreaterThan(0);
    expect(seenAtRender.filter((href) => href.includes('ONETIME'))).toEqual([]);
    expect(completeSso).toHaveBeenCalledTimes(1);
    expect(completeSso).toHaveBeenCalledWith('ONETIME', 'VERIFIER');
    expect(selectTenant).toHaveBeenCalledWith('id', 'acme');
  });

  it('sends a superuser to the admin console', async () => {
    seedPending();
    completeSso.mockResolvedValue({ identityToken: 'id', superuser: true, memberships: [] });
    renderPage();
    expect(await screen.findByText('admin home')).toBeTruthy();
    expect(selectTenant).not.toHaveBeenCalled();
  });

  it('offers a choice when the identity has several tenants', async () => {
    seedPending();
    completeSso.mockResolvedValue({
      identityToken: 'id',
      superuser: false,
      memberships: [
        { tenant: 'acme', roles: [] },
        { tenant: 'globex', roles: [] },
      ],
    });
    selectTenant.mockResolvedValue(undefined);
    renderPage();
    (await screen.findByRole('button', { name: 'globex' })).click();
    await waitFor(() => expect(selectTenant).toHaveBeenCalledWith('id', 'globex'));
    expect(await screen.findByText('alarms page')).toBeTruthy();
  });

  it('shows an error and a way back when the redeem is refused, and does not retry', async () => {
    seedPending();
    completeSso.mockRejectedValue(
      new GraphQLRequestError('invalid or expired sign-in code', 200, [{ message: 'x' }]),
    );
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorRedeemFailed'))).toBeTruthy();
    expect(screen.getByRole('link', { name: t('login:ssoBackToSignIn') })).toBeTruthy();
    expect(completeSso).toHaveBeenCalledTimes(1);
    expect(selectTenant).not.toHaveBeenCalled();
  });

  it('reports an unreachable server as that, not as a spent code', async () => {
    seedPending();
    completeSso.mockRejectedValue(new GraphQLRequestError('Failed to fetch', 0));
    renderPage();
    expect(await screen.findByText(t('login:serverUnreachable'))).toBeTruthy();
  });

  it('names a missing verifier and never redeems', async () => {
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorMissingVerifier'))).toBeTruthy();
    expect(completeSso).not.toHaveBeenCalled();
  });

  it('shows the provider-side refusal the server named', async () => {
    seedPending();
    window.history.replaceState(null, '', '/login/sso/complete?error=link_required');
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorLinkRequired'))).toBeTruthy();
    expect(completeSso).not.toHaveBeenCalled();
  });

  it('says so when a tenant refuses the identity', async () => {
    seedPending();
    completeSso.mockResolvedValue({
      identityToken: 'id',
      superuser: false,
      memberships: [{ tenant: 'acme', roles: [] }],
    });
    selectTenant.mockRejectedValue(new GraphQLRequestError('invalid credentials', 200, [{ message: 'x' }]));
    renderPage();
    expect(await screen.findByText(t('login:enterTenantFailed'))).toBeTruthy();
  });
});
