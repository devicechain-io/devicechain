---
sidebar_position: 1
title: GraphQL API
---

# GraphQL API

Every DeviceChain service that exposes an external API does so through **GraphQL**. The schema is introspectable, so the API is self-documenting — point any GraphQL client (GraphiQL, Apollo, Insomnia) at a service's endpoint to explore it.

:::note Status
The device-management and event-management schemas are the most developed; user-management is expanding. Schemas evolve while DeviceChain is pre-release — treat the live introspection result as authoritative.
:::

## Endpoints

Each service serves its own GraphQL endpoint (exact paths are environment-dependent and exposed via the ingress configured by your deployment):

| Service | Covers |
|---|---|
| device-management | devices, profiles, assets, areas, customers, groups, relationships |
| event-management | time-series event queries — `events`, `locationEvents`, `measurementEvents`, `alertEvents` |
| user-management | authentication — `login`, `selectTenant`, `refresh` |

`user-management` also serves a separate **instance admin API** (a distinct endpoint, authenticated with an identity token and authorized for the superuser) that manages the global identity directory, per-tenant memberships, the role catalog, and the tenant registry. Authorization across the data-plane services is **capability-based**: each resolver checks for a specific authority (e.g. `device:write`) carried on the caller's tenant token.

## Querying events

event-management exposes read queries over the persisted event history. Each takes a search criteria — device, event types, an occurred-time range, a relationship anchor (`{type, id}`), and pagination — and returns paginated results:

```graphql
query {
  measurementEvents(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceId: "42",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    anchor: { type: "customer", id: "7" }
  }) {
    results { deviceId occurredTime name value }
    pagination { totalRecords }
  }
}
```

All event queries are **tenant-scoped automatically** — results are limited to the caller's tenant, and a query without a resolved tenant is rejected.

## Exploring the schema

Because the API is introspectable, the most reliable reference is the schema itself:

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
