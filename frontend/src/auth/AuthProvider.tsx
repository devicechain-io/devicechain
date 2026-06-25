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
import { login as apiLogin, refresh as apiRefresh } from '@/lib/api/user-management';
import { setAuthTokenGetter } from '@/lib/graphql/client';
import { decodeToken, isExpired, type DecodedClaims } from '@/lib/auth/jwt';

// The access/refresh pair is persisted to localStorage so a reload keeps the
// session. The GraphQL client never reads storage directly: this provider
// registers a token getter (setAuthTokenGetter) that hands out a valid access
// token, transparently refreshing it when it is near expiry.

const STORAGE_KEY = 'dc-auth';

interface StoredTokens {
  accessToken: string;
  refreshToken: string;
}

interface AuthContextValue {
  /** Decoded access-token claims, or null when signed out. */
  claims: DecodedClaims | null;
  isAuthenticated: boolean;
  /** True until the persisted session has been read on first mount. */
  isLoading: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => void;
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

export function AuthProvider({ children }: { children: ReactNode }) {
  const [tokens, setTokens] = useState<StoredTokens | null>(() => readStored());
  const [isLoading, setIsLoading] = useState(true);

  // A ref mirror of tokens so the (stable) token getter always sees the latest
  // pair without being re-created on every refresh.
  const tokensRef = useRef<StoredTokens | null>(tokens);
  tokensRef.current = tokens;

  // De-dupe concurrent refreshes: many in-flight requests share one refresh.
  const refreshInFlight = useRef<Promise<string | null> | null>(null);

  const applyTokens = useCallback((next: StoredTokens | null) => {
    tokensRef.current = next;
    setTokens(next);
    writeStored(next);
  }, []);

  const logout = useCallback(() => {
    refreshInFlight.current = null;
    applyTokens(null);
  }, [applyTokens]);

  // Hands the GraphQL client a usable access token, refreshing first if the
  // current one is missing or near expiry. Returns null when the session can no
  // longer be sustained (caller proceeds unauthenticated → server fails closed).
  const getToken = useCallback(async (): Promise<string | null> => {
    const current = tokensRef.current;
    if (!current) return null;
    if (!isExpired(current.accessToken)) return current.accessToken;

    if (!refreshInFlight.current) {
      refreshInFlight.current = (async () => {
        try {
          const pair = await apiRefresh(current.refreshToken);
          applyTokens({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
          return pair.accessToken;
        } catch {
          logout();
          return null;
        } finally {
          refreshInFlight.current = null;
        }
      })();
    }
    return refreshInFlight.current;
  }, [applyTokens, logout]);

  // Register the getter with the client, and mark loading complete.
  useEffect(() => {
    setAuthTokenGetter(getToken);
    setIsLoading(false);
    return () => setAuthTokenGetter(null);
  }, [getToken]);

  const login = useCallback(
    async (username: string, password: string) => {
      const pair = await apiLogin(username, password);
      applyTokens({ accessToken: pair.accessToken, refreshToken: pair.refreshToken });
    },
    [applyTokens],
  );

  const claims = useMemo(
    () => (tokens ? decodeToken(tokens.accessToken) : null),
    [tokens],
  );

  const value = useMemo<AuthContextValue>(
    () => ({
      claims,
      isAuthenticated: claims !== null,
      isLoading,
      login,
      logout,
    }),
    [claims, isLoading, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within an AuthProvider');
  return ctx;
}