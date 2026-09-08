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
import { applyTenantDefaultLocale, resetToDetectedLocale } from '@/i18n/config';
import { MapRuntimeProvider, TenantBasemapProvider } from '@devicechain/widgets';

import { MAP_RUNTIME } from '../map-runtime';

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
  // The instance default ships a real tile source, so this normally arrives set —
  // but every field inside may still be null, because an operator can clear it. The
  // map surfaces then draw a plain panel rather than inventing a provider.
  basemap: TenantBasemap | null;
  // The RAW basemap override, for the editor's set-vs-inherited display.
  basemapOverride: TenantBasemap | null;
  // The tenant's EFFECTIVE default language (its own override folded over the
  // operator's instance default), or null when neither tier sets one. Null is also
  // what the pre-fetch fallback carries, and the two are indistinguishable here — the
  // effect below treats both as "nothing to apply", which is correct for the first
  // and a brief English shell for the second.
  locale: string | null;
  // The RAW locale override, for the editor's set-vs-inherited display.
  localeOverride: string | null;
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
    locale: t.locale ?? null,
    localeOverride: t.localeOverride ?? null,
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
  //
  // v4 adds the default locale, and it is bumped for CONSISTENCY rather than because
  // this field needs it — worth saying plainly, because the version above is the kind
  // of thing a later reader assumes is load-bearing everywhere. A stale v3 entry
  // deserializes with locale undefined, which reads as null, which is exactly what an
  // unset locale means: the seam no-ops and the browser rung answers, until the
  // background refetch lands a moment later. That is the same outcome as not reading
  // the entry at all, so a mutation that reverts this to v3 changes nothing a test can
  // see (measured — it is the one survivor of this slice's mutation run). Bump it
  // anyway: the rule is that the version tracks the SHAPE, and a version that is only
  // bumped when someone can prove it matters is a version nobody trusts.
  const [cached, setCached] = useCachedResource<TenantInfo>(
    token ? `dc-tenant:v4:${token}` : null,
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
          locale: null,
          localeOverride: null,
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

  // Apply the tenant's DEFAULT language whenever the resolved locale changes. This is
  // the one call site: TenantProvider is installed once at the top of the tree, so
  // there is no render site to forget, and the same argument that put both map
  // providers here applies unchanged.
  //
  const locale = info?.locale ?? null;
  useEffect(() => {
    applyTenantDefaultLocale(locale);
  }, [locale]);

  // 🔴 AND HAND IT BACK ON UNMOUNT. An earlier version of this file argued there was
  // "nothing to revert TO, since rungs 1, 3 and 4 are exactly where i18next already
  // is". That was true only while the browser rung was dead: the shipped
  // `locale.default` was "en", so every tenant carried a default and i18next was never
  // sitting on a browser-detected language to begin with. With the shipped default now
  // absent, it routinely is — and without this, logging out of a Spanish-default tenant
  // and into one with no default leaves the console in Spanish against an English
  // browser, because nothing ever moves it back.
  //
  // 🔴 SEPARATE EFFECT, EMPTY DEPS, ON PURPOSE. Hanging the cleanup off the [locale]
  // effect above would run it on every CHANGE as well — React tears down before it
  // re-runs — so each tenant fetch would bounce the language through detection on its
  // way to the value it was about to set. This one runs its cleanup only when the
  // tenant shell itself goes away, which is the event that actually means "this tenant
  // no longer decides".
  //
  // 🔴 Unmount is SUFFICIENT because there is no way to change tenant without one, and
  // that is a property of the route tree rather than a hope: selectTenant is reachable
  // only from /login and from /admin, both of which sit OUTSIDE AppLayout (App.tsx
  // mounts AppLayout and AdminLayout as siblings under separate guards). If a
  // tenant-switcher ever lands inside the tenant shell, this effect stops covering it —
  // the [locale] effect above would no-op on a new tenant that sets no default, leaving
  // the previous tenant's language in force — and the fix is to key this effect on the
  // tenant token rather than to widen its deps.
  useEffect(() => resetToDetectedLocale, []);

  // 🔴 BOTH map providers are installed HERE, once, rather than at each place the
  // console renders widgets — the dashboard workspace, the canvas editor, the
  // synthetic preview. Wrapping render sites individually is the version of this that
  // silently half-works: a new surface renders a map widget, nobody wraps it, and the
  // map is blank on that page only. There is no render site to forget from here.
  //
  // They are NOT the same kind of thing, which is why they nest rather than merge. The
  // basemap is tenant CONFIGURATION and is legitimately absent — no provider means no
  // tenant basemap, a state the widget handles. The map runtime is BUNDLER WIRING and is
  // never legitimately absent: a widget without it shows a notice instead of a map,
  // because the alternative is MapLibre deriving a worker URL that 404s and rendering
  // nothing at all.
  return (
    <TenantContext.Provider value={{ tenant: info, setTenant: (t) => setCached(toInfo(t)) }}>
      <TenantBasemapProvider basemap={info?.basemap ?? null}>
        <MapRuntimeProvider runtime={MAP_RUNTIME}>{children}</MapRuntimeProvider>
      </TenantBasemapProvider>
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

// useTenantLocale returns the tenant's EFFECTIVE default language, or null before the
// first fetch lands.
//
// 🔴 It is NOT "the language the console is in" and no consumer should treat it that
// way — an explicit user choice outranks it and is not visible here. Use
// i18n.resolvedLanguage for the language actually in effect. This exists for the
// EDITOR, which needs to show what the tenant's default currently resolves to.
export function useTenantLocale(): string | null {
  return useContext(TenantContext).tenant?.locale ?? null;
}

// useTenantLocaleOverride returns the RAW stored override — null means "this tenant
// inherits the operator default" — which is what the editor seeds its select from.
export function useTenantLocaleOverride(): string | null {
  return useContext(TenantContext).tenant?.localeOverride ?? null;
}

// useSetCurrentTenant returns the write-through cache setter — used by the
// branding editor to reflect a rebrand immediately (ADR-038 §1.2).
export function useSetCurrentTenant(): (tenant: CurrentTenant) => void {
  return useContext(TenantContext).setTenant;
}
