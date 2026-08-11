// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THE PROJECTION IS THE CORRECTNESS PROPERTY OF THE FENCE EDITOR, AND THIS FILE
// EXISTS TO PIN IT.
//
// The editor stores the coordinates a click produces. So when the unconfigured
// basemap changed from an empty void to the bundled Natural Earth world, the thing
// that had to be proved was NOT that continents appear — it was that a click still
// lands exactly where it used to. Those are different claims, and only one of them
// is about whether a saved fence is in the right place.
//
// A test asserting "the land layer is present" would pass a build that had silently
// broken authoring. So the assertions here are deliberately about the two things a
// style change could actually move:
//
//   1. the stored VERTEX, driven through the editor's real click handler with an
//      identical event under both styles, and
//   2. the CAMERA the map is constructed with, which is the other half of the
//      pixel→coordinate mapping.
//
// Both are compared BETWEEN the two styles rather than to a hardcoded expectation.
// A test that only checked "the bundled style yields 12.4924, 41.8902" would keep
// passing if the tiled path drifted away from it, and the property that matters is
// that the two agree.

import { cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The MapLibre stub records what the editor built and every handler it registered,
// so the real handler can be driven directly. jsdom has no WebGL; nothing here is
// painted, and nothing here needs to be — the question is what the editor DECIDED,
// not what a GPU would have drawn.
const rec = vi.hoisted(() => ({
  maps: [] as Array<{
    options: Record<string, unknown>;
    handlers: Array<{ event: string; layer?: string; fn: (e: unknown) => void }>;
  }>,
}));

vi.mock('maplibre-gl', () => {
  class FakeMap {
    entry: (typeof rec.maps)[number];
    constructor(options: Record<string, unknown>) {
      this.entry = { options, handlers: [] };
      rec.maps.push(this.entry);
    }
    // MapLibre's real `on` is overloaded: (event, fn) or (event, layerId, fn). The
    // editor registers BOTH forms and their order is load-bearing, so the stub keeps
    // them distinguishable rather than flattening them together.
    on(event: string, layerOrFn: unknown, maybeFn?: unknown) {
      const layer = typeof layerOrFn === 'string' ? layerOrFn : undefined;
      const fn = (typeof layerOrFn === 'function' ? layerOrFn : maybeFn) as (e: unknown) => void;
      this.entry.handlers.push({ event, layer, fn });
    }
    addControl() {}
    addSource() {}
    addLayer() {}
    getLayer() {
      return undefined;
    }
    queryRenderedFeatures() {
      return [];
    }
    project() {
      return { x: 0, y: 0 };
    }
    fitBounds() {}
    remove() {}
  }
  class FakeNavigationControl {}
  return { Map: FakeMap, NavigationControl: FakeNavigationControl, setWorkerUrl: () => {} };
});

// 🔴 THESE TWO MOCKS ARE NOT DECORATION — WITHOUT THEM THIS FILE MEASURES NOTHING.
//
// The editor's loader imports MapLibre's worker as an asset URL and its stylesheet
// alongside the library, and BOTH are files inside node_modules. The console's Vite
// test environment refuses to serve a node_modules file as an asset URL: the import
// rejects with "Denied ID". Measured, not assumed — an equivalent `?worker&url`
// import from the app's own src/ resolves fine, and the plain `?url` form of the
// same node_modules file is refused too, so it is the LOCATION that is denied and
// not the worker syntax.
//
// The consequence is quiet and total: `loadMapLibre()` rejects, FenceMap lands on
// its `fence-map-failed` branch, and no map is ever constructed. Every assertion
// below would then be waiting on a map that is never coming — which is how the
// first run of this file failed, and why it is worth writing down. It also means
// the editor's map lifecycle had no console-side coverage at all before this file.
//
// Mocking them here is honest rather than a workaround: the real worker URL wiring
// is a bundler concern, and it already has a dedicated guard where it belongs — the
// widgets package asserts setWorkerUrl is handed a bundler-emitted URL. Nothing in
// THIS file is about that.
vi.mock('maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url', () => ({
  default: '/assets/maplibre-gl-worker.js',
}));
vi.mock('maplibre-gl/dist/maplibre-gl.css', () => ({}));

import { FenceMap } from './FenceMap';
import { toVertex } from './geometry';

afterEach(cleanup);
beforeEach(() => {
  rec.maps.length = 0;
});

const TILE_URL = 'https://tiles.example.invalid/{z}/{x}/{y}.png';

// A click somewhere with a decisive sign on both axes — east of Greenwich and north
// of the equator — so a swapped or negated axis has nowhere to hide. The Colosseum,
// give or take.
const CLICK = { lng: 12.4924, lat: 41.8902 };

// The event MapLibre hands a click handler, in the shape the editor consumes it:
// `lngLat.wrap()` and `point`. Building it here rather than dispatching a DOM event
// is what makes this a test of the editor's DECISION — the pixel→lngLat step is
// MapLibre's own and is not what changed.
function clickEvent() {
  return {
    lngLat: { wrap: () => ({ lng: CLICK.lng, lat: CLICK.lat }) },
    point: { x: 400, y: 300 },
    originalEvent: { button: 0 },
  };
}

// Render the editor, wait for its map, and return what a single click on empty
// canvas caused it to store.
async function firstVertexFrom(props: { tileUrl?: string }) {
  const onChange = vi.fn();
  render(<FenceMap vertices={[]} onChange={onChange} {...props} />);
  await waitFor(() => expect(rec.maps).toHaveLength(1));

  const map = rec.maps[0];
  // The GENERAL click handler — the one registered without a layer. Selecting it by
  // shape rather than by index, so a future handler added above it does not silently
  // turn this into a test of something else.
  const handler = map.handlers.find((h) => h.event === 'click' && !h.layer);
  expect(handler, 'the editor registered no general click handler').toBeTruthy();

  handler!.fn(clickEvent());
  return { onChange, options: map.options };
}

describe('the bundled basemap does not move where a click lands', () => {
  it('stores the same vertex whether or not a tile source is configured', async () => {
    const bundled = await firstVertexFrom({});
    cleanup();
    rec.maps.length = 0;
    const tiled = await firstVertexFrom({ tileUrl: TILE_URL });

    // The control: a click has to have produced a corner at all, or "they match"
    // would be two nothings agreeing.
    expect(bundled.onChange).toHaveBeenCalledTimes(1);
    expect(tiled.onChange).toHaveBeenCalledTimes(1);

    const bundledRing = bundled.onChange.mock.calls[0][0];
    const tiledRing = tiled.onChange.mock.calls[0][0];

    // 🔴 THE ASSERTION THIS FILE IS FOR.
    expect(bundledRing).toEqual(tiledRing);
    // …and it is the coordinate that was clicked, not some other coordinate the two
    // paths happen to share. Driven through toVertex so the rounding this platform
    // applies is part of what is being pinned rather than duplicated as a literal.
    expect(bundledRing).toEqual([toVertex(CLICK.lng, CLICK.lat)]);
  });

  it('opens both styles on the same camera', async () => {
    const bundled = await firstVertexFrom({});
    cleanup();
    rec.maps.length = 0;
    const tiled = await firstVertexFrom({ tileUrl: TILE_URL });

    // The camera is the other half of the pixel→coordinate mapping, so it is
    // compared for the same reason the vertex is. `style` is the ONE key that is
    // meant to differ; everything else the editor passes must not.
    const cameraOf = (options: Record<string, unknown>) => {
      const { style: _style, container: _container, ...rest } = options;
      return rest;
    };
    expect(cameraOf(bundled.options)).toEqual(cameraOf(tiled.options));

    // 🔴 renderWorldCopies:false is named explicitly because it is not cosmetic: with
    // world copies on, a click on a repeated world returns an unwrapped longitude —
    // 372.49 for Rome one world over — which the form then rejects as out of range,
    // blaming the operator for clicking land the map itself drew. Both paths must
    // carry it, and the equality above would be satisfied by both being WRONG.
    expect(bundled.options.renderWorldCopies).toBe(false);
    expect(tiled.options.renderWorldCopies).toBe(false);
  });

  // 🔴 THE CREDIT LINE, WHICH THIS SLICE FIXED BY ACCIDENT AND SO HAD BETTER PIN.
  //
  // The editor used to pass `attributionControl: tileUrl ? undefined : false`. That
  // reads as "MapLibre's default when a provider is configured", and it is not what
  // it does: MapLibre resolves options as `{...defaultOptions, ...options}`, and a
  // spread copies an explicitly-undefined property over the default, after which
  // `if (resolvedOptions.attributionControl)` is falsy. The control was therefore
  // suppressed in BOTH branches — the fence editor had never shown its tile
  // provider's credit line, which OpenStreetMap's tile usage policy requires of us.
  //
  // Omitting the key is what actually gets the default, so the assertion is an
  // ABSENCE and reads oddly on purpose. `toHaveProperty` rather than a truthiness
  // check, because `attributionControl: undefined` — the exact bug — is present as a
  // key while being falsy, and only the property check can tell the two apart.
  it('suppresses nobody\'s attribution: the control is left at its default', async () => {
    await firstVertexFrom({ tileUrl: TILE_URL });
    expect(rec.maps[0].options).not.toHaveProperty('attributionControl');
    cleanup();
    rec.maps.length = 0;

    await firstVertexFrom({});
    expect(rec.maps[0].options).not.toHaveProperty('attributionControl');
  });

  // The negative control. Every assertion above is an EQUALITY between two renders,
  // and an equality proves nothing if the two renders were never actually different —
  // if `tileUrl` were ignored, both would build the identical style and the whole
  // file would pass while measuring one code path twice.
  it('the two renders really did build different styles', async () => {
    await firstVertexFrom({});
    cleanup();
    const bundledStyle = rec.maps[0].options.style as { sources: Record<string, unknown> };
    rec.maps.length = 0;
    await firstVertexFrom({ tileUrl: TILE_URL });
    const tiledStyle = rec.maps[0].options.style as {
      sources: Record<string, { tiles?: string[] }>;
    };

    expect(bundledStyle).not.toEqual(tiledStyle);
    // Named concretely, so this fails loudly if the bundled path ever silently
    // becomes a raster style pointing at a provider nobody chose.
    expect(Object.keys(bundledStyle.sources).sort()).toEqual(['boundaries', 'land']);
    expect(tiledStyle.sources.basemap?.tiles).toEqual([TILE_URL]);
  });
});
