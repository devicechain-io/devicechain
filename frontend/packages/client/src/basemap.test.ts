// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, it, expect } from 'vitest';
import { resolveBasemap, renderableBasemap, fallbackView, type Basemap } from './basemap';

// Two DIFFERENT providers throughout: pairing one provider's tiles with the other's
// credit line is the defect these tests exist for, so a fixture with one provider
// could not see it.
const TENANT: Basemap = {
  tileUrl: 'https://tenant.example.invalid/{z}/{x}/{y}.png',
  attribution: '© Tenant Tiles',
  centerLat: 33.75,
  centerLon: -84.39,
  zoom: 10,
};

const WIDGET_TILES = 'https://widget.example.invalid/{z}/{x}/{y}.png';

describe('resolveBasemap — the tile source is atomic', () => {
  // 🔴 The headline property, and the shape a per-field fold gets wrong: the override's
  // tiles must NEVER appear under the tenant's credit line.
  //
  // It is enforced by discarding the incomplete override entirely rather than by
  // rendering the tiles bare. Both readings satisfy "do not cross-credit"; only this
  // one also satisfies "do not show a provider's tiles uncredited", which is the same
  // licence rule seen from the other side. This tier never reaches the server, so it
  // is the only place the rule can be applied.
  it('discards an override tile source that arrives without its credit line', () => {
    const got = resolveBasemap({ tileUrl: WIDGET_TILES }, TENANT);

    expect(got.tileUrl).toBe(TENANT.tileUrl);
    expect(got.attribution).toBe('© Tenant Tiles');
  });

  // The control on the rule above: it must discard for the RIGHT reason. If the fold
  // had simply stopped honouring override tile sources, the test above would still
  // pass and the feature would be gone.
  it('still honours an override that brings both halves', () => {
    const got = resolveBasemap({ tileUrl: WIDGET_TILES, attribution: '© Widget' }, TENANT);

    expect(got.tileUrl).toBe(WIDGET_TILES);
    expect(got.attribution).toBe('© Widget');
  });

  // An orphan credit line is the mirror image and equally wrong: it would caption the
  // tenant's tiles with a provider that did not serve them.
  it('ignores an override attribution that names no tile source of its own', () => {
    const got = resolveBasemap({ attribution: '© Widget' }, TENANT);

    expect(got.tileUrl).toBe(TENANT.tileUrl);
    expect(got.attribution).toBe('© Tenant Tiles');
  });

  it('inherits BOTH halves when the surface overrides nothing', () => {
    const got = resolveBasemap(null, TENANT);

    expect(got.tileUrl).toBe(TENANT.tileUrl);
    expect(got.attribution).toBe('© Tenant Tiles');
  });

  it('treats a blank override as unset rather than as a choice', () => {
    // An emptied form field must re-inherit, not blank the map.
    const got = resolveBasemap({ tileUrl: '   ', attribution: '' }, TENANT);

    expect(got.tileUrl).toBe(TENANT.tileUrl);
    expect(got.attribution).toBe('© Tenant Tiles');
  });

  it('resolves to nothing when no tier has a tile source', () => {
    expect(resolveBasemap(null, null).tileUrl).toBeNull();
    expect(resolveBasemap({}, {}).tileUrl).toBeNull();
  });
});

describe('resolveBasemap — the camera folds per field', () => {
  it('lets a surface move the zoom without restating a provider', () => {
    const got = resolveBasemap({ zoom: 15 }, TENANT);

    expect(got.tileUrl).toBe(TENANT.tileUrl);
    expect(got.attribution).toBe('© Tenant Tiles');
    expect(got.zoom).toBe(15);
    expect(got.centerLat).toBe(33.75);
  });

  it('moves a coordinate as a pair', () => {
    const got = resolveBasemap({ centerLat: 41.9 }, TENANT);

    expect(got.centerLat).toBe(41.9);
    expect(got.centerLon).toBeNull();
  });

  it('ignores a non-finite override rather than propagating NaN into the camera', () => {
    const got = resolveBasemap({ zoom: Number.NaN, centerLat: Number.POSITIVE_INFINITY }, TENANT);

    expect(got.zoom).toBe(10);
    expect(got.centerLat).toBe(33.75);
  });

  // Zoom 0 is a real value (the whole world). A `||` fallback would discard it.
  it('keeps a zoom of 0', () => {
    expect(resolveBasemap({ zoom: 0 }, TENANT).zoom).toBe(0);
  });

  // 0,0 is a real point in the Gulf of Guinea. Same trap as zoom 0.
  it('keeps a centre at 0,0', () => {
    const got = resolveBasemap({ centerLat: 0, centerLon: 0 }, TENANT);

    expect(got.centerLat).toBe(0);
    expect(got.centerLon).toBe(0);
  });
});

describe('renderableBasemap', () => {
  it('returns null when there is no tile source to draw', () => {
    expect(renderableBasemap({ centerLat: 33.75, centerLon: -84.39, zoom: 10 })).toBeNull();
    expect(renderableBasemap(null)).toBeNull();
    expect(renderableBasemap({ tileUrl: '  ' })).toBeNull();
  });

  it('narrows a tile source to a renderable value', () => {
    const got = renderableBasemap(resolveBasemap(null, TENANT));

    expect(got).not.toBeNull();
    expect(got!.tileUrl).toBe(TENANT.tileUrl);
    expect(got!.attribution).toBe('© Tenant Tiles');
    expect(got!.zoom).toBe(10);
  });

  it('reports no attribution as null rather than as an empty string', () => {
    // MapLibre shows an empty credit line as an empty attribution control; null lets
    // the caller omit the property entirely.
    const got = renderableBasemap({ tileUrl: WIDGET_TILES, attribution: '' });

    expect(got!.attribution).toBeNull();
  });
});

describe('fallbackView', () => {
  it('is null when no tier declared a centre, so the caller keeps its own default', () => {
    expect(fallbackView({ tileUrl: WIDGET_TILES })).toBeNull();
    expect(fallbackView(null)).toBeNull();
  });

  it('is null on half a coordinate rather than defaulting the other half to 0', () => {
    expect(fallbackView({ centerLat: 33.75 })).toBeNull();
    expect(fallbackView({ centerLon: -84.39 })).toBeNull();
  });

  // 🔴 MapLibre takes [lng, lat]; every other surface in this codebase says lat first.
  // Getting this backwards puts an Atlanta tenant in the Indian Ocean, and it is the
  // single easiest mistake to make here.
  it('emits the centre in MapLibre order — longitude first', () => {
    const got = fallbackView(TENANT);

    expect(got).not.toBeNull();
    expect(got!.center).toEqual([-84.39, 33.75]);
    expect(got!.zoom).toBe(10);
  });

  it('reports a missing zoom as null rather than inventing one', () => {
    expect(fallbackView({ centerLat: 1, centerLon: 2 })!.zoom).toBeNull();
  });
});
