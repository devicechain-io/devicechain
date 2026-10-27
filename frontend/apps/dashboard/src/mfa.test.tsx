// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The viewer's second sign-in step. A right password does not finish the sign-in for an
// account with two-factor authentication on: login fails with the code MFA_CHALLENGE and the
// viewer asks for a code, then completes the sign-in with verifyMfa. The transport (gql) is
// the only seam faked; the real App runs. The promises are the same as the console's: the
// handle stays out of the address and storage, every refusal of the code reads as one thing,
// the per-source rate limit reads as itself, an expired challenge sends the viewer back, and a
// tenant that needs a factor the account lacks says so (this viewer cannot enrol).

import { GraphQLRequestError } from '@devicechain/client';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const h = vi.hoisted(() => ({ gql: vi.fn() }));

vi.mock('@devicechain/client', async (orig) => ({
  ...(await orig<typeof import('@devicechain/client')>()),
  gql: (...a: unknown[]) => h.gql(...a),
  setAuthTokenGetter: () => {},
}));

import App from './App';
import i18n from './i18n/config';
import { LOGIN, SELECT_TENANT, VERIFY_MFA } from './queries';

const t = (key: string) => i18n.t(key);

const future = () => new Date(Date.now() + 5 * 60_000).toISOString();
const coded = (code: string, extra: Record<string, unknown> = {}) =>
  new GraphQLRequestError(code, 200, [{ message: code, extensions: { code, ...extra } }]);
const challenge = (expiresAt = future(), methods = ['TOTP', 'RECOVERY_CODE']) =>
  coded('MFA_CHALLENGE', { challenge: 'chal-1', expiresAt, methods });
const uncoded = (message: string) => new GraphQLRequestError(message, 200, [{ message }]);

const ONE_TENANT = { identityToken: 'id', memberships: [{ tenant: 'acme', roles: [], ssoRequired: false }] };

function serve(handlers: { login?: () => unknown; verify?: (vars: any) => unknown; select?: () => unknown }) {
  h.gql.mockImplementation(async (_area: string, doc: unknown, vars: unknown) => {
    const pick = doc === LOGIN ? handlers.login : doc === VERIFY_MFA ? handlers.verify : doc === SELECT_TENANT ? handlers.select : undefined;
    if (!pick) throw new Error('unexpected document');
    return pick(vars);
  });
}

async function reachCodeStep() {
  fireEvent.change(screen.getByLabelText(t('signIn:emailLabel')), { target: { value: 'v@acme.com' } });
  fireEvent.change(screen.getByLabelText(t('signIn:passwordLabel')), { target: { value: 'hunter2' } });
  fireEvent.submit(screen.getByRole('button', { name: t('signIn:submit') }));
  await screen.findByLabelText(t('signIn:mfaCodeLabel'));
}
const typeCode = (v: string) =>
  fireEvent.change(screen.getByLabelText(/code$/i), { target: { value: v } });
const verify = () => fireEvent.click(screen.getByRole('button', { name: t('signIn:mfaVerify') }));

