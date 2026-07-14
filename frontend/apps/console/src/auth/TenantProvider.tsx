// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, useEffect, type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthProvider';
import { getCurrentTenant, type CurrentTenant, type TenantBranding } from '@/lib/api/user-management';
import { useCachedResource } from '@/lib/hooks/use-cached-resource';
import { applyBranding } from '@/lib/branding';

// The current tenant the console is acting within. Resolved from the access
// token server-side (getCurrentTenant), then cached per-tenant (stale-while-
// revalidate — tenant info changes rarely). Carries the resolved white-labeling
// branding (ADR-038), applied to the shell by the effect below.

export interface TenantInfo {
  token: string;
  name: string | null;
  description: string | null;
  // Null only in the pre-fetch fallback (bare token); a fetched tenant always
  // carries a resolved branding object (fields within it may be null = inherit).
  branding: TenantBranding | null;
  // The RAW override (no cascade) — what THIS tenant has set vs inherited. The
  // editor reads it to seed its per-field set/inherit state. Null pre-fetch.
  brandingOverride: TenantBranding | null;
}

// The context exposes the tenant plus a write-through setter, so the branding
// editor can push the mutation's freshly-resolved tenant straight into cache
// (ADR-038 §1.2) — the rebrand shows immediately, no refetch race.
interface TenantContextValue {
  tenant: TenantInfo | null;
  setTenant: (tenant: CurrentTenant) => void;
}

const TenantContext = createContext<TenantContextValue>({ tenant: null, setTenant: () => {} });

function toInfo(t: CurrentTenant): TenantInfo {
  return {
    token: t.token,
    name: t.name ?? null,
    description: t.description ?? null,
    branding: t.branding,
    brandingOverride: t.brandingOverride,
  };
}

export function TenantProvider({ children }: { children: ReactNode }) {
  const { claims } = useAuth();
  const token = claims?.tenant ?? null;
  // Cache key is versioned (v2) because the cached shape grew branding fields — a
  // pre-branding cache entry would deserialize with branding/brandingOverride
  // undefined, so bump the key to orphan the old shape rather than trust it.
  const [cached, setCached] = useCachedResource<TenantInfo>(
    token ? `dc-tenant:v2:${token}` : null,
    () => getCurrentTenant().then(toInfo),
  );
  // Fall back to the bare token so the chip paints before the first fetch lands.
  const info =
    cached ??
    (token
      ? { token, name: null, description: null, branding: null, brandingOverride: null }
      : null);

  // Apply the tenant palette/logo/title to the shell whenever the resolved
  // branding changes. The cleanup clears every branding var + resets the title on
  // unmount — so logging out (which unmounts AppLayout → TenantProvider) or leaving
  // for the instance-scoped /admin console reverts the shell to the built-in brand,
  // never leaking the prior tenant's palette/title onto the login or admin screens.
  const branding = info?.branding;
  useEffect(() => {
    applyBranding(branding);
    return () => applyBranding(null);
  }, [branding]);

  return (
    <TenantContext.Provider value={{ tenant: info, setTenant: (t) => setCached(toInfo(t)) }}>
      {children}
    </TenantContext.Provider>
  );
}

export function useCurrentTenant(): TenantInfo | null {
  return useContext(TenantContext).tenant;
}

// useSetCurrentTenant returns the write-through cache setter — used by the
// branding editor to reflect a rebrand immediately (ADR-038 §1.2).
export function useSetCurrentTenant(): (tenant: CurrentTenant) => void {
  return useContext(TenantContext).setTenant;
}
