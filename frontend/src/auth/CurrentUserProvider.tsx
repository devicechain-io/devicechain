// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, type ReactNode } from 'react';
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

const CurrentUserContext = createContext<UserInfo | null>(null);
// Write-through setter so a profile edit updates the name everywhere immediately.
const ApplyUserContext = createContext<(user: CachedUser) => void>(() => {});

function fullName(first: string | null, last: string | null): string {
  return [first, last].filter(Boolean).join(' ');
}

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
  // Fall back to the bare email so the menu paints before the first fetch lands.
  const base = cached ?? (email ? { email, firstName: null, lastName: null } : null);
  const user: UserInfo | null = base
    ? { ...base, displayName: fullName(base.firstName, base.lastName) || base.email }
    : null;

  return (
    <CurrentUserContext.Provider value={user}>
      <ApplyUserContext.Provider value={setCached}>{children}</ApplyUserContext.Provider>
    </CurrentUserContext.Provider>
  );
}

export function useCurrentUser(): UserInfo | null {
  return useContext(CurrentUserContext);
}

// Apply an updated profile (from updateProfile) to the cached current user, so
// the name refreshes across the app without a refetch.
export function useApplyCurrentUser(): (user: CachedUser) => void {
  return useContext(ApplyUserContext);
}
