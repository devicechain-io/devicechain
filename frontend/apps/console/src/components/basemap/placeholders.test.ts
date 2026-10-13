// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The tile-template placeholder allow-list, held against the renderer that defines it.
//
// 🔴 THIS TEST EXISTS BECAUSE THE LIST WAS WRITTEN FROM MEMORY AND WAS WRONG. The
// server refuses a tile URL containing any {token} MapLibre does not substitute — a
// good rule, because most tile documentation on the internet is written for Leaflet
// and its {s} subdomain token produces a stored URL that cannot draw. But the first
// draft of the allow-list omitted {prefix}, a real MapLibre token, so the guard
// refused a URL that renders perfectly and told the operator, wrongly, that their
// working template could not draw a single tile. The same false enumeration reached
// the error message and the published documentation in two languages.
//
// The fix is not "be more careful next time". A list of someone else's tokens,
// restated by hand in another language, drifts the moment they add one — silently,
// and in the fail-CLOSED direction, which shows up as a support ticket rather than a
// broken build. So the two are compared directly:
//
//   MapLibre's own source  ←→  knownPlaceholders in the Go validator
//
// Both sides are read as text. That is unlovely and it is the point: the alternative
// is a third restatement, which is the thing that broke.

/// <reference types="node" />
// ^ Node's types are not ambient in this app (it targets the browser), and this test
// deliberately reads two files off disk. Same directive, same reason, as
// packages/widgets/src/sim-dashboard-fixture.test.ts.

import { existsSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// The code the console actually bundles: the package's ESM entry, plus every chunk it
// pulls in through a relative static import.
//
// 🔴 NOT A FIXED CHUNK NAME. This used to read dist/maplibre-gl-shared.mjs, where
// Tile.prototype.getTileURL lived through 6.12. In 6.13 the entry stopped importing
// that chunk and inlined the tile code instead, yet the file still ships — so a
// hardcoded path would go on reading a file that exists and that nothing loads.
// Following the entry's own imports reads whatever the bundler is handed, however the
// next release splits it.
//
// Every path is resolved from the package root (vitest's cwd), not from this file,
// because `import.meta.url` is not a file: URL under the jsdom environment.
const MAPLIBRE_PACKAGE = resolve(process.cwd(), '../../node_modules/maplibre-gl');

const GO_VALIDATOR = resolve(
  process.cwd(),
  '../../../backend/services/user-management/basemap/basemap.go',
);

/** The ESM entry named by the package's `exports['.'].import`. */
function maplibreEntry(): string {
  const pkg = JSON.parse(readFileSync(resolve(MAPLIBRE_PACKAGE, 'package.json'), 'utf8'));
  const entry = pkg.exports?.['.']?.import;
  if (typeof entry !== 'string') {
    throw new Error("maplibre-gl's package.json has no exports['.'].import string");
  }
  return resolve(MAPLIBRE_PACKAGE, entry);
}

/** The entry and every module reachable from it through relative static imports. */
function maplibreSources(): string[] {
  const seen = new Set<string>();
  const queue = [maplibreEntry()];
  while (queue.length > 0) {
    const file = queue.shift()!;
    if (seen.has(file)) continue;
    seen.add(file);
    const src = readFileSync(file, 'utf8');
    for (const m of src.matchAll(/(?:\bfrom|\bimport)\s*["'`](\.{1,2}\/[^"'`]+\.m?js)["'`]/g)) {
      queue.push(resolve(dirname(file), m[1]));
    }
  }
  return [...seen].map((file) => readFileSync(file, 'utf8'));
}

/** Every `{token}` MapLibre substitutes, read out of its `.replace(/{token}/g, …)` chain. */
function rendererTokens(): Set<string> {
  const found = new Set<string>();
  for (const src of maplibreSources()) {
    for (const m of src.matchAll(/\.replace\(\/\\?\{([a-z0-9-]+)\\?\}\/g/gi)) {
      found.add(m[1]);
    }
  }
  return found;
}

/** The allow-list the server enforces, read out of the Go map literal. */
function validatorTokens(): Set<string> {
  const src = readFileSync(GO_VALIDATOR, 'utf8');
  const block = /var knownPlaceholders = map\[string\]bool\{([\s\S]*?)\n\}/.exec(src);
  if (!block) throw new Error('could not find knownPlaceholders in the Go validator');
  const found = new Set<string>();
  for (const m of block[1].matchAll(/"([^"]+)":\s*true/g)) found.add(m[1]);
  return found;
}

describe('the placeholder allow-list', () => {
  // 🔴 The control, and it has to come first: both readers are regexes over other
  // people's files, and a regex that matches nothing would make every comparison
  // below trivially true. A minified rename or a refactor of that chain must fail
  // HERE, loudly, rather than turning the real assertion into a tautology.
  it('can actually read both sides', () => {
    // 🔴 Path first. readFileSync would throw anyway, but a named assertion turns
    // "some file moved" into a one-line diagnosis instead of an ENOENT stack.
    expect(existsSync(MAPLIBRE_PACKAGE), `no MapLibre package at ${MAPLIBRE_PACKAGE}`).toBe(true);
    expect(existsSync(maplibreEntry()), `no MapLibre entry at ${maplibreEntry()}`).toBe(true);
    expect(existsSync(GO_VALIDATOR), `no Go validator at ${GO_VALIDATOR}`).toBe(true);

    const renderer = rendererTokens();
    const validator = validatorTokens();

    expect(renderer.size, 'read no tokens out of the MapLibre source').toBeGreaterThan(3);
    expect(validator.size, 'read no tokens out of the Go validator').toBeGreaterThan(3);
    // Anchors: two tokens that cannot plausibly disappear without this test needing
    // to be revisited anyway.
    expect(renderer.has('z')).toBe(true);
    expect(renderer.has('quadkey')).toBe(true);
  });

  it('matches MapLibre exactly — no token the renderer substitutes is refused', () => {
    const renderer = [...rendererTokens()].sort();
    const validator = [...validatorTokens()].sort();

    // Reported as sorted arrays rather than set membership so a failure names the
    // token that drifted instead of saying two sets differ.
    expect(validator).toEqual(renderer);
  });

  // The specific regression. Kept as its own case so the failure message says what
  // actually went wrong rather than making the next person diff two lists.
  it('includes {prefix}, the token the first draft forgot', () => {
    expect(validatorTokens().has('prefix')).toBe(true);
    expect(rendererTokens().has('prefix')).toBe(true);
  });

  // Leaflet's subdomain token is the whole reason the rule exists, and MapLibre does
  // not substitute it. If MapLibre ever does, the rule needs rethinking rather than
  // quietly widening.
  it('excludes Leaflet\'s {s}, which MapLibre does not substitute', () => {
    expect(rendererTokens().has('s')).toBe(false);
    expect(validatorTokens().has('s')).toBe(false);
  });
});
