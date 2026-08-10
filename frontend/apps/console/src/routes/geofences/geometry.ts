// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The geometry rules behind fence drawing, kept out of the component so they can
// be unit-tested without a map, a browser, or a network.
//
// Two representations meet here and they are deliberately NOT the same shape:
//
//   the editor's    an OPEN ring of distinct vertices — what the operator clicked.
//                   Closing the ring is a gesture that ENDS drawing; it does not
//                   append a vertex, because a vertex the operator did not place
//                   would then be draggable and deletable like one they did.
//
//   the wire's      a CLOSED GeoJSON ring, first position repeated as the last,
//                   inside the kind envelope device-management stores verbatim.
//
// Everything below exists to move between those two without losing the distinction.

/** A vertex in the editor's model: an open-ring position the operator placed. */
export interface Vertex {
  /** Longitude, WGS84 decimal degrees, [-180, 180]. */
  lng: number;
  /** Latitude, WGS84 decimal degrees, [-90, 90]. */
  lat: number;
}

/**
 * The only geometry kind device-management implements. `POLYGON_2_5D` and
 * `VOXEL_3D` exist in the schema as reserved names and are REJECTED on write —
 * so there is nothing for a picker to offer and the editor does not show one.
 */
export const POLYGON_2D = 'POLYGON_2D';

/**
 * Mirrors device-management's MaxGeoFenceVertices. It MIRRORS the server and
 * decides nothing: the server re-checks and its answer is the one that counts.
 * Kept here only so the editor can stop the operator before a save that would
 * fail, and so the vertex counter can show a budget.
 *
 * 🔴 The server counts POSITIONS ACROSS ALL RINGS, and the closing position of
 * each ring counts. An editor that counted the vertices an operator clicked
 * would report 512 while sending 513.
 */
export const MAX_FENCE_POSITIONS = 512;

/** Mirrors device-management's MaxGeoFencesPerTenant. Mirrors, decides nothing. */
export const MAX_FENCES_PER_TENANT = 100;

/**
 * The most corners an operator may place. Derived, never written down twice: the
 * server's limit is on POSITIONS and a closed ring carries one more position than
 * it has corners, so a UI that showed 512 here would be offering one the server
 * refuses.
 */
export const MAX_FENCE_VERTICES = MAX_FENCE_POSITIONS - 1;

/** A closed GeoJSON ring needs at least 4 positions — 3 corners plus the repeat. */
export const MIN_RING_POSITIONS = 4;

/** ...which is 3 distinct vertices in the editor's open-ring model. */
export const MIN_VERTICES = MIN_RING_POSITIONS - 1;

/**
 * Rounds a coordinate arriving from a MAP CLICK. Rounding on the way in rather
 * than on the way out means the closing position can be a literal copy of the
 * first, so a ring is closed BY CONSTRUCTION with no float comparison that could
 * leave a ring the server reads as open.
 *
 * 🔴 This is NOT the only way a coordinate enters the model, and the other way
 * deliberately does not round: fromGeometryDocument takes a stored document
 * VERBATIM. That is not an oversight to tidy up later — an externally authored
 * ring closed at full precision would stop being detectably closed if its
 * coordinates were re-rounded here, and re-serializing it would silently move
 * every corner. Map clicks are rounded; stored documents are not touched.
 *
 * 7 decimal places is ~1.1cm at the equator — far finer than any fence needs,
 * and it keeps a 512-position document readable.
 */
const COORD_DECIMALS = 7;

export function roundCoord(value: number): number {
  // Number(x.toFixed(n)) and Math.round(x * 1e7) / 1e7 were compared over 400k
  // random coordinates and agree everywhere except exact half-way ties, where
  // they differ by one unit in the last place — 1.1cm. Either would do; toFixed
  // is kept because it rounds the DECIMAL string, so what it returns is what a
  // reader of the stored document sees. No correctness claim is being made for
  // it beyond that, and the swap is an equivalent mutant.
  return Number(value.toFixed(COORD_DECIMALS));
}

