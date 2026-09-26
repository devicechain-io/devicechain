---
sidebar_position: 1
title: GraphQL API
---

# GraphQL API

Every DeviceChain service that exposes an external API does so through **GraphQL**. This page lists
the endpoints, how to get the schemas, the conventions every mutation follows, and the limits every
request is held to.

:::note Status
Schemas evolve while DeviceChain is pre-release. The published schema files are the authoritative
reference. Introspection is disabled by default (see [Exploring the schema](#exploring-the-schema)).
:::

## Download the schemas {#download-the-schemas}

Every schema is published here, generated from the files the services parse at startup:

| | |
|---|---|
| **Index** | [`/schema/index.json`](pathname:///schema/index.json) — every area, its auth plane, its endpoint and its schema file |
| **Schemas** | `/schema/<area>.graphql`, plus `-admin` and `-settings` for the two areas that serve those planes |

Start with the index. It names the auth plane each schema sits on, and the token that plane takes,
for every area in one place. Each published schema file also opens with a comment naming its own plane,
endpoint and token. The plane matters because an admin mutation offered to a tenant developer is a
call they can never authorize.

The files are served as plain text with permissive CORS, so you can fetch them directly:

```bash
curl -s https://docs.devicechain.io/schema/index.json | jq '.areas[] | {area, endpoint}'
curl -s https://docs.devicechain.io/schema/device-management.graphql
```

## Endpoints {#endpoints}

The ingress routes `/api/<area>/graphql` to each functional-area service and strips the prefix, so
the request reaches that service's own `/graphql`. Every endpoint below is therefore
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
| `ai-inference` | a single call, `inferRuleCandidate`, backing natural-language rule authoring — present only when the optional inference service is enabled |

Three more endpoints sit on a **separate identity-token plane** rather than the tenant plane. They
are authorized for the superuser or operator:

| Endpoint | Covers |
|---|---|
| `/api/user-management/admin/graphql` | the instance admin API — identity directory, memberships, role catalog, tenant registry and tiers |
| `/api/user-management/settings/graphql` | instance settings |
| `/api/ai-inference/admin/graphql` | operator-registered inference providers |

Authorization across the data-plane services is **capability-based**: each resolver checks for a
specific authority (for example `device:write`) carried on the caller's tenant token. A few
authorities do not line up with intuition:

- Reading device credentials requires `device:write`, not `device:read`.
- `latestLocation` requires `location:read`, while its `device-state` siblings require `state:read`.
- `demoteAssertedPresence` requires `state:demote`, which is neither of those. No role names it by
  default; only a role holding the `*` super-authority, such as the seeded `tenant-admin` role, has
  it without an explicit grant. It is the only thing outside the event pipeline that writes the
  live-state projection, and one call reaches an entire event source's devices.

`sparkplug-ingest` and `lwm2m-ingest` serve no GraphQL at all and are deliberately kept off the
`/api` router entirely. `event-sources` is routed but answers with a placeholder schema: ingest
reaches it over the device-plane transports, not this API.

## Querying events {#querying-events}

event-management exposes read queries over the persisted event history. Each takes a search
criteria — device, event types, an occurred-time range, a relationship anchor (`{type, token}`), and
pagination — and returns paginated results:

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

- Entities are named by token throughout, including inside the anchor.
- Both time bounds are inclusive, and they filter on `occurredTime`: the instant the device
  reported, not the instant the platform stored it.
- Results come back newest first.
- Pagination is 1-based.

All event queries are **tenant-scoped automatically**: results are limited to the caller's tenant,
and a query without a resolved tenant is rejected.

An event's `processedTime` is when the platform **received** it. For MQTT on the platform's broker,
this is when the broker stored the message, which after an `event-sources` outage can be well before
the event was processed. For other transports, it is when the ingest service took the event in.

**`measurementEvents` does not filter by measurement name.** Its criteria has no `name` field, so
"only the temperature readings for this device" is not directly expressible. Either filter
client-side on `results[].name`, or use `bucketedMeasurements`, which does take a `name` and returns
time buckets:

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
A reading **written now but stamped more than 30 days in the past** (by its own `occurredTime`,
which a device controls) is returned by `measurementEvents` but not by `bucketedMeasurements`, and no
error says so. This only reaches a device that buffered for over a month, or one whose clock is wrong
by that much. See [Backfilled readings and the rollup](#backfilled-readings-and-the-rollup).
:::

### Backfilled readings and the rollup {#backfilled-readings-and-the-rollup}

A bucketed read whose `intervalSeconds` is a whole multiple of 60 and which carries no anchor filter
is served from a pre-aggregated rollup rather than from the raw readings. The rollup keeps itself
current over a **trailing 30-day window**. Everything older than that was materialized once, when the
database was created.

A reading written now but stamped more than 30 days back falls between the two: too old for the
refresh window, too late for the one-time pass. The raw history is complete, so `measurementEvents`
returns it; `bucketedMeasurements` does not show it.

The boundary is how far **back** the reading is stamped, not how old the data is. A reading
backfilled by an hour, a day, or three weeks is picked up within a minute and is fine. Sub-minute
intervals and anchor-scoped reads are served from the raw readings and are unaffected.

## Exploring the schema {#exploring-the-schema}

**Introspection is disabled by default.** A production deploy that sets nothing exposes no
introspection surface. If you point a GraphQL client at an endpoint and expect it to self-document,
the introspection query is rejected.

That leaves two ways to read the schema.

**The published schema files**, listed under [Download the schemas](#download-the-schemas). This is
the reliable route: it needs no running instance and no token, which matters most while you are
still evaluating DeviceChain. The files are generated from each service's schema sources on every
docs build, so they cannot drift from the schemas the services parse. You can also read the
committed sources in the repository. Every one is a `.graphql` file named for the endpoint that
serves it: `schema.graphql` for the tenant API, plus `admin_schema.graphql` and
`settings_schema.graphql` in the areas that also serve an identity-token API.

**Introspection on a development instance.** Set `DC_GRAPHQL_DEV_TOOLS=true` on the service to
enable it. Do this on a dev instance only; it is off by default deliberately. Any value that does not
parse as a boolean is treated as disabled rather than guessed. With it enabled, the usual query
works:

```graphql
query {
  __schema {
    types { name kind }
  }
}
```

Enabling dev tools also serves a **GraphiQL explorer** at `/graphiql` on each service. Through the
ingress that is `/api/<area>/graphiql`; on a port-forward straight at the pod it is `/graphiql`. The
explorer posts to the endpoint it was reached through, so it works on all three routes: ingress,
port-forward, and the console's dev proxy. Before `v0.12.0` the page loaded and then failed every
query it sent, because it pointed at a path no service serves.

## Conventions {#conventions}

- Entities are addressed by a human-readable **token** in addition to an internal id.
- List queries take a search-criteria input with pagination.
- Mutations follow a `create* / update* / delete*` naming pattern.

### How much of a record an update writes {#an-update-replaces-the-whole-record}

**Every `update*` mutation is a partial update, and there is only one contract.** Each takes a
dedicated `*UpdateRequest`, never the `create*` sibling's input, and each distinguishes three states
rather than two:

| What you send for a field | What happens to the stored value |
| --- | --- |
| Nothing — the field is absent | Left alone |
| An explicit `null` | Cleared |
| A value | Set to that value |

Individual **fields** can still deviate: a required reference that refuses to be cleared, a
write-only secret, a field that is not in the update input at all. Those are listed in
[Where the default does not hold](#where-the-default-does-not-hold). Read it before you automate
anything.

A rename is only a rename:

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

**Send only what you mean to change.** Reading the record and posting the whole thing back is the
habit a full-replace API teaches, and it is the wrong one here. It is more work, it widens the window
in which you overwrite a concurrent edit, and on a write-only `secret` field it is actively
destructive — see [the warning below](#where-the-default-does-not-hold).

A partial update narrows concurrency conflicts without removing them. Two writers who touch
different fields no longer overwrite each other, but two who touch the same field still do.
`updateDashboard`, `updateConnector` and `updateAiProvider` take an optional `expectedUpdatedAt` and
refuse the write when the stored timestamp has moved since you read it. Pass the `updatedAt` you last
read, or omit it for last-write-wins.

#### The `token` argument names the record {#the-token-argument-names-the-record}

Every `update*` names the record by an **argument**, never by the payload, and **that argument
decides which record is written.** On all of them but two it is `token: String!`; `updateRole`
also takes `scope: String!`, and names the role by scope and token together. `updateOauthClient`
takes `clientId: String!` instead, and `updateProfile` takes no locator at all, because the record
it edits is the signed-in identity.

The payload carries no token. On every [partial update](#which-mutations-are-partial-updates), the
input has no `token` field, so a payload token that disagrees with the argument is unrepresentable:
the schema rejects it.

Two other behaviours existed before and are gone:

- A payload token that had to agree with the argument — refused when it disagreed, read as
  "unspecified" when empty. Its last two mutations have converted.
- A payload token that named the record's new token, which is how a profile, a connector, a
  provider and a notification channel were renamed. All four now have a
  [rename mutation of their own](#renaming-a-record), and with that the last update input on the
  platform that carried a token is gone.

Every update now agrees on two things: a payload token can no longer **blank** a record, and it can
never make the mutation write a record other than the one `token:` names.

#### Renaming a record {#renaming-a-record}

Four records used to be renamed by sending a different token inside a full-replace update payload.
Each now has a mutation of its own, where the new token can mean only one thing:

```graphql
renameDeviceProfile(token: String!, newToken: String!): DeviceProfile!
renameConnector(token: String!, newToken: String!): Connector!
renameAiProvider(token: String!, newToken: String!): AiProvider!
renameNotificationChannel(token: String!, newToken: String!): NotificationChannel!
```

All four follow one contract:

- A blank `newToken` (empty or whitespace-only) is refused, because it would leave a live record
  addressable by nothing.
- Renaming a record to the token it already has is an idempotent success that returns the
  record, so retrying after a partial failure is safe.
- A token another record of that kind already holds is refused by name, with `extensions.code` set
  to `CONFLICT`, whether the refusal comes from the lookup or from a concurrent rename that got
  there first. See [A value that must be unique](#unique-values).
- The required authority is the one the matching update takes: a rename is an edit of the record,
  not a new kind of act.

Each of these renames was always intended, because what depends on the record keys on its internal
id rather than its token. That covers a channel's delivery secret and the channel id a policy's rules
store, a connector's credential, and a provider's API key along with its tier grants and every
tenant's model assignment. A rename orphans none of them.

Two things a rename does still move; check both before you issue one:

- A rule's actions name their connector by token, so rules pointing at a renamed connector have
  to be re-pointed.
- `renameDeviceProfile` refuses a rename outright once the profile has been published or
  adopted by a device type, because from that point published rules and device rosters name it by
  token.

`updateNotificationPolicy` needed no rename mutation: nothing keys on a policy's token, so you move a
policy by creating the new one and deleting the old.

**A geofence's token is immutable.** `updateGeoFence` used to reconcile two tokens and refuse a
disagreement. Its input now carries no token, so no request can ask for a rename. The reason is
unchanged: detection rules name fences by token inside compiled expressions this service cannot
rewrite, so a rename would leave every one of them naming nothing while the mutation returned
success. If you need a fence under a different token, **create the new one first and delete the old
one after**. Doing it the other way round can forfeit position headroom you are grandfathered on and
leave the fence unrecreatable.

:::note[This changed]
Before this release, token handling on updates was neither uniform nor safe, and both failures
returned success. See [How token handling changed](#how-token-handling-changed).
:::

##### How token handling changed {#how-token-handling-changed}

Most `update*` mutations located the record by the payload token and ignored the argument
entirely. A request naming one entity in `token:` and another in `request.token` silently updated
the second and returned it. The rest honoured the argument but then wrote the payload token over the
stored one, so the payload still moved the record. An empty payload token, which `token: String!`
permits (`""` is a valid non-null String), blanked the record's token and left a live row
addressable by nothing.

A client that relied on the payload naming the record now gets an error rather than writing the
wrong row. A client that sends `token: ""` on an update now gets an error either way, where before it
destroyed the record's identity: the rename mutations refuse it, and on a partial update the schema
rejects it, because the input has no `token` field to send it in. A third set of mutations used to
*ignore* an empty token (that was the "must agree" rule); those have all converted.

### Where the default does not hold {#where-the-default-does-not-hold}

These are the field-level exceptions in the API this release serves. The last two rows describe a
kind of field and give examples rather than listing every one; each field's comment in the
[published schema](#download-the-schemas) says whether it can be cleared. A field that is not an
exception follows the three states above: absent leaves it alone, `null` clears it, a value sets it.

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
| A field the record cannot exist without — for example `type` and `config` on `updateConnector`; `kind`, `model` and `enabled` on `updateAiProvider`; `channelType` and `enabled` on `updateNotificationChannel`; `enabled` on `updateNotificationPolicy`; `tierToken` on `updateTenant`; `redirectUris` and `scopes` on `updateOauthClient`; and the required references and fields listed under [Fields worth knowing about](#two-fields-on-converted-mutations) | **Kept** when omitted. An explicit `null` is refused rather than cleared |
| A field validated together with another — for example `endpoint` on `updateAiProvider`, and `entityGroupToken` / `entityGroupVersion` on `updateDetectionRule` | **Kept** when omitted. A `null` clears it only if the record is still valid with what the other field holds: `endpoint: null` is refused for a provider kind with no default address, and clearing one half of a rule's group scope without the other is refused |

:::danger An empty string does not mean "leave this alone"
For every write-only `secret` field, **`""` deletes the stored credential**, and the mutation returns
success. A client that fills in every field deletes a credential it never meant to touch, and a
connector whose credential is gone fails authentication on every outbound dispatch. **Leave the field
out.** See [Secrets and empty strings](#secrets-and-empty-strings).
:::

#### Secrets and empty strings {#secrets-and-empty-strings}

You cannot read a secret back, so there is nothing to re-send; the API's answer is that omitting it
keeps it. "Read the record, change one thing, send it all back" is the habit a full-replace API
teaches, and clients written against one still do it. Filling in every field means sending
`secret: ""` for a credential you never meant to touch, which deletes it.

`null` deletes the credential too. That is the platform's ordinary meaning of a null rather than an
exception: a null clears the field it names. The inversion these fields used to carry, where
null preserved and only `""` deleted, is gone.

### Which mutations are partial updates {#which-mutations-are-partial-updates}

**All of them.** The conversion arrived one area at a time and is now complete. This section records
what changed in each area, because a client written against the old behaviour needs to know.

**device-management.** Every `update*` takes a dedicated `*UpdateRequest`:

`updateDeviceType` · `updateDevice` · `updateAssetType` · `updateAsset` · `updateCustomerType` ·
`updateCustomer` · `updateAreaType` · `updateArea` · `updateMetricDefinition` ·
`updateCommandDefinition` · `updateDetectionRule` · `updateGeoFence` · `updateEntityGroup` ·
`updateDeviceCredential` · `updateProvisioningProfile` · `updateEntityRelationshipType` ·
`updateDeviceProfile`

**Outbound connectors and AI inference.** Each converted its one update: `updateConnector` and
`updateAiProvider`. Every rename channel those areas' payload tokens carried moved to a
[dedicated rename mutation](#renaming-a-record) rather than being dropped. Both keep an optional
`expectedUpdatedAt`. On both, `type`/`config` and `kind`/`endpoint` respectively are validated as a
pair against the values the record will hold. Naming one of a pair re-checks the stored other,
and a change that would leave the record unusable is refused at the write rather than at first use.

**notification-management.** Both `update*` mutations have converted: `updateNotificationChannel` and
`updateNotificationPolicy`. Two things about the policy are worth knowing before you send one:

- **`rules` is optional, and omitting it leaves the rule set exactly as it is** — the same rows, not
  a rebuilt copy of them. It used to be required, and every update replaced the whole rule set, so an
  edit that only changed a name destroyed and recreated every rule, and an edit that left `rules` out
  emptied the policy and returned success. Whole-replace is still available: send the list. Sending
  `null` or `[]` empties the rule set; for a list those are one request spelled two ways.
- **`deviceTypeToken` is not in the update input at all.** A non-empty value is refused at write,
  because the dispatcher skips a device-type-scoped policy, so accepting one would return success on
  a policy that delivers nothing. That left the field with no request it could accept beyond a no-op.
  It stays on the create input, where the refusal explains itself.

**dashboard-management.** `updateDashboard` takes a `DashboardUpdateRequest` and carries no token at
all. Its one wrinkle is `definition`: the field is nullable so it can be *omitted*, which is how you
rename a dashboard without resending its whole document, but an explicit `null` on it is
refused, because a dashboard with no definition is not a thing. It keeps its optional
`expectedUpdatedAt` precondition. An update that names no field at all writes nothing (not even
`updatedAt`), while a stale precondition on it is still a conflict.

**user-management.** Every `update*` takes a dedicated request too:

`updateRole` · `updateTenant` · `updateTenantTier` · `updateOauthClient` · `updateProfile`

**That is the whole update surface.** No `update*` mutation anywhere takes a `create*` sibling's
input, so this page no longer carries a list for you to check a mutation against. Earlier releases
did, twice: first a roster of the areas that had not converted, then a rule saying the signature was
the authority because two contracts coexisted. Both went stale when their premise expired. What
replaced them is the [three states](#an-update-replaces-the-whole-record) and the
[field-level exceptions](#where-the-default-does-not-hold), which describe the whole API.

:::caution[Check the schema for what a given input declares]
One contract does not mean every input takes every field. Some fields are absent from their
`*UpdateRequest` on purpose, and others accept a value but refuse a `null`. The
[schema you downloaded](#download-the-schemas) is the authority for the first; the
[exceptions table](#where-the-default-does-not-hold), with each field's schema comment, covers the
second. See [What an update input can express](#what-an-update-input-can-express).
:::

#### What an update input can express {#what-an-update-input-can-express}

What an update *can* express is what its `*UpdateRequest` declares. Some fields are deliberately
absent — `deviceTypeToken` on `updateNotificationPolicy`, `memberType` on `updateEntityGroup`,
`credentialType` on `updateProvisioningProfile` — because no request for them would be accepted.
Others accept a value but refuse a `null`; the
[exceptions table](#where-the-default-does-not-hold) describes them, and each one's schema comment
says so. Neither is a question about which contract the mutation is on, because there is only one.

:::note[This changed for user-management]
Four user-management updates used to write every field their input declared. See
[user-management changes](#user-management-changes) before re-running an old client, starting with
`updateTenant`.
:::

#### user-management changes {#user-management-changes}

`updateRole`, `updateTenant`, `updateTenantTier` and `updateOauthClient` used to write every field
their input declared, so a request naming only `name` blanked the rest and returned the emptied
record. Re-check `updateTenant` first: omitting a governance override used to **erase** it, so
renaming a tenant removed every ceiling an operator had set. Omitting one now leaves it alone, and
only an explicit `null` removes it.

`tierToken` on `updateTenant` became optional. Omitting it keeps the tenant at its current tier;
an explicit `null` is refused, because every tenant has a tier.

`authorities`, `redirectUris` and `scopes` became nullable lists (`[String!]`, not `[String!]!`),
so they now have an absent state:

- Omitting one leaves it alone.
- Sending a list replaces it wholesale.
- `null` and `[]` both mean "empty".

A role's authorities may be emptied, because a role that grants nothing is a thing you can
create. An OAuth client's redirect URIs and scopes may not: an empty redirect allowlist matches
nothing, so the client could never complete an authorization.

`updateProfile` now takes `request: ProfileUpdateRequest!` instead of bare `firstName` / `lastName`
arguments. Its behaviour is unchanged: it writes only the names you send, and `""` still clears one.

#### Fields worth knowing about on the converted mutations {#two-fields-on-converted-mutations}

- **A required reference cannot be cleared.** `updateAsset`'s `assetTypeToken`, and its peers on
  devices, customers and areas, re-point the entity when you send one and leave it alone when you do
  not. An explicit `null` is refused, because "no type" is not a state those entities can be in.
  An unknown token is refused too, and the refusal is total: nothing is written.
- **`updateDeviceType`'s `profileToken` *can* be cleared**, because a device type with no
  [device profile](../concepts/domain-model.md) is a real thing. Under the old full-replace shape,
  omitting it while renaming a type **detached the profile**, which silently un-declared position for
  every device built on the type and returned success. Omitting it now keeps the current profile;
  `null`, or an empty token, detaches it. A detection rule's optional `entityGroupToken` can be
  cleared too, but only as a pair: send `null` for both `entityGroupToken` and `entityGroupVersion`.
  Clearing one and leaving the other set is refused, because a scope needs both.
- **A required field cannot be cleared either, even when it is not a reference.** A metric's
  `dataType`, a credential's `credentialType` and `enabled`, a rule's `definition` and `enabled`, a
  fence's `geometry`, a provisioning profile's `provisionKey` and `provisionSecret`: send a value to
  change one, omit it to leave it alone, and an explicit `null` is refused. The failure this
  prevents is invisible: folding `enabled: null` to `false` would disable a credential or park a rule
  and return success, and `false` is a value you could legitimately have sent.
- **Omitting a secret now keeps it.** `credentialValue` on `updateDeviceCredential` and
  `provisionSecret` on `updateProvisioningProfile` used to be blanked by any update that failed to
  restate them. That took a device, or a whole self-registering fleet, offline at its next
  connection, with a `200` on the edit that broke it.

`metadata` is replaced wholesale when you send it, and cleared by `null`. It is an opaque JSON string
in the schema rather than a map, so there is no per-key merge to choose between: the API has never been able to address an individual key.

## Input validation {#input-validation}

**An input field the schema does not define is rejected.** Sending an undeclared field fails the
whole request with an error that names the offending field and suggests the declared field you
probably meant:

```json
{
  "errors": [{
    "message": "Variable \"request\" has invalid value.\nField \"deviceProfileToken\" is not defined by type \"DeviceTypeCreateRequest\". Did you mean \"profileToken\"?"
  }]
}
```

This holds whether the value is written as a literal in the query or supplied through a variable.

It matters more than a typo check. A silently discarded field is indistinguishable from one that was
applied: the mutation returns success, and you get a partially configured entity with nothing to
indicate a value went missing. Rejecting it is what makes a success response mean the whole input
was understood.

### What a token may contain {#what-a-token-may-contain}

Every entity token, and every tenant id, must match:

```
^[A-Za-z0-9][A-Za-z0-9_-]*$
```

That is letters (either case), digits, hyphens and underscores, starting with a letter or a digit,
and at most 128 characters. Anything else is refused at write, on create *and* on update, before
anything is stored.

This is a security rule rather than a house style, which is why it is this narrow. A token is
spliced into infrastructure namespaces: a tenant id becomes a segment of a NATS subject recovered by
splitting on `.`, and a device token becomes a segment of an MQTT topic. A `.` shifts subject
segments, and `*`, `>`, `+` and `#` inject wildcards that match **across tenants**. Uppercase is
allowed deliberately, because machine-supplied identifiers like device serials and VINs are usually
uppercase.

The identifiers integrators reach for first are the ones this rejects: `sensor.001`, a MAC address
`AA:BB:CC:DD:EE:FF`, `plant/line-2`, anything with a space. **Put those in `externalId` instead.** It
is opaque, has no format constraints, and is unique within a tenant when present. Give the entity a
token you choose and keep the device's own identifier alongside it.

The console mints tokens for you from a per-entity-type template, so this rarely comes up there. It
bites first on the API and in scripted provisioning.

### A value that must be unique {#unique-values}

Some values must be unique: a token within its tenant, a device's `externalId`, a command key
within a profile, an identity's email, one membership per identity and tenant. A create, update or
rename that would repeat one is refused, and the error carries `extensions.code` set to `CONFLICT`:

```json
{
  "errors": [{
    "message": "the request conflicts with an existing record: a value that must be unique is already in use",
    "path": ["createDeviceType"],
    "extensions": { "code": "CONFLICT" }
  }]
}
```

Branch on the code, not on the message. Where the service has more to say, the message is its own
sentence instead, for example a rename onto a token already in use, and the code is the same. The
database's own wording for the collision is replaced by the sentence above, so the message does not
name a database index or column. A service's own sentence may repeat the token you sent.

`CONFLICT` means the write collided with a value that must be unique. That is usually a value you
sent, but it can be one the server assigns during the write, such as the next version number when
two publishes of the same record run at once, and then a retry succeeds. So the code does not by
itself mean that the record you asked for already exists. It means that only where the one unique
value involved is yours, such as the token of a tenant you are creating.

Some refusals look similar and do not carry `CONFLICT`:

- A save refused because the record changed since you read it ("modified by another writer; reload
  and try again") is a stale write, not a duplicate.
- A deleted tenant's token is reserved until the deletion finishes. Creating a tenant at it is
  refused without `CONFLICT`, because the token is not held by a tenant you could use.
- Creating a command with a token already in use is not refused at all: you get the original
  command back. See [Issue a command](../guides/sending-commands.md#issue-it).

## Request limits {#request-limits}

Every GraphQL endpoint refuses a request that is too large or does too much, before any of it runs.
The one exception is the limit on credential checks, which applies while the request runs (see
[Credential checks per request](#credential-checks-per-request)).

The limits are the same for every service, and you can change each one per service with the
environment variable shown. A value that is missing, not a number, or below 1 falls back to the
default: none of them can be switched off.

| Limit | Default | Variable | What is refused |
| --- | --- | --- | --- |
| Request body | 4 MiB | `DC_GRAPHQL_MAX_BODY_BYTES` | The whole HTTP body, including variables. Answered with HTTP 400. |
| Query length | 100,000 bytes | `DC_GRAPHQL_MAX_QUERY_LENGTH` | The query string itself. |
| Nesting depth | 15 | `DC_GRAPHQL_MAX_DEPTH` | Selections nested deeper than this. |
| Root fields per query | 20 | `DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS` | A query operation selecting more top-level fields than this. |
| Root fields per mutation | 5 | `DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS` | A mutation operation selecting more top-level fields than this. |
| Credential checks per request | 1 | `DC_GRAPHQL_MAX_CREDENTIAL_CHECKS` | Password checks in one request beyond this number (see [below](#credential-checks-per-request)). |

Apart from the body limit and the credential-check limit, a refused request gets HTTP 200 with a
single entry in `errors`, no `data`, and nothing executed. The credential-check limit refuses only
the checks over it, each with its own error, and the rest of the request still runs. A root-field
refusal carries `extensions.code` set to `TOO_MANY_ROOT_FIELDS`:

```json
{
  "errors": [{
    "message": "mutation (anonymous) selects 6 root fields; the maximum is 5",
    "extensions": { "code": "TOO_MANY_ROOT_FIELDS" }
  }]
}
```

**Root fields are counted by response key:**

- Every alias counts as a field of its own.
- Fields reached through a fragment count as if they were written out.
- Repeating the same key is one field.
- `@skip` and `@include` are not evaluated, so a conditional field counts whether or not it runs.
- Every operation in the document is counted, not only the one `operationName` selects.
- The rule applies over WebSocket as well as HTTP.

The mutation limit is the tight one because mutation fields run one after another. Without it, a
single request could carry hundreds of aliased copies of an expensive mutation. The console, the
dashboard app, the SDKs, `dcctl` and the MCP server send one mutation field per request and at most
two query fields. The limit applies to top-level fields only; aliases of a nested field are not
counted.

### GraphQL syntax only {#graphql-syntax-only}

**Documents must use GraphQL's own syntax.** A document containing any of the following is refused
with a syntax error, and nothing runs:

- A `//` or `/* */` comment. Use `#` for comments.
- A backquoted string, or a single-quoted character.
- A block string whose closing `"""` directly follows a backslash (the `\"""` escape). That escape is
  valid GraphQL, but the server has never read it as the specification defines it, so send such text
  in a variable instead.
- A string directly followed by a quote, such as `"x""y"`. GraphQL reads it as two adjacent strings,
  and the server used to read it as the start of a block string. Only `"""` opens a block string.

The same rule applies over WebSocket, where a subscription the server cannot read gets the syntax
error rather than the subscriptions-only message.

### Credential checks per request {#credential-checks-per-request}

One request can have only a limited number of passwords checked, however it is written: one by
default. The `login` fields up to that number are evaluated as usual. Any further `login` in the same
request, such as another alias, is not evaluated: the password is not checked, nothing is looked up,
and nothing is recorded in the audit log. It gets its own error instead of a verdict on the password,
and the other fields in the request still return their data:

```json
{
  "errors": [{
    "message": "this request has already made its credential checks; send one sign-in per request",
    "path": ["a2"],
    "extensions": { "code": "TOO_MANY_CREDENTIAL_CHECKS" }
  }]
}
```

This does not depend on how the document is written, so it still holds for a document that gets past
the root-field limit. The refusal happens before the email address is looked at, so it is the same
whether or not an account exists. Every client DeviceChain ships sends one sign-in per request, so
none of them is affected.

`DC_GRAPHQL_MAX_CREDENTIAL_CHECKS` raises the number per service; like the other limits, it cannot
be switched off. Refusals are counted on `devicechain_usermanagement_credential_checks_total` with
`outcome="request_budget"`.

### Sign-in backoff {#sign-in-backoff}

Failed password sign-ins slow down further attempts on the same email address. This applies to
`login` and to the OAuth sign-in form.

- The first five failed attempts on an address are evaluated straight away.
- After that, the address waits 1 second before its next attempt is evaluated, then 2, then 4,
  doubling up to 5 minutes.
- A successful sign-in resets the count, and so does a quiet spell of 10 minutes after the last
  attempt that was evaluated.

The count belongs to the address that was typed, whether or not an account exists for it, so the
delay does not reveal which addresses are registered. It is shared by every replica of the service.

:::warning Someone who knows an address can keep its owner out
While someone keeps sending wrong passwords for an address, its owner is refused as throttled even
with the correct password. It ends when they stop: after at most one wait of up to 5 minutes, the
owner can sign in again. See [Holding an address at the backoff](#holding-an-address-at-the-backoff).
:::

#### Holding an address at the backoff {#holding-an-address-at-the-backoff}

The count is kept per address, not per address and network location, so that an attacker cannot get
a fresh allowance by spreading guesses across many machines. The cost is that anyone who knows an
email address can keep sending wrong passwords for it. While they keep that up, each evaluation slot
goes to them, and the owner is refused as throttled even with the correct password. It is not a
permanent lockout. The `devicechain_usermanagement_credential_checks_total` metric, with
`outcome="throttled"`, shows when an account is being held this way.

#### OAuth client secrets {#oauth-client-secrets}

**OAuth client secrets are not slowed down.** Client secrets created by the admin API are 256 random
bits, which no number of guesses will find, and a client ID is public: it appears in every
authorization URL. A backoff on client secrets would not protect them. It would let anyone hold a
confidential client, and so every sign-in through it, at the backoff. A client you seed from
configuration should have a secret just as strong. Public clients have no secret at all.

#### Attempts during the wait {#attempts-during-the-wait}

An attempt made during the wait is not evaluated at all: the password is not checked and nothing is
recorded in the audit log. It is reported as its own error rather than as a wrong password, because
the password may well have been right:

```json
{
  "errors": [{
    "message": "too many failed sign-in attempts; try again in 8 seconds",
    "path": ["login"],
    "extensions": { "code": "THROTTLED", "retryAfterSeconds": 8 }
  }]
}
```

If the service cannot reach the store that keeps these counts, it refuses to check passwords at all
rather than check them without counting. The `login` error then carries `extensions.code` set to
`UNAVAILABLE`. Treat it as an outage, not as a rejected credential. The OAuth token endpoint does not
use the store, so client authentication keeps working.

#### When the attempt store is full {#when-the-attempt-store-is-full}

The store has a fixed size, and every address that is tried takes a place in it for 10 minutes,
whether or not an account exists for it. Someone sending sign-ins for enough different addresses can
fill it.

When it is full, sign-in keeps working. Passwords are still checked and answered normally, but new
failures are not counted, so addresses that are not already waiting are not slowed down until old
entries expire. An address that is already waiting stays waiting, but only until that wait ends,
which is at most 5 minutes. After that its failures are not counted either, so an account that is
being attacked while the store is full is not protected by the backoff.

This is deliberate. Refusing every sign-in instead would let anyone who can fill the store lock every
user out of the instance. Guessing is still limited by the cap on fields per request and by the cost
of each password check.

Each attempt checked this way is counted by `devicechain_usermanagement_credential_checks_total`
with `outcome="store_full"`. When the chart's alerting rules are enabled, the
`CredentialAttemptStoreFull` alert fires when there are any. The user-management log also carries a
warning at most once a minute while it lasts.

When the alert fires, someone is most likely trying many addresses:

1. Find where the sign-in traffic comes from and block it upstream.
2. If the traffic is legitimate, raise `instance.config.infrastructure.nats.kvStateMaxBytes`. That
   size applies to every state bucket, so make sure the JetStream volume has room for the increase.

Detailed, per-type reference pages will be generated from the schemas as they stabilize.
