// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Module specifiers this repo's bundlers understand and TypeScript does not model on
// its own: a stylesheet imported for its SIDE EFFECT, and Vite's `?worker&url` suffix.
//
// The map widget pulls MapLibre's stylesheet in through the same dynamic import as the
// library itself, so the CSS lands in the lazy chunk rather than in the main bundle's
// stylesheet. That import has to typecheck in more than one program — this package's
// own `tsc --noEmit` and the console's, which compiles these sources through the
// workspace link — so the declaration is REFERENCED from the importing file with a
// triple-slash path rather than merely sitting in this package's `include`. An ambient
// file only reaches a program that has been told about it, and the console's program
// includes map.tsx without including this.
declare module '*.css' {
  const url: string;
  export default url;
}

// Vite's worker-entry suffix: it BUNDLES the named module as a web-worker entry
// (following its own imports, unlike the plain `?url` form, which copies one file
// verbatim) and yields the emitted, hashed URL. The map widget and the fence editor
// both hand that URL to MapLibre's `setWorkerUrl`, because MapLibre otherwise derives
// the worker's location at runtime from its own module URL — a string no bundler can
// see, so the file is never emitted and the map quietly renders nothing.
//
// Declared alongside the stylesheet, and referenced the same way: an ambient file only
// reaches a program that has been told about it, and the console compiles map.tsx and
// FenceMap.tsx without including this package's own `include`.
declare module '*?worker&url' {
  const url: string;
  export default url;
}
