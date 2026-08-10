// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { createContext, useContext, useEffect, type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthProvider';
import {
  getCurrentTenant,
  type CurrentTenant,
  type TenantBranding,
  type TenantBasemap,
} from '@/lib/api/user-management';
import { useCachedResource } from '@/lib/hooks/use-cached-resource';
import { applyBranding } from '@/lib/branding';
import { TenantBasemapProvider } from '@devicechain/widgets';

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
  // The tenant's EFFECTIVE basemap: its override folded over the operator default.
  // Every field inside may be null — the platform ships no tile source, so an
  // instance nobody has configured resolves to nothing and the map surfaces draw a
  // plain panel rather than inventing a provider.
  basemap: TenantBasemap | null;
  // The RAW basemap override, for the editor's set-vs-inherited display.
  basemapOverride: TenantBasemap | null;
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
    basemap: t.basemap,
    basemapOverride: t.basemapOverride,
  };
}

export function TenantProvider({ children }: { children: ReactNode }) {
  const { claims } = useAuth();
  const token = claims?.tenant ?? null;
  // 🔴 Cache key is versioned, and the version MUST be bumped whenever this shape
  // grows. v2 added branding; v3 adds the basemap. A stale v2 entry deserializes with
  // basemap undefined, which is indistinguishable from "this tenant has no basemap" —
  // so every map surface would render blank for as long as that entry lived, on
  // exactly the instances that had just configured one. Orphan the old shape rather
  // than trust it.
  const [cached, setCached] = useCachedResource<TenantInfo>(
    token ? `dc-tenant:v3:${token}` : null,
    () => getCurrentTenant().then(toInfo),
  );
  // Fall back to the bare token so the chip paints before the first fetch lands.
  const info =
    cached ??
    (token
      ? {
          token,
          name: null,
          description: null,
          branding: null,
          brandingOverride: null,
          basemap: null,
          basemapOverride: null,
        }
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

  // 🔴 The basemap provider is installed HERE, once, rather than at each place the
  // console renders widgets — the dashboard workspace, the canvas editor, the
  // synthetic preview. Wrapping render sites individually is the version of this that
  // silently half-works: a new surface renders a map widget, nobody wraps it, and the
  // map is blank on that page only. There is no render site to forget from here.
  return (
    <TenantContext.Provider value={{ tenant: info, setTenant: (t) => setCached(toInfo(t)) }}>
      <TenantBasemapProvider basemap={info?.basemap ?? null}>{children}</TenantBasemapProvider>
    </TenantContext.Provider>
  );
}

export function useCurrentTenant(): TenantInfo | null {
  return useContext(TenantContext).tenant;
}

// useTenantBasemap returns the tenant's EFFECTIVE basemap, or null before the first
// fetch lands. Every map surface in the console reads it through this one hook, so
// there is a single answer to "what does this tenant's map look like".
//
// 🔴 Null here is AMBIGUOUS: it means either "not fetched yet" or "no basemap
// configured anywhere", and this hook cannot tell them apart. No consumer currently
// distinguishes them either — every map surface treats both as "no basemap", which is
// correct for the second case and, on a cold cache, a brief plain panel for the first.
// The stale-while-revalidate tenant cache makes that flash rare rather than absent.
//
// Do NOT write a comment here promising callers a "pending" state until something
// actually provides one; an earlier version of this comment did, and no code honoured
// it.
export function useTenantBasemap(): TenantBasemap | null {
  return useContext(TenantContext).tenant?.basemap ?? null;
}

// useSetCurrentTenant returns the write-through cache setter — used by the
// branding editor to reflect a rebrand immediately (ADR-038 §1.2).
export function useSetCurrentTenant(): (tenant: CurrentTenant) => void {
  return useContext(TenantContext).setTenant;
}
