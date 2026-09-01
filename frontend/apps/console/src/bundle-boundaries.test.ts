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
  //
  // 🔴 WHERE THE URL COMES FROM CHANGED, AND THE RULE DID NOT. It used to be that each
  // loader imported the `?worker&url` entry itself. Now the app has exactly one runtime
  // (src/map-runtime.ts, from @devicechain/widgets/vite) and every loader reads it, so what
  // this asserts is that a loader still POINTS the worker somewhere deliberate — not that
  // it writes the incantation. Asserting the old shape here would now force the duplication
  // back in.
  it('every module that loads MapLibre also points its worker at the app’s one runtime', () => {
    const loaders = FILES.filter(([, source]) => /import\(\s*['"]maplibre-gl['"]\s*\)/.test(source));
    // An absence claim over an empty list again: name the loaders, so a rotted glob
    // or a moved file reports a failure rather than a clean sweep of nothing.
    expect(loaders.map(([path]) => path)).toEqual([
      './routes/geofences/FenceMap.tsx',
    ]);

    for (const [path, source] of loaders) {
      expect(source, `${path} never calls setWorkerUrl`).toMatch(/setWorkerUrl\(/);
      expect(source, `${path} does not source its worker URL from MAP_RUNTIME`).toMatch(
        /MAP_RUNTIME\.workerUrl/,
      );
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

// ---- The console as a HOST of @devicechain/widgets ---------------------------
//
// 🔴 A SECOND MAPLIBRE OBLIGATION, and it is not the one above. The tests above are about
// what this app must not FOLD INTO ITS MAIN BUNDLE. This one is about what it must SUPPLY.
//
// `@devicechain/widgets` is published to npm, so it cannot write a bundler's dialect: a
// library containing `?worker&url` works under a consumer's Vite and renders a silent blank
// map under webpack or Rollup, and because the specifier is externalized, no build anywhere
// reports it. The widget therefore takes its MapLibre worker URL from the host — and this
// app is a host.
//
// Why a test rather than trust: deleting the provider breaks no build, fails no typecheck,
// and fails no behavioural test here, because nothing in the console renders a map widget
// under jsdom (FenceMap drives real MapLibre and cannot). It changes exactly one thing —
// every map widget on every board becomes a "Map runtime not configured" notice — and the
// first person to find out is an operator.
describe('the console supplies the map runtime that @devicechain/widgets requires', () => {
  it('installs MapRuntimeProvider exactly once, high in the tree', () => {
    const installers = FILES.filter(([, source]) => /<MapRuntimeProvider\b/.test(source));

    // Named rather than counted, for the same reason as every other absence claim in this
    // file: a rotted glob agrees with an empty list. Naming it also states the invariant —
    // ONE install site, so no render site can be forgotten. The console renders widgets on
    // the dashboard workspace, the canvas editor and the synthetic preview; wrapping those
    // individually is the version of this that half-works.
    expect(installers.map(([path]) => path)).toEqual(['./auth/TenantProvider.tsx']);
  });

  it('takes the runtime from the package rather than hand-rolling it', () => {
    const runtime = FILES.find(([path]) => path === './map-runtime.ts');
    expect(runtime, 'the module supplying the console map runtime has moved or gone').toBeTruthy();

    const [, source] = runtime as [string, string];
    expect(source).toMatch(/from '@devicechain\/widgets\/vite'/);
  });

  // 🔴 ONE WORKER URL PER APP, AND THIS IS THE ASSERTION THAT KEEPS IT THAT WAY.
  //
  // The fence editor drives its own MapLibre instance and needs the same bootstrap, so for
  // a while this app wrote `?worker&url` twice — once for the widget host, once inside
  // FenceMap. Two copies of a bundler incantation in one app is one copy too many: when
  // they drift, one surface goes blank and the other keeps working, which is the hardest
  // version of this bug to find. FenceMap now reads the app's single runtime.
  //
  // This also replaces a redundant "the scan reached these files" test. Its assertions were
  // implied by the presence claims above — anything that failed it failed those first — so
  // it could never be the check that fired. This one can.
  // 🔴 ANCHORED TO IMPORT SYNTAX, and the first draft was not — it matched raw text, so it
  // flagged src/map-runtime.ts for a COMMENT explaining this very rule. That is the same
  // comment-blindness that broke the package-side gate, reproduced inside the guard written
  // to replace it. Requiring the match to begin a line with `import`/`export` excludes `//`
  // lines and JSDoc ` * ` lines, which is where prose about a dialect actually lives.
  //
  // Stated limit: a dialect specifier in a mid-line dynamic `import()` would slip past. That
  // is accepted here because this is an APP-side guard and the authoritative, parser-based
  // check on what SHIPS lives in packages/widgets/src/bundler-dialect.test.ts.
  const DIALECT_IMPORT =
    /^\s*(?:import|export)\s[^;]*?['"][^'"]*\?(?:worker|url|raw|inline)\b[^'"]*['"]/m;

  it('writes no bundler dialect of its own — the package’s /vite entry is the only source', () => {
    const offenders = FILES.filter(([, source]) => DIALECT_IMPORT.test(source)).map(([path]) => path);
    expect(offenders).toEqual([]);
  });

  it('...and that pattern fires on a real one while ignoring prose about it', () => {
    expect(DIALECT_IMPORT.test("import w from 'maplibre-gl/dist/x.mjs?worker&url';")).toBe(true);
    expect(DIALECT_IMPORT.test("import u from './icon.svg?url';")).toBe(true);
    // The two shapes that made the first draft fail on its own documentation.
    expect(DIALECT_IMPORT.test("// import w from 'maplibre-gl/dist/x.mjs?worker&url';")).toBe(false);
    expect(DIALECT_IMPORT.test(" * import w from 'maplibre-gl/dist/x.mjs?worker&url';")).toBe(false);
    expect(DIALECT_IMPORT.test("// it used to carry the `?worker&url` import")).toBe(false);
    // ...and an ordinary import must not fire it.
    expect(DIALECT_IMPORT.test("import { a } from '@devicechain/widgets/vite';")).toBe(false);
  });

  it('the fence editor shares that one runtime instead of importing a worker entry again', () => {
    const fence = FILES.find(([path]) => path === './routes/geofences/FenceMap.tsx');
    expect(fence, 'the fence editor has moved or gone').toBeTruthy();

    const [, source] = fence as [string, string];
    expect(source).toMatch(/MAP_RUNTIME/);
    expect(source).toMatch(/setWorkerUrl\(/);
  });
});
