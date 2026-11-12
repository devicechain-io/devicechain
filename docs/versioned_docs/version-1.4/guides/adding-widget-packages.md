---
sidebar_position: 10
title: Adding Widget Packages
---

# Adding Widget Packages to the Console and /dash

A widget package is bundled into the application that shows it. Adding one is a build-time change:
you add the dependency, include its definitions in the application's registry, and rebuild. There is
no runtime installer and no upload. An exported dashboard is portable JSON and does not carry a
widget's implementation.

A package registered in one application is not installed in any other. The authoring console and
every application that views a dashboard using the widget must include the same package, or the
widget appears there as a placeholder. See
[widget versions and compatibility](./widget-versions-and-compatibility.md).

## In your own application

If you embed dashboards with `@devicechain/dashboards` and `@devicechain/widgets`, build one
registry from the built-in definitions plus your own, and give the same registry to the parser and
the renderer:

```tsx
import { parseDashboardDefinition } from '@devicechain/dashboards';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { builtinWidgetDefinitions, DashboardRenderer } from '@devicechain/widgets';
import { tankLevelWidgets } from '@acme/devicechain-tank-level';

const registry = createWidgetRegistry([...builtinWidgetDefinitions, ...tankLevelWidgets]);
const definition = parseDashboardDefinition(rawDefinition, { catalog: registry.catalog });

return <DashboardRenderer definition={definition} hub={hub} registry={registry} />;
```

Parsing a definition with a catalog that lacks your widgets, and then rendering with a registry that
has them, does not work: the parser has already turned the widget into a placeholder. Pass the one
registry to both.

`createWidgetRegistry` validates and seals the registry. It rejects duplicate ids (including an
attempt to reuse a built-in id), malformed descriptors, an unsupported `sdkApiVersion` and invalid
defaults or bindings, and it reports all of them in one error. There is no global registration, and
two registries on one page are independent. A registry holds one exact version of each id.

## In the DeviceChain console and /dash

The console, the `/dash` viewer and the board-replay player build their registry from one shared
composition module in the frontend source tree, `frontend/apps/host-widgets`. None of them keeps a
list of its own, so a package added there reaches every one of them at once: the definition parser,
the option validators, the renderer, the editor's Add menu and settings panel, the synthetic preview
and the recording player.

To install a widget package:

1. Add the package to the dependencies in `frontend/apps/host-widgets/package.json`, then run
   `npm install` in `frontend/`.
2. In `frontend/apps/host-widgets/src/extensions.ts`, import the package's definitions and list them
   in `extensionWidgets`:

   ```ts
   import type { ErasedWidgetDefinition } from '@devicechain/widget-sdk';
   import { tankLevelWidgets } from '@acme/devicechain-tank-level';

   export const extensionWidgets: readonly ErasedWidgetDefinition[] = [...tankLevelWidgets];
   ```

3. Rebuild. `npm run build` in `frontend/` builds the console, `/dash` and the board-replay player.
   The console and `/dash` ship together in the frontend container image built from
   `frontend/Dockerfile`; build that image and deploy it in place of the stock one (the Helm chart's
   `frontend.image` values). If you serve the board-replay player, deploy its rebuilt bundle too.

Deploy every application together. A dashboard authored in the console with your widget shows a
placeholder in a `/dash` or replay build that does not include the package.

If the composition is invalid, for example two packages claim the same id or a descriptor is
malformed, the registry throws one error listing every fault the first time the application builds it (when a
dashboard is first opened or edited), so
a broken package shows up in the build you test rather than one widget at a time in production.

This list is trusted code compiled into the application. It is not a runtime installer, and a
dashboard cannot add to it.

## Publishing while a widget is unresolved {#publishing-while-a-widget-is-unresolved}

A widget whose package is missing, or whose stored settings version the installed package cannot
read, parses to a placeholder. In the console you can still open the dashboard, move and resize the
placeholder and save the draft; its stored JSON is kept exactly. Publishing a new version is blocked
while any widget is unresolved, and the version history panel names each one and the reason. Install
the package, or remove the widget, and then publish.

The block is in the console. The server stores dashboard definitions as opaque JSON and does not
make this check.

## Check a package before you ship it

Build it against packed artifacts, the way a consumer receives the SDK, not against the monorepo. The
repository's `hack/widget-package-proof.sh all` does this for the example package: it packs the SDK,
installs the tarballs into a copy outside the repository, builds it, and checks that the package has
no private or monorepo imports, that it resolves exactly one copy of the SDK, and that its DOM-only
entry bundles no ECharts, MapLibre or built-in widget code. It then plants a chart import in the
DOM-only entry and requires the check to fail, so a check that cannot see is caught. CI runs it on
every change; it needs Node, npm, an installed frontend (`npm ci` in `frontend/`) and access to the
npm registry. On Windows, run it with Git Bash. Use the same checks on your own package.
