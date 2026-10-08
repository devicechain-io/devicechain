<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# @devicechain/dashboards

The DeviceChain dashboard runtime: the **DashboardHub**, which multiplexes live
telemetry, alarms, commands and locations over the client wire, and the typed
**dashboard-definition** contract that describes a board.

No React, no rendering. This is the layer under a widget set — the DeviceChain
console and the standalone `/dash` viewer both sit on it, and so can yours.
[`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) is
the React widget set built on this package.

```bash
npm install @devicechain/dashboards @devicechain/client graphql
```

`@devicechain/client` and `graphql` are peer dependencies. Peers rather than
ordinary dependencies on purpose: the hub classifies stream errors with
`instanceof` against the SDK's error type, and `instanceof` is false across two
copies of the same package — a duplicate would turn "you may not view this
device" into what reads like an outage.

## The hub

One hub serves a whole board. Widgets subscribe to it; it does the deduplication.

```ts
import { DashboardHub, resolveWidgetDatasource } from '@devicechain/dashboards';

const hub = new DashboardHub({
  resolver,                 // how a selector becomes device tokens
  authorities: user.scopes, // what this viewer may do; drives hub.can(...)
});

// The hub holds no slot bindings: resolve a widget's `slot` selector through them
// first. A slot with no binding resolves to an explicit `unbound` selector, which the
// hub answers with no data; a raw `slot` selector is an error.
// It is undefined only for a widget that declares no datasource at all.
const datasource = resolveWidgetDatasource(widget.datasource, bindings);
const stop = datasource && hub.subscribeWidget(datasource, {
  next: (sample) => setLatest(sample),
});

// later
stop?.();
hub.disposeAll();
```

Ten widgets pointed at the same device open **one** upstream subscription, not
ten. Each widget declares the measurement names it cares about (none means all),
and the hub fans a single stream out to the subscribers that asked for each name.

Four channels, each with its own `subscribe*` method and disposer:

| channel | what it carries |
| --- | --- |
| `subscribeWidget` | measurement samples for a datasource selector |
| `subscribeAlarms` | live alarm rows, plus `acknowledgeAlarm` / `clearAlarm` |
| `subscribeCommands` | command rows and their delivery status, plus dispatch |
| `subscribeLocations` | position series for map widgets |

`disposeAll()` tears down every open channel — a host calls it when the board
closes, which is what keeps a poll and a socket from outliving the page.

## The definition contract

A dashboard is JSON. The server stores it opaquely; this package is what gives it
a shape.

```ts
import { parseDashboardDefinition, serializeDefinition } from '@devicechain/dashboards';

const board = parseDashboardDefinition(json); // throws DashboardDefinitionError
```

Parsing is validating: a malformed board fails here, in your code, rather than
half-rendering later. `isDirty` compares a working copy against its saved
original, `resolveWidgetBox` and `activeBreakpoint` resolve responsive layout,
and the `editor-model` transforms (`addWidget`, `setWidgetBox`, `deleteWidget`,
`bringToFront`, …) are pure functions an authoring UI can wire to drag and resize.

## Slots

A board can be written once and pointed at many entities. A widget's datasource
may name a **slot** instead of a device, and the host supplies a binding for that
slot at view time — `effectiveBindings`, `resolveContextBindings`,
`applySelection` and `resolveSlotCandidates` are the machinery.

The hub holds no bindings. A slot is resolved to a concrete selector before it
is subscribed — `resolveWidgetDatasource(datasource, bindings)`, which the
`DashboardRenderer` in `@devicechain/widgets` calls for every widget — so one
hub serves a board for its whole life, and re-pointing a slot re-subscribes only
the widgets bound to it. A slot with no binding resolves to `{ kind: 'unbound' }`,
which every channel treats as zero devices; it is never turned into an absent
datasource, which the alarm channel reads as tenant-wide. A raw `slot` selector
handed straight to the hub is an error.

## Synthetic data

`SyntheticDataSource` implements the same interface as the hub and generates
plausible measurements, so a board can be previewed, screenshotted or
demonstrated with no instance behind it.

## Command status

`isTerminalCommandStatus` and `isCancellableCommandStatus` are the two questions
worth asking about a command, and they are different questions — a `SENT` command
is still in flight but is no longer cancellable. The underlying sets are
deliberately not exported: every surface asks the predicate, so a new status
cannot be learned by some call sites and not others.

## Compatibility

ESM only, built for bundler resolution (Vite, webpack, Rollup, esbuild). The
emitted declarations use extensionless specifiers, which resolve under
TypeScript's `bundler` module resolution and **not** under `node16`/`nodenext`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
