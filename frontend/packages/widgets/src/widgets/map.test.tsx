// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Reads this package's own sources off disk for the lazy-boundary guard at the bottom.
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import type { LocationSample, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// jsdom has no WebGL, so MapLibre is stubbed — as ECharts is in the chart tests. The
// stub still receives the real arguments the widget builds (the style document, and each
// marker's [lng, lat]), so the map's CONFIGURATION is exercised even though nothing is
// painted. `m` is the recording registry the stub writes into.
const m = vi.hoisted(() => ({
  maps: [] as Array<{ style: unknown; removed: boolean }>,
  markers: [] as Array<{ lngLat: [number, number]; element: HTMLElement; removed: boolean }>,
  // The worker URL the loader handed MapLibre. Recorded rather than ignored: see
  // the test at the bottom of this file for why a fake that swallowed this call
  // would be hiding the one failure mode nothing else here can see.
  workerUrls: [] as string[],
}));

vi.mock('maplibre-gl', () => {
  class FakeMap {
    style: unknown;
    constructor(options: { container: HTMLElement; style: unknown }) {
      this.style = options.style;
      m.maps.push({ style: options.style, removed: false });
      // Attach something to the container so a "the map mounted" assertion is possible.
      const canvas = document.createElement('div');
      canvas.setAttribute('data-testid', 'fake-maplibre-canvas');
      options.container.appendChild(canvas);
    }
    jumpTo() {}
    fitBounds() {}
    remove() {
      const entry = m.maps.find((x) => x.style === this.style);
      if (entry) entry.removed = true;
    }
  }
  class FakeMarker {
    private entry: { lngLat: [number, number]; element: HTMLElement; removed: boolean };
    constructor(options: { element: HTMLElement }) {
      this.entry = { lngLat: [NaN, NaN], element: options.element, removed: false };
      m.markers.push(this.entry);
    }
    setLngLat(lngLat: [number, number]) {
      this.entry.lngLat = lngLat;
      return this;
    }
    addTo(map: { style: unknown }) {
      void map;
      return this;
    }
    remove() {
      this.entry.removed = true;
    }
  }
  class FakeLngLatBounds {
    extend() {
      return this;
    }
  }
  return {
    Map: FakeMap,
    Marker: FakeMarker,
    LngLatBounds: FakeLngLatBounds,
    setWorkerUrl: (url: string) => m.workerUrls.push(url),
  };
});

import type { LocationStreamState } from '../hooks';
import { TenantBasemapProvider } from '../basemap-context';
import { MapWidget } from './map';
import { BUNDLED_BASEMAP_ATTRIBUTION, landStyleFrom, rasterStyleFor } from './map-geometry';

afterEach(cleanup);

const TILE_URL = 'https://tiles.example.invalid/{z}/{x}/{y}.png';

const widget = (options: Record<string, unknown> = {}): WidgetInstance => ({
  id: 'w',
  type: 'map',
  layout: { base: { col: 0, colSpan: 6, row: 0, rowSpan: 6, z: 1 } },
  datasource: { kind: 'device', deviceToken: 'dozer-1', measurements: [], location: { series: 'latest' } },
  options,
});

const sample = (over: Partial<LocationSample> = {}): LocationSample => ({
  id: 'loc-1',
  deviceToken: 'dozer-1',
  latitude: 33.749,
  longitude: -84.388,
  elevation: 320.5,
  accuracy: 4.2,
  speed: 3.5,
  heading: 271.5,
  occurredTime: '2026-08-09T12:00:00Z',
  ...over,
});

const state = (over: Partial<LocationStreamState> = {}): LocationStreamState => ({
  locations: [],
  deviceTokens: [],
  forbidden: false,
  loading: false,
  error: null,
  ...over,
});

// A settled state carrying two devices at known, DIFFERENT coordinates, so an
// axis/ordering mistake has somewhere to show up.
const twoDevices = () =>
  state({
    deviceTokens: ['dozer-1', 'truck-4'],
    locations: [
      sample({ deviceToken: 'dozer-1', latitude: 33.7468, longitude: -84.3903 }),
      sample({ id: 'loc-2', deviceToken: 'truck-4', latitude: 33.7512, longitude: -84.3841 }),
    ],
  });

// Network reachers jsdom exposes. The tile-less widget must touch none of them: this
// package hardcodes no tile source, so given none it has no host it may ask.
let fetchSpy: ReturnType<typeof vi.fn>;
let xhrOpenSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  m.maps.length = 0;
  m.markers.length = 0;
  fetchSpy = vi.fn(() => Promise.reject(new Error('no test may reach the network')));
  vi.stubGlobal('fetch', fetchSpy);
  xhrOpenSpy = vi.spyOn(XMLHttpRequest.prototype, 'open');
});

