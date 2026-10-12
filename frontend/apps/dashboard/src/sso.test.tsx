// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Single sign-on for the standalone viewer: the same promises as the console's, kept by
// this app's own copy of the logic. The transport (gql) is the only seam faked; the module
// and the real App run.

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', async (orig) => ({
  ...(await orig<typeof import('@devicechain/client')>()),
  gql: (...a: unknown[]) => h.gql(...a),
  setAuthTokenGetter: () => {},
}));

import App from './App';
import i18n from './i18n/config';
import { BEGIN_SSO, COMPLETE_SSO, LOGIN, SELECT_TENANT } from './queries';
import { SSO_COMPLETE_PATH, resetSsoStateForTests } from './sso';

const t = (key: string) => i18n.t(key);
const PENDING = 'dc-dash-sso-verifier';

function landOn(path: string) {
  window.history.replaceState(null, '', path);
}

// Route gql by document so one mock serves the whole flow.
function serve(handlers: Partial<Record<'begin' | 'complete' | 'select', (vars: any) => unknown>>) {
  h.gql.mockImplementation(async (_area: string, doc: unknown, vars: unknown) => {
    const pick =
      doc === BEGIN_SSO ? handlers.begin : doc === COMPLETE_SSO ? handlers.complete : doc === SELECT_TENANT ? handlers.select : undefined;
    if (!pick) throw new Error('unexpected document');
    return pick(vars);
  });
}

const rejected = (msg: string) => new GraphQLRequestError(msg, 200, [{ message: msg }]);

beforeEach(async () => {
  h.gql.mockReset();
  resetSsoStateForTests();
  window.sessionStorage.clear();
  document.head.querySelectorAll('meta[name="referrer"]').forEach((m) => m.remove());
  await i18n.changeLanguage('en');
});
afterEach(() => {
  cleanup();
  landOn('/');
});

describe('beginning a single sign-on', () => {
  const realLocation = window.location;
  const assign = vi.fn();
  beforeEach(() => {
    assign.mockReset();
    landOn('/dash/');
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, assign, origin: realLocation.origin, pathname: '/dash/' },
    });
  });
  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
  });

  const typeEmail = (v: string) =>
    fireEvent.change(screen.getByLabelText(t('signIn:emailLabel')), { target: { value: v } });
  const clickSso = () => fireEvent.click(screen.getByRole('button', { name: t('signIn:ssoButton') }));

  it('navigates to the server address as given, returning to /dash/sso/complete', async () => {
    const given = 'https://idp.example.com/authorize?state=a%2Fb#x';
    let sent: any;
    serve({
      begin: (vars) => {
        sent = vars;
        return { beginSso: { authorizationUrl: given } };
      },
    });
    render(<App />);
    typeEmail(' v@acme.com ');
    clickSso();
    await waitFor(() => expect(assign).toHaveBeenCalledWith(given));
    expect(sent.email).toBe('v@acme.com');
    expect(sent.returnTo).toBe(realLocation.origin + SSO_COMPLETE_PATH);
    expect(sent.codeChallenge).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(window.sessionStorage.getItem(PENDING)).toBeTruthy();
  });

  it('says so when no connection covers the email', async () => {
    serve({ begin: () => ({ beginSso: null }) });
    render(<App />);
    typeEmail('v@nowhere.com');
    clickSso();
    await screen.findByText(t('signIn:ssoNotConfigured'));
    expect(assign).not.toHaveBeenCalled();
    expect(window.sessionStorage.getItem(PENDING)).toBeNull();
  });

  it('asks for the email first', async () => {
    render(<App />);
    clickSso();
    await screen.findByText(t('signIn:ssoEmailRequired'));
    expect(h.gql).not.toHaveBeenCalled();
  });

  // A password sign-in: login lists the tenants (an SSO-requiring one marked) and the
  // server refuses selectTenant into a marked one with extensions.code SSO_REQUIRED.
  const SSO_REQUIRED = new GraphQLRequestError('x', 200, [{ message: 'x', extensions: { code: 'SSO_REQUIRED' } }]);
  const passwordSignIn = (memberships: { tenant: string; ssoRequired: boolean }[], select: () => unknown) => {
    h.gql.mockImplementation(async (_area: string, doc: unknown) => {
      if (doc === LOGIN) return { login: { identityToken: 'id', memberships: memberships.map((m) => ({ ...m, roles: [] })) } };
      if (doc === SELECT_TENANT) return select();
      throw new Error('unexpected document');
    });
    render(<App />);
    typeEmail('v@acme.com');
    fireEvent.submit(screen.getByRole('button', { name: t('signIn:submit') }));
  };
  const selectCalls = () => h.gql.mock.calls.filter((c) => c[1] === SELECT_TENANT).length;

  it('explains a selectTenant refusal from a single-sign-on tenant', async () => {
    passwordSignIn([{ tenant: 'acme', ssoRequired: false }], () => {
      throw SSO_REQUIRED;
    });
    await screen.findByText(t('signIn:ssoRequired'));
  });

  it('does not try to enter when every tenant requires single sign-on, and says why', async () => {
    passwordSignIn([{ tenant: 'acme', ssoRequired: true }], () => {
      throw new Error('must not be called');
    });
    await screen.findByText(t('signIn:ssoRequired'));
    expect(selectCalls()).toBe(0);
  });

  it('lists a tenant that requires single sign-on, marked and not choosable', async () => {
    passwordSignIn(
      [
        { tenant: 'acme', ssoRequired: true },
        { tenant: 'globex', ssoRequired: false },
      ],
      () => {
        throw new Error('must not be called');
      },
    );
    const acme = (await screen.findByRole('button', {
      name: i18n.t('signIn:ssoRequiredTenant', { tenant: 'acme' }),
    })) as HTMLButtonElement;
    expect(acme.disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'globex' }) as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(acme);
    expect(selectCalls()).toBe(0);
  });
});

