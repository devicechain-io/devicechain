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
  return { Map: FakeMap, Marker: FakeMarker, LngLatBounds: FakeLngLatBounds };
});

import type { LocationStreamState } from '../hooks';
import { MapWidget } from './map';

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

// Network reachers jsdom exposes. The tile-less widget must touch none of them: there is
// no default tile source, so there is no host it would be entitled to ask.
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
  it('an authorized viewer gets markers, not the permission state', () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);

    expect(screen.queryByText('Location not permitted')).toBeNull();
    expect(screen.getAllByTestId('map-marker')).toHaveLength(2);
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

describe('MapWidget — no tile source configured', () => {
  // 🔴 The library is not the map; the TILES are, and they carry separate terms. With no
  // tile URL the widget must still answer "where is my fleet" — on a plain panel, saying
  // why there is no basemap — and must reach no host at all, because there is no host it
  // was given permission to ask.
  it('still renders a marker per located device, on a plain panel, with the affordance', () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);

    // The CONTROL: the markers prove the widget rendered its data, so the absence
    // assertions below are about a settled render and not about a blank first frame.
    const markers = screen.getAllByTestId('map-marker');
    expect(markers).toHaveLength(2);
    expect(markers.map((el) => el.getAttribute('data-device'))).toEqual(['dozer-1', 'truck-4']);

    expect(screen.getByTestId('map-plain-panel')).toBeTruthy();
    expect(screen.getByTestId('map-no-tiles-note').textContent).toMatch(/no tile source configured/i);
  });

  it('issues NO network request and loads NO map library', () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);

    // Anchored on the control first: if the markers are not there, nothing below is
    // evidence of anything — an unrendered widget also makes no requests.
    expect(screen.getAllByTestId('map-marker')).toHaveLength(2);

    expect(fetchSpy).not.toHaveBeenCalled();
    expect(xhrOpenSpy).not.toHaveBeenCalled();
    // The strongest signal jsdom affords: the renderer was never even constructed, so no
    // tile request could be issued from inside it either.
    expect(m.maps).toHaveLength(0);
    expect(m.markers).toHaveLength(0);
    // And nothing in the DOM points at a host the operator did not configure.
    expect(document.body.innerHTML).not.toMatch(/https?:\/\//);
  });

  // A blank tileUrl is what a cleared authoring field leaves behind, and it must be
  // treated as "no tile source" rather than as a source whose URL is the empty string —
  // otherwise the widget reaches the renderer with a style pointing nowhere, which is
  // the "broken/blank map" this design refuses to show.
  it('treats a blank tile URL as no tile source at all', () => {
    render(<MapWidget widget={widget({ tileUrl: '' })} data={twoDevices()} />);

    expect(screen.getAllByTestId('map-marker')).toHaveLength(2); // the control
    expect(screen.getByTestId('map-no-tiles-note')).toBeTruthy();
    expect(m.maps).toHaveLength(0);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('places each marker at its own coordinates, east right and north up', () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);
    const [dozer, truck] = screen.getAllByTestId('map-marker');

    // truck-4 is east of and north of dozer-1 (larger longitude, larger latitude).
    // 🔴 A latitude/longitude swap keeps both markers on the panel in a plausible
    // scatter; only the RELATIVE placement catches it.
    expect(parseFloat(truck.style.left)).toBeGreaterThan(parseFloat(dozer.style.left));
    expect(parseFloat(truck.style.top)).toBeLessThan(parseFloat(dozer.style.top));
  });

  it('describes a marker with only the fields its device reported', () => {
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

    const marker = screen.getByTestId('map-marker');
    const label = marker.getAttribute('aria-label') ?? '';
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

  it('shows loading only while nothing has arrived', () => {
    const { unmount } = render(<MapWidget widget={widget()} data={state({ loading: true })} />);
    expect(screen.getByText('Loading…')).toBeTruthy();
    unmount();

    // Once positions are in hand a later poll must not blank them back to a spinner.
    render(<MapWidget widget={widget()} data={{ ...twoDevices(), loading: true }} />);
    expect(screen.queryByText('Loading…')).toBeNull();
    expect(screen.getAllByTestId('map-marker')).toHaveLength(2);
  });

  it('drops a sample carrying no coordinates rather than placing it at (0, 0)', () => {
    render(
      <MapWidget
        widget={widget()}
        data={state({
          deviceTokens: ['dozer-1', 'ghost'],
          locations: [sample(), sample({ id: 'loc-2', deviceToken: 'ghost', latitude: null, longitude: null })],
        })}
      />,
    );
    const markers = screen.getAllByTestId('map-marker');
    expect(markers).toHaveLength(1);
    expect(markers[0].getAttribute('data-device')).toBe('dozer-1');
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
