// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The second step of signing in. A right password does not finish the sign-in for an account
// with two-factor authentication on: login fails with the code MFA_CHALLENGE, and the screen
// asks for a code and completes the sign-in with verifyMfa. These drive the real screen and
// read what it shows, because the promises are about what a person sees and what the server
// is sent: uniform "invalid code", a rate limit that reads as one, an expired challenge that
// sends them back, and a tenant that needs a factor the account lacks sending them to set one up.

import '@/i18n/config';
import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import i18n from '@/i18n/config';
import LoginPage from './Login';

const login = vi.fn();
const verifyMfa = vi.fn();
const selectTenant = vi.fn();

vi.mock('@/lib/sso', () => ({
  startSso: vi.fn().mockResolvedValue(null),
  discardPendingSso: vi.fn(),
}));
vi.mock('@/auth/AuthProvider', () => ({
  useAuth: () => ({ isAuthenticated: false, login, verifyMfa, selectTenant }),
  consumeSessionExpired: () => false,
}));

const t = (key: string) => i18n.t(key);

const AUTH = {
  identityToken: 'id-token',
  expiresAt: '2099-01-01T00:00:00Z',
  superuser: false,
  memberships: [{ tenant: 'acme', roles: [], ssoRequired: false }],
};

const future = () => new Date(Date.now() + 5 * 60_000).toISOString();

function challengeError(expiresAt = future(), methods = ['TOTP', 'RECOVERY_CODE']) {
  return new GraphQLRequestError('multi-factor authentication required', 200, [
    {
      message: 'multi-factor authentication required',
      extensions: { code: 'MFA_CHALLENGE', challenge: 'chal-1', expiresAt, methods },
    },
  ]);
}
const coded = (code: string) => new GraphQLRequestError(code, 200, [{ message: code, extensions: { code } }]);
const uncoded = (message: string) => new GraphQLRequestError(message, 200, [{ message }]);

// Where the enrolment redirect lands, and what it was handed.
function Probe() {
  const loc = useLocation();
  return (
    <div data-testid="at">
      {loc.pathname}|{JSON.stringify(loc.state)}
    </div>
  );
}

