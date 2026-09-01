// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Type declarations for the two Vite-dialect specifiers used by `./vite.ts`, the package's
// single Vite-only entry point.
//
// 🔴 EXACT MODULE NAMES, NEVER WILDCARDS, AND THAT IS THE WHOLE POINT OF THIS FILE.
// The obvious way to write this is `declare module '*.css'` and `declare module
// '*?worker&url'` — which is what Vite's own `vite/client` provides, and what this package
// used to carry. Both are WRONG here, for a reason that has nothing to do with taste:
//
// An ambient wildcard is global to every program that includes it. A `*.css` declaration
// sitting in this package would make a stylesheet import reintroduced ANYWHERE in it
// typecheck cleanly — silently restoring the exact defect the package was just cured of,
// and hiding it from the one gate a developer runs most often. Declaring the two exact
// specifiers `./vite.ts` actually imports enables those two and nothing else: any other
// dialect import in this package is still a type error, as it should be.
//
// (The artifact-level gate in bundler-dialect.test.ts would catch it regardless — it builds
// the portable entry and the Vite entry separately and holds each to its own budget. But a
// gate and a type error catch it at different moments, and the type error is the one that
// arrives while you are still writing the line.)

declare module 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url' {
  /** The emitted, hashed URL of MapLibre's worker entry, bundled by Vite. */
  const url: string;
  export default url;
}

declare module 'maplibre-gl/dist/maplibre-gl.css' {
  const url: string;
  export default url;
}
