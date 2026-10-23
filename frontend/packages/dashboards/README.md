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

Each channel has its own method, and each subscription returns its disposer:

| method | what it carries |
| --- | --- |
| `subscribeWidget` | measurement samples for a datasource selector |
| `subscribeMembers` | a fleet widget's members, then each member's samples tagged with its device |
| `bucketMeasurements` | the time buckets (count, minimum, maximum, sum) a stat widget folds into one figure |
| `subscribeAlarms` | live alarm rows, plus `acknowledgeAlarm` / `clearAlarm` |
| `subscribeCommands` | command rows and their delivery status, plus `sendCommand` |
| `subscribeLocations` | position series for map widgets |

The widget layer depends on the `WidgetDataSource` interface, not on the hub
class, so a host can feed it from the hub, from `SyntheticDataSource` or from a
recorded run (`RecordedDataSource`). `subscribeMembers`, `bucketMeasurements` and
`readMemberSeeds` are optional on that interface: a source without them leaves
the widgets that need them saying so instead of showing a number.

`disposeAll()` tears down every open channel — a host calls it when the board
closes, which is what keeps a poll and a socket from outliving the page.

## The definition contract

A dashboard is JSON. The server stores it opaquely; this package is what gives it
a shape.

```ts
import { parseDashboardDefinition, serializeDefinition } from '@devicechain/dashboards';

const board = parseDashboardDefinition(json); // throws DashboardDefinitionError
```

A board saved by a newer release than this package is refused rather than read as
the current format: the `DashboardDefinitionError` carries `code: 'schemaTooNew'`
and the version it found in `found`. Test `code`, not `instanceof`, so the check
still holds when an application ends up with two copies of the package.

Parsing is validating: a malformed board fails here, in your code, rather than
half-rendering later. A board stored in an older format is converted as it is
parsed, and the result carries a `convertedFrom` mark until it is saved again
(`withoutConversionMark` drops it). `isDirty` compares a working copy against its
saved original.

### Layout

A board lays out on a grid of square cells. Each widget has a `WidgetLayout`: a
`Placement` per breakpoint, with `base` required. A placement is either a grid
footprint (`GridPlacement`, measured in cells) or a position relative to another
widget (`RelativePlacement`). The column count follows the container, so the same
board reads on a phone and on a wall display.

| need | exports |
| --- | --- |
| the placement a widget shows at a width | `activeBreakpoint`, `resolvePlacement` |
| what a renderer draws for a container width | `effectiveLayout`, `layoutShape`, `liveColumns` |
| pixel boxes for every widget | `layoutRects`, `gridRect`, `relativeRect`, `cellSizeAt` |
| grid lengths and the widget frame | `gridLengthPx`, `gutterPx`, `paddingPx`, `resolveFrame` |
| placement against what each widget can show | `lintLayout`, `blockingLayoutFindings` |

The `editor-model` transforms are pure functions an authoring UI can wire to drag
and resize: `addWidget`, `moveWidget`, `resizeWidget`, `setWidgetPlacement`,
`placeOnGrid`, `placeRelative`, `deleteWidget`, `setTitle`, `setCanvasGrid`,
`setCanvasDensity` and `updateWidget`, with `gridPlacementToPx` and
`pxToGridPlacement` to convert between cells and pixels. Moving or resizing a
grid widget pushes whatever it now covers out of its way. Grid widgets never
overlap, so there is no stacking order to manage.

### Time range and density

A board has a time range: `TIME_RANGE_PRESETS` (15 minutes to 7 days),
`initialTimeRange`, `pauseRange` / `resumeRange` and `rangeWindow` manage it, and
`timeRangeLinkFromSearch` reads the `range`, `paused` and `from` / `to` URL parameters
(`timeRangeFromSearch` reads only `range` and `paused`, for a host that knows no fixed window). A fixed
from-to window is an additive variant of the same state: `absoluteRange` builds it and
`TimeRangeState.absolute` marks it, while `paused`, `pausedAt` and `spanSeconds` still describe the
window as a frozen rolling range, so code that only knows rolling ranges keeps working. Its density
(`CANVAS_DENSITIES`: `compact`, `comfortable`, `wall`) scales every text and
spacing size by `DENSITY_SCALE`; a host may override the board's own value at
view time.

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

## Upgrading from 0.21

0.22 replaces the layout model, so code written against 0.21 needs changes:

| 0.21 | 0.22 |
| --- | --- |
| `WidgetBox` | `Placement` (`GridPlacement` or `RelativePlacement`) |
| `resolveWidgetBox` | `resolvePlacement` |
| `baseBox` | `basePlacement` |
| `setWidgetBox` | `setWidgetPlacement`, or `moveWidget` / `resizeWidget` |
| `gridBoxToPx` / `pxToGridBox` | `gridPlacementToPx` / `pxToGridPlacement` |
| `bringToFront` | removed: grid widgets cannot overlap, so there is no stacking order |

These keep their names but change type:

- `WidgetLayout` maps a breakpoint to a `Placement`, and `Breakpoints` maps a
  name to a `BreakpointSpec`.
- `CanvasGrid` replaces `gap` and `rowHeight` with `minCellSize`, `gutter` and
  `padding`; `GridGeometry` replaces its column and row sizes with `cell` and
  `gutter`.
- `SYNTHETIC_GENERATORS` is a list of generator ids (`'sine'`, `'ramp'`,
  `'random-walk'`) instead of `{ value, label }` pairs. Supply your own labels;
  the stored ids are unchanged.
- `parseDashboardDefinition` refuses a definition whose grid widgets overlap. A
  definition in an older format is converted before that check.

Some unions gained members, which breaks an exhaustive `switch` over them:
`WidgetChannel` has `'aggregate'` (the stat card), `OptionSpec` has
`ListOptionSpec`, and `WidgetType`, `WidgetId` and `BuiltinWidgetId` have the new
built-in widgets.

The release's upgrade guide covers what changes for stored boards and for the
other packages:
[If you build against the npm packages](https://docs.devicechain.io/deployment/releases-and-upgrades#v0220-packages).

## Compatibility

ESM only, built for bundler resolution (Vite, webpack, Rollup, esbuild). The
emitted declarations use extensionless specifiers, which resolve under
TypeScript's `bundler` module resolution and **not** under `node16`/`nodenext`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
