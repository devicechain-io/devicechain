// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The basemap a geofence map draws against, shared by every surface that shows one (the
// Basic tab's editor and the History tab's read-only picture), so they cannot disagree
// about which tiles a fence is drawn on.
import { renderableBasemap, resolveBasemap } from '@devicechain/client';
import type { TenantBasemap } from '@/lib/api/user-management';

export const TILE_URL_KEY = 'dc.geofence.tileUrl';
export const TILE_ATTRIBUTION_KEY = 'dc.geofence.tileAttribution';

export function readPreference(key: string): string {
  try {
    return window.localStorage.getItem(key) ?? '';
  } catch {
    // A browser with storage disabled must still be able to draw a fence.
    return '';
  }
}

export function writePreference(key: string, value: string) {
  try {
    if (value) window.localStorage.setItem(key, value);
    else window.localStorage.removeItem(key);
  } catch {
    /* preference is a convenience; losing it must never fail a save */
  }
}

/**
 * The basemap to draw: this browser's personal override, else the tenant's, else nothing.
 * resolveBasemap is not a per-field fold and an override missing its credit line is
 * discarded whole, so this never renders uncredited tiles.
 */
export function resolveFenceBasemap(
  tenantBasemap: TenantBasemap | null,
  override: { tileUrl: string; attribution: string } = {
    tileUrl: readPreference(TILE_URL_KEY),
    attribution: readPreference(TILE_ATTRIBUTION_KEY),
  },
) {
  return renderableBasemap(resolveBasemap(override, tenantBasemap));
}
