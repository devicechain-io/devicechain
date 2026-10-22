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
//   1. The lazy boundary around ~156 kB of Natural Earth geometry (44 kB gzipped).
//      esbuild without `splitting: true` INLINES dynamic imports of internal modules,
//      folding that payload into the main chunk. Nothing about behaviour changes, every
//      source scan stays green, and every consumer of this package — including our own
//      two apps — starts downloading a world map they will never be shown.
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
//
// 🔴 THIS IS NOT THE ONLY ARTIFACT GATE, AND THE OVERLAP IS DELIBERATE.
// frontend/scripts/verify-packages.mjs runs the same dialect scan over the PACKED
// TARBALL, across all four publishable packages, and then loads each entry under bare
// Node ESM — so it also sees what `files` left out, which this file cannot. What lives
// HERE and nowhere else is everything specific to this package's own shape: the lazy
// boundary around the Natural Earth geometry, the chunk it lives in, and the fact that
// the portable entry never reaches the /vite one. Deleting either file loses real
// coverage.

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
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

/** Every file the entry reaches through STATIC imports (what a host downloads up front). */
function staticGraph(entry: string): string[] {
  const STATIC_IMPORT = /(?:\bfrom|\bimport)\s*["'](\.{1,2}\/[^"']+\.js)["']/g;
  const seen = new Set<string>();
  const queue = [entry];
  while (queue.length > 0) {
    const file = queue.pop()!;
    if (seen.has(file)) continue;
    seen.add(file);
    for (const m of readFileSync(file, 'utf8').matchAll(STATIC_IMPORT)) queue.push(path.resolve(path.dirname(file), m[1]));
  }
  return [...seen];
}

const walkDist = (dir: string): string[] =>
  existsSync(dir) ? readdirSync(dir).map((name) => path.join(dir, name)) : [];

let main: Awaited<ReturnType<typeof graphOf>>;
let viteEntry: Awaited<ReturnType<typeof graphOf>>;

