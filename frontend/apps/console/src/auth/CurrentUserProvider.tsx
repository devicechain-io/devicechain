// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, useMemo, type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthProvider';
import { getCurrentUser } from '@/lib/api/user-management';
import { useCachedResource } from '@/lib/hooks/use-cached-resource';

// The identity the console is signed in as. The access token only carries the
// email (as username); the human name lives on the identity record, so we fetch
// it server-side (getCurrentUser) and cache it per-email (stale-while-revalidate
// — names change rarely), exactly like the tenant. displayName is derived from
// first/last name, falling back to the email.

interface CachedUser {
  email: string;
  firstName: string | null;
  lastName: string | null;
}

export interface UserInfo extends CachedUser {
  /** First + last name if present, otherwise the email. Always non-empty. */
  displayName: string;
}

// The current user plus a write-through setter so a profile edit (updateProfile)
// can refresh the name everywhere without a refetch.
interface CurrentUserValue {
  user: UserInfo | null;
  applyUser: (user: CachedUser) => void;
}

const CurrentUserContext = createContext<CurrentUserValue>({ user: null, applyUser: () => {} });

export function CurrentUserProvider({ children }: { children: ReactNode }) {
  const { claims } = useAuth();
  const email = claims?.username ?? null; // the token subject is the email
  const [cached, setCached] = useCachedResource<CachedUser>(email ? `dc-user:${email}` : null, () =>
    getCurrentUser().then((u) => ({
      email: u.email,
      firstName: u.firstName ?? null,
      lastName: u.lastName ?? null,
    })),
  );
  const value = useMemo<CurrentUserValue>(() => {
    // Fall back to the bare email so the menu paints before the first fetch lands.
    const base = cached ?? (email ? { email, firstName: null, lastName: null } : null);
    return {
      user: base
        ? { ...base, displayName: [base.firstName, base.lastName].filter(Boolean).join(' ') || base.email }
        : null,
      applyUser: setCached,
    };
  }, [cached, email, setCached]);

  return <CurrentUserContext.Provider value={value}>{children}</CurrentUserContext.Provider>;
}

export function useCurrentUser(): UserInfo | null {
  return useContext(CurrentUserContext).user;
}

// Apply an updated profile (from updateProfile) to the cached current user, so
// the name refreshes across the app without a refetch.
export function useApplyCurrentUser(): (user: CachedUser) => void {
  return useContext(CurrentUserContext).applyUser;
}
