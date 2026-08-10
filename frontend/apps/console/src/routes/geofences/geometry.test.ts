// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  CLOSE_PIXEL_RADIUS,
  MAX_FENCE_POSITIONS,
  MAX_FENCE_VERTICES,
  MIN_VERTICES,
  POLYGON_2D,
  boundsOf,
  checkGeometry,
  countPositions,
  fromGeometryDocument,
  isCloseGesture,
  roundCoord,
  toGeometryDocument,
  toVertex,
  type Vertex,
} from './geometry';

// 🔴 The fixture is chosen so a longitude/latitude SWAP cannot pass by symmetry.
// Rome: both 12.4924 and 41.8902 are individually valid as either coordinate, so
// no range check can catch a transposition — only an assertion on ORDER can. A
// fixture like San Francisco (lat 37, lng -122) would let the range check take
// the credit and leave the order untested.
const ROME: Vertex = { lng: 12.4924, lat: 41.8902 };

/** A closed, valid triangle in the editor's OPEN-ring model. */
const TRIANGLE: Vertex[] = [
  { lng: 12.4924, lat: 41.8902 },
  { lng: 12.4934, lat: 41.8902 },
  { lng: 12.4929, lat: 41.8912 },
];

/** A well-formed CLOSED ring on the wire — 3 corners plus the repeat. */
const VALID_RING = [
  [12.49, 41.89],
  [12.5, 41.89],
  [12.495, 41.9],
  [12.49, 41.89],
];

function ringOf(doc: string): number[][] {
  return JSON.parse(doc).geometry.coordinates[0];
}

describe('roundCoord', () => {
  it('keeps 7 decimals and drops the eighth', () => {
    expect(roundCoord(12.49241234)).toBe(12.4924123);
    expect(roundCoord(12.49241239)).toBe(12.4924124);
  });

  it('holds at the coordinate extremes without drifting out of range', () => {
    // The property that matters is that rounding can never push a valid
    // coordinate PAST a bound — the shape that produced the heading [0,360)
    // defect elsewhere, where a value inside the range stored as one outside it.
    expect(roundCoord(179.99999999)).toBe(180);
    expect(roundCoord(179.99999999)).toBeLessThanOrEqual(180);
    expect(roundCoord(-89.99999999)).toBeGreaterThanOrEqual(-90);
  });

  it('is idempotent, which is what makes a closing position exactly equal', () => {
    const once = roundCoord(12.49241239);
    expect(roundCoord(once)).toBe(once);
  });
});

describe('toVertex', () => {
  it('takes longitude FIRST, matching every map library and GeoJSON', () => {
    const v = toVertex(12.4924, 41.8902);
    expect(v.lng).toBe(12.4924);
    expect(v.lat).toBe(41.8902);
  });

  it('rounds at ingress so the model never holds unrounded coordinates', () => {
    expect(toVertex(12.49241234, 41.89021239)).toEqual({ lng: 12.4924123, lat: 41.8902124 });
  });
});

describe('countPositions', () => {
  it('counts the closing position the server will count', () => {
    expect(countPositions(TRIANGLE)).toBe(4);
  });

  it('is 0 for an empty ring, not 1 — nothing drawn closes nothing', () => {
    expect(countPositions([])).toBe(0);
  });

  it('agrees with the document actually produced', () => {
    // The counter and the serializer are separate code paths; this is the
    // assertion that keeps them from drifting into disagreement, which is how a
    // budget reads 512 while 513 goes on the wire.
    for (const n of [3, 4, 10]) {
      const vertices = Array.from({ length: n }, (_, i) => ({ lng: 12.49 + i / 1e4, lat: 41.89 }));
      expect(ringOf(toGeometryDocument(vertices))).toHaveLength(countPositions(vertices));
    }
  });
});

