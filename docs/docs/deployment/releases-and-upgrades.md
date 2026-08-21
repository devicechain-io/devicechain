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

**4. Check for these GraphQL operations** in anything you have written against the API:

| Operation | What changed |
| --- | --- |
| `createCommand` | Returns `CreateCommandResult!` instead of `Command!`. The command is now under a `command` field, alongside a `rejection` field that explains a refusal. |
| `updateDeviceType` | Its `request` argument is now a required `DeviceTypeUpdateRequest!`. Unrecognised fields in it are rejected rather than ignored. |
| `assertedActiveDeviceStates` | Replaced by `assertedDeviceStates`, which takes `activeOnly` and pages through `afterId` and `pageSize`. |
| `deviceCredentials`, `deviceCredentialsById`, `deviceCredentialsByToken` | Now require `device:write`. For one credential type the readable identifier *is* the bearer token, so `device:read` — which every enabled member holds — was enough to open a broker session as any device in the tenant. |
| `locationEvents` | Now requires `location:read`, as above. |
| Any `...ById(ids: [])` query | An empty id list now returns nothing. It used to return the whole table, unpaginated. |

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

#### Input that used to be accepted and now is not

- A notification policy carrying `deviceTypeToken`. Scoping a policy to a device type is
  not implemented; the write used to succeed and then deliver nothing at all.
- A notification rule whose `severity` is not one of the uppercase tiers or `*`. A
  lowercase severity used to write, read back unchanged, and never match an alarm.
- An `occurredTime` of `0001-01-01T00:00:00Z`. It is a valid timestamp, and the platform
  reserves it to mean no time was reported.

#### Configuration

One key moved. `maxEventFutureSkewSeconds` bounded how far a device-reported timestamp may
lead the platform's clock; it was an `event-processing` setting and is now a
`device-management` one, because the event time is now decided in exactly one place for
live detection and replay alike.

A configuration that still sets it under `event-processing` **starts normally** and logs a
warning naming the new location. The old value is not applied — set it under
`device-management` if you had changed it from the default of 300 seconds.

Nothing was removed from the chart's values, so a `v0.11.0` values file applies unchanged.

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
