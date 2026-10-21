---
title: Dashboards
---

# Dashboards

DeviceChain includes an embeddable, version-controlled dashboard system for visualizing live device data. A dashboard is a **tenant-scoped resource** that you author in the console. It renders from a portable JSON definition, and the same definition renders in the console, in the standalone reference viewer, or in any React application that installs the runtime packages from npm.

:::note Status
Available: the canvas editor (with undo and redo, duplicate, and copy and paste between boards), the built-in widget set (telemetry, alarm, and command/control widgets), a board time range with pause, live subscriptions that recover after an outage, widget actions (alarm ack/clear, send command — server-authorized), versioning (publish / rollback), synthetic preview, named slots and binding manifests, export, import, clone and starter templates, the standalone `/dash` reference viewer, and the runtime packages published on npm.

Planned: richer datasource selectors (relationship-graph traversal, drill-down), per-breakpoint layout editing, and additional widgets.
:::

## The canvas

A dashboard lays widgets out on a grid of **square cells**. The grid provides:

- placement by cells: each widget covers a block of whole cells, columns by rows, and each widget type can set the smallest and largest block it takes;
- no overlap: two widgets on the grid never share a cell, and moving or resizing one in the editor pushes the others down out of its way;
- widgets placed **relative** to another one, anchored to a point on it and moving with it, such as a badge over a map or a caption under a chart. These are the only widgets that sit over others;
- an optional background image or color.

A board that sets no grid of its own gets 12 columns, a minimum cell size of 56 px and an 8 px gutter.

The dashboard measures the container you mount it in (a panel, a fixed-width frame, or a full page), not the screen. While the authored columns fit at the canvas's minimum cell size, the layout is drawn as authored and the cells stretch to fill the width, so a wall display gets bigger cells rather than more of them. In a narrower container the dashboard uses as many columns as fit and places the widgets into them in reading order, each within the sizes it allows. Below 600 px wide, as on a phone, each widget takes its own row, so the dashboard reads as a single column. A mount-time sizing knob (`fill`, fixed width, or fixed height) lets the host choose.

