// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import type { LocationSample } from '@devicechain/dashboards';
import { describe, expect, it } from 'vitest';

import {
  describePosition,
  formatDegrees,
  placeable,
  projectToPanel,
  rasterStyleFor,
  type PlaceableLocation,
} from './map-geometry';

function sample(over: Partial<LocationSample> = {}): LocationSample {
  return {
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
  };
}

const at = (deviceToken: string, latitude: number, longitude: number): PlaceableLocation =>
  ({ ...sample({ deviceToken }), latitude, longitude }) as PlaceableLocation;

describe('placeable', () => {
  it('keeps only the samples that actually carry coordinates', () => {
    const kept = placeable([
      sample({ deviceToken: 'a' }),
      sample({ deviceToken: 'b', latitude: null }),
      sample({ deviceToken: 'c', longitude: null }),
      sample({ deviceToken: 'd', latitude: null, longitude: null }),
    ]);
    expect(kept.map((s) => s.deviceToken)).toEqual(['a']);
  });

  // 🔴 The counterweight, and the reason this is `!= null` and not truthiness: latitude 0
  // is the equator and longitude 0 is Greenwich. A truthy filter drops a device sitting
  // on either line and it disappears from the map with no signal at all.
  it('keeps a device at latitude 0 / longitude 0', () => {
    const kept = placeable([
      sample({ deviceToken: 'equator', latitude: 0 }),
      sample({ deviceToken: 'greenwich', longitude: 0 }),
      sample({ deviceToken: 'null-island', latitude: 0, longitude: 0 }),
    ]);
    expect(kept.map((s) => s.deviceToken)).toEqual(['equator', 'greenwich', 'null-island']);
  });
});

describe('projectToPanel', () => {
  // 🔴 THE AXIS TEST. Longitude is east-west (x) and latitude is north-south (y), and
  // screen y grows DOWNWARD while latitude grows northward — so the northernmost device
  // is at the TOP. Swapping the two axes still puts every marker inside the panel in a
  // plausible scatter, which is exactly why this is asserted on named devices rather
  // than eyeballed on a screenshot.
  it('puts east to the right and north to the top', () => {
    const [southWest, northEast] = projectToPanel([
      at('sw', 10, 10), // southernmost AND westernmost
      at('ne', 20, 20), // northernmost AND easternmost
    ]);
    expect(southWest.x).toBeLessThan(northEast.x); // west is left of east
    expect(southWest.y).toBeGreaterThan(northEast.y); // south is BELOW north
  });

  it('spreads the bounding box across the padded panel', () => {
    const points = projectToPanel([at('w', 0, -10), at('e', 0, 10)]);
    // The extremes land on the padding lines, not on the panel edges, so a marker at the
    // limit of the fleet is fully visible rather than half-clipped.
    expect(points[0].x).toBeCloseTo(0.1, 6);
    expect(points[1].x).toBeCloseTo(0.9, 6);
    // Equal latitudes: no span to stretch, so the axis centres rather than dividing by 0.
    expect(points[0].y).toBeCloseTo(0.5, 6);
    expect(points[1].y).toBeCloseTo(0.5, 6);
  });

  it('places a device in the middle of the range', () => {
    const [, middle] = projectToPanel([at('w', 0, 0), at('m', 0, 5), at('e', 0, 10)]);
    expect(middle.x).toBeCloseTo(0.5, 6);
  });

  it('centres a lone device rather than dividing by a zero span', () => {
    expect(projectToPanel([at('only', 33.749, -84.388)])).toEqual([{ x: 0.5, y: 0.5 }]);
  });

  it('returns nothing for no positions', () => {
    expect(projectToPanel([])).toEqual([]);
  });

  it('keeps every point inside the panel', () => {
    const points = projectToPanel([at('a', -33, 151), at('b', 60, -120), at('c', 0, 0)]);
    for (const p of points) {
      expect(p.x).toBeGreaterThanOrEqual(0);
      expect(p.x).toBeLessThanOrEqual(1);
      expect(p.y).toBeGreaterThanOrEqual(0);
      expect(p.y).toBeLessThanOrEqual(1);
    }
  });
});

describe('formatDegrees', () => {
  it('renders six fixed decimal places, matching the device-detail position panel', () => {
    expect(formatDegrees(33.749)).toBe('33.749000');
    expect(formatDegrees(-84.388)).toBe('-84.388000');
  });

  it('does not fall back to exponential notation for a tiny value', () => {
    expect(formatDegrees(0.0000001)).toBe('0.000000');
  });
});