afterEach(() => {
  vi.unstubAllGlobals();
  xhrOpenSpy.mockRestore();
});

describe('MapWidget — the refusal is a state of its own', () => {
  // 🔴 THE HEADLINE. location:read is deliberately outside the read-only viewer
  // baseline, so an ordinary member IS refused, routinely. "No devices to show" and "you
  // are not allowed to see this" are opposite facts and only one of them is actionable —
  // a refused viewer must never be shown an empty map.
  it('renders a permission state, NOT an empty map', () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={state({ forbidden: true })} />);

    expect(screen.getByText('Location not permitted')).toBeTruthy();
    expect(screen.getByText(/do not have permission/i)).toBeTruthy();
    // …and none of the empty/loading wordings, which would blame the devices.
    expect(screen.queryByText('No positions reported')).toBeNull();
    expect(screen.queryByText('No devices selected')).toBeNull();
    // No map is drawn and no marker is placed.
    expect(screen.queryAllByTestId('map-marker')).toHaveLength(0);
    expect(m.maps).toHaveLength(0);
  });

  // The counterweight. Rendering a permission wall is only correct while an AUTHORIZED
  // viewer actually gets their markers — a widget that always refused would pass the
  // test above and be useless.
  it('an authorized viewer gets markers, not the permission state', async () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />);

    expect(screen.queryByText('Location not permitted')).toBeNull();
    // MapLibre owns the DOM its markers live in, so they are counted through the
    // stub's registry rather than through the document.
    await waitFor(() => expect(m.markers).toHaveLength(2));
  });

  // A refusal that arrived WITH positions still in hand must clear them: the hub already
  // does that, and the widget must not render coordinates behind a permission banner
  // either. Checked here because the two together are what a viewer sees.
  it('shows no coordinates alongside the refusal', () => {
    render(
      <MapWidget widget={widget()} data={{ ...twoDevices(), forbidden: true, locations: [] }} />,
    );
    expect(screen.getByText('Location not permitted')).toBeTruthy();
    expect(screen.queryAllByTestId('map-marker')).toHaveLength(0);
  });
});

