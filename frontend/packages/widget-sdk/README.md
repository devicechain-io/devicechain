<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# @devicechain/widget-sdk

The typed contract for writing a DeviceChain dashboard widget as a React component, and the
sealed registry that makes it available to a host application. The built-in widgets are
declared through exactly this interface.

```bash
npm install @devicechain/widget-sdk @devicechain/dashboards react
```

`@devicechain/dashboards` and `react` are **peer dependencies**: the SDK defines the React
contexts a widget reads (selection, candidate lookup, time zone, theme root, time range, density,
freshness), and a context is
identified by the object that created it, so every widget package and the host must resolve
the same single copy of this package. Declare it as a peer in your own widget package too,
never as a dependency.

## What is in the box

The root entry is chart-free and map-free: the card frame (`WidgetFrame`), theme-variable
references (`css`), formatters, the change-flash cue, option readers (`optString`,
`optNumber`, ...) and the React hooks that bind a hub channel to widget state. A widget
that only draws DOM imports from here and pulls in neither Apache ECharts nor MapLibre nor
the built-in widgets.

A stream hook never returns one subscription's snapshot for another. Since 0.22,
`useAlarmStream` and `useLocationStream` return `loading: true` with no rows from the very
render whose subscription changed (a re-pointed slot, a new filter), until the new
subscription answers. Before 0.22 that one render still carried the previous subscription's
alarms or positions.

Chart helpers live on a separate entry, `@devicechain/widget-sdk/echarts`: the `EChart`
wrapper, `buildLineOption` / `buildGaugeOption` and the chart colour theme. `echarts` is an
**optional peer dependency**, needed only by a widget that imports that entry.

The chart engine itself is not part of that entry's static imports. `EChart` loads it on demand,
as one chunk, the first time a chart mounts, so a host bundler keeps ECharts out of the first
download. Until the engine arrives, the chart's box is empty. If the engine cannot be fetched,
`EChart` throws the error to the nearest error boundary, and the next mount fetches it again. A host that
wants charts drawn on its first frame awaits `loadChartEngine()` before rendering. A widget that
draws a chart can set `MyWidget.preload = loadChartEngine` on its component, and then
`preloadWidgets` from `@devicechain/widgets` fetches the engine for any board that contains it.

## Declare a widget

```tsx
import { defineWidget } from '@devicechain/widget-sdk';

export const tankLevelWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Telemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1 } },
    options: {
      unit: { kind: 'string', doc: 'Unit shown beside the level.' },
      window: { kind: 'number', integer: true, min: 1, doc: 'Samples retained.' },
    },
    defaults: { unit: '%' },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    // `data` is typed from the channel: a measurement widget receives measurement data.
    component: ({ data }) => <span>{Object.values(data.latest)[0]?.value}</span>,
  },
});
```

The descriptor is plain JSON-serializable data: no component, callback or URL. A descriptor
that disagrees with its channel, or whose defaults or bindings disagree with its option
schema, fails type checking, and `createWidgetRegistry` checks the same rules again at
runtime.

## Register it

```ts
import { createWidgetRegistry } from '@devicechain/widget-sdk';

const registry = createWidgetRegistry([tankLevelWidget]);
```

A registry is validated and sealed when it is created: duplicate ids, malformed descriptors,
an unsupported `sdkApiVersion` and invalid bindings or defaults are all reported together in
one `WidgetRegistryError`. There is no global registration; two registries on one page are
independent. Extension ids are namespaced (`scope/name`); the un-namespaced ids are reserved
for the built-in widgets.

Trusted widget code runs with the host page's privileges. The declarations in a descriptor
shape the API; they are not a security boundary.

## Offering a download from the frame

A widget can let the viewer save the data it shows. Pass `exportRows` to `WidgetFrame`: a function
that returns a `WidgetExportTable` (a file name without its extension, headings, and rows of
strings, numbers, booleans or nulls), or a promise of one. The frame then draws a download button
in its title bar, and **Download CSV** calls your function when the viewer picks it, so the table is
built on demand from what the widget is showing. The prop is optional and feature-detected: a widget
that passes none is unchanged, and the frame draws no button when its title bar is hidden.

The CSV encoder is the frame's: RFC 4180 quoting, a leading apostrophe on a text cell that begins with
`=`, `+`, `-`, `@`, tab or carriage return (so a spreadsheet never runs it as a formula), and UTF-8 with
a byte-order mark so Excel reads units such as `°C`. Put the unit in the heading, write instants as
ISO 8601 with a zone, and say in the file name what the file covers (the span and the bucket on
screen). A chart drawn with `<EChart>` also gets **Download image (PNG)** with no code of yours.

## Upgrading from 0.21

0.22 removes no export. A TypeScript build can notice three changes:

- `ChartTheme` has a required `severity` field. This matters only if you build a theme by hand
  instead of calling `useChartTheme` or `resolveChartTheme`.
- The unions `WidgetChannel` (now with `'aggregate'`), `WidgetId`, `BuiltinWidgetId`, `ThemeVar`
  and `OptionSpec` (now with `ListOptionSpec`) gained members, so an exhaustive `switch` over one
  of them needs a new case.
- `formatValue` with no precision rounds to 3 decimals, or 4 significant digits below 1, where it
  used to print the number unrounded.

Additions, which need no change:

- `WidgetFrame` takes `flush`, which removes the content padding for a widget that fills its box
  edge to edge, and `sampleTime`, the time of the one sample a single-value widget shows, so its
  age readout ages that sample rather than the newest one the host holds.
- `useElementSize` takes an optional `step` that rounds each dimension down to a multiple of it,
  so a caller that needs only a coarse size does not re-render for every pixel.
- New exports for size and density tokens (`fontVar`, `spaceVar`, `tokenStyle`,
  `useWidgetScale`, `fitValueFont`), sample age (`useFreshness`, `Freshness`, `StaleBadge`,
  `formatAge`), alarm severity (`SEVERITY_LEVELS`, `severityVar`, `useSeverityColors`),
  thresholds (`parseThresholds`, `thresholdBands`, `bandAt`), the board's time range
  (`useWidgetTimeRange`) and the hub connection (`useHubConnectionState`, `HubConnectionBadge`).

`SDK_API_VERSION` is unchanged. See
[If you build against the npm packages](https://docs.devicechain.io/deployment/releases-and-upgrades#v0220-packages)
in the release's upgrade guide.
