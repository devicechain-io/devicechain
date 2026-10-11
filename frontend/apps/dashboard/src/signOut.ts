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

/**
 * Asks the server to end the sign-in `refreshToken` belongs to. Resolves true when the
 * server confirmed (or there was nothing to end), false when it could not be reached or
 * refused; the cause is logged either way.
 */
export async function endServerSession(refreshToken: string | null): Promise<boolean> {
  if (!refreshToken) return true;
  try {
    const r = await gql('user-management', LOGOUT, { token: refreshToken }, { anonymous: true });
    return r.logout === true;
  } catch (err) {
    console.error('Sign-out: the server did not confirm the session ended', err);
    return false;
  }
}