describe('MapWidget — no tile source: the bundled world basemap', () => {
  // 🔴 THE HEADLINE OF THIS SLICE. With no tile URL the widget renders a REAL
  // MapLibre map against public-domain Natural Earth geometry compiled into this
  // package. It used to render a flat panel and a note, which was honest but read
  // as breakage — a void is indistinguishable from a failure, and that ambiguity
  // is the whole reason this exists.
  //
  // The two properties that make it safe are asserted separately below: it is a
  // real map (same renderer, same projection as the configured path), and it still
  // asks NO host for anything.
  it('renders a real map, not the flat panel', async () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);

    await waitFor(() => expect(m.maps).toHaveLength(1));
    // The control: the markers prove the widget got as far as drawing its data, so
    // the absence assertion below is about a settled render rather than a blank
    // first frame.
    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(m.markers.map((mk) => mk.element.getAttribute('data-device'))).toEqual([
      'dozer-1',
      'truck-4',
    ]);

    // The panel is GONE from this path. It survives only for a renderer that could
    // not load, which map-fallback.test.tsx drives.
    expect(screen.queryByTestId('map-plain-panel')).toBeNull();
    expect(screen.queryByTestId('map-no-tiles-note')).toBeNull();
  });

  it('draws land and boundaries from the bundled geometry', async () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);
    await waitFor(() => expect(m.maps).toHaveLength(1));

    const style = m.maps[0].style as {
      sources: Record<string, { type: string; data?: { features?: unknown[] }; attribution?: string }>;
      layers: Array<{ id: string; type: string }>;
    };
    expect(Object.keys(style.sources).sort()).toEqual(['boundaries', 'land']);
    expect(style.sources.land.type).toBe('geojson');
    expect(style.sources.boundaries.type).toBe('geojson');

    // 🔴 NOT just "a land source exists" — a source wired to an EMPTY collection
    // would satisfy that and render the same void this slice exists to remove. The
    // real dataset is 127 land polygons and 331 boundary lines; asserting a
    // generous floor catches an empty or truncated payload without pinning a number
    // that a data refresh would legitimately move.
    expect(style.sources.land.data?.features?.length ?? 0).toBeGreaterThan(50);
    expect(style.sources.boundaries.data?.features?.length ?? 0).toBeGreaterThan(50);

    expect(style.layers.map((l) => l.id)).toEqual(['ocean', 'land', 'boundaries']);
  });

  // 🔴 THE PROMISE THAT SURVIVED THE REWRITE. The old comment on this widget said it
  // "issues no request to any host" when unconfigured, and justified it by never
  // loading a map library at all. Half of that is now false — the library DOES load —
  // so the half that matters is asserted directly instead of inherited from the other.
  //
  // Natural Earth is compiled into the bundle, so a bundled map reaches nowhere. The
  // strongest form of that available here is over the STYLE DOCUMENT MapLibre was
  // actually handed: a URL of any kind in it would be a host the operator never chose.
  it('issues NO network request and names NO host, though it now does load the renderer', async () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);

    // Anchored on the control first: if the map was never built, nothing below is
    // evidence of anything — an unrendered widget also makes no requests.
    await waitFor(() => expect(m.markers).toHaveLength(2));

    expect(fetchSpy).not.toHaveBeenCalled();
    expect(xhrOpenSpy).not.toHaveBeenCalled();
    expect(JSON.stringify(m.maps[0].style)).not.toMatch(/https?:\/\//);
    expect(document.body.innerHTML).not.toMatch(/https?:\/\//);
  });

  // Natural Earth requires no attribution — its licence says crediting the authors is
  // unnecessary — so this credit is OURS, and it earns its place: without it a viewer
  // cannot tell a deliberately schematic bundled world from a provider that failed to
  // load, which is the same ambiguity in a new costume.
  it('credits the bundled data so it cannot be mistaken for a broken provider', async () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);
    await waitFor(() => expect(m.maps).toHaveLength(1));

    const style = m.maps[0].style as { sources: { land: { attribution?: string } } };
    expect(style.sources.land.attribution).toBe(BUNDLED_BASEMAP_ATTRIBUTION);
    expect(style.sources.land.attribution).toMatch(/Natural Earth/);
  });

  // A blank tileUrl is what a cleared authoring field leaves behind, and it must be
  // treated as "no tile source" rather than as a source whose URL is the empty string —
  // otherwise the widget reaches the renderer with a raster style pointing nowhere,
  // which is the broken map this design refuses to show.
  it('treats a blank tile URL as no tile source at all', async () => {
    render(<MapWidget widget={widget({ tileUrl: '' })} data={twoDevices()} />);

    await waitFor(() => expect(m.maps).toHaveLength(1));
    const style = m.maps[0].style as { sources: Record<string, { type: string }> };
    // The bundled world, NOT a raster source with an empty tile template.
    expect(style.sources.basemap).toBeUndefined();
    expect(style.sources.land?.type).toBe('geojson');
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('describes a marker with only the fields its device reported', async () => {
    render(
      <MapWidget
        widget={widget()}
        data={state({
          deviceTokens: ['loader-3'],
          locations: [
            sample({
              deviceToken: 'loader-3',
              latitude: 33.7468,
              longitude: -84.3903,
              elevation: null,
              accuracy: 9.8,
              speed: null,
              heading: null,
            }),
          ],
        })}
      />,
    );

    await waitFor(() => expect(m.markers).toHaveLength(1));
    const label = m.markers[0].element.getAttribute('aria-label') ?? '';
    // The control: the fields it DID report are there…
    expect(label).toContain('loader-3');
    expect(label).toContain('33.746800, -84.390300');
    expect(label).toContain('±9.8 m');
    // …and the unreported ones produce no invented reading. A rendered `0°` would claim
    // this loader is pointing due north.
    expect(label).not.toContain('°');
    expect(label).not.toContain('m/s');
    expect(label).not.toMatch(/\b0 m\b/);
  });
});

