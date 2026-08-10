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
  DRAG_THRESHOLD_PX,
  clickIntent,
  edgeMidpoints,
  fromGeometryDocument,
  hasMovedEnough,
  insertVertexOnEdge,
  isCloseGesture,
  lookupInSnapshot,
  moveVertex,
  removeVertex,
  segmentsIntersect,
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

/**
 * n distinct vertices on an actual circle — convex, therefore simple.
 *
 * 🔴 The budget fixtures used to be `lng: 12.49 + i/1e6, lat: 41.89`, which is a
 * straight LINE, not a ring. It passed every check that only counted positions,
 * and only failed once something looked at the SHAPE. A fixture that is not the
 * thing it claims to be will pass for as long as nothing asks.
 */
function circleOf(n: number): Vertex[] {
  return Array.from({ length: n }, (_, i) => {
    const theta = (2 * Math.PI * i) / n;
    return { lng: 12.49 + 0.01 * Math.cos(theta), lat: 41.89 + 0.01 * Math.sin(theta) };
  });
}

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
    const vertices = circleOf(MAX_FENCE_POSITIONS - 1);
    expect(countPositions(vertices)).toBe(MAX_FENCE_POSITIONS);
    expect(checkGeometry(vertices).ok).toBe(true);
  });

  it('refuses the ring one vertex beyond it', () => {
    const vertices = circleOf(MAX_FENCE_POSITIONS);
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

  it('attributes a NON-adjacent repeat to the crossing check, not to this one', () => {
    // A pinched ring is refused — but by crossingEdges, not by the adjacency
    // check, whose name would be a lie if it claimed this. Asserting the problem
    // CODE rather than merely `ok === false` is what separates the two.
    const pinched: Vertex[] = [TRIANGLE[0], TRIANGLE[1], TRIANGLE[0], TRIANGLE[2]];
    expect(checkGeometry(pinched).problem).toBe('selfIntersecting');
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
    expect(checkGeometry(circleOf(MAX_FENCE_VERTICES)).ok).toBe(true);
    expect(checkGeometry(circleOf(MAX_FENCE_VERTICES + 1)).ok).toBe(false);
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

describe('self-intersection (planar approximation)', () => {
  const bowtie: Vertex[] = [
    { lng: 0, lat: 0 },
    { lng: 1, lat: 1 },
    { lng: 1, lat: 0 },
    { lng: 0, lat: 1 },
  ];

  it('reports a bow-tie', () => {
    expect(checkGeometry(bowtie).problem).toBe('selfIntersecting');
  });

  // 🔴 ADVISORY, NOT BLOCKING — and the distinction is load-bearing, so it is
  // asserted rather than described. The planar test disagrees with the server's
  // spherical one near the antimeridian, and it disagrees by refusing rings the
  // server ACCEPTS. Gating the save on it would make any non-rectangular fence
  // over Fiji or the Aleutians unsaveable with no override.
  it('does not block the save, because the approximation can be wrong', () => {
    expect(checkGeometry(bowtie).ok).toBe(true);
  });

  it('still blocks the save for problems that are NOT approximations', () => {
    // The counterweight: without it, the assertion above would also pass against
    // a checkGeometry that had stopped blocking anything at all.
    expect(checkGeometry(bowtie.slice(0, 2)).ok).toBe(false);
    expect(checkGeometry([bowtie[0], bowtie[0], bowtie[1], bowtie[2]]).ok).toBe(false);
  });

  it('reports a ring spanning the antimeridian, and still lets it be saved', () => {
    // Measured against the real server predicate: this shape IS accepted there.
    // Reading raw longitudes as planar x turns its 3°-wide edge into one sweeping
    // 357° across the rest of the ring, so the client sees a crossing that is not
    // there. This test exists to pin that the disagreement costs a WARNING and
    // never a refusal.
    const chevron: Vertex[] = [
      { lng: 178, lat: 0 },
      { lng: -179, lat: 0.5 },
      { lng: 178, lat: 1 },
      { lng: 177, lat: 1 },
      { lng: 177, lat: 0.5 },
      { lng: 177, lat: 0 },
    ];
    const check = checkGeometry(chevron);
    expect(check.problem).toBe('selfIntersecting');
    expect(check.ok).toBe(true);
  });

  it('accepts a convex ring', () => {
    // The counterweight: without it, every refusal below could be satisfied by a
    // check that calls everything self-intersecting.
    const square: Vertex[] = [
      { lng: 0, lat: 0 },
      { lng: 1, lat: 0 },
      { lng: 1, lat: 1 },
      { lng: 0, lat: 1 },
    ];
    expect(checkGeometry(square).ok).toBe(true);
  });

  it('accepts a CONCAVE ring, which is legal and is the obvious false positive', () => {
    // An L-shape. A naive crossing test that compared adjacent edges, or that
    // treated a shared vertex as a crossing, would refuse this — and refusing a
    // legal fence mid-draw is worse than the gap this check closes.
    const ell: Vertex[] = [
      { lng: 0, lat: 0 },
      { lng: 2, lat: 0 },
      { lng: 2, lat: 1 },
      { lng: 1, lat: 1 },
      { lng: 1, lat: 2 },
      { lng: 0, lat: 2 },
    ];
    expect(checkGeometry(ell).ok).toBe(true);
  });

  it('sees a crossing on the CLOSING edge, not just between drawn edges', () => {
    // 🔴 The wrap-around edge is the one a naive scan misses, and it is not
    // hypothetical: a Go test fixture in this repo built a sawtooth whose closing
    // edge ran back across the teeth, called itself a circle, and went unnoticed
    // until an authoring gate started refusing self-intersecting rings.
    //
    // Longitude increases monotonically here, so no two DRAWN edges can cross;
    // the only crossing is the closing edge running back across the zigzag.
    const sawtooth: Vertex[] = [
      { lng: 0, lat: 0 },
      { lng: 1, lat: 1 },
      { lng: 2, lat: 0 },
      { lng: 3, lat: 1 },
      { lng: 4, lat: 2 },
    ];
    // The closing edge runs y = x/2; edge (1,1)-(2,0) runs y = 2 - x. They meet at
    // (1.333, 0.667), which lies inside both segments. Worked out rather than
    // eyeballed, because a fixture that does not actually cross would make this
    // test pass only while the check was broken.
    expect(checkGeometry(sawtooth).problem).toBe('selfIntersecting');
  });

  it('refuses a ring pinched at a shared corner', () => {
    const pinched: Vertex[] = [
      { lng: 0, lat: 0 },
      { lng: 1, lat: 0 },
      { lng: 0.5, lat: 1 },
      { lng: 1.5, lat: 1 },
      { lng: 0.5, lat: 1 },
      { lng: 0, lat: 1.5 },
    ];
    expect(checkGeometry(pinched).problem).toBe('selfIntersecting');
  });

  it('never reports a triangle, whose edges are all mutually adjacent', () => {
    // Not a gap: with three edges every pair shares a vertex, so there is nothing
    // a crossing scan could legitimately compare.
    expect(checkGeometry(TRIANGLE).ok).toBe(true);
  });

  it('is checked AFTER the budget, so an over-limit ring costs no quadratic scan', () => {
    // Ordering, asserted through the reported problem: a ring that is both
    // over-limit and self-intersecting must report the cheap failure.
    const many: Vertex[] = Array.from({ length: MAX_FENCE_POSITIONS }, (_, i) => ({
      lng: i % 2 === 0 ? 0 : 1,
      lat: (i % 60) * 0.01,
    }));
    expect(checkGeometry(many).problem).toBe('tooManyPositions');
  });
});

describe('segmentsIntersect', () => {
  const at = (lng: number, lat: number): Vertex => ({ lng, lat });

  it('sees a proper crossing', () => {
    expect(segmentsIntersect(at(0, 0), at(2, 2), at(0, 2), at(2, 0))).toBe(true);
  });

  it('says no to segments that miss', () => {
    // The counterweight: without it every case here would pass against a
    // predicate that always returns true.
    expect(segmentsIntersect(at(0, 0), at(1, 0), at(0, 1), at(1, 1))).toBe(false);
    expect(segmentsIntersect(at(0, 0), at(1, 0), at(2, 0), at(3, 0))).toBe(false);
  });

  // 🔴 THE FOUR COLLINEAR BRANCHES ARE INDIVIDUALLY REDUNDANT — measured, and the
  // tests that used to sit here were wrong about it.
  //
  // They claimed one case per branch (c on ab, d on ab, a on cd, b on cd). Every
  // one of those cases has orientations that DIFFER, so the proper-crossing test
  // above catches them and the branch named in the test never runs: disabling any
  // single branch left all four green. The only input that reaches them is a full
  // collinear overlap, where all four orientations are zero — and there any ONE
  // branch suffices, so no single-branch mutation can be killed.
  //
  // So they are tested collectively, which is what they actually are: together
  // they are the difference between agreeing and disagreeing with the server,
  // whose CrossingSign reports a shared point as MaybeCross and refuses it.
  it('sees a collinear overlap that no proper-crossing test can', () => {
    // Every orientation is zero here, so the proper-crossing test is false and
    // only the collinear handling is left. Removing ALL FOUR branches turns this
    // red; removing any one does not.
    expect(segmentsIntersect(at(0, 0), at(3, 0), at(1, 0), at(2, 0))).toBe(true);
    expect(segmentsIntersect(at(0, 0), at(0, 3), at(0, 1), at(0, 2))).toBe(true);
  });

  it('does not join collinear segments that are merely on the same line', () => {
    // 🔴 Vertical and DISJOINT. The bounds check has to look at BOTH axes: here
    // every longitude is identical, so a check that compared only longitude would
    // call these overlapping and refuse a perfectly good ring.
    expect(segmentsIntersect(at(0, 0), at(0, 3), at(0, 5), at(0, 7))).toBe(false);
    // ...and the horizontal mirror, so neither axis can be the one that is dropped.
    expect(segmentsIntersect(at(0, 0), at(3, 0), at(5, 0), at(7, 0))).toBe(false);
  });
});

describe('editing an existing ring', () => {
  const A = { lng: 0, lat: 0 };
  const B = { lng: 2, lat: 0 };
  const C = { lng: 1, lat: 2 };
  const ring = [A, B, C];

  describe('moveVertex', () => {
    it('moves the named vertex and no other', () => {
      const moved = moveVertex(ring, 1, { lng: 5, lat: 5 });
      expect(moved).toEqual([A, { lng: 5, lat: 5 }, C]);
    });

    it('does not mutate the ring it was given', () => {
      // The form holds the ring in React state; mutating in place would move the
      // shape without re-rendering, so the map and the model would disagree until
      // something unrelated triggered a render.
      const original = [...ring];
      moveVertex(ring, 1, { lng: 5, lat: 5 });
      expect(ring).toEqual(original);
    });

    it.each([-1, 3, 99])('leaves the ring unchanged for out-of-range index %i', (i) => {
      // A drag whose vertex was removed underneath it must end quietly, not
      // append a corner or throw mid-gesture.
      expect(moveVertex(ring, i, { lng: 5, lat: 5 })).toEqual(ring);
    });
  });

  describe('removeVertex', () => {
    it('removes the named vertex, preserving order', () => {
      expect(removeVertex(ring, 1)).toEqual([A, C]);
      expect(removeVertex(ring, 0)).toEqual([B, C]);
      expect(removeVertex(ring, 2)).toEqual([A, B]);
    });

    it('allows the ring to fall below three corners', () => {
      // 🔴 Deliberate. Refusing here would strand an operator with a corner they
      // cannot delete, and checkGeometry already blocks the save — so this is
      // recoverable by placing another corner. A rule in two places is a rule
      // that can disagree with itself.
      const two = removeVertex(ring, 2);
      expect(two).toHaveLength(2);
      expect(checkGeometry(two).problem).toBe('tooFewVertices');
      expect(checkGeometry(two).ok).toBe(false);
    });

    it.each([-1, 3])('leaves the ring unchanged for out-of-range index %i', (i) => {
      expect(removeVertex(ring, i)).toEqual(ring);
    });
  });

  describe('insertVertexOnEdge', () => {
    const N = { lng: 9, lat: 9 };

    it('inserts AFTER the vertex the edge starts at', () => {
      expect(insertVertexOnEdge(ring, 0, N)).toEqual([A, N, B, C]);
      expect(insertVertexOnEdge(ring, 1, N)).toEqual([A, B, N, C]);
    });

    it('appends when inserting into the CLOSING edge', () => {
      // 🔴 The last edge runs from the last vertex back to the first. Treating it
      // as an ordinary index would put the new corner at position 0 — which reads
      // on screen as the shape turning inside out, not as a corner in the wrong
      // place, so it is easy to misdiagnose.
      expect(insertVertexOnEdge(ring, 2, N)).toEqual([A, B, C, N]);
    });

    it.each([-1, 3])('leaves the ring unchanged for out-of-range edge %i', (i) => {
      expect(insertVertexOnEdge(ring, i, N)).toEqual(ring);
    });
  });

  describe('edgeMidpoints', () => {
    it('gives one midpoint per edge, including the closing edge', () => {
      // Three corners means THREE edges, not two — the closing edge is an edge.
      expect(edgeMidpoints(ring)).toEqual([
        { lng: 1, lat: 0 },
        { lng: 1.5, lat: 1 },
        { lng: 0.5, lat: 1 },
      ]);
    });

    it('is empty below two corners, because a single point spans no edge', () => {
      expect(edgeMidpoints([])).toEqual([]);
      expect(edgeMidpoints([A])).toEqual([]);
    });

    it('rounds like every other coordinate that enters the model', () => {
      const mid = edgeMidpoints([{ lng: 0, lat: 0 }, { lng: 0.00000001, lat: 0 }]);
      expect(mid[0].lng).toBe(roundCoord(mid[0].lng));
    });

    it('indexes midpoints so that inserting at i lands between i and i+1', () => {
      // The contract that ties the two together: without it the handle an operator
      // grabs and the edge it splits can drift apart, and every individual test
      // above would still pass.
      const mids = edgeMidpoints(ring);
      const grown = insertVertexOnEdge(ring, 1, mids[1]);
      expect(grown[1]).toEqual(B);
      expect(grown[2]).toEqual(mids[1]);
      expect(grown[3]).toEqual(C);
    });
  });
});

describe('hasMovedEnough', () => {
  const start = { x: 100, y: 100 };

  it('treats a still pointer as not moved', () => {
    expect(hasMovedEnough(start, { x: 100, y: 100 })).toBe(false);
  });

  it('is exclusive at the threshold and true one pixel past it', () => {
    expect(hasMovedEnough(start, { x: 100 + DRAG_THRESHOLD_PX, y: 100 })).toBe(false);
    expect(hasMovedEnough(start, { x: 100 + DRAG_THRESHOLD_PX + 1, y: 100 })).toBe(true);
  });

  it('measures true distance, not per-axis', () => {
    // (3,3) is within the threshold on each axis alone but 4.24px away in fact.
    expect(hasMovedEnough(start, { x: 103, y: 103 })).toBe(true);
  });
});

describe('clickIntent', () => {
  const ring: Vertex[] = [
    { lng: 0, lat: 0 },
    { lng: 1, lat: 0 },
    { lng: 0.5, lat: 1 },
  ];
  const firstAt = { x: 100, y: 100 };
  const base = {
    justDragged: false,
    consumedByLayer: false,
    closed: false,
    vertices: ring,
    firstVertexPoint: firstAt,
    clickPoint: { x: 400, y: 400 },
  };

  it('appends on a click in open space', () => {
    expect(clickIntent(base)).toBe('append');
  });

  // 🔴 THE REGRESSION THIS FUNCTION WAS EXTRACTED FOR. Dragging was added without
  // a movement threshold, so a plain click on a corner entered the drag machinery
  // and its trailing click was discarded — silently disabling the one gesture the
  // map's own aria-label instructs the operator to perform. Every geometry test
  // still passed, because the click never reached isCloseGesture.
  it('CLOSES on a click landing on the first corner', () => {
    expect(clickIntent({ ...base, clickPoint: firstAt })).toBe('close');
  });

  it('closes a finished ring rather than treating its first corner as dead', () => {
    // Order matters: `closed` is checked AFTER the close test, so clicking the
    // first corner of an already-closed ring is a harmless no-op instead of the
    // one dead spot on the map for no reason a user could infer.
    expect(clickIntent({ ...base, closed: true, clickPoint: firstAt })).toBe('close');
  });

  it('appends nothing to a finished ring clicked in open space', () => {
    expect(clickIntent({ ...base, closed: true })).toBe('ignored');
  });

  it('ignores the trailing click of a drag that moved', () => {
    expect(clickIntent({ ...base, justDragged: true })).toBe('ignored');
    // ...including one that lands on the first corner, which would otherwise
    // close the ring every time an operator dragged that corner.
    expect(clickIntent({ ...base, justDragged: true, clickPoint: firstAt })).toBe('ignored');
  });

  it('ignores a click a handle already answered', () => {
    expect(clickIntent({ ...base, consumedByLayer: true })).toBe('ignored');
  });

  it('appends when there is no ring yet', () => {
    expect(clickIntent({ ...base, vertices: [], firstVertexPoint: null })).toBe('append');
  });

  it('does not close below three corners, however near the click', () => {
    expect(
      clickIntent({ ...base, vertices: ring.slice(0, 2), clickPoint: firstAt }),
    ).toBe('append');
  });
});

describe('lookupInSnapshot', () => {
  const good = JSON.stringify({
    kind: POLYGON_2D,
    geometry: { type: 'Polygon', coordinates: [VALID_RING] },
  });
  const holed = JSON.stringify({
    kind: POLYGON_2D,
    geometry: {
      type: 'Polygon',
      coordinates: [
        VALID_RING,
        [
          [12.492, 41.892],
          [12.494, 41.892],
          [12.493, 41.894],
          [12.492, 41.892],
        ],
      ],
    },
  });

  it('returns the ring the engine saw', () => {
    const got = lookupInSnapshot([{ token: 'yard', geometry: good }], 'yard');
    expect(got.kind).toBe('present');
    expect(got.kind === 'present' && got.vertices).toHaveLength(3);
  });

  // 🔴 The three outcomes are distinct on purpose, and each collapse tells an
  // operator something false. Asserting the KIND rather than merely "no vertices"
  // is what separates them — a test that only checked for an empty result would
  // pass with all three folded into one.
  it('says ABSENT when the fence was not in that version', () => {
    expect(lookupInSnapshot([{ token: 'other', geometry: good }], 'yard')).toEqual({
      kind: 'absent',
    });
    expect(lookupInSnapshot([], 'yard')).toEqual({ kind: 'absent' });
  });

  it('says UNREADABLE when it was there but this viewer cannot draw it', () => {
    // Present in the set, and the engine could read it — only this editor cannot.
    // Reporting that as "absent" would claim the fence did not exist, and as an
    // empty ring would claim it enclosed nothing.
    expect(lookupInSnapshot([{ token: 'yard', geometry: holed }], 'yard')).toEqual({
      kind: 'unreadable',
    });
    expect(lookupInSnapshot([{ token: 'yard', geometry: 'not json' }], 'yard')).toEqual({
      kind: 'unreadable',
    });
  });

  it('matches on the exact token, not a prefix', () => {
    expect(lookupInSnapshot([{ token: 'yard-north', geometry: good }], 'yard').kind).toBe('absent');
  });

  it('takes the first entry when a token somehow repeats', () => {
    // The server keys on (tenant, token) so this should be impossible; pinning it
    // means the reader is deterministic if it ever is not.
    const got = lookupInSnapshot(
      [
        { token: 'yard', geometry: good },
        { token: 'yard', geometry: holed },
      ],
      'yard',
    );
    expect(got.kind).toBe('present');
  });
});
