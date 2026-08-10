// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tenant basemap, threaded to map widgets as ambient environment (ADR-079).
//
// 🔴 Why context rather than a new field on WidgetProps. A widget is contractually a
// pure function of (widget, data): the props carry what the widget SHOWS. A tenant's
// tile source is not data — it is environment, like the theme — and putting it in the
// props would change the contract for all fifteen widgets to serve one of them.
//
// The default is null, deliberately. A host that installs no provider (widgetlab, the
// synthetic preview, every existing test) behaves exactly as before: the map falls
// back to its per-widget options, and to a plain panel when it has none. Adding the
// provider is what turns a tenant's configured basemap on; nothing breaks without it.

import { createContext, useContext, type ReactNode } from 'react';
import type { Basemap } from '@devicechain/client';

const TenantBasemapContext = createContext<Basemap | null>(null);

/**
 * Supplies the tenant's effective basemap to every map widget beneath it.
 *
 * Both hosts install this ONCE, high in the tree, rather than at each place they
 * render a widget: the console from its TenantProvider, the /dash viewer app from the
 * one place it mounts a DashboardRenderer.
 *
 * 🔴 That placement is the whole safety argument, because a host that forgets this
 * does NOT fail loudly — its maps simply render as they did before the tenant setting
 * existed, on that surface only. Wrapping individual render sites is the version that
 * silently half-works; there is no render site to forget from the top of the tree.
 */
export function TenantBasemapProvider({
  basemap,
  children,
}: {
  basemap: Basemap | null;
  children: ReactNode;
}) {
  return <TenantBasemapContext.Provider value={basemap}>{children}</TenantBasemapContext.Provider>;
}

/**
 * The tenant's effective basemap, or null when no host supplied one.
 *
 * Null means "no tenant basemap in force" — either the host installs no provider, or
 * neither the tenant nor the operator has configured one. Both cases resolve the same
 * way downstream (fall back to the per-widget override, then to a plain panel), so
 * callers do not need to distinguish them.
 */
export function useTenantBasemap(): Basemap | null {
  return useContext(TenantBasemapContext);
}
