// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Module specifiers Vite understands and TypeScript does not model on its own: a
// stylesheet imported for its SIDE EFFECT, and Vite's `?worker&url` suffix.
//
// 🔴 THIS FILE BELONGS TO AN APP, DELIBERATELY, AND MUST NOT MOVE BACK INTO A PACKAGE.
// It previously lived in `@devicechain/widgets` and was referenced across the package
// boundary from here. That was wrong in two ways, and the second is the dangerous one:
//
//  1. Writing a bundler's dialect is an APPLICATION's job. A published library that
//     contains `?worker&url` works under a consumer's Vite and silently renders a blank
//     map under webpack or Rollup — and because the specifier is externalized, no build
//     anywhere reports it. `@devicechain/widgets` now takes its worker URL from the host
//     through MapRuntimeProvider, and writing that URL is what this app does below.
//
//  2. An ambient `declare module '*.css'` is GLOBAL to whatever program includes it. Had
//     it ridden along into the package's published `.d.ts`, it would have patched every
//     consumer's module resolution — and here in the tree it would MASK a stylesheet
//     import accidentally reintroduced into the package, hiding the exact regression the
//     dist-level dialect gate exists to catch. A declaration that suppresses a type error
//     must live only where that error is intended.
//
// It is REFERENCED from the importing file with a triple-slash path rather than merely
// sitting in this app's `include`: an ambient file only reaches a program that has been
// told about it, and the fence editor is compiled by more than one program.

declare module '*.css' {
  const url: string;
  export default url;
}

// Vite's worker-entry suffix: it BUNDLES the named module as a web-worker entry
// (following its own imports, unlike the plain `?url` form, which copies one file
// verbatim) and yields the emitted, hashed URL. The fence editor hands that URL to
// MapLibre's `setWorkerUrl`, because MapLibre otherwise derives the worker's location at
// runtime from its own module URL — a string no bundler can see, so the file is never
// emitted and the map quietly renders nothing.
declare module '*?worker&url' {
  const url: string;
  export default url;
}
