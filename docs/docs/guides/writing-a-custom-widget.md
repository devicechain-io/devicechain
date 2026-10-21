---
sidebar_position: 9
title: Writing a Custom Widget
---

# Writing a Custom Widget

A custom widget is a React component plus a small piece of plain data that describes it. You write
both with `@devicechain/widget-sdk`, ship them in an npm package, and register the package in the
application that shows dashboards. The built-in widgets are declared through exactly the same
interface, so a custom widget gets the same data binding, option validation and error handling.

Your widget receives resolved data. It does not authenticate, resolve datasources, poll, or open a
telemetry connection. The host does all of that and hands your component a snapshot.

A complete package you can copy is in the repository under `examples/widget-tank-level`. It builds
outside the monorepo against packed SDK tarballs.

## Install the SDK

```bash
npm install @devicechain/widget-sdk @devicechain/dashboards react
```

In your widget package, declare `@devicechain/widget-sdk`, `@devicechain/dashboards` and `react` as
**peer dependencies**, never as dependencies. The SDK defines the React contexts a widget reads
(selection, time zone, theme root), and a context is identified by the object that created it. The
host and every widget package must therefore resolve one copy of the SDK.

The SDK root entry has no chart or map code. A widget that only draws DOM imports from the root and
adds neither Apache ECharts nor MapLibre nor the built-in widgets to the host's bundle. Chart helpers
(`EChart`, `buildLineOption`, `buildGaugeOption`, `buildCompassOption`, `useChartTheme`,
`resolveChartTheme` and `severityFill`) are on `@devicechain/widget-sdk/echarts`, and `echarts` is an optional peer needed only by a package that
imports that entry.

## Declare a widget

```tsx
import { WidgetFrame, defineWidget, formatValue, pickSample } from '@devicechain/widget-sdk';

export const tankLevelWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Telemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1, default: 1 } },
    options: {
      unit: { kind: 'string', doc: 'Unit shown beside the level.', label: 'Unit' },
      window: { kind: 'number', integer: true, min: 1, doc: 'Samples kept.', label: 'Samples kept' },
    },
    defaults: { unit: '%', window: 1 },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ data }) => (
      <WidgetFrame title="Tank level">{formatValue(pickSample(data.latest, undefined)?.value, 1)}</WidgetFrame>
    ),
  },
});
```

`defineWidget` infers the channel and the option schema from the literal. A component whose data
disagrees with the channel, defaults that disagree with the schema, a binding that names a missing
option, or an interaction the channel cannot carry is a compile error. The registry checks the same
rules again when the host builds it, for callers that bypass the compiler.

### The descriptor

The descriptor is plain JSON-serializable data. It holds no component, callback, import URL or token,
so the definition parser and the validators can read it without loading or trusting your code.

