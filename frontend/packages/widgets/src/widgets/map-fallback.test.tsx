// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The map widget's LAST resort: MapLibre itself could not be loaded.
//
// 🔴 THIS LIVES IN ITS OWN FILE FOR A MECHANICAL REASON, not a stylistic one. Both
// loaders the widget uses — the MapLibre import and the bundled-geometry import —
// memoize their promise at module scope, deliberately, so that several map widgets
// on one board share a single fetch. A memoized SUCCESS can never be turned back
// into a failure, so this path is unreachable in any file where the renderer has
// already loaded once. Vitest isolates module state per test FILE, which is the
// only lever that makes the failure case drivable at all.
//
// What it protects: PlainPanel and projectToPanel look like dead code now that the
// unconfigured branch renders a real map, and deleting them would pass every other
// test in this package. They are what a viewer behind a chunk-blocking proxy, or on
// a flaky connection, still gets — the positions, relative to each other, which is
// the question a map widget exists to answer. An error box answers nothing.

/// <reference types="node" />
import type { LocationSample, WidgetInstance } from '@devicechain/dashboards';
import { cleanup, render as rtlRender, screen, waitFor } from '@testing-library/react';
import type { ReactElement } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

// The renderer is unobtainable in this file. `vi.mock` with a factory that throws is
// what a failed chunk fetch looks like from the widget's side: the dynamic import
// rejects, and nothing about the failure is under the widget's control.
vi.mock('maplibre-gl', () => {
  throw new Error('simulated: the maplibre-gl chunk could not be fetched');
});

import type { LocationStreamState } from '../hooks';
import { MapRuntimeProvider, type MapRuntime } from '../map-runtime-context';
import { MapWidget } from './map';

afterEach(cleanup);

// The host IS wired here — that is the point. This file drives the case where the host did
// everything right and the RENDERER still could not be fetched, which is a different
// failure from an unwired host (covered in map.test.tsx) and has a different answer: the
// plain panel, showing the positions, rather than a notice asking for configuration.
const TEST_RUNTIME: MapRuntime = {
  workerUrl: 'blob:devicechain-test/maplibre-worker-a3f1.mjs',
  loadStyles: () => Promise.resolve(),
};

function render(ui: ReactElement) {
  return rtlRender(<MapRuntimeProvider runtime={TEST_RUNTIME}>{ui}</MapRuntimeProvider>);
}

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
  elevation: null,
  accuracy: null,
  speed: null,
  heading: null,
  occurredTime: '2026-08-09T12:00:00Z',
  ...over,
});

// Two devices at known, DIFFERENT coordinates, so an axis or ordering mistake has
// somewhere to show up.
const twoDevices = (): LocationStreamState => ({
  deviceTokens: ['dozer-1', 'truck-4'],
  locations: [
    sample({ deviceToken: 'dozer-1', latitude: 33.7468, longitude: -84.3903 }),
    sample({ id: 'loc-2', deviceToken: 'truck-4', latitude: 33.7512, longitude: -84.3841 }),
  ],
  forbidden: false,
  loading: false,
  error: null,
});

describe('MapWidget — the renderer could not be loaded', () => {
  it('falls back to the plain panel rather than to an error', async () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);

    await waitFor(() => expect(screen.getByTestId('map-plain-panel')).toBeTruthy());
    // The positions are what the operator came for; only the basemap failed, and the
    // note says which of the two it was.
    expect(screen.getAllByTestId('map-marker')).toHaveLength(2);
    expect(screen.getByTestId('map-no-tiles-note').textContent).toMatch(/renderer unavailable/i);
  });

  // 🔴 The fallback is only worth keeping while it places markers TRUTHFULLY. A
  // latitude/longitude swap keeps both markers on the panel in a plausible scatter —
  // it is the RELATIVE placement, and only that, which catches it.
  it('places each marker at its own coordinates, east right and north up', async () => {
    render(<MapWidget widget={widget()} data={twoDevices()} />);
    await waitFor(() => expect(screen.getAllByTestId('map-marker')).toHaveLength(2));

    const [dozer, truck] = screen.getAllByTestId('map-marker');
    // truck-4 is east of and north of dozer-1 (larger longitude, larger latitude).
    expect(parseFloat(truck.style.left)).toBeGreaterThan(parseFloat(dozer.style.left));
    expect(parseFloat(truck.style.top)).toBeLessThan(parseFloat(dozer.style.top));
  });

  // A CONFIGURED widget lands here too — the tile source is irrelevant when the thing
  // that would consume it never loaded. Worth its own case because the fallback used
  // to be reachable two ways and it would be easy to leave it wired to only one.
  it('falls back the same way when a tile source WAS configured', async () => {
    render(
      <MapWidget
        widget={widget({ tileUrl: 'https://tiles.example.invalid/{z}/{x}/{y}.png' })}
        data={twoDevices()}
      />,
    );

    await waitFor(() => expect(screen.getByTestId('map-plain-panel')).toBeTruthy());
    expect(screen.getAllByTestId('map-marker')).toHaveLength(2);
    expect(screen.getByTestId('map-no-tiles-note').textContent).toMatch(/renderer unavailable/i);
  });

  // The negative control for this whole file. Every assertion above is about a
  // fallback, and a fallback is only evidence of anything if the thing it falls back
  // FROM genuinely failed. Delete the mock and the three tests above would go from
  // "the fallback works" to "the fallback is unreachable and nothing here measures
  // anything" — with no failure to say so, since a working map renders no panel and
  // `waitFor` would simply time out into a confusing error rather than a clear one.
  //
  // 🔴 The rejection REASON is deliberately not asserted, and that is a limitation of
  // the tool rather than a shortcut: vitest replaces a throwing mock factory's error
  // with its own "there was an error when mocking a module" diagnostic, so the text
  // above never reaches the caller. Verified by trying it, in both the sync and async
  // factory forms. What remains assertable is the property the file actually depends
  // on — that importing the renderer here fails — and that is what is asserted.
  it('the renderer really is unobtainable in this file', async () => {
    await expect(import('maplibre-gl')).rejects.toThrow();
  });
});