function renderLogin() {
  render(
    <MemoryRouter initialEntries={['/login']}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Probe />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function reachCodeStep() {
  fireEvent.change(screen.getByLabelText(t('login:emailLabel')), { target: { value: 'someone@example.com' } });
  fireEvent.click(screen.getByRole('button', { name: t('login:continue') }));
  fireEvent.change(await screen.findByLabelText(t('login:passwordLabel')), { target: { value: 'hunter2' } });
  fireEvent.submit(screen.getByRole('button', { name: t('login:signIn') }));
  await screen.findByLabelText(t('login:mfaCodeLabel'));
}

const typeCode = (v: string) =>
  fireEvent.change(screen.getByLabelText(/code$/i, { selector: 'input' }), { target: { value: v } });
const submitCode = () => fireEvent.click(screen.getByRole('button', { name: t('login:mfaVerify') }));

beforeEach(async () => {
  login.mockReset();
  verifyMfa.mockReset();
  selectTenant.mockReset();
  window.localStorage.clear();
  window.sessionStorage.clear();
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('the code step', () => {
  it('appears when the password is right but the account is held for its second factor', async () => {
    login.mockRejectedValue(challengeError());
    renderLogin();
    await reachCodeStep();
    // The password step is gone, and no error is shown: the password was right.
    expect(screen.queryByLabelText(t('login:passwordLabel'))).toBeNull();
    expect(screen.queryByText(t('login:invalidCredentials'))).toBeNull();
  });

  it('keeps the challenge out of the address bar and out of storage', async () => {
    login.mockRejectedValue(challengeError());
    renderLogin();
    await reachCodeStep();
    typeCode('123456');
    expect(window.location.href).not.toContain('chal-1');
    expect(JSON.stringify({ ...window.localStorage })).not.toContain('chal-1');
    expect(JSON.stringify({ ...window.sessionStorage })).not.toContain('chal-1');
  });

  it('sends the challenge and the six digits, then enters the tenant', async () => {
    login.mockRejectedValue(challengeError());
    verifyMfa.mockResolvedValue(AUTH);
    selectTenant.mockResolvedValue(undefined);
    renderLogin();
    await reachCodeStep();
    typeCode('123456');
    submitCode();
    await waitFor(() => expect(verifyMfa).toHaveBeenCalledWith('chal-1', '123456'));
    await waitFor(() => expect(selectTenant).toHaveBeenCalledWith('id-token', 'acme'));
    expect((await screen.findByTestId('at')).textContent).toContain('/|');
  });

  it('only lets a 6-digit code be submitted, and strips what is not a digit', async () => {
    login.mockRejectedValue(challengeError());
    renderLogin();
    await reachCodeStep();
    const button = screen.getByRole('button', { name: t('login:mfaVerify') }) as HTMLButtonElement;
    typeCode('12 34');
    expect(button.disabled).toBe(true);
    typeCode('12-34-56');
    expect((screen.getByLabelText(t('login:mfaCodeLabel')) as HTMLInputElement).value).toBe('123456');
    expect(button.disabled).toBe(false);
  });

  it('switches to a recovery code and sends that', async () => {
    login.mockRejectedValue(challengeError());
    verifyMfa.mockResolvedValue({ ...AUTH, memberships: [] });
    renderLogin();
    await reachCodeStep();
    fireEvent.click(screen.getByRole('button', { name: t('login:mfaUseRecovery') }));
    fireEvent.change(screen.getByLabelText(t('login:mfaRecoveryLabel')), {
      target: { value: 'abcd-efgh-jkmn-pqrs' },
    });
    submitCode();
    await waitFor(() => expect(verifyMfa).toHaveBeenCalledWith('chal-1', 'ABCD-EFGH-JKMN-PQRS'));
  });

  it('offers no recovery toggle when the challenge does not list it', async () => {
    login.mockRejectedValue(challengeError(future(), ['TOTP']));
    renderLogin();
    await reachCodeStep();
    expect(screen.queryByRole('button', { name: t('login:mfaUseRecovery') })).toBeNull();
  });
});

describe('when the code is refused', () => {
  it('says the same thing for a wrong, replayed or locked code, and clears the field', async () => {
    login.mockRejectedValue(challengeError());
    verifyMfa.mockRejectedValue(uncoded('invalid code'));
    renderLogin();
    await reachCodeStep();
    typeCode('123456');
    submitCode();
    await screen.findByText(t('login:mfaInvalidCode'));
    expect((screen.getByLabelText(t('login:mfaCodeLabel')) as HTMLInputElement).value).toBe('');
    // Still on the code step: a wrong code does not throw the password away.
    expect(screen.queryByLabelText(t('login:passwordLabel'))).toBeNull();
  });

  it('reads the per-source rate limit as one, not as a wrong code', async () => {
    login.mockRejectedValue(challengeError());
    verifyMfa.mockRejectedValue(uncoded('too many attempts; try again shortly'));
    renderLogin();
    await reachCodeStep();
    typeCode('123456');
    submitCode();
    await screen.findByText(t('login:mfaRateLimited'));
    expect(screen.queryByText(t('login:mfaInvalidCode'))).toBeNull();
  });

  it('says the server is unreachable, and not "invalid code", when there was no answer', async () => {
    login.mockRejectedValue(challengeError());
    verifyMfa.mockRejectedValue(new GraphQLRequestError('Request failed (503)', 503));
    renderLogin();
    await reachCodeStep();
    typeCode('123456');
    submitCode();
    await screen.findByText(t('login:serverUnreachable'));
    expect(screen.queryByText(t('login:mfaInvalidCode'))).toBeNull();
  });

  it('sends the person back to the password when the server says the challenge expired', async () => {
    login.mockRejectedValue(challengeError());
    verifyMfa.mockRejectedValue(coded('MFA_CHALLENGE_EXPIRED'));
    renderLogin();
    await reachCodeStep();
    typeCode('123456');
    submitCode();
    await screen.findByText(t('login:mfaChallengeExpired'));
    const password = (await screen.findByLabelText(t('login:passwordLabel'))) as HTMLInputElement;
    expect(password.value).toBe('');
  });

  it('does not even ask the server when the challenge has already lapsed', async () => {
    login.mockRejectedValue(challengeError(new Date(Date.now() + 80).toISOString()));
    renderLogin();
    await reachCodeStep();
    // The screen notices on its own, without a click.
    await screen.findByText(t('login:mfaChallengeExpired'), undefined, { timeout: 2000 });
    expect(verifyMfa).not.toHaveBeenCalled();
  });

  it('Back drops the held sign-in and the password with it', async () => {
    login.mockRejectedValue(challengeError());
    renderLogin();
    await reachCodeStep();
    fireEvent.click(screen.getByRole('button', { name: t('common:back') }));
    const password = (await screen.findByLabelText(t('login:passwordLabel'))) as HTMLInputElement;
    expect(password.value).toBe('');
  });
});

describe('a tenant that requires a second factor', () => {
  it('routes a member with none to set one up, instead of showing an error', async () => {
    login.mockResolvedValue(AUTH);
    selectTenant.mockRejectedValue(coded('MFA_ENROLLMENT_REQUIRED'));
    renderLogin();
    fireEvent.change(screen.getByLabelText(t('login:emailLabel')), { target: { value: 'someone@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: t('login:continue') }));
    fireEvent.change(await screen.findByLabelText(t('login:passwordLabel')), { target: { value: 'hunter2' } });
    fireEvent.submit(screen.getByRole('button', { name: t('login:signIn') }));
    const at = await screen.findByTestId('at');
    expect(at.textContent).toBe('/security|{"enrollForTenant":"acme"}');
  });

  it('explains MFA_REQUIRED (the account has a factor but this sign-in did not use it)', async () => {
    login.mockResolvedValue(AUTH);
    selectTenant.mockRejectedValue(coded('MFA_REQUIRED'));
    renderLogin();
    fireEvent.change(screen.getByLabelText(t('login:emailLabel')), { target: { value: 'someone@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: t('login:continue') }));
    fireEvent.change(await screen.findByLabelText(t('login:passwordLabel')), { target: { value: 'hunter2' } });
    fireEvent.submit(screen.getByRole('button', { name: t('login:signIn') }));
    await screen.findByText(t('login:mfaRequired'));
  });
});