// Two esbuild passes over the built package: well under a second alone, but CPU-bound, so on a
// busy machine they outlast the 10 s default hook budget. They get their own.
beforeAll(async () => {
  [main, viteEntry] = await Promise.all([graphOf('index.js'), graphOf('vite.js')]);
}, 60_000);

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
        'consumer now downloads ~156 kB of map data whether or not they open a map',
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

  // The level, indicator and radial family is the second lazy boundary: its seven components
  // load on first mount from one chunk (widgets/level-lazy.tsx), so a board without one never
  // pays for them. A static import of any of them from the registry folds them back into the
  // entry, and only the artifact shows it. Both halves, as for the geometry: out of the entry,
  // and still shipped behind a dynamic import.
  //
  // The marker is each component's own definition, not a string the family shares: the gauge
  // (eager, and a public export) renders the same fitted readout, so `data-level-readout` is
  // legitimately in the entry and would say nothing about where the lazy seven landed.
  it('keeps the level and radial widgets in a lazily loaded chunk of their own', () => {
    const LAZY = ['LinearGauge', 'LevelGauge', 'Battery', 'StatusLight', 'SignalBars', 'Compass', 'ProgressRing'];
    const defines = (name: string) => new RegExp(`\\bfunction\\s+${name}\\s*\\(`);
    // The entry AND every chunk it imports statically: a component folded back in can land in a
    // shared chunk the entry imports rather than in index.js itself, and is downloaded all the same.
    const STATIC_IMPORT = /(?:\bfrom|\bimport)\s*["'](\.{1,2}\/[^"']+\.js)["']/g;
    const seen = new Set<string>();
    const queue = [path.join(distDir, 'index.js')];
    while (queue.length > 0) {
      const file = queue.pop()!;
      if (seen.has(file)) continue;
      seen.add(file);
      for (const m of readFileSync(file, 'utf8').matchAll(STATIC_IMPORT)) queue.push(path.resolve(path.dirname(file), m[1]));
    }
    const entryFiles = [...seen];
    for (const name of LAZY) {
      const holder = entryFiles.find((f) => defines(name).test(readFileSync(f, 'utf8')));
      expect(holder, `${name} was folded into the initial download (${holder})`).toBeUndefined();
    }
    const entry = read('index.js');
    const chunk = [...entry.matchAll(/import\(\s*["']([^"']+)["']\s*\)/g)]
      .map((m) => m[1])
      .find((spec) => spec.includes('level-widgets'));
    expect(chunk, 'no dynamic import of the level-widgets chunk in dist/index.js').toBeTruthy();
    const chunkPath = path.resolve(distDir, chunk!);
    expect(existsSync(chunkPath), `${chunk} is imported but was not emitted`).toBe(true);
    const code = readFileSync(chunkPath, 'utf8');
    // Reach control for the absence claim above: the same pattern finds every one HERE.
    for (const name of LAZY) {
      expect(defines(name).test(code), `${name} is not in the lazy chunk`).toBe(true);
    }
    // ...and for the pattern itself: it does find an eager widget's definition in the entry.
    expect(defines('Gauge').test(entry), 'the definition pattern cannot see the eager gauge').toBe(true);
  });

  // The command controls are the third: the slider and toggle, with the sending code they share,
  // load on first mount from a chunk of their own (widgets/control-lazy.tsx), separate from the
  // level family's so a board with one kind never downloads the other.
  it('keeps the command controls in a lazily loaded chunk of their own', () => {
    const CONTROL_PAYLOAD = '"data-last-sent"';
    expect(read('index.js').includes(CONTROL_PAYLOAD), 'the command controls were folded into the entry chunk').toBe(false);
    const specs = [...read('index.js').matchAll(/import\(\s*["']([^"']+)["']\s*\)/g)].map((m) => m[1]);
    const chunk = specs.find((spec) => spec.includes('control-widgets'));
    expect(chunk, `no dynamic import of the control-widgets chunk in dist/index.js (saw ${specs.join(', ')})`).toBeTruthy();
    const chunkPath = path.resolve(distDir, chunk!);
    expect(existsSync(chunkPath), `${chunk} is imported but was not emitted`).toBe(true);
    expect(readFileSync(chunkPath, 'utf8')).toContain(CONTROL_PAYLOAD);
    // Not in the level family's chunk: two families, two downloads.
    const levelSpec = specs.find((spec) => spec.includes('level-widgets'));
    expect(readFileSync(path.resolve(distDir, levelSpec!), 'utf8').includes(CONTROL_PAYLOAD)).toBe(false);
  });

  // The aggregate and categorical widgets are the fourth lazy boundary, and the aggregate feed (the
  // bucket fold and the refresh loop) is a fifth: a board without a stat card, bar chart or pie
  // chart downloads neither. Neither pulls a chart type into the build: the bar and pie are SVG, so
  // no ECharts bar or pie module exists anywhere in what ships, and the SDK's chart registration
  // (see its own dist-shape test) stays line and gauge.
  it('keeps the stat card, bar chart, pie chart and the aggregate feed in lazily loaded chunks of their own', () => {
    const entryFiles = staticGraph(path.join(distDir, 'index.js'));
    const definitions: Record<string, RegExp> = {
      StatCard: /\bfunction\s+StatCard\s*\(/,
      BarChart: /\bfunction\s+BarChart\s*\(/,
      PieChart: /\bfunction\s+PieChart\s*\(/,
      AggregateFeed: /\bfunction\s+AggregateFeed\s*\(/,
      aggregateBuckets: /\bfunction\s+aggregateBuckets\s*\(/,
    };
    for (const [name, pattern] of Object.entries(definitions)) {
      const holder = entryFiles.find((f) => pattern.test(readFileSync(f, 'utf8')));
      expect(holder, `${name} was folded into the initial download (${holder})`).toBeUndefined();
    }
    const entry = read('index.js');
    const specs = [...entry.matchAll(/import\(\s*["']([^"']+)["']\s*\)/g)].map((m) => m[1]);
    const chunkOf = (needle: string) => {
      const spec = specs.find((s) => s.includes(needle));
      expect(spec, `no dynamic import of the ${needle} chunk in dist/index.js (saw ${specs.join(', ')})`).toBeTruthy();
      const file = path.resolve(distDir, spec!);
      expect(existsSync(file), `${spec} is imported but was not emitted`).toBe(true);
      return readFileSync(file, 'utf8');
    };
    const widgets = chunkOf('aggregate-widgets');
    for (const name of ['StatCard', 'BarChart', 'PieChart']) {
      expect(definitions[name].test(widgets), `${name} is not in the lazy widget chunk`).toBe(true);
    }
    const feed = chunkOf('aggregate-feed');
    for (const name of ['AggregateFeed', 'aggregateBuckets']) {
      expect(definitions[name].test(feed), `${name} is not in the lazy feed chunk`).toBe(true);
    }
    // Two chunks, two downloads: the cards do not carry the feed, nor the feed the cards.
    expect(definitions.AggregateFeed.test(widgets)).toBe(false);
    expect(definitions.StatCard.test(feed)).toBe(false);
    // Reach control for the absence claim: the same patterns find an eager widget's definition.
    expect(/\bfunction\s+LatestCard\s*\(/.test(entry), 'the definition pattern cannot see the eager card').toBe(true);
  });

  // The fleet widgets are the sixth lazy boundary. The table lives in the aggregate widgets' chunk
  // (it is one more widget of that family), and the per-device feed is a chunk of its own: a board
  // without a fleet table or a per-device bar chart downloads neither the feed, nor the roster and
  // seed state it keeps, nor the pure fleet model. Only the descriptor is eager.
  it('keeps the fleet table and the per-device feed in lazily loaded chunks of their own', () => {
    const entryFiles = staticGraph(path.join(distDir, 'index.js'));
    const definitions: Record<string, RegExp> = {
      FleetTable: /function\s+FleetTable\s*\(/,
      FleetFeed: /function\s+FleetFeed\s*\(/,
      buildFleet: /function\s+buildFleet\s*\(/,
      sortMembers: /function\s+sortMembers\s*\(/,
    };
    for (const [name, pattern] of Object.entries(definitions)) {
      const holder = entryFiles.find((f) => pattern.test(readFileSync(f, 'utf8')));
      expect(holder, `${name} was folded into the initial download (${holder})`).toBeUndefined();
    }
    const entry = read('index.js');
    const specs = [...entry.matchAll(/import\(\s*["']([^"']+)["']\s*\)/g)].map((m) => m[1]);
    const chunkOf = (needle: string) => {
      const spec = specs.find((s) => s.includes(needle));
      expect(spec, `no dynamic import of the ${needle} chunk in dist/index.js (saw ${specs.join(', ')})`).toBeTruthy();
      const file = path.resolve(distDir, spec!);
      expect(existsSync(file), `${spec} is imported but was not emitted`).toBe(true);
      return readFileSync(file, 'utf8');
    };
    expect(definitions.FleetTable.test(chunkOf('aggregate-widgets')), 'FleetTable is not in the lazy widget chunk').toBe(true);
    const feed = chunkOf('fleet-feed');
    expect(definitions.FleetFeed.test(feed), 'FleetFeed is not in the lazy feed chunk').toBe(true);
    // The pure fleet model is shared by the feed (which builds the state) and the table and bar
    // (which read it), so it sits in a chunk both load -- never in the initial download.
    const lazy = walkDist(path.join(distDir, 'chunks')).filter((f) => f.endsWith('.js'));
    expect(
      lazy.some((f) => definitions.buildFleet.test(readFileSync(f, 'utf8'))),
      'the fleet model is in no lazy chunk',
    ).toBe(true);
    // Two chunks, two downloads: the feed does not carry the table, nor the table the feed.
    expect(definitions.FleetTable.test(feed)).toBe(false);
    expect(definitions.FleetFeed.test(chunkOf('aggregate-widgets'))).toBe(false);
  });

  // The alarm table and count, the entity selector, the measurement table and the command button
  // are the seventh boundary, and the first whose components are ALSO public exports
  // (`AlarmTable`, `AlarmCount`, `EntitySelector`, `Table`, `CommandButton`). A public export
  // keeps a component in the static graph of THIS package's entry, so "absent from the entry
  // graph" cannot be asserted here the way it is above. What makes the boundary real is two facts
  // a host bundler (Vite 8 / rolldown) acts on, and they are what this asserts:
  //
  //   1. each component is a module of its own, OUTSIDE index.js, and the registry reaches it only
  //      through its family's dynamic import -- so a host that never touches the public export
  //      can leave the module out of its first download;
  //   2. the package declares its modules free of side effects (the test after this one), without
  //      which rolldown keeps every module the entry imports -- measured: without the field the
  //      replay app's first download did not shrink at all.
  //
  // The outcome itself -- none of them in a real host's first download -- is asserted on a real
  // host build by apps/board-replay/scripts/check-bundle-size.mjs, which runs on every build.
  // (esbuild cannot stand in for that host: its splitting does not drop a shared chunk an entry
  // imports but does not use, so an esbuild "host" would keep them all.)
  it('keeps the alarm, selection, table and button components out of the entry module, behind dynamic imports', () => {
    // Not preceded by an identifier character, so `Table` does not match `function FleetTable(`.
    const defines = (name: string) => new RegExp(`(?:^|[^\\w$])function\\s+${name}\\s*\\(`);
    const entry = read('index.js');
    const specs = [...entry.matchAll(/import\(\s*["']([^"']+)["']\s*\)/g)].map((m) => m[1]);
    const families: Record<string, string[]> = {
      'alarm-widgets': ['AlarmTable', 'AlarmCount', 'AlarmSeverity'],
      'entity-selector': ['EntitySelector'],
      table: ['Table'],
      'control-widgets': ['CommandButton'],
      'presence-widgets': ['DeviceStatus', 'OnlineDoughnut'],
    };
    for (const [chunk, names] of Object.entries(families)) {
      const spec = specs.find((s) => path.basename(s).startsWith(`${chunk}-`));
      expect(spec, `no dynamic import of the ${chunk} chunk in dist/index.js (saw ${specs.join(', ')})`).toBeTruthy();
      const chunkPath = path.resolve(distDir, spec!);
      expect(existsSync(chunkPath), `${spec} is imported but was not emitted`).toBe(true);
      // What the dynamic chunk loads: itself and every chunk it imports statically.
      const reached = staticGraph(chunkPath).map((f) => readFileSync(f, 'utf8'));
      for (const name of names) {
        expect(defines(name).test(entry), `${name} is defined in dist/index.js itself, where no host can leave it out`).toBe(false);
        expect(reached.some((code) => defines(name).test(code)), `${name} is not reached from the ${chunk} chunk`).toBe(true);
      }
    }
    // Reach control for the pattern: it does find an eager widget defined in the entry.
    expect(defines('LatestCard').test(entry), 'the definition pattern cannot see the eager card').toBe(true);
  });

  // Audited, not assumed: no module this package builds does anything at import time that a host
  // needs -- no stylesheet import, no registration with a global, no listener, no polyfill (the
  // map stylesheet is a DYNAMIC import, called by the renderer). The /vite entry is listed anyway,
  // conservatively: it is the one module whose import a bundler transforms (`?worker&url`), and it
  // costs a host nothing it did not ask for. A module that gains an import-time effect must be
  // added here. See the test above for why the field exists at all.
  it('declares its modules free of side effects, except the /vite entry', () => {
    const pkg = JSON.parse(readFileSync(path.join(distDir, '../package.json'), 'utf8')) as { sideEffects?: unknown };
    expect(pkg.sideEffects).toEqual(['./dist/vite.js']);
  });

  // The part of that audit a regex can hold: a bare `import "x"` -- an import kept ONLY for what
  // it does on load -- is the shape a new import-time effect most often takes (a stylesheet, a
  // polyfill, a module that registers itself), and under the field above a host bundler drops it.
  // Between this package's own chunks a bare import is the bundler's wiring, so only a bare import
  // of ANOTHER package is refused.
  it('imports no other package for its side effects alone', () => {
    const BARE = /(?:^|[;\n])\s*import\s*["']([^"'.][^"']*)["']/g;
    // Reach control: the pattern sees a bare package import, and passes over a chunk's own wiring.
    expect([...'x;import "polyfill";\nimport"./chunks/a.js"'.matchAll(BARE)].map((m) => m[1])).toEqual(['polyfill']);
    const files = [path.join(distDir, 'index.js'), path.join(distDir, 'vite.js'), ...walkDist(path.join(distDir, 'chunks'))];
    const found = files
      .filter((f) => f.endsWith('.js'))
      .flatMap((f) => [...readFileSync(f, 'utf8').matchAll(BARE)].map((m) => `${path.basename(f)}: ${m[1]}`));
    expect(found).toEqual([]);
  });

  it('draws the bar and pie without ECharts: no chart type of either kind is imported anywhere in the build', () => {
    const files = [...staticGraph(path.join(distDir, 'index.js')), ...walkDist(path.join(distDir, 'chunks'))];
    for (const file of new Set(files)) {
      if (!file.endsWith('.js')) continue;
      const code = readFileSync(file, 'utf8');
      expect(code, `${file} imports a bar or pie chart type`).not.toMatch(/\b(BarChart|PieChart)\b[^;]*from\s*["']echarts/);
    }
    // Reach control: the scan does read the chunk the bar and pie live in.
    expect(walkDist(path.join(distDir, 'chunks')).some((f) => /aggregate-widgets/.test(f))).toBe(true);
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
  // The SDK is imported twice, by its root and by its `/echarts` subpath: the widgets no
  // longer import ECharts themselves (the SDK's chart helpers own it), and the subpath
  // specifier surviving the bundle is what keeps ONE copy of the chart wrapper in a host.
  it('imports its sibling packages by bare specifier, rather than bundling them', () => {
    for (const peer of [
      '@devicechain/client',
      '@devicechain/dashboards',
      '@devicechain/widget-sdk',
      '@devicechain/widget-sdk/echarts',
      'react',
    ]) {
      expect(main.imports, `${peer} is not imported as an external`).toContain(peer);
    }
  });
});
