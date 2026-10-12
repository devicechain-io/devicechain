// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE WIRING HALF. signInError.test.ts proves the predicate; nothing there proves this
// SCREEN asks it. The defect being closed lived in Login.tsx and not in any helper — a
// one-line fold that reported every GraphQLRequestError as a bad password — so a suite
// that only tested a correct helper would have been green throughout the bug's life.
// These cases drive the real form and read the real banner.
//
// Both failure paths are covered, because they are two call sites with two different
// rejection messages and only one of them (step 1) is reachable without first signing in.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import LoginPage from './Login';

const login = vi.fn();
const selectTenant = vi.fn();
const startSso = vi.fn();

vi.mock('@/lib/sso', () => ({ startSso: (...a: unknown[]) => startSso(...a) }));

// The provider is the app's session machinery (token storage, refresh timers, GraphQL
// getters); none of it is this file's subject. Stubbed to the two calls the screen makes.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, login, selectTenant }),
  consumeSessionExpired: () => false,
}));

beforeEach(async () => {
  login.mockReset();
  selectTenant.mockReset();
  await i18n.changeLanguage('en');
});

afterEach(cleanup);

function renderLogin() {
  render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  );
}

const t = (key: string) => i18n.t(key);

async function submitCredentials() {
  fireEvent.change(screen.getByLabelText(t('login:emailLabel')), {
    target: { value: 'someone@example.com' },
  });
  fireEvent.change(screen.getByLabelText(t('login:passwordLabel')), {
    target: { value: 'hunter2' },
  });
  fireEvent.submit(screen.getByRole('button', { name: t('login:signIn') }));
}

// What packages/client/src/transport.ts can put in front of this screen, named by what
// actually happened rather than by its HTTP status — which is the whole point: only one of
// them carries an `errors` array, however healthy the others' statuses look.
const REJECTED = new GraphQLRequestError('invalid credentials', 200, [
  { message: 'invalid credentials' },
]);
const NO_ANSWER: [string, unknown][] = [
  // The live defect: `instanceof GraphQLRequestError` is TRUE here.
  ['the API is unreachable (fetch rejected)', new GraphQLRequestError('Failed to fetch', 0)],
  // The narrower fix's blind spot: a real status, no errors array.
  ['the ingress returns 503', new GraphQLRequestError('Request failed (503)', 503)],
  ['the service returns 502', new GraphQLRequestError('Request failed (502)', 502)],
  ['the server answers 200 with no data', new GraphQLRequestError('Empty GraphQL response', 200)],
  // 🔴 THE SHAPE THE TRANSPORT DOES NOT WRAP, and the reason "gql throws four shapes" is
  // not a closed list. `await res.json()` REJECTS on a 200 whose body is not JSON — an
  // ingress misroute serving the SPA's index.html under the API path, a captive portal —
  // and that SyntaxError leaves gql() as itself, never becoming a GraphQLRequestError. The
  // screen must still say "unreachable", which it does because the predicate is a test and
  // not a list of known errors.
  ['the API path serves HTML instead of JSON', new SyntaxError("Unexpected token '<'")],
];

describe('signing in when the server says no', () => {
  it('reports bad credentials', async () => {
    login.mockRejectedValue(REJECTED);
    renderLogin();
    await submitCredentials();
    await screen.findByText(t('login:invalidCredentials'));
  });
});

describe('signing in when there was never an answer', () => {
  it.each(NO_ANSWER)('says the server is unreachable when %s', async (_label, err) => {
    login.mockRejectedValue(err);
    renderLogin();
    await submitCredentials();

    await screen.findByText(t('login:serverUnreachable'));
    // 🔴 The positive assertion alone is not enough: both strings could render, or the
    // wrong one could render alongside. What made this a defect is the user being told
    // their password was wrong, so pin that they are NOT.
    expect(screen.queryByText(t('login:invalidCredentials'))).toBeNull();
  });
});

// Step 2 is the second call site, with its own rejection message. It has to be reached
// through step 1 — an identity with two memberships stops on the tenant picker.
describe('entering a tenant', () => {
  const TWO_TENANTS = {
    identityToken: 'id-token',
    expiresAt: '2099-01-01T00:00:00Z',
    superuser: false,
    memberships: [
      { tenant: 'acme', roles: [] },
      { tenant: 'globex', roles: [] },
    ],
  };

  const pickTenant = async () => {
    login.mockResolvedValue(TWO_TENANTS);
    renderLogin();
    await submitCredentials();
    fireEvent.click(await screen.findByRole('button', { name: 'acme' }));
  };

  it('reports the tenant failure when the server evaluated it and refused', async () => {
    selectTenant.mockRejectedValue(REJECTED);
    await pickTenant();
    await screen.findByText(t('login:enterTenantFailed'));
  });

  it('says the server is unreachable when the request never got an answer', async () => {
    selectTenant.mockRejectedValue(new GraphQLRequestError('Request failed (503)', 503));
    await pickTenant();
    await screen.findByText(t('login:serverUnreachable'));
    expect(screen.queryByText(t('login:enterTenantFailed'))).toBeNull();
  });
});

