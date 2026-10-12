// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The page the identity provider returns the browser to. Driven for real, StrictMode and
// all: the one-time code must come out of the address bar, be redeemed once, and a failure
// must end on a message rather than a retry.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import { resetSsoStateForTests } from '@/lib/sso';
import LoginSsoCompletePage from './LoginSsoComplete';

const completeSso = vi.fn();
const selectTenant = vi.fn();

const auth = vi.hoisted(() => ({ isAuthenticated: false }));

vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: auth.isAuthenticated, completeSso, selectTenant }),
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

// Every route the page could reach through the ROUTER renders a marker, so a client-side hop
// shows up as one: the page must leave by a full navigation (location.replace) instead,
// because this document forbids the Referer for its whole life and the console's maps need it.
function renderPage() {
  render(
    <StrictMode>
      <MemoryRouter initialEntries={['/login/sso/complete']}>
        <Routes>
          <Route path="/login/sso/complete" element={<LoginSsoCompletePage />} />
          <Route path="/" element={<div>routed: home</div>} />
          <Route path="/login" element={<div>routed: login screen</div>} />
          <Route path="/admin" element={<div>routed: admin home</div>} />
          <Route path="/alarms" element={<div>routed: alarms page</div>} />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

/** Nothing reached through the router: the completion page is still the one rendered. */
function expectNoRouterHop() {
  expect(screen.queryByText(/^routed:/)).toBeNull();
}

// location.replace cannot be spied on in place (jsdom makes it non-configurable), so the
// whole location is swapped for a view of the real one with replace faked. Everything
// else — search, pathname, href after history.replaceState — still reads the real thing.
const realLocation = window.location;
const replace = vi.fn();

beforeEach(async () => {
  completeSso.mockReset();
  selectTenant.mockReset();
  replace.mockReset();
  resetSsoStateForTests();
  seenAtRender.length = 0;
  window.sessionStorage.clear();
  document.head.querySelectorAll('meta[name="referrer"]').forEach((m) => m.remove());
  window.history.replaceState(null, '', '/login/sso/complete?code=ONETIME');
  Object.defineProperty(window, 'location', {
    configurable: true,
    // An empty target: a Proxy of the real Location may not answer differently for its
    // non-configurable replace.
    value: new Proxy({} as Location, {
      get: (_target, key) => {
        if (key === 'replace') return replace;
        const v: unknown = Reflect.get(realLocation, key, realLocation);
        return typeof v === 'function' ? v.bind(realLocation) : v;
      },
    }),
  });
  auth.isAuthenticated = false;
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
  window.history.replaceState(null, '', '/');
});

describe('LoginSsoCompletePage', () => {
  it('strips the code, sends no Referer, redeems once under StrictMode, and leaves for returnTo by a full navigation', async () => {
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

    await waitFor(() => expect(replace).toHaveBeenCalledWith('/alarms'));
    expect(replace).toHaveBeenCalledTimes(1);
    expectNoRouterHop();
    expect(seenAtRender.length).toBeGreaterThan(0);
    expect(seenAtRender.filter((href) => href.includes('ONETIME'))).toEqual([]);
    expect(completeSso).toHaveBeenCalledTimes(1);
    expect(completeSso).toHaveBeenCalledWith('ONETIME', 'VERIFIER');
    expect(selectTenant).toHaveBeenCalledWith('id', 'acme');
    // The tenant session was stored (selectTenant) BEFORE the page was thrown away.
    expect(selectTenant.mock.invocationCallOrder[0]).toBeLessThan(replace.mock.invocationCallOrder[0]);
  });

  it('sends a superuser to the admin console by a full navigation', async () => {
    seedPending();
    completeSso.mockResolvedValue({ identityToken: 'id', superuser: true, memberships: [] });
    renderPage();
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/admin'));
    expectNoRouterHop();
    expect(selectTenant).not.toHaveBeenCalled();
  });

  it('sends someone already signed in home by a full navigation, without spending the code', async () => {
    seedPending();
    auth.isAuthenticated = true;
    renderPage();
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/'));
    expect(replace).toHaveBeenCalledTimes(1);
    expectNoRouterHop();
    expect(completeSso).not.toHaveBeenCalled();
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
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/alarms'));
    expectNoRouterHop();
  });

  it('shows an error and a way back when the redeem is refused, and does not retry', async () => {
    seedPending();
    completeSso.mockRejectedValue(
      new GraphQLRequestError('invalid or expired sign-in code', 200, [{ message: 'x' }]),
    );
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorRedeemFailed'))).toBeTruthy();
    expect(completeSso).toHaveBeenCalledTimes(1);
    expect(selectTenant).not.toHaveBeenCalled();
    // The failure stays on screen (nothing navigates by itself)...
    expect(replace).not.toHaveBeenCalled();
    // ...and the way back is a full navigation too, not the router.
    fireEvent.click(screen.getByRole('button', { name: t('login:ssoBackToSignIn') }));
    expect(replace).toHaveBeenCalledWith('/login');
    expectNoRouterHop();
    expect(completeSso).toHaveBeenCalledTimes(1);
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
    expect(replace).not.toHaveBeenCalled();
  });
});

// A return from linking one's OWN account (beginSsoLink) lands on the same page with
// `linked=1` or `error=` and no code. The person is signed in already, so nothing is
// redeemed and nothing is navigated until they choose to continue.
describe('LoginSsoCompletePage: the return from linking an account', () => {
  const markLinkPending = () => window.sessionStorage.setItem('dc-sso-link-pending', '1');

  it('says the account is linked, redeems nothing, and continues home on request', async () => {
    window.history.replaceState(null, '', '/login/sso/complete?linked=1');
    auth.isAuthenticated = true;
    renderPage();
    expect(await screen.findByText(t('login:ssoLinkDone'))).toBeTruthy();
    expect(completeSso).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: t('login:ssoLinkContinue') }));
    expect(replace).toHaveBeenCalledWith('/');
  });

  it.each(['en', 'es'])('names what to check when the server refuses the link, in %s', async (lng) => {
    await i18n.changeLanguage(lng);
    window.history.replaceState(null, '', '/login/sso/complete?error=refused');
    markLinkPending();
    auth.isAuthenticated = true;
    renderPage();
    expect(await screen.findByText(i18n.t('login:ssoLinkRefused'))).toBeTruthy();
    expect(i18n.t('login:ssoLinkRefused')).not.toBe('ssoLinkRefused');
    expect(screen.queryByText(i18n.t('login:ssoErrorRefused'))).toBeNull();
    expect(completeSso).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
  });

  it('reports a provider failure during a link as that', async () => {
    window.history.replaceState(null, '', '/login/sso/complete?error=idp_error');
    markLinkPending();
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorIdp'))).toBeTruthy();
  });

  // The server names a link's failed check on the return address. Each reason has its own text
  // in both languages, and is read as a link's even when this tab lost its marker.
  const reasons: [string, string][] = [
    ['link_email_mismatch', 'login:ssoLinkErrorEmailMismatch'],
    ['link_unverified_email', 'login:ssoLinkErrorUnverifiedEmail'],
    ['link_session_ended', 'login:ssoLinkErrorSessionEnded'],
    ['link_already_linked', 'login:ssoLinkErrorAlreadyLinked'],
    ['link_not_allowed', 'login:ssoLinkErrorNotAllowed'],
  ];
  describe.each(['en', 'es'])('in %s', (lng) => {
    it.each(reasons)('says what %s means, with or without the marker', async (reason, key) => {
      await i18n.changeLanguage(lng);
      expect(i18n.t(key)).not.toBe(key.split(':')[1]);
      window.history.replaceState(null, '', `/login/sso/complete?error=${reason}`);
      auth.isAuthenticated = true;
      renderPage();
      expect(await screen.findByText(i18n.t(key))).toBeTruthy();
      expect(screen.queryByText(i18n.t('login:ssoLinkRefused'))).toBeNull();
      expect(completeSso).not.toHaveBeenCalled();
    });
  });

  it("keeps a sign-in's link_required a sign-in's", async () => {
    window.history.replaceState(null, '', '/login/sso/complete?error=link_required');
    seedPending();
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorLinkRequired'))).toBeTruthy();
    expect(screen.queryByText(t('login:ssoLinkContinue'))).toBeNull();
  });

  it('still reads an error with no link in progress as a sign-in refusal', async () => {
    window.history.replaceState(null, '', '/login/sso/complete?error=refused');
    seedPending();
    renderPage();
    expect(await screen.findByText(t('login:ssoErrorRefused'))).toBeTruthy();
    expect(screen.queryByText(t('login:ssoLinkRefused'))).toBeNull();
  });
});
