// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Session reuse + refresh for the standalone dashboard app.
//
// The dashboard app is served same-origin as the console, so it reuses the tenant
// session the console already stored in localStorage under `dc-auth`
// (`{ accessToken, refreshToken }`). It registers an async token getter with the
// SDK that returns the access token while it is valid and, once it expires, renews
// it from the stored refresh token (writing the new pair back in the console's
// shape so both apps share the renewed session). Without this the dashboard would
// keep presenting an expired token past the ~15-minute access-token lifetime and
// silently show stale data as if it were live.

import { gql, isExpired, setAuthTokenGetter } from '@devicechain/client';

import { REFRESH } from './queries';

// The localStorage key the console writes its tenant session to.
const AUTH_KEY = 'dc-auth';

interface StoredSession {
  accessToken?: string;
  refreshToken?: string;
}

function readSession(): StoredSession {
  try {
    const raw = localStorage.getItem(AUTH_KEY);
    if (!raw) return {};
    return JSON.parse(raw) as StoredSession;
  } catch {
    return {};
  }
}

function writeSession(session: { accessToken: string; refreshToken: string }): void {
  try {
    localStorage.setItem(AUTH_KEY, JSON.stringify(session));
  } catch {
    // A write failure (private-mode quota) is non-fatal — the in-memory token from
    // this refresh still authenticates the current session.
  }
}

// A single in-flight refresh, so concurrent getter calls (many widgets opening at
// once) collapse to ONE refresh mutation rather than a stampede.
let refreshing: Promise<string | null> | null = null;

function refreshAccessToken(refreshToken: string): Promise<string | null> {
  if (refreshing) return refreshing;
  refreshing = gql('user-management', REFRESH, { refreshToken }, { anonymous: true })
    .then((r) => {
      const next = r.refresh;
      writeSession({ accessToken: next.accessToken, refreshToken: next.refreshToken });
      return next.accessToken;
    })
    .catch(() => null)
    .finally(() => {
      refreshing = null;
    });
  return refreshing;
}

// getAccessToken returns a usable access token: the stored one while valid, else a
// freshly refreshed one, else null (the caller then fires unauthenticated / prompts).
async function getAccessToken(): Promise<string | null> {
  const { accessToken, refreshToken } = readSession();
  if (accessToken && !isExpired(accessToken)) return accessToken;
  if (refreshToken) return refreshAccessToken(refreshToken);
  return null;
}

// initAuth wires the SDK to the reused/refreshing session. Called once at startup,
// before any query/subscription runs.
export function initAuth(): void {
  setAuthTokenGetter(getAccessToken);
}

// hasValidSession reports whether the app can authenticate — either a non-expired
// access token, or a refresh token it can renew from. Only when neither exists does
// the app show the "sign in via the console" prompt.
export function hasValidSession(): boolean {
  const { accessToken, refreshToken } = readSession();
  if (accessToken && !isExpired(accessToken)) return true;
  return refreshToken != null;
}