// 🔴 THE PROPERTY THIS WHOLE SLICE HAD TO NOT BREAK.
//
// The fence editor stores the coordinates a click produces, so a change to the
// unconfigured STYLE must never change where a click LANDS. Pixels are not the
// correctness property; the projection is. A test asserting "the land layer is
// present" would pass a build that had quietly broken authoring.
//
// MapLibre 6 makes this concrete rather than theoretical: `projection` is a root
// style key whose `type` is a zoom-interpolatable expression, so a style may
// legally hand back a globe past some zoom — which re-projects the pointer. The
// two styles are therefore compared to EACH OTHER, not merely inspected.
//
// FenceMap.test.tsx carries the end of this argument: it drives the editor's real
// click handler through both styles and asserts the SAME stored vertex.
describe('the projection is the same on both styles', () => {
  it('both declare mercator, and declare it identically', () => {
    const empty = { type: 'FeatureCollection', features: [] };
    const tiled = rasterStyleFor(TILE_URL, '© Example') as { projection: unknown };
    const bundled = landStyleFrom(empty, empty) as { projection: unknown };

    expect(bundled.projection).toEqual({ type: 'mercator' });
    // The one that matters: not "each is mercator" checked twice, but that the two
    // AGREE. This is what fails if a later edit gives one of them a globe.
    expect(bundled.projection).toEqual(tiled.projection);
  });
});

// 🔴 THE ONE FAILURE MODE NOTHING ELSE HERE CAN SEE.
//
// MapLibre 6 parses tiles and geometry in a web worker whose URL it derives AT
// RUNTIME from its own module URL. That is a string no bundler can follow, so
// nothing emits the worker file and the browser resolves it to a 404 — and
// `new Worker()` does not throw on one, the failure arrives later as an async
// error event. The result is a map that builds, type-checks, passes every other
// test in this file, and then renders nothing.
//
// The loader defeats that by importing the worker through Vite's `?worker&url`
// entry and handing the emitted URL to `setWorkerUrl`. This is the assertion that
// the loader still does it — deliberately BEFORE anything is drawn, because the
// fake map draws happily either way and would never notice.
describe('the MapLibre worker', () => {
  // 🔴 `workerUrls` is deliberately NOT cleared in the beforeEach above, unlike
  // `maps` and `markers`. The loader memoizes its import for the whole module, so
  // `setWorkerUrl` can only ever be called ONCE per test file — reset the array and
  // every assertion here would depend on being the first test to render a map,
  // which is the kind of order-coupling that reads as a passing test right up until
  // someone reorders the file. Written this way the assertion holds wherever it runs.
  it('is pointed once at a URL the bundler emitted, however many maps a board holds', async () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />);
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />);

    await waitFor(() => expect(m.maps.length).toBeGreaterThanOrEqual(2));

    // Exactly one call for two maps: the memoized loader is what stops several map
    // widgets on one board racing to rewrite a global config.
    expect(m.workerUrls).toHaveLength(1);
    // And it is a real reference, not the empty string MapLibre falls back to when
    // it cannot work one out for itself — which is precisely the broken state.
    expect(m.workerUrls[0]).toBeTruthy();
    expect(typeof m.workerUrls[0]).toBe('string');
  });
});

describe('MapWidget — with a tile source', () => {
  it('builds the basemap from the configured URL and places a marker per device', async () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL, attribution: '© Example' })} data={twoDevices()} />);

    await waitFor(() => expect(m.markers).toHaveLength(2));

    const style = m.maps[0].style as { sources: { basemap: { tiles: string[]; attribution?: string } } };
    expect(style.sources.basemap.tiles).toEqual([TILE_URL]);
    expect(style.sources.basemap.attribution).toBe('© Example');
  });

  // 🔴 setLngLat takes [LONGITUDE, LATITUDE] — the opposite order from how a coordinate
  // is written everywhere else on the platform. Swapping them renders perfectly and puts
  // a device reporting Atlanta into the Indian Ocean.
  it('passes [longitude, latitude] to the marker, in that order', async () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />);

    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(m.markers[0].lngLat).toEqual([-84.3903, 33.7468]);
    expect(m.markers[1].lngLat).toEqual([-84.3841, 33.7512]);
  });

  it('labels each marker element with its device and reported fields', async () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />);

    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(m.markers.map((mk) => mk.element.getAttribute('data-device'))).toEqual(['dozer-1', 'truck-4']);
    expect(m.markers[0].element.getAttribute('aria-label')).toContain('dozer-1');
  });

  it('tears the map and its markers down on unmount', async () => {
    const { unmount } = render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />);
    await waitFor(() => expect(m.markers).toHaveLength(2));

    unmount();
    expect(m.maps.every((x) => x.removed)).toBe(true);
    expect(m.markers.every((x) => x.removed)).toBe(true);
  });

  // A refused viewer must not load a map renderer either: the widget short-circuits
  // before it ever reaches the tiled branch.
  it('loads no renderer when the viewer is refused', () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL })} data={state({ forbidden: true })} />);
    expect(screen.getByText('Location not permitted')).toBeTruthy();
    expect(m.maps).toHaveLength(0);
  });
});

