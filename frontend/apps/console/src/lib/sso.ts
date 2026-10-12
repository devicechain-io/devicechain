// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The browser half of a single sign-on.
//
// Begin: make a random secret (the verifier) that only THIS browser holds, give the server
// its SHA-256 (the challenge), and navigate to the address the server returns, as given.
// The verifier waits in sessionStorage (this tab only; wrapped in try/catch because
// storage can be blocked) for the provider to send the browser back.
//
// Complete: the provider's callback sends the browser to /login/sso/complete?code=... The
// code is one-time, so what matters here is how it is handled, in this order:
//   1. captureSsoReturn() reads it and strips the query from the address bar with
//      history.replaceState, before anything renders or loads, so it is in neither the
//      history entry nor a later Referer;
//   2. the page is told not to send a Referer at all (a meta tag here; nginx also sends the
//      header) so nothing it loads can carry the address;
//   3. runSsoCompletion() redeems it exactly once. The outcome is held for the page's
//      lifetime, so React StrictMode's second mount shares the first mount's redemption
//      instead of spending the code twice;
//   4. the page is left with leaveSsoCompletion(), a FULL navigation, so the no-Referer
//      policy dies with this document instead of following the person into the console,
//      where the default basemap's tiles must be fetched with a Referer.
// The code is never logged and never stored.

import type { IdentityAuth } from '@/lib/api/user-management';
import { beginSso } from '@/lib/api/user-management';

/** The only address the server will return a console sign-in to. */
export const SSO_COMPLETE_PATH = '/login/sso/complete';

const PENDING_KEY = 'dc-sso-pending';

interface Pending {
  verifier: string;
  returnTo: string;
}

/**
 * A same-origin path the console may send a signed-in person to. Anything else (another
 * origin, a protocol-relative address, a backslash trick, the login screens themselves)
 * becomes '/'. The query string that supplied it is never trusted on its own.
 */
export function sanitizeReturnTo(raw: string | null | undefined): string {
  if (!raw || raw.length > 512) return '/';
  if (!raw.startsWith('/') || raw.startsWith('//') || /[\\\r\n\t]/.test(raw)) return '/';
  if (raw === '/login' || raw.startsWith('/login/') || raw.startsWith('/login?')) return '/';
  try {
    // Must stay on this origin once resolved.
    const u = new URL(raw, window.location.origin);
    if (u.origin !== window.location.origin) return '/';
  } catch {
    return '/';
  }
  return raw;
}

function base64url(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

async function newPkcePair(): Promise<{ verifier: string; challenge: string }> {
  const raw = new Uint8Array(32);
  globalThis.crypto.getRandomValues(raw);
  const verifier = base64url(raw);
  const digest = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  return { verifier, challenge: base64url(new Uint8Array(digest)) };
}

function writePending(p: Pending | null): void {
  try {
    if (p) window.sessionStorage.setItem(PENDING_KEY, JSON.stringify(p));
    else window.sessionStorage.removeItem(PENDING_KEY);
  } catch {
    // sessionStorage unavailable: completion will report the missing verifier by name.
  }
}

/** Reads and removes the pending sign-in: a verifier is good for one completion. */
function takePending(): Pending | null {
  try {
    const raw = window.sessionStorage.getItem(PENDING_KEY);
    window.sessionStorage.removeItem(PENDING_KEY);
    if (!raw) return null;
    const p = JSON.parse(raw) as Partial<Pending>;
    if (typeof p.verifier !== 'string' || p.verifier === '') return null;
    return { verifier: p.verifier, returnTo: sanitizeReturnTo(p.returnTo) };
  } catch {
    return null;
  }
}

export interface StartSsoOptions {
  email?: string;
  connection?: string;
  /** Where in the console to land after sign-in; sanitized, '/' by default. */
  returnTo?: string | null;
}

/**
 * Begins a single sign-on and returns the address to navigate to, exactly as the server
 * gave it, or null when no connection speaks for the email (sign in another way). The
 * caller navigates; this does not.
 */
export async function startSso(opts: StartSsoOptions): Promise<string | null> {
  const { verifier, challenge } = await newPkcePair();
  // Written before the request: the answer can only come back after a redirect, and a
  // failed begin leaves nothing behind (below).
  writePending({ verifier, returnTo: sanitizeReturnTo(opts.returnTo) });
  try {
    const url = await beginSso({
      email: opts.email,
      connection: opts.connection,
      returnTo: window.location.origin + SSO_COMPLETE_PATH,
      codeChallenge: challenge,
    });
    if (url === null) writePending(null);
    return url;
  } catch (err) {
    writePending(null);
    throw err;
  }
}

export interface SsoReturn {
  code: string | null;
  /** The provider-side failure class the server put on the return address, if any. */
  error: string | null;
}

let captured: SsoReturn | null = null;

/**
 * Reads the code (or error class) off the address, strips the query and fragment with
 * history.replaceState, and tells the page to send no Referer. Safe to call more than
 * once: the first call does the work and later calls return the same answer.
 */
export function captureSsoReturn(): SsoReturn {
  if (captured) return captured;
  const params = new URLSearchParams(window.location.search);
  captured = { code: params.get('code'), error: params.get('error') };
  try {
    window.history.replaceState(window.history.state, '', window.location.pathname);
  } catch {
    // history unavailable: nothing more can be done about the address
  }
  const meta = document.createElement('meta');
  meta.name = 'referrer';
  meta.content = 'no-referrer';
  document.head.appendChild(meta);
  return captured;
}

export type SsoOutcome =
  | { ok: true; auth: IdentityAuth; returnTo: string }
  | {
      ok: false;
      reason: 'provider' | 'missingCode' | 'missingVerifier' | 'redeemFailed';
      /** For 'provider': the coarse class the server named (refused, link_required, ...). */
      providerError?: string;
      /** For 'redeemFailed': what the transport threw. */
      cause?: unknown;
    };

let completion: Promise<SsoOutcome> | null = null;

/**
 * Redeems the return exactly once for the life of the page. `redeem` is the AuthProvider's
 * completeSso, which stores the identity session the way a password login does.
 */
export function runSsoCompletion(
  redeem: (code: string, verifier: string) => Promise<IdentityAuth>,
): Promise<SsoOutcome> {
  if (completion) return completion;
  completion = (async (): Promise<SsoOutcome> => {
    const ret = captureSsoReturn();
    const pending = takePending();
    if (ret.error) return { ok: false, reason: 'provider', providerError: ret.error };
    if (!ret.code) return { ok: false, reason: 'missingCode' };
    if (!pending) return { ok: false, reason: 'missingVerifier' };
    try {
      const auth = await redeem(ret.code, pending.verifier);
      return { ok: true, auth, returnTo: pending.returnTo };
    } catch (cause) {
      return { ok: false, reason: 'redeemFailed', cause };
    }
  })();
  return completion;
}

/**
 * Leaves the completion page for dest with a FULL navigation, never the router. The page
 * forbids the Referer (its response header and the meta tag above), and a document keeps
 * that policy for as long as it lives: a client-side hop would carry it into every console
 * page rendered next, maps included. So the completion document is discarded and dest loads
 * as a fresh one. Nothing is lost on the way: completeSso and selectTenant have already
 * stored the session in localStorage, the way a password sign-in does. dest must be a
 * same-origin path — a sanitized returnTo, or a fixed console path.
 */
export function leaveSsoCompletion(dest: string): void {
  window.location.replace(dest);
}

/** Test seam: forget the page-lifetime state. */
export function resetSsoStateForTests(): void {
  captured = null;
  completion = null;
}
