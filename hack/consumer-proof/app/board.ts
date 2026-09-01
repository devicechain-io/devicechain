// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The board both arms render. TWO map widgets, and the second one is not decoration.
//
// 🔴 A RASTER MAP DOES NOT USE MAPLIBRE'S WORKER. Measured, and it invalidated this
// rig's first design: a board with only the raster map below rendered a complete map —
// canvas, four markers, fourteen tiles, zero console errors — while NEVER FETCHING THE
// WORKER URL AT ALL. Every worker assertion passed vacuously, on an arm where the URL
// could have been anything. A rig whose central claim cannot fail is worse than no rig.
//
// So the second widget names no tile source, which drops the widget onto its bundled
// world basemap — a GeoJSON style, and GeoJSON is parsed IN THE WORKER. Its land
// polygons are therefore a direct, positive read of worker output: no worker, no land.
// The ocean layer underneath is a flat `background` paint that renders without one,
// which is what makes the two distinguishable by colour rather than by "is it blank".

import type { DashboardDefinition } from '@devicechain/dashboards';

// The harness serves the built app and the tiles from one origin, so the widget can
// name its tiles relatively and neither arm has a port baked into it.
export const TILE_URL = `${location.origin}/tiles/{z}/{x}/{y}.png`;

// Kept in step with the driver's assertions by hand; the driver names them too.
export const LAND_COLOUR = '#1e293b';
export const OCEAN_COLOUR = '#0b1220';

const FLEET = {
  kind: 'device',
  deviceToken: 'consumer-proof',
  measurements: [],
  // Without this the synthetic source returns the EMPTY snapshot, exactly as the live
  // hub does — and both widgets would render "No devices selected" while the build,
  // the load and the tile fetches all still looked healthy.
  location: { series: 'latest' },
} as const;

export const BOARD: DashboardDefinition = {
  schemaVersion: 1,
  title: 'consumer-proof',
  canvas: {
    grid: { columns: 12, gap: 8, rowHeight: 40 },
    sizing: 'fill',
    breakpoints: { base: 0 },
  },
  widgets: [
    {
      // Proves the map fetched tiles and placed markers: an ordinary configured map.
      id: 'raster-map',
      type: 'map',
      layout: { base: { col: 0, colSpan: 6, row: 0, rowSpan: 10, z: 0 } },
      datasource: { ...FLEET },
      options: { title: 'Raster', tileUrl: TILE_URL, attribution: 'consumer-proof' },
    },
    {
      // Proves the WORKER ran: no tile source, so the bundled GeoJSON world is drawn,
      // and it can only be drawn by a worker that loaded, parsed and tiled it.
      id: 'bundled-map',
      type: 'map',
      layout: { base: { col: 6, colSpan: 6, row: 0, rowSpan: 10, z: 0 } },
      datasource: { ...FLEET },
      options: { title: 'Bundled' },
    },
  ],
};