/** Rounds a map click into a model vertex. The only ROUNDING ingress; see above. */
export function toVertex(lng: number, lat: number): Vertex {
  return { lng: roundCoord(lng), lat: roundCoord(lat) };
}

// ── Validation ──────────────────────────────────────────────────────────────

/**
 * A reason a ring cannot be saved, as a stable code. The caller maps it to an
 * i18n key — this module ships no prose, so it stays testable without a locale
 * and cannot leak an untranslated string into the console.
 */
export type GeometryProblem =
  | 'tooFewVertices'
  | 'tooManyPositions'
  | 'coordinateOutOfRange'
  | 'coordinateNotFinite'
  | 'repeatedVertex'
  | 'selfIntersecting';

export interface GeometryCheck {
  /**
   * Whether the ring may be SAVED. Not the same as "has no problem": a problem
   * can be advisory, and exactly one is (see `selfIntersecting`).
   */
  ok: boolean;
  problem?: GeometryProblem;
  /** For tooManyPositions: what the ring would actually send. */
  positions: number;
}

/**
 * Positions the wire document would carry — the number the SERVER will count.
 * An open ring of n distinct vertices closes to n + 1 positions.
 *
 * An empty ring is 0, not 1: nothing is drawn, so nothing closes.
 */
export function countPositions(vertices: readonly Vertex[]): number {
  return vertices.length === 0 ? 0 : vertices.length + 1;
}

function coordinateProblem(v: Vertex): GeometryProblem | undefined {
  if (!Number.isFinite(v.lng) || !Number.isFinite(v.lat)) return 'coordinateNotFinite';
  if (v.lng < -180 || v.lng > 180) return 'coordinateOutOfRange';
  if (v.lat < -90 || v.lat > 90) return 'coordinateOutOfRange';
  return undefined;
}

function samePosition(a: Vertex, b: Vertex): boolean {
  return a.lng === b.lng && a.lat === b.lat;
}

/**
 * Whether two edges of the ring cross, treating the coordinates as PLANAR.
 *
 * 🔴 This is an APPROXIMATION and is not the answer that counts. The server does
 * the same test on the sphere, where the shortest path between two points bows
 * poleward and a ring spanning the antimeridian is not what its numbers look
 * like — so on a large or high-latitude fence the two can legitimately disagree.
 * The server's answer decides; this exists only to tell an operator mid-draw,
 * before they press save. It is NOT used to gate the save — see the advisory note
 * at its call site — precisely because it can be wrong in the direction of
 * refusing a legal ring near the antimeridian.
 *
 * Adjacent edges are skipped because they legitimately share a vertex —
 * including the wrap-around pair (last, first), which is adjacent on a closed
 * ring and is exactly the pair a naive index comparison misses.
 */
function crossingEdges(vertices: readonly Vertex[]): boolean {
  const n = vertices.length;
  if (n < 4) return false; // a triangle's edges are all mutually adjacent
  for (let i = 0; i < n; i++) {
    const a = vertices[i];
    const b = vertices[(i + 1) % n];
    for (let j = i + 1; j < n; j++) {
      if (j === i + 1 || (i === 0 && j === n - 1)) continue;
      const c = vertices[j];
      const d = vertices[(j + 1) % n];
      if (segmentsIntersect(a, b, c, d)) return true;
    }
  }
  return false;
}

/** Orientation of the ordered triple, by the sign of the cross product. */
function orientation(p: Vertex, q: Vertex, r: Vertex): number {
  const v = (q.lng - p.lng) * (r.lat - p.lat) - (q.lat - p.lat) * (r.lng - p.lng);
  return v > 0 ? 1 : v < 0 ? -1 : 0;
}

function onSegment(p: Vertex, q: Vertex, r: Vertex): boolean {
  return (
    Math.min(p.lng, r.lng) <= q.lng &&
    q.lng <= Math.max(p.lng, r.lng) &&
    Math.min(p.lat, r.lat) <= q.lat &&
    q.lat <= Math.max(p.lat, r.lat)
  );
}

