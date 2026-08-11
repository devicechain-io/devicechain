---
title: Dashboards
---

# Dashboards

DeviceChain includes an embeddable, version-controlled dashboard system for visualizing live device data. A dashboard is a **tenant-scoped resource** authored in the console and rendered from a portable JSON definition — the same definition renders in the console, in the standalone reference viewer, or in a React application built against this repository's frontend workspace.

:::note Status
Available: the canvas editor, the built-in widget set (telemetry, alarm, and command/control widgets), live subscriptions, **widget actions** (alarm ack/clear, send command — server-authorized), versioning (publish / rollback), synthetic preview, named slots + binding manifests, and export — plus the standalone `/dash` reference viewer. Planned: publishing the runtime packages to the public npm registry (today they are unpublished workspace source with no build step of their own — each application's bundler compiles them from TypeScript), richer datasource selectors (relationship-graph traversal, drill-down), per-breakpoint layout editing, and additional widgets.
:::

## The canvas

A dashboard lays widgets out on a **fluid CSS grid**: a high-resolution column grid (widgets are placed by column/row span, not fixed pixels), z-order / layering, an optional per-widget pixel offset for fine nudging or overlap, and an optional background image or color. Because the columns are fractional, a dashboard **fills the width of whatever container it is mounted in** — a panel, a fixed-width frame, or a full page — and a mount-time sizing knob (`fill`, fixed width, or fixed height) lets the host choose. Snap-to-grid is inherent to the grid, and because widgets can share cells and layer by z, they can still overlap (for example, cards over a floor-plan image). The definition format and the renderer carry a **per-breakpoint** box for every widget, so a dashboard can arrange differently on different screen sizes — but the canvas editor writes the base breakpoint only, so a console-authored dashboard has exactly one layout today.

## Widgets

Built-in widgets span five channels — **telemetry**, **alarm**, **control**, **selection**, and **location**. The time-series chart and the gauge render over [Apache ECharts](https://echarts.apache.org/), the map over [MapLibre GL](https://maplibre.org/); the rest are plain DOM:

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

:::note The map's tiles come from the tenant, not from the widget
The map widget renders device positions over the tenant's **basemap**, which a new instance ships already configured — so a map widget draws tiles without anyone setting one up. The widget's own `tileUrl` and `attribution` options are an override for that one board, useful for trying a provider before committing it tenant-wide.

Which provider you should actually use is a tenant-level decision, not a per-widget one: see [Basemaps](./basemaps.md).

If no tier has a tile source — an operator set the instance default to `{}` and the tenant set nothing — the widget still draws a real map, against a bundled world basemap of public-domain continents and borders. A flat panel of relative positions remains only for the case where the map renderer itself cannot be loaded at all, such as behind a proxy that blocks it.

Reading positions also requires the `location:read` authority, which is **not** granted by the read-only baseline every member receives — see [device location](../guides/connecting-a-device.md). A viewer without it is told so, rather than shown an empty map.
:::

Widgets are themed with CSS custom properties, so an embedding application controls their appearance without modifying widget code.

Widgets can also carry **actions** — acknowledge or clear an alarm, or send a command — which the server authorizes against the caller's own tenant-scoped rights (for example, an action requiring `alarm:write` is inert for a read-only viewer).

Opening a dashboard at all requires **`dashboard:read`**, which is *not* part of the read-only baseline every enabled tenant member receives — so a member with no assigned role can view devices, events, state, commands, and alarms, but cannot list or open a dashboard until a role grants it. `dashboard:write` gates create, update, publish, rollback, and delete.

## Datasources

A widget does not embed a query — it embeds a typed **selector** that the runtime resolves:

- **`device`** — a single device by token.
- **`anchor`** — telemetry scoped to an organizational entity (a customer, area, or asset), aggregated by a server-side query over the events anchored to that entity.
- **`slot`** — a **named placeholder** the host resolves at mount time from its binding manifest (see *Embedding* below). This is what the console writes today: it rewrites concrete `device` and `anchor` selectors into slots when it loads a dashboard, so an authored dashboard is a reusable template by default.

Two further kinds (`devices`, `relatedTraversal`) are reserved so a stored definition stays forward-compatible; the runtime rejects them until they are implemented.

Selectors are resolved through the client SDK against the GraphQL API, so resolution is **live** — a device newly assigned to an area appears on that area's dashboard without editing it — and **permission-checked**, because it uses the caller's own tenant-scoped, authenticated API access. How live values arrive depends on the channel: telemetry widgets read a **GraphQL subscription**, multiplexed so that a crowded dashboard opens one stream per device rather than one per widget; alarm widgets re-read a query, triggered by a live alarm stream and backstopped by a 30-second poll; control widgets are poll-only, because command-delivery exposes no subscription. Alarm and control widgets each hold their own stream and timer — only the telemetry channel is multiplexed.

## Authoring, versioning, and preview

Dashboards are authored in the **console**:

- A drag-and-resize **canvas editor** with real device / anchor pickers.
- **Versioning** — the live definition is a mutable **draft**; **publish** captures it as an immutable version, and you can **roll back** to any earlier version (which re-drafts it in place). History is a list of published snapshots, not a diff. A published version's definition is not readable on its own: the version list carries only its number, optional label and description, and who published it when — rollback, a write, is the only way to get its contents back.
- **Synthetic preview** — swap live data for a client-side generator (sine / ramp / random-walk) to validate layout, scales, and thresholds before any device has reported.
- **Export** — download or copy a definition to share or embed elsewhere.

## Embedding: definitions, slots, and binding manifests

A dashboard definition is portable and **reusable as a template**. Rather than hard-coding which device each widget reads, widgets bind to **named slots**; a host supplies a **binding manifest** at mount time that maps each slot to a concrete device or anchor. So **one definition + two manifests → two live dashboards** for two different devices, with no change to the definition itself.

The runtime is structured as layered packages:

| Package | Role |
|---|---|
| `@devicechain/client` | the TypeScript SDK — authentication, GraphQL operations, live subscriptions |
| `@devicechain/widgets` | the React widget components (datasource in, pixels out) and the renderer that lays them out |
| `@devicechain/dashboards` | the `DashboardHub` (owns the connection, resolves selectors, multiplexes telemetry subscriptions) and the definition, selector, slot, and binding-manifest types |

A React application embeds a live dashboard by constructing a hub with a resolver and a binding manifest and rendering the definition. That path is worked end to end inside this repository — the console and the standalone `/dash` application both take it — but the runtime packages are not published, so embedding today means building against the frontend workspace rather than installing from a registry. The standalone **`/dash`** application is the reference external embedder: it has its own login, accepts an exported definition plus a binding manifest, and renders it. It is view-only as to **authoring** — there is no editor, no save, and it never fetches a dashboard from the service — but widget actions stay available: a viewer holding `alarm:write` or `command:write` can acknowledge and clear alarms and dispatch commands to real devices from the dashboard it renders, and the server enforces those rights either way.

See also the [Architecture](./architecture.md) overview and the [GraphQL API reference](../reference/graphql-api.md).