describe('toGeometryDocument', () => {
  it('writes [longitude, latitude], not [latitude, longitude]', () => {
    const ring = ringOf(toGeometryDocument([ROME, ...TRIANGLE.slice(1)]));
    expect(ring[0]).toEqual([12.4924, 41.8902]);
    // Stated the other way too, so the intent survives a careless "fix":
    expect(ring[0][0]).toBe(ROME.lng);
    expect(ring[0][1]).toBe(ROME.lat);
  });

  it('closes the ring with a position exactly equal to the first', () => {
    const ring = ringOf(toGeometryDocument(TRIANGLE));
    expect(ring).toHaveLength(TRIANGLE.length + 1);
    expect(ring[ring.length - 1]).toEqual(ring[0]);
  });

  // 🔴 There is deliberately NO test that the closing position is a copy rather
  // than a shared reference. One was written and removed: it asserted after
  // JSON.parse, which always yields fresh arrays, so it held whichever way the
  // serializer was written — a control that could not fail. The spread in
  // toGeometryDocument stays as defensive code, and the mutation that removes it
  // is an EQUIVALENT mutant: nothing mutates the ring between the push and the
  // stringify, so sharing the reference has no observable effect today.

  it('declares the only kind device-management implements', () => {
    const doc = JSON.parse(toGeometryDocument(TRIANGLE));
    expect(doc.kind).toBe(POLYGON_2D);
    expect(doc.geometry.type).toBe('Polygon');
  });

  it('nests the ring one level deep — a Polygon is an ARRAY of rings', () => {
    // Emitting the ring directly as `coordinates` produces a document that still
    // parses as JSON and still names the right kind, so nothing but this shape
    // assertion separates it from a correct one.
    const doc = JSON.parse(toGeometryDocument(TRIANGLE));
    expect(Array.isArray(doc.geometry.coordinates)).toBe(true);
    expect(doc.geometry.coordinates).toHaveLength(1);
    expect(Array.isArray(doc.geometry.coordinates[0][0])).toBe(true);
  });

  it('emits an empty ring array for no vertices rather than a bare [null]', () => {
    expect(ringOf(toGeometryDocument([]))).toEqual([]);
  });
});

describe('checkGeometry', () => {
  it('accepts the smallest ring a polygon can have', () => {
    expect(TRIANGLE).toHaveLength(MIN_VERTICES);
    expect(checkGeometry(TRIANGLE)).toEqual({ ok: true, positions: 4 });
  });

  it('refuses one vertex short of it', () => {
    expect(checkGeometry(TRIANGLE.slice(0, 2)).problem).toBe('tooFewVertices');
  });

  it('refuses an empty ring', () => {
    expect(checkGeometry([]).problem).toBe('tooFewVertices');
  });

  // ── The budget boundary, from both sides ──
  // 🔴 The limit counts POSITIONS, so the last ACCEPTED ring has one fewer
  // vertex than the limit. A check written against vertices instead of
  // positions passes the first of these and fails the second.
  it('accepts a ring whose closed form is exactly at the limit', () => {
    const vertices = Array.from({ length: MAX_FENCE_POSITIONS - 1 }, (_, i) => ({
      lng: 12.49 + i / 1e6,
      lat: 41.89,
    }));
    expect(countPositions(vertices)).toBe(MAX_FENCE_POSITIONS);
    expect(checkGeometry(vertices).ok).toBe(true);
  });

  it('refuses the ring one vertex beyond it', () => {
    const vertices = Array.from({ length: MAX_FENCE_POSITIONS }, (_, i) => ({
      lng: 12.49 + i / 1e6,
      lat: 41.89,
    }));
    const check = checkGeometry(vertices);
    expect(check.problem).toBe('tooManyPositions');
    // The reported number is what the wire would carry, not what was clicked —
    // otherwise the message says 512 while explaining a rejection at 513.
    expect(check.positions).toBe(MAX_FENCE_POSITIONS + 1);
  });

  // ── Coordinate ranges, at the boundary rather than near it ──
  it.each([
    ['longitude at +180', { lng: 180, lat: 41.89 }],
    ['longitude at -180', { lng: -180, lat: 41.89 }],
    ['latitude at +90', { lng: 12.49, lat: 90 }],
    ['latitude at -90', { lng: 12.49, lat: -90 }],
  ])('accepts %s — the bound is inclusive', (_label, edge) => {
    expect(checkGeometry([edge as Vertex, TRIANGLE[1], TRIANGLE[2]]).ok).toBe(true);
  });

  it.each([
    ['longitude past +180', { lng: 180.0000001, lat: 41.89 }],
    ['longitude past -180', { lng: -180.0000001, lat: 41.89 }],
    ['latitude past +90', { lng: 12.49, lat: 90.0000001 }],
    ['latitude past -90', { lng: 12.49, lat: -90.0000001 }],
  ])('refuses %s', (_label, edge) => {
    expect(checkGeometry([edge as Vertex, TRIANGLE[1], TRIANGLE[2]]).problem).toBe(
      'coordinateOutOfRange',
    );
  });

  it.each([
    ['NaN', NaN],
    ['Infinity', Infinity],
    ['-Infinity', -Infinity],
  ])('refuses a %s coordinate as not finite, not merely out of range', (_label, bad) => {
    // NaN fails every range comparison, so an implementation with no finiteness
    // check still REJECTS these — it just reports the wrong reason. Asserting the
    // problem code is what makes this test able to fail.
    expect(checkGeometry([{ lng: bad, lat: 41.89 }, TRIANGLE[1], TRIANGLE[2]]).problem).toBe(
      'coordinateNotFinite',
    );
  });

  it('refuses two adjacent vertices in the same place', () => {
    expect(checkGeometry([TRIANGLE[0], TRIANGLE[0], TRIANGLE[1], TRIANGLE[2]]).problem).toBe(
      'repeatedVertex',
    );
  });

  it('refuses a ring whose last vertex repeats the first', () => {
    // The operator clicking the first vertex to CLOSE must not also place it.
    // Left unchecked this becomes a zero-length closing edge, which saves fine
    // and is then refused by the detection engine as a duplicate vertex.
    expect(checkGeometry([...TRIANGLE, TRIANGLE[0]]).problem).toBe('repeatedVertex');
  });

  it('allows a repeat between NON-adjacent vertices, which is a different defect', () => {
    // A pinched ring is refused by the engine but is not what this check is for;
    // claiming it here would make the check's name a lie and mask the real gap.
    const pinched: Vertex[] = [TRIANGLE[0], TRIANGLE[1], TRIANGLE[0], TRIANGLE[2]];
    expect(checkGeometry(pinched).ok).toBe(true);
  });
});

