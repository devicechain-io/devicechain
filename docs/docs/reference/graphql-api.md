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
| event-management | time-series event queries |
| user-management | users, roles, authentication |

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

Detailed, per-type reference pages will be generated from the schemas as they stabilize.
