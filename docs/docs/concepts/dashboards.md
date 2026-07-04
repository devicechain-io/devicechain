---
sidebar_position: 4
title: Dashboards
---

# Dashboards

DeviceChain includes an embeddable, version-controlled dashboard system for visualizing live device data. A dashboard is a **tenant-scoped resource** authored in the console and rendered from a portable JSON definition — the same definition can be embedded in any React app or opened in the standalone reference viewer.

:::note Status
Available: the canvas editor, the built-in widget set, live subscriptions, versioning (publish / rollback), synthetic preview, named slots + binding manifests, and export — plus the standalone `/dash` reference viewer. Planned: publishing the runtime packages to the public npm registry (they build in-repo today), richer datasource selectors (relationship-graph traversal, drill-down), widget actions, and additional widgets. (ADR-039)
:::

## The canvas

A dashboard is a **canvas** of positioned widgets — absolute position and size, z-order / layering, an optional background image or color, and snap-to-grid. Because the layout is a canvas rather than a rigid grid, widgets can overlap and layer (for example, cards over a floor-plan image). Layouts are **per-breakpoint**, so a dashboard can arrange differently on different screen sizes.

## Widgets

Six built-in widgets render over [Apache ECharts](https://echarts.apache.org/):

| Widget | Shows |
|---|---|
| **Time-series chart** | one or more measurement series over a time window |
| **Gauge** | a single latest value against a range / thresholds |
| **Latest-value card** | a single current reading with its timestamp |
| **Table** | recent rows for a device or anchor |
| **Label** | static text |
| **Image** | a static image (e.g. a floor plan behind other widgets) |

Widgets are themed with CSS custom properties, so an embedding application controls their appearance without modifying widget code.

## Datasources

A widget does not embed a query — it embeds a typed **selector** that the runtime resolves:

- **`device`** — a single device by token.
- **`anchor`** — telemetry scoped to an organizational entity (a customer, area, or asset), aggregated by a server-side query over the events anchored to that entity.

Selectors are resolved through the client SDK against the GraphQL API, so resolution is **live** — a device newly assigned to an area appears on that area's dashboard without editing it — and **permission-checked**, because it uses the caller's own tenant-scoped, authenticated API access. Live values arrive over **GraphQL subscriptions**, multiplexed so that a crowded dashboard opens one stream per device rather than one per widget.

## Authoring, versioning, and preview

Dashboards are authored in the **console**:

- A drag-and-resize **canvas editor** with real device / anchor pickers.
- **Versioning** — the live definition is a mutable **draft**; **publish** captures it as an immutable version, and you can **roll back** to any earlier version (which re-drafts it in place). History is a list of published snapshots, not a diff.
- **Synthetic preview** — swap live data for a client-side generator (sine / ramp / random-walk) to validate layout, scales, and thresholds before any device has reported.
- **Export** — download or copy a definition to share or embed elsewhere.

## Embedding: definitions, slots, and binding manifests

A dashboard definition is portable and **reusable as a template**. Rather than hard-coding which device each widget reads, widgets bind to **named slots**; a host supplies a **binding manifest** at mount time that maps each slot to a concrete device or anchor. So **one definition + two manifests → two live dashboards** for two different devices, with no change to the definition itself.

The runtime is structured as layered packages:

| Package | Role |
|---|---|
| `@devicechain/client` | the TypeScript SDK — authentication, GraphQL operations, live subscriptions |
| `@devicechain/widgets` | the React widget components (datasource in, pixels out) |
| `@devicechain/dashboards` | the `DashboardHub` (owns the connection, resolves selectors, multiplexes subscriptions) and the renderer |

Any React application embeds a live dashboard by constructing a hub with a resolver and a binding manifest and rendering the definition. The standalone **`/dash`** application is the reference external embedder: it has its own login, accepts an exported definition plus a binding manifest, and renders it **view-only** — the worked example of embedding DeviceChain dashboards in a separate application.

See also the [Architecture](./architecture.md) overview and the [GraphQL API reference](../reference/graphql-api.md).