beforeEach(async () => {
  h.gql.mockReset();
  window.sessionStorage.clear();
  window.localStorage.clear();
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

describe('the code step', () => {
  it('appears when the password is right but the account is held for its second factor', async () => {
    serve({ login: () => { throw challenge(); } });
    render(<App />);
    await reachCodeStep();
    expect(screen.queryByLabelText(t('signIn:passwordLabel'))).toBeNull();
    expect(screen.queryByText(t('signIn:invalidCredentials'))).toBeNull();
  });

  it('keeps the challenge out of the address bar and storage', async () => {
    serve({ login: () => { throw challenge(); } });
    render(<App />);
    await reachCodeStep();
    typeCode('123456');
    expect(window.location.href).not.toContain('chal-1');
    expect(JSON.stringify({ ...window.localStorage, ...window.sessionStorage })).not.toContain('chal-1');
  });

  it('sends the challenge and the six digits, then enters the tenant', async () => {
    let sent: any;
    serve({
      login: () => { throw challenge(); },
      verify: (vars) => { sent = vars; return { verifyMfa: ONE_TENANT }; },
      select: () => ({ selectTenant: { accessToken: 'a.b.c', refreshToken: 'r' } }),
    });
    render(<App />);
    await reachCodeStep();
    typeCode('12 34-56');
    verify();
    await waitFor(() => expect(sent).toEqual({ challenge: 'chal-1', code: '123456' }));
    await waitFor(() => expect(h.gql.mock.calls.some((c) => c[1] === SELECT_TENANT)).toBe(true));
  });

  it('sends a recovery code when asked to', async () => {
    let sent: any;
    serve({
      login: () => { throw challenge(); },
      verify: (vars) => { sent = vars; return { verifyMfa: { identityToken: 'id', memberships: [] } }; },
    });
    render(<App />);
    await reachCodeStep();
    fireEvent.click(screen.getByRole('button', { name: t('signIn:mfaUseRecovery') }));
    fireEvent.change(screen.getByLabelText(t('signIn:mfaRecoveryLabel')), { target: { value: 'abcd-efgh-jkmn-pqrs' } });
    verify();
    await waitFor(() => expect(sent).toEqual({ challenge: 'chal-1', code: 'ABCD-EFGH-JKMN-PQRS' }));
  });
});

describe('when the code is refused', () => {
  it('says the same thing for a wrong, replayed or locked code', async () => {
    serve({ login: () => { throw challenge(); }, verify: () => { throw uncoded('invalid code'); } });
    render(<App />);
    await reachCodeStep();
    typeCode('123456');
    verify();
    await screen.findByText(t('signIn:mfaInvalidCode'));
    expect((screen.getByLabelText(t('signIn:mfaCodeLabel')) as HTMLInputElement).value).toBe('');
  });

  it('reads the per-source rate limit as one', async () => {
    serve({ login: () => { throw challenge(); }, verify: () => { throw uncoded('too many attempts; try again shortly'); } });
    render(<App />);
    await reachCodeStep();
    typeCode('123456');
    verify();
    await screen.findByText(t('signIn:mfaRateLimited'));
    expect(screen.queryByText(t('signIn:mfaInvalidCode'))).toBeNull();
  });

  it('says unreachable, not "invalid code", when there was no answer', async () => {
    serve({ login: () => { throw challenge(); }, verify: () => { throw new GraphQLRequestError('Request failed (503)', 503); } });
    render(<App />);
    await reachCodeStep();
    typeCode('123456');
    verify();
    await screen.findByText(t('signIn:serverUnreachable'));
    expect(screen.queryByText(t('signIn:mfaInvalidCode'))).toBeNull();
  });

  it('sends the viewer back to the password when the challenge expired', async () => {
    serve({ login: () => { throw challenge(); }, verify: () => { throw coded('MFA_CHALLENGE_EXPIRED'); } });
    render(<App />);
    await reachCodeStep();
    typeCode('123456');
    verify();
    await screen.findByText(t('signIn:mfaChallengeExpired'));
    expect(((await screen.findByLabelText(t('signIn:passwordLabel'))) as HTMLInputElement).value).toBe('');
  });

  it('notices a lapsed challenge on its own, without asking the server', async () => {
    serve({ login: () => { throw challenge(new Date(Date.now() + 80).toISOString()); } });
    render(<App />);
    await reachCodeStep();
    await screen.findByText(t('signIn:mfaChallengeExpired'), undefined, { timeout: 2000 });
    expect(h.gql.mock.calls.some((c) => c[1] === VERIFY_MFA)).toBe(false);
  });
});

describe('a tenant that requires a second factor', () => {
  it('says to set one up in the console when the account has none', async () => {
    serve({ login: () => ({ login: ONE_TENANT }), select: () => { throw coded('MFA_ENROLLMENT_REQUIRED'); } });
    render(<App />);
    fireEvent.change(screen.getByLabelText(t('signIn:emailLabel')), { target: { value: 'v@acme.com' } });
    fireEvent.change(screen.getByLabelText(t('signIn:passwordLabel')), { target: { value: 'hunter2' } });
    fireEvent.submit(screen.getByRole('button', { name: t('signIn:submit') }));
    await screen.findByText(t('signIn:mfaEnrollmentRequired'));
  });
});
