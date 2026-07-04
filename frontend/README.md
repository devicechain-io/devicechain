# DeviceChain Frontend

An **npm workspace** of React apps and libraries that talk to the backend
services over GraphQL. It contains the management console, an embeddable
dashboard runtime published as packages, and a reference external dashboard
viewer.

## Workspace layout

```
frontend/
  apps/
    console/         The management console (served at /). Authenticates against
                     user-management (ADR-008/033) and hosts every admin surface:
                     devices, events, device state, commands, RBAC, tenant admin,
                     and dashboard AUTHORING (canvas editor, versioning, synthetic
                     preview, slot binding, export). React 19 + Vite + Tailwind +
                     shadcn/ui, graphql-codegen client-preset.
    dashboard/       The /dash app — a VIEWER-ONLY reference external embedder
                     (ADR-039). It brings its own login, takes an EXPORTED
                     definition + a host binding manifest, and renders view-only.
                     Proof of the embed story: one definition + two manifests →
                     two live dashboards on different devices.
  packages/
    client/          @devicechain/client — the SDK: the GraphQL-over-fetch transport
                     + token seam, JWT helpers, and a graphql-ws subscribe() client.
    dashboards/      @devicechain/dashboards — the dashboard runtime + definition
                     contract: DashboardHub (multiplexes live subscriptions), the
                     slot / binding-manifest model, parse/serialize/migrate, and the
                     canvas-editor transforms.
    widgets/         @devicechain/widgets — six built-in widgets (time-series chart
                     + gauge over Apache ECharts, latest-card, table, label, image)
                     + the view-only DashboardRenderer. Pure { widget, data }
                     contract; themed via CSS vars (no Tailwind — run anywhere).
```

## Stack

- **React 19** + **TypeScript** (strict), bundled with **Vite 6**
- **Tailwind CSS v4** with a zinc **shadcn/ui** theme (console only; the widget
  packages are Tailwind-free so a host can embed them)
- **react-router-dom 7**, **sonner** toasts, **lucide-react** icons
- The **`@devicechain/client`** SDK — a fetch-based GraphQL client (no Apollo) plus
  a `graphql-ws` subscription client for live telemetry
- **graphql-codegen** (client-preset) types the console's operations; the SDK and
  reference viewer hand-author their typed documents (`documentMode: 'string'`)

## Getting started

```bash
cd frontend
npm install                  # installs the whole workspace
npm run codegen              # generate the console's typed GraphQL operations
npm run dev                  # per-app dev server (e.g. apps/console)
```

To exercise the full stack, deploy onto the local kind cluster (see
`deploy/local/`) and use `deploy/local/bounce.sh frontend` to rebuild + roll the
frontend image, then browse the ingress at `http://localhost/`.

## How auth works (two-tier — ADR-033)

Login is two steps. `login(email, password)` authenticates the **global identity**
and returns an instance-scoped **identity token** plus the list of tenants the
identity may act in. `selectTenant(identityToken, tenant)` exchanges it for a
tenant-scoped **access/refresh** JWT pair. The console's `AuthProvider` holds the
session, decodes the access token for display/routing, and registers a token
getter with the SDK that transparently refreshes the access token near expiry.
Decoded claims are **never** trusted for authorization — every protected operation
is enforced server-side by the token's authorities.

The `/dash` viewer runs the same two-step login independently (it does **not**
reuse the console's session).

## CI gates (run before committing)

```bash
npm ci && npm run codegen && npm run typecheck && npm run build && npm test
```

## Scripts

| Command | Purpose |
| --- | --- |
| `npm run dev` | Start a dev server |
| `npm run codegen` | Regenerate the console's typed GraphQL operations |
| `npm run typecheck` | `tsc --noEmit` across the workspace |
| `npm run build` | Type-check + production build (all apps) |
| `npm test` | Vitest across the packages |
