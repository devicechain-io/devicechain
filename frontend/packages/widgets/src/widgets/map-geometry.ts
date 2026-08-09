// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The map widget's pure parts: which samples are placeable, where they land on a
// tile-less panel, and how a fix is described in words.
//
// Split out from the component for the usual reason the chart-option builders are —
// this is the arithmetic that decides whether a marker is in the right place, and it is
// worth an awful lot more when it can be asserted directly than when it can only be
// inferred from a rendered `style` string.

import type { LocationSample } from '@devicechain/dashboards';

// A sample that actually has coordinates. A LocationSample's latitude/longitude are
// nullable (the projection's columns are), and a sample missing either is not a
// position — placing it would put a marker at (0, 0) in the Gulf of Guinea and label it
// with a real device's name.
export interface PlaceableLocation extends LocationSample {
  latitude: number;
  longitude: number;
}

// placeable keeps the samples that can be drawn.
//
// `!= null`, NOT truthiness: latitude 0 is the equator and longitude 0 is Greenwich,
// and both are real reported positions. A truthy filter would silently drop a device
// sitting on either line.
export function placeable(samples: readonly LocationSample[]): PlaceableLocation[] {
  return samples.filter(
    (s): s is PlaceableLocation => s.latitude != null && s.longitude != null,
  );
}

// A point on the tile-less panel, as a FRACTION of its width/height, so the component
// can express it as a percentage and stay resolution-independent.
export interface PanelPoint {
  x: number;
  y: number;
}

// Fraction of the panel kept clear at each edge, so a marker at the extreme of the
// bounding box sits inside the panel rather than half-clipped by it.
const PANEL_PADDING = 0.1;

// projectToPanel lays a set of positions out on a plain (tile-less) panel.
//
// An equirectangular fit of the positions' own bounding box: longitude → x, latitude →
// y, each stretched across the padded panel. Deliberately NOT a web-mercator
// projection — without a basemap there is nothing to align to, so the only job is to
// show the relative arrangement of a fleet truthfully, and at the extent of a site or a
// city the difference between the two is invisible. (With a tile source the widget uses
// MapLibre's real projection instead; this function is never asked to agree with one.)
//
// 🔴 THE AXES ARE NOT INTERCHANGEABLE, AND THE Y AXIS IS INVERTED. Longitude is
// east-west (x); latitude is north-south (y); and screen y grows DOWNWARD while
// latitude grows northward, so the maximum latitude maps to y = 0. Swap the two and
// every marker still lands inside the panel, in a plausible-looking scatter — the
// output stays well-formed, which is exactly why this is asserted directly rather than
// eyeballed.
//
// A degenerate axis (one device, or several at the same latitude) has no span to
// stretch across, so it centres on that axis rather than dividing by zero.
export function projectToPanel(positions: readonly PlaceableLocation[]): PanelPoint[] {
  if (positions.length === 0) return [];

  let minLat = Infinity;
  let maxLat = -Infinity;
  let minLon = Infinity;
  let maxLon = -Infinity;
  for (const p of positions) {
    if (p.latitude < minLat) minLat = p.latitude;
    if (p.latitude > maxLat) maxLat = p.latitude;
    if (p.longitude < minLon) minLon = p.longitude;
    if (p.longitude > maxLon) maxLon = p.longitude;
  }

  const lonSpan = maxLon - minLon;
  const latSpan = maxLat - minLat;
  const scale = 1 - 2 * PANEL_PADDING;

  return positions.map((p) => ({
    x: PANEL_PADDING + scale * (lonSpan === 0 ? 0.5 : (p.longitude - minLon) / lonSpan),
    // maxLat at the TOP: screen y grows downward, latitude grows northward.
    y: PANEL_PADDING + scale * (latSpan === 0 ? 0.5 : (maxLat - p.latitude) / latSpan),
  }));
}

// Coordinates are WGS84 decimal degrees platform-wide. Six decimal places is ~0.1 m at
// the equator — finer than any consumer receiver — and fixed notation stops a small
// value rendering in exponential form. Matches the device-detail position panel, so the
// same fix reads identically in both places.
export function formatDegrees(value: number): string {
  return value.toFixed(6);
}

// describePosition renders one fix as a marker's hover/screen-reader text.
//
// 🔴 IT REPORTS ONLY WHAT THE DEVICE REPORTED. Elevation, accuracy, speed and heading
// are each optional, and an absent one contributes NO SEGMENT at all — it is never
// filled in with a zero. A marker reading "0°" claims the device reported due north; a
// marker with no heading segment claims nothing, which is the truth. Every test is
// `!= null`, so a genuine 0 (parked, sea level, due north) is still shown.
//
// Units are fixed platform-wide: metres, metres per second, degrees clockwise from true
// north.
export function describePosition(sample: PlaceableLocation): string {
  const parts: string[] = [
    sample.deviceToken,
    `${formatDegrees(sample.latitude)}, ${formatDegrees(sample.longitude)}`,
  ];
  if (sample.elevation != null) parts.push(`${sample.elevation} m`);
  if (sample.accuracy != null) parts.push(`±${sample.accuracy} m`);
  if (sample.speed != null) parts.push(`${sample.speed} m/s`);
  if (sample.heading != null) parts.push(`${sample.heading}°`);
  return parts.join(' · ');
}

// rasterStyleFor builds the MapLibre style document for a user-supplied raster tile
// source.
//
// 🔴 THERE IS NO DEFAULT TILE SOURCE, AND THAT IS A DECISION RATHER THAN AN OMISSION.
// The library is not the map — the TILES are, and they carry their own terms. Defaulting
// to OpenStreetMap's public tile servers would put every self-hosted instance in breach
// of the OSM tile usage policy the moment it had real traffic, silently, on donated
// bandwidth nobody agreed to spend. So the URL is part of the widget's own config, and
// with none set the widget draws positions on a plain panel and says so. An
// INSTANCE-level default (an operator naming their own tile service once, for every
// board) is a deliberate follow-up, not part of this.
//
// `attribution` is passed to the source rather than hidden: a tile source's licence
// commonly REQUIRES visible credit, so it is a property of the data, not decoration a
// host may strip.
export function rasterStyleFor(tileUrl: string, attribution: string | undefined): unknown {
  return {
    version: 8,
    sources: {
      basemap: {
        type: 'raster',
        tiles: [tileUrl],
        tileSize: 256,
        ...(attribution ? { attribution } : {}),
      },
    },
    layers: [{ id: 'basemap', type: 'raster', source: 'basemap' }],
  };
}