// Single sign-on. The browser is sent to the address the server gave, as given; a domain
// with no connection says so and leaves the password form usable; and a tenant that
// requires single sign-on is explained rather than reported as a bad password.
describe('signing in with single sign-on', () => {
  const realLocation = window.location;
  const assign = vi.fn();
  beforeEach(() => {
    startSso.mockReset();
    assign.mockReset();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, assign, origin: realLocation.origin },
    });
  });
  afterEach(() => {
    Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
  });

  const typeEmail = (v: string) =>
    fireEvent.change(screen.getByLabelText(t('login:emailLabel')), { target: { value: v } });
  const clickSso = () => fireEvent.click(screen.getByRole('button', { name: t('login:ssoButton') }));

  it('navigates to the address the server returned, untouched', async () => {
    const given = 'https://idp.example.com/authorize?state=a%2Fb&nonce=n#x';
    startSso.mockResolvedValue(given);
    renderLogin();
    typeEmail(' someone@acme.com ');
    clickSso();
    await waitFor(() => expect(assign).toHaveBeenCalledWith(given));
    expect(startSso).toHaveBeenCalledWith({ email: 'someone@acme.com', returnTo: null });
  });

  it('asks for the email first and does not call the server without one', async () => {
    renderLogin();
    clickSso();
    await screen.findByText(t('login:ssoEmailRequired'));
    expect(startSso).not.toHaveBeenCalled();
  });

  it('says so when no connection covers the email, and leaves the password form', async () => {
    startSso.mockResolvedValue(null);
    renderLogin();
    typeEmail('someone@nowhere.com');
    clickSso();
    await screen.findByText(t('login:ssoNotConfigured'));
    expect(assign).not.toHaveBeenCalled();
    expect(screen.getByLabelText(t('login:passwordLabel'))).toBeTruthy();
  });

  it('reports an unavailable instance, and an unreachable server separately', async () => {
    startSso.mockRejectedValueOnce(REJECTED);
    renderLogin();
    typeEmail('someone@acme.com');
    clickSso();
    await screen.findByText(t('login:ssoUnavailable'));
    startSso.mockRejectedValueOnce(new GraphQLRequestError('Failed to fetch', 0));
    clickSso();
    await screen.findByText(t('login:serverUnreachable'));
  });

  // The server refuses a password session in such a tenant at selectTenant (never at login,
  // which proves only the password) with extensions.code SSO_REQUIRED.
  const SSO_REQUIRED = new GraphQLRequestError('this tenant requires single sign-on', 200, [
    { message: 'this tenant requires single sign-on', extensions: { code: 'SSO_REQUIRED' } },
  ]);
  const auth = (memberships: { tenant: string; ssoRequired: boolean }[]) => ({
    identityToken: 'id',
    expiresAt: 'x',
    superuser: false,
    memberships: memberships.map((m) => ({ ...m, roles: [] })),
  });

  it('explains a refusal from a tenant that requires single sign-on, not as a tenant failure', async () => {
    login.mockResolvedValue(auth([{ tenant: 'acme', ssoRequired: false }]));
    selectTenant.mockRejectedValue(SSO_REQUIRED);
    renderLogin();
    await submitCredentials();
    await screen.findByText(t('login:ssoRequired'));
    expect(screen.queryByText(t('login:invalidCredentials'))).toBeNull();
  });

  it('does not try to enter when every tenant requires single sign-on, and says why', async () => {
    login.mockResolvedValue(auth([{ tenant: 'acme', ssoRequired: true }]));
    renderLogin();
    await submitCredentials();
    await screen.findByText(t('login:ssoRequired'));
    expect(selectTenant).not.toHaveBeenCalled();
    // Still on the form, where the SSO button is.
    expect(screen.getByRole('button', { name: t('login:ssoButton') })).toBeTruthy();
  });

  it('lists a tenant that requires single sign-on, marked and not choosable', async () => {
    login.mockResolvedValue(
      auth([
        { tenant: 'acme', ssoRequired: true },
        { tenant: 'globex', ssoRequired: false },
      ]),
    );
    renderLogin();
    await submitCredentials();
    const acme = (await screen.findByRole('button', { name: /^acme/ })) as HTMLButtonElement;
    expect(acme.textContent).toContain(t('login:ssoRequiredBadge'));
    expect(acme.disabled).toBe(true);
    const globex = screen.getByRole('button', { name: 'globex' }) as HTMLButtonElement;
    expect(globex.disabled).toBe(false);
    fireEvent.click(acme);
    expect(selectTenant).not.toHaveBeenCalled();
  });
});

// 🔴 THE INSTRUMENT CHECK. Every assertion above compares against a string i18next
// resolved, and i18next answers an unknown key with the key's own tail — so a renamed or
// deleted catalog entry would have this suite comparing 'serverUnreachable' to
// 'serverUnreachable' and passing while the screen showed a raw key to the user.
describe('the messages this screen can show are real prose', () => {
  it.each([
    'login:invalidCredentials',
    'login:enterTenantFailed',
    'login:serverUnreachable',
    'login:ssoRequired',
    'login:ssoRequiredBadge',
    'login:sessionSsoRequired',
    'login:ssoNotConfigured',
    'login:ssoUnavailable',
  ])(
    '%s resolves to text, not to its own key',
    async (key) => {
      await waitFor(() => expect(i18n.t(key)).not.toBe(key.split(':').pop()));
    },
  );
});
