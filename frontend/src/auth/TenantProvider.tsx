// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthProvider';
import { getCurrentTenant } from '@/lib/api/user-management';

// The current tenant the console is acting within. Resolved from the access
// token server-side (getCurrentTenant), then cached per-tenant in localStorage:
// tenant info changes rarely, so we render from cache immediately and refresh in
// the background (stale-while-revalidate). This is also the anchor for tenant
// branding (logo/colors) — those fields will extend TenantInfo as they land.

export interface TenantInfo {
  token: string;
  name: string | null;
  description: string | null;
}

const TenantContext = createContext<TenantInfo | null>(null);

const CACHE_PREFIX = 'dc-tenant:';

function readCache(token: string): TenantInfo | null {
  try {
    const raw = window.localStorage.getItem(CACHE_PREFIX + token);
    return raw ? (JSON.parse(raw) as TenantInfo) : null;
  } catch {
    return null;
  }
}

function writeCache(info: TenantInfo) {
  try {
    window.localStorage.setItem(CACHE_PREFIX + info.token, JSON.stringify(info));
  } catch {
    // storage disabled / quota — the chip falls back to a live fetch each load
  }
}

export function TenantProvider({ children }: { children: ReactNode }) {
  const { claims } = useAuth();
  const token = claims?.tenant ?? null;
  const [info, setInfo] = useState<TenantInfo | null>(() => (token ? readCache(token) : null));

  useEffect(() => {
    if (!token) {
      setInfo(null);
      return;
    }
    // Seed from cache (or the bare token) so the chip paints instantly, then
    // refresh from the server in the background.
    setInfo(readCache(token) ?? { token, name: null, description: null });
    let cancelled = false;
    getCurrentTenant()
      .then((t) => {
        if (cancelled) return;
        const next: TenantInfo = {
          token: t.token,
          name: t.name ?? null,
          description: t.description ?? null,
        };
        setInfo(next);
        writeCache(next);
      })
      .catch(() => {
        // Keep the cached / token-only value; a transient failure shouldn't
        // blank the header.
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  return <TenantContext.Provider value={info}>{children}</TenantContext.Provider>;
}

export function useCurrentTenant(): TenantInfo | null {
  return useContext(TenantContext);
}
