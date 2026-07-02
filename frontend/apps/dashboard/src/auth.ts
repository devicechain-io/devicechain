// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Session reuse for the standalone dashboard app.
//
// The dashboard app is served same-origin as the console, so it reads the tenant
// access token the console already stored in localStorage under `dc-auth` and
// registers it as the SDK's token getter — queries and subscriptions then
// authenticate as the logged-in user. This is deliberately minimal for the
// view-only slice (PR D1): it does NOT implement login or token refresh. A user
// opens a dashboard from the console (fresh session); if no valid token is present
// the app shows a "sign in via the console" prompt rather than a broken page.
// (Full standalone login/refresh — likely a shared @devicechain/client auth
// helper — is a follow-up.)

import { isExpired, setAuthTokenGetter } from '@devicechain/client';

// The localStorage key the console writes its tenant session to.
const AUTH_KEY = 'dc-auth';

function readAccessToken(): string | null {
  try {
    const raw = localStorage.getItem(AUTH_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { accessToken?: string };
    return parsed.accessToken ?? null;
  } catch {
    return null;
  }
}

// initAuth wires the SDK to the console's stored access token. Called once at
// startup, before any query/subscription runs.
export function initAuth(): void {
  setAuthTokenGetter(async () => readAccessToken());
}

// hasValidSession reports whether a non-expired access token is present, so the
// app can prompt for sign-in instead of firing doomed authenticated requests.
export function hasValidSession(): boolean {
  const token = readAccessToken();
  return token != null && !isExpired(token);
}
