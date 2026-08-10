// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// ---- Lazy-load boundaries the console must not cross -----------------------
//
// 🔴 A SOURCE-LEVEL GUARD, and it has to be, because the defect it catches is
// invisible to every behavioural test in this app.
//
// The WIDGETS package already gates this boundary — but that scan walks
// `packages/widgets/src` and nothing else, so it could not see the console at
// all. (Nor can either scan see `packages/dashboards/src` or
// `packages/client/src`, which also bundle into this entry chunk; neither
// touches MapLibre today, and closing that remaining gap is its own task rather
// than something this file can reach with a relative glob.)
// The geofence editor is the first console code to touch
// MapLibre, which means that until this file existed, one ordinary top-level
// import here would have folded ~200 KB gzipped of map renderer into the entry
// chunk for EVERY operator — including everyone who never opens a geofence — and
// nothing anywhere would have gone red.
//
// Nothing about behaviour changes when that happens. The only observable is the
// shipped bundle, and a bundle is not something a unit test can see, so the
// CAUSE is gated here rather than the effect.
//
// `import type` is fine and is used deliberately: type imports are erased before
// a bundler sees them.
//
// The sources are read through Vite's glob rather than node:fs — the same idiom
// i18n/parity.test.ts uses — because this app's tsconfig pulls in no Node types,
// and because a glob resolves the way the BUNDLER resolves, which is the thing
// whose behaviour is actually under test.

import { describe, expect, it } from 'vitest';

