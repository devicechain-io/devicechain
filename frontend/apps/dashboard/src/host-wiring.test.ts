// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THIS APP IS THE REFERENCE EXTERNAL EMBEDDER, and these are the obligations that come
// with rendering `@devicechain/widgets` from outside the package.
//
// The map widget takes its MapLibre worker URL from the host, because emitting a worker
// entry is a bundler-specific act and a published library that writes one bundler's dialect
// renders a silent blank map under every other. On Vite that costs the host one import from
// `@devicechain/widgets/vite`; on another bundler it costs a few lines. Either way it is a
// burden the host carries, and a burden nobody checks is one that quietly stops being
// carried.
//
// Why a test rather than trust: removing the provider breaks no build, fails no typecheck,
// and fails no behavioural test in this app, because nothing here renders a map widget under
// jsdom. It changes exactly one thing — every map on every published dashboard becomes a
// "Map runtime not configured" notice — and the first person to find out is a viewer.
//
// The sources are read through Vite's glob rather than node:fs, the idiom the console's
// bundle-boundaries test uses, because a glob resolves the way the BUNDLER resolves.

import { describe, expect, it } from 'vitest';

const SOURCES = import.meta.glob('./**/*.{ts,tsx}', {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>;

const FILES = Object.entries(SOURCES).filter(([path]) => !/\.test\.tsx?$/.test(path));

describe('the /dash viewer wires the map runtime it is obliged to supply', () => {
  it('installs MapRuntimeProvider around the renderer', () => {
    const installers = FILES.filter(([, source]) => /<MapRuntimeProvider\b/.test(source));

    // 🔴 Named, not counted. A rotted glob yields an empty list, and an empty list agrees
    // with a count. Naming the file also states the invariant: ONE install site, high in the
    // tree, so there is no render site to forget.
    expect(installers.map(([path]) => path)).toEqual(['./App.tsx']);

    const [, app] = installers[0];
    // It must WRAP the renderer, not sit somewhere inert beside it.
    expect(app).toMatch(/<MapRuntimeProvider[^>]*>[\s\S]*<DashboardRenderer/);
  });

  it('takes the runtime from the package rather than hand-rolling it', () => {
    const runtime = FILES.find(([path]) => path === './map-runtime.ts');
    expect(runtime, 'the module that supplies the map runtime has moved or gone').toBeTruthy();

    const [, source] = runtime as [string, string];
    expect(source).toMatch(/from '@devicechain\/widgets\/vite'/);
  });

  // 🔴 A REAL CONTROL, replacing a redundant one. An earlier version of this file ended with
  // a "the scan reached these files" test whose assertions were implied by the presence
  // claims above — any input that failed it failed those first, so it could never be the
  // thing that fired. This asserts something none of them do, and it is the invariant the
  // whole slice exists to protect on the HOST side:
  //
  // this app must not write a bundler dialect of its own. The package's `/vite` entry is the
  // single place `?worker&url` is written, deliberately, so that swapping bundlers is one
  // import and not a hunt through app code. A copy reintroduced here would work — and would
  // silently make this app the second authority on a bundler incantation, which is how the
  // two drift and one surface goes blank.
  //
  // 🔴 ANCHORED TO IMPORT SYNTAX rather than matching raw text. The console's twin of this
  // guard was first written as a raw-text scan and immediately flagged a COMMENT that
  // explained this very rule — the same comment-blindness that broke the package-side gate,
  // reproduced inside the guard meant to replace it. This file passes either way today,
  // which is exactly the condition under which an ungated rule rots, so it is anchored here
  // too rather than left to luck.
  //
  // Stated limit: a dialect specifier in a mid-line dynamic `import()` slips past. Accepted,
  // because this is an APP-side guard and the authoritative parser-based check on what SHIPS
  // lives in packages/widgets/src/bundler-dialect.test.ts.
  const DIALECT_IMPORT =
    /^\s*(?:import|export)\s[^;]*?['"][^'"]*\?(?:worker|url|raw|inline)\b[^'"]*['"]/m;

  it('writes no bundler dialect of its own', () => {
    const offenders = FILES.filter(([, source]) => DIALECT_IMPORT.test(source)).map(([path]) => path);
    expect(offenders).toEqual([]);
  });

  it('...and that pattern fires on a real one while ignoring prose about it', () => {
    expect(DIALECT_IMPORT.test("import w from 'maplibre-gl/dist/x.mjs?worker&url';")).toBe(true);
    expect(DIALECT_IMPORT.test("// import w from 'maplibre-gl/dist/x.mjs?worker&url';")).toBe(false);
    expect(DIALECT_IMPORT.test(" * import w from 'maplibre-gl/dist/x.mjs?worker&url';")).toBe(false);
    expect(DIALECT_IMPORT.test("import { a } from '@devicechain/widgets/vite';")).toBe(false);
  });
});