A board also has a **density**: `compact`, `comfortable` (the default) or `wall`. It scales every text and spacing size, the gutters and the smallest cell a column may shrink to (by 0.9, 1 and 1.5), so `wall` draws a board meant to be read across a room. The author sets it in the editor, and a viewer can override it (see the `/dash` [`density` parameter](#session-refresh-and-kiosk-mode)).

The definition format and the renderer carry a per-breakpoint placement for every widget, chosen by the width of the dashboard's container, and a breakpoint layout defined this way is drawn in place of the automatic rearrangement. The canvas editor writes the base breakpoint only, though, so a console-authored dashboard has one layout today, rearranged automatically in narrower containers.

## Widgets

Built-in widgets span six channels: telemetry, static (no data), alarm, control, selection, and location. The time-series chart, the gauge and the compass render over [Apache ECharts](https://echarts.apache.org/), and the map over [MapLibre GL](https://maplibre.org/). The rest are plain DOM.

The level and indicator widgets (linear gauge, level gauge, battery, status light, signal bars) never guess a scale: a gauge with no min and max, or no data, says so rather than drawing a plausible fill. A value off the scale pins the fill to the end and marks the readout with ▲ or ▼. They color by threshold band only when the value is in an abnormal band; a `normal` band keeps the widget's neutral color. The compass and the progress ring follow the same rules: a heading outside 0–360 folds onto the dial (370° reads 10°), and a progress ring with no min and max says so. The gauge, compass and progress ring print their reading as page text, not as part of the chart drawing.

The stat card shows one figure of one measurement over a time span: its average, minimum, maximum, sum, the number of readings, or its latest reading. The figure is computed by the platform from every reading in the span, not from the averages a chart draws, so a maximum is the largest reading and not the largest average. The card follows the dashboard's time range unless you give it a span of its own, says which span it covers beside the figure ("Maximum · 1h"), and stops updating when the dashboard is paused. The span ends at the last whole minute and starts on a whole minute, so the figure never includes readings from before the span. A live card reads again once a minute for an hour's span, less often for longer spans (every 42 minutes for a week), and not while the browser tab is hidden. A card set to the latest reading shows that reading's age. It colors by threshold band like the level widgets. When the figure cannot be computed (the data source cannot, or the read failed, or the span holds no readings) the card says so and shows no number; the number of readings is the exception, and a span with none reads 0. The bar chart and the pie chart draw the latest reading of each selected measurement, one bar or one wedge per measurement; a pie is shares of a whole, so it refuses negative values and all zeros rather than draw something else. Both are drawn as page text and shapes, so their labels are readable text rather than part of a canvas. A bar's value is printed beside it when it fits, the pie's legend gives each value and its share, and every value is also in the chart's description for screen readers.

The command slider and toggle show what was last sent to the device, with that command's delivery status (pending, delivered or failed), never the device's current state. The slider sends once when released, or on Enter from the keyboard, not while it is dragged; a value it was not told to send (Escape, or the drag abandoned) is discarded. The toggle sends the opposite of the last value sent only while that command is pending or delivered. With nothing sent, when the last command failed, or when it is not among the recent commands, the toggle does not guess: it offers On and Off and sends the one you choose. Neither control sends again while its previous command is still being accepted. If the command has another required parameter with no default value, or the chosen parameter is missing or of the wrong type, the control says so and sends nothing. A viewer without `command:write` sees them read-only. The slider also has a **stepper** mode (set `mode` to stepper): − and + step the value by the parameter's declared step, within its declared minimum and maximum, and nothing is sent until you press Send (or Enter). Escape discards the value you have stepped to.

| Widget | Channel | Shows |
|---|---|---|
| **Time-series chart** | telemetry | one or more measurement series over the dashboard's time range, or over a span of its own |
| **Gauge** | telemetry | a single latest value against a range / thresholds |
| **Latest-value card** | telemetry | a single current reading with its timestamp |
| **Table** | telemetry | the latest reading of each measurement of one device |
| **Linear gauge** | telemetry | a single latest value as a bar (horizontal or vertical) or a thermometer, on a required min/max scale |
| **Level gauge** | telemetry | a tank filled to the latest value between a required min and max, read out as the value or a percent |
| **Battery** | telemetry | a battery glyph filled to the latest value, on a 0–100 percent scale unless you set min and max |
| **Status light** | telemetry | a lamp colored by the value's threshold band, or lit and dark at an on-at value, with a text caption |
| **Signal bars** | telemetry | one bar per threshold, lit for each threshold the latest value has reached |
| **Compass** | telemetry | a heading (wind direction, bearing) as a needle on a full turn, with the degrees and the nearest of eight compass points |
| **Progress ring** | telemetry | a ring filled between a required min and max, read out as a percent or the value |
| **Stat card** | telemetry | the average, minimum, maximum, sum, reading count or latest reading of one measurement over the dashboard's time range (or a span of its own) |
| **Bar chart** | telemetry | the latest reading of each selected measurement as a bar, in the data source's order or sorted by value; columns in a wide widget, rows in a tall one |
| **Pie chart** | telemetry | each selected measurement's share of the total of their latest readings, with a legend of names, values and percentages |
| **Fleet table** | telemetry | one row per device of a group and one column per measurement, each cell that device's own latest reading with its age, colored by the column's thresholds; sortable, paged, and the device name selects that device in linked widgets. A bar chart set to *one bar per device* draws the same per-device readings as bars |
| **Label** | static | static text |
| **Image** | static | a static image (e.g. a floor plan behind other widgets) |
| **Alarm table** | alarm | live alarms for a device or anchor |
| **Alarm count** | alarm | a rolled-up count of open alarms |
| **Alarm severity** | alarm | the active alarms broken down by severity: a stacked bar over a legend with a count for each severity, for every alarm in scope or for the devices of one type |
| **Command / control** | control | a typed parameter form that dispatches a command and shows its live delivery lifecycle |
| **Command slider** | control | a slider for one numeric parameter of a command, sent when released; its range comes from the command definition |
| **Command toggle** | control | a switch for one boolean parameter of a command, one command per click |
| **Entity selector** | selection | a picker that re-points a named slot, so a viewer chooses which entity the dashboard — or one widget within it — shows |
| **Map** | location | the last known position of the bound devices |

Most telemetry widgets take **thresholds**, a **unit** and a **precision**. A threshold names a severity (`normal`, `warning`, `minor`, `major` or `critical`), never a color, so the theme decides how each band looks in light and dark; only the abnormal bands are colored. With no precision set, a value is rounded to 3 decimals, or to 4 significant digits below 1.

A widget that shows readings says how old its value is (for example, "12 sec. ago"). If its **Stale after (seconds)** setting is filled in, a value older than that is grayed out and badged as stale; no default is assumed, because any fixed number would paint healthy, slow-reporting devices as stale. A widget with nothing to show says why (no device chosen, no data in the range, a group it cannot show) rather than drawing an empty frame.

Widgets are themed with CSS custom properties, so an embedding application controls their appearance without modifying widget code.

## Time range and pause {#time-range-and-pause}

A board has a **time range**: the span of time, back from now, that its charts draw and its stat cards compute over. The viewer picks 15 minutes, 1 hour, 6 hours, 24 hours or 7 days from the board's header. The author can save a different default in the editor; a board that saves none uses one hour. A widget can pin a span of its own, from a minute to a week, which the viewer's choice does not change; a time-series chart pinned to five minutes stays a five-minute strip.

**Pause** freezes what the board shows at that instant: charts, cards, gauges, tables, alarm widgets and the map hold the values on screen, and resume picks up the live values again. A pause freezes the display only. Commands, and alarm acknowledge and clear, still go out at once and their status keeps updating, which the paused indicator says ("commands still send").

A chart reads its history in buckets that keep a read to at most 240 points: one minute for 15 minutes and an hour, 2 minutes for 6 hours, 6 minutes for 24 hours and 42 minutes for 7 days. A chart keeps at most 2,000 live samples; when a long span at a high rate passes that, the oldest go first and the chart says from when it covers.

### Map tiles and location access

:::note The map's tiles come from the tenant, not from the widget
Which tile provider to use is a tenant-level decision, not a per-widget one. See [Basemaps](./basemaps.md).
:::

The map widget renders device positions over the tenant's **basemap**. A new instance ships with one already configured, so a map widget draws tiles without anyone setting one up. The widget's own `tileUrl` and `attribution` options override the basemap for that one board, which is useful for trying a provider before you commit to it tenant-wide.

If no tier has a tile source — an operator set the instance default to `{}` and the tenant set nothing — the widget still draws a real map, against a bundled world basemap of public-domain continents and borders. A flat panel of relative positions remains only for the case where the map renderer itself cannot be loaded or started at all, such as behind a proxy that blocks it or in a browser with no usable WebGL.

Reading positions also requires the `location:read` authority. It is **not** granted by the read-only baseline every member receives; see [device location](../guides/connecting-a-device.md). A viewer without it is told so, rather than shown an empty map.

### Actions and permissions

Widgets can carry **actions**: acknowledge or clear an alarm, or send a command. The server authorizes each action against the caller's own tenant-scoped rights. For example, an action requiring `alarm:write` is inert for a read-only viewer.

Opening a dashboard requires `dashboard:read`, which every enabled tenant member holds. It is part of the read-only baseline, alongside reading devices, events, state, commands, and alarms. A dashboard is a saved arrangement of data those authorities already reach, so a member who can read the data can open the view of it.

`dashboard:write` gates create, update, publish, rollback, and delete, and stays role-granted.

## Datasources

A widget does not embed a query. It embeds a typed **selector** that the runtime resolves:

- **`device`** — a single device by token.
- **`anchor`** — telemetry scoped to an organizational entity (a customer, area, or asset), named by a tracked relationship. The runtime expands it client-side to the devices currently related to that entity and streams each member's raw samples: one stream per device, for up to 500 members. Only the fleet table and a bar chart set to one bar per device read an anchor, because they keep each device's readings apart. Every other telemetry widget shows one device, and bound to an anchor it says so instead of mixing its devices' readings under one measurement name. Aggregating the anchor's events into a single series server-side is reserved. A selector's `aggregation` field is stored and round-tripped, but not yet read.
- **`slot`** — a named placeholder the host resolves at mount time from its binding manifest (see [Embedding](#embedding-definitions-slots-and-binding-manifests) below). This is what the console writes today: it rewrites concrete `device` and `anchor` selectors into slots when it loads a dashboard, so an authored dashboard is a reusable template by default.

Two further kinds, `devices` and `relatedTraversal`, are reserved so a stored definition stays forward-compatible. The runtime rejects them until they are implemented.

Selectors are resolved through the client SDK against the GraphQL API. That makes resolution:

- **current** — a device newly assigned to an area appears on that area's dashboard without editing it, the next time the dashboard is opened (a board already open keeps the members it resolved until it is reloaded);
- **permission-checked** — it uses the caller's own tenant-scoped, authenticated API access.

How live values arrive depends on the channel:

| Channel | How values arrive |
|---|---|
| telemetry | a GraphQL subscription, multiplexed so that a crowded dashboard opens one stream per device rather than one per widget |
| alarm | re-reads a query, triggered by a live alarm stream and backstopped by a 30-second poll |
| control | poll-only, because command-delivery exposes no subscription |

Alarm and control widgets each hold their own stream and timer. Only the telemetry channel is multiplexed.

A subscription's connection closes when the access token it opened with expires (see [Subscriptions
over WebSocket](../reference/graphql-api.md#subscriptions-over-websocket)). The standalone `/dash`
viewer renews its access token shortly before it expires, so a board left running on a wall keeps
receiving live data. If the renewal is refused, the viewer says so (see [Session
refresh](#session-refresh-and-kiosk-mode)) instead of freezing.

### Connection state and recovery {#connection-state-and-recovery}

The board's header shows whether its live data is arriving: **live**, **reconnecting** while a
dropped connection is being retried, and **offline** once it has been down for 20 seconds or the
browser itself has gone offline. A quiet device on a healthy connection still reads live; how old a
value is, is the freshness readout's job, not this badge's.

Live widgets recover from an outage of any length without a reload. A dropped connection is retried
for as long as the board is open, and a stream the server ends is opened again after a wait that
doubles up to 30 seconds. A widget whose code failed to download shows the failure in its own frame,
and **Reset widget** downloads it again.

#### After a reconnect {#after-a-reconnect}

Readings sent while a board's connection is down never arrive on the live stream, so when a stream
comes back (the network or the server returned, or the server ended the stream and the board
opened it again) each widget reads them from stored history instead:

- a **line chart** reads its history again and replaces what it drew before the outage with it.
  Until that read lands, or if it fails, the chart breaks its line across the outage instead of
  drawing a straight line from the last reading before it to the first one after. A failed read is
  tried again every 30 seconds. On a paused board the read waits until you resume. A chart not
  bound to a single device has no stored history to read, so its break stays;
- a **fleet table** or a **bar chart with one bar per device** reads each device's latest values
  again, at once and once more just after the next whole minute, so a reading sent in the outage's
  last minute is included. A device whose read fails keeps the value it showed;
- a **stat card** reads its figure again at once instead of at its next scheduled refresh.

## Authoring, versioning, and preview

You author dashboards in the **console**:

- **Canvas editor** — drag and resize, with real device / anchor pickers. Widgets snap to cells, and moving or resizing one pushes its neighbors down. A focused widget moves a cell with the arrow keys; Shift and the arrow keys resize a grid widget by a cell and nudge a relative one by a quarter cell. A widget's **Placement** is either **Grid** or **Relative to…** another widget, anchored to one of nine points on it. The editor also sets the board's density and default time range.
- **Layout checks** — each widget shows a layout problem on itself: a size outside what the widget supports, a widget past the right edge of the grid, a relative widget that falls outside the canvas, a widget that declares no size limits, or a title that repeats its subject line. The first three are errors and block publishing; **Fit to limits** clamps every widget into the sizes it supports and brings every widget back onto the canvas. The others are advice.
- **Undo, redo, duplicate, copy and paste** — undo with Ctrl+Z (Cmd+Z on a Mac) and redo with Ctrl+Shift+Z or Ctrl+Y (Cmd+Shift+Z), up to 100 steps; typing into one field is one step. The history survives a save and a publish, so you can back out of an edit you have just saved, and is cleared by a rollback. Ctrl+D (Cmd+D) duplicates the selected widget. Copy and paste use the browser's own copy and paste, and a **Paste** button pastes the widget last copied in the tab, including on another board. A pasted or duplicated widget lands in the first free space at its own size, and a relative widget is pasted onto the grid. It keeps its data source only when the target board has the same slot bound the same way; otherwise it is pasted without one and the editor says why. A widget copied in another tenant never carries a device or entity with it. None of these shortcuts acts while you are typing in a field or while a dialog or menu is open.
- **Versioning** — the working definition is a mutable **draft**. **Publish** captures it as an immutable version and makes that version **live**: it is the one viewers are shown. You can make an earlier version live again without touching the draft (**make live**), or **roll back** to an earlier version, which overwrites the draft with it. History is a list of published snapshots, not a diff.
- **Synthetic preview** — swap live data for a client-side generator (sine / ramp / random-walk) to validate layout, scales, and thresholds before any device has reported.
- **Export** — download or copy a definition to share or embed elsewhere.
- **Import, clone, and starter templates** — create a new dashboard from an exported definition (pasted or uploaded; any dashboard the server can store imports, since the 1 MiB limit applies to the definition without its indentation), from an existing dashboard (**Clone**, on the list and in the editor's export menu), or from one of the starter templates offered by **New dashboard** (a fleet overview, a single-device detail, and an alarms board). All three go through the same check the editor applies when it opens a stored dashboard, and always create a **new** dashboard under a token you choose: an existing dashboard is never overwritten. The result is a **draft**; publishing stays an explicit step.
  - A widget this console cannot resolve (for example, from a package that is not installed here) is kept as a placeholder with its stored definition intact. The import lists it, and publishing stays blocked while any placeholder remains.
  - A device, customer, area, or asset that the definition binds to but this tenant does not have is shown before anything is saved. For each one you can pick a replacement from this tenant or leave the slot unbound and bind it later. Bindings that do exist are kept as they are. A template, or a definition exported with **Copy as template**, carries no bindings, so it asks you to pick a device for each device slot.
  - Every external site the definition loads content from (an image, map tiles, a canvas background) is listed before anything is saved, since the browser contacts those sites whenever the dashboard is opened.
  - The starter templates use example measurement names such as `temperature` and `humidity`; change them in the editor to the ones your devices report.

The draft is for authors. Reading a draft definition, or the definition of a published version, needs `dashboard:write`. A member holding only `dashboard:read` sees the dashboard's name, description and which version is live, and is served the live version's definition through the `publishedDashboard` query. A dashboard that has never been published serves nothing to them, and the answer says so rather than returning a blank board.

Publishing or making a version live does not change the draft's `updatedAt`, so saving the draft with the baseline you already hold still works. The version list itself carries only each version's number, optional label and description, and who published it when.

A dashboard records the format it was saved in. A viewer or console older than that format refuses to open the board and says to upgrade, rather than drawing it wrong; an application that calls `parseDashboardDefinition` gets a `DashboardDefinitionError` whose `code` is `'schemaTooNew'` (check `code` rather than `instanceof`, which fails when an application bundles two copies of the package). Boards saved in an older format still open and are converted on load, approximately for the layout. In the editor a converted board shows a banner asking you to review the layout and save; saving writes the new format. A time-series chart that kept a sample count from an older release now follows the board's time range, and the banner names it so you can pin a span where it should differ.

## Embedding: definitions, slots, and binding manifests {#embedding-definitions-slots-and-binding-manifests}

A dashboard definition is portable and reusable as a template. Rather than hard-coding which device each widget reads, widgets bind to **named slots**. At mount time, a host supplies a **binding manifest** that maps each slot to a concrete device or anchor. So one definition plus two manifests gives two live dashboards for two different devices, with no change to the definition itself.

The runtime is structured as layered packages:

| Package | Role |
|---|---|
| `@devicechain/client` | the TypeScript SDK — authentication, GraphQL operations, live subscriptions |
| `@devicechain/widgets` | the React widget components (datasource in, pixels out) and the renderer that lays them out |
| `@devicechain/dashboards` | the `DashboardHub` (owns the connection, resolves selectors, multiplexes telemetry subscriptions) and the definition, selector, slot, and binding-manifest types |

A React application embeds a live dashboard by constructing one hub with a resolver, then rendering the definition with a binding manifest. The renderer resolves each slot through the manifest, so re-pointing a slot, for example when a viewer drills into an alarm, re-subscribes only the widgets bound to that slot; the rest of the board keeps its live values. The console and the standalone `/dash` application both take this path inside this repository, building against the same artifacts an outside consumer downloads. The packages are published to npm, so an external application installs them the same way. See [npm Packages](../reference/npm-packages.md) for the install line, the version and dist-tag policy, and the one piece of host wiring the map widget needs.

### The `/dash` reference viewer

The standalone **`/dash`** application is the reference external embedder. It has its own login. Give it a dashboard token and it fetches the version of that dashboard that is currently live; or paste an exported definition, which needs no access to the service and is how an external embedder or an offline preview works. Either way you can add a binding manifest, and it renders the result.

It is view-only as to authoring: there is no editor and no save, and it only ever asks for the live version, never the draft. Widget actions stay available, though. A viewer holding `alarm:write` or `command:write` can acknowledge and clear alarms and dispatch commands to real devices from the dashboard it renders, and the server enforces those rights either way.

#### Session refresh and kiosk mode {#session-refresh-and-kiosk-mode}

`/dash` renews its access token ahead of expiry, using the refresh token it received at sign-in. A network outage or a server error does not end the session: the viewer keeps retrying, with growing pauses, until the current access token actually expires. If the server refuses the renewal (the sign-in was ended on the server, or the refresh token lapsed), or the outage outlasts the access token, the board is replaced by a full-screen **Your session has ended** notice with a **Sign in again** button, so a display never sits frozen on stale values. Signing in again returns to the same board when the address carries a `dashboard` parameter.

The address can carry parameters, so a bookmark or a wall display opens the right board in the right mode:

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark&density=wall
```

| Parameter | Values | Effect |
| --- | --- | --- |
| `dashboard` | a dashboard token | Loads that dashboard's live version straight after sign-in. The parameter survives sign-in, including single sign-on. |
| `kiosk` | `1` (or `true`) | Hides the header and its controls. A connection badge and a fullscreen button float over the board's corners instead. There is no sign-out control in this mode. |
| `chrome` | `0` (or `false`) | With `kiosk`, also hides the floating controls, for a screen capture or a page that frames the board itself. The connection badge still appears while the board is reconnecting or offline, so a board that stopped receiving data never looks live. |
| `theme` | `dark`, `light` | Sets the color theme. Outside kiosk mode a toggle in the header changes it and remembers the choice; a `theme` parameter is never remembered. |
| `density` | `compact`, `comfortable`, `wall` | Overrides the density the board was authored with. It scales every text and spacing size, the gutters and the smallest cell a column may shrink to, so `wall` draws larger and, on a narrow screen, with fewer columns, and `compact` draws smaller. The board's own setting applies when the parameter is absent, and the parameter is never saved to the board or remembered. |
| `range` | `15m`, `1h`, `6h`, `24h`, `7d` | Opens the board at that time range instead of the board's own default. The viewer can still change it. |
| `paused` | `1` | Opens the board paused at the moment it loads. Any other value opens it live. |

A parameter with an unrecognized value is ignored and a warning is written to the browser console; the board still loads with that setting at its default.

See also the [Architecture](./architecture.md) overview and the [GraphQL API reference](../reference/graphql-api.md).