| Field | Meaning |
| --- | --- |
| `id` | A namespaced id, `scope/name` such as `acme/tank-level`, in lowercase letters, digits and hyphens. Ids without a slash are reserved for the built-in widgets. Namespace ownership is a convention between operators in this release; it does not prove who published a package. |
| `version` | The release of this widget, as `MAJOR.MINOR.PATCH`. It is provenance. See [widget versions and compatibility](./widget-versions-and-compatibility.md). |
| `sdkApiVersion` | The generation of the SDK contract the widget was written for. Currently `1`. |
| `optionsVersion` | The shape of the stored options. Bump it only when stored options stop meaning the same thing. |
| `display` | `title`, and optionally `description` and `category`, for a host's Add menu. `titleId` and `categoryId` are optional message ids, described under [labels and help text](#labels-and-help-text). |
| `channel` | Which data the widget consumes. See below. |
| `datasource` | `{ mode: 'none' }`, or `mode: 'optional'` or `'required'` with the `selectors` an author may pick: `device`, `anchor` or `slot`. A `control` channel widget targets one device, so it offers only `device` and `slot`. |
| `binding` | How the host subscribes, written as constants and references to your own option keys. |
| `options` | The option schema. |
| `constraints` | Optional rules that span more than one option, such as `greater-than`. |
| `defaults` | The starting options of a newly added widget. |
| `interactions` | The host callbacks the widget asks for. |
| `freshness` | Optional, `true` to opt in. On the `measurement` channel only: the frame shows how old the widget's newest sample is. If the widget also declares a numeric `staleAfterSeconds` option, a value older than that is grayed out and badged as stale. Without that option the age is shown and nothing is ever marked stale. A widget that shows a single value passes that sample's time as `<WidgetFrame sampleTime={sample?.occurredTime ?? null}>`, so the age is of the value on screen rather than of whichever measurement arrived last. |
| `layout` | Optional size limits in grid cells. See [Size](#size). |

### Channels and binding policy

A widget declares one channel:

| Channel | Component receives | Binding policy |
| --- | --- | --- |
| `measurement` | `latest` by measurement name, a rolling `samples` window, and an `error`; `members` for a fleet widget | `window`: how many samples to keep. `windowSeconds`: makes the widget draw a span of time instead (see [the board's time range](#the-boards-time-range)). `members`: makes it a fleet widget (see below) |
| `aggregate` | `aggregates` (the count, minimum, maximum, sum and average of each measurement over the span, or why they could not be computed) and the newest live reading per measurement | `windowSeconds`: the span the figures cover |
| `alarm` | The current alarm rows, a total count, and load and error flags | `pageSize`, and optionally `state`, `severity` and `acknowledged` filters |
| `control` | Command history for a target device | `pageSize` |
| `location` | Location samples for the bound entity | none |
| `selection` | No data. The widget drives a slot from the host's candidate lookup. | none |
| `none` | No data. A static widget. | none |

A binding value is either a constant, `{ const: 25 }`, or a reference to an option key,
`{ option: 'window', min: 1, default: 1 }`. The reference is checked against the option's kind when
you compile. You cannot supply a GraphQL string or a subscription function: the host owns the
transport. The `selection` and `none` channels open no data channel and must declare
`datasource: { mode: 'none' }`.

A `measurement` widget that declares `members` reads its data source device by device: bound to a
group, `data.members` lists each device with its own latest reading per measurement, so two devices
reporting the same name stay apart. `{ const: true }` turns the mode on always, and an option
reference with `equals` turns it on for one value of an enum option, as the bar chart's *one bar per
device* does. A `measurement` or `aggregate` widget without the mode shows one device: offer only
`device` and `slot` as its selectors, because the host refuses a group that reaches it, with a
sentence pointing to the fleet table, rather than show whichever device reported last.

When a slot has no device, or a bound device is deleted, the host shows its standard unbound or
unavailable state and does not hand your component the previous entity's data.

## Options

An option has a `kind` (`string`, `number`, `boolean`, `enum` or `list`) and a `doc` sentence. A number can
declare `min`, `max` and `integer`. An enum declares its `values`. A string can declare
`format: 'json'`, which requires the value to parse as JSON. `required` marks an option whose
absence leaves the widget with nothing to show; an empty string counts as absent. The host
validates stored options against the schema, and the console generates its settings form from it.

A `list` option stores an array of small objects, such as a widget's thresholds. It declares an
`item` object whose fields are `string`, `number`, `boolean` or `enum` specs (a field can be
`required`, and a list cannot be nested), an optional `maxItems`, and `ordered: true` when the order of the entries means something (the settings form then offers move up and move down on each row). A stored entry with a field the
`item` does not declare is rejected, and the settings form renders one row per entry with one control
per field. The built-in `thresholds` option, which most built-in telemetry widgets carry, is one: each
entry names a severity (`normal`, `warning`, `minor`, `major` or `critical`), never a color, so the
theme decides how it looks in light and dark. Only the abnormal levels have a color: a value in a
`normal` band looks exactly as it would with no thresholds, so a healthy reading is never painted.

`optThresholds`, `bandAt` and `thresholdBands` read the same option in your own widget, and
`useSeverityColors` gives the theme's color for each abnormal level.

Read options defensively with the SDK helpers `optString`, `optNumber` and `optBoolean`. For a hand-edited
value of the wrong type they do not throw: `optString` and `optNumber` return `undefined`, and
`optBoolean` returns `false` for anything but a stored `true`.

### Labels and help text {#labels-and-help-text}

Give every option a portable `label`, and a `help` where one sentence helps. A host that has never
heard of your widget shows that text as is, so your options are understandable without any
translation on the host. Each piece of text can also carry a message id that a host looks up in its
own translations first:

| Text | Portable text | Optional message id |
| --- | --- | --- |
| Option label | `label` | `labelId` |
| Option help | `help` | `helpId` |
| Enum choices | `valueLabels`, keyed by value | `valueLabelIds`, keyed by value |
| The unset entry of an enum | `placeholder` | `placeholderId` |
| Widget title and category | `display.title`, `display.category` | `display.titleId`, `display.categoryId` |

The console uses the message id when its own translations define it, then your portable text. A
label with neither falls back to the option key made readable (`maxRows` shows as "Max rows"), and an
enum choice with neither shows its raw value. A message id the host does not define is not an error,
so your package works in a host that has never seen it.

```tsx
options: {
  show: {
    kind: 'enum',
    values: ['bar', 'number'],
    doc: 'Whether the card draws the fill bar under the number.',
    label: 'Show',
    valueLabels: { bar: 'Number and fill bar', number: 'Number only' },
    placeholder: 'Number and fill bar',
  },
},
```

### Specialized authoring controls

An option can name a specialized authoring workflow in its `control` field. This is unrelated to the
`control` data channel. Without it, the option gets the generic field for its kind: a text box, a
number box, a checkbox or a drop-down.

The vocabulary is open. Any non-empty string registers, and a host that does not implement the
named control shows the generic field for that option instead. A package therefore stays installable
on a host older than the controls it names. Name your own controls in your namespace, such as
`acme/capacity-presets`. Unprefixed names belong to the host.

The console implements the controls listed in `HOST_OPTION_CONTROLS`, exported by
`@devicechain/dashboards`:

| Control | Workflow |
| --- | --- |
| `command-vocabulary` | Picks one of the bound device's commands and writes its key, label and parameters together. |
| `slot-target` | Picks one of the dashboard's slots. |
| `device-type` | Picks a device type from the tenant's catalogue and stores its token. On a string option or a string field of a list item. |
| `basemap` | Marks the raster map tile source and its attribution. |

A control the console does not implement, including every namespaced one, gets the generic field.
To give your options an authoring experience of their own, ship an editor.

### A custom editor

`implementation.editor` is an optional React component. When a widget has one, the console shows it
in place of the generated option fields. The datasource fields and the console's notices stay the
console's. The editor receives `WidgetEditorProps`, exported by `@devicechain/widget-sdk`:

| Prop | Meaning |
| --- | --- |
| `options` | The widget's stored options, read-only. |
| `setOptions(patch)` | The only way to change them. Several keys in one call apply together. |
| `services` | Host services, passed explicitly. See below. |
| `descriptor` | Your registration's descriptor, for its option labels, bounds and enum values. |

`setOptions` is checked against your option schema before it is applied. A key the schema does not
declare, or a value of the wrong kind, throws at the moment the editor makes the mistake rather than
being stored for nothing to read. The console catches that error and shows it beside your editor, and nothing is written. `undefined` and an empty string remove a key, as does `false` for a
boolean. A number outside the declared bounds is brought within them.

`services` (`WidgetEditorServices`) holds:

| Service | Meaning |
| --- | --- |
| `slots` | The dashboard's slots, keyed by name. Empty on a dashboard with none. |
| `deviceToken` | The token of the device the widget is bound to directly, when it is. |
| `commandChoices(deviceToken)` | The commands a device accepts. Optional: supplied only when your descriptor declares the `command-vocabulary` control on an option, and left out by a host without a command vocabulary. |

An editor receives no hub, no client and no access token, and it must not import anything from the
host application. A service the host cannot supply is absent, and your editor must cope with that.
If the editor throws while rendering, the console replaces it with an error message and a retry
button, and the author's other changes on the dashboard are kept.

```tsx
implementation: {
  kind: 'trusted-react',
  component: TankLevelCard,
  editor: function TankLevelCardEditor({ options, setOptions, descriptor }) {
    return (
      <label>
        {descriptor.options.unit?.label ?? 'unit'}
        <input value={options.unit ?? ''} onChange={(e) => setOptions({ unit: e.target.value })} />
      </label>
    );
  },
},
```

Inside `defineWidget`, `options` and `setOptions` are typed from your option schema, so a misspelled
key is also a compile error. The example package's card widget has a complete editor.

## Size {#size}

A widget declares how big it may be in an optional `layout` block on its descriptor. Sizes are counted
in grid cells (columns across, rows down), not pixels:

```ts
layout: {
  defaultSpan: { cols: 4, rows: 3 }, // the size a newly added widget takes
  minSpan: { cols: 3, rows: 2 },     // the smallest it can be resized to (default 1 x 1)
  maxSpan: { cols: 8, rows: 6 },     // the largest (default: no limit)
  resize: 'both',                    // 'both' (default), 'horizontal', 'vertical' or 'none'
},
```

Every value is a positive whole number, and `minSpan` must not exceed `defaultSpan`, which must not
exceed `maxSpan`. A `resize` mode other than `both` holds the other axis at `defaultSpan`: `'horizontal'`
fixes the rows, `'vertical'` the columns and `'none'` both. A `minSpan` or `maxSpan` you declare must agree
with `defaultSpan` on a fixed axis. The registry rejects a block that breaks these rules when the host
builds it.

The editor adds a new widget at `defaultSpan`. `lintLayout` in `@devicechain/dashboards` reports a
dashboard that places the widget outside its limits as an error. Choose a `minSpan` that is the smallest
box in which your widget is still readable. A widget without a `layout` block is added at 4 x 2 cells
with no limits, and `lintLayout` reports that it declared none.

Cells are square, and a dashboard never shows a cell smaller than its minimum cell size (56 pixels
by default; a narrower screen rearranges the dashboard instead). So size `minSpan` at that smallest
cell: with the default 8-pixel gutter, 2 cells are 120 pixels, 3 are 184 and 4 are 248. On a wider
screen the cells grow and your widget only gets bigger. Releases before the square-cell grid
counted cells on a grid of 24 narrow columns and 40-pixel rows; a package that still declares those
numbers keeps working, but its spans are about twice as wide as they should be, and `lintLayout`
reports any board that places it outside them. Halve the columns and recheck the rows.

Text follows the board's density. `WidgetFrame` pads the widget and sets its content area up as a size
container, so inside it CSS container units (`cqw`, `cqh`) measure the space your widget really has.
Take text sizes from the type ramp (`fontVar('md')` and the other `xs` to `display` steps) and spacing from
`spaceVar(1)` to `spaceVar(4)` rather than writing pixel values: both multiply by the board's density
(compact, comfortable or wall), which the dashboard's author sets and a viewer can override. For a large
readout, `fitValueFontCss(chars)` gives the CSS font size for a number of characters. A canvas cannot read
CSS variables, so call `useWidgetScale()` from a component rendered inside the frame (or pass the frame a
function child) to get `sizeClass` (`compact`, `regular` or `large`), the content size in pixels, and
`px('font-md')` for a token's pixel value. The frame hides the subtitle and then the title as the widget
gets short; the title stays available to assistive technology.

## The board's time range {#the-boards-time-range}

A board has a time range that the viewer can change and pause (see
[Dashboards](../concepts/dashboards.md#time-range-and-pause)). A `measurement` widget that declares
`binding.windowSeconds` draws a span of time rather than a sample count: the span the referenced
option pins, or the board's range when the option is unset. The host seeds the span's history and
keeps at most `MAX_SPAN_SAMPLES` (2,000) live samples; when it had to drop older samples that were
still inside the span, `data.truncatedBefore` says from when the window really covers, and your
widget should say so rather than label a short window with the long span.

`useWidgetTimeRange()` returns the range: `spanSeconds`, `paused`, and `pausedAt` while paused. It
is `undefined` when the widget is rendered outside a board, so handle that case instead of assuming an
hour. `withinSpan(samples, spanSeconds, end)` drops the samples older than the span. While the board
is paused the host holds every data channel except `control` at the snapshot on screen, so your
widget needs no pause logic of its own.

`formatValue(value)` with no precision prints at most 3 decimals, or 4 significant digits below 1;
pass a precision to fix the number of decimals.

## Interactions and the trust model

`interactions` lists the host callbacks the widget uses: `alarmActions` (acknowledge and clear, alarm
channel only), `commandSend` (control channel only), `selection` and `candidates`. The host supplies
only the callbacks you declared, further limited to what the host supports and what the viewer is
allowed to do. A widget with no callback behaves as read-only. The server stays authoritative: an
action the viewer may not take is refused by the server, and the refusal reaches your component.

**A widget package is trusted code.** It is bundled into the host application at build time and runs
in the host page with the host's privileges. The declarations in a descriptor shape the API; they are
not a security boundary and do not stop malicious code in a package. The SDK does not pass your
component the access token, the client or the raw hub, but nothing prevents code on the same page
from reaching them. Install only packages you have reviewed, from publishers you trust. The host
wraps every widget in an error boundary, so a widget that throws shows an error in its own frame and
its siblings keep rendering. That does not contain an infinite loop or hostile code.

## Next

- [Add the package to the console and the dashboard app](./adding-widget-packages.md).
- [How stored widgets stay compatible across package upgrades](./widget-versions-and-compatibility.md).
