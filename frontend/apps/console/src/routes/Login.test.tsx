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
const discardPendingSso = vi.fn();

vi.mock('@/lib/sso', () => ({
  startSso: (...a: unknown[]) => startSso(...a),
  discardPendingSso: () => discardPendingSso(),
}));

// The provider is the app's session machinery (token storage, refresh timers, GraphQL
// getters); none of it is this file's subject. Stubbed to the two calls the screen makes.
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, login, selectTenant }),
  consumeSessionExpired: () => false,
}));

beforeEach(async () => {
  login.mockReset();
  selectTenant.mockReset();
  // The first screen asks the server which way the email signs in. Unless a case says
  // otherwise, no connection speaks for the domain, which is the password path.
  startSso.mockReset();
  startSso.mockResolvedValue(null);
  discardPendingSso.mockReset();
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

// The email first, then the password once the screen has asked for it.
async function submitCredentials() {
  fireEvent.change(screen.getByLabelText(t('login:emailLabel')), {
    target: { value: 'someone@example.com' },
  });
  fireEvent.click(screen.getByRole('button', { name: t('login:continue') }));
  const password = await screen.findByLabelText(t('login:passwordLabel'));
  fireEvent.change(password, { target: { value: 'hunter2' } });
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

// IDENTIFIER-FIRST. The first screen asks for the email alone and asks the server which
// way it signs in. What these cases pin is the part that is easy to get subtly wrong: the
// answer is about the DOMAIN, so it must look the same however the person got there.
describe('the first screen asks for the email alone', () => {
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

  const typeEmail = (v: string) =>
    fireEvent.change(screen.getByLabelText(t('login:emailLabel')), { target: { value: v } });
  const clickContinue = () =>
    fireEvent.click(screen.getByRole('button', { name: t('login:continue') }));
  const passwordField = () => screen.queryByLabelText(t('login:passwordLabel'));
  const GIVEN = 'https://idp.example.com/authorize?state=a%2Fb&nonce=n#x';

  // What a person sees, as the set of things they can do next: the buttons by name and
  // whether a password field exists. Two screens with the same shape here are
  // indistinguishable to anyone not reading the DOM's text.
  const shape = () =>
    JSON.stringify({
      buttons: screen.getAllByRole('button').map((b) => b.textContent),
      password: passwordField() !== null,
      notice: screen.queryByText(t('login:ssoDomainNotice')) !== null,
    });

  it('shows no password field and sends nothing but the email', () => {
    renderLogin();
    expect(passwordField()).toBeNull();
    expect(screen.getByRole('button', { name: t('login:continue') })).toBeTruthy();
    expect(screen.getByRole('button', { name: t('login:ssoButton') })).toBeTruthy();
    expect(startSso).not.toHaveBeenCalled();
    expect(login).not.toHaveBeenCalled();
  });

  it('asks about the trimmed email, and signs in nothing while it asks', async () => {
    renderLogin();
    typeEmail(' someone@acme.com ');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    expect(startSso).toHaveBeenCalledTimes(1);
    expect(startSso).toHaveBeenCalledWith({ email: 'someone@acme.com', returnTo: null });
    expect(login).not.toHaveBeenCalled();
  });

  it('offers single sign-on for a domain with a connection, and does not navigate by itself', async () => {
    startSso.mockResolvedValue(GIVEN);
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    await screen.findByText(t('login:ssoDomainNotice'));
    expect(assign).not.toHaveBeenCalled();
    expect(passwordField()).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: t('login:ssoContinue') }));
    // The address the server gave, untouched.
    expect(assign).toHaveBeenCalledWith(GIVEN);
  });

  it('keeps the password reachable at such a domain, and drops the begun sign-in when it is chosen', async () => {
    startSso.mockResolvedValue(GIVEN);
    login.mockResolvedValue({
      identityToken: 'id',
      expiresAt: 'x',
      superuser: false,
      memberships: [{ tenant: 'acme', roles: [], ssoRequired: false }],
    });
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    fireEvent.click(await screen.findByRole('button', { name: t('login:usePassword') }));

    expect(discardPendingSso).toHaveBeenCalledTimes(1);
    expect(assign).not.toHaveBeenCalled();
    fireEvent.change(await screen.findByLabelText(t('login:passwordLabel')), {
      target: { value: 'hunter2' },
    });
    fireEvent.submit(screen.getByRole('button', { name: t('login:signIn') }));
    await waitFor(() => expect(login).toHaveBeenCalledWith('someone@acme.com', 'hunter2'));
  });

  it('goes to the password for a domain with no connection, without a word about single sign-on', async () => {
    renderLogin();
    typeEmail('someone@nowhere.example');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    expect(screen.queryByText(t('login:ssoNotConfigured'))).toBeNull();
    expect(screen.queryByText(t('login:ssoDomainNotice'))).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    // ...and the explicit button is still there.
    expect(screen.getByRole('button', { name: t('login:ssoButton') })).toBeTruthy();
  });

  // 🔴 THE ENUMERATION CASE. A domain nobody has set up and a domain whose people use
  // passwords both come back null, and must be the very same screen. The only difference
  // the UI may make is the one the person is owed: whether single sign-on is offered.
  it('shows an unknown domain and a password-only domain as the same screen', async () => {
    renderLogin();
    typeEmail('someone@unknown.example');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    const unknown = shape();
    cleanup();

    renderLogin();
    typeEmail('someone@passwords-only.example');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    expect(shape()).toBe(unknown);
  });

  it.each([
    ['the instance has no single sign-on', REJECTED],
    ['the address is throttled', new GraphQLRequestError('too many requests', 429)],
    ['the server is unreachable', new GraphQLRequestError('Failed to fetch', 0)],
  ])('falls to the password field, quietly, when %s', async (_label, err) => {
    startSso.mockRejectedValue(err);
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    expect(screen.queryByRole('alert')).toBeNull();
    // The failure is the SAME screen as a domain with no connection.
    expect(screen.getByRole('button', { name: t('login:ssoButton') })).toBeTruthy();
  });

  it('lets the person change the email, and forgets the password they typed', async () => {
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    fireEvent.change(await screen.findByLabelText(t('login:passwordLabel')), {
      target: { value: 'hunter2' },
    });
    fireEvent.click(screen.getByRole('button', { name: t('login:changeEmail') }));

    expect(passwordField()).toBeNull();
    expect((screen.getByLabelText(t('login:emailLabel')) as HTMLInputElement).readOnly).toBe(false);
    typeEmail('other@acme.com');
    clickContinue();
    const again = (await screen.findByLabelText(t('login:passwordLabel'))) as HTMLInputElement;
    expect(again.value).toBe('');
  });

  it('does not let the address be edited once the password is asked for', async () => {
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    expect((screen.getByLabelText(t('login:emailLabel')) as HTMLInputElement).readOnly).toBe(true);
  });

  it('keeps Sign in with SSO working from the password step', async () => {
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    await screen.findByLabelText(t('login:passwordLabel'));
    startSso.mockResolvedValue(GIVEN);
    fireEvent.click(screen.getByRole('button', { name: t('login:ssoButton') }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith(GIVEN));
  });

  // 🔴 Enter on the single sign-on step. The email field keeps focus after Enter on the
  // first screen, so a second Enter submits the SAME form, which on this step has no
  // password field: it must continue to the provider, never send a password sign-in with
  // an empty password (a failed attempt counted against the account).
  it('continues to the provider on Enter at a domain with a connection, and sends no password', async () => {
    startSso.mockResolvedValue(GIVEN);
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    const sso = await screen.findByRole('button', { name: t('login:ssoContinue') });
    expect(document.activeElement).toBe(sso);

    fireEvent.submit(screen.getByLabelText(t('login:emailLabel')).closest('form')!);
    await waitFor(() => expect(assign).toHaveBeenCalledWith(GIVEN));
    expect(login).not.toHaveBeenCalled();
    expect(startSso).toHaveBeenCalledTimes(1);
  });

  it('drops the begun sign-in when the email is changed at a domain with a connection', async () => {
    startSso.mockResolvedValue(GIVEN);
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    fireEvent.click(await screen.findByRole('button', { name: t('login:changeEmail') }));

    expect(discardPendingSso).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(t('login:ssoDomainNotice'))).toBeNull();
    const field = screen.getByLabelText(t('login:emailLabel')) as HTMLInputElement;
    expect(field.readOnly).toBe(false);
    expect(document.activeElement).toBe(field);
    expect(assign).not.toHaveBeenCalled();
  });

  it('moves focus to the password field when the password is asked for', async () => {
    renderLogin();
    typeEmail('someone@acme.com');
    clickContinue();
    const password = await screen.findByLabelText(t('login:passwordLabel'));
    expect(document.activeElement).toBe(password);
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
    'login:continue',
    'login:changeEmail',
    'login:ssoContinue',
    'login:ssoDomainNotice',
    'login:usePassword',
  ])(
    '%s resolves to text, not to its own key',
    async (key) => {
      await waitFor(() => expect(i18n.t(key)).not.toBe(key.split(':').pop()));
    },
  );
});