describe('the return to /dash/sso/complete', () => {
  beforeEach(() => landOn(`${SSO_COMPLETE_PATH}?code=ONETIME`));

  function renderStrict() {
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    );
  }

  it('strips the code, sends no Referer, redeems once under StrictMode, and signs in', async () => {
    window.sessionStorage.setItem(PENDING, 'VERIFIER');
    serve({
      complete: () => ({
        completeSso: { identityToken: 'id', memberships: [{ tenant: 'acme', roles: [] }] },
      }),
      select: () => ({ selectTenant: { accessToken: 'a.b.c', refreshToken: 'r' } }),
    });
    renderStrict();
    expect(window.location.search).toBe('');
    expect(window.location.href).not.toContain('ONETIME');
    expect(document.head.querySelector('meta[name="referrer"]')?.getAttribute('content')).toBe(
      'no-referrer',
    );
    // Past the sign-in screens: the address is back at /dash/ once the return is consumed.
    await waitFor(() => expect(window.location.pathname).toBe('/dash/'));
    const completes = h.gql.mock.calls.filter((c) => c[1] === COMPLETE_SSO);
    expect(completes).toHaveLength(1);
    expect(completes[0][2]).toEqual({ code: 'ONETIME', codeVerifier: 'VERIFIER' });
    expect(h.gql.mock.calls.filter((c) => c[1] === SELECT_TENANT)).toHaveLength(1);
    expect(window.sessionStorage.getItem(PENDING)).toBeNull();
  });

  it('shows an error and a way back when the redeem fails, without retrying', async () => {
    window.sessionStorage.setItem(PENDING, 'VERIFIER');
    serve({
      complete: () => {
        throw rejected('invalid or expired sign-in code');
      },
    });
    renderStrict();
    await screen.findByText(t('signIn:ssoErrorRedeemFailed'));
    expect(h.gql.mock.calls.filter((c) => c[1] === COMPLETE_SSO)).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: t('signIn:ssoBack') }));
    await screen.findByLabelText(t('signIn:passwordLabel'));
    expect(h.gql.mock.calls.filter((c) => c[1] === COMPLETE_SSO)).toHaveLength(1);
  });

  it('names a missing verifier and never redeems', async () => {
    renderStrict();
    await screen.findByText(t('signIn:ssoErrorMissingVerifier'));
    expect(h.gql).not.toHaveBeenCalled();
  });

  it('shows the provider-side refusal the server named', async () => {
    window.sessionStorage.setItem(PENDING, 'VERIFIER');
    landOn(`${SSO_COMPLETE_PATH}?error=idp_error`);
    renderStrict();
    await screen.findByText(t('signIn:ssoErrorIdp'));
    expect(h.gql).not.toHaveBeenCalled();
  });
});