/**
 * Whether segments ab and cd meet, counting collinear TOUCHES as meeting.
 *
 * Exported for its own tests. The four collinear branches below are shadowed, for
 * every ring the editor can realistically produce, by the proper-crossing test
 * above them — a mutation disabling any one of them leaves the ring-level suite
 * green. They are kept, and tested here directly, because the server does the same
 * test on the sphere via CrossingSign, which reports a shared point as MaybeCross
 * and refuses it; dropping them would make the client quietly more permissive than
 * the server on degenerate overlaps.
 */
export function segmentsIntersect(a: Vertex, b: Vertex, c: Vertex, d: Vertex): boolean {
  const o1 = orientation(a, b, c);
  const o2 = orientation(a, b, d);
  const o3 = orientation(c, d, a);
  const o4 = orientation(c, d, b);
  if (o1 !== o2 && o3 !== o4) return true;
  // Collinear touches count. Two NON-adjacent edges sharing a point is a pinched
  // ring — still not a shape with one interior, and the server refuses it too, so
  // treating it as merely touching would let it through here alone.
  if (o1 === 0 && onSegment(a, c, b)) return true;
  if (o2 === 0 && onSegment(a, d, b)) return true;
  if (o3 === 0 && onSegment(c, a, d)) return true;
  if (o4 === 0 && onSegment(c, b, d)) return true;
  return false;
}

/**
 * Checks what can be checked without a map or a network.
 */
export function checkGeometry(vertices: readonly Vertex[]): GeometryCheck {
  const positions = countPositions(vertices);

  for (const v of vertices) {
    const problem = coordinateProblem(v);
    if (problem) return { ok: false, problem, positions };
  }

  // Adjacent repeats — including first ≡ last, which the closing position would
  // turn into a zero-length edge. The engine refuses duplicate vertices outright,
  // so catching it at draw time is the difference between "you can't place that"
  // and a fence that saves and later refuses to compile.
  for (let i = 0; i < vertices.length; i++) {
    const next = vertices[(i + 1) % vertices.length];
    if (vertices.length > 1 && samePosition(vertices[i], next)) {
      return { ok: false, problem: 'repeatedVertex', positions };
    }
  }

  if (vertices.length < MIN_VERTICES) return { ok: false, problem: 'tooFewVertices', positions };
  if (positions > MAX_FENCE_POSITIONS) {
    return { ok: false, problem: 'tooManyPositions', positions };
  }

  // Last, deliberately: it is the only O(V²) check here and it runs on every
  // render while drawing. Placing it behind the budget gate means an over-limit
  // ring is rejected before anything quadratic runs on it.
  //
  // 🔴 ADVISORY — `ok` stays true. This is the one problem that does not block a
  // save, and that is not a softening, it is the only correct answer: the planar
  // test DISAGREES with the server on rings spanning the antimeridian, and it
  // disagrees in the direction of refusing shapes the server accepts. Measured —
  // an antimeridian chevron (178,0 → -179,0.5 → 178,1 → 177,1 → 177,0.5 → 177,0)
  // is "crossing" here and perfectly fine there, because reading raw longitudes
  // as planar x turns a 3°-wide edge into one sweeping 357° across the ring.
  //
  // Blocking on that would make any non-rectangular fence over Fiji, Chukotka or
  // the Aleutians unsaveable with no override, to catch a mistake the server
  // catches anyway with a clear message. So: warn while drawing, let the server
  // decide. An earlier version returned ok:false here while carrying a comment
  // saying it did not gate anything.
  if (crossingEdges(vertices)) {
    return { ok: true, problem: 'selfIntersecting', positions };
  }

  return { ok: true, positions };
}

// ── Serialization ───────────────────────────────────────────────────────────

/**
 * Builds the document device-management stores verbatim in `geo_fences.geometry`.
 *
 * 🔴 GeoJSON position order is [LONGITUDE, LATITUDE] (RFC 7946) — the opposite of
 * how coordinates are spoken, and the single most common way a fence lands in the
 * wrong hemisphere. The round-trip test pins it with a deliberately asymmetric
 * point, so a swap cannot pass by symmetry.
 *
 * Only the exterior ring is written. Holes are a later slice; the wire format
 * already allows them (rings after the first are holes) and this shape extends
 * to them without a format change.
 */
