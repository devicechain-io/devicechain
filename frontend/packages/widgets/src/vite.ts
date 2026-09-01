// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// `@devicechain/widgets/vite` — the ready-made map runtime for hosts built with Vite.
//
// 🔴 THIS IS THE ONE FILE IN THIS PACKAGE ALLOWED TO WRITE A BUNDLER'S DIALECT, and the
// exception is deliberate, bounded, and enforced rather than promised. The main entry
// (`@devicechain/widgets`) contains no bundler-specific specifier at all — that is what
// lets a webpack, Next or Rollup consumer build it — and the dialect gate proves it by
// building the two entries SEPARATELY: zero dialect specifiers reachable from `index.ts`,
// exactly one from here. So the carve-out cannot quietly widen, and it cannot leak into
// the portable entry, because a consumer only ever gets this module by importing it BY
// NAME.
//
// 🔴 WHY THE EXCEPTION IS WORTH IT. Without it every Vite consumer — which today means
// every likely consumer, including this repo's own console and /dash — must learn that
// MapLibre needs a worker URL, learn their bundler's idiom for emitting one, and write it
// correctly, to render a map. That is a lot of ceremony to hand someone who asked for a map
// widget, and getting it wrong has a famously quiet failure mode. Simplicity for the
// consumer is worth one carefully fenced file.
//
// VERIFIED, not assumed: a package inside `node_modules` writing `?worker&url` is resolved
// correctly by a CONSUMER's Vite — tested outside this monorepo against Vite 6.4.3 and
// 8.2.2, with the worker emitted as its own entry and its `maplibre-gl-shared.mjs` sibling
// INLINED into it. That last part is why `?worker&url` and not plain `?url`: the plain form
// copies one file verbatim and leaves the sibling dangling.

import type { MapRuntime } from './map-runtime-context';

// `?worker&url` makes Vite treat the module as a worker ENTRY — bundling what it imports
// along with it — and hand back the emitted, hashed URL.
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';

/**
 * A complete `MapRuntime` for a Vite host.
 *
 * ```tsx
 * import { MapRuntimeProvider } from '@devicechain/widgets';
 * import { viteMapRuntime } from '@devicechain/widgets/vite';
 *
 * <MapRuntimeProvider runtime={viteMapRuntime}>{children}</MapRuntimeProvider>
 * ```
 *
 * A module constant, so it is referentially stable for the life of the page.
 *
 * `loadStyles` keeps MapLibre's 83 KB stylesheet (10.7 KB gzipped) on the renderer's lazy
 * chunk, so a viewer who opens no map downloads none of it. Import the stylesheet yourself
 * instead if you would rather have it eagerly — the field is optional and this is only the
 * default that costs a first-time user nothing to accept.
 */
export const viteMapRuntime: MapRuntime = {
  workerUrl,
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
