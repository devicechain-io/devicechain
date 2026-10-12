// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The browser half of a single sign-on for the standalone viewer.
//
// This mirrors apps/console/src/lib/sso.ts, and the duplication is deliberate for the same
// reason as signInError.ts: /dash is the reference external embedder a third party clones,
// so it must not depend on console internals. The contract is the server's (beginSso and
// completeSso), not a shared module.
//
// Begin: make a random verifier only THIS browser holds, give the server its SHA-256, and
// navigate to the address the server returns, as given. Complete: the provider's callback
// returns to /dash/sso/complete?code=... and the code, being one-time, is handled in this
// order: captureSsoReturn() reads it and strips the query from the address bar before
// anything renders, and tells the page to send no Referer; runSsoCompletion() redeems it
// once, shared across React StrictMode's double mount; the page is then left with a full
// navigation (below). The code is never logged or stored, and the tokens it yields live in
// memory only (App.tsx), save for the one hop out of the return page.

import { gql } from '@devicechain/client';

import { BEGIN_SSO, COMPLETE_SSO, type Membership } from './queries';

/** The only address the server will return a /dash sign-in to. */
export const SSO_COMPLETE_PATH = '/dash/sso/complete';

const PENDING_KEY = 'dc-dash-sso-verifier';

function base64url(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function writeVerifier(v: string | null): void {
  try {
    if (v) window.sessionStorage.setItem(PENDING_KEY, v);
    else window.sessionStorage.removeItem(PENDING_KEY);
  } catch {
    // sessionStorage unavailable: completion will report the missing verifier by name.
  }
}

function takeVerifier(): string | null {
  try {
    const v = window.sessionStorage.getItem(PENDING_KEY);
    window.sessionStorage.removeItem(PENDING_KEY);
    return v || null;
  } catch {
    return null;
  }
}

/**
 * Begins a single sign-on for `email`. Returns the provider's address to navigate to,
 * exactly as the server gave it, or null when no connection speaks for the email's domain.
 */
export async function startSso(email: string): Promise<string | null> {
  const raw = new Uint8Array(32);
  globalThis.crypto.getRandomValues(raw);
  const verifier = base64url(raw);
  const digest = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  writeVerifier(verifier);
  try {
    const r = await gql(
      'user-management',
      BEGIN_SSO,
      {
        email,
        returnTo: window.location.origin + SSO_COMPLETE_PATH,
        codeChallenge: base64url(new Uint8Array(digest)),
      },
      { anonymous: true },
    );
    const url = r.beginSso?.authorizationUrl ?? null;
    if (url === null) writeVerifier(null);
    return url;
  } catch (err) {
    writeVerifier(null);
    throw err;
  }
}

export interface SsoReturn {
  code: string | null;
  /** The coarse failure class the server put on the return address, if any. */
  error: string | null;
}

let captured: SsoReturn | null = null;

/** Reads the code off the address, strips the query, and forbids the Referer. Idempotent. */
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
  | { ok: true; identityToken: string; memberships: Membership[] }
  | {
      ok: false;
      reason: 'provider' | 'missingCode' | 'missingVerifier' | 'redeemFailed';
      providerError?: string;
      cause?: unknown;
    };

let completion: Promise<SsoOutcome> | null = null;

/** Redeems the return exactly once for the life of the page. */
export function runSsoCompletion(): Promise<SsoOutcome> {
  if (completion) return completion;
  completion = (async (): Promise<SsoOutcome> => {
    const ret = captureSsoReturn();
    const verifier = takeVerifier();
    if (ret.error) return { ok: false, reason: 'provider', providerError: ret.error };
    if (!ret.code) return { ok: false, reason: 'missingCode' };
    if (!verifier) return { ok: false, reason: 'missingVerifier' };
    try {
      const r = await gql(
        'user-management',
        COMPLETE_SSO,
        { code: ret.code, codeVerifier: verifier },
        { anonymous: true },
      );
      return {
        ok: true,
        identityToken: r.completeSso.identityToken,
        memberships: r.completeSso.memberships,
      };
    } catch (cause) {
      return { ok: false, reason: 'redeemFailed', cause };
    }
  })();
  return completion;
}

// ── Leaving the return page ─────────────────────────────────────────────────
// The return page forbids the Referer (nginx's header and the meta tag above), and a
// document keeps that policy for as long as it lives. /dash is a single-page app, so
// staying in this document after the sign-in would carry the policy into every dashboard
// it renders next — and the default basemap's tiles must be fetched WITH a Referer. So the
// page is left with a full navigation and /dash/ loads as a fresh document.
//
// That costs this viewer its session, which lives in memory only. The session therefore
// crosses the one hop in sessionStorage: written immediately before the navigation, taken
// (read AND removed) by the next document's first render, and never written anywhere else.
// sessionStorage is this tab's alone and dies with it; a hop that never lands leaves the
// pair there until the tab closes or /dash next loads in it, whichever is first. The
// alternative of redeeming in the next document instead would put the one-time code and
// its verifier in the same place for the same hop, which is no smaller, and would spend the
// code outside the page built to handle its failures.

const HANDOFF_KEY = 'dc-dash-sso-session';

/** The tenant session the return page hands to the next document. */
export interface SsoSession {
  accessToken: string;
  refreshToken: string;
}

/**
 * Hands the session to the next document and leaves the return page for /dash/ with a
 * full navigation. Returns false, having navigated nowhere, when the session could not be
 * written: the caller must then treat the sign-in as failed rather than stay signed in on
 * this document.
 */
export function leaveWithSession(session: SsoSession): boolean {
  try {
    window.sessionStorage.setItem(HANDOFF_KEY, JSON.stringify(session));
  } catch {
    return false;
  }
  leaveSsoReturn();
  return true;
}

/** Leaves the return page for /dash/ with a full navigation, never a history push. */
export function leaveSsoReturn(): void {
  window.location.replace('/dash/');
}

let handedOver: SsoSession | null | undefined;

/**
 * Takes the session a return page handed over, once per page: the first call reads and
 * removes it, later calls (React StrictMode renders twice) get the same answer.
 */
export function takeSsoSession(): SsoSession | null {
  if (handedOver !== undefined) return handedOver;
  handedOver = null;
  try {
    const raw = window.sessionStorage.getItem(HANDOFF_KEY);
    window.sessionStorage.removeItem(HANDOFF_KEY);
    if (!raw) return null;
    const s = JSON.parse(raw) as Partial<SsoSession>;
    if (typeof s.accessToken === 'string' && s.accessToken !== '' && typeof s.refreshToken === 'string') {
      handedOver = { accessToken: s.accessToken, refreshToken: s.refreshToken };
    }
  } catch {
    // storage unavailable or the value is not ours: no session, so the sign-in form shows
  }
  return handedOver;
}

/** Drops the handed-over session from memory too (sign-out). */
export function forgetSsoSession(): void {
  handedOver = null;
}

/** Test seam: forget the page-lifetime state. */
export function resetSsoStateForTests(): void {
  captured = null;
  completion = null;
  handedOver = undefined;
}
