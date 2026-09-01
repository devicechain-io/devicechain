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
 * `loadStyles` imports MapLibre's stylesheet (`maplibre-gl/dist/maplibre-gl.css`), which is
 * the host's for the same reason: a bare CSS import inside a published library is a second
 * bundler-dialect assumption.
 *
 * 🔴 IT IS A FUNCTION, NOT AN EAGER IMPORT AT THE HOST, AND THAT IS NOT STYLE. The
 * stylesheet is **70 KB raw / 10 KB gzipped** (measured, not estimated — an earlier draft
 * of this file called it "a few KB" and was wrong by an order of magnitude). A host that
 * imports it at module scope puts all of it in the main stylesheet for every viewer,
 * including the console user who never opens a board with a map — which is precisely the
 * lazy boundary the widget's own dynamic `import('maplibre-gl')` exists to hold. Passing a
 * loader keeps the CSS on the same lazy chunk it was on before this seam existed.
 *
 * 🔴 REQUIRED, not optional. An optional loader is one a host forgets, and the failure is
 * a map with unstyled controls and misplaced attribution — visible, annoying, and easy to
 * misread as a widget bug rather than missing wiring.
 */
export type MapRuntime = {
  workerUrl: string;
  loadStyles: () => Promise<unknown>;
};

const MapRuntimeContext = createContext<MapRuntime | null>(null);

/**
 * Supplies the host's MapLibre runtime to every map widget beneath it.
 *
 * Install it ONCE, high in the tree, next to TenantBasemapProvider — the console from its
 * TenantProvider, the /dash viewer from the one place it mounts a DashboardRenderer.
 *
 * A host wires it with its own bundler's worker-entry dialect. Under Vite:
 *
 * ```tsx
 * import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url';
 *
 * const MAP_RUNTIME = {
 *   workerUrl,
 *   loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
 * };
 *
 * <MapRuntimeProvider runtime={MAP_RUNTIME}>{children}</MapRuntimeProvider>
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