describe('MapWidget — the empty and loading states', () => {
  it('distinguishes "nothing is bound" from "nothing has reported"', () => {
    const { unmount } = render(<MapWidget widget={widget()} data={state({ deviceTokens: [] })} />);
    expect(screen.getByText('No devices selected')).toBeTruthy();
    unmount();

    render(<MapWidget widget={widget()} data={state({ deviceTokens: ['dozer-1'], locations: [] })} />);
    expect(screen.getByText('No positions reported')).toBeTruthy();
  });

  it('shows loading only while nothing has arrived', async () => {
    const { unmount } = render(<MapWidget widget={widget()} data={state({ loading: true })} />);
    expect(screen.getByText('Loading…')).toBeTruthy();
    unmount();

    // Once positions are in hand a later poll must not blank them back to a spinner.
    render(<MapWidget widget={widget()} data={{ ...twoDevices(), loading: true }} />);
    expect(screen.queryByText('Loading…')).toBeNull();
    await waitFor(() => expect(m.markers).toHaveLength(2));
  });

  it('drops a sample carrying no coordinates rather than placing it at (0, 0)', async () => {
    render(
      <MapWidget
        widget={widget()}
        data={state({
          deviceTokens: ['dozer-1', 'ghost'],
          locations: [sample(), sample({ id: 'loc-2', deviceToken: 'ghost', latitude: null, longitude: null })],
        })}
      />,
    );
    await waitFor(() => expect(m.markers).toHaveLength(1));
    expect(m.markers[0].element.getAttribute('data-device')).toBe('dozer-1');
  });
});

// ---- The tenant basemap (ADR-079) -------------------------------------------
//
// Precedence is per-widget override → tenant → plain panel. These drive the REAL
// style document the widget hands MapLibre, so they see the tile URL and the credit
// line the map would actually request — not just which branch was taken.

const TENANT_TILES = 'https://tenant.example.invalid/{z}/{x}/{y}.png';

function basemapOf(index = 0) {
  return (m.maps[index].style as { sources: { basemap: { tiles: string[]; attribution?: string } } })
    .sources.basemap;
}

