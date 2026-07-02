// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthProvider';
import { getCurrentTenant } from '@/lib/api/user-management';
import { useCachedResource } from '@/lib/hooks/use-cached-resource';

// The current tenant the console is acting within. Resolved from the access
// token server-side (getCurrentTenant), then cached per-tenant (stale-while-
// revalidate — tenant info changes rarely). This is also the anchor for tenant
// branding (logo/colors) — those fields will extend TenantInfo as they land.

export interface TenantInfo {
  token: string;
  name: string | null;
  description: string | null;
}

const TenantContext = createContext<TenantInfo | null>(null);

export function TenantProvider({ children }: { children: ReactNode }) {
  const { claims } = useAuth();
  const token = claims?.tenant ?? null;
  const [cached] = useCachedResource<TenantInfo>(token ? `dc-tenant:${token}` : null, () =>
    getCurrentTenant().then((t) => ({
      token: t.token,
      name: t.name ?? null,
      description: t.description ?? null,
    })),
  );
  // Fall back to the bare token so the chip paints before the first fetch lands.
  const info = cached ?? (token ? { token, name: null, description: null } : null);

  return <TenantContext.Provider value={info}>{children}</TenantContext.Provider>;
}

export function useCurrentTenant(): TenantInfo | null {
  return useContext(TenantContext);
}
