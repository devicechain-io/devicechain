---
sidebar_position: 3
title: Releases & Upgrades
---

# Releases & Upgrades

DeviceChain ships as a set of prebuilt, versioned container images plus a Helm chart.
You do **not** need to build anything to run it — pull a released version, install the
chart, and upgrade in place with zero downtime.

:::warning v0.9.0 and v0.10.0 cannot be upgraded into
Two releases so far require recreating the instance rather than upgrading it:

- **`v0.9.0`** replaced every service's migration chain with a single frozen baseline, so a
  `v0.8.x` database meets it and fails on `already exists`. See
  [The v0.9.0 baseline squash](#v090-baseline-squash).
- **`v0.10.0`** changed the primary key of the event tables to fix a defect that was
  silently discarding telemetry. See [The v0.10.0 event key change](#v0100-event-key).

If you are on either earlier version, read the matching section below before you do
anything else.
:::

:::caution Upgrading to v0.12.0 needs a few changes first
`v0.12.0` upgrades in place, but it changes the topic a device answers a command on, moves
one permission, and changes several things whose shape stayed the same. A `helm upgrade`
will report success either way. Read
[v0.12.0 — an upgrade that changes contracts](#v0120-upgrade) before you start.
:::

## Versioning model

Every release is a single semantic-version git tag (`vX.Y.Z`). That one version covers
**everything together** — each service image, the operator, the Helm chart, and the
`dcctl` CLI are all published at the same version. There is no per-service version skew to
reason about: a deployment is one coherent number.

Keeping it that way takes two commands rather than one, because the operator is not part of
the chart — see [Zero-downtime upgrades](#zero-downtime-upgrades) for the procedure.

- **Stable releases** are `vX.Y.Z` (e.g. `v1.2.0`). The `:latest` tag tracks the most
  recent stable release.
- **Pre-releases** are `vX.Y.Z-rc.N` (e.g. `v1.2.0-rc.1`). These never move `:latest`.

## Pre-1.0 stability {#pre-10-stability}

:::warning DeviceChain is pre-1.0

Until **v1.0.0**, any release — including a patch release — may change APIs, schemas, or
behavior without a compatibility shim. This is deliberate: while the data model is still
settling, we prefer a clean cutover to carrying a shim we would have to support forever.

**Every breaking change is called out at the top of that release's notes. Read them before
upgrading.** They are the authoritative list; the version number alone does not tell you
whether a release is safe for your deployment.

:::

Concretely, before v1.0.0 you should expect that a release may:

- **tighten validation**, so a request that previously succeeded is now rejected — usually
  because it was being silently accepted or silently discarded
- **change or remove a GraphQL field**, rather than deprecating it for a cycle
- **alter database schema** in ways that a downgrade will not undo
- **replace the migration baseline outright**, which removes the upgrade path entirely rather
  than merely making it one-way. When that happens the release notes say so at the top, and
  the only route forward is to recreate the instance. `v0.9.0` and `v0.10.0` are such releases

The "upgrade in place with zero downtime" property above describes the *mechanics* of a
rolling upgrade. It is not a promise that your existing API calls keep the same meaning
across a pre-1.0 version bump.

Once v1.0.0 ships, this section is replaced by a normal semantic-versioning compatibility
promise: breaking changes only in a major version.

Because releases are frequent before GA, the **minor** version marks a milestone (a
significant feature or subsystem landing) and the **patch** version carries the ongoing
cadence of fixes and hardening. A patch release is not automatically a low-risk upgrade
during this period — again, the release notes are what tell you.

## Images

Images are published to the public GitHub Container Registry under
`ghcr.io/devicechain-io` — for example `ghcr.io/devicechain-io/device-management`. They are
multi-arch (`linux/amd64` and `linux/arm64`) and built on a distroless nonroot base, so
they run as an unprivileged user with no shell and a minimal attack surface.

Because the registry is public, no credentials are required to pull released images.

## Installing a specific version

Pin the image tag to the release you want:

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

The Helm chart itself is also published as an OCI artifact, so you can install it without a
checkout of the repository:

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version 1.2.0 \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

The chart is also listed on
[Artifact Hub](https://artifacthub.io/packages/helm/devicechain/devicechain), which
shows every published version alongside its default values and rendered templates.

## Zero-downtime upgrades {#zero-downtime-upgrades}

Upgrading to a new version is **two commands**, and the chart and services are built to roll
customers forward without dropping traffic. Three releases so far are exceptions, all
documented below: the durable-ingest cutover, which is still an ordinary upgrade but has a
visible side effect, and **`v0.9.0` and `v0.10.0`, which cannot be upgraded into at all**.
Check the release notes for the version you are moving to before running them:

```bash
# 1. The services. Carry the current release's values forward, then change only
#    the version. This file contains your instance's secrets — delete it when done.
helm get values dc -n default -o yaml > dc-values.yaml

helm upgrade dc deploy/helm/devicechain \
  -n default \
  -f dc-values.yaml \
  --set image.tag=v1.3.0

rm dc-values.yaml

# 2. The operator. Not part of the chart, so `helm upgrade` cannot move it.
dcctl upgrade local devicechain --version v1.3.0
```

:::warning Both steps, every time — the second one is not optional
The operator (its CRDs, RBAC and controller) is **not installed by the Helm chart**. `dcctl`
applies it from manifests embedded in the binary, so `helm upgrade` has no way to reach it,
and an upgrade that stops after step 1 leaves your instance running the new services against
the controller it was first bootstrapped with — indefinitely, and with no error to tell you.

`dcctl upgrade` touches nothing else. It does not run the Helm upgrade, does not apply the
infrastructure stack, and **does not generate, read or rotate any credential**, so it is safe
on a live instance. Use it rather than re-running `dcctl bootstrap`, which *does* rotate every
generated credential.

Pass the same version to both steps. Run `dcctl upgrade` with `--dry-run` first if you want to
see exactly which objects it would move.
:::

:::warning Carry the values forward — `--set image.tag=…` on its own will not work
Your instance's values are not all typed by hand. `dcctl bootstrap` generates several and
stores them in the release — among them the instance root key, the cross-service auth secret,
the NATS service credential and callout issuer seed, and the broker's CA.

Helm's rule is the trap here. An upgrade that passes **no** values at all reuses the ones
already in the release. But the moment you pass *any* value — including the single `--set`
that changes the version, which is the whole point of an upgrade — Helm starts from the
chart's defaults instead, and everything `dcctl bootstrap` generated is gone.

Nothing is corrupted when that happens, because the chart refuses to render without the root
key:

```
Error: UPGRADE FAILED: execution error at (devicechain/templates/instance-config.yaml:27:4): instance.config.infrastructure.secrets.rootKey is required: area "notification-management" owns an envelope-encrypted secret store and cannot form its KEK without it, so it would crash-loop. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically.
```

The fix is the `helm get values` step above. `--reuse-values` also works, but it silently
keeps stale entries when the chart's own defaults move between versions, so prefer writing the
values out and passing them with `-f`, where you can see them.
:::

What makes the rollout safe:

- **Surge-before-terminate.** Each Deployment uses a `RollingUpdate` strategy with
  `maxUnavailable: 0` and `maxSurge: 1`, so a new pod must pass its `/readyz` readiness
  probe **before** an old pod is removed. Capacity never dips during the rollout.
- **Graceful shutdown / connection draining.** When a pod is asked to terminate it first
  reports "not ready" (so the Service stops routing new requests to it), waits a short
  drain window for that change to propagate, and only then finishes in-flight work and
  shuts down. Configure the window with `shutdownDrainSeconds` (default `5`), kept safely
  under `terminationGracePeriodSeconds` (default `30`).
- **Coordinated schema migrations.** Services run database migrations under a database-level
  lock, so when several replicas start at once exactly one applies migrations and the rest
  wait — no races, no duplicate DDL.

:::tip Run at least two replicas in production
For true zero-downtime, run `replicas: 2` (or more) for each area so the rollout always has
a live pod serving traffic. A single replica still has a brief gap while its one pod is
replaced. Set it globally with `--set replicas=2`, or per area under
`functionalAreas.<area>.replicas`. A `PodDisruptionBudget` is rendered automatically for any
area with more than one replica, so node drains can't evict every replica at once.
:::

### The v0.9.0 baseline squash {#v090-baseline-squash}

`v0.9.0` is the **first** of the two releases that cannot be reached with `helm upgrade`
(the other is [`v0.10.0`](#v0100-event-key)).

Before it, each service's schema was built by a chain of migrations applied in order. `v0.9.0`
replaces every one of those chains with a **single frozen baseline** — one migration per
service that creates the whole schema as it stands. A database created by `v0.8.x` has already
applied the old chain, so when it meets the baseline it tries to create tables that are
already there and fails with `already exists`. The failure is loud and happens at startup; it
does not corrupt anything.

There is no migration path, and before `v1.0.0` there will not be one. Carrying a compatibility
shim for a schema shape that is still moving is exactly the cost this project has chosen not to
take on while every install is still an early one.

**To move to `v0.9.0`, recreate the instance:**

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain
dcctl bootstrap local devicechain
```

:::caution Export first — recreation discards your data
The [destroy guard](#data-durability) protects the databases from an ordinary `helm` operation,
not from a deliberate `dcctl destroy`. If the instance holds telemetry, device definitions or
dashboards you care about, dump them before you start. There is no in-place path that preserves
them across this release.
:::

A schema change normally **appends** a new migration to the baseline, which is an ordinary
in-place upgrade. That is the rule, and it holds for almost every release.

:::note This section once promised it would never happen again
It said the squash described "a single release, not a new policy". Then `v0.10.0` needed a
recreate too, for an unrelated reason. The honest version of the rule is: appending is the
norm, and before `v1.0.0` a release may still require a recreate when a defect cannot be
fixed any other way. **Any release that does will say so in its notes and here.** Check both
before upgrading rather than assuming from the version number.
:::

### The v0.10.0 event key change {#v0100-event-key}

`v0.10.0` is the second release that **cannot be reached with `helm upgrade`**, for a
different reason from the squash.

An event was identified by the combination of its tenant, device, type and timestamp. That
combination is not unique: a device that samples two sensors and publishes each as its own
message under one shared timestamp produces two genuinely different events that look
identical to the database. The second one was silently discarded — its readings were stored
against the first event's record, and once dropped it could never be recognised as a repeat,
so every later retry of that message added another copy of its readings.

Any device stamping whole seconds could hit this by emitting twice in one second, and the
published .NET SDK stamped whole seconds until this release.

`v0.10.0` gives every event, every reading and every relationship record an identity derived
from its own content, and makes that identity the key. Storing telemetry correctly means
changing the primary key of the largest tables in the system, and those tables are
compressed — a database engine will not alter a key on compressed data in place. There is no
upgrade path that preserves the existing rows.

**To move to `v0.10.0`, recreate the instance:**

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain
dcctl bootstrap local devicechain
```

The same caution applies as above: recreation discards your telemetry, device definitions
and dashboards. Export anything you need before you start.

Two changes to how the API reports time come with it, and neither needs any action:

- Timestamps are now returned at the precision they were recorded — previously they were
  rounded down to the whole second on the way out, so two readings 200 milliseconds apart
  came back looking simultaneous. Whole-second timestamps are unchanged on the wire.
- Requests that use a record's `updatedAt` value to avoid overwriting someone else's edit
  are now checked at that same precision. Two edits inside one second could previously both
  pass the check, and the later one silently overwrote a change it had never seen.

### v0.11.0 — a normal upgrade again {#v0110-upgrade}

`v0.11.0` is the first release since `v0.8.5` that can be reached with `helm upgrade`. Its
schema change **adds** three migrations rather than replacing a baseline, so an existing
`v0.10.0` database is carried forward with its rows intact instead of having to be recreated.

What lands in the database:

- two new tables recording the progress and history of a tenant deletion, and
- two columns on the tenant table tracking its lifecycle state.

Every tenant that already exists is set to the normal, active state as the column is added,
so nothing changes for a running instance until you actually delete a tenant.

:::note What was tested, and what was not
Two checks were run before release, and they are worth keeping apart because they measured
different things.

**The migrations, against a database with data in it.** A `v0.10.0` schema was built, filled
with representative rows, and carried forward. Every one of those rows came through
byte-identical, and the resulting schema is identical to a fresh `v0.11.0` install — for every
functional area, not only the one that changed.

**The upgrade itself, on a running instance.** A `v0.10.0` instance was built from the
published `v0.10.0` images, given real tenants and identities, then upgraded with the command
above. Every service rolled out, row counts across all 67 tables were unchanged apart from the
new migration entries and the audit records they wrote, signing in still worked for an account
created under `v0.10.0`, and the new tenant-deletion API answered on the upgraded instance.

Four limits, stated plainly:

- **Only the databases were verified.** Broker (JetStream) state, object storage and key-value
  state are not covered by either check.
- **PostgreSQL 16 only.** Fresh installs are verified on both supported majors; the upgrade
  path itself was measured on 16.
- **The row-by-row comparison came from the first check, not the second.** The running instance
  was verified by row *counts*, which would not notice a row altered in place rather than
  removed.
- **The web console was left at its `v0.10.0` image** during the second check, so the `v0.11.0`
  console was not exercised against an upgraded instance.
:::

### v0.12.0 — an upgrade that changes contracts {#v0120-upgrade}

`v0.12.0` is reachable with `helm upgrade`. Its schema change **adds** migrations rather
than replacing a baseline, so an existing `v0.11.0` database is carried forward with its
rows intact, and this was measured on a running instance rather than reasoned about.

What it does change is **contracts** — the MQTT topic a device answers a command on, a
handful of GraphQL operations, and the meaning of several things whose shape did not
change at all. None of that is visible in a `helm upgrade` that reports success, so read
this section before you run it.

#### Do these before you upgrade

**1. Update any device that answers commands.** The topic a device publishes a command
response to is now scoped to that device:

```
# before
{instanceId}/{tenant}/command-responses
# now
{instanceId}/{tenant}/command-responses/{deviceToken}
```

The old topic is no longer permitted by the credentials a device is issued, so a device
that is not updated will have its responses refused at the broker — it will still receive
and act on commands, but the platform will never record that it did, and every one of them
will eventually read as timed out.

The reason for the change is that the old topic let **any** device in a tenant publish a
response naming **any** command, including one issued to a different device. Nothing in
the response said who sent it, so nothing could tell. The device token is now part of the
topic, which is part of what the broker signs, so a device can only answer for itself.

Upgrade the devices first if you can. Responses sent on the old topic during the changeover
are refused, not queued, and a small number of responses already in flight at the moment of
the upgrade are dropped rather than delivered.

**2. Rename an event source whose id is exactly `lwm2m`.** That value is the one the LwM2M
service files its own device presence under, and presence records are matched by exact
equality — so your source and that service overwrite each other's rows. `event-sources`
now refuses to start on it, which stops all ingest for the instance.

An id that merely *reads* as a transport, such as `sparkplug:plant-a` or `lwm2m:site-a`,
now starts with a warning instead of refusing. Rename those when convenient. In both cases
note the trap in renaming: the presence already recorded under the old id is not carried
over, and nothing backfills it.

**3. Check who reads location history.** The queries that return device positions now
require the `location:read` permission rather than `event:read`. This permission is not in
the read-only baseline a viewer receives, so an account that could read position history on
`v0.11.0` cannot on `v0.12.0`. Grant it explicitly to the roles that need it.

The same permission now also gates **previewing a rule that tests geofence containment**.
A preview of that kind returns, per device, when it entered and left a region — a read of
position however it was asked for — so `previewRule` requires `location:read` in addition
to the `device:read` every preview takes. A rule author who could preview every draft on
`v0.11.0` is refused on containment drafts until that permission is granted. Previews that
test no containment are unaffected.

**And check who reads it through an AI assistant.** The MCP server gained a `query_locations`
tool that returns a device's reported positions, and reaching it takes **two** grants that are
deliberately kept apart. The agent's authorization must include a new `location` OAuth scope,
*and* the person who authorized it must hold a role granting `location:read`. Neither alone is
enough: the scope is a ceiling on what a token may carry, not a grant of anything.

An agent authorized for `read-only` alone cannot read position, however much its user holds.
That is the point of a separate scope rather than a wider `read-only`: the consent screen shows
a person the raw scope string, so folding position into `read-only` would have meant an
authorization that looks identical before and after while now including where devices — and,
often enough, where the people carrying them — have been. Keeping it separate means granting an
agent observability is not the same act as granting it location history, and a user can allow
one while withholding the other.

An MCP client you have already registered will keep working and will keep being refused
position until its authorization request asks for `read-only location` and the user
re-authorizes it. The viewer baseline is unchanged: `location:read` is still not something a
member receives by default. See [AI Access (MCP)](../concepts/mcp.md).

**4. Check for these GraphQL operations** in anything you have written against the API:

| Operation | What changed |
| --- | --- |
| `createCommand` | Returns `CreateCommandResult!` instead of `Command!`. The command is now under a `command` field, alongside a `rejection` field that explains a refusal. |
| `updateDeviceType` | Its `request` argument is now a required `DeviceTypeUpdateRequest!`, and the semantic changed with it: this is a **partial update**. An omitted field now KEEPS its stored value instead of erasing it, and an explicit null clears it. So a client that cleared a field by leaving it out must now send null for it — and, in the other direction, renaming a type no longer detaches the profile its devices resolve capabilities through. A client written against the old whole-record behaviour — one that reads the type, then sends every field back — still works and still writes what it sends. `token` is also gone from the input, so an update can no longer move a type's token. Unrecognised fields in the request are rejected rather than ignored. |
| `assertedActiveDeviceStates` | Replaced by `assertedDeviceStates`, which takes `activeOnly` and pages through `afterId` and `pageSize`. |
| `deviceCredentials`, `deviceCredentialsById`, `deviceCredentialsByToken` | Now require `device:write`. For one credential type the readable identifier *is* the bearer token, so `device:read` — which every enabled member holds — was enough to open a broker session as any device in the tenant. |
| `locationEvents` | Now requires `location:read`, as above. |
| `geoFenceSetSnapshot`, `currentGeoFenceSet` | Their `fences` field is now paginated: it takes a required `pagination` argument and returns `results` alongside a `pagination` record, instead of a plain list. Read pages until `pageEnd` reaches `totalRecords`. A fence set at the documented limits is larger than a single response can carry, so the list form could not be returned at all for the tenants most likely to ask for it. |
| Any `...ById(ids: [])` query | An empty id list now returns nothing. It used to return the whole table, unpaginated. |

**5. Stop zero-padding entity ids.** An `id` argument is now parsed as a decimal number
and nothing else. It used to be parsed with the base inferred from the literal, so a
zero-padded `"017"` — exactly what a client that formats ids to a fixed width sends — was
read as **octal** and resolved to row 15: the wrong entity, returned successfully with no
error to notice. `"0x2"`, `"0b101"` and `"1_0"` were accepted the same way. All four forms
are now refused outright. Send `"17"`.

**6. Expect every service pod to roll, once.** The instance configuration document the
services are handed now has the coordinate for any functional area this deployment did not
enable removed from it. On a deployment without `ai-inference` — which is every profile
except `full` — that changes the document's bytes and therefore the checksum annotation
that rolls pods, so the `helm upgrade` restarts every service rather than only the ones
whose image moved. It is a normal rolling update and needs nothing from you; it is here so
that a full roll does not read as a symptom.

The reason for removing the coordinate is that a hostname for a service nobody deployed
was worse than no hostname: the rule authoring surface built its natural-language
"Describe" door against it, failed to resolve the name, and reported that the tenant had
not consented to external AI routing — blaming a tenant setting for a service the operator
never installed. It now says the feature is not enabled on this deployment, which is true.

#### Changes with no signature change

These are the ones a client cannot detect by looking at the schema.

**Updating a device profile clears its location declaration.** A profile can now declare
that its devices report position, and `updateDeviceProfile` replaces the whole profile. A
client written against `v0.11.0` does not send the new field, so updating a profile for any
reason — renaming it, editing its description — silently un-declares position for every
device on it. The only symptom is that map surfaces go quiet. Send the field, or set the
declaration again after any update from an older client.

**Windowed detection rules no longer count buffered readings from outside their window.**
Repeating, sliding-aggregate and correlation rules used to fold in a reading from any point
in the past, which let a rule reading "three readings within ten seconds" fire on readings an
hour apart — a store-and-forward device uploading its buffer was the usual trigger. Those
rules now discard a reading that arrives after the window it belonged to has passed, as
tumbling-window and session rules already did. Expect **fewer** alarms from those rule kinds
on any fleet that uploads in batches, and check `detect_late_samples_total` to see how much
is being discarded. Readings are stored and charted exactly as before; this affects
detection only. See [running the detection
engine](./detection-engine.md#timing-what-when-means).

**Geofence geometry is validated more strictly, and stored as written rather than as sent.**
Three changes, all at the point a fence is created or updated:

- A position must be exactly `[longitude, latitude]`. A third or later ordinate used to be
  accepted and ignored.
- The geometry document may carry only the keys the platform reads — `kind` and `geometry`
  at the top level, `type` and `coordinates` inside it. Any other key used to be stored and
  never looked at.
- Coordinates are rewritten into plain decimal notation before being stored. A coordinate
  sent as `1e-300` comes back as its full decimal expansion. No value is rounded and no
  fence changes shape, but a document read back is not byte-identical to the one sent.

A fence is also now refused if its stored form exceeds 32 KiB. That is roughly twice the
size of a fence using every vertex the platform allows, so ordinary geometry is unaffected;
what it refuses is a document whose size comes from notation rather than from shape. The
console has always written positions in the accepted form, so fences drawn in the console
are unaffected. Existing stored fences are **not** rewritten and keep working — but one that
breaks a rule above will be refused the next time it is saved.

**Cancelling a command records `CANCELLED`.** It used to record `EXPIRED`, which it shared
with a command that simply ran out its time. If you branch on `EXPIRED` to detect your own
cancellation, it will no longer be there.

**Commands can now sit in `HELD` or `PARKED`.** A command addressed to a device the
platform knows is absent is held rather than published, and one that was dispatched to a
device that turned out to be unreachable is parked. Both are waiting, not finished, and
both are new — code that treats anything other than `QUEUED` or `SENT` as terminal will get
this wrong. The full set is now `QUEUED`, `HELD`, `SENT`, `PARKED`, `SUCCESSFUL`, `FAILED`,
`TIMEOUT`, `EXPIRED`, `CANCELLED`.

**A reading is stored at the instant it was taken.** When a message carries many samples,
each with its own timestamp — every Sparkplug and LwM2M upload does, and so does any device
that buffers while offline — those samples used to be stored at the instant the message
arrived. They are now stored at their own. A device uploading an hour of buffered readings
writes them across that hour rather than at the moment of upload, so history, charts,
retention and detection all see them where they actually belong.

**A presence source that stops running now hands its devices back.** A device marked `ASSERTED` used
to keep whatever presence it last had, indefinitely: the inactivity sweep skips asserted devices and
a data event cannot flip one, so a device that was connected when its source went away read connected
forever, and one that was offline had its commands held forever. Broker-asserted MQTT presence now
releases the devices it asserted when it is deliberately disabled, or when its NATS system-account
credential is missing — returning them to `INFERRED` without asserting anything about connectivity.
On an instance where that applies, expect one state-change event per device, paced, counted under
`presence_events_total{state="demoted"}`, and expect those devices to come back under the ten-minute
inactivity sweep. Sparkplug and LwM2M have no automatic release: `dcctl presence demote` and
`device-state`'s new `demoteAssertedPresence` mutation do it by hand, for any source. The mutation
needs a new `state:demote` permission that no role holds by default. A new gauge,
`presence_tap_off{reason}`, reports whether broker-asserted presence is running at all — something
nothing reported before, because a quiet fleet and a tap that never started look identical from
outside. See [returning a device to inferred presence](./edge-services.md#demoting-a-device).

**A redelivered reading no longer duplicates its rows.** A measurement event's identity is
derived from a digest of its own content, and that identity is what makes a redelivery
harmless. For a reading carrying more than one metric over a JSON transport, the digest was
computed over an order the platform invented rather than one the device sent, so the same
reading resolved to a different identity roughly four times in five. When the platform
redelivered such a message — which it does routinely, on an unacknowledged publish or a
transient write failure — the duplicate was not recognised: the measurement rows were
written a second time and the hourly rollups counted them twice. Single-metric readings, and
readings arriving over Sparkplug or LwM2M, were never affected. The fix is forward-only:
duplicates already written before the upgrade stay where they are, and their rollups stay
inflated. If you have charts that looked too high on multi-metric devices, this is why, and
they will read correctly from the upgrade onwards.

**Every paged list now returns rows in a declared order.** Of the platform's 37 list
endpoints, 31 named no order at all, which leaves a paged read free to hand the same row out
on two pages and never show another one — a real defect that had already been reported twice
as a screen reshuffling under an operator. Each list now sorts on a total, unambiguous key.
If you have code that depended on the incidental order a particular query happened to return,
it will now see a stable one instead, which may not be the same one. One order was chosen
deliberately rather than mechanically: device credentials are listed with the most runway
left first, because an unbounded read of them feeds credential reuse, and ordering by id
would have handed back the credential closest to expiry.

**A command answered in plain text now records its answer.** A device replying to a command
with something that is not JSON — `acknowledged`, a bare status word — used to fail the write
with a database type error and leave the command in `SENT`, retrying the same doomed write
once a minute for the life of the row. The command then timed out against a device that had
answered it correctly. Such a response is now stored, losslessly, as a JSON string. Values an
**API caller** supplies are unchanged: those must still be valid JSON, because a caller
sending malformed JSON is a caller who should be told so.

**Commands to Sparkplug devices now fail immediately instead of being lost.** The platform
has no command path to a Sparkplug device — those nodes live on your own MQTT infrastructure
and nothing bridges the two — and the check that was supposed to refuse such a command was
comparing against a value no device ever carries, so it matched nothing and every one of
those commands was accepted and then quietly went nowhere. They are now recorded `FAILED`
straight away with that as the reason, and counted under
`command_delivery_undeliverable_total`. Expect commands that used to sit until their TTL and
record `TIMEOUT` to appear as prompt failures instead. See [Commands](../concepts/commands.md).

**A command the platform lost track of is re-armed rather than blamed on the device.** A
command could reach `SENT` and then be reached by nothing — the pod that published it dies
before recording the outcome — and `SENT` had no exit except the TTL, which recorded
`TIMEOUT` against a device that was never sent anything. A background pass now finds those
and re-arms them to `PARKED`, so they are delivered on the device's next wake.
`command_delivery_stranded_recovered_total` carries a `{disposition}` label saying where each
one landed. **This applies to LwM2M devices only** — on plain MQTT a command that appears to
have gone nowhere cannot be told apart from one that arrived and whose answer was lost, so the
behaviour there is unchanged and `command_delivery_stranded_skipped_total{reason="transport"}`
will show a steady rate that is not a fault. See [when the platform loses track of a
command](../concepts/commands.md#stranded-commands).

**A rule action the platform cannot ever deliver is dropped instead of retried.** When a
REACT action is refused for a reason no retry can change — a `sendCommand` aimed at a device
that no longer exists, or at a command outside that device's published vocabulary — it used
to be retried to the redelivery limit and then counted as poison, which put an authoring
mistake on the same shelf as an infrastructure failure. It is now dropped on the first such
refusal and counted under `react_actions_permanently_rejected_total`, labelled by action
type. A standing rate on that counter means a rule is aimed at something its devices cannot
accept; the poison counter it used to inflate now means what it says.

**A truncated cross-service response is counted.** Services read each other's responses up to
a fixed 1 MiB cap, and a response over that was silently cut short. It is now counted by
`devicechain_svcclient_responses_truncated_total`, labelled by peer. The reading should be
flat at zero; a non-zero one means some service is acting on a partial answer, which is worth
knowing about before the symptom reaches a screen.

#### Input that used to be accepted and now is not

- A notification policy carrying `deviceTypeToken`. Scoping a policy to a device type is
  not implemented; the write used to succeed and then deliver nothing at all.
- A notification rule whose `severity` is not one of the uppercase tiers or `*`. A
  lowercase severity used to write, read back unchanged, and never match an alarm.
- An `occurredTime` of `0001-01-01T00:00:00Z`. It is a valid timestamp, and the platform
  reserves it to mean no time was reported.
- An enqueue that would push a tenant past its **held-command ceiling**. Commands withheld
  for an absent device accumulate with no natural brake — a sleeping fleet's backlog can sit
  for days — and nothing bounded that before. The limit resolves from the tenant's own
  override, else its tier's, else a platform default of 10,000, and there is no value at any
  level meaning unlimited. The refusal carries the code `HELD_CEILING_EXCEEDED` and is the
  only temporary one the enqueue gate produces: it frees as those devices return. A client
  that treats every rejection as permanent should special-case it. See [how much backlog a
  tenant may hold](../concepts/commands.md#held-command-ceiling).
- An enqueue that would push a tenant past the part of that ceiling **reserved for
  delivery**. A share of the limit — 20% by default — is kept for the platform's own command
  delivery, so a single fleet write cannot consume all of it and leave every automated
  `sendCommand` for that tenant refused until the backlog drains. Everything issuing commands
  on your behalf is bounded by the remainder: the console, the SDKs, `dcctl` and your own
  integrations alike. The practical consequence is that a large batch that would have been
  admitted whole may now be partly refused; where the batch was allowed to fan out
  partially, its record says which devices did not fit. See [part of the ceiling is reserved for
  delivery](../concepts/commands.md#delivery-machinery-reserve).

#### Bootstrap and the CLI

These reach an instance through `dcctl bootstrap` and the infrastructure apply rather than
through `helm upgrade`, so none of them lands during the upgrade above. They are here because
each is a change in what goes wrong.

**A broker configuration change now restarts the broker.** `nats-server` cannot hot-reload its
authorization-callout block or its JetStream limits, and its refusal is wholesale — it abandons
the entire reload, including every unrelated change in the same apply. What that looked like
from outside was the worst kind of nothing: the apply reported success, the ConfigMap showed
the new values, and the running broker was still on the configuration it booted with, with the
only evidence one line inside the broker's own log. Services then failed to authenticate
against a ConfigMap that proved their credentials were right. The broker's StatefulSet now
carries a hash of its rendered configuration in its pod template, so the server always comes up
on the file it was given. The cost is that broker configuration changes now roll those pods,
where previously only a chart or image bump did: budget roughly 50–70 seconds per pod, which on
a single-server broker is a brief full outage and on three is a rolling restart.

**The third-party chart versions are pinned.** `ingress-nginx` and `cert-manager` were
installed at whatever their repository last published, which meant the chart repository was a
dependency of *planning* as well as of applying: when its release-asset host returned 503,
the plan failed with an error naming neither the chart nor the network, and it cost two
failed bootstraps before the cause was found. They are pinned to `4.15.1` and `v1.21.1`
respectively — the versions the drilled cluster runs. If you had been relying on picking up a
newer one automatically, you now upgrade it deliberately.

**A `dcctl` you built yourself now has a usable default image tag.** `make -C backend/cli
build` produced a binary whose default image tag came from the repository's `VERSION` file —
a value no release ever sets and no image was ever pushed under. Every workload landed in
`ImagePullBackOff`, several minutes into a bootstrap that had reported healthy progress the
whole way. A locally built `dcctl` now defaults to `dev`, which the unpublished-version guard
recognises and refuses early with a message you can read, rather than late with one you
cannot. A released `dcctl` was never affected: its tag comes from the release itself.

#### Configuration

One key moved. `maxEventFutureSkewSeconds` bounded how far a device-reported timestamp may
lead the platform's clock; it was an `event-processing` setting and is now a
`device-management` one, because the event time is now decided in exactly one place for
live detection and replay alike.

A configuration that still sets it under `event-processing` **starts normally** and logs a
warning naming the new location. The old value is not applied — set it under
`device-management` if you had changed it from the default of 300 seconds.

Nothing was removed from the chart's values, so a `v0.11.0` values file applies unchanged.

:::caution Drain devices during this one upgrade, or accept a possible detection-engine reset
The bound moved, so for the length of this rollout **neither side is holding it**. On
`v0.11.0` only the detection engine bounded a device-reported time; on `v0.12.0` only event
resolution does. The two services roll as independent deployments, so there is a window where
a `v0.11.0` event-processing has already been replaced while a `v0.11.0` device-management is
still publishing — and an event crossing in that window is checked by neither.

What it costs if one arrives with a wildly future timestamp: detection tracks a single time
frontier across the whole instance, so that one event advances it and every tenant's pending
timers fire at once. Recovering means resetting the engine's snapshot.

**This is a one-upgrade boundary, not a standing weakness** — once both services are on
`v0.12.0` they agree permanently, and an instance you destroy and recreate is never exposed.
If you are upgrading in place with devices sending, stop device traffic for the rollout, or
be prepared to reset the detection snapshot afterwards.
:::

**A service that refuses its own configuration now exits non-zero.** It used to log
"refusing to start" and then terminate with status 0, so the pod reported `Completed` —
exactly what an orderly shutdown reports, and indistinguishable from one at a glance. Those
pods will now `CrashLoopBackOff` instead. Nothing has changed about which configurations are
refused; what changed is that the refusal is now visible in `kubectl get pods`, in a restart
count, and to anything that alerts on either. A service that fails to shut down cleanly is
reported the same way, for the same reason. If you have an alert that treats a `Completed`
service pod as benign, this is the release where the underlying failure starts reaching you.

### The one-time durable-ingest cutover

The release that introduces **durable MQTT ingest** changes how `event-sources` receives
device telemetry: instead of subscribing to the broker as an MQTT client, it consumes a
durable capture stream that the broker writes to before it acknowledges the device. This
is what stops telemetry being lost when `event-sources` is down.

Crossing that release once is a normal `helm upgrade` — but expect a **brief window of
duplicated telemetry**, and plan for it:

- During the rollout the outgoing pod is still ingesting over MQTT while the incoming pod
  has already begun consuming the capture stream, so messages published in that overlap are
  ingested by both. The window is bounded by how long the two pods coexist — the incoming
  pod's startup plus the outgoing pod's drain.
- Events that carry **both** an `altId` **and** a device-supplied `occurredTime` are unaffected:
  the write-side dedup key is `(tenant, altId, occurredTime)`, so those duplicates collapse. An
  event with an `altId` but no `occurredTime` does **not** collapse — the decoder stamps the
  current time when the device omits one, and the two copies are decoded in different pods at
  different instants, so they get different timestamps and land as two rows. Telemetry with no
  `altId` is not deduplicated at all.
- The overlap is preferred deliberately. The alternative ordering — stopping the old pod
  before the capture stream exists — loses every message the broker acknowledges in the gap,
  and that loss is silent: the device is told the message was accepted and it is never
  stored. A duplicate reading is visible and correctable; a missing one is neither.

:::danger Do not set `event-sources` to `Recreate`
`strategy: Recreate` on `event-sources` produces exactly the lossy ordering above, because
it terminates the old pod before the new one creates the capture stream. The chart refuses
to render this configuration rather than let it drop telemetry silently. `event-sources`
is not a single-writer service and gains nothing from `Recreate` — once cut over it can run
multiple replicas, which the MQTT-client path it replaces could not.
:::

## Data durability {#data-durability}

The database tier is intentionally **lifecycle-independent** from the application. Both
databases are provisioned as separate infrastructure with a destroy guard, so upgrading,
reinstalling or uninstalling the *application* never touches them. That is the common case
and it is safe.

:::caution Removing the database from the infrastructure configuration is a different act
The guard protects each database while it is *in* the infrastructure configuration. It does
not protect one that has been taken *out* of it: a resource removed from the configuration
is no longer covered by rules the configuration declares, and the removal plan will
succeed. The database clusters also own their volumes, so removing one takes its data with
it rather than leaving an unattached volume behind.

Do not edit the database out of the infrastructure configuration as a way of replacing it.

Upgrading an instance created before the databases moved onto the operator is the one case
where this comes up, and it is refused at plan time rather than left to chance. Dump both
databases first, then re-run the bootstrap with `--allow-legacy-db-removal` — which asserts
you have handled the data, and verifies nothing. For a local instance, `dcctl destroy`
followed by a fresh bootstrap is simpler and discards the data deliberately.
:::

This is durability of the running volumes — it is not a substitute for scheduled backups and
point-in-time recovery, which are provisioned with the production infrastructure. See
[Deployment & Operator](./kubernetes-operator.md) for how the infrastructure and application
layers are separated.
