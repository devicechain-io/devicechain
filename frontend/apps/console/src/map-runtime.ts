// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The console's MapLibre runtime, in Vite's dialect — the one thing a map widget cannot
// work out for itself.
//
// 🔴 WHY THIS LIVES IN THE APP. `@devicechain/widgets` is published to npm, and a library
// that writes one bundler's dialect works under that bundler and silently renders a blank
// map under every other. Worse, the specifier is externalized at build time, so NO BUILD
// ANYWHERE REPORTS IT — a green pipeline over a broken artifact. Emitting a worker entry
// is an application's job because only an application knows its own bundler. See
// packages/widgets/src/map-runtime-context.tsx for the full argument.
//
// The console is a Vite app, so it spells it `?worker&url`; the fence editor
// (routes/geofences/FenceMap.tsx) writes the same two lines for its own MapLibre instance,
// and that duplication is correct — both are this app declaring what this app's bundler
// emits, not a library guessing.

/// <reference path="./css-modules.d.ts" />

import type { MapRuntime } from '@devicechain/widgets';

// `?worker&url` makes Vite treat the module as a worker ENTRY — bundling the
// `./maplibre-gl-shared.mjs` it imports along with it, which is why the plain `?url` form
// (a verbatim copy with its sibling import dangling) is not enough — and hand back the
// emitted, hashed URL. Importing it costs a URL string in the main bundle, not the library.
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';

/**
 * Installed once, at the top of the tree, by TenantProvider.
 *
 * 🔴 A MODULE CONSTANT, not an object literal in JSX: a fresh object every render is an
 * identity change, and nothing downstream should have to defend against one.
 *
 * 🔴 `loadStyles` is a FUNCTION so the 70 KB stylesheet (10 KB gzipped) stays on the same
 * lazy chunk as the renderer. Importing it at module scope here would put it in the main
 * stylesheet for every console user, including the great majority who never open a board
 * with a map on it.
 */
export const MAP_RUNTIME: MapRuntime = {
  workerUrl,
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
