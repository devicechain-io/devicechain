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
| `command-delivery` | command dispatch — `createCommand`, `cancelCommand`, fleet-wide batches (`createCommandBatch`, `cancelCommandBatch`), command history |
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

:::caution Deeply backfilled readings are missing from `bucketedMeasurements`
A bucketed read whose `intervalSeconds` is a whole multiple of 60 and which carries no anchor
filter is served from a pre-aggregated rollup rather than from the raw readings. The rollup keeps
itself current over a **trailing 30-day window**, and everything older than that was materialized
once, when the database was created.

So a reading **written now but stamped more than 30 days in the past** — by its own `occurredTime`,
which a device controls — falls between the two: too old for the refresh window, too late for the
one-time pass. `measurementEvents` returns it and the raw history is complete; `bucketedMeasurements`
does not show it, and no error says so.

The boundary is how far **back** the reading is stamped, not how old the data is. A reading
backfilled by an hour, or a day, or three weeks is picked up within a minute and is fine. This
only reaches a device that buffered for over a month, or one whose clock is wrong by that much.
Sub-minute intervals and anchor-scoped reads are served from the raw readings and are unaffected.
:::

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

### An update replaces the whole record {#an-update-replaces-the-whole-record}

`update*` mutations take the **same input as their `create*` sibling**, and they mean what that
implies: every field you send is written, and **every field you leave out is erased**. There is no
patch semantic. The mutation returns the entity and succeeds, so a field you did not mean to clear
is gone with nothing to indicate it.

```graphql
# Renaming a device this way ALSO clears its externalId and its metadata.
mutation {
  updateDevice(token: "sensor-001", request: { token: "sensor-001", name: "Cold store probe" }) {
    token
  }
}
```

Read the entity first, change what you mean to change, and send the whole thing back:

```graphql
query { devicesByToken(tokens: ["sensor-001"]) { token name externalId metadata } }
```

Two consequences worth planning around. Because the write covers every field, **two people editing
one entity overwrite each other across all of it**, not only where they overlap. And because the
update input is the create input, it carries the **token** — for most entities the resolver locates
the row by the request's token, so it is not a rename channel, but where an entity's token is
genuinely fixed the server says so rather than moving it (a geofence is the worked example).

Three deliberate exceptions, which are exceptions because omission means something specific rather
than nothing:

| Where | An omitted field means |
| --- | --- |
| A notification channel's `secret` | **Unchanged.** You never need to re-send it; a non-null value replaces it, an empty string clears it |
| A tenant's [governance overrides](../concepts/governance.md) | **Inherit the platform default** — never "unlimited" |
| A device profile's `activeVersion` | Not writable here at all; it moves only by publish and rollback |

:::note
This is the current contract, not the intended one. Partial updates — where an omitted field is
left alone — are planned before 1.0, and will apply uniformly across every area rather than
per-service. Until then, treat every `update*` as a full replace unless the table above says
otherwise.
:::

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

### What a token may contain {#what-a-token-may-contain}

Every entity token — and every tenant id — must match:

```
^[A-Za-z0-9][A-Za-z0-9_-]*$
```

Letters (either case), digits, hyphens and underscores, starting with a letter or a digit, and at
most **128 characters**. Anything else is refused at write, on create *and* on update, before
anything is stored.

This is a security rule rather than a house style, which is why it is this narrow. A token is
spliced into infrastructure namespaces: a tenant id becomes a segment of a NATS subject recovered
by splitting on `.`, and a device token becomes a segment of an MQTT topic. So a `.` shifts subject
segments, and `*`, `>`, `+` and `#` inject wildcards that match **across tenants**. Uppercase is
allowed deliberately, because machine-supplied identifiers like device serials and VINs are usually
uppercase.

The identifiers integrators reach for first are the ones this rejects — `sensor.001`, a MAC address
`AA:BB:CC:DD:EE:FF`, `plant/line-2`, anything with a space. **Put those in `externalId` instead**,
which is opaque, has no format constraints, and is unique within a tenant when present. Give the
entity a token you choose and keep the device's own identifier alongside it.

The console mints tokens for you from a per-entity-type template, so this rarely comes up there;
it is the API and scripted-provisioning path where it bites first.

Detailed, per-type reference pages will be generated from the schemas as they stabilize.
