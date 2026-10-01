---
sidebar_position: 4
title: Releases & Upgrades
---

# Releases & Upgrades

DeviceChain ships as a set of prebuilt, versioned container images plus a Helm chart.
You do **not** need to build anything to run it — pull a released version, install the
chart, and upgrade in place with zero downtime.

:::warning Some versions cannot be upgraded into
Three points in the history require recreating the instance rather than upgrading it:

- **`v0.9.0`** replaced every service's migration chain with a single frozen baseline, so a
  `v0.8.x` database meets it and fails on `already exists`. See
  [The v0.9.0 baseline squash](#v090-baseline-squash).
- **`v0.10.0`** changed the primary key of the event tables to fix a defect that was
  silently discarding telemetry. See [The v0.10.0 event key change](#v0100-event-key).
- **anything built by `v0.16.0` or earlier**, which recorded no declaration of what the
  instance is — the record an upgrade now reads to know what to deploy. See
  [Instances built by v0.16.0 and earlier](#pre-declaration-recreate).

If you are on any of them, read the matching section below before you do anything else.
:::

:::caution Crossing v0.12.0 needs a few changes first
`v0.12.0` upgrades in place, but it changes the topic a device answers a command on, moves
one permission, and changes several things whose shape stayed the same. The upgrade
will report success either way. Read
[v0.12.0 — an upgrade that changes contracts](#v0120-upgrade) before you start.

This applies to **any** upgrade that crosses `v0.12.0`, not only one that stops there — going
from `v0.11.0` straight to a later patch does not skip those changes.
:::

## Versioning model

Every release is a single semantic-version git tag (`vX.Y.Z`). That one version covers
**everything together** — each service image, the operator, the Helm chart, and the
`dcctl` CLI are all published at the same version. There is no per-service version skew to
reason about: a deployment is one coherent number.

Two commands move all of it, and which one moves what follows the thing's **lifetime**.
The operator is one controller per cluster, shared by every instance on it, so
`dcctl install` moves it along with the rest of the cluster's prerequisites. The
configuration document, the chart release and the service images belong to one instance, so
`dcctl upgrade` moves those, one instance at a time. See
[Zero-downtime upgrades](#zero-downtime-upgrades) for the procedure.

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
  the only route forward is to recreate the instance. `v0.9.0` and `v0.10.0` are such
  releases
- **stop being upgradeable onto from an older instance** for a reason that is not the schema
  at all — the release after `v0.16.0` reads a record of what an instance is that earlier
  releases never wrote, and refuses rather than guessing one

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

`DC_ROOT_KEY` below is the instance's secret-store root key — required by every
profile, generated once with `openssl rand -base64 32`, and passed unchanged on every
install and upgrade. See
[Deploying with Helm](./kubernetes-operator.md#deploying-with-helm) for why.

Substitute a real released tag for `<version>` — the
[releases page](https://github.com/devicechain-io/devicechain/releases) lists them, and an
unreleased value fails at the pull rather than at install time.

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

The Helm chart itself is also published as an OCI artifact, so you can install it without a
checkout of the repository. The chart version is the release version without its leading `v`
— `--version 0.16.0` installs release `v0.16.0` — so there is no separate number to look up;
`helm show chart oci://ghcr.io/devicechain-io/charts/devicechain` prints the latest, and
`--version` refuses anything that was never published:

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version <chart-version> \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

The chart is also listed on
[Artifact Hub](https://artifacthub.io/packages/helm/devicechain/devicechain), which
shows every published version alongside its default values and rendered templates.

### Upgrading a chart-only install {#chart-only-upgrade}

An instance installed with `helm install` rather than `dcctl bootstrap` is upgraded with
`helm upgrade`, and it keeps a trap the `dcctl` path does not have.

`dcctl upgrade` does not apply to it. That command reads an instance's declaration and its
configuration document back out of the cluster, and a chart-only install has neither.

The release name below is `dc` because that is the name the `helm install` above chose. An
instance installed by `dcctl bootstrap` carries a release named after the instance —
`devicechain` installs as `dc-devicechain` — so any `helm` command aimed at one of those
needs that name instead.

```bash
helm get values dc -n default -o yaml > dc-values.yaml

helm upgrade dc deploy/helm/devicechain \
  -n default \
  -f dc-values.yaml \
  --set image.tag=<new-version>

rm dc-values.yaml   # this file holds your instance's secrets
```

:::warning Carry the values forward — `--set image.tag=…` on its own will not work
Helm's rule is the trap. An upgrade that passes **no** values at all reuses the ones already
in the release. But the moment you pass *any* value — including the single `--set` that
changes the version, which is the whole point of an upgrade — Helm starts from the chart's
defaults instead, and everything you set at install time is gone. That includes the instance
root key, without which the stored secrets of a running instance cannot be read.

Nothing is corrupted when it happens, because the chart refuses to render without the root
key:

```
Error: UPGRADE FAILED: execution error at (devicechain/templates/instance-config.yaml:21:4): instance.config.infrastructure.secrets.rootKey is required: every instance seals its token-signing key under it, along with any integration credentials it stores, and user-management cannot start without it. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically.
```

`--reuse-values` also works, but it silently keeps stale entries when the chart's own
defaults move between versions, so prefer writing the values out and passing them with `-f`,
where you can see them.
:::

:::caution If the instance config comes from a Secret you manage
An install that mounts its instance config from a Secret the chart does not write — set with
`instance.existingSecret`, the pattern External Secrets and sealed-secrets produce — now has
to satisfy four conditions, and `helm upgrade` **fails the render** rather than proceeding
when one is not met. Nothing in the release changes when that happens; the refusal is the
whole effect.

- **The Secret must be named `dci-<instance.id>-config`**, in the instance's namespace. Any
  other name is refused, because `dcctl` reads the config back by exactly that name to decide
  whether an instance already exists, and treats a missing Secret as a fresh install — a
  re-run would mint a new root key and new database and broker credentials over a live
  instance. If your Secret is under another name, create it again under this one (an
  External Secrets `target.name`, a sealed secret's `metadata.name`) before you upgrade.
- **`instance.existingSecretChecksum` is required**: the sha256 of the document under the
  Secret's `instance` key, as 64 lowercase hex characters. The chart cannot read your Secret,
  so this is what rolls the pods when the config changes; without it a rotated credential
  would apply cleanly, restart nothing and report success. Recompute it whenever the document
  changes:

  ```bash
  kubectl get secret dci-<instance.id>-config -n dci-<instance.id> \
    -o jsonpath='{.data.instance}' | base64 -d | sha256sum | cut -d' ' -f1
  ```

- **`networkPolicy.externalConfigPorts` is required while `networkPolicy.enabled` is on**,
  with keys `nats` and `rdb` restating the broker and database ports your document names.
  The chart would otherwise take them from its own defaults, and a port that disagrees
  silently blocks the services' own egress — which presents as a broker or database outage.
- **`metrics.natsBrokerHost` is required while `metrics.natsPodMonitor` is on** — the
  broker's hostname as your document names it (the short Service name for a broker in this
  instance's namespace, `<service>.<namespace>` for one elsewhere). It decides which
  namespace the PodMonitor watches; a default that does not match monitors nothing. Or set
  `metrics.natsPodMonitor=false`.

Each `helm` error names the value it wants and why. The two transforms the chart normally
applies while writing the document — the `infrastructure.shutdown` block, and the removal of
`infrastructure.aiInference` when that area is not deployed — remain yours to reproduce, as
before.
:::

## Zero-downtime upgrades {#zero-downtime-upgrades}

Upgrading is **two commands** — one for the cluster, then one for each instance on it — and
the chart and services are built to roll customers forward without dropping traffic. Four
exceptions are documented below: the durable-ingest cutover, which is still an ordinary
upgrade but has a visible side effect, and **`v0.9.0`, `v0.10.0` and any instance built by
`v0.16.0` or earlier, which cannot be upgraded into at all**. Check the release notes for
the version you are moving to before running it:

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

A release is one version across the service images, the chart, the operator and `dcctl`.
The two commands split it by what each thing belongs to:

**`dcctl install` moves the cluster.** The operator — its namespace, CRDs, RBAC and
controller — is applied from manifests embedded in `dcctl`. It is not part of the Helm
chart, so nothing inside the chart can reach it. The whole rendered stream is applied rather
than just the controller's image, because the CRDs are in it: a schema left at the version
the instance was bootstrapped at silently discards any field a later release added. This
command also moves the rest of the cluster's shared prerequisites; see
[Install the cluster](./bootstrap.md#install).

**`dcctl upgrade` moves one instance**, and moves nothing that is shared:

1. **the configuration document** every service reads its credentials and endpoints from,
   recomposed from this release's chart and written by `dcctl`, which owns it;
2. **the Helm release** that runs the services, which rolls them onto the new images and
   waits for each area to finish.

**Order matters, and the upgrade checks it.** The operator is what the instance's
declaration is defined by, so it has to be at the new release before an instance is moved
onto it. `dcctl upgrade` reads the operator the cluster is carrying and **refuses** an
instance whose cluster has no operator at all, or whose operator is identifiably a
different release's — naming the install command to run first. It does not apply the
operator itself: on a cluster holding several instances that would move every other
instance's controller as a side effect of upgrading one, silently.

There is one case it lets through with a warning rather than refusing. `dcctl install`
records which release installed the definitions; an operator put on the cluster **by hand**
carries no such record, and `dcctl` cannot tell a deliberate hand-install from one an older
`dcctl` overwrote. Rather than overrule a choice it cannot see, it prints a note naming the
install command and continues. If you did not install the operator by hand, treat that note
as the refusal it would otherwise have been and run `dcctl install` before going further.

Run either with `--dry-run` first if you want to see what it would move. `dcctl upgrade`
takes the target cluster from the instance's own record rather than guessing, and says
which.

:::tip It reads every credential and mints none
`dcctl upgrade` keeps what the instance is running on: the database owner passwords, the
broker's authority and logins, the cross-service secret, the secret-store root key, and —
where the cluster runs them — the monitoring dashboard's admin password and the in-cluster
backup store's credential. A version change cannot become a credential change.

This is verified rather than asserted. An upgrade of a running instance was checked by
comparing a digest of every one of those credentials before and after, and the only thing
that had changed was the image tag — on every service, the console and the operator.
:::

:::warning An upgrade is not a way to rotate credentials
Because it keeps them by design, it rotates nothing. If you need to change a credential, an
upgrade will not do it — and for several of them there is no supported procedure today.
:::

:::note It moves a version, not an instance's shape
The profile, the topology and the enabled functional areas come from the instance's own
declaration — what `dcctl bootstrap` recorded in the cluster — not from flags typed here.
Changing what an instance *is* is a different question with different answers: raising the
replica count, for one, does not re-replicate messaging streams that were created at the old
one.

Two things are deliberately outside this command as well. It does not run the infrastructure
apply, because one of that apply's inputs cannot be recovered from the cluster — the endpoint
and bucket names of an off-site backup destination, which come from the file you gave
`dcctl install --backup-credentials-file`. And it does not touch the databases beyond letting
the services run their own migrations.
:::

### What else an upgrade checks {#upgrade-checks}

Two things ride along with it, because a version bump is the thing that reliably happens to a
live instance and a calendar is not.

**The broker's certificate.** The messaging broker serves a certificate valid for a year,
issued by an authority `dcctl` mints at bootstrap and keeps in the cluster. An upgrade
re-issues that certificate when it is inside its last 30 days, or when it no longer covers
every name the brokers dial each other by — which is what scaling an instance up does to a
certificate that is otherwise still comfortably in date. The re-issue is under the **same**
authority, so nothing has to re-trust anything, and the broker is restarted so that it
actually serves the new certificate rather than holding the old one until something unrelated
rolls it. Outside those conditions the check runs and does nothing.

An instance bootstrapped before `dcctl` kept that authority cannot have its certificate
re-issued in place. The upgrade says so and continues rather than failing; recreating the
instance is what mints a fresh authority and certificate.

**The root-key escrow.** Every upgrade checks that the escrow artifact for this instance
still protects the key the instance is actually running on. That check needs **no
passphrase** — the artifact records a fingerprint of the key it protects, so matching it
against the running one opens nothing.

| What it finds | What it does |
|---|---|
| The artifact protects the running key | Says so, and leaves it alone |
| The artifact protects a **different** key | Warns loudly. It most often belongs to an earlier instance of the same name, and restoring from it would recover a cluster that cannot read its own secrets |
| There is no artifact | Writes one, if you passed `--escrow-passphrase-file` (or set `DCCTL_ESCROW_PASSPHRASE`). Otherwise it warns that the only copy of the root key is inside the cluster |

This is how an instance first created with `--no-escrow` gains an escrow later. None of these
outcomes fails the upgrade: an escrow problem is about a future disaster and the upgrade in
front of it is about the running instance, and an operator who cannot upgrade will work around
the check rather than fix it.

:::note The instance half used to be a `helm upgrade`
The procedure was: write the current release's values to a file with `helm get values`, pass
them back with `-f` alongside the new image tag, delete the file because it held your
secrets, and then move the operator separately.

That dance existed only because the Helm release was where the instance's generated
credentials lived, and Helm starts from the chart's defaults the moment you pass it any value
at all — so an upgrade that did not carry them forward by hand lost them. `dcctl` owns the
configuration document now, the release no longer holds those credentials, and the step that
told you to write your secrets to a file simply goes.

The gap that form left open was an upgrade stopping after the `helm` half, leaving the new
services running against the controller the instance was first bootstrapped with —
indefinitely, and with no error to say so. That is what the refusal above closes: the two
commands are still two, because the operator belongs to the cluster and the release belongs
to the instance, but `dcctl upgrade` now reads which operator the cluster is carrying, and
says so — refusing where it can tell the two apart, and warning where it cannot.
:::

What makes the rollout safe:

- **Surge-before-terminate.** Deployments default to a `RollingUpdate` strategy with
  `maxUnavailable: 0` and `maxSurge: 1`, so a new pod must pass its `/readyz` readiness
  probe **before** an old pod is removed, and capacity never dips during the rollout. Four
  areas ship with `strategy: Recreate` and one replica instead, because only one of their
  pods may serve at a time: `event-processing` (the rule engine is a single writer), `mcp`
  (a client's session lives on the pod that opened it), `sparkplug-ingest` (one Sparkplug
  Host per pod) and `lwm2m-ingest` (one CoAP/UDP socket per pod). For those, every old pod
  stops before the new one starts, so a rollout has a brief gap by design — and the chart
  refuses `Recreate` with more than one replica.
- **Graceful shutdown / connection draining.** When a pod is asked to terminate it first
  reports "not ready" (so the Service stops routing new requests to it), waits a short
  drain window for that change to propagate, and only then finishes in-flight work and
  shuts down. Configure the window with `shutdownDrainSeconds` (default `5`), kept safely
  under `terminationGracePeriodSeconds` (default `30`). The two are one budget and the
  services check it: the drain may take at most **half** the grace period, because the
  window only waits — finishing in-flight requests, draining the broker consumers and
  closing the database pool all happen after it, and the kubelet sends SIGKILL when the
  grace period expires whether or not that has finished. A larger window is refused when
  the service starts (and by `dcctl bootstrap` before it installs anything), not
  discovered when a pod is already shutting down. Set `shutdownDrainSeconds: 0` to skip
  the drain entirely, which suits a single-instance run with no Service to be pulled out
  of.
- **Coordinated schema migrations.** Services run database migrations under a database-level
  lock, so when several replicas start at once exactly one applies migrations and the rest
  wait — no races, no duplicate DDL.

:::tip Run at least two replicas in production
For true zero-downtime, run `replicas: 2` (or more) for each area that can serve from more
than one pod, so the rollout always has a live pod serving traffic. A single replica still
has a brief gap while its one pod is replaced. Set it globally with `--set replicas=2`, or
per area under `functionalAreas.<area>.replicas`. The four single-pod areas above are the
exception: `mcp`, `sparkplug-ingest` and `lwm2m-ingest` refuse more than one replica under
any strategy, and `event-processing` takes a second replica only as a warm standby, with
`strategy: RollingUpdate` set alongside it — the render fails and says why otherwise. A
`PodDisruptionBudget` is rendered automatically for any area with more than one replica, so
node drains can't evict every replica at once.

Rate ceilings are enforced by each replica separately, so two replicas of `event-sources`,
`outbound-connectors` or `ai-inference` can admit up to twice a tenant's ceiling. See
[Governance](../concepts/governance.md#per-replica).
:::

### The v0.9.0 baseline squash {#v090-baseline-squash}

`v0.9.0` is the **first** of the two releases that cannot be reached by upgrading in place
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
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

With the current `dcctl` the cluster is recreated too, not just the instance: see
[why](#pre-declaration-recreate).

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

`v0.10.0` is the second release that **cannot be reached by upgrading in place**, for a
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
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

With the current `dcctl` the cluster is recreated too, not just the instance: see
[why](#pre-declaration-recreate).

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

`v0.11.0` is the first release since `v0.8.5` that can be reached in place. Its
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

`v0.12.0` is reachable in place. Its schema change **adds** migrations rather
than replacing a baseline, so an existing `v0.11.0` database is carried forward with its
rows intact, and this was measured on a running instance rather than reasoned about.

What it does change is **contracts** — the MQTT topic a device answers a command on, a
handful of GraphQL operations, and the meaning of several things whose shape did not
change at all. None of that is visible in an upgrade that reports success, so read
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
that rolls pods, so the upgrade restarts every service rather than only the ones
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

:::note[This no longer applies]
`updateDeviceProfile` has since become a [partial
update](../reference/graphql-api.md#which-mutations-are-partial-updates): a request that says
nothing about the declaration now leaves it alone, and clearing one takes an explicit `null`.
The advice above is what to do on a `v0.12.x` or `v0.13.x` instance; on a current one there is
nothing to carry forward. Renaming a profile is a [mutation of its
own](../reference/graphql-api.md#renaming-a-record).
:::

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
through the release, so none of them lands during the upgrade above. They are here because
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

### v0.12.1 — a patch, nothing to do {#v0121-upgrade}

`v0.12.1` is an ordinary in-place upgrade from `v0.12.0`. It adds no migration, so the database is
untouched, and it changes no API, topic, permission or configuration key — everything the
v0.12.0 section above describes is still exactly what you are running.

Two fixes are worth knowing about:

- **Status colours in the web console** now meet WCAG AA contrast in both light and dark
  themes. The `pending` and `online` badges failed in both themes, and error text failed in
  dark mode. What the colours *mean* has not changed — but the filled badges are visibly
  darker, because that is the only way white lettering on them becomes readable.
- **The inactivity monitor** no longer reads every device in every tenant into memory on each
  pass, and no longer issues a database round trip per device it flips; it decides and writes
  in one statement. Devices go inactive on exactly the same schedule as before — this is a
  cost change, not a behaviour change — and it matters most on large fleets, and in the
  moments right after a presence source hands its devices back.

### v0.13.0 — geofence limits become part of your plan {#v0130-upgrade}

`v0.13.0` is an ordinary in-place upgrade, and it changes no topic, permission or configuration key.

It does change the database, additively: it creates one table for geofence shapes, adds three
nullable columns to the tenant record, and rewrites stored geofence history into the new form
once, in place. Nothing is dropped and nothing has to be recreated.

That last step is the one worth knowing about if you already use geofencing. From `v0.13.0` a
fence's shape is stored once and referred to by its content rather than copied into every
version of your fence set, and the upgrade rewrites the fence history you already have so it
refers to shapes the same way. Your fences and their history come through unchanged; what
changes is how they are stored. The step is safe to re-run and does nothing on an instance that
has already had it.

What changes is that the two geofence limits that used to be fixed for everyone — 512 positions
in one fence, 100 fences per tenant — are now **settings on your plan**, joined by a third:
a limit on the total positions across your whole fence set. All three keep their previous
values by default, so **a tenant that has never had them changed is metered exactly where it
was** and needs to do nothing.

Two things are worth knowing before you upgrade:

- **The whole-set limit is new, and its default is what the other two already implied**:
  51,200 positions, which is 100 fences of 512. So a tenant using geofencing exactly to the
  documented limits is *at* the new limit, never over it. The total counts **distinct** shapes,
  so two fences drawn identically cost one.
- **A change is refused only when it makes a number larger.** If an operator later lowers one
  of your limits below what you already hold, you keep every fence. Editing a fence's name or
  description, and deleting a fence, always keep working — the check is on growth, not on size.
  This is what keeps a plan change from stranding fences that were legal when drawn. Making a
  fence smaller almost always works too; the exception is that the whole-set total counts
  *distinct* shapes, so editing one of several identically-drawn fences separates it from the
  rest and can raise the total even though that fence shrank.

One consequence to plan around: because deleting a fence lowers the stored total, a tenant that
is over a limit and deletes a fence cannot recreate it. To move a fence to a different token,
**create the new one first and delete the old one after** — which needs one spare fence slot for
the moment both exist.

Operators packaging tiers should know these have real ceilings, because they are not all spent
on the tenant alone: the whole-set total is a share of a geometry cache every tenant on the
instance draws from, and the fence count bounds an announcement that has to fit one broker
message. The refusals name both the number and
the setting to raise, and a `geofence_cap_refusals_total` metric counts them by which limit
refused.

### v0.14.0 — the packages you build against {#v0140-upgrade}

`v0.14.0` is an ordinary in-place upgrade from `v0.13.x`. It adds no migration, so the database is
untouched, and it changes no API, topic, permission or configuration key. **If you only run the
platform, there is nothing to do.**

What changed is around it — the artifacts you build against, and the CLI you run it with.

**The web runtime is published.** `@devicechain/client`, `@devicechain/dashboards`,
`@devicechain/widgets` and `@devicechain/brand` are on npm, so embedding a dashboard or a widget
in your own application is an install rather than a build against our source tree. The four are
released together at one version and pinned to each other. See
[npm Packages](../reference/npm-packages.md) for the install line and the dist-tag policy.

**If you were building our widgets from the source tree, one change is yours to make.**
`maplibre-gl` is now a peer dependency of `@devicechain/widgets`: your application supplies the
library, its worker URL and its stylesheet, instead of the widget package deciding those for
you. That is what makes the package work under a bundler we do not control — but it means a map
widget with no host wiring above it now renders an explicit notice rather than a blank canvas,
which is the symptom to expect if you upgrade without doing it. The wiring is short and is
written out at [Rendering a map](../reference/npm-packages.md#map-host-wiring). Nothing changes
on the server.

**The .NET and Unity client SDK is published** to nuget.org as `DeviceChain.Sdk`.

**`dcctl` can tell you what it has bootstrapped, and shut all of it down.**

```bash
# every instance, the cluster it lives in, and whether that cluster is still there
dcctl instances list

# tear down all of them
dcctl destroy --all
```

🔴 **This closes a defect worth acting on, not just knowing about.** Until now nothing recorded
which cluster an instance had been bootstrapped into — it was derived from the instance name at
create time and derived again at destroy time. That derivation is wrong for any instance
bootstrapped with `--kube-context`, and the failure was silent in the worst direction: `dcctl
destroy` asked the provider to delete a cluster that did not exist, which succeeds quietly,
removed the local state, and reported the instance destroyed while its actual cluster kept
running. **If you have ever bootstrapped with `--kube-context` and destroyed that instance
afterwards, its cluster is probably still up.** `dcctl instances list` cannot show you these —
the destroy removed the local record, which is precisely the problem — so ask the provider
directly (for the local provider, `kind get clusters`) and delete what you recognise.

From this release the cluster is written down at bootstrap and read back at destroy, and the
closing line says which of three things happened: the recorded cluster was deleted, the cluster
was already gone and only local state was cleared, or the record could not be trusted and
nothing was touched. None of them is the old sentence printed over a cluster still running.

Instances created before this release have no such record and list as `no record — destroy will
guess the cluster`. Destroy still works on them, falling back to the old derivation, so the
caveat above continues to apply to them and only to them.

:::note `dcctl destroy` no longer deletes clusters
In current releases `dcctl destroy` removes an instance only — its Helm release, its NATS
broker and event store, its database and login, its namespace and its local state — and never deletes a cluster or the prerequisites
`dcctl install` put there. `dcctl destroy --all` therefore removes every instance and leaves
every cluster running. To delete a local cluster, use `kind delete cluster --name <name>`. See
[Removing an instance](./bootstrap.md#destroy).
:::

### v0.15.0 — updates stop erasing what you did not send {#v0150-upgrade}

`v0.15.0` is an ordinary in-place upgrade from `v0.14.x`. The new migrations run themselves as the
services start, there is nothing to recreate, and no data needs moving by hand.

The breaking changes are in the **API** and in **outbound network access**, not in the upgrade
itself. If you run the platform and drive it through the console, there is nothing here for you
to do. The sections below are for people who call the API directly, who send notifications
through something inside their own network, who run the MCP server, or who have customised
`event-sources` configuration.

#### Update operations no longer replace the whole record

This is the change that affects the most people, and it is the reason this release is marked
breaking.

Before, an update replaced the record: **any field you left out was erased.** Now a field you do
not mention is left exactly as it was, and clearing a value takes an explicit `null`.

The request itself is a new shape that no longer carries the record's own name — updating and
renaming are separate operations, and there are now dedicated `rename…` mutations for the four
types that need one. So an application calling the API directly must **drop the name from its
update requests and regenerate its client code.**

**A request in the old shape is refused outright** with an error naming the field it no longer
accepts. It is not half-applied, and it does not fail quietly — which means you find out at the
first call rather than from a record that has lost half its contents.

:::caution The one case that changes quietly
An application that cleared a value by **leaving the field out** now keeps the old value instead.
Nothing errors; the update simply does less than it used to. If your code relies on omission to
clear a field, send an explicit `null` instead.

Note that not every field accepts `null` — some are required and refuse it with a named error.
Those are fields that could never legitimately be cleared.
:::

:::danger One sharp edge worth knowing about
If you build an update request by binding a **separate variable per field**, a variable you do
not supply arrives as an **explicit null** rather than as an absent field — and explicit null
means *clear this*. On a notification policy's `rules` that empties the entire rule set and
returns success. Bind the whole request object as one variable, or only include the fields you
actually intend to change.
:::

#### The id on a stored event has changed

An event's `id` is now the event's own identifier, rather than a value assembled from the device
token, the event type and the timestamp. **Any id you saved from an earlier release will no
longer match anything.**

The previous form was also not unique: a device reporting two measurements at the same instant
produced the **same id for both**, so any client keeping a normalized cache keyed on it was
silently merging those readings into one. If you stored ids, re-read them; if you keyed on them,
this is a correctness fix as much as a break.

#### Outbound connections to private addresses are now refused

Notification webhooks, **SMTP relays** and connector HTTP calls can no longer reach loopback,
private, carrier-grade NAT, link-local or cloud metadata addresses. The check happens at connect
time, and a refusal is **final — it is not retried.**

This is on by default and there is no switch to turn it off.

:::caution If your mail relay lives inside the cluster, alarm mail will stop
This is the failure most likely to catch you, because nothing about it looks like a network
policy change: notifications simply stop arriving, and the failure is recorded as permanent
rather than pending. Allow the specific addresses you use:

```yaml
instance:
  config:
    infrastructure:
      egress:
        allowedDestinations:
          - 10.96.0.25/32      # the in-cluster SMTP relay
```

List each destination as its own `/32`. Destinations on the public internet are unaffected and
need no entry.
:::

#### If you run the MCP server

Two changes need action, and one of them stops the service from starting:

- **A resource URL with a trailing slash is now refused at startup.** An identifier is compared
  exactly, so a trailing slash meant tokens were bound to an address that never quite matched.
  It used to be accepted and then quietly fail to line up; now it fails loudly at boot. Remove
  the slash.
- **The protected-resource metadata has moved** to the location the specification defines, with
  the well-known segment between the host and the path. The chart routes it for you. **If you
  terminate ingress yourself, add a route** for the `/.well-known/` prefix that does not rewrite
  the path.

#### Two configuration keys were removed, and they behave differently

- **`debug`, inside an `eventSources` entry.** Configuration is validated strictly, so leaving
  this in place **stops `event-sources` from starting**, with an error naming the field. Remove it.
- **`inboundEventBatching` and its `maxBatchSize` / `batchTimeoutMs`.** This one is retired rather
  than rejected: it is stripped at load with a warning, so the service starts normally. Remove it
  at your convenience.

The difference is not arbitrary — a key that is retired is one we can still recognise by name, so
it can be dropped for you. A key nested inside a list entry cannot be, which is why the first one
has to stop the service instead.

#### Also in this release

Commands are now dispatched the moment they are enqueued rather than waiting for the next sweep,
and the sweep interval is configurable if you want to change how often the safety net runs. Dead
letters can be read and queried instead of only counted. There is a reporting view you can point
a BI tool at. Assets gained parent/child hierarchy and a documented property contract, devices
gained a replacement operation, alarms gained bulk acknowledgement, and a tenant can choose the
language its console opens in.

The published npm packages and the .NET/Unity SDK carry no source changes in this release. If
your own code sends update mutations through them, though, that code is yours to regenerate.

### v0.16.0 — devices must name the dispatch they are answering {#v0160-upgrade}

`v0.16.0` is an ordinary in-place upgrade from `v0.15.x`. One new migration runs itself as
`command-delivery` starts — it adds a column with a default, backfills existing rows in the same
statement, and needs nothing from you.

There is **one pre-flight worth doing before you upgrade**, and one breaking change that affects
devices rather than API callers. Beyond those, this release is mostly about services refusing to
start on configuration that used to be accepted and quietly ignored — which is safer, and which
can stop a pod that has been running happily for months.

#### Before you upgrade: audit your listener ports for a collision

`event-sources` runs more than one HTTP listener in a single process — GraphQL on its own port,
plus every HTTP event source you have configured. Until now, two of them landing on the same port
killed one ingest transport **silently**: the losing listener died inside a goroutine and was
never mentioned again.

Binds are now synchronous and a failure is fatal, so a collision that has been quietly broken for
months **crash-loops the deployment instead.** That is the right behaviour and it is also the one
change here most likely to surprise you, because nothing warns you about it today.

Check each source's `port` against the others and against the GraphQL port, and check that the
chart's `extraPorts` entry agrees with each source's own `port`. A device-facing `port: "0"` is
also refused now.

#### Any device that answers a command must echo the dispatch nonce

This is the release's one breaking wire change, and **the population it affects is devices built
outside this repository** — firmware, gateways, anything speaking the command protocol directly.

A delivery envelope carries a `dispatchNonce`. A device answering that command must now send the
same value back in its response envelope. An answer that omits it, or that names a dispatch the
command has already moved off, is **refused and recorded as a dead letter** rather than settling
the command.

The reason is a real defect, not tidiness: the same command can legitimately be published more
than once — released back to the queue and dispatched again — and without a nonce there is no way
to tell which dispatch an answer belongs to. An answer to a superseded dispatch was settling the
newer one with the older one's outcome.

:::caution How to tell whether this is safe for your fleet
Nothing in the platform can enumerate devices built elsewhere, so the platform counts them for you
instead. After upgrading, watch:

- **`devicechain_commanddelivery_command_delivery_responses_without_nonce_total`** — answers that
  named no dispatch at all. Mostly devices that have not been updated, and on a fleet where every
  device speaks the current contract this should be **zero**. It counts any answer with no nonce,
  though, so a duplicate or late answer to an already-settled command lands here too.
- **`devicechain_commanddelivery_command_delivery_responses_stale_nonce_total`** — answers naming a
  dispatch the command has moved off. The usual reading is that commands are being published more
  than once, which is the defect this change exists to fix; a device replaying an old outbox entry
  produces it too.

(The doubled `command_delivery` is not a typo — the series carries the platform namespace and the
functional area as prefixes, so the name above is what you paste into a query.)

A refused answer is **not discarded.** It is written as a dead letter, because the device's report
of what it did exists nowhere else — so you can find those answers while you work through the
first counter.
:::

If your devices use the **.NET/Unity SDK**, upgrading the SDK is the whole fix — it carries the
nonce for you in both directions. The LwM2M downlink adapter and the device simulator were updated
in the same change. The edge agent is unaffected: it sends telemetry and receives no commands.

A related counter arrives alongside them:
`devicechain_commanddelivery_command_delivery_responses_not_answerable_total` counts an answer that
named the dispatch its command is on and still could not settle it. No command state in today's
vocabulary produces that, so it should read **zero**; it exists to catch a state being added later
without anyone deciding whether an answer may settle it.

#### Services now refuse to start on things they used to accept

Each of these is a fail-closed correction, and each can stop a pod that previously ran:

| What | The condition that now refuses |
| --- | --- |
| Secret store | an instance root key that is well-formed but **wrong** — previously started and failed at the first secret it was asked for |
| Instance configuration | a **misspelled key** — previously discarded in silence, with the default applied |
| Instance configuration | `DC_SHUTDOWN_DRAIN_SECONDS` still set — the environment variable is gone; the value is `infrastructure.shutdown.drainSeconds` |
| Instance configuration | a shutdown drain window longer than **half** the pod's `terminationGracePeriodSeconds` |
| Listeners | two listeners on one port, or a device-facing `port: "0"` |
| Any HTTP listener | a port already in use — previously logged from inside a goroutine while the service reported a successful start |

The misspelled-key one is worth a moment. A typo in `maxSubscriptionMessageBytes` measurably
halved the effective frame ceiling with nothing logged — the key was dropped and the default
applied, which looks exactly like a working configuration. Strict decoding means you find out at
startup instead.

#### Metrics: eleven new series, none renamed or removed

Every series that existed in v0.15.0 keeps its exact name — nothing was renamed and nothing was
dropped, including through the change that gave each service its own metrics registry.

What is new: five counters on `command-delivery` (the two nonce counters above, plus exhausted
dispatches, answers a command's state could not accept, and responses lost because a dead letter
could not be written), two alarm dead-letter counters on `device-management`, one early-close
counter on `event-sources`, `is_serving` on `lwm2m-ingest`, and the two Sparkplug rebirth counters
below.

:::caution `is_leader` changes meaning on `lwm2m-ingest`, and the docs recommend alerting on it
The gauge is now raised when the replica **acquires** the lease, rather than after it has finished
building its term. A term build takes up to 30 seconds per bound tenant, so a replica that had
just won a failover previously reported `is_leader=0` for as long as 30 seconds per tenant while
actually holding the lease.

If you follow the `sum(devicechain_lwm2mingest_is_leader) != 1` alert the deployment guide
recommends, that false-leaderless window disappears. The new
**`devicechain_lwm2mingest_is_serving`** gauge is what now distinguishes "leader, still building its
term" from "leader and serving" — the state one gauge could not express. `is_leader == 1` with
`is_serving == 0` sustained is a leader wedged in its build. Note the chart ships no alerting rule
for either; this is guidance to author, not a rule you inherit.
:::

**`sparkplug-ingest` gains `rebirth_enqueued_total` and `rebirth_dropped_total`.** A saturated
rebirth queue used to be indistinguishable from an idle one on every series the service exported,
because `rebirth_requests_total` counts successful publishes — so saturation made it *stop rising*.
Read the new pair together: drops climbing while requests hold at a ceiling is fan-out outrunning a
healthy publisher; drops climbing while requests stay flat points at the broker connection.

#### Other behaviour worth knowing about

- **A command the platform cannot publish now fails.** Previously it cycled between queued and
  sent on every sweep until its TTL elapsed days later, and then recorded a timeout — which says a
  device did not answer, when nothing had ever been dispatched. It now stops at a bound (20 attempts
  by default, about ten minutes at the default 30-second sweep cadence) and records failure naming
  the platform. The bound is yours to change under
  `functionalAreas.command-delivery.config.maxDispatchFailures`.
- **A dead letter's `reason` for a blocked connector destination is now `unprocessable`** rather
  than `exhausted`. Update any alert or saved query keyed on the old value; existing records read
  back unchanged.
- **An alarm state change that could not be published is now dead-lettered and counted**, and
  `alarm_event_dead_letter_lost_total` joins the `DeadLetterWriteLost` alert.
- **An inbound message that cannot be decoded is no longer archived whole.** The record points at
  the original by subject and stream sequence instead.
- **GraphQL subscriptions are closed cleanly at shutdown** with a `1001` frame, and an inbound
  frame is now capped — `infrastructure.graphql.maxSubscriptionMessageBytes`, default 4 MiB. This
  is the only new chart value in the release, and it has a default.
- **Governance refresh is rate-bounded** — 50 lookups/sec with a burst of 100, per governed
  dimension, and a 10-second negative cache after a failed lookup. One resolver keeps roughly 3000
  tenants warm without ever reaching the bound. Past it, a tenant that has already been resolved
  keeps serving its **last-known value** rather than dropping to the platform default; only a tenant
  that has never resolved gets the default.
- **Separately**, a platform-default ingest ceiling of `0` is now floored to 100 messages/sec with a
  burst of 200, rather than admitting nothing. That is a different axis from the lookup rate above.
- **Shutdown is bounded** by a budget derived from the grace period, minus the drain window and a
  two-second margin, and honours cancellation throughout. **Consumer read loops** back off and then fail the process rather
  than spinning or retrying forever.
- **Two chart details that are easy to trip over.**
  `instance.config.infrastructure.metrics.httpPort` is **retired** — a document that still carries it
  logs a warning naming the key and starts normally, and the chart no longer writes it. And
  `instance.config.infrastructure.shutdown` is now **written for you** by the chart from the
  top-level `shutdownDrainSeconds` and `terminationGracePeriodSeconds`; setting that block by hand
  makes `helm` fail the render rather than silently disagreeing with the pod spec. If you supply the
  instance config through `instance.existingSecret`, that block is yours to add.
- **A pod that ends itself releases its leadership lease on the way out.** On `lwm2m-ingest` both
  paths that used to exit while still holding it are fixed. The 30-second wait before a replacement
  can take over is now what follows an **abrupt** loss — a node failure, a `kill -9` — not what
  follows a pod deciding to stop.

#### The published packages

The **.NET/Unity SDK carries the command-nonce change** described above; upgrading it is how a
device built on it keeps answering commands.

`@devicechain/client`, `@devicechain/dashboards`, `@devicechain/widgets` and `@devicechain/brand`
have no source changes in this release. One thing to know if you install `@devicechain/widgets`
yourself: its **`maplibre-gl` peer range moves from `^6.6.0` to `^6.7.0`**. If you pin maplibre-gl
at 6.6.x you will see an unmet-peer warning, or an install failure under a package manager that
enforces peers strictly. Nothing else about the packages changed.

### v0.17.0 — instances stop owning the cluster they run on {#v0170-upgrade}

🔴 **There is no in-place upgrade into `v0.17.0`.** Every instance built by `v0.16.0` or earlier
has to be destroyed and built again — `dcctl upgrade` refuses and prints the recipe rather than
doing anything partial. [The next section](#pre-declaration-recreate) is the one to read, and it
says what to export first: there is no path that preserves your telemetry, device definitions or
dashboards across this release.

What follows is what changes for you once you are on it.

#### `dcctl bootstrap` is now two commands

`dcctl install <provider>` prepares a **cluster**, once. `dcctl bootstrap <provider> <instance>`
builds an **instance** on a cluster that is already prepared, as many times as you want instances.

```bash
dcctl install local
dcctl bootstrap local my-instance
```

Everything that sizes or shapes the cluster moved to `install` and is recorded there, so `--ha`,
`--compact`, `--no-monitoring`, `--no-cnpg` and `--max-connections` are set **once** and every
instance on that cluster follows them. A bootstrap has no flags for them any more. `dcctl
bootstrap` refuses on a cluster where `install` has not completed, and the refusal names the
command to run.

`dcctl destroy` now tears down one instance's own infrastructure and **leaves the cluster
standing**. `--keep-cluster` is gone, because it describes what destroy always does now.

#### A future upgrade is two commands, and the first one is the cluster's

The operator moved with the split. It is **one controller per cluster**, shared by every instance
on it, so `dcctl install` is what puts it there and what moves it:

```bash
dcctl install local --version <new-version>
dcctl upgrade local <instance> --version <new-version>
```

`dcctl upgrade` no longer applies the operator. It **reads** the one the cluster is carrying and
refuses an instance whose cluster has no operator, or has one identifiably from another release,
naming the install command to run first. The reason is worth knowing if you run several instances
on one cluster: an upgrade that applied the operator itself moved it for **every** instance on
that cluster, silently, as a side effect of upgrading one.

There is one case it lets through with a warning instead. An operator you installed **by hand**
carries no record of which release put it there, and `dcctl` cannot tell that apart from one an
older `dcctl` overwrote — so it prints a note naming the install command and continues. If you did
not install it by hand, treat that note as the refusal it would otherwise have been.

#### One cluster now holds as many instances as you build

Each instance gets a namespace of its own — **`dci-<instance>`**, not the bare instance id — with
its own broker, its own event store, and its own login and database on the shared relational
store. Two things on a cluster can still belong to only one instance, and a bootstrap refuses
rather than colliding: the **ingress host**, and the local MQTT NodePort.

The prefix is why the namespace is not simply your instance id: an instance can no longer be given
a name that collides with a namespace the cluster itself uses.

#### If you grant `dcctl` explicit RBAC

On a cluster somebody else administers, `dcctl` needs verbs it did not before: **`list`, `patch`
and `delete`** on `instances.core.devicechain.io` alongside `get`, `create` and `update`, plus
**`list` on secrets** in `dc-system`. Every bootstrap and upgrade now asks the cluster which
instances it already holds and what they have claimed, and that question is a list. An account
holding only the previously documented set is refused partway through a bootstrap.

#### Two things that moved, and one that is gone

- **Grafana single sign-on through DeviceChain is gone.** Grafana is reached by port-forwarding its
  Service — there is no ingress route — and signed into with a per-cluster admin credential in the
  `dc-grafana-admin` Secret. See [Observability](./observability.md).
- **`dcctl`'s local state moved.** Per-instance records are under
  `~/.devicechain/instances/<instance>/`, and a new per-cluster directory
  `~/.devicechain/clusters/<cluster-uid>/` holds the cluster's own infrastructure state. That
  directory is keyed on the cluster's identity rather than its name, and it is the **only** copy of
  that state — no backup contains it. An installed cluster can only be re-installed from the
  machine that holds it. See [Install the cluster](./bootstrap.md#install).
- **A bootstrap asks the relational store what it already holds** before it mints a root key. A
  database sitting there under the instance's name can only have outlived the cluster that built
  it, so the bootstrap stops instead of minting a key that could not decrypt the rows already
  there.

### Instances built by v0.16.0 and earlier {#pre-declaration-recreate}

An instance bootstrapped by **`v0.16.0`, or by any release before it, cannot be upgraded onto
the release that follows `v0.16.0`**. `dcctl upgrade` refuses rather than trying.

`dcctl bootstrap` now records a **declaration** — a cluster-scoped object saying what the
instance *is*: its profile, its topology, how it is exposed, and which functional areas it
runs. `dcctl upgrade` reads that declaration to know what to deploy, which is what lets it
move a version without being told an instance's shape all over again. Releases up to
and including `v0.16.0` wrote no such record, so there is nothing for the upgrade to read.

It says so, rather than treating your instance as a name that does not exist:

```
instance "devicechain" IS in this cluster — named by the DeviceChain Helm releases in this
cluster — and it carries no declaration, so it was built by a release older than the one
that began recording them.
```

There is no compatibility shim, and before `v1.0.0` there will not be one. What the older
instance was configured with was never written down in a form this release can read, so a
declaration invented after the fact would be a guess applied over a live instance.

**To move onto this release, recreate the instance — and the cluster under it.** The
releases that built these instances had no `dcctl install`: they installed the cluster's
shared prerequisites as part of the instance, and recorded no install. So `dcctl destroy`
refuses to run `tofu destroy` over the state those releases wrote and needs `--without-state`, which still
leaves those prerequisites behind; `dcctl bootstrap` refuses a cluster with no install
record, and `dcctl install` would collide with what the older release left. Start from a
fresh cluster in between:

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

For a cluster reached with `--kube-context`, which `dcctl` never deletes, pass that
`--kube-context` to the `destroy` line as well — the reason is in the note below — then
delete and recreate the cluster with whatever created it, and pass the same `--kube-context`
to `install` and `bootstrap`. `dcctl upgrade` prints this same recipe when it refuses.

:::note This release's `dcctl` does not read the older release's local state
`dcctl` now keeps what it knows about an instance under
`~/.devicechain/instances/<instance>/`. Releases up to `v0.16.0` kept it one level up, at
`~/.devicechain/<instance>/`, and **the new `dcctl` does not read, list or remove that
directory** — there is deliberately no migration, because `dcctl` cannot tell an old
instance directory from one you made yourself. Three things follow for the recipe above:

- **`dcctl instances list` shows none of your older instances.** On a machine holding only
  instances built by `v0.16.0` or earlier it prints `No DeviceChain instances on this machine
  (nothing under ~/.devicechain/instances).` Nothing has been lost; the instances are still
  in their clusters, and `helm list -A` still shows their releases.
- **`destroy` guesses the cluster.** The cluster record the older release wrote is in the
  directory the new `dcctl` does not read, so `destroy` prints
  `No record of which cluster instance "<instance>" lives in — GUESSING cluster … from its
  name` and derives it from the instance name. That guess is right for a local instance
  named the way the recipe names it, and **wrong for one bootstrapped with `--kube-context`**,
  which is why that flag goes on the `destroy` line. `--without-state` is still needed: the
  infrastructure state is also in the directory `dcctl` no longer looks in.
- **The old directory stays on disk.** `destroy` removes `~/.devicechain/instances/<instance>/`
  — which for such an instance holds nothing — and leaves `~/.devicechain/<instance>/`
  where it was, with its `infra/terraform.tfstate` (the database superuser password and the
  broker's TLS private key, in cleartext) and `broker-credentials.json`. Once the instance is
  gone, remove it yourself:

  ```bash
  rm -rf ~/.devicechain/<instance>
  ```

  Do **not** touch `~/.devicechain/escrow/`. The root-key escrow artifact has always lived
  there, outside any instance directory, and it still opens that instance's database backups
  — see [Disaster Recovery](./disaster-recovery.md#after-destroy).
:::

:::caution Export first — recreation discards your data
The [destroy guard](#data-durability) protects the databases from an ordinary `helm`
operation, not from a deliberate `dcctl destroy`. If the instance holds telemetry, device
definitions or dashboards you care about, dump them before you start.
:::

:::tip The refused upgrade changes nothing
`dcctl upgrade` reads the instance before it writes anything, so the refusal lands before the
first change: the Helm release stays on the revision it was on, the operator keeps running
the image it was running, and every row is where it was. Running it to see what it says costs
nothing. Both halves of that — the refusal, and the instance being untouched afterwards — are
exercised against a real cluster on every release.
:::

Once you are on a release that records a declaration, ordinary in-place upgrades resume.
`dcctl instances list` shows what is declared, and in which cluster.

### v0.18.0 — what failed silently now says so, and ingest keeps up with its ceiling {#v0180-upgrade}

`v0.18.0` is an in-place upgrade from `v0.17.0`: `dcctl install` for the cluster, then `dcctl
upgrade` for each instance on it, as described [for v0.17.0](#v0170-upgrade). An instance built
by `v0.16.0` or earlier still has to be [destroyed and built again](#pre-declaration-recreate).

Much of what changed makes a failure visible that used to pass unnoticed: a lost message is now
counted or dead-lettered, a service that cannot do its work restarts instead of staying ready,
and a setting the platform cannot honour stops the service at startup. Events are also persisted
and merged into live state in batches, and resolved several at a time, so a default install keeps
up with the 1000 messages per second each tenant is allowed.

**Who has to do something:**

- **Everyone:** every user signs in again once, and OAuth clients, including AI assistants
  connected through MCP, authorize again. Nothing needs preparing for it.
- **Every instance built before this release:** check the superuser's password after the upgrade.
  The upgrade does not change it, and it may still be the old published default.
- **Anyone with webhook notification channels:** each one needs an `auth` key before the upgrade,
  or it stops delivering.
- **Anyone whose values set a key this release refuses**, set `resources` for a single service,
  sized the JetStream volume themselves, restricts which registries nodes can pull from, or lets
  tenant connectors reach private addresses: see [Before you upgrade](#v0180-before).
- **Anyone who calls the GraphQL API from their own code or scripts:** read [API and
  errors](#v0180-api). The alarm `message` field is gone, several refusals now carry a code, and
  some requests the server used to accept are refused.
- **Anyone who watches pod restart counts or searches service logs:** a service whose broker
  connection closes for good, or whose message reads keep failing for two minutes, now restarts
  instead of staying ready ([Messaging](#v0180-messaging)), and services now log at `info`, so
  debug lines you may search for are gone ([details](#v0180-log-level)).
- **Anyone who routes or silences alerts by name:** one alert is renamed and several are added;
  see [Alerts added and renamed](#v0180-alerts).

Everything else is under [What changed](#v0180-what-changed), by area. Each item says whether it
needs anything from you; most do not.

#### Before you upgrade {#v0180-before}

Do these in order. Each links to the item that has the details.

1. **Give every webhook notification channel an `auth` key.** A channel saved without one stops
   delivering at the upgrade, including one that works today. The current release accepts the key
   and ignores it, so adding it first leaves no gap. In each tenant, find the webhook channels
   whose `config` has no `auth`:

   ```graphql
   query {
     notificationChannels(criteria: {pageNumber: 1, pageSize: 100, channelType: "webhook"}) {
       results { token config hasSecret enabled }
       pagination { totalRecords }
     }
   }
   ```

   If `totalRecords` is more than 100, read the next `pageNumber` too. Which value to use is in
   [Webhook notification channels must say how they authenticate](#v0180-webhook-auth).
2. **Remove or change configuration the new services refuse.** Each of these stops a service from
   starting, and the error names the setting.
   - `dispatchBacklog` under `functionalAreas.outbound-connectors.config`: delete it
     ([details](#v0180-dispatch-backlog)).
   - `auth.superuserPassword` under `functionalAreas.user-management.config`: delete it
     ([details](#v0180-superuser)).
   - `checkpointIntervalSeconds` above 30 for `event-processing`: lower it
     ([details](#v0180-checkpoint-interval)).
   - A connection pool set at or below a new worker count. `event-management` refuses a
     `tsdbConfiguration.maxOpenConnections` of 5 or fewer, and `device-state` an
     `rdbConfiguration.maxOpenConnections` of 5 or fewer, unless you also set `persistence.writers`
     (event-management) or `projection.writers` (device-state) below it
     ([details](#v0180-batched-persistence)). `device-management` refuses an
     `rdbConfiguration.maxOpenConnections` of 10 or fewer unless you set `resolution.workers` below
     it ([details](#v0180-resolution-workers)).
3. **A chart-only `telemetry` or `ingest-only` install: set an instance root key.** The chart now
   refuses to render any profile without one. An instance built with `dcctl bootstrap` already has
   one ([details](#v0180-root-key)).
4. **If your values set `resources` for a single service, check the rendered pods.** A service's
   `resources` is now merged over the top-level `resources` key by key, instead of replacing it,
   and `device-management` and `event-management` get a 2-core CPU limit of their own. A top-level
   CPU limit above 2 cores therefore lowers those two services to 2 cores, and a namespace
   `ResourceQuota` or `LimitRange` you added can refuse them ([details](#v0180-cpu-limits)).
5. **If you sized the JetStream volume yourself, check its free room.** The reservation grows by
   128 MiB for sign-in counts, 128 MiB for MQTT connect counts, and 64 MiB for device-management's
   new cache bucket until you delete the two it replaces (16, 16 and 4 MiB on the compact preset).
   If there is not enough room, device-management does not start
   ([details](#v0180-profile-cache-bucket)). An instance installed with `--compact` keeps its 2Gi
   JetStream volume: `dcctl upgrade` does not re-apply an instance's infrastructure, so the volume
   is not resized, and the new buckets still fit in it. Only an instance built by this release's
   `dcctl bootstrap` gets the compact preset's new 3Gi volume. Do not delete the `dc-nats`
   StatefulSet to resize it: no dcctl command recreates it on an existing instance
   ([details](#v0180-mqtt-connect-backoff)).
6. **Make sure your nodes can pull from `cgr.dev`.** The in-cluster backup store now runs
   `cgr.dev/chainguard/minio`, pinned by digest; allow it through any egress rules, or mirror it at
   that digest ([details](#v0180-backup-store-image)). If you run `dcctl` where the OpenTofu
   provider registry cannot be reached, make it or a provider mirror reachable: every
   `dcctl install`, `dcctl bootstrap` and `dcctl destroy` now asks it for the pinned provider
   versions ([details](#v0180-dcctl-install-rerun)).
7. **If a tenant connector publishes to a private address, allow it.** MQTT, Kafka, SNS and SQS
   connectors can no longer reach loopback, private, carrier-grade NAT, link-local or
   cloud-metadata addresses, including Amazon MSK brokers, Amazon MQ and SNS or SQS through an
   interface endpoint with private DNS. Allow each address as its own `/32` under
   `instance.config.infrastructure.egress.allowedDestinations`. Check MQTT URL schemes and Kafka
   ACLs by client id at the same time ([details](#v0180-connector-egress)).
8. **Set `outboundMessagesPerSecond` and `outboundBurst` the same for `event-processing` and
   `outbound-connectors`.** Otherwise the new `ConnectorDispatchRateLimited` warning fires whenever
   a tenant metered at the platform default sends faster than the lower of the two
   ([details](#v0180-new-warnings)).
9. **Update GraphQL clients of your own.** Remove `message` from alarm selections and upgrade
   `@devicechain/dashboards` and `@devicechain/widgets` together with the platform
   ([details](#v0180-alarm-message)). Call `tenantDeletions` with its new criteria argument
   ([details](#v0180-tenant-deletions)). The other API changes, listed under [API and
   errors](#v0180-api), need attention only if your code relies on the old behaviour.
10. **Fix CEL conditions that are true for every device without an attribute.** A threshold or
    duration condition such as `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]` stops
    running at the upgrade. The corrected form is valid on the current release too
    ([details](#v0180-cel-attribute-conditions)).
11. **Check your broker's ACLs if you run an MQTT source on your own broker or a Sparkplug
    source.** A subscription the broker refuses now stops the whole `event-sources` service for an
    external MQTT source ([details](#v0180-external-mqtt-resubscribe)), and keeps a Sparkplug
    source offline ([details](#v0180-sparkplug-refused-group)).
12. **Update alert routes, silences and dashboards that name what changed.**
    `EventProcessingStreamNearFull` is renamed `JetStreamStreamNearFull`
    ([details](#v0180-unread-loss)). `JetStreamLeaseBucketNotReplicated` has a new summary
    ([details](#v0180-new-warnings)). The three dead-letter alerts moved to their own rule group
    ([details](#v0180-dead-letter-rule-group)). Five dead-letter loss series were replaced by one
    name per service ([details](#v0180-dead-letter-lost)).

#### During the upgrade {#v0180-during}

- **Everyone is signed out once.** Console, dashboard and SDK sessions end at their next refresh,
  within 15 minutes unless you have changed the access-token lifetime, and every access and refresh
  token issued before the upgrade stops working once the rollout completes. OAuth clients, MCP
  clients included, have to authorize again ([details](#v0180-sessions)).
- **For a few seconds, requests can be refused with `401 invalid or expired token`**, and a
  sign-in with `invalid or expired token`, even for a token issued moments earlier. This lasts
  until the last user-management pod of the previous release has stopped. Signing in again and
  retrying then succeeds ([details](#v0180-sessions)).
- **Several things restart once.** `device-management` and `event-management` restart one pod at a
  time for their new CPU limits ([details](#v0180-cpu-limits)). `dcctl install` restarts the
  relational store's instances, with a brief write outage under `--ha` and a longer one on a
  single instance ([details](#database-primary-failover-in-seconds)), and restarts the backup
  object store onto its new image, which pauses archiving while it pulls
  ([details](#v0180-backup-store-image)).
- **Alarms can be delayed or lost if the rollout stalls.** A `device-management` pod of the
  previous release cannot store a new alarm; the alarm is retried about once a minute and is
  normally stored by an upgraded pod. Keep the rollout short. A console tab opened before the
  upgrade gets an error on alarm lists until it is reloaded ([details](#v0180-alarm-message)).
- **LwM2M commands issued mid-rollout can be delayed by several minutes**, while `lwm2m-ingest`
  and `command-delivery` are on different releases ([details](#v0180-lwm2m-confirm)).
- **A profile publish, a rollback or a geofence edit can take up to one cache TTL to reach every
  `device-management` replica** while both releases are running
  ([details](#v0180-profile-cache-bucket)).
- **Some things can be recorded twice.** A give-up can be dead-lettered once by a pod of the
  previous release and once from the broker's notice ([details](#v0180-no-outcome-dead-letters)).
  An event with no `occurredTime` that was still unacknowledged can be stored twice
  ([details](#v0180-processed-time)).
- **A dead-letter alert that was pending or firing starts over** once its rule moves to the new
  group ([details](#v0180-dead-letter-rule-group)).

#### After the upgrade {#v0180-after}

None of these is required for the platform to run. Each cleans up something the upgrade leaves
behind.

- **Check the superuser's password.** On an instance with no generated password, the upgrade
  prints a warning at the end. If you never changed that superuser's password, it is still
  `devicechain`: sign in and change it ([details](#v0180-superuser)).
- **Delete the two cache buckets `device-management` no longer uses.** Each keeps its reservation
  until you delete it. With the `nats` CLI and a login that can manage JetStream in the platform's
  account:

  ```bash
  nats stream rm KV_<instance>_device-management_metric-defs-by-type
  nats stream rm KV_<instance>_device-management_profile-scope-by-type
  ```

  ([details](#v0180-profile-cache-bucket))
- **Give an older event store the new shutdown settings.** `dcctl upgrade` does not re-apply an
  instance's databases, so an instance bootstrapped before this release keeps the old ones on its
  event store until you patch it ([details](#database-primary-failover-in-seconds)).
- **Clear alarms raised by a CEL condition that is now refused.** The rule no longer runs, so
  nothing resolves them ([details](#v0180-cel-attribute-conditions)).
- **Look for alarms that were not raised during the rollout** with
  `dcctl dead-letters list --kind detection-action --source device-management`
  ([details](#v0180-alarm-message)).
- **Send again any device password that should begin or end with a space.** Values saved before
  this release were stored trimmed ([details](#v0180-credential-values)).
- **Give a provisioning profile stored with an empty secret a real one** with
  `updateProvisioningProfile` ([details](#v0180-provisioning-secret)).
- **Look for stored alert levels above 2147483647**, which now make the alert listing that
  includes them an error ([details](#v0180-int-range)).
- **Keep protecting backups taken before the upgrade.** They hold the token-signing keys this
  upgrade retires ([details](#v0180-sessions)).

#### What changed {#v0180-what-changed}

The rest of this section describes the changes an operator or an API caller can notice, grouped
by area.

#### Ingest and resolution {#v0180-ingest}

##### HTTP ingest has its own allowance {#v0180-http-allowance}

HTTP ingest requests are now metered against a per-tenant allowance of their own. Before this
release they spent the same allowance as the tenant's MQTT, NATS and broker presence traffic. HTTP
takes the tenant from the request path and checks the device credential only after the request is
admitted, so anyone who could reach port 8081 and knew a tenant's name could use that allowance up
and cause the tenant's MQTT telemetry to be dropped, including messages the broker had already
acknowledged to the device. Now such a caller can use up only the tenant's HTTP allowance.

This changes what a tier's ingest ceiling means. On each `event-sources` replica the ceiling
already applied separately to live device traffic and to a backlog drained after an outage, and
HTTP is now a third allowance beside them. A tenant can therefore be admitted at up to three times its ceiling per replica
in the worst case ([When ingest can admit a tenant above its
ceiling](../concepts/governance.md#ingest-above-ceiling)). If you size tier ceilings for billing or
capacity, allow for it.

Expose port 8081 only behind network controls, such as a NetworkPolicy or an ingress that
authenticates callers. The chart does not route it through its ingress, but by default any pod in
the cluster can reach it.

##### Invented tenant names on HTTP ingest no longer grow memory, and two new alerts {#v0180-unconfirmed-tenants}

The HTTP ingest endpoint used to create a separate rate allowance for every tenant name in a
request path, confirmed or not, so a stream of invented names could grow `event-sources`' memory
without bound. Names the control plane has not confirmed now share a fixed set of 1024 allowances,
and past that one shared allowance at the platform default. Tenants arriving over MQTT, NATS or
LwM2M are unaffected ([Tenant names that cannot be
confirmed](../concepts/governance.md#unconfirmed-tenants)).

Two warnings are added:

- `RateLimiterOverflowInUse` fires when the shared allowance is in use.
- `TenantsMeteredAtPlatformDefault` fires when a service has been unable to read tenants' ceilings
  from user-management for 15 minutes and is metering them at its platform default ([Before a
  tenant's ceiling is known](../concepts/governance.md#unresolved-ceilings)).

Each service now exports
`…_governance_unresolved_admissions_total{dimension,cause}`, and `event-sources` also exports
`…_ratelimit_overflow_admissions_total`.

A tenant's allowance is also now metered on one clock that never runs backwards. A service
draining a backlog on the time each message was sent could previously admit more than the ceiling
when those times went backwards (a broker leader change between servers whose clocks disagree)
or when a tenant's ceiling changed mid-drain; such messages are now charged
at the latest time the allowance has already seen, which can shed a little more but never admits
more.

##### `processedTime` now means when the platform received an event {#v0180-processed-time}

An event's `processedTime` (in GraphQL, and the `processed_time` column of the analytics `events`
view) is now the time the platform **received** the event. For MQTT on the platform's broker, that
is the moment the broker stored the message. Before this release it was the moment `event-sources`
decoded it. Normally the two differ by milliseconds, but after an `event-sources` outage they
differed by the whole outage.

An event sent with no `occurredTime` is now also dated when it was received, so readings that
waited in the platform during an outage keep the time they arrived rather than the time they were
processed. What follows from that:

- **Windowed detection rules can miss such readings after an outage.** Readings arriving over LwM2M
  or Sparkplug keep the detection engine's frontier at the current time while `event-sources` is
  down. When `event-sources` catches up, readings it dates an outage in the past arrive late to
  repeating, sliding-aggregate and correlation rules: they are stored and charted, but not counted
  in those windows, and `detect_late_samples_total` rises. Readings that carry their own
  `occurredTime` have always behaved this way. See [what "when" means to the detection
  engine](./detection-engine.md#timing-what-when-means).
- **Events decoded on both sides of the upgrade can be stored twice.** An event's id is derived
  from its content, including its `occurredTime`. A message with no `occurredTime` that was stored
  before the upgrade and is delivered again after it now gets a different time, and so a different
  id. This can happen only to messages that were still waiting to be acknowledged while the
  upgrade rolled out.
- Rows written before the upgrade are not changed.
- Measurement rollups place a backlog in the buckets for when it was received. The rollups are
  refreshed 30 days back, so nothing from a shorter outage is left out of them.
- Outbound actions are metered on when their telemetry reached the platform. For telemetry that
  waited out an `event-sources` outage, that is now when it arrived rather than when it was
  decoded. For a tenant whose only traffic waited in the broker, the catch-up is therefore not
  charged as one burst. A tenant that also sent telemetry over LwM2M or Sparkplug during the
  outage has already moved its outbound meter to the present, so its backlog is still charged
  together, as it was before.

##### Device events sent over MQTT are forwarded several at a time {#v0180-mqtt-forwarding}

Nothing needs doing at the upgrade.

- **`event-sources` keeps up to 128 publishes to inbound-events waiting for the broker at once**
  for the events devices send over MQTT to the platform broker, instead of at most five. A
  device's message is still acknowledged only after the event it carried has been stored, and one
  whose publish fails is still left for redelivery. Events sent over HTTP, and through an external
  MQTT broker you configured, are published as before.
- **When publishing to inbound-events keeps failing, `event-sources` slows down** the same way
  `device-management` does: after a failed publish it waits half a second, doubling up to two
  seconds, and until a publish succeeds it sends one at a time.
- **More device messages can be redelivered after `event-sources` stops abruptly:** up to the 128
  that were waiting for the broker, on top of those it held before. Each carries the same
  duplicate-detection id as before, so an event that was already stored is not stored twice.
- **A device message that failed on every delivery is still routed to failed-decode, at most four
  at a time.** One that arrives while four are being routed is left for the broker to end, and is
  recorded as a dead letter instead of on failed-decode.
- **A device's events can reach inbound-events slightly out of order, as they could before:** five
  decoders work through the captured messages at once, and every replica publishes.
- **`devicechain_eventsources_jetstream_publish_duration_seconds` gains a `mode="pipelined"`
  series for `suffix="inbound-events"`.** [Observability](./observability.md) describes the modes.

##### An MQTT source on your own broker subscribes again after a reconnect {#v0180-external-mqtt-resubscribe}

An MQTT event source that reads from a broker you run subscribed once, when it started. The client
reconnects on its own with a clean session, so after a broker restart or a network drop the broker
held no subscription for it: the source stayed connected, reported nothing wrong, and ingested
nothing until the pod restarted. It now subscribes again on every connection. The default
install's own gateway source reads from the platform's stream and was not affected.

- **A broker that refuses the subscription after a reconnect now stops the whole `event-sources`
  service**, and so does one that never acknowledges it. Kubernetes restarts the pod, and a broker
  that still refuses at start stops it again, as it already did. An ACL change on your broker
  therefore shows as a crash loop, and HTTP and platform-broker ingest go down with it. Check
  before upgrading that the source's credential may still subscribe to its topics. See [Transport
  matrix](../reference/transport-matrix.md).
- A connection that drops while the subscription is being sent is left to the client's reconnect.
- **When broker presence cannot recover, `event-sources` now stops cleanly.** It used to exit at
  once, skipping its readiness drain, every source's stop, the GraphQL server and the broker
  drain. It now runs all of them before it exits, and the pod restarts as before ([Device
  presence](../concepts/device-presence.md)).

##### A Sparkplug source with a refused group stays offline {#v0180-sparkplug-refused-group}

If the broker accepts a Sparkplug source's connection but refuses its subscription to
any one of the source's groups (most often because the source's credential may not read that
group), the source no longer announces itself online. It ingests none of its groups, disconnects,
and retries with a growing wait of up to 30 seconds until every group is granted. One refused group
therefore stops that whole source until the broker's ACL is fixed. The same happens if the broker
does not acknowledge the online announcement itself. Before it disconnects, the source publishes its
offline state, so an online announcement the broker kept without acknowledging it does not linger.

Before, the source announced itself online with the group missing. That group's edge nodes then
flushed their buffered data into a subscription that did not exist, and the source later marked
their devices disconnected for staying silent. A new counter,
`devicechain_sparkplugingest_subscribe_failures_total`, counts the abandoned sessions: alert on any
increase. The log line names the refused group. If you watch the Sparkplug host state, a source with
a refused group now shows as offline rather than online. [Edge services](./edge-services.md) has
the details.

##### Resolved events are published several at a time {#v0180-resolved-publish}

Nothing needs doing at the upgrade.

- **`device-management` keeps up to 128 resolved-event publishes waiting for the broker at once,**
  instead of waiting for each before sending the next, so one pod's resolution is no longer held
  to one publish round trip at a time. An inbound event is still acknowledged only after every
  resolved event it produced has been stored, and one whose publish fails is still left for
  redelivery.
- **A resolved event is stored once when its inbound event is redelivered.** Each resolved publish
  carries a duplicate-detection id derived from its inbound event, so when a publish was stored but
  its acknowledgement was lost, the copy published on redelivery is dropped by the broker rather
  than stored twice. The broker keeps each id for two minutes, which costs NATS memory for every
  resolved event published in that window, about 100 bytes each as measured in-process.
- **A failed event is acknowledged only after its record is stored on the failed-events stream.**
  Before, the inbound event was acknowledged when its record was handed over for publishing, so a
  record that failed to publish was lost. Now the inbound event is redelivered, or at its last
  delivery recorded as a dead letter. The record carries the same kind of duplicate-detection id,
  so a redelivery does not record the failure twice.
- **When publishing to resolved-events keeps failing, `device-management` slows down** rather than
  failing its whole inbound backlog at full speed: after a failed publish it waits half a second,
  doubling up to two seconds, and until a publish succeeds it sends one at a time. Publishes that
  failed together, as every publish in flight does when the connection to the broker drops, share
  one wait.
- **A device's resolved events can reach the stream slightly out of order,** and could before:
  every replica publishes, and a rolling update runs two pods at once. Detection's
  [`watermarkLatenessSeconds`](./detection-engine.md) tolerates that. An event whose publish
  failed is published again at least 60 seconds later, which is beyond the default.
- **A new histogram, `devicechain_<area>_jetstream_publish_duration_seconds{suffix, mode}`,**
  measures every JetStream publish. [Observability](./observability.md) describes it.

##### device-management resolves more events at once {#v0180-resolution-workers}

`device-management` resolved inbound events with five resolvers, a number fixed in code. Resolving
an event is mostly waiting: for the database to authenticate its credential, then for the message
broker's key-value store to return its profile and relationships, one after another. So five
resolvers limited how many events a pod could resolve a second while most of its CPU sat idle. On a
test cluster the pod resolved about 1600 events a second on 1.5 of its 4 cores, and the events above
that rate waited in the pod, about 140 at a time, then in the stream, with detection falling behind
them. It now runs 10 by default, and the number is configurable. Measured in-process against a
three-server broker, with every lookup taking 750 µs, 5 resolvers resolved about 1500 events a
second and 10 about 2900. See [Event resolution](./observability.md#event-resolution).

- **The new setting is `resolution.workers`** (default `10`). It must be below the service's
  connection pool (`rdbConfiguration.maxOpenConnections`, 20 unless set). A value out of range stops
  the service from starting, and the error names the setting. The default is refused only if you set
  `maxOpenConnections` for `device-management` to 10 or fewer: set `resolution.workers` below it
  before upgrading.
- **device-management holds more database connections while it resolves events.** Each resolver
  holds one while it authenticates an event's credential, which under the default `required` device
  authentication is every event. With every resolver busy that is now up to 10 connections instead
  of 5, from the pool the GraphQL API, the MQTT connect checks and the consumer that applies alarm
  raises and resolves also use. If you set
  `maxOpenConnections` below 20, check that what is left is enough for them. More than half the pool
  is allowed, and logged at startup.
- **A pod at its CPU limit gains nothing from more resolvers.** This raises the rate only where the
  pod has CPU to spare.
- **A new metric, `resolve_workers`,** reports how many resolvers the pod runs, and
  `resolve_inflight` can now reach 10. `resolve_inflight` held at `resolve_workers` means events are
  arriving faster than the pod resolves them.
- **Rolling back:** an earlier `device-management` refuses a configuration that sets
  `resolution.workers`, as it refuses any setting it does not know. Remove the setting before
  rolling back.

##### device-management keeps one cache bucket per device type instead of two {#v0180-profile-cache-bucket}

The cached metric definitions and rule scope of a device type are now one key-value bucket,
`<instance>_device-management_profile-resolution-by-type`, so each measurement event reads its
device type's published profile once instead of three times, and one event can no longer be
validated against one profile version and labelled with another. Nothing needs doing unless you
size the JetStream volume yourself or manage buckets by hand.

- **The upgrade adds one cache bucket to the JetStream reservation** (64 MiB by default, 4 MiB on
  the compact preset). device-management creates the new bucket when it starts, and the two it
  replaces keep their reservation until you delete them (see the next item). If the JetStream
  volume has less free room than one cache bucket, creating the new bucket fails for lack of
  storage and device-management does not start. Once the two old buckets are deleted, the
  reservation is one cache bucket lower than before the upgrade, which is also what a fresh
  install reserves.
- **An upgraded instance keeps the two buckets this one replaces**:
  `<instance>_device-management_metric-defs-by-type` and
  `<instance>_device-management_profile-scope-by-type`. Nothing writes to them after the upgrade,
  and their entries expire within the cache TTL the buckets were created with (60 seconds unless
  `metricDefCacheTtlSeconds` was changed before this upgrade), but each keeps reserving its
  ceiling until you delete it. Deleting them needs the `nats` CLI with a login that can manage
  JetStream in the platform's account; `dcctl` has no command for it:
  `nats stream rm KV_<instance>_device-management_metric-defs-by-type` and
  `nats stream rm KV_<instance>_device-management_profile-scope-by-type`. Leaving them costs only
  that reservation. A tenant deletion still clears them for one more release.
- **While the upgrade is rolling**, a profile published or rolled back, or a geofence edit, can take
  up to one cache TTL to reach every device-management replica: a replica of the previous release
  clears only the old buckets, and a replica of this one only the new bucket. A geofence edit
  missed this way means location events are stamped with the previous fence set for up to that
  TTL.

##### device-management keeps resolving events when a NATS server drops off the network {#v0180-nats-server-drop}

Nothing needs doing at the upgrade.

- **A lookup in one of `device-management`'s key-value caches waits at most half a second,**
  instead of the five seconds a NATS request is allowed. When a NATS server dropped off the
  network without closing its connections, some of these lookups were sent to it and each waited
  the full five seconds, so event resolution slowed to a few events a second for about a minute,
  and nothing was logged.
- **A cache that times out, or that no server answers for, is skipped for five seconds,** and its
  lookups go to the database. `device-management` logs a warning when that starts and a line when
  the cache answers again. See [Caches that stop answering](./observability.md#kv-caches) for the
  four new metrics.
- **Removing a cache entry after a change is never skipped,** and a removal that fails is now
  logged (`A key-value cache eviction failed`); before, a failure was silent.
- **Resolving an event that takes longer than five seconds is now logged as a warning**
  (`Event resolution is slow`), at most once every 30 seconds.

#### Detection {#v0180-detection}

##### A threshold or duration condition that is true for every device without an attribute is now refused {#v0180-cel-attribute-conditions}

**Detection rules.** A threshold or duration condition written in CEL that would be true on every
event from every device that lacks the attributes it reads, whatever the event carries, is now
refused when the profile is published. The shape is usually a negated presence test joined with
`||`, for example `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]`, or a negated presence
test on its own. Such a rule raised an alarm for every device without the attribute, whatever the
device reported, and kept it raised for as long as the attribute was missing. That includes devices
whose attribute was set to something other than a number or with `CLIENT` scope, not only devices
that never set one.

What you will see:

- Publishing a profile containing such a rule fails, and the error names the condition and says why.
- A rule of this shape that was published before the upgrade, including one published while the
  upgrade is rolling out, **stops running** at the upgrade. Rule health reports it as
  `COMPILE_ERROR` with the same reason, and the `event-processing` log records a line beginning
  `Published detection rule failed to compile; skipping`.
- **Alarms such a rule had already raised stay active until you clear them.** The rule no longer
  runs, so nothing resolves them.
- Rolling a profile back to a version published before the upgrade brings such a rule back as
  `COMPILE_ERROR`. Publish a fixed version instead.

Not affected: dynamic thresholds built on the form or the canvas; CEL conditions that still test the
event, such as `!("tempLimit" in attr) && m["temp"] > 80.0`; and a condition used as a filter on a
repeating, rate-of-change, windowed-aggregate or area-correlation rule, such as `!("maint" in attr)`.

To fix a refused rule, test the attribute positively, or write the fallback as its own comparison
(`"temp" in m && ("tempLimit" in attr ? m["temp"] > attr["tempLimit"] : m["temp"] > 80.0)`), then
publish the profile again. See
[Dynamic thresholds in a CEL expression](../concepts/event-processing.md#dynamic-thresholds-in-cel).

The preview documentation is corrected too. Preview resolves no device attributes, so a CEL fallback
previews its fallback on every device; it does not preview as never firing.

##### Duration rules place late readings by their own time {#v0180-duration-late-readings}

A duration rule ("temperature above 80 for 10 minutes") now places each reading by the time it was
taken rather than the order it arrived in, and discards a reading that meets its condition but is
further behind the detection engine's frontier than the rule's hold time. Before this release a
late reading was applied as if it were the newest one:

- **A late reading that did not meet the condition cancelled a hold that newer readings still
  supported.** A device uploading buffered readings could delay a duration alarm by up to a full
  hold each time, or keep it from raising at all while the condition held throughout. Now a late
  reading older than the run is ignored, and one showing that the condition stopped part-way
  through restarts the run from the newest reading that met it.
- **A late reading that met the condition could open a run across a break the engine had already
  seen**, and raise an alarm the readings did not support. It is now ignored.
- **A reading from long before the frontier could open a run whose hold had already passed**, and
  raise the alarm at the next event. It is now discarded and counted on
  `detect_late_samples_total`, whose description now names duration rules alongside the sliding
  kinds.

A reading that does not meet the condition still ends a raised duration alarm however late it
arrives, unless it is older than the alarm: a late reading from before the alarm was raised,
arriving after it, does not withdraw it, and is counted on `detect_late_samples_total` when it was
taken inside the run that raised the alarm (one older than the whole run is ignored). Because
readings are now placed where they belong, a duration alarm can raise earlier or later than it did
before, depending on the order its readings arrived in; it no longer raises on a run the readings
show was broken. As before, an episode is certain to raise only if it lasts its hold time plus the
lateness tolerance. See [what "when" means to the detection engine](./detection-engine.md#timing-what-when-means).

Because the frontier is shared by the whole instance, a device whose timestamps consistently trail
the rest of the fleet by more than a duration rule's hold time plus the lateness tolerance, from a
slow clock or a slow path, never raises that rule: each of its readings that meets the condition is discarded and counted as
late. Before this release such readings were applied.

The canvas preview runs with no lateness tolerance, so it discards a late reading with no margin.
It now says how many readings it set aside as late, for duration rules and the sliding kinds.

**What it costs.** To tell a late reading from a break, a duration rule now keeps a small record
and an expiry timer for every device that sends the rule's metric without meeting its condition,
until one hold time after that device's newest such reading. Before, such a device held nothing.
For a device that reports at least once per hold, the record is therefore permanent while it keeps
reporting:

- **Two live keys per device per duration rule**, counted toward the per-tenant live-key ceiling,
  which is measured and not enforced. A tenant with 100,000 reporting devices under five duration
  rules reaches the default `maxLiveKeysPerTenant` of 1,000,000 on this alone, and the
  `DetectTenantOverStateBudget` warning then fires. Raise `maxLiveKeysPerTenant` if that is your
  fleet.
- **About 290 bytes of detection checkpoint per device per duration rule**
  (`detect_snapshot_bytes`), measured in-process with a 26-character rule id and 19-character
  device tokens. It grows with the length of both.
- **The frontier moves on every idle interval.** Those expiry timers are pending work, so an
  instance with a duration rule and any device reporting its metric advances and checkpoints the
  frontier on a quiet stream instead of staying at rest.

A raised device also keeps its run until the condition stops, where it used to give it up at the
raise.

A checkpoint written before the upgrade restores unchanged, including any duration alarm raised at
the time. Rolling back to the previous release afterwards does not turn the new records into
alarms: it reads only the open runs from the checkpoint and ignores the rest. Nothing needs doing
at the upgrade.

##### A failing action no longer stops a rule's other actions {#v0180-failing-action}

Nothing needs doing at the upgrade. Read this if any rule lists more than one action.

Before this release a rule's actions ran in the order they were listed, and the first one that
failed stopped the rest. On each retry the actions before it ran again and the actions after it
never ran at all, so a command that could not be enqueued for a few minutes — for example because
the tenant was at its held-command limit — meant an alarm listed after it was never raised. Every
action is now attempted on every delivery, whatever happens to the others.

- **A rule no longer needs its most important action listed first.** Rules reordered to work
  around this can stay as they are.
- **A rule that relied on the old behaviour to run an action only when an earlier one succeeded no
  longer gets that:** every action runs regardless of the others.
- **A retry by the detection engine no longer sends a webhook or connector publish a second time,
  within about ten minutes of the first attempt.** Alarm updates and connector requests sent again
  in that time are recognised by the message bus and stored once. The two streams involved are
  reconfigured automatically when the new version starts; remembering each request for ten minutes
  costs NATS memory in proportion to how often rules fire. Past that window, and when the connectors
  service itself retries a call, a request can still reach its destination twice.
- **While one of a detection's actions keeps failing, each retry charges its webhook and connector
  actions against the tenant's outbound rate again,** even though the re-sent request is stored
  once. A sustained failure, such as a tenant at its held-command limit, can therefore cause sheds
  in the tenant's other rules.
- **A detection that is dead-lettered after its retries now names, in the letter's detail, each
  action that failed on the final attempt,** by kind and idempotency key, as `sendCommand/failed/<key>`
  (and `httpCall/shed/<key>` for one the outbound rate refused), the same form shed letters use. The
  `ReactPoisonDropping` alert's summary and description are reworded to match.
- **The detection engine now fetches one detection at a time from the message bus,** so a detection
  is never left waiting behind a slow one long enough to be delivered twice, and an attempt against
  a service that does not answer ends when its delivery does instead of running on. Each action
  gets its share of that time, so commands to a service that does not answer cannot use it all up
  before an alarm listed after them is raised.

##### Outbound actions are no longer dropped when the detection engine catches up {#v0180-catch-up-metering}

After a restart, rollout or failover, the detection engine works through the telemetry that arrived
while it was down. Outbound webhook and connector actions from that backlog used to be counted
against the tenant's outbound rate as if they had all happened at once, so most of them were dropped
with only a metric as a record. They are now metered, both where they are triggered and in the
connectors service, on the time the telemetry reached the platform. A tenant within its limit loses
nothing to a catch-up and is not slowed by it.

An action that is still over the limit is recorded as a dead letter with reason `shed` and kind
`detection-action`, up to about one letter a second per tenant (60 at once) and ten a second in
total. Beyond that the actions are counted and summarised in one letter per tenant per minute. Four
settings on `event-processing` tune the budget: `shedLetterPerSecond`, `shedLetterBurst`,
`shedLetterGlobalPerSecond` and `shedLetterGlobalBurst`. Two warnings are added:
`ReactShedLettersOverBudget` and `RateMeteringClockFallback`.

Detections the engine re-publishes after a restart are now recognised by the message bus and stored
once within a 30-minute window, so subscribers to the derived-events feed see fewer duplicates. Each
derived event now carries a `triggeredAt` field. If you filter dead letters by reason, expect `shed`
letters of kind `detection-action`. Nothing needs doing at the upgrade.

##### With a warm standby, only the replica running detection dispatches actions {#v0180-warm-standby}

On an `event-processing` deployment with a warm standby, the standby used to take a share of
detection actions (commands, alarms and connector calls) and charge the connector calls against
its own copy of each tenant's outbound ceiling, so a tenant could reach up to twice that ceiling.
Actions are now dispatched only by the replica that holds the detection partition. When the
partition moves, both replicas can dispatch for up to about five seconds, and a connector call
made twice in that window reaches its destination twice.

With one replica (the default) nothing changes, except after the pod stops without a graceful
shutdown (a crash or an out-of-memory kill). The actions waiting to be dispatched then resume when
the replacement takes the partition, up to about 35 seconds later, rather than as soon as the
replacement starts. Detection resumes after a further handover wait and the replay, as it did
before this release.

Rate ceilings in `event-sources`, `outbound-connectors` and `ai-inference` are enforced by each
replica separately. This is now documented under
[Governance](../concepts/governance.md#per-replica), and matters if you run more than one replica
of those services.

##### `checkpointIntervalSeconds` is capped at 30, and some silent failures now warn {#v0180-checkpoint-interval}

`event-processing` now refuses to start when `checkpointIntervalSeconds` is above 30. The
detection engine acknowledges its input only when it checkpoints, so an interval near or past the
broker's 60-second acknowledgement window held messages on a quiet stream until the broker
delivered them again, and after five windows counted them as exhausted deliveries, which raises
`ReplayCoveredDeliveriesExhausted` on a healthy engine. 30 leaves room for the checkpoint itself. The limit is a startup refusal rather than
a chart check, so `helm upgrade` with a larger value succeeds and the pod then fails to start. If
your values set it higher, lower it before upgrading. The default (10) is unaffected.

Some failures that were silent at the default log level now log a warning:

- a failed sample of a stream's or KV bucket's size and replication, or of a consumer's unread
  loss;
- a failed sample of the detection engine's consumer lag;
- device authentication at the broker that failed for a reason other than the device's own
  credential: the credential store failing, or a stored credential that has no secret and so can
  never authenticate.

A device presenting a wrong, unknown, expired or revoked credential is still logged only at debug.
During a broker outage the sampling warnings repeat on every sampling pass, about every 30 seconds
per stream; a sample interrupted by shutdown stays at debug. During a credential-store (database)
outage the authentication warning repeats once per device connect attempt, so a fleet reconnecting
through the outage logs one warning per attempt.

##### Lost rule, device and attribute changes are repaired automatically {#v0180-fact-repair}

Publishing or rolling back a device profile, creating or re-typing a device, re-pointing a device
type at another profile, and setting a threshold attribute each notify the detection engine once.
Before this release a lost notification was never retried: a newly published profile version ran
**no rules at all**, a device that never reported was never watched for silence, and a dynamic
threshold kept its old value — with nothing marked failed and no alert. The documented remedy was
to publish the profile again.

`event-processing` now compares its copy of each tenant's published rules, active profile versions,
devices and threshold attributes with `device-management` when it takes over detection and every
five minutes after, and corrects what differs. A lost change is repaired within about seven
minutes, and a deleted device or attribute within about ten. You no longer need to republish
after a broker disruption.

What you will see:

- **The upgrade adds two columns to `device-management`'s database**: when each profile's active
  version became active, and when each device's current profile membership began. Both are
  nullable and filled in by use, so the migration is quick on any fleet size and a replica of the
  previous release keeps working during the rollout. A profile whose active version was chosen
  before the upgrade is treated as active since that version was published — or, if it was rolled
  back to, since just after the newest version was published.
- **Three new warnings**: `DeviceFactPublishFailing` (notifications are failing to send),
  `DetectFactsRepaired` (the engine corrected something it had not been told about) and
  `DetectFactReconcileFailing` (the comparison itself is failing). `DetectFactsRepaired` counts
  only a real loss — a change whose notification is merely still on its way is left for the next
  comparison — so it fires after the upgrade only if notifications had in fact been lost before it.
- `device-management` exports `fact_publish_failures_total`, and `event-processing` exports
  `detect_fact_reconcile_repairs_total` and `detect_fact_reconcile_failures_total`.
- A rollback now carries the moment it was made as stored by `device-management`. Each publish or
  rollback of a profile is stamped later than the one before it, even when the replicas that made
  them have clocks that disagree. Two changes to the same profile made at the same instant are the
  exception: either may keep the earlier moment, and the detection engine still ends up on the
  version `device-management` stores.
- `event-processing` now also calls `user-management` to list tenants, and `device-management` to
  read rules, devices and attributes, with the service secret it already uses for geofences. If the
  service secret or either address is not configured, the comparison is off and the service logs a
  warning at startup, as geofence evaluation does.

##### The automation canvas authors Connectivity rules, and will not save over a rule it cannot show in full {#v0180-canvas}

Nothing needs doing at the upgrade.

- **The canvas has a Connectivity node,** so a "device went offline" rule can be built there as well
  as in the form builder.
- **When the canvas opens an existing rule, it checks that saving would keep the whole rule.**
  Before, a rule of a type the canvas could not show opened as an empty canvas with no explanation,
  and a rule with a field or action type the canvas does not model opened without it. A save from
  the canvas then replaced the stored rule with the reduced one. Now the canvas explains what it
  cannot show and turns saving off for that rule; edit it through the API instead.
- **A canvas-built rule whose definition was changed through the API is laid out again from that
  definition** when the canvas opens it, rather than from its older saved layout, so a canvas save
  no longer undoes the change. The canvas says when it has done this. The exception is a saved
  canvas that no longer compiles, which cannot be compared with the rule: it opens as it was, and
  its note warns that saving it undoes any such change.
- **The canvas keeps an alarm-key template** set through the API. It shows the template read-only
  and saves it unchanged.
- **A canvas save no longer clears a rule's name or description** when the rule's definition does
  not carry them. They are sent only when you edit them on the canvas.

#### Commands {#v0180-commands}

##### Command responses that could not be recorded are dead-lettered again {#v0180-command-responses}

In `v0.16.0` and `v0.17.0`, command-delivery could not write a single dead letter. Every one it
tried was refused before it was written, counted as lost, and the device's answer was gone. That
meant `DeadLetterWriteLost` could fire while the broker was healthy. That case is fixed, and it
changes what happens to the commands involved:

- A response that could not be recorded after every attempt is listed as a dead letter, and its
  command now moves to `FAILED`, with an error saying the device answered and the answer was lost.
  Before, such a command stayed in flight until something else settled it.
- A response that named no dispatch, or a dispatch its command had already moved off, is listed as
  a dead letter and settles nothing. The command is left as it was.

##### LwM2M commands are confirmed immediately before they reach the device {#v0180-lwm2m-confirm}

A command delivered to an LwM2M device is now confirmed with command-delivery immediately before
the adapter carries it out. A delivery the platform has already re-armed or re-sent is discarded
instead of reaching the device a second time. Such a delivery can turn up late after an outage or a
failover, and before this release it could actuate a device that a later delivery had already
actuated. If command-delivery cannot be reached, LwM2M commands wait and are retried. They are never
sent unconfirmed.

What changes that you can see:

- **Cancelling a batch now also stops LwM2M commands that were published but had not yet reached
  their device.** They record `CANCELLED`. The cancel result still counts them as already sent,
  because that is what they were when the cancel ran.
- **For an LwM2M command, `sentTime` now records when the device was actually sent it.** That can be
  later than when the command was first published.
- **`lwm2m-ingest` refuses to start without `infrastructure.commandDelivery`** whenever it has device
  identities to serve. The chart always sets it, so only a hand-built configuration is affected.
- **Metrics.** `lwm2m-ingest` adds `devicechain_lwm2mingest_commands_stale_dispatch_total` (deliveries discarded because the
  platform had already moved on: a duplicate actuation avoided, not a fault) and
  `devicechain_lwm2mingest_command_live_claim_errors_total` (commands not carried out because command-delivery could not
  confirm them). `devicechain_lwm2mingest_command_drain_dedup_total` is removed.
- **During the upgrade itself,** a new `lwm2m-ingest` cannot confirm commands with a command-delivery
  that is still on the previous version. An LwM2M command issued in that window can be delayed by
  several minutes. If it runs out of retries before both services are upgraded, it is re-armed and
  delivered on the device's next wake.

Nothing needs doing at the upgrade.

##### A slow LwM2M device no longer holds up other devices' commands {#v0180-lwm2m-slow-device}

When an LwM2M device is slow to answer and its commands pile up, further commands for it are now
set aside in command-delivery and delivered in order moments later. Before this release they
stalled the whole adapter: every other LwM2M device's commands waited behind the slow one.
Commands for a device that has no connection are set aside the same way, without taking up room
that connected devices need.

What changes that you can see:

- **A connected device receives the rest of a long backlog without reconnecting.** Its waiting
  commands used to be delivered 32 per wake, and the rest waited for the device to wake again,
  which a device that stays connected never does. They are now delivered a few at a time, taking
  turns with other devices' commands, until the backlog is empty.
- **One device's commands still arrive in the order they were sent.** This now also holds when a
  command could not be confirmed with command-delivery and is retried: the commands after it wait
  for it. Two cases can still deliver a command after the ones sent behind it, and both need
  command-delivery to be failing. One is an outage that outlasts the command's retries. The other
  is a confirmation that command-delivery recorded but whose answer never reached `lwm2m-ingest`,
  for example because the request timed out. In both cases the command stays sent but not carried
  out until the platform finds it stranded, and it is then delivered on the device's next
  connection, after the later commands, or expires.
- **After a failover, commands sent while devices reconnect are delivered a moment later.** Each
  device's waiting commands are delivered before any new one, so for that short window new
  commands are set aside too. Expect a brief rise in command-delivery traffic after a failover.
- **A command set aside this way shows the status `PARKED`,** even though its device is connected.
  Before this release `PARKED` meant only that the device had no live connection. See
  [Commands](../concepts/commands.md).
- **Metrics.** `lwm2m-ingest` adds `devicechain_lwm2mingest_commands_overflow_parked_total`, with a
  `reason` label (`full`, `offline`, `bind`, `unconfirmed`),
  `devicechain_lwm2mingest_command_overflow_blocked_total` (the adapter waited because
  command-delivery was slow) and `devicechain_lwm2mingest_command_drain_turns_total`.
  `devicechain_lwm2mingest_command_drain_dropped_total` is removed: a device's request for its
  waiting commands is no longer dropped when the adapter is busy.

Nothing needs doing at the upgrade.

#### Connectors and notifications {#v0180-connectors}

##### Connectors can no longer reach private addresses, and the connectors service has new clients {#v0180-connector-egress}

MQTT, Kafka, SNS and SQS connectors now get the same connect-time check that webhooks and mail
relays already had. A destination that resolves to a loopback, private, carrier-grade NAT,
link-local or cloud-metadata address is refused. The refusal is **final**: the dispatch is
dead-lettered as `blocked` and not retried. For Kafka this covers every broker the cluster
advertises, not only the addresses you configured. The check runs in the service itself, so it no
longer depends on `networkPolicy.enabled` or on your cluster enforcing it.

What to check before upgrading:

- **Destinations on private addresses stop receiving.** This includes:
  - an in-cluster or peered broker;
  - **Amazon MSK brokers**, which are private by default;
  - **Amazon MQ** used for MQTT;
  - **SNS and SQS reached through an interface VPC endpoint with private DNS enabled**. With
    private DNS, even the default `sns.<region>.amazonaws.com` / `sqs.<region>.amazonaws.com`
    names resolve to private addresses, so connectors with no endpoint override are affected too.

  Allow each address as its own `/32` under
  `instance.config.infrastructure.egress.allowedDestinations`. An interface endpoint has one
  address per availability zone, and each needs its own entry. An allowance applies to every
  tenant and every connector and webhook path, not only the one you have in mind.
- **MQTT URLs must use `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://` or `wss://`**,
  with an explicit port and one broker per entry. `tcps://`, `mqtt+ssl://` and `unix://` are no
  longer accepted. They are refused when a connector is saved, and a stored connector that uses
  one is dead-lettered as `invalid` when it fires. Kafka addresses must be `host:port`.
- **Proxy environment variables** (`HTTPS_PROXY`, `ALL_PROXY`) are no longer used by connectors,
  and neither are `AWS_*` variables or AWS config files on the pod.
- **The Kafka client changed.**
  - The default client id is now `devicechain` (it was `bento`). Set `clientId` on the connector
    if your brokers apply ACLs or quotas by client id.
  - Records without a key are now spread with sticky partitioning.
  - The protocol version is negotiated with the broker rather than fixed.
  - Delivery is unchanged: leader acknowledgement, no idempotent producer.
- **SQS messages are sent one at a time** (`SendMessage`), not in batches. The IAM permission is
  the same `sqs:SendMessage`.

Two things improve as a side effect. A Kafka broker that is briefly unreachable is now retried
rather than dead-lettered as `invalid`. And the connectors service's binary, which is most of its
image, is about a third of its previous size.

##### The connectors service no longer accepts dispatchBacklog {#v0180-dispatch-backlog}

If your values set `dispatchBacklog` under `functionalAreas.outbound-connectors.config`, delete it
**before** upgrading. The service refuses to start with it, and the error names the key.

The setting sized a buffer between the service's reader and its send workers, and that buffer is
gone. The reader now fetches only as many dispatches as there are workers free to start them
(`maxConcurrentSends`), so a dispatch no longer waits in the process while the broker's
acknowledgement window runs. The notification service reads alarms the same way, one per
dispatcher.

This fixes a duplicate. Before, a burst of alarms or connector dispatches queued behind a slow
channel could sit in the service longer than that window. The broker then handed the same messages
out again while the first copies were still waiting, and both copies were sent: a second page for
one alarm, or a second call to the same webhook. Each send is now also cut off with time to spare
before the window closes.

A new alert, `ReaderHeldMessagePastAckWait`, fires if either service still holds a message past the
window. [Messages held past their acknowledgement
window](./observability.md#held-past-ack-wait) explains what each case means.

##### Webhook notification channels must say how they authenticate {#v0180-webhook-auth}

A webhook channel's config now needs an `auth` key: `none`, `bearer` or `header`. Before, the
channel sent `Authorization: Bearer <secret>` when a secret was stored and **no credential at all**
when none was, so a channel whose secret was missing or had been cleared kept posting
unauthenticated, and nothing reported it. [Configuring notification
channels](../guides/notification-channels.md) has the details.

- **A webhook channel saved before this release has no `auth`, and it stops delivering at the
  upgrade**, including one that works today. Each delivery to it is refused on the first attempt
  and not retried, for every alarm and every escalation that routes to it, and the alarm is not
  redelivered for that channel. The notification service logs the tenant, the channel's token and
  the reason, and counts it on
  `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`.
- **Find the channels to fix before you upgrade.** In each tenant, run
  `notificationChannels(criteria: {pageNumber: 1, pageSize: 100, channelType: "webhook"}) { results { token config hasSecret enabled } pagination { totalRecords } }`
  and look for a `config` with no `auth`. If `totalRecords` is more than 100, repeat with the next
  `pageNumber` until you have read them all.
- **Add `auth` before you upgrade.** The current release accepts the key and ignores it, so there
  is no gap. Use `bearer` for a channel that has a secret (`hasSecret: true`) and `none` for one
  that has none and should not, such as a Slack incoming webhook. A channel that set `authHeader`
  needs `header`. One that set only `authScheme` (for example `Token`) needs
  `"auth":"header","authHeader":"Authorization"`. With `bearer` or `none`, remove `authHeader` and
  `authScheme`: this release refuses them there rather than ignoring them. With `none`, send
  `secret: null` in the same update if the channel has a secret.
- **A channel whose `auth` and secret disagree is refused when it is saved.** This covers
  `bearer` or `header` with no secret, `none` with a secret, and clearing the secret of a `bearer`
  or `header` channel. Rotating the secret of a channel that has no `auth` yet is refused too, until
  `auth` is added in the same request. An update that only renames, describes or disables a channel
  is not checked, so you can disable a broken channel without fixing it first; enabling one is
  checked.
- **An SMTP channel with a username and no secret** is now refused before the platform connects to
  the mail server, and it is not retried. Before, it connected, then gave up without
  authenticating, and retried every attempt.
- **An `httpCall` action whose secret handle names no stored secret** is dead-lettered once with
  the outcome `invalid` and not retried. Before, it was retried until the redelivery limit and
  dead-lettered as exhausted, so a secret stored during that window could still let the call
  through. Nothing replays a dead letter, so that firing's call is not made; correct the action's
  secret handle so later firings authenticate. The call is never sent without its credential.

##### A notification policy save made from a stale copy can be refused {#v0180-policy-precondition}

`updateNotificationPolicy` takes an optional `expectedUpdatedAt`. Send the `updatedAt` you last
read, or the one the previous update returned, and a save made from a stale copy is refused, with
nothing written, if anyone has changed the policy since, its rules included. The refusal reads
`notification policy was modified by another writer; reload and try again` and carries no
`extensions.code`. The update's response now reads `updatedAt` back from the database, so it can
be sent as the next precondition. Leave the argument out and the last write wins, as before. The
console has no notification-policy editor; only API callers can send it. See [Which mutations
are partial updates](../reference/graphql-api.md#which-mutations-are-partial-updates).

#### Persistence and state {#v0180-persistence}

##### Events are persisted in batches {#v0180-batched-persistence}

`event-management` now commits the events waiting for a writer together, in one transaction, instead
of one transaction per event. On a replicated event store each commit waits for a standby, and that
wait, not the database's work, was what limited how fast events could be stored. A batch pays it
once. Which events are stored does not change, and an event is still acknowledged only after it has
been stored. An event that is refused is written again on its own, so it is retried or reported
exactly as before, and the rest of its batch is committed without it.

Three settings are new, all optional: `persistence.writers` (default `5`, the number that was fixed
before), `persistence.maxBatch` (default `32`; `1` restores one transaction per event) and
`persistence.lingerMillis` (default `0`). `device-state` gains `projection.writers` (default `5`).
See [Event persistence](./observability.md#event-persistence).

- **A writer count must be below the service's connection pool.** A value out of range stops the
  service from starting, and the error names the setting. The default of 5 is refused only if you
  set `tsdbConfiguration.maxOpenConnections` for `event-management`, or
  `rdbConfiguration.maxOpenConnections` for `device-state`, to 5 or fewer. Before upgrading, set
  the writer count below the pool, or raise the pool no further than its default of 20: the
  platform's database connection limits are sized for that default.
- **`device-state` merges in batches too.** See [Live device state is merged in batches](#v0180-live-state-batches).
- **If you chart the persistence metrics:** `persist_inflight` can now exceed the number of
  writers, because it counts events waiting for their batch to commit, and
  `persist_duration_seconds` now includes that wait. Two metrics are new, `persist_batch_size` and
  `persist_batch_fallbacks_total`.

##### Live device state is merged in batches, and a consumer that stays behind raises a warning {#v0180-live-state-batches}

`device-state` merged every event into a device's live state (connectivity, activity, latest
readings and last position) in two transactions of its own. On a replicated database each commit
waits for the standby, so the live state fell behind whenever events arrived faster than it could
commit them one at a time, and after a sustained high rate it could lag the stored events by more
than an hour while nothing reported it. It now merges the events waiting for a writer in one
transaction, the way `event-management` persists them. Measured in-process against TimescaleDB with
a synchronous standby, five writers merged about 3500 events a second, against about 85 before.

- **What a device's live state ends up holding does not change,** with one exception. A reading or
  a position replaces the stored one only when it is strictly newer, and times are now compared as
  the database stores them, to the microsecond. Before, a reading arriving in the same microsecond
  as the stored one could replace it even when it was older. Now the one stored first stays.
- **An event is still acknowledged only after it has been stored.** If one tenant's part of a batch
  is refused, that tenant's events are merged again one at a time, so only an event that is itself
  refused is retried or dropped as before, and the other tenants' events are committed together.
- **Two settings are new, both optional:** `projection.maxBatch` (default `32`; `1` restores one
  event at a time) and `projection.lingerMillis` (default `0`), with the same ranges as
  `event-management`'s. See [Live device state](./observability.md#live-state-projection).
- **If you chart `device-state`'s metrics:** `state_inflight` can now exceed the number of writers,
  because it counts events waiting for their batch to commit, and `state_duration_seconds` now
  includes that wait. Two metrics are new, `state_batch_size` and `state_batch_fallbacks_total`.
- **Every service now reports how many messages are waiting for each consumer it reads,** as
  `jetstream_consumer_pending_messages` and `jetstream_consumer_ack_pending_messages`. A new
  warning, `JetStreamDurableFallingBehind`, fires when a consumer has had more than 10000 messages
  waiting for it for 15 minutes. `event-processing`'s detection consumer is left to
  `DetectConsumerBacklogHigh`, which is unchanged. See
  [A consumer that stays behind](./observability.md#consumer-backlog).

#### Databases and failover {#v0180-databases}

##### A database primary fails over in seconds {#database-primary-failover-in-seconds}

Deleting a database primary's pod, draining its node, or rolling out a change to it used to hold
the failover for three minutes: the primary waited for every client to disconnect, and the
platform's services never do. It now gives clients five seconds and then shuts down, so under
`--ha` a standby takes over in well under a minute. See
[When a database primary stops](./bootstrap.md#ha-database-failover).

- **A multi-instance database now rolls its primary by switchover.** Earlier releases restarted
  the primary in place and waited for it, with no standby promoted, which made every such
  rollout a full write outage.
- **A stopping database instance is stopped forcibly after two minutes**, instead of thirty. A
  database pod that stayed `Terminating` for up to half an hour after its database had stopped,
  leaving the cluster a standby short, is now removed after at most two minutes.
- **If the backup store is unreachable when an instance stops**, the instance no longer waits up
  to thirty minutes for its last write-ahead log to be archived. It waits at most two. Committed
  data is not affected, but the archive can have a gap, as described on the page linked above.

**The relational store's instances restart once when `dcctl install` moves the cluster.** Under
`--ha` the standbys restart first and the primary role is then switched over to one of them,
which is a brief write outage. A single-instance install restarts its only instance in place,
and the relational store is unavailable until the restart finishes. Writes made during it are
retried.

**The event store of an existing instance keeps the old settings.** `dcctl upgrade` does not
re-apply an instance's databases, so only instances bootstrapped from this release get the new
settings on their event store. To give an existing instance's event store the same settings,
patch its database cluster with ONE of these commands. This restarts its instances once, as
above:

```bash
# under --ha: the switchover setting goes in the SAME patch as the timings
kubectl -n dci-<instance> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"primaryUpdateMethod":"switchover","smartShutdownTimeout":5,"stopDelay":120,"switchoverDelay":120}}'
# a single-instance install
kubectl -n dci-<instance> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"smartShutdownTimeout":5,"stopDelay":120,"switchoverDelay":120}}'
```

Under `--ha`, do not split the first command into two patches with the timings first. Changing
`stopDelay` starts the restart immediately, and if the switchover setting is not yet in place the
primary is restarted in place with no standby promoted, which is the long outage this release
removes.

**Under `--ha`, a database image change and a database parameter change must be applied
separately.** With the switchover setting in place, the database operator refuses an update that
changes the database image and any database parameter at once. A single `dcctl install` run that
both moves to a release with a new database image and changes `--max-connections` is therefore
refused. Run `dcctl install` without the `--max-connections` change first, then again with it.

:::caution The restart that applies these settings still has the old thirty-minute limit
The two-minute limit belongs to each database pod, so it arrives with the restart that replaces
the pod, and the pods being replaced still carry thirty minutes. If a database pod stays
`Terminating` for more than two minutes during that restart, and its log shows
`failed waiting for all runnables to end within grace period of 30s`, its database has already
stopped and the pod is not going to finish on its own. Remove it, and the operator recreates it:

```bash
# the relational store's pods are dc-rdb-<n> in dc-system,
# the event store's are dc-tsdb-<n> in dci-<instance>
kubectl -n dc-system delete pod dc-rdb-1 --grace-period=0 --force
```

Only do this when the pod's node is `Ready` and you have read that line in its log. A pod that is
`Terminating` because its node is unreachable is a different case: its database may still be
running, and it must not be force-deleted. See [losing a node](./bootstrap.md#ha-node-loss).
:::

##### A database transaction left idle for a minute is ended {#v0180-idle-transactions}

Nothing needs doing at the upgrade.

Every service now asks the database to end any of its transactions that sits idle for 60 seconds.
This covers a pod that freezes, or loses the network, in the middle of a transaction. Before, such a
transaction stayed open, holding its locks, until the connection was noticed to be dead, which with
default operating-system settings can take hours, and it could still commit when the network came
back. That could write data for a tenant whose deletion had already been reported complete. When
the limit is hit, the database logs `terminating connection due to idle-in-transaction timeout` and
rolls the transaction back. The request it belonged to fails with an error, and work a service takes
from a stream is delivered to it again. See [Database write refusal](./tenant-deletion.md#database-writes).

##### What losing a node, and getting it back, looks like is documented {#v0180-node-loss}

Nothing changes in behaviour. Under `--ha`, [losing a node](./bootstrap.md#ha-node-loss) now
describes, in order, what an operator sees: how quickly the broker, the services and the
databases recover, that event processing can pause for about a minute, why evicted pods on the
lost node stay `Terminating` and must not be force-deleted
while it is unreachable, and that a node's **return** is itself a short disruption. A broker
server that was cut off comes back having held elections on its own, and the other servers
elect their leaders again when it rejoins: expect a few seconds of "temporarily unavailable"
from JetStream about 45 seconds after the node returns. Nothing is lost. Plan a node's return
the way you plan its loss.

The 30-second node-loss eviction on the services' pods is unchanged and deliberate, and is now
checked on every pod the chart renders, the console included.

#### Messaging {#v0180-messaging}

##### A service whose broker connection closes for good now restarts {#v0180-broker-connection-closed}

Every service's pod now fails its liveness check (`/healthz`), and with it its readiness check,
when its connection to the message broker is closed for good without the service asking for it:
for example when the broker stops accepting a credential that was revoked or rotated under the
running pod, or sends an error the client cannot parse. Kubernetes restarts the pod, and the new
pod reads its credential again. Before this release the pod stayed live and ready, logged one
error line and did nothing more. A close the service asked for, as at an orderly stop, never
triggers it.

- **A credential change can show as restarts.** If the broker drops a pod's connection before the
  pod itself is replaced, for example during a credential rotation, that pod restarts once.
- **An orderly stop now waits for the broker connection to drain**, within the pod's termination
  budget, so messages already received are handled before the pod exits. A stop can take longer
  than before, up to that budget.

`event-sources` also has a connection of its own for broker presence, and the same now holds for
it. It reads MQTT connection events from the broker over that connection, signed in with the
system-account credential. If the broker closed it for good, the pod carried on as live and ready
with broker presence silently frozen: nothing was asserted or released, and nothing restarted it.

Now the pod fails its liveness check and Kubernetes restarts it. A refused credential reaches every
replica at once, so **every `event-sources` pod restarts**, and HTTP ingest is unavailable while
they do; MQTT telemetry is stored by the broker and processed when they return. If the restarted
pods still cannot sign in, broker presence turns off with reason `broker_unreachable` and asserted
devices return to inferred presence, as when the broker cannot be reached at startup ([Device
presence](../concepts/device-presence.md)).


##### Read loops that keep failing now restart their service {#v0180-read-loops}

A loop that reads messages from a stream used to retry a failing read once a second for as long as
it kept failing, while its pod reported ready and consumed nothing. Every such loop now retries
with a growing pause, up to 5 seconds, and once reads have failed without a break for two minutes
it ends the process with a non-zero exit, so Kubernetes restarts the pod. One successful read
starts the two minutes again, which is long enough to ride out a broker failover or restart. The
restart connects to the broker again, re-creates the consumer and reads the mounted credential
again.

The loops that gain this in this release are:

- `outbound-connectors`' connector dispatch;
- `user-management`'s dead-letter store;
- `command-delivery`'s dead-letter write-back;
- seven in `event-processing`: resolved events, rule and attribute updates, the device roster and
  device deletions, geofence sets, and the action dispatcher;
- `lwm2m-ingest`'s command dispatcher.

What you will see is a restart count where there used to be a ready pod that consumed nothing.

##### A consumer that falls behind a full stream now raises an alert {#v0180-unread-loss}

A full JetStream stream discards its oldest messages. Before this release, a consumer that had not
read those messages yet lost them without any metric or alert saying so. Every service now measures
this for each durable consumer it reads and exports two new series:

- `devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`: messages the
  consumer moved past without reading them.
- `devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`: messages discarded
  ahead of a consumer that has stopped reading (one that was handed no messages since the previous
  sample; a consumer that is reading but behind reads 0 here).

Two new critical alerts read them: `JetStreamDurableLostUnread` and
`JetStreamDurableStalledBehindStream`. Deleting a tenant can fire the first one: the deletion removes
that tenant's messages, including any a consumer had not reached yet. See
[Messages a consumer never read](./observability.md#unread-loss) for what each alert means and what
to do.

The near-full warning is **renamed** from `EventProcessingStreamNearFull` to
**`JetStreamStreamNearFull`**, and it now covers the streams of every service, not only
event-processing's. The threshold (80% of the byte ceiling for 10 minutes) is unchanged. If an
Alertmanager route or silence names the old alert, change it to the new name.

##### Messages abandoned on their last attempt are now dead-lettered {#v0180-no-outcome-dead-letters}

A message a service gave up on after handling it used to be the only kind that reached the
dead-letter list. A message whose five delivery attempts all ran out with **no** outcome — a pod
stopped mid-handling, or a handler that ran past its acknowledgement window — left no record
anywhere, because nothing reached the code that writes the letter. The broker does notice, and
now every service records those too, from the broker's own notice.

What changes for you:

- **Three new kinds**, `event`, `command` and `control-fact`, for messages on the device-event,
  command and control-plane streams. The kind of a letter is now fixed by the stream the message
  arrived on. `dcctl dead-letters list --kind` offers all of them.
- **A new reason, `no-outcome`.** It never settles a command: the last attempt may have done its
  work and lost only its acknowledgement. For high-volume streams (device events, commands and
  detection actions) the letter carries no copy of the message; its detail names where the
  original is until the stream ages it out. The same holds for a message too large to copy, and a
  connector request's letter points at the connectors service's own dead-letter stream, which
  holds the full request.
- **A new stream, `max-deliveries`,** created by every service that reads a stream. It reserves
  8 MiB at default sizing and fits the existing JetStream volume; nothing needs resizing. It is
  empty in steady state, and a new alert, `MaxDeliveryRecordsWaiting`, fires if notices wait on it
  unrecorded ([Messages that ran out of delivery
  attempts](./observability.md#max-delivery-records)).
- **The detection engine's give-ups are counted, not dead-lettered.** `event-processing` reads
  `resolved-events` from its own saved checkpoint and reads the stream again after a restart, so
  an event whose attempts ran out there has not been lost. When the engine cannot save its
  checkpoint for longer than the broker keeps redelivering (usually a database outage), every
  event in that window runs out of attempts, and a letter for each would report losses that did
  not happen. They are counted with `outcome="replay-covered"` instead, and a new warning alert,
  `ReplayCoveredDeliveriesExhausted`, reports them. The other services that read
  `resolved-events` still dead-letter theirs.
- **The dead-letter stream gains a 30-minute duplicate window,** applied in place on upgrade, and
  so does the connectors service's own dead-letter stream. It is what makes a give-up recorded both
  by a service and by the broker's notice land once.
- **`DeadLetterWriteLost` has a third cause:** a dead letter that the dead-letter store or the
  command writeback ran out of attempts on, which may now age out of the stream without having
  been stored (its last attempt may have stored it and lost only the acknowledgement).

During the rolling upgrade a give-up can be lettered twice, once by a pod of the old release and
once from the broker's notice. The two are the same failure; nothing was lost.

##### The dead-letter loss counters are one metric per service {#v0180-dead-letter-lost}

A service that gives up on a message and then cannot record it as a dead letter now counts that
loss on **`dead_letter_lost_total`** under its own subsystem, the same name in every service. These
five series are gone:

- `devicechain_eventprocessing_react_events_dead_letter_lost_total`
- `devicechain_notificationmanagement_notifications_dead_letter_lost_total`
- `devicechain_commanddelivery_command_delivery_responses_dead_letter_lost_total`
- `devicechain_devicemanagement_raise_alarm_dead_letter_lost_total`
- `devicechain_devicemanagement_alarm_event_dead_letter_lost_total`

These five replace them:

- `devicechain_eventprocessing_dead_letter_lost_total`
- `devicechain_notificationmanagement_dead_letter_lost_total`
- `devicechain_commanddelivery_dead_letter_lost_total`
- `devicechain_devicemanagement_dead_letter_lost_total`, one counter for both of device-management's
  paths
- `devicechain_outboundconnectors_dead_letter_lost_total`, which is **new**. An outbound connector
  dispatch whose dead-letter copy could not be written on its final delivery used to be counted
  only as `connector_dispatch_total{outcome="dead_write_failed"}`, which no alert read. It is still
  counted there, and is now counted here as well.

The `DeadLetterWriteLost` alert selects these by name rather than listing them, so it now covers
outbound connectors too. If your own dashboards or rules name the old series, change them. The
selector `{__name__=~"devicechain_[a-z0-9]+_dead_letter_lost_total"}` covers every service. Use
`[a-z0-9]+` and not `.+`: while the upgrade rolls, pods that have not been replaced yet still
export device-management's two old names, and `.+` matches both of them.

The counter also counts a letter the service **refused** as malformed, which is a defect in that
service rather than a broker problem. The pod's `LOST` error log line says which of the two
happened.

#### Security and sign-in {#v0180-security}

##### Sessions end on a password reset, and everyone signs in again once {#v0180-sessions}

Two changes end every session at the upgrade.

**Each user now has a session value**, and every token that can be exchanged for a new one carries
it: refresh tokens, the sign-in token the console holds before a tenant is chosen, and OAuth
authorization codes. Resetting a user's password, disabling the user, or deleting the user changes
that value, and a token carrying the old one is refused. Before this release a password reset left
every refresh token already issued working, so a stolen one kept renewing itself for as long as it
was used.

**The key that signs every access and refresh token is now sealed under the instance root key.**
It used to be stored unencrypted in user-management's database, so anyone who could read that
database, a backup of it or its write-ahead-log archive could sign tokens every service accepts.
Now it is sealed like every other stored credential, and only the key currently in use has a
private half at all. When a key is rotated out, its private half is deleted and only its public
half is kept, so tokens it signed keep verifying until they expire. The upgrade **deletes every
signing key the instance had**, rather than sealing them, because each one has already sat
unencrypted in every backup taken so far. user-management generates a new key when it starts.

What you will see at the upgrade:

- **Every user signs in again.** Tokens issued before the upgrade carry no session value, so they
  cannot be refreshed, and once the rollout completes they no longer validate either. Console,
  dashboard and SDK sessions end at their next refresh, within 15 minutes of the upgrade unless you
  have changed the access-token lifetime. Sessions in an embedded dashboard app end too. A console
  that is sitting on the tenant picker or the admin pages when that happens can report an error
  when a tenant is chosen, rather than returning to the sign-in page. Signing out and back in
  clears it.
- **OAuth clients, including AI assistants connected through MCP, must authorize again.** Their
  refresh tokens are refused with `invalid_grant`.
- **The old key is trusted until the rollout completes.** Until the last old user-management pod
  has stopped, it keeps signing tokens with the old key and publishing that key for other services
  to verify against. `helm upgrade` and `dcctl upgrade` replace every pod, so the old key stops
  being trusted when they finish. While that old pod is stopping, a request made in the first
  seconds of the rollout can be refused with `401 invalid or expired token`, and a sign-in with
  `invalid or expired token`, even for a token issued moments earlier; once the old
  user-management pod has stopped, signing in again and retrying succeeds.
- A user created by a service that had not yet been replaced while the upgrade rolled out has no
  session value and cannot sign in. The sign-in fails as a wrong password would, and the
  user-management log names the user and the cause. An administrator resetting that user's password
  fixes it.

What changes from then on:

- **A password reset, disabling a user, or deleting a user ends every session that user holds.**
  Their refresh tokens stop working at the next use, and a sign-in token or authorization code
  issued before the change can no longer be exchanged for a new session. Re-enabling a disabled
  user does not bring the old sessions back.
- **Tokens already issued for direct use are not revoked.** An access token, and the sign-in token
  on the admin API, keep working until they expire: 15 minutes, unless you have changed the
  access-token lifetime. That includes an administrator's sign-in token.
- **Deleting a user and creating one with the same email starts clean.** The new user does not
  pick up any session the old one still held.
- Changing a user's roles or memberships does not sign them out. It takes effect at their next
  refresh, as before.

Backups and archived write-ahead log taken **before** the upgrade still contain the old keys.
Those keys are no longer trusted anywhere once the rollout completes, but the files are still
worth protecting as the credentials they were.

##### A new signing key is trusted within about a second {#v0180-signing-key-trust}

When user-management starts signing tokens with a new key, as it does at this upgrade, every
other service learns that key the first time it sees a token signed with it, by fetching the key
set user-management publishes. Earlier releases allowed each service pod that fetch at most once
every 30 seconds. If the fetch was answered by a user-management pod still publishing the previous
key set, as the old pod does while a rolling upgrade is under way, that service refused every
token signed with the new key as `invalid or expired token` until the 30 seconds had passed. A
sign-in straight after an upgrade could fail this way.

- **A service now fetches again as soon as one second after its previous fetch finished.**
  Requests that arrive while a fetch is under way wait for it instead of being refused.
- **Tokens naming a key user-management never published** still cost it at most one fetch per
  second from each service pod.
- **A fetch that does not find the key is now logged.** The service logs `JWKS refetched on an
  unknown kid, and the fetched set does not hold it.` with the key id, which tells a token refused
  during a key change apart from a token that is simply not valid.

Nothing needs configuring.

##### The instance root key is now required in every profile {#v0180-root-key}

The root key used to be needed only by profiles that store integration credentials. The chart
therefore let the `telemetry` and `ingest-only` profiles render without one. Every instance now
seals its signing key under the root key, so the chart **fails the render** for any profile when
no key is set. An instance built with `dcctl bootstrap` already has one and needs nothing. A
chart-only `telemetry` or `ingest-only` install needs a key before it can be upgraded. Generate
one with `openssl rand -base64 32`, pass it as `instance.config.infrastructure.secrets.rootKey`,
and keep it: the same value has to be passed on every later upgrade.

The root key now also decides whether anyone can sign in. With a wrong or lost key,
user-management refuses to start, and so does every service that stores integration
credentials. Every other service then stays not-ready, because it cannot get the keys it needs
to validate a token. The whole API is down, not just the integrations. See
[Disaster recovery](./disaster-recovery.md#root-key) for what to do about it.

Deleting a stored credential now also removes it from the database completely. Before, the
sealed row stayed in the table, marked deleted. Credentials that were deleted **before** this
release stay that way: they cannot be used, but their sealed rows are still in the table and in
backups.

##### The superuser no longer has a default password {#v0180-superuser}

Earlier releases created every instance's superuser, `superuser@devicechain.local`, with the same
published password, `devicechain`. A new instance now gets a password generated for it: `dcctl
bootstrap` prints it once and keeps it in the Secret `dci-<instance>-superuser` in the instance's
namespace. There is no default any more. If user-management starts with an empty identity table and
no password in that Secret, it refuses to create the superuser.

**The upgrade does not change an existing superuser's password.** The upgrade cannot know whether
you changed it, so it leaves it alone, and it prints a warning at the end for any instance that has
no generated password. If you never changed the password on such an instance, it is still
`devicechain`. Sign in and change it, or recreate the instance to have one generated.

`dcctl sim` and the drill tools no longer assume the old password either. `dcctl sim` reads the
generated one from the instance's Secret, and takes `--admin-password` or `$DC_ADMIN_PASSWORD` for an
instance that has none.

If your values set `auth.superuserPassword` for user-management
(`functionalAreas.user-management.config.auth.superuserPassword`), delete it before upgrading.
user-management now refuses to start with it, and the error names the key. The seed password comes
only from the Secret `dci-<instance>-superuser`, or the one named by `instance.superuserSecret`.

##### Sign-in is rate-limited, and GraphQL requests carry fewer fields {#v0180-sign-in-limits}

Nothing needs doing at the upgrade unless your own code or scripts do one of the things below. The
console, the dashboard app, the SDKs and `dcctl` already stay within every limit.

- **An operation may select at most 5 top-level fields in a mutation and 20 in a query.** Aliases
  count, and so do fields reached through fragments. A request over the limit runs nothing and gets
  one error with the code `TOO_MANY_ROOT_FIELDS`. Split such a request, or raise
  `DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS` / `DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS` for that service.
- **One request can have one password checked.** A further `login` in the same request is not
  evaluated and gets the code `TOO_MANY_CREDENTIAL_CHECKS`. Sign in once per request.
- **Repeated failed sign-ins on one email address are slowed down.** After five failures in a row,
  the next attempt on that address waits 1 second, doubling up to 5 minutes. An attempt made during
  the wait gets the code `THROTTLED` with `retryAfterSeconds`, not "invalid credentials". A sign-in
  that the server cannot count gets `UNAVAILABLE`. If your code signs in, handle both as their own
  errors rather than as a wrong password. OAuth client secrets are not slowed down.
- **The JetStream reservation grows by 128 MiB** (16 MiB on the compact preset), for the bucket that
  holds the sign-in counts. On the compact preset the cache buckets shrink from 8 to 4 MiB each to
  make room. If you sized the JetStream volume yourself close to the reservation, check that it has
  the room.
- **A new alert, `CredentialAttemptStoreFull`,** fires if that bucket fills up. Sign-in keeps
  working when it is full, but repeated failures are no longer slowed down. [Sign-in
  backoff](../reference/graphql-api.md#sign-in-backoff) explains what to do.

[Request limits](../reference/graphql-api.md#request-limits) and [sign-in
backoff](../reference/graphql-api.md#sign-in-backoff) have the details.

##### A session refresh survives a brief outage {#v0180-session-refresh}

Refreshing a session used to use up the refresh token before re-checking the
session. A database or broker error during that check then ended the session: the refresh failed
as "invalid or expired token", and the token could not be used again. The check now runs first. A
store error leaves the token valid and returns an error you can retry ("the session could not be
refreshed right now; try again"), and the OAuth token endpoint returns `server_error` without the
underlying error text. A refresh that is refused because the session ended, the membership was
removed or disabled, or the tenant refuses access still uses the token up.

Only a client that retries with the same refresh token benefits. An OAuth client, such as an AI
agent connecting over MCP, now gets `server_error` rather than `invalid_grant` during such an outage,
so it can retry instead of asking the user to authorize it again. The Go client library the
simulator, the load tests and `dcctl` use no longer loses its refresh token to the outage, but it
still falls back to a password sign-in whenever a refresh fails, as it did before. The console still
signs the user out on any refresh failure. Nothing needs doing at the upgrade.

##### Repeated failed MQTT password connects are slowed down {#v0180-mqtt-connect-backoff}

Nothing needs doing unless a device connects with a wrong MQTT password in a loop, or you sized
the JetStream volume yourself. [Repeated failed connects are slowed
down](../guides/device-credentials.md#connect-backoff) has the details.

- **After 10 failed connects in a row for one MQTT username, the next attempt waits 1 second,**
  doubling up to 30 seconds. A connect made during the wait is refused like a wrong password, even
  if the password is right. A successful connect resets the count. Access-token connects and
  credentials in event bodies are not affected.
- **Someone who knows a device's MQTT username can keep that device from reconnecting** for as long
  as they keep sending wrong passwords for it. Devices that are already connected are not affected
  until they reconnect.
- **Password connects now need JetStream.** If the store that holds the counts cannot be reached,
  password connects are refused, including briefly while that store's JetStream leader changes, for
  example while a NATS node restarts.
- During a database outage, a device whose password connects keep failing is slowed down the same
  way, so after its first 10 attempts its refused connects are logged at debug rather than as a
  warning each. An unreachable count store is logged as one warning a minute.
- **The JetStream reservation grows by 128 MiB** (16 MiB on the compact preset) for the new bucket
  that holds the counts.
- **The compact preset's JetStream volume grows from 2Gi to 3Gi for instances built by this
  release:** the store grows from 1 GiB to 2 GiB. An existing compact instance keeps its 2Gi
  volume, because `dcctl upgrade` does not re-apply infrastructure, and the new bucket still fits
  in it.
- **A new alert, `DeviceCredentialAttemptStoreFull`** (warning), fires when that bucket fills.
  Connects keep working, but without the slow-down. A very large reconnect wave can fill it as well
  as an attack can.

##### Credential values are stored exactly as sent {#v0180-credential-values}

- **A device credential's `credentialValue` is no longer trimmed.** Earlier releases removed
  leading and trailing whitespace when the value was saved, but compared the password a device
  presented without trimming it, so an MQTT password that began or ended with a space could never
  authenticate. The value is now stored exactly as sent. Only an empty value, or an explicit
  `null` on update, stores no password.
- **The other direction changes too.** A value pasted with a trailing newline or space used to be
  saved without it, so a device presenting the password without it was accepted. It is now saved
  with it, and such a device is refused until the value is sent again without the newline.
- **Values saved before this release are not changed by the upgrade.** They were stored trimmed,
  and the whitespace cannot be recovered. If a device's configured password has surrounding
  spaces, send the value again with `updateDeviceCredential`.
- **A tier with no color now reads `color: null`** on the admin API, where it used to read `""`.
  Sending `""` or `null` still clears it, and the upgrade converts every stored empty color to
  null. Surrounding spaces are now trimmed before the color is checked, so `" amber "` is accepted
  as `amber` where it used to be refused. The console needs nothing; code of your own that
  compares `color` with `""` should test for null.
- **`firstName` and `lastName` are trimmed like other display text**, on `createIdentity` and on
  `updateProfile`, and a cleared name is stored as null. Reads already returned `null` for an
  empty name, and still do: the upgrade converts every stored empty name to null. It does the same
  for an AI provider's empty `endpoint`, which likewise already read as `null`. A name an earlier
  release saved with surrounding spaces keeps them until an update names that field, which then
  trims it.
- **A stored number too large for a GraphQL `Int` is now an error rather than a wrong number.**
  A notification policy's `throttleSeconds`, `escalateAfterSeconds` and `maxEscalations`, and a
  tenant's burst, shed-priority, held-command and geofence overrides on the admin API, used to wrap
  such a value into a negative one. Values written through the API always fit, so this affects
  only a value written to the database some other way. A notification-policy update that does not
  name one of those three fields also no longer rewrites it.

##### A provisioning profile can no longer be created with a blank secret {#v0180-provisioning-secret}

`createProvisioningProfile` now refuses an empty or whitespace-only `provisionKey` or
`provisionSecret`. A profile with an empty secret would match a device that also sent an empty
secret. No instance was exposed to that, because the path a device would use to present a
provisioning secret has not shipped yet; the check closes the gap before it does.
`updateProvisioningProfile` already refused a blank value. A profile
already stored with an empty secret now matches no presented secret at all, empty or not: give it a
real one with `updateProvisioningProfile`.

#### API and errors {#v0180-api}

##### GraphQL WebSockets carry subscriptions only, and close when their token expires {#v0180-websocket}

The WebSocket a service accepts on its GraphQL endpoint changes in three ways:

- **It runs subscriptions and nothing else.** A query or mutation sent over it is refused with an
  error telling you to use HTTP, and nothing runs. Before, both were executed, using the token the
  connection had presented when it opened. Send queries and mutations as HTTP requests.
- **It closes with code `4401` when the access token it authenticated with expires.** Before, a
  connection stayed open, and its subscriptions kept streaming, for as long as the client answered
  pings. To keep a feed running, open a new connection with a fresh token and subscribe again.
  - `@devicechain/client` does this for you. When a connection it had established is closed with
    `4401`, it reconnects once with a newly resolved token, subscribes again, and reports the
    reconnect to your sink as `connected(true)`.
  - The .NET SDK raises the close from `SubscribeAsync` as an exception that names the code.
    Subscribe again to continue; the new connection takes a fresh token from the session.
  - The standalone dashboard viewer does not refresh its token, so its live widgets stop when the
    token expires. Sign in again.
- **A service with no subscriptions no longer accepts a WebSocket at all.** The upgrade request is
  refused with HTTP 400. Before, the connection opened and every operation sent on it failed.

##### GraphQL documents must use GraphQL's own comments and strings {#v0180-graphql-syntax}

Nothing needs doing at the upgrade unless your own code or scripts write GraphQL documents by hand.
The console, the dashboard app, the SDKs, `dcctl` and the MCP server never send any of the
following.

- **A document written with `//` or `/* */` comments, backquoted strings, or single-quoted
  characters is now refused** with a syntax error, and nothing in it runs. Earlier releases accepted
  these, although they are not GraphQL. Use `#` for comments and `"` for strings.
- **A block string whose closing `"""` directly follows a backslash is refused as well**, as in
  `\"""`. That escape is valid GraphQL, but earlier releases never read it as the specification
  defines it: they closed the string at those three quotes and read the rest of the document from
  there. Send such text in a variable.
- **A string directly followed by a quote, such as `"x""y"`, is refused.** GraphQL reads that as two
  adjacent strings, which is never a valid value; earlier releases read it as the start of a block
  string instead. Only `"""` opens a block string, and an empty string `""` is unaffected.
- **Over a GraphQL WebSocket, a subscription the server cannot read now gets the syntax error**, not
  the message saying that only subscriptions are accepted. A document that names an operation it
  does not hold gets its own error too. Both are errors for that operation only; the connection
  stays open.

[Request limits](../reference/graphql-api.md#request-limits) has the details.

##### The alarm `message` field is removed {#v0180-alarm-message}

Alarms had a `message` field that nothing ever filled in: it was always null. It is removed
everywhere it appeared:

- **GraphQL:** `Alarm.message` and `AlarmEvent.message` (the `alarmStream` subscription) are gone.
  A query or subscription that still selects `message` is now refused with
  `Cannot query field "message"`. Remove it from your own documents before upgrading.
- **`@devicechain/dashboards` and `@devicechain/widgets`:** `AlarmRow` no longer has `message`, so
  code that reads `AlarmRow.message` no longer compiles. The alarm table widget no longer shows a
  tooltip on the alarm key, and the dashboard editor's preview no longer shows made-up alarm
  messages. Versions of these packages from before this release still select `message`, so the
  upgraded server refuses their alarm list and alarm widgets stop loading: upgrade the packages
  together with the platform.
- **Notifications:** alarm emails no longer have a `Message` line and webhook payloads no longer have
  a `message` key. Neither ever appeared, because the value was always empty.
- **MCP:** `list_alarms` and `get_alarm` no longer return `message`.
- **Database:** the empty `message` column is dropped from the alarms table when device-management
  starts.

While the upgrade rolls out:

- A console tab opened before the upgrade, and a console, dashboard or MCP pod still on the previous
  release, get an error on alarm lists until the tab is reloaded or the pod is replaced.
- A `device-management` pod still on the previous release cannot store a new alarm. The alarm is
  retried about once a minute and is normally stored by an upgraded pod. If pods of the previous
  release keep running for more than about five minutes, for example because a new pod never
  becomes ready, the alarm is given up and recorded as a dead letter, and it is raised only when
  its condition clears and occurs again. Keep the rollout short, and afterwards check
  `dcctl dead-letters list --kind detection-action --source device-management` for alarms that
  were not raised.
- Alarm lists, acknowledge and clear served by a `device-management` pod still on the previous
  release can fail once per database connection. Repeating the request succeeds.

##### A duplicate now answers with the code `CONFLICT` {#v0180-conflict}

Nothing needs doing unless your own code or scripts recognise a duplicate by reading the error
message. [A value that must be unique](../reference/graphql-api.md#unique-values) has the details.

- **A create, update or rename that repeats a value that must be unique** now carries
  `extensions.code` set to `CONFLICT`. Branch on the code. It means the write collided with a
  unique value, which is not always one you sent: two publishes of the same record racing for the
  next version number collide too, and a retry then succeeds.
- **The database's own wording is replaced.** Where a message used to end in
  `duplicate key value violates unique constraint "…" (SQLSTATE 23505)` or `UNIQUE constraint
  failed: …`, it now ends in `the request conflicts with an existing record: a value that must be
  unique is already in use`, which names no database index or column.
- **Refusals that already had their own wording keep it and gain the code:** renaming onto a token
  already in use, adding a second membership in the same tenant, and declaring a command key the
  profile already has.
- **`dcctl sim create` recognises an existing tenant, identity or membership by the code,** so
  re-running it with the same name completes. Before, a re-run stopped at the membership step. Use a
  `dcctl` from this release with an instance of this release: an older instance does not send the
  code, and this `dcctl` then reports the duplicate as an error.
- **These are not duplicates and do not carry `CONFLICT`:** creating a tenant at a deleted tenant's
  reserved token, and a save refused because the record changed since it was read.

##### Refused references and refused values answer with a code {#v0180-reference-codes}

Nothing needs doing unless your own code or scripts recognise these refusals by reading the error
message. [A reference or a value that is refused](../reference/graphql-api.md#reference-and-invalid-values)
has the details.

- **A delete refused because other records still refer to the record** now carries
  `extensions.code` set to `REFERENCE_VIOLATION`: a device profile, device type or other type still
  in use, an entity group a detection rule still scopes to, a tenant tier that tenants are still
  at, a tenant that still has memberships, an AI provider that is still granted, and a
  notification channel a policy rule still names. The message is unchanged.
- **The database's own wording is replaced.** Where a message used to end in `violates foreign key
  constraint "…" (SQLSTATE 23503)`, it now ends in `the request refers to a record that does not
  exist, or removes one that other records still refer to` and carries `REFERENCE_VIOLATION`. A
  value the database refuses for any other integrity reason, such as a missing required value, is
  answered the same way with `the request contains a value this record does not allow` and
  `INVALID_VALUE`. Neither sentence names a table, column or constraint, or repeats a value you
  sent.
- **Neither is `CONFLICT`,** so `dcctl` and any code that treats `CONFLICT` as "already exists" do
  not carry on over them. A refusal that involves both a duplicate and one of these now carries the
  new code rather than `CONFLICT`.
- **The server logs each of these database refusals as a warning** naming the constraint, table
  and column, since the service's own check normally answers first. The detail, which can repeat
  the values sent, is not logged.

##### Alert levels above 2147483647 are refused, and four more numbers are no longer wrapped {#v0180-int-range}

An alert's `level` was accepted up to 4294967295 but is served as a GraphQL `Int`, which stops at
2147483647, so a larger level read back as a negative number. Four fields had the same fault: the
stored number was cut down to 32 bits on the way out, so a value past 2147483647 became a
plausible negative one. Each is now an error instead, like the fields listed under [Credential values are stored exactly as
sent](#v0180-credential-values).

- **An alert with a `level` above 2147483647 is now refused when it arrives.** The device's
  message is refused as bad data, as an alert with no `type` is: HTTP answers `400` and an MQTT
  publish is dead-lettered. Nothing needs changing unless a device sends such levels.
- **An alert already stored with such a level makes the alert listing that includes it an
  error.** `level` cannot be null, so the whole `alertEvents` answer is null with the error,
  rather than one alert going missing. This applies to rows stored before the upgrade and to any
  stored while `event-sources` pods on the previous release are still running. The row stays
  until retention removes it. To see whether you have any, run this against the event-management
  database:
  `SELECT tenant_id, device_token, count(*) FROM "event-management".alert_events WHERE level > 2147483647 GROUP BY 1, 2;`
- **A latest measurement's `classifier`** is the id of the metric definition the reading was
  bound to. Once those ids pass 2147483647, `classifier` is an error on that field alone: it reads
  null and the reading's `value`, `unit` and time still come back. The console and the MCP tools
  do not read this field. (The same id is a string on stored measurement events, where it has no
  limit.)
- **A device state's `inactivityTimeout`** and **a tenant's branding `logoMaxHeight`** are only
  ever written with values that fit, so this affects only a value written to the database some
  other way. A wide `inactivityTimeout` makes the whole device-state listing that selects it an
  error. A wide `logoMaxHeight` makes the console's request for the tenant fail, so the console
  can no longer load that tenant's branding, locale default or map settings, and the branding
  editor may not open. To clear it, set the logo height again with `setTenantBranding` through
  the API, sending the title and colors as well: that mutation replaces them together, so one
  left out is cleared.

##### `tenantDeletions` on the admin API is paged {#v0180-tenant-deletions}

`tenantDeletions` on `/api/user-management/admin/graphql` now takes a criteria argument and
returns one page, like the admin API's `auditEvents` and `deadLetters`. A call in the old shape is
refused.

```graphql
# before
tenantDeletions(completed: false, limit: 50, offset: 0) { token epoch completedAt }

# now
tenantDeletions(criteria: {pageNumber: 1, pageSize: 50, completed: false}) {
  results { token epoch completedAt }
  pagination { totalRecords }
}
```

`pageNumber` and `pageSize` are required. A page size below 1 reads 100 records and one above 1000
reads 1000, as for the platform's other lists. Before, leaving `limit` out read the whole deletion
history. `completed` is optional, as before. See [Tenant deletion](./tenant-deletion.md#stalled-alert).

##### Audit entries name the row they changed {#v0180-audit-rows}

Twenty audited mutations wrote audit-journal entries with an empty row key and label: version
changes to profiles, groups and asset types, device claims, dashboards, connectors, inference
providers, and command state changes. They now record the affected row's primary key, and eleven
of them its label as well; the command state changes record the key only. Entries written before
the upgrade stay as they were. Nothing needs doing.

#### Observability {#v0180-observability}

##### Alerts added and renamed {#v0180-alerts}

If you route or silence alerts by name, these are the chart's new alerts, and the one that was
renamed. Each links to the item that explains it.

| Alert | Severity | What it reports | Details |
| --- | --- | --- | --- |
| `JetStreamStreamNearFull` | warning | Renamed from `EventProcessingStreamNearFull`; now covers every service's streams | [details](#v0180-unread-loss) |
| `JetStreamDurableLostUnread` | critical | A consumer lost messages it had not read | [details](#v0180-unread-loss) |
| `JetStreamDurableStalledBehindStream` | critical | A stopped consumer is losing messages ahead of it | [details](#v0180-unread-loss) |
| `JetStreamDurableFallingBehind` | warning | A consumer has had more than 10000 messages waiting for 15 minutes | [details](#v0180-live-state-batches) |
| `JetStreamReplicationUnobserved` | warning | A pod cannot read a stream's replication state | [details](#v0180-new-warnings) |
| `ReaderHeldMessagePastAckWait` | warning | A message was held past its acknowledgement window and redelivered | [details](#v0180-dispatch-backlog) |
| `MaxDeliveryRecordsWaiting` | warning | Notices of messages that ran out of attempts are not being recorded | [details](#v0180-no-outcome-dead-letters) |
| `ReplayCoveredDeliveriesExhausted` | warning | The detection engine's checkpoint has not been saved for too long | [details](#v0180-no-outcome-dead-letters) |
| `RateLimiterOverflowInUse` | warning | HTTP ingest is sharing one allowance across unconfirmed tenant names | [details](#v0180-unconfirmed-tenants) |
| `TenantsMeteredAtPlatformDefault` | warning | A service cannot read tenants' ceilings and meters them at the default | [details](#v0180-unconfirmed-tenants) |
| `ReactShedLettersOverBudget` | warning | Shed actions are being summarised rather than recorded one by one | [details](#v0180-catch-up-metering) |
| `RateMeteringClockFallback` | warning | Outbound actions are metered without their trigger time | [details](#v0180-catch-up-metering) |
| `ConnectorDispatchRateLimited` | warning | The connectors service is shedding dispatches the detection engine admitted | [details](#v0180-new-warnings) |
| `CredentialAttemptStoreFull` | critical | The bucket that counts failed sign-ins is full | [details](#v0180-sign-in-limits) |
| `DeviceCredentialAttemptStoreFull` | warning | The bucket that counts failed MQTT password connects is full | [details](#v0180-mqtt-connect-backoff) |
| `DeviceFactPublishFailing` | warning | Rule, device and attribute notifications are failing to send | [details](#v0180-fact-repair) |
| `DetectFactsRepaired` | warning | The detection engine corrected a change it had not been told about | [details](#v0180-fact-repair) |
| `DetectFactReconcileFailing` | warning | The detection engine's comparison with device-management is failing | [details](#v0180-fact-repair) |
| `TenantPurgeStalled` | warning | A tenant deletion is not progressing | [details](#v0180-tenant-purge-alerts) |
| `TenantPurgeVisibilityLost` | warning | Nothing reports whether tenant deletions are progressing | [details](#v0180-tenant-purge-alerts) |

`ReactPoisonDropping`, `DeadLetterStoreLosing` and `DeadLetterWriteLost` keep their names and move
to their own rule group ([details](#v0180-dead-letter-rule-group)).

##### Services log at `info` by default, and the level can be set {#v0180-log-level}

Every service used to log at `debug`, whatever you wanted. Services now log at `info` by default,
and a new key in the instance configuration, `infrastructure.logging.level`, sets the level for
the whole instance. See [The log level](./observability.md#logs).

- **It accepts exactly `trace`, `debug`, `info`, `warn` or `error`, in lowercase.** Any other
  value, including `INFO` or a number, stops the services from starting, and the log names the key
  and the accepted values.
- **Lines you may have searched for are gone at the default level**: the per-message lines on the
  ingest path, the broker read and write confirmations, and similar diagnostics. Set `debug` to see
  them again. `debug` and `trace` write a line for every device message, so they are for
  diagnosing a problem, not for running.
- **A service's configuration document is no longer written to the log** at startup. The service
  logs `config_sha256`, the first 16 hexadecimal characters of the document's SHA-256, instead.
- **An instance installed with `dcctl bootstrap` runs at `info`**, and `dcctl` has no option to
  change the level yet. A chart install sets it with your other values, for example
  `--set instance.config.infrastructure.logging.level=debug`. With `instance.existingSecret`, add
  it to the document you supply and update `instance.existingSecretChecksum`.

##### Database messages are structured log lines, and a query that finds nothing is no longer logged as a failure {#v0180-database-log-lines}

Earlier releases printed database messages in a format of their own: coloured, multi-line text
written outside the service's JSON log, with no `instance` or `area` field, whatever
`infrastructure.logging.level` was set to. Every service that writes tenant data printed one such
block, reading `record not found`, for each database transaction that wrote it; in
`event-management` that was one for every event stored. The block was the ordinary answer to a
check made before each write, not an error, and at a high event rate it made up most of the log
and hid real failures.

- **Database messages are now JSON log lines**, at `error` when a statement failed
  (`database statement failed`) and at `warn` when one took longer than 200 ms
  (`slow database statement`), with the fields `sql`, `rows`, `elapsed_ms` and `caller`. A query
  that finds no rows is no longer logged as a failure. That includes the check before each write.
- **The `sql` field no longer shows the values a statement was sent with.** It shows the
  statement's placeholders (`$1`, `$2`, …). Earlier releases filled the values in, including in
  the line for a failed write. The database's own error message is still logged as it is, and some
  of those quote the value they rejected.
- **`sqlDebug` now follows the log level.** Its per-statement lines are written at `info`, so an
  instance whose level is `warn` or `error` no longer shows them.
- **A service that cannot reach its own database at startup logs each failed attempt at
  `error`**, as `failed to initialize database, got error …`. Earlier releases printed it as text
  outside the JSON log.

If you matched the old text (for example `record not found` or `SLOW SQL`), match the `message`
field instead. Nothing needs configuring.

##### Two new warnings: an unreadable stream, and connector sheds the detection engine admitted {#v0180-new-warnings}

- **`JetStreamReplicationUnobserved`** (warning, `jetstream-replication` group) fires when a
  running pod has been unable for 15 minutes to read the replication state of a stream it could
  read earlier. Until now a service that could not read a stream stopped reporting it, and the
  other replication alerts went quiet for that stream rather than saying so. It also fires for
  every stream on every pod during a broker outage, which is deliberate: nothing else in the chart
  reports one. It resolves six hours after the stream was last read, whether or not it can be read
  again. See [Replication](./observability.md#replication).
- **`ConnectorDispatchRateLimited`** (warning, `governance` group) fires when outbound-connectors
  has shed dispatches as over their tenant's outbound rate for 15 minutes. The detection engine
  already sheds over-quota actions before dispatching them, so this means the two services
  disagree about the ceiling (most often their platform defaults differ) or failing sends are
  being retried and metered again. Before upgrading, check that `outboundMessagesPerSecond` and
  `outboundBurst` are set the same for event-processing and outbound-connectors. Otherwise it
  fires whenever a tenant metered at the platform default sends faster than the lower of the two.
  See
  [Tenants metered at the platform default](./observability.md#tenant-ceilings).
- **`JetStreamLeaseBucketNotReplicated` has a new summary**, "The partition-lease bucket is not
  replicated". Its name, labels and severity are unchanged. Update any route or silence that
  matches on the old summary text.

##### The dead-letter alerts move to their own rule group {#v0180-dead-letter-rule-group}

The three dead-letter alerts, `ReactPoisonDropping`, `DeadLetterStoreLosing` and
`DeadLetterWriteLost`, now ship in their own `dead-letter` PrometheusRule (group
`devicechain.dead-letter`) instead of `event-processing`. Their names, labels, severities,
thresholds and descriptions are unchanged, so routes and silences that match on the alert name or
its labels keep working.

- **One that is pending or firing during the upgrade starts over.** Prometheus sees a new rule, so
  the alert resolves and comes back once its `for` wait (5 or 10 minutes) has passed, if the
  condition still holds.
- **If you select PrometheusRule objects by name**, or look up rule groups in the Prometheus UI,
  add `dead-letter`.
- **`DeadLetterStoreLosing` and `DeadLetterWriteLost` no longer end in `or vector(0)`.** An
  expression that returns nothing and one that returns a false comparison leave an alert in the
  same state, so the clause changed nothing. Both alerts fire and resolve exactly as before.

##### Two warnings for a tenant deletion that is not progressing {#v0180-tenant-purge-alerts}

A tenant deletion that cannot finish leaves the coordinator's own pass metrics healthy, so the
chart now alerts on it directly, in a new rule group, `devicechain.tenant-purge` (PrometheusRule
`tenant-purge`):

- **`TenantPurgeStalled`** (warning) fires when the oldest open deletion has been open for more
  than twice the configured token hold (24 hours at the default hold) for 15 minutes.
- **`TenantPurgeVisibilityLost`** (warning) fires when user-management's deletion gauges have not
  been collected for 15 minutes, so the first alert could not fire.

user-management exports the two gauges they read,
`devicechain_usermanagement_tenant_purge_in_flight` and
`devicechain_usermanagement_tenant_purge_oldest_age_seconds`. A `tokenHoldSeconds` of `0` is read
as the default, as the service reads it. See [How you are told](./tenant-deletion.md#stalled-alert).

##### Maintenance tasks report every pass, and sweeps no longer run on an exact interval {#v0180-maintenance-passes}

Ten maintenance tasks, the sweeps, reconcilers and schedulers that run on a timer, now export three
series each: `devicechain_<area>_<task>_passes_total{outcome}`,
`…_pass_duration_seconds` and `…_last_success_timestamp_seconds`. The outcome is `complete`,
`partial`, `failed`, `skipped` or `cancelled`. `skipped` (another replica holds the task's lock)
and `cancelled` (the service was stopping) are not faults, and a skipped pass does not move the
last-success time. [Maintenance passes](./observability.md#maintenance-passes) lists the tasks.

Five of them, user-management's dead-letter sweep and tenant-deletion coordinator,
notification-management's retention sweep and escalation scheduler, and event-management's anchor
sweep, now start each pass at a random point within 10% either side of their interval, rather than
exactly on it, so replicas that started together no longer reach the database in the same
instant. Nothing needs configuring.

#### Deployment and sizing {#v0180-deployment}

##### device-management and event-management may use more CPU {#v0180-cpu-limits}

`device-management` and `event-management` may now each use up to 2 CPU cores; every other backend
service keeps the 500m limit. At 500m, `device-management` resolved at most about 720 events per
second on a four-node test cluster, below the 1000 messages per second each tenant is allowed by
default, so a tenant sending at its allowance built a backlog that grew for as long as it kept
sending. `event-management` needs about half a core at that rate, its whole old limit, so it would
have been the next limit. See [Service sizing](./bootstrap.md#service-sizing).

The upgrade restarts those two services once, one pod at a time. Requests are unchanged, so no pod
needs more room on a node, and `--compact` instances keep their lower requests.

- **A service's `functionalAreas.<service>.resources` is now merged over the top-level
  `resources`, key by key,** instead of replacing it. The console's `frontend.resources` is not
  affected. Check the rendered pods before upgrading if your values set `resources` for a single
  service:
  - If you set only `requests` for a service, it had no limits and now gets the top-level limits.
    To keep a service running without a limit, remove that limit from the top-level `resources`
    and set it on the services that want one.
  - If you set only `limits` for a service, Kubernetes gave it requests equal to those limits; it
    now gets the top-level requests (100m and 128Mi, or 25m and 64Mi under `--compact`), which
    reserve less on the node and can change the pod's QoS class. Set that service's `requests` to
    keep its reservation.
  - If your block for `device-management` or `event-management` does not set `limits.cpu`, that
    service now gets the new 2-core limit.
  - A request above the merged limit is refused when the chart renders, naming the service; for
    example a service that sets only `requests.memory: 512Mi` against the top-level 256Mi limit.
    Set its limit too.
  - A key other than `requests`, `limits` or `claims` under a service's `resources` is refused.
- **A top-level `resources.limits.cpu` above 2 cores no longer reaches `device-management` or
  `event-management`,** because their new 2-core limit is their own and a service's own key wins
  over the top-level one. If your values raise only the top-level CPU limit, for example to 4,
  the upgrade LOWERS these two services to 2 cores. Set
  `functionalAreas.device-management.resources.limits.cpu` and
  `functionalAreas.event-management.resources.limits.cpu` to keep what they had. For the same
  reason, a top-level `resources.requests.cpu` above 2 cores, which rendered before, is now
  refused, naming the service's own limit as the one it is above; raise that limit too.
- **A namespace `ResourceQuota` on `limits.cpu`, or a `LimitRange` with a CPU `max`, can refuse
  the two pods** now that their limits are higher. Nothing DeviceChain installs creates either;
  check any you added.
- **To keep the previous limits,** set `functionalAreas.device-management.resources.limits.cpu`
  and `functionalAreas.event-management.resources.limits.cpu` to `500m`. That restores the old
  ceiling of about 720 events per second.

##### The in-cluster backup store pulls from a maintained image {#v0180-backup-store-image}

The object store that `dcctl install` runs in `dc-system` to hold database backups pulled its
MinIO image from `quay.io/minio/minio`. Those images are no longer published: the registry refuses
an anonymous pull, so on a machine that had not already cached the image, `dcctl install` stopped
with the object store in `ImagePullBackOff`. Clusters that already had the image cached kept
running, but only while the object-store pod stayed on a node that had it: a pod moved to another
node — by a drain, an eviction or a replaced node — could not start, and archiving stopped until it
could. Upgrading removes that exposure.

This release pulls `cgr.dev/chainguard/minio`, pinned by digest: a build of a maintained fork of
the same MinIO server, still licensed AGPL-3.0. It reads the data the previous server wrote as it
is.

- **Before upgrading, make sure your nodes can pull from `cgr.dev`** — allow it through any egress
  rules, or mirror `cgr.dev/chainguard/minio` at the digest this release pins. If the pull fails,
  the object store stays down and the databases keep their write-ahead log locally until it
  returns.
- **A fresh install works again** with the default backup destination.
- **On an existing cluster, `dcctl install` restarts the object-store pod once** onto the new
  image. Stored backups and write-ahead log are kept. While the pod restarts, archiving pauses and
  the databases hold write-ahead log locally, for as long as the new image takes to pull and start.
- **This cannot be undone by installing an earlier release.** An earlier release's `dcctl install`
  would stop the object store and then fail to pull the image it names, and archiving would stop
  until this release's `dcctl install` runs again.
- **If `dcctl install` failed on an earlier release because of this**, run this release's
  `dcctl install` again: it replaces the object store that attempt left behind (see [A failed `dcctl install` can be run
  again](#v0180-dcctl-install-rerun)).

Nothing needs configuring. If you point backups at your own object store with
`--backup-credentials-file`, nothing changes for you.

##### A failed `dcctl install` can be run again {#v0180-dcctl-install-rerun}

If `dcctl install` failed while the backup object store was starting, for example because its
image could not be pulled, or was interrupted at that point, every later run failed too, before
changing anything, with `Unexpected Identity Change` naming `kubernetes_deployment_v1`. This
happened with Terraform 1.12 or later, and the only way out was to delete the cluster and its
directory under `~/.devicechain/clusters/`. A re-run now deletes the half-created object store,
creates it again and waits for it to become ready. A cluster already stuck this way recovers the
same way, with no manual step, once the cause is fixed: run the same `dcctl install` again. If the
cause is still there, the re-run fails the same way rather than reporting the cluster installed.

- **`dcctl install` now checks that the backup object store has rolled out** before it reports the
  cluster installed. A change to the store that timed out, such as a new image that could not be
  pulled, used to be accepted by the next run, because the failed change had already been
  recorded. That run now waits up to five minutes for the store and then fails, naming it.
- **The Kubernetes provider moves from 2.38.0 to 3.2.1.** The fix is in that release. The first
  dcctl command that applies or destroys infrastructure on each cluster downloads it. Nothing on
  the cluster changes.
- **The infrastructure providers are now pinned to exact versions** (Kubernetes 3.2.1, Helm
  2.17.0), and dcctl moves each root's `.terraform.lock.hcl` onto them on every run. Before this, a
  cluster kept whichever versions its first install happened to resolve. Every run that applies
  or destroys infrastructure, `dcctl destroy` included, now asks the provider registry for these versions, so the registry, or a
  provider mirror you have configured, must be reachable. The unused TLS provider is no longer
  declared.
- **An earlier dcctl cannot operate a cluster this release has run against.** Its `init` refuses
  the lock file, which now names provider versions its configuration does not allow.
- **If you run the OpenTofu configuration directly** rather than through dcctl, run
  `tofu init -upgrade` once in each root: a plain `init` refuses a lock file on the old versions.

##### Release archives carry signed build provenance {#v0180-provenance}

From `v0.18.0`, each release publishes `devicechain_<version>_provenance.sigstore.json` (the
version without its `v`, e.g. `devicechain_0.18.0_provenance.sigstore.json`) beside the `dcctl`
and `dc-edge-agent` archives: a signed record of the workflow run that built them. To check
an archive you downloaded:

```bash
gh attestation verify <archive> --repo devicechain-io/devicechain
# or offline, against the bundle from the release
gh attestation verify <archive> --repo devicechain-io/devicechain \
  --bundle devicechain_0.18.0_provenance.sigstore.json
```

Nothing needs doing.

### Next release {#next-upgrade}

What the release after `v0.18.0` changes, collected as it lands.

#### `dcctl destroy` removes an instance's in-cluster backups, and new alerts warn before archiving stops a database

**`dcctl destroy` now removes an instance's backups from the cluster's own object store.** Once
the instance's namespace is gone, destroy deletes everything under the path its event store was
archiving to, and checks that the path is empty. It reads that path before it changes anything,
and prints it. Backups in an object store you supplied are never deleted: destroy prints where
they are. Pass `--keep-backups` to keep the in-cluster backups as well — and do pass it before
you rebuild an instance from its own backups in the same cluster with `--restore-tsdb-from`,
because a destroy without it deletes the archive that restore reads. If the object store cannot
be reached, destroy still finishes, and says what it left. Archives left behind by destroys run
with an earlier release stay where they are: after removing the instance's own backups, destroy
lists the ones under the same instance name, and [What happens to the instance's backups](./bootstrap.md#destroy-backups) shows how to remove
them.

**New alerts warn before archiving takes a database down.** `PostgresWALArchiveBacklog` fires
when a database is holding write-ahead log it has not shipped, including when the archiver is slow
or hung rather than failing. `BackupDestinationFillingFast` and `DatabaseVolumeFillingFast` fire
on how fast the backup store or an event-store volume is filling, not only on a fixed threshold.
The backup sizing guidance is corrected too: under sustained ingest, the archived log costs more
per event than the data, so the previous 20 GiB default in-cluster store filled in hours rather
than days; see the backup store item below. See
[Backups that stop shipping](./observability.md#backup-archiving).

#### A full ingest stream refuses new events instead of discarding unread ones

When a consumer fell so far behind that its unread backlog filled `inbound-events` or
`resolved-events`, the stream discarded its oldest events to make room, and those were events
nobody had processed yet. The device had already been told they were accepted.

Now, when `device-management`'s unread backlog on `inbound-events`, or `event-management`'s on
`resolved-events`, reaches 90% of what the stream can hold, the platform stops accepting new
events until the backlog drops below 80%. The two streams keep their week of already-processed
events, and that history does not count towards the limit. Only unread events do.

- **HTTP** ingest answers `503` with `Retry-After: 10` while it refuses. Retry on `503`. A `503`
  without a `Retry-After` still means the publish itself failed.
- **MQTT** devices were already acknowledged by the broker. Their messages wait in the capture
  stream until ingest resumes.
- **Sparkplug and LwM2M** readings, and messages from an external MQTT broker, are dropped and
  counted, because those protocols give the platform no way to make the device retry. Connect and
  disconnect transitions are still accepted, and nothing limits how many: a fleet that reconnects
  in a loop can still push the stream to its ceiling, where it discards its oldest events as before.
- The refusal applies to **every tenant**, because the streams are shared. A slow `device-state`
  or `event-processing` does not cause it.
- Two alerts are added: `JetStreamUnreadBacklogNearFull` (warning) and
  `JetStreamIngestBackpressureEngaged` (critical). See
  [Backpressure on the ingest path](./observability.md#ingest-backpressure).
- The simulator and load harness count a `503` with a `Retry-After` as shed, not failed.

Nothing to do at upgrade. No stream is reconfigured, and a service still on the previous release
keeps its previous behaviour until it is upgraded.

#### device-management answers repeated lookups from memory {#next-local-cache}

Nothing needs doing at the upgrade.

- **Each `device-management` replica keeps what it read from its key-value caches in memory for
  up to five seconds**, and answers repeated lookups for the same device, device type or tenant
  from there instead of asking NATS. It asks NATS again once what it holds is five seconds old,
  or sooner if the in-memory copy is full. A cache time to live below five seconds also shortens
  the in-memory copy.
- **A change can take up to five seconds longer to reach the events that other replicas
  resolve**, on top of what the cache's time to live already allowed. A device deleted, or
  re-created under the same token, can still resolve through its old record on another replica
  for those seconds, and a rule whose group scope was just changed can be evaluated there against
  the previous scope. Events that present a device credential are checked against the database
  every time, as before, and an alarm edge for a device that was just deleted is still dropped at
  once on every replica.
- **Four new metrics** count lookups answered from memory, entries dropped from it, and its size.
  `kv_cache_request_duration_seconds{op="get"}` now counts only the lookups memory could not
  answer. See [Caches that stop answering](./observability.md#kv-caches).

#### Services are sized from measured throughput {#next-service-sizing}

`event-sources` and `device-state` may now use up to 2 CPU cores, like `device-management` and
`event-management`. At 500m, `event-sources`' slower responses capped the rate devices could send,
and `device-state` let the live device view fall minutes behind at rates the rest of an
installation handled. The four services also **request** CPU sized from measurement instead of
100m each: `device-management` 500m, `event-management` 400m, `device-state` 400m,
`event-sources` 150m. `device-management`, `event-management` and `event-sources` prefer a node
that is not running the event store's primary. A new instance's event store gets a 32Gi volume
instead of 8Gi. See [Service sizing](./bootstrap.md#service-sizing), which also records the
measured throughput.

**Before you upgrade an instance installed without `--compact`:**

- **Check there is room for the larger requests.** Once upgraded, the instance requests about
  1 CPU more than before. During the rolling update each service runs its new pod beside the old
  one, so the nodes need the new requests free as well: about 1.5 CPU for these four services.
  Compare the nodes' free allocatable CPU (`kubectl describe nodes`, "Allocated resources") with
  the table in Service sizing. If a new pod cannot be placed, it stays `Pending` and the upgrade
  fails after waiting, with the instance partly upgraded: services whose new pods started are on
  the new release, and the rest are still on the old one. Make room and run `dcctl upgrade` again
  to finish. An instance built with `dcctl` has no way to keep the old requests; the remedy is
  capacity.
- **A `ResourceQuota` or `LimitRange` on the instance's namespace** can refuse the new 2-core limit
  of `event-sources` and `device-state`, or the larger requests, as it could for
  `device-management` and `event-management` in v0.18.0.

Instances installed with `--compact` keep their 25m and 64Mi requests.

**If you install the chart yourself, with your own values:**

- A top-level `resources.requests.cpu` no longer reaches `device-management`,
  `event-management`, `device-state` or `event-sources`: their measured request wins over it. Set
  theirs under `functionalAreas.<service>.resources.requests`, or set `useMeasuredRequests: false`
  to apply the top-level requests to every service again.
- A top-level `resources.limits.cpu` above 2 cores now lowers `event-sources` and `device-state`
  to 2, as it already did for the other two. Set theirs under
  `functionalAreas.<service>.resources.limits`.
- A service's own CPU limit below its measured request (for example `100m` on `event-sources`)
  rendered before and is now refused, naming `measuredRequests`. Raise the limit, set the
  service's own request, or set `useMeasuredRequests: false`.
- The chart's top-level values and a service's block under `functionalAreas` now refuse a key
  the chart does not read, so a misspelled key fails the render instead of being ignored.

**The event store volume:** nothing changes for an existing instance; only instances created by
this release get 32Gi. To grow an existing one, see [Event store
volume](./bootstrap.md#event-store-volume). A new cluster's backup store is now sized so that
the event store fills first; see the backup store item below.

The upgrade changes the pod templates of `device-management`, `event-management` and
`event-sources`, so the rolling update schedules their new pods under the new placement
preference. A preference applies only when a pod is scheduled, though: if the event store's
primary later fails over to another node, a pod already running there stays until it is next
rescheduled.

#### event-management stores events with 10 writers and batches of up to 64 {#next-persistence-defaults}

With the other per-event services sized to keep up, storing events became the first limit of a
default installation: 5 writers committing up to 32 events each filled every batch from about
4,400 events per second and stored no more than about 4,200. `persistence.writers` now defaults to
`10` and `persistence.maxBatch` to `64`. Those were the settings of a tuned run that kept up to
about 6,000 events per second with the CPU limits of `event-management` and the other per-event
services raised to 4 cores. `event-management` used at most about 1.7 of them, and it was not
measured under its default limit of 2, so no sustained rate is claimed for a default installation.
That run also set `device-state`'s `projection.maxBatch` to `64` and `projection.lingerMillis` to
`25`, which a default installation does not. Its batches averaged below 32, so it does not show the
larger batch helping. See [Measured throughput](./bootstrap.md#measured-throughput) for the full
settings.

**Before you upgrade:** if you set `tsdbConfiguration.maxOpenConnections` for `event-management` to
`10` or less and did not set `persistence.writers`, the new `event-management` pod refuses to
start, and its error names `persistence.writers` and the size of the pool. The rolling update
keeps the old pod running and storing events, and `dcctl upgrade` fails after waiting, with the
instance partly upgraded. Set `persistence.writers` below your pool (your previous default was
`5`), or remove the pool setting to use the default of 20, and run the upgrade again. Pools of 11
to 19 start, and log at startup that more than half the pool is given to writers; set
`persistence.writers` to half your pool to silence it.

- An installation that sets `persistence.writers` or `persistence.maxBatch` keeps its values.
- At rates where a writer finds one event at a time, nothing changes: it commits that event alone,
  as before.
- Under a backlog, up to 10 writers commit at once instead of 5, from the same pool of 20. The
  pool's ceiling is unchanged, so the connections the event store keeps for `event-management`
  still cover it.
- `maxBatch` still accepts `1` to `64`.
- `--compact` installations get the same defaults; their requests, limits and volumes do not
  change.
- Every measurement behind the new defaults was on a replicated (`--ha`) event store, where each
  commit waits for a standby. An installation with a single event-store instance has not been
  measured with them.

#### Stream warnings are based on unread messages, not on history {#next-unread-alerts}

`JetStreamStreamNearFull` fired for any stream over 80% of its byte ceiling. Streams keep a week of
messages, most of them already processed, so on a busy instance it fired while nothing was at risk.

- **New: `JetStreamDurableUnreadNearFull` (warning).** Fires when a consumer has not yet read more
  than 80% of what its stream can hold, for 5 minutes. That is what happens before a stream
  discards messages a consumer never read. Messages already read do not count. It covers every
  consumer except the two that hold ingest back, which `JetStreamUnreadBacklogNearFull` covers.
- **Changed: `JetStreamStreamNearFull` is now `info`, and fires only for a stream that holds
  records for an operator:** `failed-decode`, `failed-events`, `connector-dispatch.dead`,
  `max-deliveries`, and `dead-letters` while `user-management`, which stores its letters, does not
  report reading it. Nothing processes what these streams hold, so near their ceiling they are
  about to discard records nobody has looked at.
  It now also counts a stream's message ceiling, not only its bytes. If you route or silence alerts
  by name or severity, check those rules: the default Alertmanager configuration of
  kube-prometheus-stack does not deliver `info` alerts.
- New series: `devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}` and
  `devicechain_<area>_jetstream_stream_sink{stream}`.

Nothing to do at upgrade. See [Messages a consumer never read](./observability.md#unread-loss).

#### device-management keeps more devices' lookups in memory, and makes an event's lookups at the same time {#next-per-device-cache}

Nothing needs doing at the upgrade.

- **The in-memory copy described [above](#next-local-cache) holds up to 131,072 entries, or
  24 MiB, per replica for each of the three lookup caches kept per device** (a device by its
  token, its tracked relationships, its group memberships), so a replica that sees a large fleet
  within five seconds can answer its lookups from memory. The caches kept per device type and per
  tenant hold 4,096 entries or 4 MiB. Set
  `inMemoryCache.perDeviceCacheEntries` and `inMemoryCache.perDeviceCacheMiB` in
  `device-management`'s configuration to change the bound, and raise the service's memory limit
  with it. A release before this one refuses to start with either setting, so remove them before
  going back to one.
- **A device that reports less often than every five seconds is still not answered from memory**,
  however large the cache: a value is kept for five seconds from when it was read, and each of
  that device's events still reads the key-value bucket once. See
  [Caches that stop answering](./observability.md#kv-caches) for what that costs and what to
  raise.
- **An event's profile, relationships and group-scope lookups are made at the same time**, and so
  are its group-membership lookups, instead of one after another. An event that misses memory for
  all three waits for one round trip to NATS rather than three. The database is still read one
  lookup at a time, for whatever the caches could not answer, so `resolution.workers` still
  counts connections as before, and a measurement that fails validation still never reads its
  relationships from the database.
- Two more metrics, `kv_cache_local_max_entries` and `kv_cache_local_max_bytes`, give each cache's
  bound. `kv_cache_local_bytes` counts each entry's full size in memory.

#### The backup store is sized so the event store fills first {#next-backup-store-size}

A new cluster's in-cluster backup store is **160 GiB** instead of 20 GiB, and **20 GiB** instead
of 8 GiB under `--compact` when TLS is kept. That is disk the cluster now claims on its default
StorageClass. At 20 GiB, sustained ingest filled the store after 12 to 16 million events, well
before a 32 GiB event store is full. Archiving then stopped, and the event store's primary filled
its own volume with write-ahead log it could not ship. Measured on Google Kubernetes Engine, the
archive costs up to about 1.9 KB per event for both databases, not the roughly 1 KB published
before. The new size holds the archive of one full default event store with more than a third of
the store free. See [Backup store size](./bootstrap.md#backup-store-size), including when one
instance's worth is not enough: several instances ingesting, an event store that takes more than
about a day to fill or never fills, or a grown event store.

**Existing clusters keep their store's size.** The store's volume is now sized only when it is
created: `dcctl install`, the first step of every upgrade, leaves an existing store's volume
alone, and so does a direct `tofu apply`. Without that, the new default would ask a StorageClass
without volume expansion to grow the volume, which it refuses, and a store you had grown by hand
was already asked to shrink back to the default, which every provisioner refuses. Setting
`backup_object_store_storage` on an existing store now does nothing. To give an existing cluster
the new size, grow the volume yourself, on a StorageClass that allows expansion, as that page
shows. On kind the size is not enforced, so nothing needs doing.

#### Each database keeps its own recovery window: 30 days for core data, 7 for event data {#next-backup-retention}

The `backup_retention` setting, which each OpenTofu configuration declared under the same name, is
replaced by one setting per database: `backup_retention_rdb` in the cluster configuration, default
`30d`, and `backup_retention_tsdb` in the instance configuration, default `7d`. The relational
database holds tenants, users, devices, rules, secrets and each device's last-known state, and it
can now be recovered to any point in the last 30 days instead of 7. The event store keeps 7 days,
as before. A window must be a whole number and a unit, `d` for days, `w` for weeks or `m` for
months, and anything else is refused before the apply starts. See
[Recovery windows](./bootstrap.md#backup-retention).

A deleted tenant's core data now also stays restorable from backups for 30 days rather than 7,
until it ages out of the window. See [What is deliberately kept](./tenant-deletion.md#retained).

**At upgrade**, `dcctl install` gives the relational database the new window. The change is to
that database's backup configuration; the database cluster's own specification does not change.
Backups already taken are kept. Nothing ages out of a 30-day window until it is 30 days old, so for
about three weeks after the upgrade the relational database's share of the backup store grows:
toward about four times the log it keeps today, plus about 23 more nightly base backups. Then it
levels off.

**Before you upgrade, check the store's headroom.** An existing cluster keeps its store's size (see
the previous item), so a store created before this release is still 20 GiB unless you grew it, and
so is the store under `--compact`. Where the event store never fills because a retention window
bounds it, the sustained ingest rate that fills a 20 GiB store falls from about 19 to about 13
events per second, and the default 160 GiB store's from about 150 to about 100. Those figures use
the relational database's share of the log measured once, about 14% for a small fleet reporting
fast. It is not measured for larger or slower fleets, where it is likely higher; if the relational
log were all of it, the 160 GiB figure would be about 35. If the store has little room to spare,
grow its volume first, as [Backup store size](./bootstrap.md#backup-store-size) shows. If you apply
the OpenTofu configuration yourself, you can instead keep the old window with
`backup_retention_rdb = "7d"`; `dcctl install` has no option for it.

**If you set `backup_retention`, rename it**: `backup_retention_rdb` in the cluster configuration,
`backup_retention_tsdb` in the instance configuration. How the old name fails depends on where it
is set. A `-var backup_retention=…` is refused. A `backup_retention` line in a `.tfvars` file draws
only a warning, and the store then gets its new default instead of your value: 30 days for the
relational database, 7 for the event store. A `TF_VAR_backup_retention` environment variable is
ignored without any warning.

#### Any service can serve Go runtime profiles, off by default {#next-profiling}

Every service can now serve Go runtime profiles (CPU, heap, allocations, goroutines and the
execution trace), so you can measure where a service spends its time instead of inferring it.
It is off unless you turn it on for a service with
`functionalAreas.<service>.profiler.enabled: true`. Only that service's pods restart. The
profiles are served on a listener of their own, on the pod's loopback address by default, so
you reach them with `kubectl port-forward`. That listener is never a container port, a Service
port or an ingress route. Nothing changes for an instance that does not set it, and nothing
needs doing. See [Profiling a service](./observability.md#profiling).

#### Database base backups can be volume snapshots {#next-snapshot-backups}

On a cluster whose storage driver takes CSI volume snapshots, `dcctl install
--backup-snapshot-class <class>` takes each database's daily base backup as a volume snapshot
instead of a full copy in the backup store. Nothing changes unless you pass the flag. See
[Volume-snapshot base backups](./bootstrap.md#snapshot-base-backups).

- Log archiving does not change, and a full base backup still goes to the backup store once a
  week, on Sunday at 04:00. The store prunes archived log only against the base backups it holds,
  and every restore reads the store.
- The class must exist, have `deletionPolicy: Delete`, and belong to the driver that provisions
  the database volumes. `dcctl install` checks all three before it changes anything, and so does
  each `dcctl bootstrap`. Google Kubernetes Engine and Azure AKS include a snapshot controller;
  on Amazon EKS, install the snapshot controller add-on first.
- CloudNativePG does not delete old snapshots. The DeviceChain operator now does, every ten
  minutes: it keeps every snapshot inside the database's recovery window and the newest one
  before it. To do that, the operator's ClusterRole gains, in every namespace: `get` on
  namespaces; `get`, `list` and `delete` on CloudNativePG Backups; `get`, `list` and `patch` on
  CloudNativePG ScheduledBackups, to record each pass; and `create` and `patch` on
  `events.k8s.io` Events, to report what it pruned. It acts only in namespaces DeviceChain
  created, on the ScheduledBackups its own configuration renders.
- A restore (`--restore-rdb-from`, `--restore-tsdb-from`) still reads the backup store, not the
  snapshots: the newest weekly base backup and the log since, so it can replay up to a week of
  log. An instance's snapshots are deleted with it.
- The backup store keeps up to a week more log for each database, so it fills sooner where log
  is most of what it holds: with the default windows and store, at about 60 events per second of
  sustained ingest rather than about 100.
- `PostgresNoRecentSnapshotBackup`, `DatabaseSnapshotPruningStalled` and
  `DatabaseSnapshotBackupsUnobserved` are new, and on such a cluster `PostgresNoRecentBaseBackup`
  waits 8.5 days instead of 36 hours. The monitoring stack's kube-state-metrics now also reads
  CloudNativePG Backups and ScheduledBackups, which those alerts need.
- The setting belongs to the cluster: every instance follows it, and changing it is refused
  while instances run on the cluster.

**Re-run `dcctl install` with this release before any bootstrap, upgrade or destroy.** The
install record has a new field, and this `dcctl` refuses a record written by an earlier one:
`dcctl bootstrap` and `dcctl upgrade` stop and say so, and `dcctl destroy` still removes the
instance but leaves its database and login in the shared relational database and its backups in
the in-cluster store, and says it did.
Re-running install is already the first step of every upgrade.

#### An event carries at most 256 readings, and gateways split larger messages

**One event now carries at most 256 readings, on every transport, and the limit is not
configurable.** A reading is one metric value of a measurement, or one location or alert entry.
Before this release, the JSON device event on HTTP and MQTT accepted up to 1000 readings by
default, and an operator could raise that without an upper bound, or lower it.

**Before you upgrade,** you can apply the new limit early: set `maxReadingsPerMessage: 256` in
the event-sources configuration on your current release and watch `total_msg_too_many_readings`.
Every message it counts is one this release refuses, so change those devices' firmware to send at
most 256 readings per message.

- **HTTP and MQTT:** a message with more than 256 readings is refused whole, never trimmed. HTTP
  answers `400`, naming the count and the limit. On MQTT the device is not told, because the
  broker acknowledges before decoding. The refusal is counted on `total_msg_too_many_readings`
  and the message goes to the failed-decode stream. A device that batches more than 256 readings
  must split them across messages. Messages captured before the upgrade and decoded after it,
  including any still spooled at an edge agent, are judged on the new limit.
- **The `maxReadingsPerMessage` setting is retired.** If your event-sources configuration still
  sets it, the service starts, logs a warning and ignores it. A value you had set lower than 256
  is no longer honoured either: the limit is 256. Remove the key.
- **Sparkplug B:** a message with more than 256 metric values used to become one event. It now
  becomes consecutive events of at most 256, with every value at its own timestamp. Queries that
  count *events* see more of them for wide Sparkplug messages; the stored readings are the same.
  Rules see each event on its own, so a hold-time or absence rule can now fire between two events
  of one wide message.
- **LwM2M:** a Notify with more than 256 numeric values used to keep the first 256 and drop the
  rest. It is now stored as several events, and the tenant's sample budget is charged one event
  at a time: a Notify larger than the budget can admit at once keeps the events it admits, and
  the rest are counted on `ingest_samples_shed_total`. The `notify_samples_truncated_total` metric
  is removed. Remove it from any dashboard or alert.

#### An event carrying thousands of readings is stored {#next-large-events}

An event with more readings than fit in one database statement (more than about 5,950
measurements, 5,450 locations or 6,550 alerts, or 9,350 relationship anchors) could never be
stored. The database driver refused the statement, `event-management` retried the event until
its deliveries ran out, and then recorded it on the `failed-events` stream as a downstream
failure rather than as a problem with the event. Before the 256-reading limit above, such an
event could come from a Sparkplug message with thousands of metrics, or from a JSON transport
whose `maxReadingsPerMessage` had been raised above its default of 1000. The event store now writes a large event in as many
statements as it needs, inside the same transaction, so it is stored whole or not at all like
any other, and a redelivery of it adds nothing. The same retry-then-downstream-failure path
was taken by a state-change event whose session id is too large for the database's signed
64-bit column; that event is now recorded as invalid on its first delivery. Nothing needs doing.

#### The event store updates fewer indexes for each event {#next-event-store-indexes}

`event-management` removes twelve indexes from the event store. Each one either repeated what
another index already gave the same queries, or was read by no query the platform makes. Each
stored row now updates fewer indexes: a base event row three instead of five (four instead of six
when it carries an alternate id), a measurement row four instead of five, a location, alert or
relationship-anchor row two instead of four, and a presence-change row one instead of four. A
measurement event with one reading and no anchors, for example, updates seven indexes instead of
ten.

- The indexes that stop an event being stored twice are unchanged, and every read
  `event-management` serves is still served by an index.
- **A device's event list does more work on recent data.** The total shown with a device's
  event list, and a list of a device's events filtered by event type, now visit every one of the
  device's rows that is not yet compressed (the last week of data, by default) instead of only
  the rows they count or return. That includes the rows of a device with the same token in any
  other tenant, so a busy device named `gateway-1` in one tenant also slows the total for
  `gateway-1` in another. Compressed data is read per device and tenant, as before. A device that
  sends events at a high rate shows this most.
- **SQL and BI access.** A query on `analytics.event_anchors` or `analytics.state_change_events`
  that filters on time alone now reads all of your tenant's rows in each not-yet-compressed chunk
  the range touches (a day of data per chunk, by default), rather than only the rows in the
  range. Add an anchor filter (`anchor_type` and `anchor_token`) or a device filter
  (`device_token`) and it is served by an index as before. The other views are unaffected.
- **At the upgrade.** The first time the new `event-management` starts, it removes the indexes
  one at a time. Removing one needs a moment when no other transaction is using that table, and
  while it waits, reads and writes of that table wait with it. Each attempt gives up after at
  most 5 seconds, and a busy table is retried every 2 seconds for up to a minute. If a long
  query, a tenant erasure, or the database's own compression or retention job keeps a table busy
  longer, `event-management` stops with an error
  that names the table and the index, and continues from there when it restarts; the previous
  `event-management` keeps storing events meanwhile. The error also carries a query that lists
  the sessions holding the table or any of its chunks. If it keeps stopping, look for
  long-running SQL or BI queries against the event store. Removing an index also locks every
  chunk of its table; if the database runs out of lock slots, the error says so and names the
  setting to raise.
- Going back to `v0.18.0` leaves the indexes removed, and `v0.18.0` works without them.

#### The databases' primaries prefer different nodes {#next-primary-spread}

Each database now prefers a node that is not running another DeviceChain database's primary. In
testing on three 8-vCPU nodes, the relational and the event-store primary had been placed on the
same node, which ran at 94 to 98% CPU while the other two ran at 45 to 51%.

- It is a preference, not a requirement: a cluster with fewer nodes still schedules every
  database instance.
- It acts when a database pod is scheduled, which in practice means when an instance's event
  store is created. A failover, a switchover, or the switchover that ends a rolling update can
  still leave both primaries on one node. [Where the database primaries
  run](./bootstrap.md#ha-database-primaries) shows how to check and how to move one.

**Before you upgrade, check for a quota on cross-namespace placement.** The database pods now
carry a placement preference that looks at other namespaces. A `ResourceQuota` with the
`CrossNamespacePodAffinity` scope refuses such pods, preferred or not, in a namespace where it
forbids them: the relational database's restarted instances in the cluster's namespace
(`dc-system` by default), and a new instance's event store in its own namespace. Nothing
DeviceChain installs creates one. To list any that exist:

```bash
kubectl get resourcequota -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,SCOPES:.spec.scopeSelector
```

An empty list does not settle it. The API server's quota admission configuration can name
`CrossNamespacePodAffinity` under `limitedResources`, and then such pods are refused in every
namespace that has **no** quota with that scope admitting them. That configuration lives in the
control plane, not in a `kubectl` object, so ask whoever runs the cluster whether it is set; if it
is, give `dc-system` and each instance's namespace a quota with that scope before upgrading.

**At the upgrade.** The relational database takes the new setting the next time you run `dcctl
install` with this release, and its instances restart once. Under `--ha` the standbys restart
first and the primary role is then switched over to one of them, which is on another node. If
the two primaries shared a node before the upgrade, that switchover moves them apart; if they did
not, it can put them on one node, so check where they are afterwards. A single-instance
installation restarts its only instance in place, and the relational database is unavailable
until it has restarted; writes made meanwhile are retried.

An existing instance's event store is not changed: `dcctl upgrade` does not re-apply it, and its
instances are already placed. It does not carry the label the other databases look for, so
neither the relational database nor a new instance's event store avoids its primary, and on an
installation whose instances all predate this release the relational database restarts for a
preference that has nothing to act on until an instance is bootstrapped. Instances bootstrapped
with this release take part. Run `dcctl install` before you bootstrap a new instance, so that the
relational database's pods carry the label the new event store looks for.

#### Services keep their database connections open between uses {#next-warm-pool}

A service's connection pool used to keep only half of its connections open between uses: 10 of
the default 20. Whenever more than half the pool was in use at once, every connection over that
half was closed when it was released and opened again for the next query, which costs the service
and the database a new login each time. `device-management` reaches that point when
`resolution.workers` is raised above 10, since its resolvers share the pool with its GraphQL API,
its MQTT connect checks and its alarm consumer. In a CPU profile with 16 resolvers on the default
pool, at about 5,200 events per second on three 8-vCPU nodes, logging in again took 14–15% of
`device-management`'s CPU, against 0.2% with the default 10 resolvers.

- **Every connection a pool opens now stays open between uses**, up to the pool size. A
  connection is still closed an hour after it was opened, as before, and opened again when it is
  next needed. Raising `resolution.workers`, `persistence.writers` or `projection.writers` towards
  the pool size no longer makes a service reconnect.
- **The database can show more idle connections from each service after a busy period**: up to
  the size of its pool. At the default configuration nothing needs doing: an instance's
  [connection budget](./bootstrap.md#connection-budget) allows each area one pod with a full pool
  of the default size, plus one more pod during a rollout. If you run a service at `replicas`
  above 1, or have raised its `maxOpenConnections`, its pods now keep those connections after a
  busy period instead of giving back all but half of them, so check that the instance's connection
  limit still covers them.
- `maxIdleConnections` is still honoured when set, up to `maxOpenConnections`. Setting it lower
  holds fewer connections on the database, at the cost of a new connection and login for every
  query that finds more than that many in use.

### The one-time durable-ingest cutover

The release that introduces **durable MQTT ingest** changes how `event-sources` receives
device telemetry: instead of subscribing to the broker as an MQTT client, it consumes a
durable capture stream that the broker writes to before it acknowledges the device. This
is what stops telemetry being lost when `event-sources` is down.

Crossing that release once is an ordinary in-place upgrade — but expect a **brief window of
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
databases first, then re-run `dcctl install` with `--allow-legacy-db-removal` for the
relational database, and the bootstrap with it for the event store — which asserts you have
handled the data, and verifies nothing. For a local instance, recreating it is simpler and
discards the data deliberately — destroy it, recreate the cluster, then install and
bootstrap, as described under
[Instances built by v0.16.0 and earlier](#pre-declaration-recreate).
:::

This is durability of the running volumes — it is not a substitute for scheduled backups and
point-in-time recovery, which are provisioned with the production infrastructure. See
[Deployment & Operator](./kubernetes-operator.md) for how the infrastructure and application
layers are separated.
