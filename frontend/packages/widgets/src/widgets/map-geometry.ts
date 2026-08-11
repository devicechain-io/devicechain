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
// 🔴 THIS PACKAGE HARDCODES NO TILE SOURCE, AND THAT IS A DECISION RATHER THAN AN
// OMISSION. The library is not the map — the TILES are, and they carry their own terms,
// so a widget library is the wrong place to choose a provider on an operator's behalf.
// The URL arrives as config, and with none set the widget falls back to the BUNDLED
// world basemap below — public-domain geometry compiled into this package, which asks
// no host for anything and so adopts nobody's terms. That is the distinction the rule
// is actually about: not "ship no map", but "make no request on an operator's behalf
// to a host they never chose".
//
// The PLATFORM does ship a default (ADR-079): an instance-wide setting a tenant can
// override, which a board then inherits when its own options name nothing. That is a
// named, visible, replaceable choice made once by an operator — a different thing from
// this module quietly baking a host into every build of the library.
//
// `attribution` is passed to the source rather than hidden: a tile source's licence
// commonly REQUIRES visible credit, so it is a property of the data, not decoration a
// host may strip.
export function rasterStyleFor(tileUrl: string, attribution: string | undefined): unknown {
  return {
    version: 8,
    projection: PROJECTION,
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

// ---- The bundled world basemap ----------------------------------------------

// 🔴 THE PROJECTION IS THE CORRECTNESS PROPERTY OF THIS WHOLE FILE, and it is
// declared explicitly — on BOTH styles — rather than left to the default.
//
// The fence editor stores the coordinates a click produces, so what a style
// changes must be what is DRAWN and never where a click LANDS. Mercator is
// MapLibre's default, so writing it out changes no behaviour today. What it buys
// is that the two styles now state the same thing in the same words, and a test
// can compare them: an invariant nobody wrote down is an invariant a later edit
// breaks without anything going red.
//
// This is not hypothetical in MapLibre 6. `projection` is a root style key whose
// `type` is a ZOOM-INTERPOLATABLE expression, so a style is entitled to hand back
// a globe past some zoom. A globe re-projects the pointer; every ring an author
// then drew would be saved somewhere they never pointed at, on a map that looked
// beautiful throughout.
const PROJECTION = { type: 'mercator' } as const;

// The bundled basemap's credit line. Natural Earth requires NO attribution — its
// licence says in as many words that crediting the authors is unnecessary — so
// this is their own suggested short citation, shown for a reason of ours: a
// viewer has to be able to tell a schematic bundled world apart from a configured
// provider, or "no tiles" reads as "broken" all over again.
export const BUNDLED_BASEMAP_ATTRIBUTION = 'Made with Natural Earth';

// The bundled style's palette. Literal colours, not theme tokens: a MapLibre style
// document is JSON handed to a WebGL renderer, and it does not resolve the CSS
// custom properties the rest of this package themes with. These sit close to the
// dark surface the fence editor already drew, so the change reads as "the void
// grew continents" rather than as a new skin.
const OCEAN = '#0b1220';
const LAND = '#1e293b';
const BOUNDARY = '#475569';

// landStyleFrom builds the bundled world style from Natural Earth geometry.
//
// Kept separate from the loader, and pure, so the style document can be asserted
// without pulling 150 KiB of coordinates into a test — and so the one thing worth
// asserting about it (that it projects exactly as the tiled style does) is
// checkable in isolation.
export function landStyleFrom(land: unknown, boundaries: unknown): unknown {
  return {
    version: 8,
    projection: PROJECTION,
    sources: {
      land: { type: 'geojson', data: land, attribution: BUNDLED_BASEMAP_ATTRIBUTION },
      boundaries: { type: 'geojson', data: boundaries },
    },
    layers: [
      { id: 'ocean', type: 'background', paint: { 'background-color': OCEAN } },
      { id: 'land', type: 'fill', source: 'land', paint: { 'fill-color': LAND } },
      {
        id: 'boundaries',
        type: 'line',
        source: 'boundaries',
        paint: { 'line-color': BOUNDARY, 'line-width': 0.5 },
      },
    ],
  };
}

// loadLandStyle fetches the bundled geometry and builds its style.
//
// 🔴 THE DYNAMIC IMPORT IS THE WHOLE POINT. natural-earth-data is ~150 KiB (~44
// KiB over the wire) and it must never enter this package's entry chunk: with a
// tile source configured — which, since the platform ships a default provider, is
// the ordinary case — nobody should download a world map they will not see. A
// static `import { NATURAL_EARTH }` at the top of this file would give every
// viewer the payload and change nothing observable, which is how it would survive
// review.
//
// Memoized on the promise, so several map widgets on one board parse it once.
//
// 🔴 That memoizes a REJECTION too: one transient chunk failure leaves every
// unconfigured map on the page falling back until a reload. Deliberate, and the same
// bargain loadMapLibre has always made — a retry-on-failure cache would let N widgets
// re-request a chunk that is failing for a reason retrying will not fix. Worth knowing
// that the blast radius grew when this became the DEFAULT unconfigured path rather
// than a rarity.
let landStylePromise: Promise<unknown> | undefined;

export function loadLandStyle(): Promise<unknown> {
  if (!landStylePromise) {
    landStylePromise = import('./natural-earth-data').then((mod) =>
      landStyleFrom(mod.NATURAL_EARTH.land, mod.NATURAL_EARTH.boundaries),
    );
  }
  return landStylePromise;
}
