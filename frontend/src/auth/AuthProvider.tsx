// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import {
  login as apiLogin,
  selectTenant as apiSelectTenant,
  refresh as apiRefresh,
  type IdentityAuth,
} from '@/lib/api/user-management';
import { setAuthTokenGetter, setIdentityTokenGetter } from '@/lib/graphql/client';
import { decodeToken, isExpired, type DecodedClaims } from '@/lib/auth/jwt';

// The access/refresh pair is persisted to localStorage so a reload keeps the
// session. The GraphQL client never reads storage directly: this provider
// registers a token getter (setAuthTokenGetter) that hands out a valid access
// token, transparently refreshing it when it is near expiry.
//
// A second, instance-scoped "identity session" (ADR-033) is persisted alongside
// it: the identity token returned by login(), plus whether the principal is a
// superuser and the tenants it may act in. It authenticates the admin console
// (setIdentityTokenGetter); unlike the tenant session it has no refresh token, so
// it simply expires (the admin console then routes back to login).

const STORAGE_KEY = 'dc-auth';
const IDENTITY_STORAGE_KEY = 'dc-identity';

interface StoredTokens {
  accessToken: string;
  refreshToken: string;
}

interface StoredIdentity {
  identityToken: string;
  superuser: boolean;
  memberships: IdentityAuth['memberships'];
}

interface AuthContextValue {
  /** Decoded access-token claims, or null when signed out. */
  claims: DecodedClaims | null;
  isAuthenticated: boolean;
  /** True until the persisted session has been read on first mount. */
  isLoading: boolean;
  /**
   * Authenticate an email/password (ADR-033). Returns the identity token + the
   * tenants the identity may act in, and starts the identity session (used by the
   * admin console); does NOT start a tenant session — call selectTenant with the
   * returned identityToken to do that.
   */
  login: (email: string, password: string) => Promise<IdentityAuth>;
  /** Exchange an identity token for a tenant-scoped session. */
  selectTenant: (identityToken: string, tenant: string) => Promise<void>;
  logout: () => void;

  // ── Identity session (admin console) ──
  /** True while a non-expired identity token is held. */
  isIdentityAuthenticated: boolean;
  /** Whether the authenticated identity holds the superuser system role. */
  superuser: boolean;
  /** The tenants the authenticated identity may act in. */
  memberships: IdentityAuth['memberships'];
  /** The live identity token, or null when the identity session is absent/expired. */
  identityToken: string | null;
}

const AuthContext = createContext<AuthContextValue | null>(null);

function readStored(): StoredTokens | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as StoredTokens) : null;
  } catch {
    return null;
  }
}

function writeStored(tokens: StoredTokens | null) {
  try {
    if (tokens) window.localStorage.setItem(STORAGE_KEY, JSON.stringify(tokens));
    else window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // storage disabled / quota — session stays in-memory only
  }
}

function readStoredIdentity(): StoredIdentity | null {
  try {
    const raw = window.localStorage.getItem(IDENTITY_STORAGE_KEY);
    if (!raw) return null;
    const id = JSON.parse(raw) as StoredIdentity;
    // Drop an expired identity session on load (it has no refresh path).
    return isExpired(id.identityToken) ? null : id;
  } catch {
    return null;
  }
}

function writeStoredIdentity(identity: StoredIdentity | null) {
  try {
    if (identity) window.localStorage.setItem(IDENTITY_STORAGE_KEY, JSON.stringify(identity));
    else window.localStorage.removeItem(IDENTITY_STORAGE_KEY);
  } catch {
    // storage disabled / quota — session stays in-memory only
  }
}

// A one-shot flag set when a session is dropped because it expired (as opposed to
// an explicit sign-out), read once by the login screen so the redirect isn't
// mysterious.
const SESSION_EXPIRED_KEY = 'dc-session-expired';

function flagSessionExpired() {
  try {
    window.sessionStorage.setItem(SESSION_EXPIRED_KEY, '1');
  } catch {
    // sessionStorage unavailable — the login notice is best-effort
  }
}

