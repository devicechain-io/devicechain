// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The invitation code between the moment its link is opened and the moment it is
// accepted. The link carries it in the URL fragment (never sent to a server); the
// invitation page moves it here at once and clears the address bar, so the code does not
// sit in the browser history or in a screenshot of the page. sessionStorage is per tab
// and dies with it, and it survives exactly one thing the page needs it to: the round trip
// through an identity provider when the person accepts with single sign-on.

const KEY = 'dc-pending-invitation';

/** The code in a fragment such as `#code=abc`, or null. */
export function codeFromHash(hash: string): string | null {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash);
  const code = params.get('code');
  return code && code.trim() ? code.trim() : null;
}

export function holdInvitation(code: string): void {
  try {
    window.sessionStorage.setItem(KEY, code);
  } catch {
    // Storage refused (a private window): the page still holds the code in memory, and
    // only the single sign-on round trip is lost.
  }
}

export function heldInvitation(): string | null {
  try {
    return window.sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}

export function dropInvitation(): void {
  try {
    window.sessionStorage.removeItem(KEY);
  } catch {
    // nothing to drop
  }
}

/** Where the invitation page lives; the single sign-on completion returns here when a code is held. */
export const INVITATION_PATH = '/invitation';
