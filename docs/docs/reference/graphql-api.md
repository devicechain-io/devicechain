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

**Every `update*` mutation is a partial update, and there is only one contract.** Each takes a
dedicated `*UpdateRequest` — never the `create*` sibling's input — and each distinguishes three
states rather than two. Individual **fields** can still deviate: a required reference that refuses
to be cleared, a write-only secret, a field that is not in the update input at all. Those are
enumerated [below](#where-the-default-does-not-hold), and that table is the complete list. Read it
before you automate anything.

The three states:

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

**Send only what you mean to change.** Reading the record first and posting the whole thing back is
the habit a full-replace API teaches, and it is the wrong one here: it is more work, it widens the
window in which you overwrite a concurrent edit, and on a write-only `secret` field it is actively
destructive — see [the warning below](#where-the-default-does-not-hold).

Concurrency is the one thing a partial update narrows without removing: two writers who touch
different fields no longer clobber each other, but two who touch the same field still do.
`updateDashboard`, `updateConnector` and `updateAiProvider` take an optional `expectedUpdatedAt`
and refuse the write when the stored timestamp has moved since you read it. Pass the `updatedAt`
you last read; omit it for last-write-wins.

#### The `token` argument names the record {#the-token-argument-names-the-record}

Every `update*` declares `token: String!`, and **that argument is what decides which record is
written.** What the *payload* token does — where one still exists — depends on the mutation, and the
difference is real, so it is listed rather than smoothed over.

There used to be a third answer: a payload token that had to **agree** with the argument, refused
when it disagreed and read as "unspecified" when empty. Its last two mutations have converted, so
the row naming it is gone rather than left standing empty.

| The payload token | Which mutations | A token that **disagrees** | A token that is **empty** |
| --- | --- | --- | --- |
| **Is not there at all** | every [partial update](#which-mutations-are-partial-updates) | *unrepresentable* — the input has no `token` field, so the schema rejects it | — |

There was a second: a payload token that **named the record's new token**, which is how a
profile, a connector, a provider and a notification channel were renamed. All four now have a
[rename mutation of their own](#renaming-a-record), so that row is gone too — and with it the
last update input on the platform that carried a token at all. The single row above is the whole
answer now.

#### Renaming a record {#renaming-a-record}

Four records used to be renamed the same way: by sending a different token inside a
full-replace update payload. Each now has a **mutation of its own**, where the new token can
mean only one thing:

```graphql
renameDeviceProfile(token: String!, newToken: String!): DeviceProfile!
renameConnector(token: String!, newToken: String!): Connector!
renameAiProvider(token: String!, newToken: String!): AiProvider!
renameNotificationChannel(token: String!, newToken: String!): NotificationChannel!
```

All four follow one contract. A **blank** `newToken` — empty or whitespace-only — is refused,
because it would leave a live record addressable by nothing. Renaming a record to the token it
**already has** is an idempotent success that returns the record, so retrying after a partial
failure is safe. A token **another record of that kind already holds** is refused by name rather
than surfacing as a constraint violation. And the authority is the one the matching update
takes: a rename is an edit of the record, not a new kind of act.

Each of these renames was always intended, because what depends on the record keys on its
internal id rather than on its token: a channel's delivery secret and the channel id a policy's
rules store, a connector's credential, a provider's API key along with its tier grants and every
tenant's model assignment. A rename orphans none of them.

Two things a rename does still move, and both are worth checking before you issue one. A REACT
rule names its connector **by token**, so rules pointing at a renamed connector have to be
re-pointed. And `renameDeviceProfile` refuses a rename outright once the profile has been
**published or adopted** by a device type, because from that point published rules and device
rosters name it by token.

`updateNotificationPolicy` needed no such mutation: nothing keys on a policy's token, so a policy
is moved by creating the new one and deleting the old.

**A geofence's token is immutable, and the rule now lives in the top row.** `updateGeoFence` used
to reconcile two tokens and refuse a disagreement; its input carries none at all, so there is no
request that asks for a rename. The reason is unchanged: detection rules name fences by token
inside compiled expressions this service cannot rewrite, so a rename would leave every one of them
naming nothing while the mutation returned success. If you need a fence under a different token,
**create the new one first and delete the old one after** — doing it the other way round can
forfeit position headroom you are grandfathered on and leave the fence unrecreatable.

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
writing the wrong row. If you have one that sends `token: ""` on an update, it now gets an error
either way — refused by the rename mutations, and rejected by the schema on a partial update, whose
input has no `token` field to send it in — where before it destroyed the record's identity. It used
to be *ignored* on a third set of mutations, which is what the "must agree" rule did with an empty
token; those have all converted.
:::

### Where the default does not hold {#where-the-default-does-not-hold}

Every field-level exception in the API this release serves. Anything not named here follows the
three states above: absent leaves it alone, `null` clears it, a value sets it.

| Field | What omitting it does |
| --- | --- |
| `secret` on `updateNotificationChannel`, `updateConnector`, `updateAiProvider` | **Kept.** A value rotates it; `null` — or an empty string — deletes it. You cannot read a secret back, so omitting it is how you say "leave the credential alone" |
| `config` on `updateTenantTier` | **Kept.** Clearing a tier's settings re-prices every tenant at it, so it is not reachable by omission — send `null` or `{}` to clear |
| `selector` on `updateEntityGroup` | **Kept** when omitted. Unlike most partial-update fields it cannot be *cleared*: `null` is refused, because a dynamic group with no selector matches nothing and cannot be repaired. A static group is refused a selector outright |
| `definition` on `updateDashboard` | **Kept** when omitted, which is how you rename a dashboard without resending its document. Like `selector` above it cannot be *cleared*: `null` is refused, because a dashboard with no definition is not a thing. A malformed one refuses the whole update, so a rename sent with it is not applied either |
| `firstName` / `lastName` on `updateProfile` | **Kept.** An empty string clears, and `null` means the same thing — these are the display-name columns, where "empty" is a value a person may legitimately have rather than an absence |
| `credentialType` on `updateProvisioningProfile` | **Not in the update input.** Provisioning can mint exactly one credential type today, so the field would only ever restate what is stored. It used to be *reset* to `ACCESS_TOKEN` by any update that omitted it |
| `activeVersion` on a device profile or an entity group | Nothing: it is not writable here at all, and moves only by publish and rollback |
| `memberType` / `membershipMode` on `updateEntityGroup` | **Not in the update input.** Both are identity, so a change is unrepresentable rather than refused |
| A tenant's [governance overrides](../concepts/governance.md) on `updateTenant` | **Kept.** Sending `null` removes the override, which means **inherit the tier and then the platform default** — never zero, and never "unlimited" |

:::danger An empty string is not a safe way to say "leave this alone"
For every write-only `secret` field, **`""` deletes the stored credential.** You cannot read a
secret back, so there is nothing to re-send; the API's answer is that omitting it keeps it.

This matters because "read the record, change one thing, send it all back" is the habit a
full-replace API teaches, and clients written against one still do it. Filling in every field means
sending `secret: ""` for a credential you never meant to touch — which deletes it, and the mutation
returns success. A connector whose credential is gone starts failing authentication on every
outbound dispatch. **Leave the field out.**

`null` deletes the credential too. That is the platform's ordinary meaning of a null rather than an
exception: a null clears the field it names. The **inversion** these fields used to carry — where
null preserved and only `""` deleted — is gone.
:::

### Which mutations are partial updates {#which-mutations-are-partial-updates}

**All of them.** The conversion arrived one area at a time and is now complete, so this section is
no longer a roster of which mutations are safe — it is a record of what changed in each area, kept
because a client written against the old behaviour needs to know. In device-management, **every
`update*`** takes a dedicated `*UpdateRequest`:

`updateDeviceType` · `updateDevice` · `updateAssetType` · `updateAsset` · `updateCustomerType` ·
`updateCustomer` · `updateAreaType` · `updateArea` · `updateMetricDefinition` ·
`updateCommandDefinition` · `updateDetectionRule` · `updateGeoFence` · `updateEntityGroup` ·
`updateDeviceCredential` · `updateProvisioningProfile` · `updateEntityRelationshipType` ·
`updateDeviceProfile`

Outbound connectors and AI inference have converted their one update each: `updateConnector` and
`updateAiProvider`. Every rename channel those areas' payload tokens carried moved to a
[dedicated rename mutation](#renaming-a-record) rather than being dropped.

In notification-management, **both** `update*` mutations have converted: `updateNotificationChannel`
and `updateNotificationPolicy`. Two things about the policy are worth knowing before you send one:

- **`rules` is optional, and omitting it now leaves the rule set exactly as it is** — the same rows,
  not a rebuilt copy of them. It used to be required, and every update replaced the whole rule set,
  so an edit that only changed a name destroyed and recreated every rule; an edit that left `rules`
  out emptied the policy and returned success. Whole-replace is still available: send the list.
  Sending `null` **or** `[]` empties the rule set — for a list those are one request spelled two ways.
- **`deviceTypeToken` is not in the update input at all.** A non-empty value is refused at write
  (the dispatcher skips a device-type-scoped policy, so accepting one would return success on a
  policy that delivers nothing), which left the field with no request it could accept beyond a
  no-op. It stays on the create input, where the refusal explains itself.

In dashboard-management, **`updateDashboard`** takes a `DashboardUpdateRequest` and carries no
token at all. Its one wrinkle is `definition`: the field is nullable so it can be *omitted* — that
is how you rename a dashboard without resending its whole document — but an explicit `null` on it
is **refused**, because a dashboard with no definition is not a thing. It keeps its optional
`expectedUpdatedAt` precondition, and an update that names no field at all writes nothing (not even
`updatedAt`) while a stale precondition on it is still a conflict.

In user-management, **every `update*`** now takes a dedicated request too:

`updateRole` · `updateTenant` · `updateTenantTier` · `updateOauthClient` · `updateProfile`

Outbound connectors and AI inference have converted their one update each: **`updateConnector`**
and **`updateAiProvider`**. Both keep an optional `expectedUpdatedAt`, and on both, `type`/`config`
and `kind`/`endpoint` respectively are validated as a **pair** against the values the record will
hold — so naming one of a pair re-checks the stored other, and a change that would leave the record
unusable is refused at the write rather than at first use.

**That is the whole update surface.** No `update*` mutation anywhere takes a `create*` sibling's
input, so there is no remaining mutation for a reader to check against a list — and this page no
longer carries one. Earlier releases did, twice: first a roster of the areas that had not converted
(wrong every time one landed), then a rule saying the signature was the authority because two
contracts coexisted. Both were written to be drift-proof and both drifted, in the same way — their
premise expired. What replaced them is the [three states](#an-update-replaces-the-whole-record) and
the [field-level exceptions](#where-the-default-does-not-hold), which is a statement about the
whole API rather than a partition of it.

:::caution[Check the schema for what a given input declares]
One contract does not mean every input takes every field. What an update *can* express is what its
`*UpdateRequest` declares, and some fields are deliberately absent — `deviceTypeToken` on
`updateNotificationPolicy`, `memberType` on `updateEntityGroup`, `credentialType` on
`updateProvisioningProfile` — because no request for them would be accepted. Others accept a value
but refuse a `null`.

The [schema you downloaded](#download-the-schemas) is the authority for the first of those; the
[exceptions table](#where-the-default-does-not-hold) is the authority for the second. Neither is a
question about which contract the mutation is on, because there is only one.
:::

:::note[This changed for user-management]
`updateRole`, `updateTenant`, `updateTenantTier` and `updateOauthClient` used to write every field
their input declared, so a request naming only `name` blanked the rest and returned the emptied
record. `updateTenant` is the one to re-check first: omitting a governance override used to **erase**
it, so renaming a tenant removed every ceiling an operator had set. Omitting one now leaves it alone,
and only an explicit `null` removes it.

`tierToken` on `updateTenant` became **optional**. Omitting it keeps the tenant at its current tier;
an explicit `null` is refused, because every tenant has a tier.

`authorities`, `redirectUris` and `scopes` became **nullable lists** (`[String!]`, not `[String!]!`),
so they now have an absent state. Omitting one leaves it alone; sending a list replaces it wholesale;
`null` and `[]` both mean "empty". A role's authorities **may** be emptied, because a role that
grants nothing is a thing you can create. An OAuth client's redirect URIs and scopes **may not** —
an empty redirect allowlist matches nothing, so the client could never complete an authorization.

`updateProfile` now takes `request: ProfileUpdateRequest!` instead of bare `firstName` / `lastName`
arguments. Its behaviour is unchanged: it writes only the names you send, and `""` still clears one.
:::

#### Fields worth knowing about on the converted mutations {#two-fields-on-converted-mutations}

- **A required reference cannot be cleared.** `updateAsset`'s `assetTypeToken`, and its peers on
  devices, customers and areas, re-point the entity when you send one and leave it alone when you
  do not — but an explicit `null` is **refused**, because "no type" is not a state those entities
  can be in. An unknown token is refused too, and the refusal is total: nothing is written.
- **`updateDeviceType`'s `profileToken` is the one reference that *can* be cleared**, because a
  device type with no [device profile](../concepts/domain-model.md) is a real thing. Under the old
  full-replace shape, omitting it while renaming a type **detached the profile** — which silently
  un-declared position for every device built on the type, successfully. Omitting it now keeps the
  current profile; `null`, or an empty token, detaches it.

- **A required field cannot be cleared either, even when it is not a reference.** A metric's
  `dataType`, a credential's `credentialType` and `enabled`, a rule's `definition` and `enabled`, a
  fence's `geometry`, a provisioning profile's `provisionKey` and `provisionSecret`: send a value to
  change one, omit it to leave it alone, and an explicit `null` is **refused**. This is worth
  stating separately from the reference case because the failure it prevents is invisible: folding
  `enabled: null` to `false` would disable a credential or park a rule and return success, and
  `false` is a value you could legitimately have sent.
- **Omitting a secret now keeps it.** `credentialValue` on `updateDeviceCredential` and
  `provisionSecret` on `updateProvisioningProfile` used to be blanked by any update that failed to
  restate them — which took a device, or a whole self-registering fleet, offline at its next
  connection, with a `200` on the edit that broke it.

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