export function consumeSessionExpired(): boolean {
  try {
    if (window.sessionStorage.getItem(SESSION_EXPIRED_KEY) === '1') {
      window.sessionStorage.removeItem(SESSION_EXPIRED_KEY);
      return true;
    }
  } catch {
    // ignore
  }
  return false;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [tokens, setTokens] = useState<StoredTokens | null>(() => readStored());
  const [identity, setIdentity] = useState<StoredIdentity | null>(() => readStoredIdentity());
  const [isLoading, setIsLoading] = useState(true);

  // A ref mirror of tokens so the (stable) token getter always sees the latest
  // pair without being re-created on every refresh.
  const tokensRef = useRef<StoredTokens | null>(tokens);
  tokensRef.current = tokens;

  // A ref mirror of the identity session, for the (stable) identity-token getter.
  const identityRef = useRef<StoredIdentity | null>(identity);
  identityRef.current = identity;

  // De-dupe concurrent refreshes: many in-flight requests share one refresh.
  const refreshInFlight = useRef<Promise<string | null> | null>(null);

  const applyTokens = useCallback((next: StoredTokens | null) => {
    tokensRef.current = next;
    setTokens(next);
    writeStored(next);
  }, []);

  const applyIdentity = useCallback((next: StoredIdentity | null) => {
    identityRef.current = next;
    setIdentity(next);
    writeStoredIdentity(next);
  }, []);

  const logout = useCallback(() => {
    refreshInFlight.current = null;
    applyTokens(null);
    applyIdentity(null);
  }, [applyTokens, applyIdentity]);

  // expireTenant / expireIdentity drop a session that can no longer be sustained
  // (the refresh call failed, or a no-refresh identity token lapsed) and flag the
  // cause for the login screen. Clearing the session state flips the route guards,
  // which redirect to /login — instead of leaving a dead session in place to fire
  // failing requests and render a raw error.
  const expireTenant = useCallback(() => {
    flagSessionExpired();
    refreshInFlight.current = null;
    applyTokens(null);
  }, [applyTokens]);

  const expireIdentity = useCallback(() => {
    flagSessionExpired();
    applyIdentity(null);
  }, [applyIdentity]);

  // Hands the GraphQL client a usable access token, refreshing first if the
  // current one is near expiry. If the refresh token itself has lapsed (long
  // inactivity) or the refresh call fails, the tenant session is expired cleanly
  // and the caller gets null (the route guard then redirects to login).
  const getToken = useCallback(async (): Promise<string | null> => {
    const current = tokensRef.current;
    if (!current) return null;
    if (isExpired(current.refreshToken, 0)) {
      expireTenant();
      return null;
    }
    if (!isExpired(current.accessToken)) return current.accessToken;

    if (!refreshInFlight.current) {
      refreshInFlight.current = (async () => {
        try {
          const pair = await apiRefresh(current.refreshToken);
          // The session can be cleared or replaced while this refresh is in
          // flight (logout, expiry). A promise can't be cancelled, so guard here:
          // if the live session is no longer the one we started refreshing, drop
          // the result instead of resurrecting a dead session into storage.
          if (tokensRef.current !== current) return null;
          applyTokens({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
          return pair.accessToken;
        } catch {
          // Only expire the session this refresh belonged to — not one that has
          // since been logged out or replaced.
          if (tokensRef.current === current) expireTenant();
          return null;
        } finally {
          refreshInFlight.current = null;
        }
      })();
    }
    return refreshInFlight.current;
  }, [applyTokens, expireTenant]);

  // Hands the admin client the identity token while it is still valid. Identity
  // tokens have no refresh path, so an expired one expires the identity session —
  // which routes the admin console back to login — and returns null.
  const getIdentityToken = useCallback(async (): Promise<string | null> => {
    const current = identityRef.current;
    if (!current) return null;
    if (isExpired(current.identityToken, 0)) {
      expireIdentity();
      return null;
    }
    return current.identityToken;
  }, [expireIdentity]);

  // Register the getters with the client, and mark loading complete.
  useEffect(() => {
    setAuthTokenGetter(getToken);
    setIdentityTokenGetter(getIdentityToken);
    setIsLoading(false);
    return () => {
      setAuthTokenGetter(null);
      setIdentityTokenGetter(null);
    };
  }, [getToken, getIdentityToken]);

  // Step 1: authenticate the identity and start the identity session (used by the
  // admin console). A tenant session is not started until a tenant is selected.
  const login = useCallback(
    async (email: string, password: string) => {
      const auth = await apiLogin(email, password);
      applyIdentity({
        identityToken: auth.identityToken,
        superuser: auth.superuser,
        memberships: auth.memberships,
      });
      return auth;
    },
    [applyIdentity],
  );

  // Step 2: exchange the identity token for a tenant-scoped session pair.
  const selectTenant = useCallback(
    async (identityToken: string, tenant: string) => {
      const pair = await apiSelectTenant(identityToken, tenant);
      applyTokens({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
    },
    [applyTokens],
  );

  const claims = useMemo(
    () => (tokens ? decodeToken(tokens.accessToken) : null),
    [tokens],
  );

  // A session counts as authenticated only while it can still be sustained: the
  // tenant session until its refresh token lapses (the short-lived access token is
  // refreshed transparently), the identity session until its non-refreshable token
  // expires. The route guards read these, so an expired session redirects to login
  // rather than rendering pages whose requests will fail.
  const sessionAlive = tokens !== null && !isExpired(tokens.refreshToken, 0);
  const identityAlive = identity !== null && !isExpired(identity.identityToken, 0);

  const value = useMemo<AuthContextValue>(
    () => ({
      claims,
      isAuthenticated: sessionAlive,
      isLoading,
      login,
      selectTenant,
      logout,
      isIdentityAuthenticated: identityAlive,
      superuser: identityAlive && (identity?.superuser ?? false),
      memberships: identity?.memberships ?? [],
      identityToken: identityAlive ? (identity?.identityToken ?? null) : null,
    }),
    [claims, sessionAlive, identityAlive, isLoading, login, selectTenant, logout, identity],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within an AuthProvider');
  return ctx;
}