export function toGeometryDocument(vertices: readonly Vertex[]): string {
  const ring = vertices.map((v) => [v.lng, v.lat]);
  // Closed by construction: a literal copy of the first position, never a
  // recomputed or re-rounded one.
  if (ring.length > 0) ring.push([...ring[0]]);
  return JSON.stringify({
    kind: POLYGON_2D,
    geometry: { type: 'Polygon', coordinates: [ring] },
  });
}

/**
 * Reads a stored document back into editor vertices, dropping the closing
 * position so the operator sees the ring they drew rather than a duplicate
 * vertex sitting under the first one.
 *
 * Returns null for anything this editor cannot represent — a reserved kind, a
 * non-Polygon, a ring with holes, a malformed document. Null means "show this
 * fence read-only", never "show an empty map": an editor that silently opened a
 * fence it had partly failed to read would let a save destroy the rest of it.
 */
export function fromGeometryDocument(raw: string): Vertex[] | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null;

  const envelope = parsed as { kind?: unknown; geometry?: unknown };
  if (envelope.kind !== POLYGON_2D) return null;

  const geometry = envelope.geometry as { type?: unknown; coordinates?: unknown } | undefined;
  if (!geometry || geometry.type !== 'Polygon' || !Array.isArray(geometry.coordinates)) return null;

  // A hole-bearing fence is readable but not yet editable here. Refusing it is
  // what keeps a save from dropping the holes on the floor.
  if (geometry.coordinates.length !== 1) return null;

  const ring = geometry.coordinates[0];
  if (!Array.isArray(ring) || ring.length < MIN_RING_POSITIONS) return null;

  const vertices: Vertex[] = [];
  for (const position of ring) {
    // 🔴 Exactly 2, not "at least 2". RFC 7946 allows a third element (altitude)
    // and device-management stores the document verbatim, so an integration may
    // legitimately have written one. This editor's Vertex has nowhere to put it,
    // and toGeometryDocument would write the ring back WITHOUT it — the same
    // destroy-on-save that holes are refused to prevent, three lines up.
    if (!Array.isArray(position) || position.length !== 2) return null;
    const [lng, lat] = position;
    // A TYPE guard, not a behavioural one: Number.isFinite below already rejects
    // every non-number (it does not coerce, so a string, null or boolean all
    // fail it). This line earns its place by letting the push below be
    // type-correct without a cast — removing it is an equivalent mutant.
    if (typeof lng !== 'number' || typeof lat !== 'number') return null;
    const vertex: Vertex = { lng, lat };
    if (coordinateProblem(vertex)) return null;
    vertices.push(vertex);
  }

  // Drop the closing position, but only if it IS one. A ring the server would
  // have rejected as unclosed is not something to quietly repair.
  const first = vertices[0];
  const last = vertices[vertices.length - 1];
  if (!samePosition(first, last)) return null;
  vertices.pop();

  return vertices;
}

// ── Editing an existing ring ────────────────────────────────────────────────
//
// These are the whole of what a drag or a delete does to the model. Keeping them
// here rather than inside the map component means the RULES are testable without
// a DOM, a WebGL context, or a pointer — and the component is left holding only
// the part that genuinely needs MapLibre: turning a pixel into a coordinate.

/**
 * Moves one vertex, leaving every other position untouched.
 *
 * Out-of-range indices return the ring unchanged rather than appending or
 * throwing. A drag whose vertex was removed underneath it — by an undo, or by a
 * concurrent edit — should end quietly, not corrupt the ring or crash the editor
 * mid-gesture.
 */
export function moveVertex(
  vertices: readonly Vertex[],
  index: number,
  next: Vertex,
): Vertex[] {
  if (index < 0 || index >= vertices.length) return [...vertices];
  const out = [...vertices];
  out[index] = next;
  return out;
}

/**
 * Removes one vertex.
 *
 * 🔴 It does NOT refuse to go below three. Blocking the removal would leave an
 * operator stuck with a corner they cannot delete, and checkGeometry already
 * reports `tooFewVertices` and blocks the save.
 *
 * "Recoverable by placing another corner" is only true because the FORM reopens
 * a ring that drops below MIN_VERTICES — while a ring is closed, a map click
 * adds nothing. Without that reset this function would strand the editor, so the
 * two belong together even though they live apart.
 */
