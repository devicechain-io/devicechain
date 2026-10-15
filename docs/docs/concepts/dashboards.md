---
title: Dashboards
---

# Dashboards

DeviceChain includes an embeddable, version-controlled dashboard system for visualizing live device data. A dashboard is a **tenant-scoped resource** that you author in the console. It renders from a portable JSON definition, and the same definition renders in the console, in the standalone reference viewer, or in any React application that installs the runtime packages from npm.

:::note Status
Available: the canvas editor, the built-in widget set (telemetry, alarm, and command/control widgets), live subscriptions, widget actions (alarm ack/clear, send command — server-authorized), versioning (publish / rollback), synthetic preview, named slots and binding manifests, export, import, clone and starter templates, the standalone `/dash` reference viewer, and the runtime packages published on npm.

Planned: richer datasource selectors (relationship-graph traversal, drill-down), per-breakpoint layout editing, and additional widgets.
:::

## The canvas

A dashboard lays widgets out on a **fluid CSS grid**. The grid provides:

- a high-resolution column grid — you place widgets by column/row span, not by fixed pixels;
- z-order and layering;
- an optional per-widget pixel offset, for fine nudging or overlap;
- an optional background image or color.

Because the columns are fractional, a dashboard fills the width of whatever container you mount it in: a panel, a fixed-width frame, or a full page. A mount-time sizing knob (`fill`, fixed width, or fixed height) lets the host choose.

Snap-to-grid is inherent to the grid. Widgets can still overlap, because they can share cells and layer by z — for example, cards over a floor-plan image.

The definition format and the renderer carry a per-breakpoint box for every widget, so a dashboard can arrange differently on different screen sizes. The canvas editor writes the base breakpoint only, though, so a console-authored dashboard has exactly one layout today.

## Widgets

Built-in widgets span six channels: telemetry, static (no data), alarm, control, selection, and location. The time-series chart, the gauge and the compass render over [Apache ECharts](https://echarts.apache.org/), and the map over [MapLibre GL](https://maplibre.org/). The rest are plain DOM.

The level and indicator widgets (linear gauge, level gauge, battery, status light, signal bars) never guess a scale: a gauge with no min and max, or no data, says so rather than drawing a plausible fill. A value off the scale pins the fill to the end and marks the readout with ▲ or ▼. They colour by threshold band only when the value is in an abnormal band; a `normal` band keeps the widget's neutral colour. The compass and the progress ring follow the same rules: a heading outside 0–360 folds onto the dial (370° reads 10°), and a progress ring with no min and max says so. The gauge, compass and progress ring print their reading as page text, not as part of the chart drawing.

| Widget | Channel | Shows |
|---|---|---|
| **Time-series chart** | telemetry | one or more measurement series over a time window |
| **Gauge** | telemetry | a single latest value against a range / thresholds |
| **Latest-value card** | telemetry | a single current reading with its timestamp |
| **Table** | telemetry | recent rows for a device or anchor |
| **Linear gauge** | telemetry | a single latest value as a bar (horizontal or vertical) or a thermometer, on a required min/max scale |
| **Level gauge** | telemetry | a tank filled to the latest value between a required min and max, read out as the value or a percent |
| **Battery** | telemetry | a battery glyph filled to the latest value, on a 0–100 percent scale unless you set min and max |
| **Status light** | telemetry | a lamp coloured by the value's threshold band, or lit and dark at an on-at value, with a text caption |
| **Signal bars** | telemetry | one bar per threshold, lit for each threshold the latest value has reached |
| **Compass** | telemetry | a heading (wind direction, bearing) as a needle on a full turn, with the degrees and the nearest of eight compass points |
| **Progress ring** | telemetry | a ring filled between a required min and max, read out as a percent or the value |
| **Label** | static | static text |
| **Image** | static | a static image (e.g. a floor plan behind other widgets) |
| **Alarm table** | alarm | live alarms for a device or anchor |
| **Alarm count** | alarm | a rolled-up count of open alarms |
| **Command / control** | control | a typed parameter form that dispatches a command and shows its live delivery lifecycle |
| **Entity selector** | selection | a picker that re-points a named slot, so a viewer chooses which entity the dashboard — or one widget within it — shows |
| **Map** | location | the last known position of the bound devices |

Widgets are themed with CSS custom properties, so an embedding application controls their appearance without modifying widget code.

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
- **`anchor`** — telemetry scoped to an organizational entity (a customer, area, or asset), named by a tracked relationship. The runtime expands it client-side to the devices currently related to that entity and streams each member's raw samples: one stream per device, for up to 500 members. Aggregating the anchor's events into a single series server-side is reserved. A selector's `aggregation` field is stored and round-tripped, but not yet read.
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

## Authoring, versioning, and preview

You author dashboards in the **console**:

- **Canvas editor** — drag and resize, with real device / anchor pickers.
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
| `kiosk` | `1` (or `true`) | Hides the header and its controls and offers a fullscreen button. There is no sign-out control in this mode. |
| `theme` | `dark`, `light` | Sets the colour theme. Outside kiosk mode a toggle in the header changes it and remembers the choice; a `theme` parameter is never remembered. |
| `density` | `compact`, `comfortable`, `wall` | Reserved for the canvas density override. The value is validated and accepted, but it does not change the rendering yet. |

A parameter with an unrecognised value is ignored and a warning is written to the browser console; the board still loads with that setting at its default.

See also the [Architecture](./architecture.md) overview and the [GraphQL API reference](../reference/graphql-api.md).
