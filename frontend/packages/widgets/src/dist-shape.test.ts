// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// @vitest-environment node

// ---- What actually ships, read off the artifact ----------------------------
//
// 🔴 THIS FILE READS `dist/`, NOT `src/`, AND THAT IS THE ENTIRE POINT. Every other
// guard around this package scans sources: bundler-dialect.test.ts builds the entries
// in memory, the two apps' bundle-boundaries scans read their own text. All of them
// are proxies for a question only the built package can answer — what does a consumer
// download, and what is in it?
//
// Two invariants live here because the BUILD is where they can be broken silently:
//
//   1. The lazy boundary around ~152 KiB of Natural Earth geometry. esbuild without
//      `splitting: true` INLINES dynamic imports of internal modules, folding that
//      payload into the main chunk. Nothing about behaviour changes, every source
//      scan stays green, and every consumer of this package — including our own two
//      apps — starts downloading a world map they will never be shown.
//
//   2. The bundler-dialect carve-out. `?worker&url` is legal in exactly one module,
//      `vite.ts`, reached only by name. If it ever appears in the main entry, a
//      webpack or Rollup consumer gets a specifier their bundler cannot resolve —
//      and, because the library it points into is external and the specifier is
//      therefore only ever COPIED, never resolved, no build anywhere says so.
//
// 🔴 THE SPECIFIERS ARE READ BY A PARSER, NOT BY A REGEX OVER THE FILE. esbuild does
// not strip comments unless it is minifying, so this package's own source comments —
// several of which QUOTE the forbidden specifier while explaining the rule — are still
// in `dist/index.js`. A text scan would flag those, and the first fix for that is to
// start stripping comments by hand, which is how the source-level version of this gate
// acquired three separate evasions. Ask esbuild instead: `metafile.inputs[*].imports`
// is every specifier the real parser saw, with the comments already gone.
//
// `npm test` in this package runs `build-packages.mjs` first, so `dist` is this
// package's current source rather than whatever was last left there.

import { existsSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as esbuild from 'esbuild';
import { beforeAll, describe, expect, it } from 'vitest';

const distDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../dist');
const read = (rel: string) => readFileSync(path.join(distDir, rel), 'utf8');

/** A marker from the Natural Earth payload itself, not from the module that loads it. */
const GEOMETRY_PAYLOAD = '"land":{"type":"FeatureCollection"';

/** A specifier only one bundler understands: a query string, or a stylesheet as a module. */
const isDialect = (specifier: string) => specifier.includes('?') || specifier.endsWith('.css');

/**
 * Every module reachable from a built entry, and every specifier each one imports, as
 * the parser saw them. `packages: 'external'` keeps node_modules out (we want THIS
 * package's graph) while still recording the bare specifiers it points at.
 */
async function graphOf(entry: string) {
  const result = await esbuild.build({
    entryPoints: [path.join(distDir, entry)],
    bundle: true,
    write: false,
    metafile: true,
    format: 'esm',
    platform: 'browser',
    packages: 'external',
    logLevel: 'silent',
  });
  const inputs = Object.keys(result.metafile.inputs);
  const imports = Object.values(result.metafile.inputs).flatMap((input) =>
    input.imports.map((i) => i.path),
  );
  return { inputs, imports };
}

let main: Awaited<ReturnType<typeof graphOf>>;
let viteEntry: Awaited<ReturnType<typeof graphOf>>;

beforeAll(async () => {
  main = await graphOf('index.js');
  viteEntry = await graphOf('vite.js');
});

describe('the built package', () => {
  // 🔴 The reach control, first, because most of what follows is an absence claim over
  // file contents — and an absence claim over a file nobody opened is worth nothing. A
  // missing or truncated dist would otherwise make every assertion here pass.
  it('is on disk and was actually read', () => {
    for (const file of ['index.js', 'index.d.ts', 'vite.js', 'vite.d.ts']) {
      expect(existsSync(path.join(distDir, file)), `dist/${file} is missing`).toBe(true);
    }
    // The whole widget set, bundled: tens of KB. A near-empty entry would satisfy
    // every "does not contain" assertion below.
    expect(statSync(path.join(distDir, 'index.js')).size).toBeGreaterThan(20_000);
    expect(read('index.js')).toContain('DashboardRenderer');
    // And the parser reached it too — an empty graph is the same false green.
    expect(main.imports.length).toBeGreaterThan(5);
  });

  it('keeps the Natural Earth geometry out of the main entry', () => {
    expect(
      read('index.js').includes(GEOMETRY_PAYLOAD),
      'the world geometry was inlined into the entry chunk — `splitting` is off, and every ' +
        'consumer now downloads ~152 KiB of map data whether or not they open a map',
    ).toBe(false);
  });

  // The other half of the same claim, and the half that makes it mean anything: the
  // geometry has to still be REACHABLE, in a chunk of its own, behind a dynamic import.
  // "Not in the entry" is equally true of a payload that was dropped altogether.
  it('still ships the geometry, in a chunk of its own, behind a dynamic import', () => {
    const dynamic = [...read('index.js').matchAll(/import\(\s*["']([^"']+)["']\s*\)/g)].map(
      (m) => m[1],
    );
    const chunk = dynamic.find((spec) => spec.includes('natural-earth-data'));
    expect(
      chunk,
      `no dynamic import of the geometry chunk in dist/index.js (saw ${dynamic.join(', ')})`,
    ).toBeTruthy();

    const chunkPath = path.resolve(distDir, chunk!);
    expect(existsSync(chunkPath), `${chunk} is imported but was not emitted`).toBe(true);
    expect(readFileSync(chunkPath, 'utf8')).toContain(GEOMETRY_PAYLOAD);
  });

  it('writes no bundler dialect anywhere in the main entry graph', () => {
    expect(
      main.imports.filter(isDialect),
      'a bundler-specific specifier in the main entry is unresolvable for a webpack or ' +
        'Rollup consumer, and no build anywhere reports it',
    ).toEqual([]);
  });

  // The carve-out, asserted as an exact set rather than as "at least the worker". A
  // third dialect specifier appearing here would be a change to the contract this entry
  // documents, and should have to be written down.
  it('confines the dialect to the /vite entry, exactly two specifiers', () => {
    expect(viteEntry.imports.filter(isDialect)).toEqual([
      'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url',
      'maplibre-gl/dist/maplibre-gl.css',
    ]);
  });

  it('does not reach the /vite entry from the main one', () => {
    // Chunk splitting could otherwise route the two entries through shared code and
    // drag the dialect back into the portable entry's graph.
    expect(main.inputs.filter((input) => input.endsWith('vite.js'))).toEqual([]);
  });

  // 🔴 The structural half of the mock-propagation claim. Its behavioural half lives in
  // apps/console/src/package-dist-mocking.test.ts, which proves vitest intercepts a bare
  // specifier imported from INSIDE a built package. That proof only says something about
  // THIS package if this package really does import its siblings by bare specifier
  // rather than having bundled them in — which is what this asserts.
  //
  // `echarts/core`, not `echarts`: the widgets import ECharts by subpath and never by
  // its bare name, so that is the specifier the artifact actually carries.
  it('imports its sibling packages by bare specifier, rather than bundling them', () => {
    for (const peer of ['@devicechain/client', '@devicechain/dashboards', 'react', 'echarts/core']) {
      expect(main.imports, `${peer} is not imported as an external`).toContain(peer);
    }
  });
});
