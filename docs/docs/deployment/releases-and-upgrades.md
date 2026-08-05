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

## Versioning model

Every release is a single semantic-version git tag (`vX.Y.Z`). That one version covers
**everything together** — each service image, the operator, the Helm chart, and the
`dcctl` CLI are all published at the same version. There is no per-service version skew to
reason about: a deployment is one coherent number.

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

## Zero-downtime upgrades

Upgrading to a new version is normally a plain `helm upgrade`, and the chart and services are
built to roll customers forward without dropping traffic. Three releases so far are
exceptions, all documented below: the durable-ingest cutover, which is still a
`helm upgrade` but has a visible side effect, and **`v0.9.0` and `v0.10.0`, which cannot be
upgraded into at all**. Check the release notes for the version you are moving to before
running the command:

```bash
# Carry the current release's values forward, then change only the version.
# This file contains your instance's secrets — delete it when you are done.
helm get values dc -n default -o yaml > dc-values.yaml

helm upgrade dc deploy/helm/devicechain \
  -n default \
  -f dc-values.yaml \
  --set image.tag=v1.3.0

rm dc-values.yaml
```

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
`helm upgrade`. That is the rule, and it holds for almost every release.

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