describe('describePosition', () => {
  it('reports every field the device reported', () => {
    const text = describePosition(sample() as PlaceableLocation);
    expect(text).toContain('dozer-1');
    expect(text).toContain('33.749000, -84.388000');
    expect(text).toContain('320.5 m');
    expect(text).toContain('±4.2 m');
    expect(text).toContain('3.5 m/s');
    expect(text).toContain('271.5°');
  });

  // 🔴 ABSENT IS NOT ZERO. A device whose receiver supplies no heading has NOT reported
  // due north, and no speed is not "stationary". The absent field must contribute no
  // segment at all — an operator cannot tell an invented 0 from a measured one.
  it('shows NOTHING for an unreported optional — never a zero', () => {
    const text = describePosition(
      sample({ elevation: null, accuracy: null, speed: null, heading: null }) as PlaceableLocation,
    );
    // Exact equality, not a set of `not.toContain`s: the fields must be ABSENT, and only
    // the whole string can say that. (A `not.toContain('0')` would be self-defeating
    // here anyway — the coordinates are full of zeros.)
    expect(text).toBe('dozer-1 · 33.749000, -84.388000');
    expect(text).not.toContain('°');
    expect(text).not.toContain('m/s');
  });

  // The counterweight: suppressing absences is only correct while a genuine zero
  // survives. Parked (speed 0), sea level (elevation 0) and due north (heading 0) are
  // all real readings, and a filter that dropped them would be the same lie inverted.
  it('shows a genuine zero', () => {
    const text = describePosition(
      sample({ elevation: 0, accuracy: 0, speed: 0, heading: 0 }) as PlaceableLocation,
    );
    expect(text).toContain('0 m');
    expect(text).toContain('±0 m');
    expect(text).toContain('0 m/s');
    expect(text).toContain('0°');
  });
});

describe('rasterStyleFor', () => {
  it('points the basemap at the configured tile URL and nothing else', () => {
    const style = rasterStyleFor('https://tiles.example.invalid/{z}/{x}/{y}.png', '© Example') as {
      sources: { basemap: { tiles: string[]; attribution?: string } };
      layers: { source: string }[];
    };
    expect(style.sources.basemap.tiles).toEqual(['https://tiles.example.invalid/{z}/{x}/{y}.png']);
    expect(style.sources.basemap.attribution).toBe('© Example');
    expect(style.layers.map((l) => l.source)).toEqual(['basemap']);
    // 🔴 No default tile host is smuggled in beside the configured one: the whole
    // document must mention no host but the operator's.
    expect(JSON.stringify(style)).not.toMatch(/openstreetmap|osm\.org|mapbox|maptiler/i);
  });

  // 🔴 THE NO-DEFAULT RED LINE, HELD AT THE FUNCTION THAT BUILDS THE STYLE.
  //
  // This test exists because a mutation survived without it: substituting a public tile
  // host for a blank URL (`tiles: [tileUrl || 'https://tile.openstreetmap.org/...']`)
  // changed nothing any test could see, because the WIDGET happens to short-circuit to
  // the plain panel before it ever calls this. That guard is one edit away from moving,
  // and a fallback hiding behind it would quietly point every self-hosted instance at
  // donated bandwidth. So the rule is held here as well: this function uses EXACTLY the
  // URL it was handed, for every input, and substitutes nothing.
  it('uses exactly the URL it was given and never substitutes a public host', () => {
    for (const url of ['https://tiles.example.invalid/{z}/{x}/{y}.png', 'http://10.0.0.5:8080/{z}/{x}/{y}.png', '']) {
      const style = rasterStyleFor(url, undefined) as { sources: { basemap: { tiles: string[] } } };
      expect(style.sources.basemap.tiles).toEqual([url]);
      expect(JSON.stringify(style)).not.toMatch(
        /openstreetmap|osm\.org|tile\.osm|mapbox|maptiler|stadiamaps|carto(cdn|db)/i,
      );
    }
  });

  it('omits attribution when none is configured rather than inventing one', () => {
    const style = rasterStyleFor('https://tiles.example.invalid/{z}/{x}/{y}.png', undefined) as {
      sources: { basemap: Record<string, unknown> };
    };
    expect(style.sources.basemap).not.toHaveProperty('attribution');
  });
});
