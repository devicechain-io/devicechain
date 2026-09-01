<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# @devicechain/widgets

React dashboard widgets for DeviceChain, plus the renderer that lays a board out.
Eleven widgets across five channels — charts, gauges, tables, alarm views, a
command button, an entity selector and a map — all bound to live data through
[`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards).

Themed entirely with CSS custom properties. No Tailwind, no global styles, no
opinions about your app's shell — these are meant to be embedded.

```bash
npm install @devicechain/widgets @devicechain/dashboards @devicechain/client graphql react react-dom
```

## Render a board

```tsx
import { DashboardRenderer } from '@devicechain/widgets';
import { DashboardHub, parseDashboardDefinition } from '@devicechain/dashboards';

const hub = new DashboardHub({ resolver, authorities: user.scopes });
const definition = parseDashboardDefinition(json);

<DashboardRenderer definition={definition} hub={hub} actions={hub} />;
```

Omit `actions` for a strictly read-only mount and the acting widgets — alarm
acknowledge, command send — render without their controls. That is the whole
opt-in: a viewer that never passes `actions` cannot write, regardless of what the
board contains.

Or drive a single widget yourself with `ConnectedWidget`, and look components up
by type through `WIDGET_REGISTRY` and its per-channel siblings.

## The widgets

| channel | widgets |
| --- | --- |
| measurement | `timeseries-chart`, `gauge`, `latest-card`, `table`, `label`, `image` |
| alarm | `alarm-table`, `alarm-count` |
| control | `command-button` |
| selection | `entity-selector` |
| location | `map` |

Charts and gauges are [Apache ECharts](https://echarts.apache.org); the map is
[MapLibre GL](https://maplibre.org), loaded lazily so a board with no map on it
downloads none of it.

`WIDGET_OPTIONS` is the typed schema for every widget's options bag —
`validateWidgetOptions` reports exactly where a stored bag disagrees with what the
renderer expects, which is what lets an authoring UI show a problem instead of
rendering something wrong.

## Rendering a map: one required piece of host wiring

MapLibre parses tiles in a web worker, and it derives that worker's URL at
**runtime** from its own module URL — a computed string no bundler can trace. So
no bundler emits the file, the browser 404s it, `new Worker()` does not throw, and
the map renders as an empty box with nothing in the console. It is a famously
quiet failure, and it is not something this package can paper over: only your
bundler can emit that file.

So the worker URL comes from you, through a provider. **On Vite that is one
import:**

```tsx
import { MapRuntimeProvider } from '@devicechain/widgets';
import { viteMapRuntime } from '@devicechain/widgets/vite';

<MapRuntimeProvider runtime={viteMapRuntime}>
  <DashboardRenderer definition={definition} hub={hub} />
</MapRuntimeProvider>;
```

On any other bundler, build the runtime yourself. All it needs is a URL your app
actually serves — reached through your bundler's asset-URL idiom, or by copying
`maplibre-gl/dist/maplibre-gl-worker.mjs` into your static output:

```tsx
const runtime = {
  workerUrl: '/assets/maplibre-gl-worker.mjs',
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
```

`loadStyles` is optional — import MapLibre's stylesheet yourself if you would
rather have it eagerly. Supplying it here keeps the 83 KB (10.7 KB gzipped) on the
map's lazy chunk, so a viewer who opens no map downloads none of it.

**A map widget with no provider above it renders a visible notice, not a blank
canvas.** That is deliberate: an unwired host should be able to see what is
missing.

`maplibre-gl` is a peer dependency, so your app and this package share exactly one
copy. A worker built from one version driving a main thread on another is the same
blank map by a different route.

## Basemap tiles

`TenantBasemapProvider` supplies the tile source. It is genuinely optional — with
no provider the map falls back to a plain view — and the resolution cascade lives
in `@devicechain/client` so every DeviceChain surface resolves an override over
the tenant default identically.

## Theming

Every colour, radius and font comes from a CSS custom property, so a host restyles
widgets by setting variables on any ancestor. The DeviceChain values ship as
[`@devicechain/brand`](https://www.npmjs.com/package/@devicechain/brand); nothing
here requires them.

## Compatibility

React 19, ESM only, built for bundler resolution (Vite, webpack, Rollup, esbuild).
The emitted declarations use extensionless specifiers, which resolve under
TypeScript's `bundler` module resolution and **not** under `node16`/`nodenext`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
