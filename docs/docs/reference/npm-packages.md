---
sidebar_position: 3
title: npm Packages
---

# npm Packages

The dashboard runtime is published to npm, so a React application outside this
repository can install it and render live DeviceChain dashboards.

:::note Status
These packages are **pre-1.0**. Types, props and exports can change between releases;
read the release notes before upgrading. They are ESM-only and built for a bundler —
see [What these packages assume](#what-these-packages-assume).
:::

## What is published

| Package | What it is |
|---|---|
| [`@devicechain/client`](https://www.npmjs.com/package/@devicechain/client) | The TypeScript SDK — the auth token seam, GraphQL over fetch, JWT decode, and live subscriptions. Framework-agnostic; no React. |
| [`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards) | The `DashboardHub` — owns the connection, resolves selectors, multiplexes telemetry subscriptions — plus the definition, selector, slot and binding-manifest types. |
| [`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) | The React widgets and the renderer that lays a board out. |
| [`@devicechain/brand`](https://www.npmjs.com/package/@devicechain/brand) | The brand tokens and the generated stylesheets. Optional — the widgets are themed entirely with CSS custom properties and require none of it. |

```bash
npm install @devicechain/widgets @devicechain/dashboards @devicechain/client \
            graphql react react-dom
```

`@devicechain/dashboards` and `@devicechain/client` are **peer dependencies** of the
widgets, pinned to the exact same version. npm 7 and later install peers for you, so
that install line is the whole of it — but the pin is deliberate and worth knowing
about, because it is what stops npm from quietly nesting a second copy of the SDK
under the widgets. Two copies would be worse than an error: the runtime distinguishes
"you are not allowed to see this" from "the connection dropped" by the identity of the
error class it catches, and an error thrown by one copy is not an instance of the other
one's class. A permission refusal would then read as an outage.

## Versions and dist-tags

All four packages are published **together, at one version**, from the release that
cuts the platform itself. Version `0.14.0` of the widgets goes with version `0.14.0`
of the SDK; there is no independent package versioning to reason about.

| Tag | Points at |
|---|---|
| `latest` | The most recent **stable** release. This is what a bare `npm install` resolves. |
| `next` | The most recent **prerelease** (`-rc.1` and friends). Opt in with `npm install @devicechain/widgets@next`. |

Every version published by the release workflow carries [npm
provenance](https://docs.npmjs.com/generating-provenance-statements) — a signed,
publicly verifiable statement of which repository, workflow, and commit built it, which
npmjs.com surfaces on the package page. Every version from `0.14.0` onward carries it.
The `0.14.0-0` bootstrap versions are the exception: they were published by hand to
create the packages in the first place, and predate the workflow that signs them.

## Render a board

```tsx
import { DashboardRenderer } from '@devicechain/widgets';
import { DashboardHub, parseDashboardDefinition } from '@devicechain/dashboards';

const hub = new DashboardHub({ resolver, authorities: user.scopes });
const definition = parseDashboardDefinition(json);

<DashboardRenderer definition={definition} hub={hub} actions={hub} />;
```

Omit `actions` for a strictly read-only mount: the acting widgets — alarm acknowledge,
command send — then render without their controls. That is the whole opt-in, and it is
belt to the server's braces. A viewer who never receives `actions` cannot write from
the board no matter what it contains, and the server enforces `alarm:write` and
`command:write` regardless.

See [Dashboards](../concepts/dashboards.md) for what a definition is, how slots and
binding manifests let one definition serve many devices, and how the standalone `/dash`
viewer uses them.

## Rendering a map: one required piece of host wiring {#map-host-wiring}

The map widget needs one thing from you, and skipping it fails quietly.

MapLibre parses vector tiles in a web worker, and it derives that worker's URL at
**runtime**, from its own module URL — a computed string no bundler can trace. So no
bundler emits the file, the browser 404s it, `new Worker()` does not throw, and the map
renders as an empty box with nothing in the console. Only your bundler can emit that
file, which is why the URL has to come from you.

**On Vite that is one import:**

```tsx
import { MapRuntimeProvider } from '@devicechain/widgets';
import { viteMapRuntime } from '@devicechain/widgets/vite';

<MapRuntimeProvider runtime={viteMapRuntime}>
  <DashboardRenderer definition={definition} hub={hub} />
</MapRuntimeProvider>;
```

On any other bundler you supply the URL yourself. There is one requirement and it is
the whole of the difficulty: MapLibre loads that URL as a **module worker**, so
whatever you serve there must be a module with no unresolved imports left in it.
`maplibre-gl/dist/maplibre-gl-worker.mjs` is not one on its own — its first line
imports a sibling, `maplibre-gl-shared.mjs`.

:::danger Do not point at a lone copy of the worker file
In particular, do not reach for `new URL('maplibre-gl/dist/maplibre-gl-worker.mjs',
import.meta.url)`. It looks right and it is not: webpack copies that one file as an
asset, the sibling is never emitted, and the worker dies on its own first line — with a
200 on the worker URL, a canvas on screen, markers placed, the build exited 0, and
nothing in the console. The map simply has no map in it.
:::

Two recipes that work. On **webpack**, give the worker its own entry, so webpack bundles
its imports into it:

```js
entry: {
  main: './src/index.tsx',
  'maplibre-worker': 'maplibre-gl/dist/maplibre-gl-worker.mjs',
},
output: {
  filename: (data) =>
    data.chunk.name === 'maplibre-worker' ? 'maplibre-worker.js' : '[name].[contenthash].js',
},
```

…then point at the filename you chose:

```tsx
const runtime = { workerUrl: '/maplibre-worker.js' };
```

Or, on **any bundler at all**, copy the two files yourself into one served directory:

```tsx
//   maplibre-gl/dist/maplibre-gl-worker.mjs  ->  public/vendor/
//   maplibre-gl/dist/maplibre-gl-shared.mjs  ->  public/vendor/
const runtime = {
  workerUrl: '/vendor/maplibre-gl-worker.mjs',
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
```

`loadStyles` is optional — import MapLibre's stylesheet eagerly yourself if you would
rather. Supplying it here keeps the 83 KB (10.7 KB gzipped) on the map's lazy chunk, so
a viewer who never opens a map downloads none of it.

**A map widget with no provider above it renders a visible notice, not a blank canvas.**
That is deliberate: an unwired host should be able to see what is missing rather than
having to guess.

### `maplibre-gl` is a peer dependency

Declare it in your own application, alongside the widgets:

```bash
npm install maplibre-gl
```

Two reasons, and the second is not obvious. The first is that the worker URL is yours
to emit, so the library has to be yours to own. The second is version skew: if your app
supplied the worker while the widgets resolved MapLibre from a copy of their own, a
worker built from one version would be driving a main thread on another — the same
blank map, by a different route. A peer makes one shared copy structural.

If your application does not declare it, npm's hoisting will still let the import
resolve, and it will keep working right up until someone installs with pnpm's strict
mode or a similar isolated layout, where a dependency nobody declared is a dependency
nobody gets.

## Basemap tiles

`TenantBasemapProvider` supplies the tile source, and it is genuinely optional — with
no provider the map falls back to a plain view. The resolution cascade lives in
`@devicechain/client`, so every DeviceChain surface resolves a per-user override over
the tenant default identically. See [Basemaps](../concepts/basemaps.md).

## Theming

Every colour, radius and font the widgets use comes from a CSS custom property, so a
host restyles them by setting variables on any ancestor element. There is no Tailwind,
no global stylesheet and no opinion about your application's shell. The DeviceChain
values ship as `@devicechain/brand`; nothing requires them.

## What these packages assume

| | |
|---|---|
| **Module format** | ESM only. There is no CommonJS build. |
| **React** | 19. |
| **Bundler** | Vite, webpack, Rollup or esbuild. |
| **TypeScript resolution** | The emitted declarations use extensionless specifiers, which resolve under `moduleResolution: "bundler"` and **not** under `node16`/`nodenext`. |

Those are the supported combinations, and the line between "supported" and "probably
fine" is drawn on purpose. They are exercised against a real browser in three
applications built outside this repository from the packed tarballs — one on Vite, one
on webpack, and one on the copy-the-worker recipe above — each of which renders the map
and is checked on the tiles it actually fetched and the markers it actually placed,
because "it rendered" is exactly the assertion this widget's failure mode passes.

Nothing there exercises Next.js or React Server Components, CommonJS consumers, or
`node16`/`nodenext` resolution. Those are untested rather than known-broken, and this
page will say so when that changes.

## Building against the workspace instead

Everything above describes installing from the registry. The packages are also the
source of truth inside this repository: the console and the `/dash` viewer both build
against the same `dist` a consumer downloads, rather than against the TypeScript
sources, so a break in the published artifact breaks the applications too. If you are
working on DeviceChain itself, see the [local development
guide](../guides/local-development.md).
