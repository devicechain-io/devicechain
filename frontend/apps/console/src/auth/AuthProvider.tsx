// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import i18n from 'i18next';
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
  verifyMfa as apiVerifyMfa,
  completeSso as apiCompleteSso,
  selectTenant as apiSelectTenant,
  refresh as apiRefresh,
  logout as apiLogout,
  getIdentityMemberships as apiIdentityMemberships,
  type IdentityAuth,
} from '@/lib/api/user-management';
import {
  setAuthTokenGetter,
  setIdentityTokenGetter,
  createSessionTokens,
  decodeToken,
  isExpired,
  type DecodedClaims,
} from '@devicechain/client';
import { acceptInvitation as apiAcceptInvitation } from '@/lib/api/members';
import { useToast } from '@/components/ui/toast';
import { serverErrorCode } from '@/i18n/signInError';

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
  /** How this identity session was started; absent on a session stored before this was kept. */
  method?: SignInMethod;
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
  /**
   * Finish a sign-in that login held for a second factor (it failed with MFA_CHALLENGE):
   * `code` is an authenticator code or a recovery code. Answers and stores exactly what login
   * does, so everything after (selectTenant, refresh, logout) is the same.
   */
  verifyMfa: (challenge: string, code: string) => Promise<IdentityAuth>;
  /**
   * Replace the identity session with a fresh sign-in the server handed back (turning two-factor
   * authentication on ends every session the account had, and answers the caller's
   * replacement). A tenant session already held is renewed against the new identity so the
   * person carries on where they are; the identity session stays even when that fails.
   */
  adoptIdentity: (auth: IdentityAuth) => Promise<void>;
  /**
   * Drop every local session WITHOUT asking the server to end them, for the moment the server
   * has just ended them itself (two-factor authentication turned off). Sign-out proper is logout.
   */
  forgetSessions: () => void;
  /**
   * Redeem the one-time code a single sign-on returned (with the verifier held since the
   * sign-in began). The result is stored exactly as login's is, so every later step
   * (selectTenant, refresh, logout) is the same for both ways of signing in.
   */
  completeSso: (code: string, verifier: string) => Promise<IdentityAuth>;
  /**
   * Accept an invitation with a new account's password or the identity token of the
   * account it was issued to. Answers what login answers and starts the identity session
   * the same way, so the next step is selectTenant as after any sign-in.
   */
  acceptInvitation: (code: string, proof: { password: string } | { identityToken: string }) => Promise<IdentityAuth>;
  /** Exchange an identity token for a tenant-scoped session. */
  selectTenant: (identityToken: string, tenant: string) => Promise<void>;
  /**
   * Re-read the identity's live memberships (ADR-042 fix) so the tenant picker
   * reflects a membership added/removed mid-session, without a re-login. A no-op
   * when no identity session is held; failures are swallowed (best-effort).
   */
  refreshMemberships: () => Promise<void>;
  /**
   * Sign out: clears the local session at once, then ends the sign-in on the server (so
   * its refresh and identity tokens stop working). The local session is gone even when the
   * server cannot be reached - the user asked to leave - but that failure is logged and
   * shown, not swallowed, because the server-side session is then still open. When the
   * server confirms, and the sign-in came through a connection set to sign people out at
   * the identity provider too, the browser is then sent to the provider's sign-out page.
   */
  logout: () => Promise<void>;

  // ── Identity session (admin console) ──
  /** True while a non-expired identity token is held. */
  isIdentityAuthenticated: boolean;
  /** Whether the authenticated identity holds the superuser system role. */
  superuser: boolean;
  /** The tenants the authenticated identity may act in. */
  memberships: IdentityAuth['memberships'];
  /** The live identity token, or null when the identity session is absent/expired. */
  identityToken: string | null;
  /**
   * How the stored identity session was started, EVEN after its token has lapsed (so the
   * console can say "sign in again" rather than hide what needs a fresh sign-in); null when
   * none is stored or it predates this. Whether the session is usable is identityToken.
   */
  signInMethod: SignInMethod | null;
}

/** How the person signed in: with a password, or through an identity provider. */
export type SignInMethod = 'password' | 'sso';

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

/**
 * Why a session was dropped: it lapsed, or its tenant started requiring single sign-on
 * (the server refused the refresh with SSO_REQUIRED), which a fresh password sign-in
 * cannot fix, so the login screen says so instead of "your session expired".
 */
export type SessionEndReason = 'expired' | 'ssoRequired';