const SOURCES = import.meta.glob('./**/*.{ts,tsx}', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>;

/** Every non-test source file in the console, as [path, contents]. */
const FILES = Object.entries(SOURCES).filter(([path]) => !/\.test\.tsx?$/.test(path));

/**
 * A top-level import of the module that is NOT type-only: `import x from …`,
 * `import { a } from …`, `import * as x from …`, bare `import '…'`, and
 * `export … from …`.
 *
 * 🔴 The character class is `[^;]`, NOT `[^;\n]`. A class excluding newlines can
 * never match a wrapped import — and a wrapped import is the LIKELY offender,
 * because prettier wraps any binding list past the line width and a real MapLibre
 * consumer imports several names. The semicolon is what keeps the non-greedy
 * match from spanning two statements; the newline was never doing that job.
 */
function staticValueImport(module: string): RegExp {
  const m = module.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(
    `^\\s*(?:import|export)\\s+(?!type\\b)[^;]*?from\\s*['"]${m}['"]|^\\s*import\\s*['"]${m}['"]`,
    'm',
  );
}

const MAPLIBRE = staticValueImport('maplibre-gl');

describe('the MapLibre lazy boundary', () => {
  it('is not crossed by any static import in the console', () => {
    const offenders = FILES.filter(([, source]) => MAPLIBRE.test(source)).map(([path]) => path);
    expect(
      offenders,
      'a static import of maplibre-gl folds the whole map renderer into the console entry chunk for every operator',
    ).toEqual([]);
  });

  // 🔴 The negative control. The check above is an ABSENCE claim over a file
  // list, and an absence claim is worth nothing until the list is shown to be
  // non-empty AND to contain the file that would offend if anything did. A
  // rotted glob would report "no offenders" forever, and would keep reporting it
  // long after someone moved the map code somewhere the glob does not reach.
  it('the scan actually reaches the file that loads the library', () => {
    expect(FILES.length).toBeGreaterThan(50);

    const fenceMap = FILES.find(([path]) => path.endsWith('/geofences/FenceMap.tsx'));
    expect(fenceMap, 'the console file that loads MapLibre was not found by the scan').toBeTruthy();

    const source = fenceMap![1];
    expect(source).toMatch(/import\(\s*['"]maplibre-gl['"]\s*\)/); // the dynamic load
    expect(source).toMatch(/import type .*from 'maplibre-gl'/); // erased, so allowed
  });

  // 🔴 THE SIBLING RULE, AND THE ONE WITH NO RUNTIME TEST BEHIND IT.
  //
  // Loading MapLibre lazily is only half of loading it correctly. MapLibre 6 parses
  // tiles and geometry in a web worker whose URL it derives AT RUNTIME from its own
  // module URL — a string no bundler can follow — so unless the importer asks for the
  // worker through Vite's `?worker&url` entry and hands the emitted URL to
  // `setWorkerUrl`, nothing emits the file, the browser resolves it to a 404, and
  // `new Worker()` does not throw: the failure surfaces later as an async error event
  // and the map simply renders nothing.
  //
  // This is a SOURCE check rather than a behavioural one because FenceMap drives real
  // MapLibre and cannot run under jsdom, so nothing in this app executes that loader.
  // (The dashboard map widget's loader has the same rule enforced at runtime, in
  // packages/widgets/src/widgets/map.test.tsx.) A source check is weaker than running
  // it — it proves the call is written, not that it works — but the alternative here
  // was no check at all on the surface an operator actually draws fences with.
  it('every module that loads MapLibre also points its worker at an emitted URL', () => {
    const loaders = FILES.filter(([, source]) => /import\(\s*['"]maplibre-gl['"]\s*\)/.test(source));
    // An absence claim over an empty list again: name the loaders, so a rotted glob
    // or a moved file reports a failure rather than a clean sweep of nothing.
    expect(loaders.map(([path]) => path)).toEqual([
      './routes/geofences/FenceMap.tsx',
    ]);

    for (const [path, source] of loaders) {
      expect(source, `${path} does not import the worker entry`).toMatch(
        /import\(\s*['"]maplibre-gl\/dist\/maplibre-gl-worker\.mjs\?worker&url['"]\s*\)/,
      );
      expect(source, `${path} never calls setWorkerUrl`).toMatch(/setWorkerUrl\(/);
    }
  });

  it('the pattern fires on a value import and stays quiet on a type import', () => {
    // Without this, a regex that matched nothing at all would pass the scan and
    // the control alike.
    expect(MAPLIBRE.test("import maplibregl from 'maplibre-gl';")).toBe(true);
    expect(MAPLIBRE.test("import { Map } from 'maplibre-gl';")).toBe(true);
    expect(MAPLIBRE.test("import * as ml from 'maplibre-gl';")).toBe(true);
    expect(MAPLIBRE.test("import 'maplibre-gl';")).toBe(true);
    expect(MAPLIBRE.test("export { Map } from 'maplibre-gl';")).toBe(true);
    // 🔴 The WRAPPED forms. An earlier draft used a character class that excluded
    // newlines, so every one of these slipped through — and this is the shape
    // prettier produces the moment a binding list passes the line width, which is
    // to say the shape a real MapLibre consumer would actually have.
    expect(MAPLIBRE.test("import {\n  Map,\n  NavigationControl,\n} from 'maplibre-gl';")).toBe(
      true,
    );
    expect(MAPLIBRE.test("import\n  maplibregl\nfrom 'maplibre-gl';")).toBe(true);
    expect(MAPLIBRE.test("export {\n  Map,\n} from 'maplibre-gl';")).toBe(true);
    // ...and the wrapped TYPE import still must not fire.
    expect(MAPLIBRE.test("import type {\n  Map,\n  Marker,\n} from 'maplibre-gl';")).toBe(false);
    // A preceding unrelated statement must not let the match run into this one:
    // the semicolon, not the newline, is what bounds it.
    expect(MAPLIBRE.test("import type { Map } from 'maplibre-gl';\nconst x = 1;")).toBe(false);

    expect(MAPLIBRE.test("import type { Map } from 'maplibre-gl';")).toBe(false);
    expect(MAPLIBRE.test("  const m = await import('maplibre-gl');")).toBe(false);
    // And it must not fire on a DIFFERENT module whose name merely starts with
    // this one, or the guard would fail on an unrelated dependency.
    expect(MAPLIBRE.test("import x from 'maplibre-gl-draw';")).toBe(false);
  });
});