describe('fromGeometryDocument', () => {
  it('round-trips a ring back to the vertices that produced it', () => {
    expect(fromGeometryDocument(toGeometryDocument(TRIANGLE))).toEqual(TRIANGLE);
  });

  it('round-trips coordinates in the right ORDER', () => {
    const back = fromGeometryDocument(toGeometryDocument([ROME, TRIANGLE[1], TRIANGLE[2]]));
    // A serializer and parser that BOTH swap round-trip perfectly, so equality
    // with the input proves nothing on its own. Assert the absolute values.
    expect(back?.[0].lng).toBe(12.4924);
    expect(back?.[0].lat).toBe(41.8902);
  });

  it('drops the closing position rather than showing a vertex under the first', () => {
    const back = fromGeometryDocument(toGeometryDocument(TRIANGLE));
    expect(back).toHaveLength(TRIANGLE.length);
  });

  // 🔴 Every fixture below carries a VALID ring. An earlier draft of these used
  // `[[]]`, which the ring-length guard rejects on its own — so the whole group
  // passed with the kind check DELETED, and proved nothing about kind. Keeping
  // the ring valid is what leaves exactly one guard able to fire.
  const documentWith = (over: Record<string, unknown>) =>
    JSON.stringify({
      kind: POLYGON_2D,
      geometry: { type: 'Polygon', coordinates: [VALID_RING] },
      ...over,
    });

  it.each([
    ['a reserved kind', documentWith({ kind: 'POLYGON_2_5D' })],
    ['an unknown kind', documentWith({ kind: 'HEXAGON' })],
    ['a null kind', documentWith({ kind: null })],
    ['a non-string kind', documentWith({ kind: 2 })],
    [
      'a missing kind',
      JSON.stringify({ geometry: { type: 'Polygon', coordinates: [VALID_RING] } }),
    ],
    [
      'a non-Polygon geometry whose coordinates are otherwise well formed',
      documentWith({ geometry: { type: 'MultiPolygon', coordinates: [VALID_RING] } }),
    ],
    [
      'a missing geometry type',
      documentWith({ geometry: { coordinates: [VALID_RING] } }),
    ],
    ['a missing geometry', JSON.stringify({ kind: POLYGON_2D })],
    ['coordinates that are not an array', documentWith({ geometry: { type: 'Polygon', coordinates: 'x' } })],
    ['malformed JSON', '{"kind":'],
    ['a JSON array', '[]'],
    ['a JSON string', '"POLYGON_2D"'],
    ['a JSON null', 'null'],
    [
      'a bare GeoJSON polygon with no envelope',
      JSON.stringify({ type: 'Polygon', coordinates: [VALID_RING] }),
    ],
  ])('refuses %s', (_label, raw) => {
    expect(fromGeometryDocument(raw)).toBeNull();
  });

  it('reads the same document once the defect under each fixture is removed', () => {
    // The counterweight the group above needs: proof that a VALID_RING document
    // with the right kind and type DOES load. Without it, every rejection test
    // could be passing because the shared fixture was broken all along.
    expect(fromGeometryDocument(documentWith({}))).toHaveLength(3);
  });

  it('refuses a ring with holes rather than silently dropping them', () => {
    // 🔴 The failure mode this prevents is a SAVE, not a read: open a
    // hole-bearing fence in an editor that ignored the holes, press save, and
    // the holes are gone from the stored geometry forever.
    const outer = [
      [12.49, 41.89],
      [12.5, 41.89],
      [12.495, 41.9],
      [12.49, 41.89],
    ];
    const hole = [
      [12.492, 41.892],
      [12.494, 41.892],
      [12.493, 41.894],
      [12.492, 41.892],
    ];
    const withHole = JSON.stringify({
      kind: POLYGON_2D,
      geometry: { type: 'Polygon', coordinates: [outer, hole] },
    });
    expect(fromGeometryDocument(withHole)).toBeNull();
    // The same document WITHOUT the hole must read, or the test above passes for
    // the wrong reason — because the fixture was malformed, not because of holes.
    const withoutHole = JSON.stringify({
      kind: POLYGON_2D,
      geometry: { type: 'Polygon', coordinates: [outer] },
    });
    expect(fromGeometryDocument(withoutHole)).toHaveLength(3);
  });

  it('refuses an unclosed ring rather than quietly repairing it', () => {
    const unclosed = JSON.stringify({
      kind: POLYGON_2D,
      geometry: {
        type: 'Polygon',
        coordinates: [
          [
            [12.49, 41.89],
            [12.5, 41.89],
            [12.495, 41.9],
            [12.4999, 41.8999],
          ],
        ],
      },
    });
    expect(fromGeometryDocument(unclosed)).toBeNull();
  });

  it('refuses a ring too short to be closed', () => {
    const short = JSON.stringify({
      kind: POLYGON_2D,
      geometry: {
        type: 'Polygon',
        coordinates: [
          [
            [12.49, 41.89],
            [12.5, 41.89],
            [12.49, 41.89],
          ],
        ],
      },
    });
    expect(fromGeometryDocument(short)).toBeNull();
  });

  // 🔴 A THREE-element position is legal GeoJSON — RFC 7946 allows altitude —
  // and device-management stores whatever an integration wrote, verbatim. This
  // editor has nowhere to put the third element, and toGeometryDocument writes
  // rings back as pairs, so ACCEPTING one would mean an operator who renames the
  // fence silently strips every altitude. Refusing is the same call, for the same
  // reason, as refusing holes.
  it('refuses a position carrying an altitude rather than dropping it on save', () => {
    const withAltitude = JSON.stringify({
      kind: POLYGON_2D,
      geometry: {
        type: 'Polygon',
        coordinates: [
          [
            [12.49, 41.89, 120],
            [12.5, 41.89, 121],
            [12.495, 41.9, 119],
            [12.49, 41.89, 120],
          ],
        ],
      },
    });
    expect(fromGeometryDocument(withAltitude)).toBeNull();
    // The counterweight: the SAME ring as pairs must load, so the refusal is
    // provably about the third element and not about the fixture.
    expect(fromGeometryDocument(JSON.stringify({
      kind: POLYGON_2D,
      geometry: { type: 'Polygon', coordinates: [VALID_RING] },
    }))).toHaveLength(3);
  });

  it('refuses a position that is not a pair of numbers', () => {
    const bad = JSON.stringify({
      kind: POLYGON_2D,
      geometry: {
        type: 'Polygon',
        coordinates: [
          [
            [12.49, 41.89],
            ['12.5', 41.89],
            [12.495, 41.9],
            [12.49, 41.89],
          ],
        ],
      },
    });
    expect(fromGeometryDocument(bad)).toBeNull();
  });

  it('refuses an out-of-range stored coordinate instead of loading it', () => {
    const bad = JSON.stringify({
      kind: POLYGON_2D,
      geometry: {
        type: 'Polygon',
        coordinates: [
          [
            [12.49, 91],
            [12.5, 41.89],
            [12.495, 41.9],
            [12.49, 91],
          ],
        ],
      },
    });
    expect(fromGeometryDocument(bad)).toBeNull();
  });
});

