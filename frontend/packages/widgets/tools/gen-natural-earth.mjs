// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Regenerates src/widgets/natural-earth-data.ts — the bundled world basemap.
//
//   node tools/gen-natural-earth.mjs
//
// Its output is COMMITTED, and this script exists so that "where did that 150 KB
// of coordinates come from" has an answer that can be re-run rather than trusted.
// It needs network access; nothing in the build or the test suite runs it.
//
// ---------------------------------------------------------------------------
// PROVENANCE AND LICENCE
//
// Natural Earth, 1:110m physical land + admin-0 land boundary lines, taken from
// nvkelso/natural-earth-vector at the pinned RELEASE TAG below.
//
// 🔴 The tag is pinned rather than tracking `master`, and that is not pedantry:
// `master` currently reports itself as `5.2.0-pre`, i.e. an unreleased revision.
// Bundling a pre-release means shipping whatever happened to be committed the day
// someone ran this, which is not a thing anyone can reproduce afterwards. The two
// SHA-256 digests below are checked before a single byte is used, so a silently
// retagged or mirrored file fails loudly here instead of quietly changing the
// world map.
//
// The licence was read at the pinned tag, not recalled. LICENSE.md says, in full
// and without qualification: "Everything here is public domain." and "No
// permission is needed to use Natural Earth. Crediting the authors is
// unnecessary."
//
// So the credit line the generated style carries is NOT an obligation — it is the
// short citation Natural Earth themselves offer ("Made with Natural Earth"). We
// show it anyway, for a reason that is ours rather than theirs: a user looking at
// the bundled map needs to be able to tell it apart from a configured provider,
// and an unlabelled schematic world is exactly the "is this broken?" ambiguity
// this whole slice exists to remove.
// ---------------------------------------------------------------------------

import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const TAG = 'v5.1.2';

const SOURCES = [
  {
    key: 'land',
    file: 'ne_110m_land.geojson',
    sha256: '9e0729ee253ca7d7a5c4ae9395fb1902264c5377c52e224d13dd85010e2835d9',
  },
  {
    key: 'boundaries',
    file: 'ne_110m_admin_0_boundary_lines_land.geojson',
    sha256: 'd42479fd79552cca4eec7f85fcdca717a790d29ff06be7676f1af0568c6d3f7c',
  },
];

// Decimal places kept per coordinate. 2 is ~1.1 km at the equator, which is far
// finer than 1:110m data can justify — at that scale a single pixel is tens of
// kilometres until you are zoomed further in than this dataset can answer.
//
// Counter-intuitively, LOWER precision is also SMALLER for two compounding
// reasons, and it is worth knowing the second one: shorter numbers, and rounding
// collapses runs of neighbouring points onto each other, which the de-duplication
// below then removes outright. Measured over the real files: p=1 → 138 KiB,
// p=2 → 152 KiB, p=3 → 168 KiB. p=1 was rejected on shape quality (11 km bins
// visibly square off small islands), not on size.
const PRECISION = 2;

function round(coordinates) {
  const out = [];
  for (const [lng, lat] of coordinates) {
    const point = [
      Number(lng.toFixed(PRECISION)),
      Number(lat.toFixed(PRECISION)),
    ];
    // Drop a point that rounded onto its predecessor. Without this, rounding
    // manufactures zero-length segments — harmless to draw, pure payload.
    const previous = out[out.length - 1];
    if (!previous || previous[0] !== point[0] || previous[1] !== point[1]) {
      out.push(point);
    }
  }
  return out;
}

// Reduce one geometry, or return null if rounding degenerated it past the point
// of being drawable: a linear ring needs 4 positions (it is closed), a line needs
// 2. Returning the degenerate shape instead would hand MapLibre geometry it is
// entitled to reject.
function reduce(geometry) {
  const { type, coordinates } = geometry;
  const ring = (r) => round(r);
  switch (type) {
    case 'LineString': {
      const line = ring(coordinates);
      return line.length >= 2 ? { type, coordinates: line } : null;
    }
    case 'MultiLineString': {
      const lines = coordinates.map(ring).filter((l) => l.length >= 2);
      return lines.length ? { type, coordinates: lines } : null;
    }
    // 🔴 A POLYGON'S FIRST RING IS ITS OUTLINE; THE REST ARE HOLES. Filtering the
    // list would therefore be wrong in a way that renders beautifully: if an
    // exterior ring degenerated while an interior one survived, the survivor would
    // slide into position 0 and a LAKE would be promoted to a landmass. Refusing
    // the whole polygon when its exterior does not survive is the only safe rule —
    // losing an island is a visible absence, inventing one is a wrong map.
    //
    // Unreachable with today's data (the run reports zero degenerate geometries,
    // and the sole interior ring is the Caspian inside a far larger exterior), so
    // this is a guard against a future refresh or a coarser PRECISION rather than a
    // bug being fixed. It is written down because the filtering version looks
    // correct and its output would look plausible.
    case 'Polygon': {
      const rings = coordinates.map(ring);
      if (rings[0].length < 4) return null;
      return { type, coordinates: rings.filter((r) => r.length >= 4) };
    }
    case 'MultiPolygon': {
      const polygons = coordinates
        .map((polygon) => polygon.map(ring))
        .filter((polygon) => polygon[0].length >= 4)
        .map((polygon) => polygon.filter((r) => r.length >= 4));
      return polygons.length ? { type, coordinates: polygons } : null;
    }
    default:
      throw new Error(`unhandled geometry type: ${type}`);
  }
}