function flagSessionExpired(reason: SessionEndReason = 'expired') {
  try {
    window.sessionStorage.setItem(SESSION_EXPIRED_KEY, reason === 'ssoRequired' ? 'sso' : '1');
  } catch {
    // sessionStorage unavailable — the login notice is best-effort
  }
}

export function consumeSessionExpired(): SessionEndReason | null {
  try {
    const v = window.sessionStorage.getItem(SESSION_EXPIRED_KEY);
    if (v === '1' || v === 'sso') {
      window.sessionStorage.removeItem(SESSION_EXPIRED_KEY);
      return v === 'sso' ? 'ssoRequired' : 'expired';
    }
  } catch {
    // ignore
  }
  return null;
}

// How long sign-out waits for the server before leaving anyway. The user asked to go; a
// hung request must not keep them signed in.
const LOGOUT_TIMEOUT_MS = 5000;

// endServerSessions calls the logout mutation for each token and rejects if any call
// failed or the server took too long (every call is still attempted). It resolves to the
// identity provider's sign-out address when the server answered one (a single sign-on
// through a connection set to sign people out at the provider too), else null.
async function endServerSessions(tokens: string[]): Promise<string | null> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error('sign-out timed out')), LOGOUT_TIMEOUT_MS);
  });
  try {
    const results = await Promise.race([
      Promise.allSettled(tokens.map((t) => apiLogout(t))),
      timeout,
    ]);
    const failed = results.find((r): r is PromiseRejectedResult => r.status === 'rejected');
    if (failed) throw failed.reason;
    // Both tokens usually name one sign-in, and whichever call reached it first answers.
    for (const r of results) {
      if (r.status === 'fulfilled' && r.value) return r.value;
    }
    return null;
  } finally {
    clearTimeout(timer);
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [tokens, setTokens] = useState<StoredTokens | null>(() => readStored());
  const [identity, setIdentity] = useState<StoredIdentity | null>(() => readStoredIdentity());
  const [isLoading, setIsLoading] = useState(true);
  const { toast } = useToast();

  // A ref mirror of tokens so the (stable) token getter always sees the latest
  // pair without being re-created on every refresh.
  const tokensRef = useRef<StoredTokens | null>(tokens);
  tokensRef.current = tokens;

  // A ref mirror of the identity session, for the (stable) identity-token getter.
  const identityRef = useRef<StoredIdentity | null>(identity);
  identityRef.current = identity;

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

  // expireTenant / expireIdentity drop a session that can no longer be sustained
  // (the refresh call failed, or a no-refresh identity token lapsed) and flag the
  // cause for the login screen. Clearing the session state flips the route guards,
  // which redirect to /login — instead of leaving a dead session in place to fire
  // failing requests and render a raw error.
  const expireTenant = useCallback((reason: SessionEndReason = 'expired') => {
    flagSessionExpired(reason);
    applyTokens(null);
  }, [applyTokens]);

  const session = useMemo(
    () =>
      createSessionTokens({
        read: () => tokensRef.current,
        apply: (pair) => applyTokens(pair),
        refresh: async (refreshToken) => {
          const pair = await apiRefresh(refreshToken);
          return { accessToken: pair.accessToken, refreshToken: pair.refreshToken };
        },
        onEnd: (err) => {
          expireTenant(
            err !== undefined && serverErrorCode(err) === 'SSO_REQUIRED' ? 'ssoRequired' : 'expired',
          );
        },
      }),
    [applyTokens, expireTenant],
  );

  const logout = useCallback(async () => {
    // Both tokens name the same sign-in when they came from one login; send each that is
    // held, so a stale tenant session left over from an earlier login is ended too.
    const held = [tokensRef.current?.refreshToken, identityRef.current?.identityToken].filter(
      (t): t is string => !!t,
    );
    // Forget the tokens here FIRST, then ask the server. The user may close the tab the
    // moment they click: waiting on the network (up to LOGOUT_TIMEOUT_MS) before clearing
    // would leave the tokens in localStorage on a shared machine whenever that request
    // never finishes - worse than the client-only sign-out this replaces.
    session.reset();
    applyTokens(null);
    applyIdentity(null);
    let providerSignOut: string | null = null;
    try {
      providerSignOut = await endServerSessions(held);
    } catch (err) {
      console.error('Sign-out: the server did not confirm the session ended', err);
      toast(i18n.t('login:signOutFailed'), 'error');
      return;
    }
    // Only once the server has confirmed the sign-out here: send the browser to the
    // provider to end the session there too. It comes back to the sign-in page.
    if (providerSignOut) window.location.assign(providerSignOut);
  }, [applyTokens, applyIdentity, session, toast]);

  const expireIdentity = useCallback(() => {
    flagSessionExpired();
    applyIdentity(null);
  }, [applyIdentity]);

  // Hands the GraphQL client a usable access token, refreshing first if the
  // current one is near expiry. If the refresh token itself has lapsed (long
  // inactivity) or the refresh call fails, the tenant session is expired cleanly
  // and the caller gets null (the route guard then redirects to login). The
  // refresh lifecycle (skew, one shared in-flight refresh, dropping a result for a
  // replaced session) is @devicechain/client's createSessionTokens; this only
  // supplies where the pair lives and what happens when the session ends.
  const getToken = session.getAccessToken;

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
        method: 'password',
        superuser: auth.superuser,
        memberships: auth.memberships,
      });
      return auth;
    },
    [applyIdentity],
  );

  // A held sign-in finishes with the code: the result is stored exactly as login's is.
  const verifyMfa = useCallback(
    async (challenge: string, code: string) => {
      const auth = await apiVerifyMfa(challenge, code);
      applyIdentity({
        identityToken: auth.identityToken,
        method: 'password',
        superuser: auth.superuser,
        memberships: auth.memberships,
      });
      return auth;
    },
    [applyIdentity],
  );

  // Single sign-on's step 1: the code from the provider's return replaces the password.
  const completeSso = useCallback(
    async (code: string, verifier: string) => {
      const auth = await apiCompleteSso(code, verifier);
      applyIdentity({
        identityToken: auth.identityToken,
        method: 'sso',
        superuser: auth.superuser,
        memberships: auth.memberships,
      });
      return auth;
    },
    [applyIdentity],
  );

  // An accepted invitation answers exactly what login answers. With a password it is a
  // password sign-in; with an identity token it continues the session that token came
  // from, single sign-on included, so the way that session started is kept.
  const acceptInvitation = useCallback(
    async (code: string, proof: { password: string } | { identityToken: string }) => {
      const auth = await apiAcceptInvitation(code, proof);
      applyIdentity({
        identityToken: auth.identityToken,
        method: 'password' in proof ? 'password' : (identityRef.current?.method ?? 'password'),
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

  // Re-read live memberships for the current identity session and replace the
  // cached login snapshot, so a membership added/removed elsewhere (the admin
  // console) shows in the tenant picker without a re-login. Best-effort: a
  // missing session or a failed call leaves the current snapshot untouched.
  const refreshMemberships = useCallback(async () => {
    const current = identityRef.current;
    if (!current || isExpired(current.identityToken, 0)) return;
    try {
      const memberships = await apiIdentityMemberships(current.identityToken);
      applyIdentity({ ...current, memberships });
    } catch {
      // best-effort — keep the existing snapshot
    }
  }, [applyIdentity]);

  // Turning two-factor on rotates the account's session epoch: the identity token that enrolled
  // and the tenant session beside it both stop working, and the server hands back a fresh
  // identity. Swap it in, then re-enter the tenant the person was in so they are not thrown out
  // at the next refresh.
  const adoptIdentity = useCallback(
    async (auth: IdentityAuth) => {
      const tenant = decodeToken(tokensRef.current?.accessToken ?? '')?.tenant ?? null;
      applyIdentity({
        identityToken: auth.identityToken,
        method: 'password',
        superuser: auth.superuser,
        memberships: auth.memberships,
      });
      if (tenant) {
        try {
          const pair = await apiSelectTenant(auth.identityToken, tenant);
          applyTokens({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
        } catch {
          // The old tenant session ends on its own at its next refresh; the identity
          // session above is already the fresh one, so signing in again is one step away.
        }
      }
    },
    [applyIdentity, applyTokens],
  );

  const forgetSessions = useCallback(() => {
    session.reset();
    applyTokens(null);
    applyIdentity(null);
  }, [session, applyTokens, applyIdentity]);

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
      verifyMfa,
      adoptIdentity,
      forgetSessions,
      completeSso,
      acceptInvitation,
      selectTenant,
      refreshMemberships,
      logout,
      isIdentityAuthenticated: identityAlive,
      superuser: identityAlive && (identity?.superuser ?? false),
      memberships: identity?.memberships ?? [],
      identityToken: identityAlive ? (identity?.identityToken ?? null) : null,
      signInMethod: identity?.method ?? null,
    }),
    [
      claims,
      sessionAlive,
      identityAlive,
      isLoading,
      login,
      verifyMfa,
      adoptIdentity,
      forgetSessions,
      completeSso,
      acceptInvitation,
      selectTenant,
      refreshMemberships,
      logout,
      identity,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within an AuthProvider');
  return ctx;
}