export function removeVertex(vertices: readonly Vertex[], index: number): Vertex[] {
  if (index < 0 || index >= vertices.length) return [...vertices];
  return vertices.filter((_, i) => i !== index);
}

/**
 * Inserts a vertex into the edge that starts at `edgeIndex`, so a drawn ring can
 * gain detail without being redrawn.
 *
 * Edge i runs from vertex i to vertex i+1, and the LAST edge runs from the last
 * vertex back to the first — inserting into that one appends, which is why this
 * takes an edge index rather than a vertex index. Getting that wrong puts the new
 * corner at the start of the ring instead of the end, which looks like the shape
 * turning inside out.
 */
export function insertVertexOnEdge(
  vertices: readonly Vertex[],
  edgeIndex: number,
  v: Vertex,
): Vertex[] {
  if (edgeIndex < 0 || edgeIndex >= vertices.length) return [...vertices];
  const out = [...vertices];
  out.splice(edgeIndex + 1, 0, v);
  return out;
}

/** The midpoint of each edge, in ring order — where an insert handle belongs. */
export function edgeMidpoints(vertices: readonly Vertex[]): Vertex[] {
  if (vertices.length < 2) return [];
  return vertices.map((a, i) => {
    const b = vertices[(i + 1) % vertices.length];
    return { lng: roundCoord((a.lng + b.lng) / 2), lat: roundCoord((a.lat + b.lat) / 2) };
  });
}

// ── The close gesture ───────────────────────────────────────────────────────

/** A point in screen space, as MapLibre's `project` and `MapMouseEvent.point` give it. */
export interface ScreenPoint {
  x: number;
  y: number;
}

/** How near the first vertex a click must land, in pixels, to close the ring. */
export const CLOSE_PIXEL_RADIUS = 12;

/**
 * Whether a click should CLOSE the ring rather than add another vertex.
 *
 * 🔴 Measured in SCREEN space, not in degrees. A fixed degree tolerance is a
 * different real distance at every zoom and every latitude: a fence drawn zoomed
 * out could never be closed, while one drawn zoomed in would close on a click
 * nowhere near the first corner. Pixels are the units the gesture actually
 * happens in.
 *
 * Below three vertices there is nothing to close — two points do not bound an
 * area — so an early click back on the first vertex adds a vertex like any other.
 */
export function isCloseGesture(
  vertexCount: number,
  first: ScreenPoint,
  click: ScreenPoint,
  radius: number = CLOSE_PIXEL_RADIUS,
): boolean {
  if (vertexCount < MIN_VERTICES) return false;
  return Math.hypot(first.x - click.x, first.y - click.y) <= radius;
}

/**
 * Whether a press has travelled far enough to be a DRAG rather than a click.
 *
 * Pixels, for the same reason isCloseGesture uses pixels: a degree tolerance is a
 * different real distance at every zoom.
 */
export function hasMovedEnough(
  start: ScreenPoint,
  current: ScreenPoint,
  threshold: number = DRAG_THRESHOLD_PX,
): boolean {
  return Math.hypot(current.x - start.x, current.y - start.y) > threshold;
}

/** Pixels a press must travel before it becomes a drag. */
export const DRAG_THRESHOLD_PX = 3;

/** What a click on the map should be taken to mean. */
export type ClickIntent = 'ignored' | 'close' | 'append';

/**
 * The decision a click on the drawing surface resolves to.
 *
 * 🔴 EXTRACTED BECAUSE IT BROKE WHILE IT LIVED INSIDE THE MAP COMPONENT, where
 * no test could reach it. Vertex dragging was added without a movement
 * threshold, so a plain click on a corner entered the drag machinery and its
 * trailing click was discarded — which silently disabled "click the first corner
 * to close the shape", the one gesture the map's own instructions describe. Every
 * unit test still passed, because the click never reached the rule they cover.
 *
 * The three suppressors are ordered by how they arise, and each is distinct:
 *   justDragged     a drag that MOVED just ended; its trailing click is not one.
 *   consumedByLayer a handle already answered this exact click.
 *   closed          the ring is finished, so nothing is appended — but a click
 *                   may still CLOSE it, which is why this is checked last.
 */
