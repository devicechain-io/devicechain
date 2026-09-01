// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The /dash viewer's MapLibre runtime, in Vite's dialect.
//
// 🔴 THIS APP IS THE REFERENCE EXTERNAL EMBEDDER, so these few lines matter more here than
// in the console. /dash exists to be "just another embedder" of the published packages —
// what it has to do to render a map widget is exactly what a third-party app will have to
// do, and until now it did nothing at all, because `@devicechain/widgets` was reaching into
// Vite's dialect on its host's behalf. That worked here and would have rendered a silent
// blank map in any consumer not built with Vite, with no build anywhere reporting it.
//
// So the wiring below IS the contract, and it is deliberately not hidden behind a helper
// exported from the package: a helper could only be written in one bundler's dialect, which
// is the defect. A webpack or Rollup embedder writes these same two values using its own
// worker-entry idiom. See packages/widgets/src/map-runtime-context.tsx.

/// <reference path="./css-modules.d.ts" />

import type { MapRuntime } from '@devicechain/widgets';

// Vite emits the worker as its own entry (bundling the shared module it imports, which the
// plain `?url` form would leave dangling) and hands back the hashed URL. `maplibre-gl` is a
// PEER dependency of @devicechain/widgets, so this app declares it directly — and that is
// what guarantees the worker and the renderer come from one copy of the library.
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';

/**
 * Installed once, where App mounts the DashboardRenderer.
 *
 * A module constant rather than an inline literal, and `loadStyles` a function rather than
 * a module-scope import, so the 70 KB stylesheet (10 KB gzipped) stays on the renderer's
 * lazy chunk instead of in this app's main stylesheet.
 */
export const MAP_RUNTIME: MapRuntime = {
  workerUrl,
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
