// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A stylesheet imported for its SIDE EFFECT, which every bundler in this repo handles
// and TypeScript does not model on its own.
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