export function clickIntent(input: {
  justDragged: boolean;
  consumedByLayer: boolean;
  closed: boolean;
  vertices: readonly Vertex[];
  /** Where the first vertex sits on screen, or null if there is no ring yet. */
  firstVertexPoint: ScreenPoint | null;
  clickPoint: ScreenPoint;
}): ClickIntent {
  if (input.justDragged || input.consumedByLayer) return 'ignored';

  if (
    input.firstVertexPoint &&
    isCloseGesture(input.vertices.length, input.firstVertexPoint, input.clickPoint)
  ) {
    return 'close';
  }

  // Checked AFTER the close test: a finished ring takes no new corners, but
  // closing an already-closed ring is a no-op rather than something to forbid,
  // and ordering it the other way round would make the first corner of a closed
  // ring the only dead spot on the map for no reason a user could infer.
  if (input.closed) return 'ignored';

  return 'append';
}

// ── Reading the frozen archive ──────────────────────────────────────────────

/** One fence as it was frozen into a set version. */
export interface SnapshotEntry {
  token: string;
  geometry: string;
}

/**
 * How a fence appeared in a given fence-set version.
 *
 * 🔴 THREE OUTCOMES, NOT TWO, and collapsing any pair of them tells an operator
 * something false:
 *
 *   'absent'     the fence was not in the set then — created later, or deleted
 *                and its token reused. NOT the same as having no shape.
 *   'unreadable' it was there, and this editor cannot draw what was stored (a
 *                reserved kind, a ring with holes). The engine could read it;
 *                only this viewer cannot.
 *   'present'    here is the ring, as the engine saw it.
 *
 * "Not in this version" rendered as an empty map would claim the fence existed
 * and enclosed nothing, which is a statement about the FENCE. The truth is a
 * statement about the VERSION.
 *
 * 🔴 A KNOWN LIMIT OF THE ARCHIVE, not of this function: snapshot entries carry
 * token and geometry only, and fence deletes are hard with tokens reusable. So an
 * old version's entry for a token may belong to a DIFFERENT fence that held it
 * then, and nothing here can tell. That is why the caller says "the shape stored
 * under this token" rather than "this fence's shape". Closing it properly means
 * freezing a stable fence identity into the snapshot, server-side.
 */
export type SnapshotLookup =
  | { kind: 'absent' }
  | { kind: 'unreadable' }
  | { kind: 'present'; vertices: Vertex[] };

export function lookupInSnapshot(
  entries: readonly SnapshotEntry[],
  token: string,
): SnapshotLookup {
  const entry = entries.find((e) => e.token === token);
  if (!entry) return { kind: 'absent' };
  const vertices = fromGeometryDocument(entry.geometry);
  if (!vertices) return { kind: 'unreadable' };
  return { kind: 'present', vertices };
}

// ── Camera ──────────────────────────────────────────────────────────────────

/** [west, south, east, north] — MapLibre's fitBounds order. */
export type BoundingBox = [number, number, number, number];

/**
 * The bounds enclosing a ring, or null when there is nothing to enclose.
 *
 * Deliberately naive about the antimeridian: a fence spanning it produces bounds
 * the long way round. Fences are site-scale in every case this serves, and a
 * wrong guess at which way an operator meant to wrap would be worse than a wide
 * view they can correct.
 */
export function boundsOf(vertices: readonly Vertex[]): BoundingBox | null {
  if (vertices.length === 0) return null;
  let west = vertices[0].lng;
  let east = vertices[0].lng;
  let south = vertices[0].lat;
  let north = vertices[0].lat;
  for (const v of vertices) {
    if (v.lng < west) west = v.lng;
    if (v.lng > east) east = v.lng;
    if (v.lat < south) south = v.lat;
    if (v.lat > north) north = v.lat;
  }
  return [west, south, east, north];
}
