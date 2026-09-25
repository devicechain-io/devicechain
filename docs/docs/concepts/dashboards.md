---
title: Dashboards
---

# Dashboards

DeviceChain includes an embeddable, version-controlled dashboard system for visualizing live device data. A dashboard is a **tenant-scoped resource** that you author in the console. It renders from a portable JSON definition, and the same definition renders in the console, in the standalone reference viewer, or in any React application that installs the runtime packages from npm.

:::note Status
Available: the canvas editor, the built-in widget set (telemetry, alarm, and command/control widgets), live subscriptions, widget actions (alarm ack/clear, send command — server-authorized), versioning (publish / rollback), synthetic preview, named slots and binding manifests, export, the standalone `/dash` reference viewer, and the runtime packages published on npm.

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

Built-in widgets span five channels: telemetry, alarm, control, selection, and location. The time-series chart and the gauge render over [Apache ECharts](https://echarts.apache.org/), and the map over [MapLibre GL](https://maplibre.org/). The rest are plain DOM.

| Widget | Channel | Shows |
|---|---|---|
| **Time-series chart** | telemetry | one or more measurement series over a time window |
| **Gauge** | telemetry | a single latest value against a range / thresholds |
| **Latest-value card** | telemetry | a single current reading with its timestamp |
| **Table** | telemetry | recent rows for a device or anchor |
| **Label** | telemetry | static text |
| **Image** | telemetry | a static image (e.g. a floor plan behind other widgets) |
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

- **live** — a device newly assigned to an area appears on that area's dashboard without editing it;
- **permission-checked** — it uses the caller's own tenant-scoped, authenticated API access.

How live values arrive depends on the channel:

| Channel | How values arrive |
|---|---|
| telemetry | a GraphQL subscription, multiplexed so that a crowded dashboard opens one stream per device rather than one per widget |
| alarm | re-reads a query, triggered by a live alarm stream and backstopped by a 30-second poll |
| control | poll-only, because command-delivery exposes no subscription |

Alarm and control widgets each hold their own stream and timer. Only the telemetry channel is multiplexed.

## Authoring, versioning, and preview

You author dashboards in the **console**:

- **Canvas editor** — drag and resize, with real device / anchor pickers.
- **Versioning** — the live definition is a mutable **draft**. **Publish** captures it as an immutable version, and you can **roll back** to any earlier version, which re-drafts it in place. History is a list of published snapshots, not a diff.
- **Synthetic preview** — swap live data for a client-side generator (sine / ramp / random-walk) to validate layout, scales, and thresholds before any device has reported.
- **Export** — download or copy a definition to share or embed elsewhere.

A published version's definition is not readable on its own. The version list carries only its number, optional label and description, and who published it when. Rollback, which is a write, is the only way to get its contents back.

## Embedding: definitions, slots, and binding manifests {#embedding-definitions-slots-and-binding-manifests}

A dashboard definition is portable and reusable as a template. Rather than hard-coding which device each widget reads, widgets bind to **named slots**. At mount time, a host supplies a **binding manifest** that maps each slot to a concrete device or anchor. So one definition plus two manifests gives two live dashboards for two different devices, with no change to the definition itself.

The runtime is structured as layered packages:

| Package | Role |
|---|---|
| `@devicechain/client` | the TypeScript SDK — authentication, GraphQL operations, live subscriptions |
| `@devicechain/widgets` | the React widget components (datasource in, pixels out) and the renderer that lays them out |
| `@devicechain/dashboards` | the `DashboardHub` (owns the connection, resolves selectors, multiplexes telemetry subscriptions) and the definition, selector, slot, and binding-manifest types |

A React application embeds a live dashboard by constructing a hub with a resolver and a binding manifest, then rendering the definition. The console and the standalone `/dash` application both take this path inside this repository, building against the same artifacts an outside consumer downloads. The packages are published to npm, so an external application installs them the same way. See [npm Packages](../reference/npm-packages.md) for the install line, the version and dist-tag policy, and the one piece of host wiring the map widget needs.

### The `/dash` reference viewer

The standalone **`/dash`** application is the reference external embedder. It has its own login, accepts an exported definition plus a binding manifest, and renders it.

It is view-only as to authoring: there is no editor, no save, and it never fetches a dashboard from the service. Widget actions stay available, though. A viewer holding `alarm:write` or `command:write` can acknowledge and clear alarms and dispatch commands to real devices from the dashboard it renders, and the server enforces those rights either way.

See also the [Architecture](./architecture.md) overview and the [GraphQL API reference](../reference/graphql-api.md).