describe('MapWidget — the tenant basemap', () => {
  it('inherits the tenant tile source when the widget configures none', async () => {
    render(
      <TenantBasemapProvider basemap={{ tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' }}>
        <MapWidget widget={widget()} data={twoDevices()} />
      </TenantBasemapProvider>,
    );

    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(basemapOf().tiles).toEqual([TENANT_TILES]);
    expect(basemapOf().attribution).toBe('© Tenant Tiles');
  });

  it('lets a per-widget tile source win over the tenant', async () => {
    render(
      <TenantBasemapProvider basemap={{ tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' }}>
        <MapWidget widget={widget({ tileUrl: TILE_URL, attribution: '© Example' })} data={twoDevices()} />
      </TenantBasemapProvider>,
    );

    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(basemapOf().tiles).toEqual([TILE_URL]);
    expect(basemapOf().attribution).toBe('© Example');
  });

  // 🔴 The licence rule, asserted on the document MapLibre actually renders, and it
  // rules out BOTH failures rather than only the obvious one. A per-field fold would
  // put "© Tenant Tiles" under tiles the tenant never served; applying the widget's
  // URL bare would render a provider's tiles with no credit at all. A widget option
  // is set in a board's config panel and never passes through the server's
  // validation, so an incomplete pair is discarded whole here instead.
  it('ignores a per-widget tile source that carries no credit line of its own', async () => {
    render(
      <TenantBasemapProvider basemap={{ tileUrl: TENANT_TILES, attribution: '© Tenant Tiles' }}>
        <MapWidget widget={widget({ tileUrl: TILE_URL })} data={twoDevices()} />
      </TenantBasemapProvider>,
    );

    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(basemapOf().tiles).toEqual([TENANT_TILES]);
    expect(basemapOf().attribution).toBe('© Tenant Tiles');
  });

  // The counterweight: a tenant that supplies a CENTRE but no tile source is still a
  // tenant with no tile source, and must land on the bundled world rather than on a
  // raster style built from nothing. Without this, "the tenant configured something"
  // and "the tenant configured a PROVIDER" could quietly fold into each other.
  it('falls back to the bundled world when no tier supplies a tile source', async () => {
    render(
      <TenantBasemapProvider basemap={{ centerLat: 33.75, centerLon: -84.39, zoom: 10 }}>
        <MapWidget widget={widget()} data={twoDevices()} />
      </TenantBasemapProvider>,
    );

    await waitFor(() => expect(m.maps).toHaveLength(1));
    const style = m.maps[0].style as { sources: Record<string, { type: string }> };
    expect(style.sources.basemap).toBeUndefined();
    expect(style.sources.land?.type).toBe('geojson');
    expect(screen.queryByTestId('map-plain-panel')).toBeNull();
  });

  // A host that installs no provider must behave exactly as it did before this
  // feature existed — which is what makes widgetlab and the synthetic preview safe.
  it('falls back to per-widget options with no provider at all', async () => {
    render(<MapWidget widget={widget({ tileUrl: TILE_URL, attribution: '© Example' })} data={twoDevices()} />);

    await waitFor(() => expect(m.markers).toHaveLength(2));
    expect(basemapOf().tiles).toEqual([TILE_URL]);
  });
});

// ---- The lazy boundary ------------------------------------------------------
//
// 🔴 THIS IS A SOURCE-LEVEL GUARD, AND IT HAS TO BE, because the defect it catches is
// invisible to every behavioural test in this file.
//
// MapLibre is ~200 KB gzipped, and only a dashboard that actually contains a map widget
// may pay for it. What keeps it out of the main bundle is that the ONLY value-level
// reference to the library is a dynamic `import()`. Add one ordinary top-level import
// anywhere in this package — for a helper, for a constant, for a type someone forgot to
// mark `import type` — and the bundler folds the whole renderer into the entry chunk for
// every console user, including the ones who never open a board with a map on it.
//
// Nothing about the widget's BEHAVIOUR changes when that happens. Every test above still
// passes. The only observable is the shipped bundle, and a bundle is not something a
// unit test can see — so the cause is gated here instead of the effect.
//
// `import type` is explicitly fine: type imports are erased before a bundler ever sees
// them, and this widget uses them for MapLibre's Map/Marker types.
describe('the MapLibre lazy boundary', () => {
  const SRC = join(dirname(fileURLToPath(import.meta.url)), '..');

  function sourceFiles(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) return sourceFiles(full);
      return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [full] : [];
    });
  }

  // A top-level import of the library that is NOT type-only: `import maplibre from …`,
  // `import { Map } from …`, `import * as x from …`, `import '…'`, `export … from …`.
  // 🔴 `[^;]`, NOT `[^;\n]`. A class excluding newlines cannot match a WRAPPED
  // import, and a wrapped import is the likely offender: prettier wraps any
  // binding list past the line width, and a real MapLibre consumer imports
  // several names. The semicolon is what stops the non-greedy match spanning two
  // statements — the newline was never doing that job, only hiding offenders.
  const STATIC_VALUE_IMPORT =
    /^\s*(?:import|export)\s+(?!type\b)[^;]*?from\s*['"]maplibre-gl['"]|^\s*import\s*['"]maplibre-gl['"]/m;

  it('is not crossed by any static import in the package', () => {
    const offenders = sourceFiles(SRC).filter((file) =>
      STATIC_VALUE_IMPORT.test(readFileSync(file, 'utf8')),
    );
    expect(
      offenders,
      'a static import of maplibre-gl folds the whole renderer into the main bundle for every viewer',
    ).toEqual([]);
  });

  // 🔴 The negative control. The check above is an ABSENCE claim, and an absence claim
  // over a file list is worth nothing until the list is shown to be non-empty and to
  // contain the file in question — a rotted path or a broken glob would report "no
  // offenders" forever. So: the scan reaches map.tsx, map.tsx really does reach the
  // library, and it does so through a dynamic import.
  it('the scan actually reaches the file that loads the library', () => {
    const files = sourceFiles(SRC);
    expect(files.length).toBeGreaterThan(5);
    const mapWidget = files.find((f) => f.endsWith(join('widgets', 'map.tsx')));
    expect(mapWidget).toBeTruthy();

    const source = readFileSync(mapWidget as string, 'utf8');
    expect(source).toMatch(/import\(\s*['"]maplibre-gl['"]\s*\)/); // the dynamic load
    expect(source).toMatch(/import type .*from 'maplibre-gl'/); // erased, so allowed
    // And the pattern used above does fire when it should: the type-only line must not
    // match it, but a value import of the same module must.
    expect(STATIC_VALUE_IMPORT.test("import maplibregl from 'maplibre-gl';")).toBe(true);
    expect(STATIC_VALUE_IMPORT.test("import type { Map } from 'maplibre-gl';")).toBe(false);
  });
});

// ---- The SECOND lazy boundary ----------------------------------------------
//
// 🔴 THE SAME DEFECT WITH A DIFFERENT PAYLOAD, and it arrived with the bundled
// world basemap. natural-earth-data is ~150 KiB of coordinates (~44 KiB over the
// wire) and must stay behind the dynamic import in map-geometry.ts, because the
// ordinary case is now a CONFIGURED tile source — the platform ships a default
// provider — and a viewer looking at a provider's tiles must not also download a
// world map they will never be shown.
//
// It is worth its own guard rather than a line in the one above, for the reason
// that makes this class of bug survive review: `natural-earth-data` exports a
// plain constant. Importing it statically is the NATURAL thing to write, reads as
// obviously correct, changes no behaviour whatsoever, and every test in this
// package keeps passing. Only the bundle moves.
describe('the bundled-geometry lazy boundary', () => {
  const SRC = join(dirname(fileURLToPath(import.meta.url)), '..');

  function sourceFiles(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) return sourceFiles(full);
      return /\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name) ? [full] : [];
    });
  }

  // Matches a static import of the data module by any relative spelling, since it
  // could be reached from a sibling or from a subdirectory. `import type` is
  // allowed and excluded here: NaturalEarthData is a type, and a type is erased.
  const STATIC_DATA_IMPORT =
    /^\s*(?:import|export)\s+(?!type\b)[^;]*?from\s*['"][^'"]*natural-earth-data['"]|^\s*import\s*['"][^'"]*natural-earth-data['"]/m;

  it('is not crossed by any static import in the package', () => {
    const offenders = sourceFiles(SRC).filter((file) =>
      STATIC_DATA_IMPORT.test(readFileSync(file, 'utf8')),
    );
    expect(
      offenders,
      'a static import of natural-earth-data puts a world map in the entry chunk for every viewer',
    ).toEqual([]);
  });

  it('the scan reaches the module that loads the geometry, and the pattern can fire', () => {
    const files = sourceFiles(SRC);
    const geometry = files.find((f) => f.endsWith(join('widgets', 'map-geometry.ts')));
    expect(geometry, 'the module that loads the bundled geometry was not found').toBeTruthy();

    const source = readFileSync(geometry as string, 'utf8');
    expect(source).toMatch(/import\(\s*['"]\.\/natural-earth-data['"]\s*\)/);

    // 🔴 The pattern's own negative control. An absence claim proved by a regex is
    // only as good as the regex, and one that matched nothing would sweep clean
    // forever. These are the shapes that must fire…
    expect(STATIC_DATA_IMPORT.test("import { NATURAL_EARTH } from './natural-earth-data';")).toBe(
      true,
    );
    expect(STATIC_DATA_IMPORT.test("import { NATURAL_EARTH } from '../widgets/natural-earth-data';")).toBe(
      true,
    );
    expect(
      STATIC_DATA_IMPORT.test("import {\n  NATURAL_EARTH,\n} from './natural-earth-data';"),
    ).toBe(true);
    // …and these must not: the erased type import, and the dynamic load itself.
    expect(
      STATIC_DATA_IMPORT.test("import type { NaturalEarthData } from './natural-earth-data';"),
    ).toBe(false);
    expect(STATIC_DATA_IMPORT.test("  const d = await import('./natural-earth-data');")).toBe(
      false,
    );
  });

  // The data module is only worth guarding while it is actually large. If a future
  // refresh reduced it to a stub, the guard above would still pass and would be
  // protecting nothing — and, more to the point, the map would be drawing nothing.
  it('the bundled geometry is really the size this boundary exists for', () => {
    const data = readFileSync(join(SRC, 'widgets', 'natural-earth-data.ts'), 'utf8');
    expect(data.length).toBeGreaterThan(100_000);
  });
});
