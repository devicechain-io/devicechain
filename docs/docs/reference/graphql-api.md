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
| `device-state` | live last-known state — `latestMeasurements`, `latestLocation`, `deviceStates` — plus `demoteAssertedPresence`, which returns an event source's asserted devices to inferred presence |
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
require `state:read`. `demoteAssertedPresence` requires `state:demote`, which is neither of those and
is granted to no role by default: it is the only thing outside the event pipeline that writes the
live-state projection, and one call reaches an entire event source's devices.

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

Enabling dev tools also serves a **GraphiQL explorer** at `/graphiql` on each service — through
the ingress that is `/api/<area>/graphiql`, and on a port-forward straight at the pod it is
`/graphiql`. Before `v0.12.0` the page loaded and then failed every query it sent, because it
was pointed at a path no service serves; it now posts to the endpoint it was reached through,
so it works on all three routes (ingress, port-forward, and the console's dev proxy).

## Conventions

- Entities are addressed by a human-readable **token** in addition to an internal id.
- List queries take a search-criteria input with pagination.
- Mutations follow a `create* / update* / delete*` naming pattern.

### How much of a record an update writes {#an-update-replaces-the-whole-record}

**Two update contracts are in service at once, and which one a mutation is on is visible in its
signature.** A mutation taking a dedicated `*UpdateRequest` is a **partial update**; one taking its
`create*` sibling's input is a **full replace**. Those are the only two contracts — but two caveats
sit on top of them and both bite in practice. A handful of individual fields on the full-replace
mutations behave differently from the mutation they are on, and one behaves in exactly the opposite
way; those are enumerated [below](#where-the-default-does-not-hold). And the **token** inside a
full-replace payload has its own rules, which differ by mutation — see
[the token argument](#the-token-argument-names-the-record). Read both tables before you automate
anything.

A **partial update** distinguishes three states rather than two:

| What you send for a field | What happens to the stored value |
| --- | --- |
| Nothing — the field is absent | Left alone |
| An explicit `null` | Cleared |
| A value | Set to that value |

So a rename is only a rename:

```graphql
# Changes the name. The description, the externalId, the metadata and the device's
# type are all left exactly as they were, because none of them is mentioned.
mutation {
  updateDevice(token: "sensor-001", request: { name: "Cold store probe" }) {
    token
    name
  }
}
```

A **full replace** rebuilds the stored record from the request: every field you send is written,
and **every field you leave out is erased**. The mutation returns the entity and succeeds, so a
field you did not mean to clear is gone with nothing to indicate it. The remedy is to read the
entity first, change what you mean to change, and send the whole thing back:

```graphql
query {
  metricDefinitionsByToken(tokens: ["inlet-temp"]) {
    token name description unit minValue maxValue metadata
    deviceProfile { token }
  }
}
```

Two consequences of a full replace worth planning around. Because the write covers every field,
**two people editing one entity overwrite each other across all of it**, not only where they
overlap — except on `updateDashboard`, `updateConnector` and `updateAiProvider`, which take an
optional `expectedUpdatedAt` and refuse the write when the stored timestamp has moved since you
read it. And because the update input is the create input, it carries a **token**, which is where
the two contracts differ most.

#### The `token` argument names the record {#the-token-argument-names-the-record}

Every `update*` declares `token: String!`, and **that argument is what decides which record is
written.** What the *payload* token does — where one still exists — depends on the mutation, and
there are three answers rather than one. The differences are real, so they are listed rather than
smoothed over.

| The payload token | Which mutations | A token that **disagrees** | A token that is **empty** |
| --- | --- | --- | --- |
| **Is not there at all** | every [partial update](#which-mutations-are-partial-updates) | *unrepresentable* — the input has no `token` field, so the schema rejects it | — |
| **Must agree** | `updateMetricDefinition`, `updateCommandDefinition`, `updateDetectionRule`, `updateEntityGroup`, `updateDeviceCredential`, `updateProvisioningProfile`, `updateEntityRelationshipType`, `updateNotificationPolicy`, `updateDashboard` | **refused** | ignored — read as "unspecified" |
| **Names the new token** (a rename) | `updateDeviceProfile`, `updateNotificationChannel`, `updateConnector`, `updateAiProvider` | **renames the record** | **refused** |
| **Must agree, exactly** | `updateGeoFence` | **refused** | **refused** — a fence token must also satisfy the [token grammar](#what-a-token-may-contain) |

The rename row is not a leftover. Each of those four keys the things that depend on the record by
its internal id rather than by its token — a channel's delivery secret, a connector's credential, a
provider's key handle — so renaming one orphans nothing. Two carry an extra guard of their own:
`updateDeviceProfile` refuses a rename once the profile has been **published or adopted** by a
device type, because from that point published rules and device rosters *do* name it by token; and
`updateGeoFence` refuses a rename outright, because detection rules name fences by token inside
compiled expressions this service cannot rewrite.

What every row agrees on is that a payload token can no longer **blank** a record, and can never
make the mutation write a record other than the one `token:` names.

:::note[This changed]
Before this release the behaviour was neither uniform nor safe, and both failures returned success.

Most `update*` mutations located the record by the **payload** token and ignored the argument
entirely, so a request naming one entity in `token:` and another in `request.token` silently updated
the second and returned it. The rest honoured the argument but then wrote the payload token over the
stored one — so the payload still moved the record, and an **empty** payload token, which
`token: String!` permits (`""` is a perfectly good non-null String), blanked the record's token and
left a live row addressable by nothing.

If you have a client that relied on the payload naming the record, it now gets an error rather than
writing the wrong row. If you have one that sends `token: ""` on an update, it now gets an error on
the rename mutations and is ignored on the rest — where before it destroyed the record's identity.
:::

### Where the default does not hold {#where-the-default-does-not-hold}

Every exception in the API this release serves, on top of the partial/full-replace split above.
Anything not named here follows its mutation's contract.

| Field | What omitting it does |
| --- | --- |
| `secret` on `updateNotificationChannel`, `updateConnector`, `updateAiProvider` | **Kept** — and an empty string *clears* it. The inverse of a partial update's `null`; see the warning below |
| `config` on `updateTenantTier` | **Kept.** Clearing a tier's settings re-prices every tenant at it, so it is not reachable by omission — send `{}` to clear |
| `selector` on `updateEntityGroup` | **Kept.** An omitted *or empty* selector leaves the compiled one in place |
| `firstName` / `lastName` on `updateProfile` | **Kept.** The one `update*` taking bare arguments rather than a `request`; an empty string still clears |
| `credentialType` on `updateProvisioningProfile` | **Reset to `ACCESS_TOKEN`** — neither kept nor cleared |
| `activeVersion` on a device profile or an entity group | Nothing: it is not writable here at all, and moves only by publish and rollback |
| `memberType` / `membershipMode` on `updateEntityGroup` | Nothing on omission, but *sending a different value* is refused — both are immutable |
| A tenant's [governance overrides](../concepts/governance.md) on `updateTenant` | Erased — and erased here means **inherit the platform default**, never "unlimited" |

:::danger An empty string is not a safe way to say "leave this alone"
For the three write-only `secret` fields, **null preserves and `""` deletes** — the exact inverse
of a partial update, where null clears. You cannot read a secret back, so there is nothing to
re-send; the API's answer is that omitting it keeps it.

This matters because the full-replace advice above — read the entity, send the whole thing back —
pushes you toward filling in every field. Doing that for a secret you did not mean to touch, by
sending `secret: ""`, deletes the stored credential and the mutation returns success. A connector
whose credential is gone starts failing authentication on every outbound dispatch. **Leave the
field out.**
:::

### Which mutations are partial updates {#which-mutations-are-partial-updates}

Partial updates are arriving one area at a time rather than all at once, and the intent is to
convert the rest before 1.0. In device-management this release, these take a dedicated
`*UpdateRequest`:

`updateDeviceType` · `updateDevice` · `updateAssetType` · `updateAsset` · `updateCustomerType` ·
`updateCustomer` · `updateAreaType` · `updateArea`

No other area has converted yet: notification-management, dashboard-management,
outbound-connectors, ai-inference and user-management are all still full replaces. (The five
`update*` mutations on user-management's admin plane already take a dedicated `*UpdateRequest` that
drops the token, so they carry no payload-token question — but they are still full replaces on
every field they do declare.)

You do not have to keep that list: **read the signature**. `request: FooUpdateRequest!` is partial;
a `FooCreateRequest` is a full replace, whether or not it is spelled with a trailing `!`. The
[schema you downloaded](#download-the-schemas) is the authority.

#### Two fields worth knowing about on the converted mutations {#two-fields-on-converted-mutations}

- **A required reference cannot be cleared.** `updateAsset`'s `assetTypeToken`, and its peers on
  devices, customers and areas, re-point the entity when you send one and leave it alone when you
  do not — but an explicit `null` is **refused**, because "no type" is not a state those entities
  can be in. An unknown token is refused too, and the refusal is total: nothing is written.
- **`updateDeviceType`'s `profileToken` is the one reference that *can* be cleared**, because a
  device type with no [device profile](../concepts/domain-model.md) is a real thing. Under the old
  full-replace shape, omitting it while renaming a type **detached the profile** — which silently
  un-declared position for every device built on the type, successfully. Omitting it now keeps the
  current profile; `null`, or an empty token, detaches it.

`metadata` is replaced wholesale on both contracts when you send it, and cleared by `null` on a
partial update. It is an opaque JSON string in the schema rather than a map, so there is no per-key
merge to choose between — the API has never been able to address an individual key.

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
