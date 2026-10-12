// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Ending the viewer's sign-in on the server.
//
// Clearing React state only forgets the tokens HERE; the refresh token the server issued
// stays valid until it expires, so anyone who copied it (a shared machine, a screen
// recording of the network tab) could keep minting access tokens. The logout mutation ends
// the sign-in server-side.
//
// A failure must not trap the viewer in a session they asked to leave, so the caller
// clears its local state regardless - but it must not be SILENT either: the server-side
// session is still open, and the viewer should know to try again. This returns whether the
// server confirmed, and logs the cause.

import { gql } from '@devicechain/client';

import { LOGOUT } from './queries';

/** What signing out on the server came to. */
export interface SignOutOutcome {
  /** True when the server confirmed (or there was nothing to end). */
  ended: boolean;
  /**
   * The identity provider's sign-out address, when the server answered one: the sign-in
   * came through a connection set to sign people out at the provider too. Null otherwise.
   */
  providerSignOut: string | null;
}

/**
 * Asks the server to end the sign-in `refreshToken` belongs to. `ended` is true when the
 * server confirmed (or there was nothing to end), false when it could not be reached or
 * refused; the cause is logged either way.
 */
export async function endServerSession(refreshToken: string | null): Promise<SignOutOutcome> {
  if (!refreshToken) return { ended: true, providerSignOut: null };
  try {
    const r = await gql('user-management', LOGOUT, { token: refreshToken }, { anonymous: true });
    if (!r.logout) throw new Error('The server did not confirm the sign-out');
    return { ended: true, providerSignOut: providerSignOutTarget(r.logout.providerSignOutUrl) };
  } catch (err) {
    console.error('Sign-out: the server did not confirm the session ended', err);
    return { ended: false, providerSignOut: null };
  }
}

/**
 * The provider address, if it is one a browser may be sent to: an absolute http(s) URL.
 * Anything else is dropped, so a malformed answer degrades to signing out here only.
 */
export function providerSignOutTarget(raw: string | null | undefined): string | null {
  if (!raw) return null;
  try {
    const u = new URL(raw);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.toString() : null;
  } catch {
    return null;
  }
}