async function fetchSource({ file, sha256 }) {
  const url = `https://raw.githubusercontent.com/nvkelso/natural-earth-vector/${TAG}/geojson/${file}`;
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${url} -> HTTP ${response.status}`);
  const body = Buffer.from(await response.arrayBuffer());
  const digest = createHash('sha256').update(body).digest('hex');
  if (digest !== sha256) {
    throw new Error(
      `${file} digest mismatch\n  expected ${sha256}\n  got      ${digest}\n` +
        `The pinned tag no longer serves the bytes this was generated from. Do not ` +
        `"fix" this by pasting the new digest in without looking at what changed.`,
    );
  }
  console.log(`  ${file}: ${(body.length / 1024).toFixed(1)} KiB, digest ok`);
  return JSON.parse(body.toString('utf8'));
}

const collections = {};
console.log(`Natural Earth ${TAG}`);
for (const source of SOURCES) {
  const raw = await fetchSource(source);
  const features = [];
  let dropped = 0;
  for (const feature of raw.features) {
    const geometry = reduce(feature.geometry);
    if (!geometry) {
      dropped += 1;
      continue;
    }
    // Every property is discarded. Nothing renders from them — the style paints
    // land as one fill and boundaries as one line — and admin-0 boundary
    // properties in particular carry per-country DISPUTE CLASSIFICATIONS
    // (FCLASS_ISO, FCLASS_US, FCLASS_CN, ...) that this platform has no business
    // shipping an opinion about, let alone one nothing reads.
    features.push({ type: 'Feature', properties: {}, geometry });
  }
  collections[source.key] = { type: 'FeatureCollection', features };
  console.log(
    `  -> ${features.length} features kept, ${dropped} degenerate after rounding`,
  );
}

const payload = JSON.stringify(collections);

// The payload is embedded as a JSON string parsed at load rather than as an
// object literal: V8 parses JSON.parse of a large string materially faster than
// it parses the equivalent literal, and it keeps the generated file free of
// anything a bundler might try to be clever about.
//
// That is only safe while the payload needs no escaping, so it is CHECKED rather
// than assumed. Coordinates are numbers and the keys are ASCII, so a quote or a
// backslash appearing here would mean the reducer let a property through.
//
// U+2028 and U+2029 are in the set for a subtler reason: they are legal RAW
// inside a JSON string and illegal raw inside a JavaScript one, so they are the
// one class of character that survives JSON.stringify unescaped and then breaks
// the emitted file. Nothing in Natural Earth contains them; the check is here so
// that stays a measurement rather than an assumption.
// (And yes: written as \u escapes. Spelling them literally here made THIS file
// a syntax error — the same trap, one level up.)
if (/['\\\u2028\u2029]/.test(payload)) {
  throw new Error('payload needs escaping — refusing to emit a broken literal');
}

const out = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  'src',
  'widgets',
  'natural-earth-data.ts',
);

writeFileSync(
  out,
  `// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// GENERATED FILE — DO NOT EDIT. Regenerate with:
//
//     node tools/gen-natural-earth.mjs
//
// Natural Earth 1:110m land + admin-0 land boundaries, ${TAG}, public domain.
// Coordinates rounded to ${PRECISION} decimal places. The generator carries the
// provenance, the licence reading and the reasoning behind both.
//
// 🔴 THIS MODULE IS ~${Math.round(payload.length / 1024)} KiB AND MUST STAY BEHIND A DYNAMIC IMPORT.
// It is reached only through loadLandStyle() in map-geometry.ts, so an operator
// who has a tile source configured — which is now everyone by default — never
// downloads a byte of it. A static import anywhere puts a world map in the entry
// chunk for every user of this package.

export interface NaturalEarthData {
  land: unknown;
  boundaries: unknown;
}

export const NATURAL_EARTH: NaturalEarthData = JSON.parse(
  '${payload}',
) as NaturalEarthData;
`,
);

console.log(`\nwrote ${out}`);
console.log(`payload ${(payload.length / 1024).toFixed(1)} KiB`);
