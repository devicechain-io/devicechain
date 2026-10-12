// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The browser half of single sign-on: what begin sends and keeps, and what the return
// does with a one-time code. The transport is the only seam faked.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const beginSso = vi.fn();
vi.mock('@/lib/api/user-management', () => ({ beginSso: (...a: unknown[]) => beginSso(...a) }));

import {
  SSO_COMPLETE_PATH,
  captureSsoReturn,
  resetSsoStateForTests,
  runSsoCompletion,
  sanitizeReturnTo,
  startSso,
} from './sso';

const AUTH = { identityToken: 'id-tok', expiresAt: 'x', superuser: false, memberships: [] };

function landOn(path: string) {
  window.history.replaceState(null, '', path);
}

beforeEach(() => {
  beginSso.mockReset();
  resetSsoStateForTests();
  window.sessionStorage.clear();
  document.head.querySelectorAll('meta[name="referrer"]').forEach((m) => m.remove());
  landOn('/');
});
afterEach(() => landOn('/'));

describe('startSso', () => {
  it('sends the challenge of a verifier it keeps, and returns the server address untouched', async () => {
    const given = 'https://idp.example.com/authorize?x=1&state=a%20b#frag';
    beginSso.mockResolvedValue(given);
    const url = await startSso({ email: 'a@acme.com', returnTo: '/devices' });
    expect(url).toBe(given);
    const req = beginSso.mock.calls[0][0];
    expect(req.email).toBe('a@acme.com');
    expect(req.returnTo).toBe(window.location.origin + SSO_COMPLETE_PATH);
    expect(req.codeChallenge).toMatch(/^[A-Za-z0-9_-]{43}$/);
    // The verifier is kept for the return, and is not what was sent.
    const kept = JSON.parse(window.sessionStorage.getItem('dc-sso-pending')!);
    expect(kept.verifier).not.toBe(req.codeChallenge);
    expect(kept.returnTo).toBe('/devices');
    // SHA-256(verifier) is the challenge.
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(kept.verifier));
    const expected = btoa(String.fromCharCode(...new Uint8Array(digest)))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
    expect(req.codeChallenge).toBe(expected);
  });

  it('keeps nothing when no connection covers the email, or when begin fails', async () => {
    beginSso.mockResolvedValueOnce(null);
    expect(await startSso({ email: 'a@nowhere.com' })).toBeNull();
    expect(window.sessionStorage.getItem('dc-sso-pending')).toBeNull();

    beginSso.mockRejectedValueOnce(new Error('down'));
    await expect(startSso({ email: 'a@acme.com' })).rejects.toThrow('down');
    expect(window.sessionStorage.getItem('dc-sso-pending')).toBeNull();
  });
});

describe('sanitizeReturnTo', () => {
  it.each([
    ['/devices', '/devices'],
    ['/devices?x=1', '/devices?x=1'],
    ['https://evil.example/', '/'],
    ['//evil.example/', '/'],
    ['/\\evil.example', '/'],
    // The two above are also caught by the same-origin check once resolved. These are not:
    // they resolve to THIS origin, so only the textual refusals stand between them and a
    // returnTo that is not a plain path.
    [`//${window.location.host}/devices`, '/'],
    [`/\\${window.location.host}/devices`, '/'],
    ['/%2F%2Fevil.example', '/%2F%2Fevil.example'],
    ['javascript:alert(1)', '/'],
    ['devices', '/'],
    ['/login', '/'],
    ['/login/sso/complete', '/'],
    ['', '/'],
    [null, '/'],
  ])('%s -> %s', (raw, want) => {
    expect(sanitizeReturnTo(raw)).toBe(want);
  });
});

describe('the return', () => {
  it('strips the query from the address and forbids the Referer before anything redeems it', () => {
    landOn('/login/sso/complete?code=SECRET&x=1#h');
    const ret = captureSsoReturn();
    expect(ret.code).toBe('SECRET');
    expect(window.location.search).toBe('');
    expect(window.location.hash).toBe('');
    expect(window.location.href).not.toContain('SECRET');
    expect(document.head.querySelector('meta[name="referrer"]')?.getAttribute('content')).toBe(
      'no-referrer',
    );
  });

  it('redeems once however many times it is asked for, and consumes the verifier', async () => {
    window.sessionStorage.setItem(
      'dc-sso-pending',
      JSON.stringify({ verifier: 'V', returnTo: '/alarms' }),
    );
    landOn('/login/sso/complete?code=C1');
    const redeem = vi.fn().mockResolvedValue(AUTH);
    // StrictMode mounts twice: both asks share one redemption.
    const [a, b] = await Promise.all([runSsoCompletion(redeem), runSsoCompletion(redeem)]);
    expect(redeem).toHaveBeenCalledTimes(1);
    expect(redeem).toHaveBeenCalledWith('C1', 'V');
    expect(a).toBe(b);
    expect(a).toEqual({ ok: true, auth: AUTH, returnTo: '/alarms' });
    expect(window.sessionStorage.getItem('dc-sso-pending')).toBeNull();
  });

  it('names a missing verifier and does not redeem', async () => {
    landOn('/login/sso/complete?code=C1');
    const redeem = vi.fn();
    const out = await runSsoCompletion(redeem);
    expect(out).toMatchObject({ ok: false, reason: 'missingVerifier' });
    expect(redeem).not.toHaveBeenCalled();
  });

  it('names a provider-side refusal and does not redeem', async () => {
    window.sessionStorage.setItem('dc-sso-pending', JSON.stringify({ verifier: 'V', returnTo: '/' }));
    landOn('/login/sso/complete?error=link_required');
    const redeem = vi.fn();
    const out = await runSsoCompletion(redeem);
    expect(out).toMatchObject({ ok: false, reason: 'provider', providerError: 'link_required' });
    expect(redeem).not.toHaveBeenCalled();
    // A failed sign-in spends its verifier too: it is never left for a later return.
    expect(window.sessionStorage.getItem('dc-sso-pending')).toBeNull();
  });

  it('spends the verifier when the return carries no code', async () => {
    window.sessionStorage.setItem('dc-sso-pending', JSON.stringify({ verifier: 'V', returnTo: '/' }));
    landOn('/login/sso/complete');
    const out = await runSsoCompletion(vi.fn());
    expect(out).toMatchObject({ ok: false, reason: 'missingCode' });
    expect(window.sessionStorage.getItem('dc-sso-pending')).toBeNull();
  });

  it('reports a failed redemption without trying again', async () => {
    window.sessionStorage.setItem('dc-sso-pending', JSON.stringify({ verifier: 'V', returnTo: '/' }));
    landOn('/login/sso/complete?code=C1');
    const redeem = vi.fn().mockRejectedValue(new Error('invalid or expired sign-in code'));
    const out = await runSsoCompletion(redeem);
    expect(out).toMatchObject({ ok: false, reason: 'redeemFailed' });
    await runSsoCompletion(redeem);
    expect(redeem).toHaveBeenCalledTimes(1);
    expect(window.sessionStorage.getItem('dc-sso-pending')).toBeNull();
  });

  it('never logs the code', async () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
      vi.spyOn(console, m).mockImplementation(() => {}),
    );
    window.sessionStorage.setItem('dc-sso-pending', JSON.stringify({ verifier: 'V', returnTo: '/' }));
    landOn('/login/sso/complete?code=SECRETCODE');
    await runSsoCompletion(vi.fn().mockRejectedValue(new Error('no')));
    for (const s of spies) {
      expect(JSON.stringify(s.mock.calls)).not.toContain('SECRETCODE');
      s.mockRestore();
    }
  });
});
