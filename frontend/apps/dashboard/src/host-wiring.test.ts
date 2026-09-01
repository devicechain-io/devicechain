// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// 🔴 THIS APP IS THE REFERENCE EXTERNAL EMBEDDER, and these are the obligations that come
// with rendering `@devicechain/widgets` from outside the package.
//
// The map widget takes its MapLibre worker URL from the host, because emitting a worker
// entry is a bundler-specific act and a published library that writes one bundler's dialect
// renders a silent blank map under every other. That moves a burden onto every host — this
// one included — and a burden nobody checks is one that quietly stops being carried.
//
// What makes it worth a test rather than trust: removing the provider does NOT break a
// build, does NOT fail a typecheck, and does NOT fail any behavioural test in this app,
// because nothing here renders a map widget under jsdom. It changes one thing only — every
// map on every published dashboard becomes a "Map runtime not configured" notice — and the
// first person to find out is a viewer.
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

    // 🔴 Named, not counted. An absence claim over an empty list passes when the glob
    // rots; naming the file means a move reports a failure rather than a clean sweep of
    // nothing. It is also the honest statement of the invariant: ONE install site, high in
    // the tree, so there is no render site to forget.
    expect(installers.map(([path]) => path)).toEqual(['./App.tsx']);

    const [, app] = installers[0];
    // It must wrap the renderer, not sit somewhere inert beside it.
    expect(app).toMatch(/<MapRuntimeProvider[^>]*>[\s\S]*<DashboardRenderer/);
  });

  it('builds that runtime from a worker entry its own bundler emits', () => {
    const runtime = FILES.find(([path]) => path === './map-runtime.ts');
    expect(runtime, 'the module that supplies the map runtime has moved or gone').toBeTruthy();

    const [, source] = runtime as [string, string];
    // Vite's worker-entry dialect. 🔴 Writing it HERE is correct and writing it in the
    // package is not: this is an application and knows its own bundler.
    expect(source).toMatch(/maplibre-gl\/dist\/maplibre-gl-worker\.mjs\?worker&url/);
    // And the stylesheet stays behind a FUNCTION, so the 10 KB (gzipped) of MapLibre CSS
    // rides the renderer's lazy chunk instead of this app's main stylesheet.
    expect(source).toMatch(/loadStyles:\s*\(\)\s*=>\s*import\(/);
  });

  // 🔴 The reach control. Both assertions above are claims about a file list, and a list
  // built by a rotted glob is empty and agrees with everything.
  it('the scan actually reaches this app’s sources', () => {
    expect(FILES.length).toBeGreaterThan(3);
    expect(FILES.map(([path]) => path)).toContain('./App.tsx');
    expect(FILES.map(([path]) => path)).toContain('./map-runtime.ts');
  });
});