describe('MAX_FENCE_VERTICES', () => {
  it('is one below the position limit, because closing costs a position', () => {
    expect(MAX_FENCE_VERTICES).toBe(MAX_FENCE_POSITIONS - 1);
  });

  it('is exactly the largest ring checkGeometry accepts', () => {
    // Ties the number the UI SHOWS to the number the checker ENFORCES. Without
    // this they are two independent constants that agree today by coincidence,
    // and the UI would go on offering a corner the server refuses.
    const ring = (n: number) =>
      Array.from({ length: n }, (_, i) => ({ lng: 12.49 + i / 1e6, lat: 41.89 }));
    expect(checkGeometry(ring(MAX_FENCE_VERTICES)).ok).toBe(true);
    expect(checkGeometry(ring(MAX_FENCE_VERTICES + 1)).ok).toBe(false);
  });
});

describe('isCloseGesture', () => {
  const first = { x: 100, y: 100 };

  it('closes on a click exactly on the first vertex', () => {
    expect(isCloseGesture(3, first, { x: 100, y: 100 })).toBe(true);
  });

  it('closes at the radius and not one pixel beyond it', () => {
    // Both sides of the boundary, because a `<` written as `<=` (or the reverse)
    // is invisible to a test that only samples the middle of the range.
    expect(isCloseGesture(3, first, { x: 100 + CLOSE_PIXEL_RADIUS, y: 100 })).toBe(true);
    expect(isCloseGesture(3, first, { x: 100 + CLOSE_PIXEL_RADIUS + 1, y: 100 })).toBe(false);
  });

  it('measures true distance, not per-axis distance', () => {
    // (9, 9) is inside the radius on each axis alone but 12.7px away in fact.
    // A check written as `dx <= r && dy <= r` would close here, snapping the
    // ring shut on a click the operator meant as a new corner.
    expect(Math.hypot(9, 9)).toBeGreaterThan(CLOSE_PIXEL_RADIUS);
    expect(isCloseGesture(3, first, { x: 109, y: 109 })).toBe(false);
  });

  it('is symmetric — direction from the first vertex cannot matter', () => {
    for (const [dx, dy] of [
      [8, 0],
      [-8, 0],
      [0, 8],
      [0, -8],
    ]) {
      expect(isCloseGesture(3, first, { x: 100 + dx, y: 100 + dy })).toBe(true);
    }
  });

  it('does not close below three vertices, however near the click lands', () => {
    // Two points bound no area, so an early click back on the first corner is a
    // corner, not a close. Asserting the EXACT-hit case makes this about the
    // vertex count and nothing else.
    expect(isCloseGesture(0, first, { x: 100, y: 100 })).toBe(false);
    expect(isCloseGesture(1, first, { x: 100, y: 100 })).toBe(false);
    expect(isCloseGesture(2, first, { x: 100, y: 100 })).toBe(false);
    expect(isCloseGesture(3, first, { x: 100, y: 100 })).toBe(true);
  });

  it('honours an explicit radius', () => {
    expect(isCloseGesture(3, first, { x: 130, y: 100 }, 40)).toBe(true);
    expect(isCloseGesture(3, first, { x: 130, y: 100 }, 20)).toBe(false);
  });
});

describe('boundsOf', () => {
  it('returns [west, south, east, north] — MapLibre fitBounds order', () => {
    // Asymmetric on purpose: a west/south transposition would survive a square.
    expect(
      boundsOf([
        { lng: 12.49, lat: 41.89 },
        { lng: 12.53, lat: 41.91 },
        { lng: 12.51, lat: 41.87 },
      ]),
    ).toEqual([12.49, 41.87, 12.53, 41.91]);
  });

  it('is null for an empty ring, so the camera is left alone', () => {
    expect(boundsOf([])).toBeNull();
  });

  it('handles a single vertex as a degenerate box rather than null', () => {
    expect(boundsOf([ROME])).toEqual([12.4924, 41.8902, 12.4924, 41.8902]);
  });

  it('handles negative coordinates without collapsing to the first vertex', () => {
    expect(
      boundsOf([
        { lng: -0.12, lat: 51.5 },
        { lng: -0.2, lat: 51.4 },
      ]),
    ).toEqual([-0.2, 51.4, -0.12, 51.5]);
  });
});
