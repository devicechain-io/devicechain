// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// The map runtime: the pieces of MapLibre that only the HOST'S BUNDLER can produce.
//
// 🔴 WHY THIS EXISTS AT ALL. MapLibre parses tiles and geometry off the main thread, and
// left to itself it derives the worker's URL at runtime from its own module URL —
// `new URL('./maplibre-gl-worker.mjs', import.meta.url)`. That is a string no bundler can
// follow, so nothing emits the file, the browser resolves it to a 404, and `new Worker()`
// does not throw on one: the failure arrives later as an async error event. The map builds,
// type-checks, passes every test, and renders nothing.
//
// The only thing that can emit that file is the application's own bundler, using its own
// dialect — Vite spells it `?worker&url`, webpack and Rollup each spell it differently.
// So the URL is something a HOST computes and a LIBRARY cannot.
//
// 🔴 THIS PACKAGE USED TO WRITE VITE'S DIALECT ITSELF, and that is the defect this seam
// removes. A published library containing `import('…?worker&url')` works under a consumer's
// Vite and silently renders a blank map under anything else — and because the specifier is
// externalized, no bundler ever tries to resolve it, so NOTHING IN ANY BUILD REPORTS IT.
// A green pipeline over a broken artifact is strictly worse than a red one.
//
// ⚠️ HOW THIS DIFFERS FROM TenantBasemapProvider, which sits beside it and looks alike.
// The basemap provider is OPTIONAL BY DESIGN: a host that installs none behaves exactly as
// it did before the tenant setting existed, and that is a coherent state. This one is
// REQUIRED, and a host that omits it gets a loud notice rather than a map. The asymmetry is
// deliberate and is the whole point: a missing basemap is a configuration a viewer can read
// off the screen, while a missing worker URL is invisible — it is the blank map above.
// Copying the basemap's optional-by-default shape here would relocate that defect into
// every unwired host instead of removing it.

import { createContext, useContext, type ReactNode } from 'react';

/**
 * The bundler-produced runtime a map widget needs from its host.
 *
 * `workerUrl` is the emitted, host-resolvable URL of MapLibre's worker entry
 * (`maplibre-gl/dist/maplibre-gl-worker.mjs`). It is handed to MapLibre's `setWorkerUrl`
 * before the first `Map` is constructed.
 *
 * `loadStyles` is OPTIONAL, and imports MapLibre's stylesheet
 * (`maplibre-gl/dist/maplibre-gl.css`). Omit it and import the stylesheet yourself, the way
 * every map library asks you to — that is the ordinary path and nothing here second-guesses
 * it. Supplying it buys one thing: the CSS rides the renderer's lazy chunk instead of your
 * main stylesheet, which is worth **83 KB raw / 10.7 KB gzipped** to a viewer who never
 * opens a board with a map on it.
 *
 * 🔴 The size is measured against maplibre-gl 6.6.0, the version this package peers on. An
 * earlier draft said 70 KB "measured, not estimated" — measured, but against a stale
 * `node_modules` carrying 6.2.0. Measuring the right thing in the wrong tree reads exactly
 * like measuring, which is why the version is named here.
 *
 * 🔴 It is a FUNCTION rather than a boolean or a URL because the host's bundler owns its
 * CSS pipeline: only the host can say "load this, lazily, your way".
 */
export type MapRuntime = {
  workerUrl: string;
  loadStyles?: () => Promise<unknown>;
};

const MapRuntimeContext = createContext<MapRuntime | null>(null);

/**
 * Supplies the host's MapLibre runtime to every map widget beneath it.
 *
 * Install it ONCE, high in the tree, next to TenantBasemapProvider — the console from its
 * TenantProvider, the /dash viewer from the one place it mounts a DashboardRenderer.
 *
 * 🔑 ON VITE, DO NOT WRITE ANY OF THIS — import the ready-made runtime:
 *
 * ```tsx
 * import { viteMapRuntime } from '@devicechain/widgets/vite';
 *
 * <MapRuntimeProvider runtime={viteMapRuntime}>{children}</MapRuntimeProvider>
 * ```
 *
 * On any other bundler you supply the URL yourself, and there is ONE REQUIREMENT that
 * decides whether it works: MapLibre loads the URL as a MODULE worker, so whatever it
 * serves must be a module with no unresolved imports left in it.
 * `maplibre-gl/dist/maplibre-gl-worker.mjs` does not satisfy that on its own — its first
 * line is `import … from './maplibre-gl-shared.mjs'`.
 *
 * 🔴 SO DO NOT POINT AT A VERBATIM COPY OF THE WORKER FILE, and in particular do not
 * reach for `new URL('maplibre-gl/dist/maplibre-gl-worker.mjs', import.meta.url)`. That
 * looks exactly right and is wrong: webpack treats the target as an ASSET and copies it,
 * the sibling is never emitted, and the worker dies on its own first line. Measured
 * against webpack 5, from a packed tarball: the worker URL answered 200, both maps drew
 * canvases, all markers appeared, tiles loaded, THE BUILD EXITED 0 AND THE CONSOLE WAS
 * EMPTY — and the bundled basemap rendered ocean with no land on it.
 *
 * Two recipes that were measured working, in `hack/consumer-proof`:
 *
 * ```js
 * // webpack — make the worker a second ENTRY, so its imports are bundled into it.
 * entry: { main: './src/index.tsx', 'maplibre-worker': 'maplibre-gl/dist/maplibre-gl-worker.mjs' }
 * // …then: <MapRuntimeProvider runtime={{ workerUrl: '/maplibre-worker.js' }}>
 * ```
 *
 * ```js
 * // any bundler, or none — copy the worker AND ITS SIBLING, under their own names,
 * // into one served directory: maplibre-gl-worker.mjs + maplibre-gl-shared.mjs
 * // …then: <MapRuntimeProvider runtime={{ workerUrl: '/vendor/maplibre-gl-worker.mjs' }}>
 * ```
 *
 * 🔴 Hold the runtime in a MODULE CONSTANT, as above, rather than building it inline in
 * JSX. An object literal in the element is a new identity on every render, and while the
 * widget deliberately keys its effects on `workerUrl` rather than on the object to survive
 * that, a host that also passes it anywhere keyed on identity would tear down and rebuild
 * the map on every parent render. There is nothing per-render in it to justify the risk.
 *
 * 🔴 `maplibre-gl` is a PEER dependency of this package precisely so that the copy the host
 * takes the worker from is the same copy this package renders with. A worker built from one
 * version driving a main thread from another is the blank map again, by a different route.
 */
export function MapRuntimeProvider({
  runtime,
  children,
}: {
  runtime: MapRuntime;
  children: ReactNode;
}) {
  return <MapRuntimeContext.Provider value={runtime}>{children}</MapRuntimeContext.Provider>;
}

/**
 * The host's map runtime, or null when no host supplied one.
 *
 * 🔴 Callers MUST treat null as a refusal to render a map, not as a default to paper over.
 * There is no sensible fallback: the only alternative to a host-supplied URL is MapLibre's
 * own runtime derivation, which is the broken behaviour this seam exists to prevent.
 */
export function useMapRuntime(): MapRuntime | null {
  return useContext(MapRuntimeContext);
}
