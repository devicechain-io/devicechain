---
sidebar_position: 1
title: GraphQL API
---

# GraphQL API

Every DeviceChain service that exposes an external API does so through **GraphQL**.

:::note Status
Schemas evolve while DeviceChain is pre-release. The published schema files are the
authoritative reference — **introspection is disabled by default** (see [Exploring the
schema](#exploring-the-schema)).
:::

## Download the schemas

Every schema is published here, generated from the files the services parse at startup:

| | |
|---|---|
| **Index** | [`/schema/index.json`](pathname:///schema/index.json) — every area, its auth plane, its endpoint and its schema file |
| **Schemas** | `/schema/<area>.graphql`, plus `-admin` and `-settings` for the two areas that serve those planes |

Start with the index. It names the auth plane each schema sits on, which the schema
files themselves do not say — and an admin mutation offered to a tenant developer is a
call they can never authorize.

These are served as plain text with permissive CORS, so they can be fetched directly:

```bash
curl -s https://docs.devicechain.io/schema/index.json | jq '.areas[] | {area, endpoint}'
curl -s https://docs.devicechain.io/schema/device-management.graphql
```

## Endpoints

The ingress routes `/api/<area>/graphql` to each functional-area service, stripping the prefix so
it reaches that service's own `/graphql`. So every endpoint below is
`https://<your-host>/api/<area>/graphql`:

| Area | Covers |
|---|---|
| `user-management` | authentication — `login`, `selectTenant`, `refresh` — and the tenant's own governance view |
| `device-management` | devices, device types, profiles, assets, areas, customers, groups, relationships, alarms, credentials, detection-rule authoring |
| `event-management` | time-series event queries — `events`, `locationEvents`, `measurementEvents`, `alertEvents`, `bucketedMeasurements` |
| `device-state` | live last-known state — `latestMeasurements`, `latestLocation`, `deviceStates` |
| `command-delivery` | command dispatch — `createCommand`, `cancelCommand`, command history |
| `event-processing` | detection-rule validation, replay preview, rule health |
| `dashboard-management` | dashboard CRUD and versioning |
| `outbound-connectors` | per-tenant outbound connector CRUD |
| `notification-management` | notification channels and policies |
| `ai-inference` | a single call, `inferRuleCandidate`, backing the natural-language rule-authoring door — present only when the optional inference service is enabled |

Three further endpoints sit on a **separate identity-token plane** rather than the tenant plane, and
are authorized for the superuser or operator:

| Endpoint | Covers |
|---|---|
| `/api/user-management/admin/graphql` | the instance admin API — identity directory, memberships, role catalog, tenant registry and tiers |
| `/api/user-management/settings/graphql` | instance settings |
| `/api/ai-inference/admin/graphql` | operator-registered inference providers |

Authorization across the data-plane services is **capability-based**: each resolver checks for a
specific authority (e.g. `device:write`) carried on the caller's tenant token. Note that a few
authorities do not line up with intuition — reading device credentials requires `device:write`,
not `device:read`, and `latestLocation` requires `location:read` while its `device-state` siblings
require `state:read`.

`sparkplug-ingest` and `lwm2m-ingest` serve no GraphQL at all and are deliberately kept off the
`/api` router entirely. `event-sources` is routed but answers with a placeholder schema — ingest
reaches it over the device-plane transports, not this API.

## Querying events

event-management exposes read queries over the persisted event history. Each takes a search
criteria — device, event types, an occurred-time range, a relationship anchor (`{type, token}`),
and pagination — and returns paginated results:

```graphql
query {
  measurementEvents(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    anchor: { type: "customer", token: "acme-corp" }
  }) {
    results { deviceToken occurredTime name value }
    pagination { totalRecords }
  }
}
```

Entities are named by **token** throughout, including inside the anchor. Both time bounds are
inclusive, they filter on `occurredTime` (the instant the device reported, not the instant the
platform stored it), and results come back newest first. Pagination is 1-based.

**`measurementEvents` does not filter by measurement name.** The criteria has no `name` field, so
"just the temperature readings for this device" is not directly expressible — either filter
client-side on `results[].name`, or use `bucketedMeasurements`, which does take a `name` and
returns time buckets:

```graphql
query {
  bucketedMeasurements(criteria: {
    deviceToken: "sensor-001",
    name: "temperature",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    intervalSeconds: 300
  }) { bucketStart name avg min max sum count }
}
```

All event queries are **tenant-scoped automatically** — results are limited to the caller's tenant, and a query without a resolved tenant is rejected.

## Exploring the schema

**Introspection is disabled by default.** A production deploy that sets nothing exposes no
introspection surface, so pointing a GraphQL client at an endpoint and expecting it to
self-document will not work — the introspection query is rejected.

That leaves two ways to read the schema.

**The published schema files**, listed under [Download the schemas](#download-the-schemas)
above. This is the reliable route because it needs no running instance and no token — which
matters most when you are still evaluating DeviceChain. They are generated from
`backend/services/<area>/graphql/` on every docs build, so they cannot drift from the schemas
the services parse. (The committed sources are there too, if you would rather read them in
place; note the filenames are not uniform — most areas use `schema.graphql`, but
`user-management` uses `schema.gql`, `admin_schema.gql` and `settings_schema.gql`.)

**Introspection on a development instance.** Set `DC_GRAPHQL_DEV_TOOLS=true` on the service to
enable it. Do this on a dev instance only; it is off by default deliberately. Any value that does
not parse as a boolean is treated as disabled rather than guessed. With it enabled, the usual
query works:

```graphql
query {
  __schema {
    types { name kind }
  }
}
```

## Conventions

- Entities are addressed by a human-readable **token** in addition to an internal id.
- List queries take a search-criteria input with pagination.
- Mutations follow a `create* / update* / delete*` naming pattern.

## Input validation

**An input field the schema does not define is rejected.** Sending an undeclared field
fails the whole request with an error naming the offending field, and suggesting the
declared field you probably meant:

```json
{
  "errors": [{
    "message": "Variable \"request\" has invalid value.\nField \"deviceProfileToken\" is not defined by type \"DeviceTypeCreateRequest\". Did you mean \"profileToken\"?"
  }]
}
```

This holds whether the value is written as a literal in the query or supplied through a
variable.

It matters more than a typo check. A silently discarded field is indistinguishable from one
that was applied: the mutation returns success, and you get a partially-configured entity
with nothing to indicate a value went missing. Rejecting is what makes a success response
mean the whole input was understood.

Detailed, per-type reference pages will be generated from the schemas as they stabilize.
