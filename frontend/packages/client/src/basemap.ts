// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The client half of the basemap cascade (ADR-079).
//
// The SERVER resolves a tenant's override over the operator's instance default and
// hands back one effective value. This module resolves the last tier — a per-surface
// override, set on the widget or held as a personal preference in the browser — over
// that tenant value, and turns the result into something a renderer can use.
//
// It lives in the SDK rather than in either app because THREE surfaces need the
// identical answer: the console's geofence editor, the dashboard map widget, and the
// `/dash` viewer app, which has its own login and would otherwise be the one place
// that quietly disagrees.

/** A basemap at any tier: the tile source, and a fallback view. */
export interface Basemap {
  tileUrl?: string | null;
  attribution?: string | null;
  centerLat?: number | null;
  centerLon?: number | null;
  zoom?: number | null;
}

/** A resolved basemap that definitely has a tile source, so a renderer can draw it. */
export interface ResolvedBasemap {
  tileUrl: string;
  attribution: string | null;
  centerLat: number | null;
  centerLon: number | null;
  zoom: number | null;
}

/** Treat blank and whitespace-only strings as unset — an emptied form field means "inherit". */
function text(v: string | null | undefined): string | null {
  if (typeof v !== 'string') return null;
  const trimmed = v.trim();
  return trimmed === '' ? null : trimmed;
}

function num(v: number | null | undefined): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null;
}

/**
 * Fold a per-surface override over the tenant's effective basemap.
 *
 * 🔴 The TILE SOURCE is atomic, exactly as it is on the server. If the override names
 * a tile URL, its attribution comes from the override too — never from the tenant. A
 * per-field fold would credit the tenant's provider for the override's tiles, which
 * is a licence violation manufactured by the merge rather than a cosmetic mismatch.
 * This is the same rule as basemap.Merge in user-management, and it has to be
 * restated here because this tier never reaches the server.
 *
 * The camera folds per field: a widget that only wants to open closer in should not
 * have to restate a provider to keep it.
 */
export function resolveBasemap(override: Basemap | null | undefined, tenant: Basemap | null | undefined): Basemap {
  const o = override ?? {};
  const t = tenant ?? {};

  // 🔴 AN INCOMPLETE OVERRIDE IS NOT AN OVERRIDE. A tile URL with no credit line is
  // refused outright by the server at the tenant and instance tiers — the two halves
  // are stored as one value or not at all — but THIS tier never reaches the server: a
  // widget's own options and the fence editor's browser-local fields are entered
  // client-side and rendered directly.
  //
  // Without this the atomic fold below did its job and still produced the defect it
  // exists to prevent: it correctly refused to lend the tenant's credit line to the
  // override's tiles, and then drew those tiles with NO credit line at all. Correct
  // about the cross-crediting, wrong about the licence.
  //
  // So an override naming tiles without their attribution is discarded whole, and the
  // surface falls back to the tenant's properly-credited basemap. Failing closed to a
  // map that is credited beats honouring a preference that cannot legally be shown —
  // and the fallback is a real map, not a blank one.
  const overrideAttribution = text(o.attribution);
  const overrideTiles = overrideAttribution === null ? null : text(o.tileUrl);
  const tileUrl = overrideTiles ?? text(t.tileUrl);
  const attribution = overrideTiles ? overrideAttribution : text(t.attribution);

  // Latitude and longitude move as a pair for the same reason: half a coordinate
  // from each tier names a point neither one chose.
  const hasOverrideCentre = num(o.centerLat) !== null || num(o.centerLon) !== null;
  const centerLat = hasOverrideCentre ? num(o.centerLat) : num(t.centerLat);
  const centerLon = hasOverrideCentre ? num(o.centerLon) : num(t.centerLon);

  return {
    tileUrl,
    attribution,
    centerLat,
    centerLon,
    zoom: num(o.zoom) ?? num(t.zoom),
  };
}

/**
 * Narrow a folded basemap to one that can actually be rendered, or null.
 *
 * A basemap with a camera but no tile source is legitimate — an operator may set a
 * starting view before choosing a provider — but there is nothing to draw, and the
 * caller should say so rather than request tiles from an empty URL.
 */
export function renderableBasemap(b: Basemap | null | undefined): ResolvedBasemap | null {
  const tileUrl = text(b?.tileUrl);
  if (!tileUrl) return null;
  return {
    tileUrl,
    attribution: text(b?.attribution),
    centerLat: num(b?.centerLat),
    centerLon: num(b?.centerLon),
    zoom: num(b?.zoom),
  };
}

/**
 * The fallback view a surface opens at when it has nothing of its own to fit to.
 *
 * 🔴 A FALLBACK, never an override (ADR-079 §5). A surface with real content — an
 * existing fence's ring, a widget's markers — fits that and ignores this entirely.
 * Returns null when no tier declared a centre, so the caller keeps its own default
 * rather than being moved to 0,0.
 */
export function fallbackView(b: Basemap | null | undefined): { center: [number, number]; zoom: number | null } | null {
  const lat = num(b?.centerLat);
  const lon = num(b?.centerLon);
  if (lat === null || lon === null) return null;
  return { center: [lon, lat], zoom: num(b?.zoom) };
}
