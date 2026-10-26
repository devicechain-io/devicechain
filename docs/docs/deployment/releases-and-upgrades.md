---
sidebar_position: 4
title: Releases & Upgrades
---

# Releases & Upgrades

DeviceChain ships as a set of prebuilt, versioned container images plus a Helm chart.
You do **not** need to build anything to run it — pull a released version, install the
chart, and upgrade in place with a rolling update.

:::warning Some versions cannot be upgraded into
Four points in the history can require recreating the instance rather than upgrading it:

- **`v0.9.0`** replaced every service's migration chain with a single frozen baseline, so a
  `v0.8.x` database meets it and fails on `already exists`. See
  [The v0.9.0 baseline squash](#v090-baseline-squash).
- **`v0.10.0`** changed the primary key of the event tables to fix a defect that was
  silently discarding telemetry. See [The v0.10.0 event key change](#v0100-event-key).
- **anything built by `v0.16.0` or earlier**, which recorded no declaration of what the
  instance is — the record an upgrade now reads to know what to deploy. See
  [Instances built by v0.16.0 and earlier](#pre-declaration-recreate).
- **`v0.19.0`, for an instance whose event store holds more than 4,000,000 rows not yet
  compressed**, or a table of more than 500 chunks whose oldest events you cannot remove: its
  first start rebuilds the event store's keys over those rows and refuses above those bounds. See
  [Check the row count](#v0190-row-count).

If you are on any of them, read the matching section below before you do anything else.
:::

:::caution Upgrade through v0.23.0
Releases after `v0.23.0` write events in a form that `v0.22.0` and earlier cannot read. Coming
from `v0.22.0` or earlier, upgrade every instance to `v0.23.0` first, and only then to a later
release. See [The event wire format](#event-wire-format).
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
[Rolling upgrades](#rolling-upgrades) for the procedure.

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
- **refuse an in-place upgrade above a data-size bound** — `v0.19.0` refuses an event store
  holding more than 4,000,000 rows not yet compressed, or a table of more than 500 chunks, and
  says so at the top of its notes

The "upgrade in place with a rolling update" property above describes the *mechanics* of a
rolling upgrade. It is not a promise that your existing API calls keep the same meaning
across a pre-1.0 version bump.

From v1.0.0 this section is replaced by a semantic-versioning compatibility promise: breaking
changes only in a major version. See [Versioning and upgrade policy](./versioning-policy.md).

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

<a id="zero-downtime-upgrades"></a>

## Rolling upgrades {#rolling-upgrades}

Upgrading is **two commands** — one for the cluster, then one for each instance on it — and
the chart and services are built to roll forward one pod at a time, so an area with more than one
replica keeps serving while it rolls. Five
exceptions are documented below: the durable-ingest cutover, which is still an ordinary
upgrade but has a visible side effect; **`v0.9.0`, `v0.10.0` and any instance built by
`v0.16.0` or earlier, which cannot be upgraded into at all**; and **`v0.19.0` for an instance
whose event store holds more than 4,000,000 rows not yet compressed**, or a table of more than
500 chunks whose oldest events you cannot remove ([Check the row count](#v0190-row-count)).
Check the release notes for the version you are moving to before running it:

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

:::note What has been measured
The upgrade has been rehearsed by hand on a local cluster: an instance installed with `--ha` was upgraded
in place with MQTT traffic running (four devices publishing at QoS 1, 20 messages a second between them,
with `infrastructure.nats.mqttFailClosed` off, as it is by default), its broker rolled twice, and devices
reconnected after each roll. In one rehearsal every message the broker had acknowledged was still stored
afterwards (11,009 of 11,009). In another, 11 of 11,262 acknowledged messages were lost, all of them while a
broker server restarted and the MQTT capture stream changed leader. With `mqttFailClosed` off, expect a
short window of loss like this at every broker roll, at any rate of traffic; see
[MQTT, the platform broker](../reference/transport-matrix.md#mqtt--the-platform-broker). The rehearsals
are manual. The automated upgrade check that gates a release runs with no traffic.

An upgrade under sustained production load, on a cluster of production size, has not been
measured, so DeviceChain does not promise an upgrade without interruption. Plan for a short
interruption wherever a pod is replaced: an area with a single replica, and each broker server.
Without `--ha` the single broker server restarts and the broker is unavailable meanwhile.
A device that sends while it is disconnected must retry.
:::

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

1. **the instance's infrastructure**: its message broker (NATS) and its event store, applied
   from the OpenTofu configuration this release ships, so a release that changes either reaches
   existing instances and not only new ones. See
   [What an upgrade applies to the infrastructure](#upgrade-infrastructure);
2. **the configuration document** every service reads its credentials and endpoints from,
   recomposed from this release's chart and written by `dcctl`, which owns it;
3. **the Helm release** that runs the services, which rolls them onto the new images and
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

:::warning Upgrade the platform before the edge agents
An edge agent on the [gateway uplink](./edge-services.md#gateway-identity) publishes in the gateway namespace. A platform whose `event-sources` does not know that namespace, running with `infrastructure.nats.mqttFailClosed` on, acknowledges those messages at the broker and drops them, and nothing on the agent shows it. Finish upgrading the cluster and every instance (every replica of `event-sources` and `device-management`, and the broker's grants) before you restart any agent on a version that uses it. An agent whose broker has not been upgraded cannot subscribe to its notices and does not report its uplink connected.
:::

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
one. To add a functional area to a running instance, use
[`dcctl enable-area`](./bootstrap.md#enable-area), which records it in that declaration.

One thing is deliberately outside this command as well: it does not touch the databases' data
beyond letting the services run their own migrations, and it never resizes a volume.
:::

### Operator tuning is carried {#upgrade-tuning}

An upgrade does not change an instance's shape, but it does keep the tuning you gave it. A values file passed to `dcctl bootstrap --values` or `dcctl upgrade --values` is recorded on the instance and applied again by every later upgrade; `--reset-values` removes it. See [Tuning an Instance](./tuning-an-instance.md).

The fail-closed MQTT acknowledgement is carried the same way: `--mqtt-fail-closed` on `dcctl bootstrap` or `dcctl upgrade` is recorded on the instance, and an upgrade that omits the flag keeps what was recorded, on or off. See [fail-closed MQTT](./observability.md#mqtt-fail-closed).

### What an upgrade applies to the infrastructure {#upgrade-infrastructure}

Before it moves the services, `dcctl upgrade` applies the instance's OpenTofu configuration,
the same one `dcctl bootstrap` applied when it built the instance. That configuration holds the
instance's NATS servers and its event store: the database's image, settings and placement, and
its backup and archive configuration. The upgrade plans first and prints what will change; with
`--dry-run` it prints the plan and stops. It needs `tofu` on `PATH`, as a bootstrap does, and
preparing the configuration asks the provider registry (or your mirror) for its providers; a
dry run does that too, in the local working directory it plans in.

It keeps what the instance is running on. Every credential is kept, as above. The JetStream
volume and the event store's volume keep their sizes. When this release creates a different
size for new instances, the upgrade says so, and
[Event store volume](./bootstrap.md#event-store-volume) shows how to grow one yourself. The event
store keeps the path it archives under, and a store that was restored keeps the archive it was
restored from.

It refuses, and changes nothing, when:

- **This machine has no state for the instance.** The configuration's state lives in
  `~/.devicechain/instances/<instance>/` on the machine that bootstrapped it. Run the upgrade
  there, or copy that directory first. It holds credentials, so keep it private (mode `0700`).
- **The plan would delete or replace something the configuration manages**, such as the broker
  or the event store. The refusal names what. The release notes for such a release say how to
  move. This judges the resources the configuration manages; an object dropped from inside one
  of them is shown as a change to that resource.
- **The plan would shorten the event store's recovery window, or stop declaring an analytics
  reader**, that nothing this upgrade reads declares. Those are values set by hand, and applying
  without them would prune backups or leave a reader's password unmanaged. Declare them as
  described below and run the upgrade again.
- **The plan would switch the event store's backups off.** No declaration gets this through:
  `dcctl` decides whether backups are on from how the cluster was installed. `--skip-infrastructure`
  moves the services and leaves the event store as it is.
- **The broker or the event store is not healthy before it starts**, such as a server that is
  not ready or a store mid-failover. Bring it back first. The one exception is a broker whose last
  roll did not finish, with some servers already on new settings and one of them not ready. Only
  an apply can change the settings it is rolling to, so the upgrade applies over it, says so with
  the plan, and waits for the new settings to roll out. A broker short of a server on the settings
  every server already runs, for example after losing a node, is still refused.
- **The plan changes the event store's image and its settings together** on a store of more
  than one instance, which the database operator refuses. The refusal gives the order to apply
  them in.

`--skip-infrastructure` moves only the services, as upgrades before this release did, and the
upgrade ends by saying what it left.

**Values you set on the configuration yourself.** An upgrade applies the configuration with
`dcctl`'s values, as a bootstrap does. A value is kept when it is declared in a
`terraform.tfvars` or `*.auto.tfvars` file beside the instance's state, in
`~/.devicechain/instances/<instance>/infra/instance/`, or in a `TF_VAR_<name>` environment
variable: OpenTofu reads both on every apply `dcctl` runs. A value passed by hand with `-var` is
not kept. The values operators are told to set this way include `backup_retention_tsdb`,
`timescale_analytics_readers` (see [SQL and BI access](../guides/sql-and-bi-access.md)) and the
broker's `nats_cpu_request`, `nats_memory_request` and `nats_memory_limit`. A broker request or
limit the upgrade would lower is printed as a warning with the plan.

This works only for a variable `dcctl` does not pass itself, because OpenTofu lets a `-var` on the
command line override those files and `TF_VAR_`. `dcctl` passes, on every apply:

- **for an instance installed with `--compact`**, the broker's `nats_cpu_request` and
  `nats_memory_request`, and the sizes of its volumes. Setting those requests in
  `terraform.tfvars` has no effect on a compact instance. `nats_memory_limit` is not among them.
- **from how the cluster was installed**, whether database backups are on
  (`enable_database_backups`), the snapshot class (`backup_snapshot_class`) and the databases'
  node placement. Change those through `dcctl install`, not in this file.

The upgrade's warnings and refusals say which case a value is in.

What a running instance sees:

- **NATS servers restart when their settings change.** Under `--ha` they restart one at a time;
  each is Ready before the next stops. Without `--ha` the single server restarts and the broker is unavailable
  while it does, usually about a minute. Services and devices reconnect on their own; publishes
  made meanwhile are refused. If the node has no room for the server's new requests, the server
  stays `Pending`: under `--ha` the other two keep serving, but without `--ha` the broker stays
  **down** until there is room, and the upgrade fails after up to 15 minutes saying so.
- **The device authentication callout is encrypted, and its keys move.** A device's password used to
  cross the broker in the clear to any service that listened for it. `dcctl upgrade` creates a
  Secret named `dci-<instance>-callout` that holds the callout's signing seed and a new encryption
  seed and is mounted into `device-management` only; the instance configuration keeps just their
  public halves. The first upgrade that does this applies the broker in two steps so devices keep
  connecting: the services roll first onto a responder that reads both encrypted and unencrypted
  requests, and only then is the broker applied again with encryption on. That second step is one
  more rolling restart of the NATS servers. Devices connected through the server that restarts
  reconnect on their own, within seconds under `--ha`; without `--ha` the broker is unavailable for
  the usual minute. If the second step fails, devices keep connecting, and running
  `dcctl upgrade` again finishes it.
- **Everyone signs in once more, and the sign-in state moves to an account of its own.** Refresh
  tokens, OAuth authorization codes, single sign-on records and the sign-in throttle used to live in
  the NATS account every service shares, where any service login could read them or copy them into a
  stream of its own. `dcctl upgrade` creates a new login and a Secret named `dci-<instance>-identity`
  that holds its password and is mounted into `user-management` only. The broker gets a separate
  `IDENTITY` account that only that login can enter and that shares nothing with the other account.
  The old buckets are **deleted, not copied**: their tokens were readable by every service, so
  carrying them over would keep the exposure alive. Every session from before the upgrade ends, so
  people sign in again in the console and dashboards, and MCP and OAuth clients authorize again.
  Access tokens already issued keep working until they expire, 15 minutes by default. While
  `user-management` rolls, a sign-in or token refresh that reaches a pod still on the previous release
  can fail with an error for a few seconds; it succeeds when retried. Nothing is lost
  silently: `user-management` logs the buckets it deleted the first time it starts, and `dcctl
  upgrade` says so when it finishes. The broker restarts once, one server at a time under `--ha`, to
  add the account. `user-management` refuses to start if the instance says the account exists and its
  password is missing, rather than fall back to the shared account.
- **Every area gets a broker login of its own.** Until now every service signed in to the message
  broker with one shared login, so any pod could present, and any leak of its configuration exposed,
  the credential of all of them. `dcctl upgrade` creates one login per area that uses the broker, and
  one for broker administration, and stores each in a Secret named `dci-<instance>-nats-<area>` that
  is mounted into that area's pod only. `mcp` and `ai-inference` never open a broker connection and
  mount none. Each login is held to what its area uses, and no more: the streams it writes, the
  consumers it reads under (named exactly, so one area cannot rewrite, take messages from or
  acknowledge another's), the key-value buckets it opens, and its own reply
  subjects. A login can no longer publish into another area's streams, write another area's cache, or
  subscribe to another area's replies. The grants are generated from the same declarations the
  services are checked against, so they cannot fall behind what a service does. The shared login stays in place beside the new ones through this
  upgrade, with the permissions it always had, so services that have not restarted yet keep connecting;
  until the next upgrade it is still in the configuration every pod reads, so a pod can still present it, and
  this is not yet a boundary against a compromised service. Run `dcctl upgrade` once more to retire it. When every
  area already runs its own login and the previous upgrade finished, that upgrade removes the shared login from the broker and from the
  instance configuration, with one more broker restart, and its closing summary says which phase ran
  (or why it did not retire the login). A new instance never has a shared login. Once it is retired,
  rolling back to an earlier release is not supported: run the newer `dcctl upgrade` again instead. Two grants are wider by design: event-sources keeps update rights on the broker's MQTT
  stores, and user-management can purge a deleted tenant's data across every stream, except for the
  consumers event-sources keeps its acknowledged-message durability on. An installation that runs the chart without dcctl and
  brings its own broker can print the same grants with `dcctl broker permissions`. The broker
  restarts once, one server at a time under `--ha`, to add the users. An area refuses to
  start if the instance says the logins exist and its own is missing, rather than fall back to the
  shared one. Existing logins are never replaced: an upgrade creates only the ones that are absent.
- **Streams and buckets are now created by one administration login, before the services roll.** A
  stream's configuration decides what is stored under which subject, so letting every service create
  and change the streams it uses meant any one of them could reshape a stream to capture or forge
  messages. The chart now runs a job, `<instance>-broker-admin`, before every install and upgrade
  (a Helm pre-install and pre-upgrade hook). It signs in with the broker administration login, which
  no Deployment mounts, and does what every service used to do for itself at start: it creates what is
  missing, puts back the subjects, duplicate window, discard policy, size ceilings and replica count of
  what exists, and, for the one capture stream an older release created with a different retention,
  waits for its reader to drain it before recreating it as declared. That wait can take a few minutes.
  A service no longer creates or changes anything: it checks that every stream and bucket it opens
  matches what its release declares, and refuses to start, naming the stream and what differs, when one
  is missing or wrong. If the job fails, the upgrade fails before any service rolls, and `dcctl upgrade`
  prints the job's log; the services of the previous release keep running. `dcctl broker reconcile
  <instance>` runs the same job again, which is also how to recover a stream or bucket deleted by hand.
  Area logins no longer hold the right to create, update or delete a stream or bucket, with two
  exceptions that stay by design: event-sources keeps its rights on the broker's own MQTT stores, and
  user-management keeps the right to delete the old copies of the sign-in buckets. event-processing's
  replays of the resolved-events stream (its catch-up after a restart, the live-gap fill and the rule
  preview) now read the stream by sequence, which creates no consumer on the broker, so no area holds a
  right over another area's consumers on a message stream; the administration job turns on the
  stream's direct reads for this. Two behaviours change with it. The cache buckets' lifetime is now fixed at five minutes, because the job creates them
  and no longer asks each service; the device-management cache time-to-live settings bound only how long a
  replica holds an entry in its own memory. And a replica count raised in the instance configuration takes
  effect when the job runs, at the next upgrade, rather than at every pod restart. A pod of the previous
  release that restarts after the broker was updated but before the job has run is refused when it tries
  to reconfigure a stream; roll forward and it starts. An installation that runs the chart without dcctl
  needs the namespace and the administration login's Secret in place before the first install, because
  the job runs before the chart creates anything; it can switch the job off with `brokerAdmin.enabled=false` and
  create the streams and buckets itself with the `dc-broker-admin` image.
- **The event store restarts when its pods change**, for example for a new image or placement.
  A settings-only change reloads without a restart. Under `--ha` the standbys restart first, then
  the primary role switches to one of them. Without `--ha` the one instance restarts and
  event-management cannot store events until it is back. Those events wait in the ingest stream
  and are retried, and one is given up on only after about four minutes of failed retries.
- **The two can restart at the same time.** They are applied together, and the upgrade waits for
  both to report healthy before it moves the services.

A release that changes the event store's image now reaches existing instances through this
step: their instances restart onto it. The release notes for such a release say what else it
needs.

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
To keep serving through a rollout, run `replicas: 2` (or more) for each area that can serve from more
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

### The event wire format {#event-wire-format}

Every event a device sends crosses the broker twice as a message on a retained stream: once as
received, and once resolved against its device. `infrastructure.messaging.wireFormat` in the
instance configuration sets how those messages are encoded:

| Value | Encoding |
| --- | --- |
| `compact` | **The default from `v0.24.0`.** Each time as a count of nanoseconds and each device credential's seal in binary. About a third smaller per message, which means less broker disk, network and retention used per event. |
| `legacy` | Each time as text and each seal in its text form, byte for byte what `v0.22.0` and earlier wrote. `v0.23.0` writes it by default. |

Anything else, including `Compact`, is refused: the service does not start, and its log names
the key. It is part of the mounted configuration, like the [log level](./observability.md#the-log-level),
and is set the same way.

- **Every release from `v0.23.0` on reads both.** Messages written in the `legacy` form before an
  upgrade stay on the retained streams and are read as before, including by the replay preview of
  a detection rule over a window that started before the upgrade.
- **Upgrade through `v0.23.0`, not straight from `v0.22.0` or earlier.** `v0.22.0` and earlier
  read only `legacy`: a service on one of them drops a `compact` message, or sets it aside as
  failed, so a rolling upgrade from one of them straight to a release that writes `compact` loses
  the events written while old and new pods run side by side. Upgrade every instance to `v0.23.0`
  first, wait until every pod runs it, and only then upgrade to the later release. On a chart
  install you can instead set `legacy` for the upgrade and remove it once every pod has rolled.
  `dcctl` has no option to set it.
- **Rolling back to `v0.23.0`** needs nothing for the wire format: it reads the `compact` messages
  a later release wrote.
- **Rolling back to `v0.22.0` or earlier** needs `legacy` set first, and the consumers to have
  drained every `compact` message from the streams.

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
before upgrading rather than assuming from the version number. `v0.19.0` is the latest, for the
size of the data rather than its shape: an instance whose event store holds more than 4,000,000
rows not yet compressed, or a table of more than 500 chunks whose oldest events you cannot remove
([Check the row count](#v0190-row-count)).
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

### The release after v0.25.2 — devices can register themselves {#self-registration-upgrade}

This section is written ahead of the release that ships [device self-registration](../guides/device-self-registration.md), so the version in its heading is left for that release to name. It applies to an upgrade from `v0.25.2` or earlier. Device-management gains a database migration that runs on its own, and the API changes below are breaking for anything that scripts provisioning profiles.

Until now a provisioning profile could be created and edited, but nothing read it: no device could register through one. So nothing that worked yesterday stops working because a fleet used it. What changes is how profiles are made and what they need.

#### Provisioning profiles {#self-registration-profiles}

- **The server generates the provision secret.** `createProvisioningProfile` no longer takes `provisionSecret`. It returns `{ profile, issuedSecret }` where it used to return the profile, and `issuedSecret` is shown **once**. A script that selects `token` from the result must now select `profile { token }`. `updateProvisioningProfile` no longer takes `provisionSecret` either. `rotateProvisioningProfileSecret(token)` is the only way to replace a secret. Both are breaking: regenerate any client built against the old schema and check your scripts. The new operations are listed under [Provisioning profiles](../reference/graphql-api.md#provisioning-profiles).
- **Existing profiles are refused until you rotate them.** A secret an operator chose is not accepted by the new endpoint, because only a secret the server generated is. Every profile that exists at the upgrade answers `401 PROVISIONING_FAILED` to a registration, and the console marks it **Rotate required** (`secretIssued` is `false` in GraphQL). Rotate it, copy the new secret, and give it to the fleet. Nothing about the profile's other settings changes.
- **The provision key is unique per tenant, not across the instance.** Two tenants can now use the same key. The database constraint that made it instance-wide is replaced by one per tenant.
- **A profile can have a cap.** `maxDevices` is new and optional. A profile that has it unset stays uncapped.

#### Devices {#self-registration-devices}

- **A profile adopts an existing device only if the device was earmarked for it.** Under both strategies, and including `ALLOW_NEW`, a device that exists but was not earmarked for the profile is refused with `409 DEVICE_NOT_EARMARKED`. Earmark with `provisioningProfileToken` on `createDevice` and `importDevices`, or the console's **Expected provisioning profile**. Existing devices have no earmark, so if you plan to let a profile take over devices you already created, earmark them first (an `importDevices` re-run with the column set does it).
- **A profile cannot be deleted while it has issued credentials**, and a device type cannot be deleted while a profile uses it. Both answer `IN_USE`. Reset or delete the devices first.

#### Operating it {#self-registration-operating}

- **The endpoint needs the API ingress.** `POST /api/device-management/provisioning/{tenant}/register` is reached through the same `/api/<area>` route as the GraphQL API. An instance with `ingress.enabled: false` has no public path to it.
- **`dcctl bootstrap` and `dcctl upgrade` now render `infrastructure.http.trustedProxies`** from the cluster's pod range, so the per-source limit sees the device and not the ingress controller. The instance configuration changes, so the services restart with the rest of the rolling upgrade. Check it if you run your own ingress controller or sit behind a load balancer: [Client addresses and trusted proxies](./bootstrap.md#trusted-proxies). Without it every device shares one request allowance per ingress pod, which a stranger can use up.
- **New configuration**, all optional, under device-management's `provisioning` key: `readDeadline`, `maxInFlight`, `maxConcurrentRegistrations`, `sourceBurst` and `sourceEvery`. The defaults are in [the limits](../reference/device-protocol.md#self-registration-limits).
- **New metrics and three alerts** (`ProvisioningAuthFailuresSustained`, `ProvisioningRateLimited`, `ProvisioningMisconfigured`) arrive with the chart. They are quiet on an instance that does not use the endpoint. See [Device self-registration](./observability.md#self-registration-alerts).
- **A new governance dimension** caps the registrations a tenant can make: a burst of 500 and 20 a second by default, set per tier or per tenant. See [Governance & Quotas](../concepts/governance.md).

### v0.25.2 — the service-token mint leaves the public port {#v0252-upgrade}

`v0.25.2` is a security patch for `v0.25.1`. There is no migration, and no API, topic or permission
changes. The instance configuration gains one key, `infrastructure.userManagement.internalPort`, which the
chart sets to `8082`; leave it there, because the chart's Service, pod and network policy use that fixed
port. Everything
the [v0.25.1](#v0251-upgrade) and [v0.25.0](#v0250-upgrade) sections describe is still what you are
running, including their known limitations.

Upgrade the cluster, then each instance. Give `dcctl install` the settings the cluster was installed with,
such as `--ha` and `--database-node-selector`, as well as the new `--version`:

```bash
dcctl install local --version v0.25.2
dcctl upgrade local devicechain --version v0.25.2 --dry-run
dcctl upgrade local devicechain --version v0.25.2
```

**Coming from `v0.25.0` or `v0.24.0`,** follow the [v0.25.1 notes](#v0251-upgrade) with
`--version v0.25.2`: they apply unchanged, and so does what they say about the v0.25.0 upgrades.

#### The internal endpoints are off the public port {#v0252-internal-endpoints}

`user-management` served two endpoints meant only for the platform's own services on its public port:
`/auth/service-token`, which exchanges the shared service secret for a short-lived service token, and
`/auth/jwks`, the key set every service validates tokens with. The only thing keeping them off the
internet was an nginx configuration snippet on the chart's API ingress. **An install that brought its own
ingress controller therefore had both reachable from outside,** protected by the shared service secret,
which is compared in constant time.

From this release `user-management` serves both on a separate internal port, `8082`, that the chart
publishes only on the cluster-internal Service. No ingress, route or load balancer refers to it, and the
public port answers 404 for both paths whatever sits in front of it, so **a bring-your-own controller
needs no edge rule to hide them.** Every service now fetches the key set and mints its token on the
internal port. See [Endpoints that never leave the cluster](../concepts/security-model.md#internal-endpoints).

- **The chart no longer sets an nginx configuration snippet.**
- **The ingress controller `dcctl install` sets up is unchanged in this release** and still accepts
  snippets, because instances not yet upgraded rely on them. A later release turns snippets off, behind a
  `dcctl install` check that refuses while any Ingress of the controller's class still carries a
  `*-snippet` annotation.
- **External token verifiers are unaffected:** they use the `jwks_uri` in the authorization-server
  metadata, `/oauth/jwks`, which stays public.

#### What to expect {#v0252-during}

- **`user-management` and its peers must roll together.** A pod from an older release asks the public
  port for the key set and the mint, which now answer 404, and it does not retry its way out: its mint
  calls fail until that pod is replaced by one from this release. `dcctl upgrade` replaces every service's
  pods, so this lasts as long as the roll; do not hold an area back on an older image.
- **An older pod that restarts after `dcctl upgrade` has written the new configuration fails to start.**
  Older binaries decode the instance configuration strictly and reject the new `internalPort` key, so such
  a pod crash-loops until it is replaced by one from this release.
- **If you run your own network policy engine,** allow pods in the instance namespace to reach port `8082`
  on `user-management`, and nothing more. The chart's own network policy for that port is off by default,
  and `dcctl` installs run with it off.
- **The Grafana boards the chart ships** each open with a table of the instance's firing alerts and link to
  the instance's other boards, and a third board, *Instance overview*, holds that table only for now.

### v0.25.1 — the database and backup-store images, rebuilt for current advisories {#v0251-upgrade}

`v0.25.1` is a patch for `v0.25.0`. There is no migration, and no API, topic, permission, chart value or
configuration key changes. It moves the three third-party images the cluster and each instance run their
databases and backup store on to builds that no longer carry the advisories listed below, and it corrects
what two services report to Prometheus and the Grafana boards the chart ships for them (see
[The metrics and the boards](#v0251-metrics)). Everything the [v0.25.0 section](#v0250-upgrade) describes is still what you are
running.

The [v0.25.0 known limitations](#v0250-upgrade) still apply, including this one: with `mqttFailClosed` off, the default, acknowledged QoS 1 messages can also be lost under sustained load with no broker restart, when `event-sources` falls behind and the capture stream fills. `devicechain_<area>_jetstream_consumer_unread_skipped_total` counts them and `JetStreamDurableLostUnread` alerts.

Upgrade the cluster, then each instance. Give `dcctl install` the settings the cluster was installed with,
such as `--ha` and `--database-node-selector`, as well as the new `--version`:

```bash
dcctl install local --version v0.25.1
dcctl upgrade local devicechain --version v0.25.1 --dry-run
dcctl upgrade local devicechain --version v0.25.1
```

**Coming from `v0.24.0`,** follow the [v0.25.0 notes](#v0250-upgrade) with `--version v0.25.1`: they apply
unchanged, including running `dcctl upgrade` twice. **Coming from `v0.25.0`,** one `dcctl upgrade` is
enough if both of the v0.25.0 upgrades ran. If only the first did, the shared broker login is still in
place, and this upgrade retires it, with the one broker restart that takes; its `--dry-run` and its closing
summary say so (see [The broker's logins, in two upgrades](#v0250-broker-logins)).

#### What moves {#v0251-images}

| Image | Runs | From | To | Advisories it no longer carries |
| --- | --- | --- | --- | --- |
| `ghcr.io/devicechain-io/postgresql-timescaledb` | each instance's event store | `17.10-ts2.28.3-r1` | `17.11-ts2.28.3-r1` | Perl `5.36.0-7+deb12u3`: CVE-2026-13221, CVE-2026-42496, CVE-2026-8376 (critical), CVE-2026-42497, CVE-2026-48962, CVE-2026-57432, CVE-2026-57433 (high). PCRE2 `10.42-1`: CVE-2026-103111, CVE-2026-86145, CVE-2026-89157, CVE-2026-89161 (high) |
| `ghcr.io/cloudnative-pg/postgresql` | the cluster's shared relational database (`dc-rdb`, in `dc-system`) | `17.10-standard-bookworm` | `17.11-standard-bookworm` | the same Perl and PCRE2 advisories |
| `cgr.dev/chainguard/minio` | the cluster's in-cluster backup store, where backups go to it | digest `sha256:32c504be…` | digest `sha256:73297936…` | CVE-2026-111497 (Brotli decoder, high), CVE-2026-111943 (Go `crypto/x509`, in the bundled `mc` client, high) |

The event store image is DeviceChain's own build on the CloudNativePG PostgreSQL base, so it moves with
that base. PostgreSQL moves from 17.10 to 17.11, a minor release: the data files are kept as they are, and
nothing is dumped or restored. TimescaleDB stays at 2.28.3. Scanned with Trivy for high and critical
advisories that have a fix, each new image reports none.

#### The metrics and the boards {#v0251-metrics}

- **`event-processing` zeroes its leader-only gauges when a replica stops leading.** A replica that had
  lost the detection lease kept exporting the backlog, lag and state sizes of its last term. The
  board and the `DetectConsumerBacklogHigh`, `DetectWatermarkLagHigh` and `DetectTenantOverStateBudget`
  alerts take the highest reading across replicas, so that frozen reading could show a backlog no replica
  had and keep an alert from clearing. A standby now reports zero for them. The count of loaded rules is
  not affected, since every replica loads the rules.
- **`command-delivery` exports its command-batch counters at zero from start**, so its board reads zero
  rather than "No data" before the first batch, and an `increase()` over them sees the first one.
- **The two boards the chart ships are corrected:** their series match what the services export, rare
  events are shown as counts, panels that mixed units are split, and panels for the retained-sample
  budget are added.

#### What to expect {#v0251-during}

- **`dcctl install` restarts the relational database's instances once**, onto the new image: under `--ha`
  the standbys first, then a switchover to one of them, so writes pause for the switchover only. A
  single-instance relational database is unavailable until its one instance has restarted, and writes made
  meanwhile are retried. `dcctl install` waits until every instance runs the new image.
- **`dcctl install` restarts the backup store's pod once**, when the cluster keeps its backups in the
  cluster. Stored backups and write-ahead log are kept. While the pod restarts, archiving pauses and the
  databases hold write-ahead log locally. A cluster that sends its backups to your own object store with
  `--backup-credentials-file` runs no backup store, and nothing changes there.
- **`dcctl upgrade` restarts the instance's event store once**, onto the new image, in the infrastructure
  step before the services move: under `--ha` the standbys first, then a switchover; without `--ha` the
  one instance restarts, and event-management cannot store events until it is back. Those events wait in
  the ingest stream and are retried; one is given up on only after about four minutes of failed retries. The upgrade waits until every instance of the store runs the new image before it moves the
  services. The message broker's settings do not change, so its servers do not restart.
- **The services roll onto the `v0.25.1` images** one pod at a time, as in any upgrade. Their code is the
  same as `v0.25.0`'s apart from the two metric changes above.
- **If you mirror images,** mirror the three new references above before you upgrade, and the `v0.25.1`
  DeviceChain images as usual.
- **If you set `timescale_image` yourself** in the `terraform.tfvars` beside an instance's infrastructure
  state, the upgrade keeps your value, and the event store stays on the image it names. Set it to
  `ghcr.io/devicechain-io/postgresql-timescaledb:17.11-ts2.28.3-r1`, or remove the line, to move.
- **Changing a database setting in the same run is refused** on a database of more than one instance: the
  database operator does not accept a new image and new settings at once. Keep `dcctl install` to the
  settings the cluster already has (a different `--max-connections`, for example, is such a change), and
  change the setting in a run of its own.

### v0.25.0 — each area signs in to the broker as itself, and releases are signed {#v0250-upgrade}

`v0.25.0` is an in-place upgrade from `v0.24.0`: `dcctl install` for the cluster, then `dcctl upgrade` for
each instance, as described [for v0.17.0](#v0170-upgrade). Give `dcctl install` the settings the cluster
was installed with, such as `--ha` and `--database-node-selector`, as well as the new `--version`.
**Unlike most releases, `dcctl upgrade` is run twice.** The first run gives every area a message-broker
login of its own and keeps the shared login beside them; the second retires the shared login (see
[The broker's logins, in two upgrades](#v0250-broker-logins)). **It cannot be reached in place from
`v0.22.0` or earlier:** upgrade through `v0.23.0` and `v0.24.0` first (see
[Before you upgrade](#v0250-before)). Its three migrations add five tables, a column and their indexes
and rewrite no existing row (see [During the upgrade](#v0250-during)). **Roll forward from it, not back**
(see [Going back](#v0250-going-back)).

The commands, with the `v` in the version and a rehearsal first:

```bash
dcctl install local --version v0.25.0
dcctl upgrade local devicechain --version v0.25.0 --dry-run
dcctl upgrade local devicechain --version v0.25.0
# when that has finished and every area is Ready, run it again to retire the shared broker login:
dcctl upgrade local devicechain --version v0.25.0
```

`--version` takes the tag with its `v`. On a cluster other than the default local one, pass `--kube-context`
to `dcctl install` and `dcctl upgrade`, and pass `--registry` when the images come from a registry other than
the default. The `--dry-run` of each run says which of the two it would perform. If the instance was
bootstrapped without root-key escrow, `dcctl upgrade` prints an escrow warning; that is expected and
unrelated to this upgrade. A value of `--mqtt-body-credential` set for `v0.24.0` is kept by an upgrade that
omits the flag; it remains transitional, and `dcctl` documents its removal as planned for `v0.26.0` (see
[`v0.24.0`](#v0240-mqtt-body-credential)).

This release does two things about trust. Inside the cluster, **no service shares a broker credential with
another any more**: each area signs in to the message broker as itself, held to what it uses, and one
administration login alone creates and changes streams and buckets. Outside it, **every image and the
chart are signed**, so you can check that what you run was built by DeviceChain's release workflow. Around
them: a device's access token is stored as a keyed digest, the edge agent is a gateway with an identity of
its own, tenants can create service accounts for their integrations, and over-the-air updates can be rolled
out to a device group. What changes for somebody:

- **Run `dcctl upgrade` twice.** The first upgrade keeps the shared `dc_service` login; the second removes
  it. See [The broker's logins, in two upgrades](#v0250-broker-logins).
- **A device's access token can no longer be read back.** Anything that read `credentialId` of an
  `ACCESS_TOKEN` credential and used it as the token now gets a fingerprint. See
  [Device access tokens are stored as digests](#v0250-access-tokens).
- **An edge agent must be moved to its gateway identity** before it is upgraded; the old direct-device
  uplink is gone. See [The edge agent is a gateway](#v0250-edge-gateway).
- **A C# SDK connection that the broker refuses now raises `MqttConnectRefusedException`** instead of
  being retried as if the network were down. See [The C# SDK](#v0250-csharp-sdk).
- **An installation that runs the chart without `dcctl`** needs the instance namespace and the broker
  administrator's Secret before its first install, or `brokerAdmin.enabled=false`. See
  [The broker's logins, in two upgrades](#v0250-broker-logins).
- **The broker restarts once in each of the two upgrades,** one server at a time under `--ha`. See
  [During the upgrade](#v0250-during).
- **There is no going back to `v0.24.0`.** See [Going back](#v0250-going-back).
- **Upgrade through `v0.23.0` and `v0.24.0`** when coming from `v0.22.0` or earlier.
- **Three migrations:** two tables in `update-management`, three in `user-management`, and one column and
  index in `device-management`. See [During the upgrade](#v0250-during).
- **Releases are signed.** Nothing needs doing; see [The first signed release](#v0250-signing).
- **Tuning you set by hand is reverted by an upgrade unless you give it to `--values`.** That was true
  before; this release adds the way to keep it. See [Operator tuning is carried](#upgrade-tuning).

#### Before you upgrade {#v0250-before}

1. **Coming from `v0.22.0` or earlier: stop.** Upgrade to `v0.23.0`, then to `v0.24.0`, waiting each time
   until every pod runs the new release, and only then to `v0.25.0`. The compact wire format that
   `v0.24.0` made the default is unreadable to `v0.22.0` and earlier (see
   [The event wire format](#event-wire-format)), and skipping a minor is not a tested path (see
   [Supported upgrade paths](./versioning-policy.md#upgrade-paths)). Read the [`v0.24.0` notes](#v0240-upgrade)
   too. An instance built by `v0.16.0` or earlier still cannot be upgraded; see
   [Instances built by v0.16.0 and earlier](#pre-declaration-recreate).
2. **Find what reads a device's access token back.** A script, a provisioning tool or a spreadsheet that
   fetched the token from `deviceCredentials` or `deviceCredentialsByToken` will receive a fingerprint
   beginning `dctk1$` instead. The tokens in the devices keep working. Decide where each token you did
   not record will come from: set a new token on the credential (see
   [Device access tokens are stored as digests](#v0250-access-tokens)).
3. **If you run the edge agent, plan its move to a gateway before you upgrade it.** Create a gateway
   device with an `MQTT_BASIC` credential for each agent, and assign the devices the site fronts to it.
   Do not restart an agent on this version until the platform, including its broker, has been upgraded.
   See [The edge agent is a gateway](#v0250-edge-gateway).
4. **If you run the chart without `dcctl`, create the namespace and the administrator's Secret first,** or
   set `brokerAdmin.enabled=false` and create the streams and buckets yourself with the `dc-broker-admin`
   image. See [What an upgrade applies to the infrastructure](#upgrade-infrastructure). An installation
   that brings its own broker can print the grants every login needs with `dcctl broker permissions`.
5. **If you mirror images into your own registry, add `dc-broker-admin`.** The pre-upgrade job runs it, and
   the upgrade fails before any service rolls if it cannot be pulled. It is also in the list of
   [signed images](./verifying-releases.md#what).
6. **If you tuned the instance by hand** (replica counts, resources, an area's own configuration), put it in
   a values file and pass it as `--values` on the first upgrade. An upgrade rebuilds an instance's settings
   from its declaration, so anything set another way is reverted. See [Tuning an Instance](./tuning-an-instance.md).
7. **If a C# or Unity device program connects to the platform's MQTT broker,** read
   [The C# SDK](#v0250-csharp-sdk) before you rebuild it.

#### During the upgrade {#v0250-during}

- **Three migrations run, one per first start of the service that owns it.** None rewrites a row, and
  each can be run again if it is interrupted.
  - `device-management` adds the nullable column `devices.gateway_id`, which records the gateway a device
    sits behind (it is empty for every existing device), and a partial index on
    `(tenant_id, gateway_id)`. The index is built with a plain `CREATE INDEX`, which holds a lock against
    writes to `devices` while it builds; it covers only devices that have a gateway, so it is empty on
    the first run.
  - `update-management` creates `update_rollouts` and `update_rollout_targets`, which hold group
    rollouts. Both start empty.
  - `user-management` creates `iam_service_accounts`, `iam_service_account_roles` and
    `iam_service_account_secrets`, which hold service accounts and the digests of their secrets. All
    three start empty.
- **`device-management` converts every stored access token to its digest on start.** This is not a
  migration: it runs after the migration chain on every start, covers every tenant and soft-deleted
  rows, writes each row only if it still holds the value it read, and finds nothing to do the second
  time. A device keeps the token it has; the same string authenticates before and after.
- **While `device-management` rolls, devices that authenticate with an access token can fail to connect.**
  Once a pod of this release has converted the rows, a pod of `v0.24.0` still in the roll looks a
  presented token up by its plaintext and finds nothing, so for the length of the overlap it refuses
  access-token connects and per-event checks. Devices retry and reach a pod of this release. A token
  that a pod of the previous release writes during the overlap is converted at the next start of a pod of
  this release. Devices that authenticate with `MQTT_BASIC` are not affected.
- **The broker restarts once in the first upgrade, and once in the second.** The first adds the logins
  to the broker; the second removes the shared one. Under `--ha` each restarts one server at a time, each
  Ready before the next stops; without `--ha` the single server restarts and the broker is unavailable
  meanwhile, usually about a minute. Devices and services reconnect on their own. **Do not restart a
  broker server by hand while it rolls.** See [What an upgrade applies to the infrastructure](#upgrade-infrastructure).
- **A job runs between the broker and the services.** The chart runs `<instance>-broker-admin` as a Helm
  pre-install and pre-upgrade hook. It creates or repairs every stream and key-value bucket, including
  turning on direct reads for the `resolved-events` stream that event-processing's replays now use. If it
  fails, the upgrade fails before any service rolls, `dcctl` prints the job's log, and the services of
  `v0.24.0` keep running. The job is allowed 540 seconds by default (`brokerAdmin.activeDeadlineSeconds`)
  and one retry (`brokerAdmin.backoffLimit`).
- **A pod of the previous release that restarts while the upgrade runs does not start.** `dcctl` writes
  the new instance configuration just before the job, and it carries a key (`areaLogins`) that `v0.24.0`
  does not declare, so a `v0.24.0` pod that restarts after that refuses the configuration. Pods that keep
  running are not affected. Let the upgrade finish rather than restarting pods by hand; the pods that
  replace them run this release.
- **Each area waits for its own login.** After the first upgrade an area refuses to start if the instance
  says per-area logins exist and its own Secret is missing, rather than fall back to the shared login.

#### Going back {#v0250-going-back}

**Roll forward. Do not go back to `v0.24.0`, with `helm rollback` or any other way.** If the upgrade fails
partway, fix the cause and run `dcctl upgrade` again with the same `--version`. Going back leaves an
instance that does not work:

- **Every device on an access token is refused.** The token is now stored as a digest, and `v0.24.0` looks
  a presented token up by its plaintext. A rollback does not restore the plaintext, and the plaintext is
  not kept anywhere.
- **The shared broker login may be gone.** After the second upgrade the broker no longer has `dc_service`
  and the instance configuration no longer names it, so a service of `v0.24.0` has nothing to connect
  with. `dcctl` says so when it retires the login: rolling back across this release is not supported,
  re-run the newer `dcctl upgrade` instead.
- **The configuration is written for this release.** `dcctl` writes the instance configuration to the Secret
  `dci-<instance>-config`, outside the chart's release, so `helm rollback` restores the previous
  release's workloads and leaves the configuration as this release wrote it. It carries the key
  `areaLogins`, which `v0.24.0` does not declare; services refuse an unknown configuration key at startup.

The new tables and the new column would not be in the way: `v0.24.0` reads the same database and leaves
them unused.

#### After the upgrade {#v0250-after}

- **Run `dcctl upgrade` a second time,** once the first has ended with every area Ready. See
  [The broker's logins, in two upgrades](#v0250-broker-logins).
- **Check the signatures of what you installed,** if you want to. See
  [The first signed release](#v0250-signing).
- **Keep `metrics.enabled` and `metrics.alerts` on** (both are by default) to install the three new
  tenant-deletion alerts: `TenantPurgeStoreFailing`, `TenantPurgeStoreHolding` and
  `TenantPurgeCoordinatorFailing`. See [Tenant deletion](./tenant-deletion.md#blocked-store).
- **Move each edge agent** when the platform is done. See [The edge agent is a gateway](#v0250-edge-gateway).
- **Set a new token on any access-token credential you did not record.** See
  [Device access tokens are stored as digests](#v0250-access-tokens).

##### The broker's logins, in two upgrades {#v0250-broker-logins}

Until now every service signed in to the message broker with one shared login, so any pod could present,
and any leak of its configuration exposed, the credential of all of them. The mechanism is described under
[What an upgrade applies to the infrastructure](#upgrade-infrastructure), where the per-area logins, the
administration job and `dcctl broker reconcile` are covered; what it means for the upgrade:

- **The first upgrade is phase 1 of 2.** It creates a login for every area that uses the broker (`mcp` and
  `ai-inference` never open a broker connection and get none) and one for broker administration, each in a
  Secret named `dci-<instance>-nats-<area>` that only that area mounts. The shared `dc_service` login stays,
  with the permissions it always had, so a pod that has not rolled yet keeps its connection. Until the
  second upgrade it is still in the configuration every pod reads, so a pod can still present it. The
  closing summary says "This was phase 1 of 2: run `dcctl upgrade` once more to retire it."
- **The second upgrade retires it.** It does so only when the instance it starts from already runs every
  area on its own login. It checks that:
  - the running configuration already tells the areas to use their own logins;
  - every login's Secret existed before this run, so none is being created now;
  - the running broker already holds a login for every area and for the administrator;
  - the running release carries the broker administration job;
  - the last run over the instance ended Ready; and
  - this run applies the broker, so `--skip-infrastructure` never retires the login.

  When all hold, the broker and the instance configuration stop carrying the shared login, with one more
  broker restart, and the services roll once more onto the changed configuration. The closing summary
  says "The shared message broker login is retired." When one does not, the login is kept and the summary says "The shared message broker login was NOT retired"
  and names the cause; resolve it and run `dcctl upgrade` again.
- **Do not pass `--skip-infrastructure` to either run.** It applies nothing to the broker, so it neither
  adds the logins nor retires the shared one.
- **A new instance never has a shared login.** `dcctl bootstrap` creates only the per-area logins.
- **Rolling back across the second upgrade is unsupported.** See [Going back](#v0250-going-back).
- **Services check, and the administrator changes.** A service verifies every stream and bucket it opens
  and stops with an error naming the stream and what differs when one is missing or wrong. Run
  `dcctl broker reconcile <instance>` to put it back, which also recovers a stream or bucket deleted by
  hand.
- **Two behaviours change with it.** The cache buckets' lifetime is now fixed at five minutes, because
  the job creates them and no longer asks each service; the `device-management` cache time-to-live settings
  now bound only how long a replica holds an entry in its own memory. And a replica count raised in the
  instance configuration takes effect when the job runs, at the next upgrade, rather than at every pod
  restart.
- **Chart-only installs** need the namespace and the administration Secret in place before the first
  install, because the job runs before the chart creates anything. The Secret is named
  `dci-<instance>-nats-broker-admin` and holds `user` and `password`. `dcctl broker permissions` prints
  the grants for a broker you run yourself.
- **Restoring from a backup gets new logins.** See [Disaster recovery](./disaster-recovery.md).

See the [security model](../concepts/security-model.md) for what the logins do and do not protect.

##### Device access tokens are stored as digests {#v0250-access-tokens}

An `ACCESS_TOKEN` credential's token is its credential id, and it used to be stored and returned as sent, so
a database or backup leak yielded a working credential for every token device. It is now stored as
a keyed digest of the same kind an `MQTT_BASIC` password already is: an HMAC-SHA-256 over the tenant and
the token, under a key derived from the instance root key. The stored form is `dctk1$<key id>$<digest>`.

- **Existing devices keep their tokens.** No device needs reprovisioning.
- **The token is shown once,** in the response to the call that sets it (create, an update that rotates it,
  a device replacement that mints one) and in the console's banner, and never again. A read returns the
  fingerprint, which identifies the credential and cannot be presented as the token.
- **To get a usable token for a credential whose token you did not record, set a new one:** **Set new
  token** in the console, or `updateDeviceCredential` with only `credentialId`. The device's current token
  stops opening connections as soon as you do. See
  [Show-once passwords and tokens, and rotation](../guides/device-credentials.md#show-once).
- **`deviceCredentials(criteria: {credentialId})` accepts the token or the fingerprint.**
- **Changing a credential's type to or from `ACCESS_TOKEN` must name `credentialId` in the same request,**
  because the stored value means a digest under one type and a username under the other. A request that
  does not is refused as `INVALID_VALUE`. A token over 256 bytes is refused as `LIMIT_EXCEEDED`.
- **The console's connect snippets** show `<access-token>` for an access-token credential, as they already
  showed a placeholder for a password.
- **A different root key makes every digest foreign.** As for password digests, an instance restored next
  to the wrong root key logs an error naming how many stored digests its key did not make.

##### The edge agent is a gateway {#v0250-edge-gateway}

The edge agent signs in to the platform as a device of its own, a **gateway**, and publishes each local
device's events in its own namespace. The platform accepts an event only for a device assigned to that
gateway. This is the only uplink mode: the old direct-device mode, in which the agent forwarded to the
cloud broker verbatim, is removed. See [Gateway identity](./edge-services.md#gateway-identity) and
[the gateway namespace](../reference/device-protocol.md#gateway-namespace).

- **An agent's configuration must change.** `agentId` is no longer a key, and a leftover one stops the
  agent at start, with an error naming `uplink.gatewayToken` as its replacement. `uplink.tenant`,
  `uplink.gatewayToken`, `uplink.username` (in the form `{tenant}:{credentialId}`, refused at load
  otherwise) and `uplink.passwordEnv` are required.
- **Create the gateway device and assign its devices before the agent drains its backlog.** Events spooled
  under the old mode are forwarded in the new namespace, and one for a device that is not yet assigned is
  refused. The spool and its durable are narrowed to `uplink.tenant` in place; spooled events of any other
  tenant are not forwarded and stay in the spool until its ring buffer evicts them.
- **Assign devices with `assignDeviceGateway`,** or with `gatewayToken` when creating one, or from the
  device's **Gateway** tab in the console. A device has at most one gateway, a gateway cannot itself be
  behind a gateway, and a gateway that still has devices cannot be deleted. It needs `device:write`, and
  takes effect on the device's next event.
- **A refused event is not silent.** The platform records it on the failed-events stream with the reason
  `NotGatewayChild`, counts it in `resolve_not_gateway_child_total`, and sends the gateway a notice; the
  agent logs it, counts it, and keeps the event in `refused-events.jsonl` in its store directory.
- **Upgrade the platform first.** An `event-sources` of `v0.24.0` running with
  `infrastructure.nats.mqttFailClosed` on acknowledges gateway messages it cannot attribute and drops
  them, and the agent sees success. Finish upgrading every `event-sources` and `device-management` replica
  and the broker before you restart any agent on this version. An agent whose broker has not been
  upgraded cannot subscribe to its notices and does not report `uplink_connected`. See the warning under
  [Rolling upgrades](#rolling-upgrades).
- **Commands to devices behind a gateway are not routed yet.** The agent forwards events only.

##### The C# SDK {#v0250-csharp-sdk}

The SDK did not recognise a refused connection. The MQTT library reports a CONNACK that is not a success
by throwing, and the SDK treated that as an ordinary transport failure, so a session's reconnect loop
kept retrying a credential the broker had refused as if the network were down. It now raises
`MqttConnectRefusedException` for a CONNACK that judged the client (unsupported protocol version, client
identifier not valid, bad user name or password, not authorized, banned, bad authentication method), and a
session's reconnect loop stops on it and reports the session blind. A CONNACK of "server unavailable" or
"server busy", and a socket that fails mid-connect, are still retried. `MqttConnectRefusedException` gains
a constructor that takes the underlying exception, which is a public API addition; nothing is removed.

What this changes for a device program: one that depended on an indefinite retry now has to handle the
refusal, for example after its credential is revoked or replaced, or its access token is set again.

##### The first signed release {#v0250-signing}

Every service image, the operator, the edge agent, the broker administrator and the console, and the Helm
chart, are signed with cosign, keyless, by digest. Each image also carries a signed SPDX SBOM and SLSA
build provenance, and the chart carries provenance. A signature is bound to the release workflow and the
tag, so one from any other workflow, repository or branch does not verify as a release. The GitHub Release is
not published until `hack/verify-release-signatures.sh <tag>` has passed against what was pushed.

- **To check what you downloaded,** follow [Verifying releases](./verifying-releases.md); the script needs
  `cosign` 3 or later, `jq` and the GitHub CLI, and no account or key of ours.
- **Releases before `v0.25.0` carry no signature,** and verifying one fails with "no signatures found".
- **The database image is not covered.** The operand image that runs the event store is published by a
  separate workflow and is not signed in this release.
- **The `dcctl` and edge agent archives** carry build provenance as they have since
  [`v0.18.0`](#v0180-provenance).

Nothing needs doing for an upgrade. See the [security policy](https://github.com/devicechain-io/devicechain/security/policy)
for how to report a problem.

##### Service accounts {#v0250-service-accounts}

A tenant administrator can create **service accounts** under **Settings**, next to **Members**, for scripts
and integrations that should not run on a person's refresh token. An account holds exactly the roles it is
given (with no viewer baseline), optionally limited to some customers or groups, and obtains an ordinary
access token valid for five minutes with the OAuth `client_credentials` grant. A secret is shown once, stored
as a keyed digest, and expires (365 days by default, 730 at most); rotating one leaves the old one working for
an overlap you choose, 24 hours by default. A tenant can hold 200 accounts, and an account cannot
administer the tenant. See [Service accounts](../user-guide/service-accounts.md). An operator's OAuth client
cannot use this grant, and a service account's credentials authenticate nothing on the other grants.

##### Group rollouts of over-the-air updates {#v0250-rollouts}

`createUpdateRollout`, `startUpdateRollout`, `pauseUpdateRollout`, `resumeUpdateRollout` and
`abortUpdateRollout` assign one artifact to the members of a device group, never more than `maxInFlight`
devices (1 to 1,000) with an unfinished update, and **Updates → Rollouts** in the console shows them.
Starting a rollout freezes the group's members; a group of more than 10,000 devices is refused. A device
the assignment rules refuse is skipped with that rule's code and holds no slot. An optional failure
threshold pauses the rollout by itself, and resuming it then needs a higher threshold. Aborting cancels
the devices not yet started and leaves the ones in flight to finish. Rollouts need `artifact:write`
to change and `artifact:read` to view, and a member limited to part of the tenant is refused. See
[Rolling out to a group](../concepts/over-the-air-updates.md#rollouts). New metrics are
`update_rollout_dispatches_total`, `update_rollout_transitions_total` and `update_rollout_step_failures_total`.

#### What else changed {#v0250-what-changed}

##### Known limitations {#v0250-known-limitations}

- **Through the first upgrade the shared login is still readable by every pod.** That is the window
  between the two upgrades; run the second promptly.
- **Two broker grants are wider by design:** `event-sources` keeps update rights on the broker's own MQTT
  stores, and `user-management` can purge a deleted tenant's data across every stream, except for the
  consumers `event-sources` keeps its acknowledged-message durability on.
- **A gateway's commands are not routed,** a notice to a gateway is best effort, and the agent holds a
  bounded copy (8,192 events and 16 MiB) of what it forwarded to match a notice against.
- **Rollouts have no waves or canary stage,** no schedule, and no alert for a stalled rollout driver.
- **The SBOM is generated from each image's `amd64` variant.**
- **With `mqttFailClosed` off, which is the default, QoS 1 messages the broker has acknowledged can be lost
  while the MQTT capture stream changes leader, at any load,** and nothing counts those
  (`JetStreamCaptureLeaderChanged` warns that a change happened). They can also be lost with no leader change, under
  sustained load: in a load test on a default install, `event-sources` fell behind reading the capture stream, the stream
  filled to its bound and discarded its oldest unread messages, and 1,357,188 acknowledged events, about 7 to 13% of the
  MQTT traffic, were lost. `devicechain_<area>_jetstream_consumer_unread_skipped_total` counts them and `JetStreamDurableLostUnread` alerts. Why
  `event-sources` fell behind is still being investigated. A broker roll restarts every server, the
  leader among them, and this upgrade rolls the broker twice, so expect a short loss window during each
  broker roll unless fail-closed is on. In a rehearsal of this upgrade with four devices sending 20 QoS 1 messages a
  second between them, 11 of 11,262 acknowledged messages were lost, all during the broker restarts; in an
  earlier rehearsal none were. To close the window, turn on `infrastructure.nats.mqttFailClosed`. `dcctl` has
  no option for it in this release and `--values` does not accept it, so a value set by hand on an instance
  `dcctl` manages is written over by the next `dcctl upgrade`; a later release adds `--mqtt-fail-closed` to
  `dcctl bootstrap` and `dcctl upgrade`, which records the choice on the instance (see
  [fail-closed MQTT](./observability.md#mqtt-fail-closed)). The trade-off: while the broker's message store has no leader, a QoS 1
  publish is not acknowledged, so the device must send it again, and the instance keeps up with less
  traffic than with the setting off. On one test cluster it lost nothing while it kept up, at 6,000 and
  9,000 events a second through repeated leader changes; at 12,000 a second it fell behind, two broker
  servers ran out of memory, and acknowledged messages were lost. Size the install to stay below its
  ceiling. See [fail-closed MQTT](./observability.md#mqtt-fail-closed).
- **Carried from `v0.24.0`:** a calculated-metric backfill skips compressed history, and a broker crash
  with a large backlog is not covered by `mqttFailClosed`.

##### Also in this release {#v0250-also}

- **`dcctl bootstrap` and `dcctl upgrade` take `--values`,** a YAML file of tuning (replica counts,
  resources, an area's own configuration) that is recorded on the instance and applied again by every
  later upgrade. A key `dcctl` owns, such as an image, a credential or an endpoint, is refused by name before
  anything changes. `--reset-values` removes it, and `--dry-run` prints what would change. See
  [Tuning an Instance](./tuning-an-instance.md).
- **`dcctl broker reconcile <instance>` and `dcctl broker permissions`** are new; see above.
- **An entity's audit entry is saved in the same transaction as the change.** It used to be written after the
  change committed, so a failed audit write told the caller the change had failed although it had been
  saved, and left it unrecorded. Now both are saved or neither is, which means a failed audit write fails the
  change. Sign-in and authentication events stay best effort.
- **Tenant deletion shows what holds it back.** `tenant_purge_store_blocked{store,reason}` counts open
  deletions per storage system, with `tenant_purge_store_rows_erased_total`,
  `tenant_purge_store_erase_duration_seconds`, `tenant_purge_completed_total`, `tenant_purge_failures_total`,
  `tenant_purge_duration_seconds` and `tenant_purge_clean_duration_seconds`, and three alerts. See
  [Tenant deletion](./tenant-deletion.md#blocked-store).
- **Tenant deletion is faster in two places.** The key-value erasure runs 32 purges at a time instead of one,
  and S3 object deletion is batched, up to 1,000 keys per request. In a single-node loopback test, erasing
  63,000 keys went from 28.9 s to 3.1 s. A batch in which any key fails to delete now fails the pass, which
  is repeated until nothing is left.
- **A writer follows a leader election until it has caught up.** A publisher that routes to a stream's
  leader used to work out the new leader once, and kept the old target if that pass failed. It now retries
  with a growing delay, for at most 30 seconds. This affects performance only.
- **Dashboards.** A board can show a fixed window, chosen with **Custom range** or opened from a
  `?from=&to=` link, in place of the rolling range (see
  [Custom range](../user-guide/viewing-dashboards-in-dash.md#custom-range)). A new history table widget
  lists one device's stored history as rows of bucket averages, and a widget with something to hand over
  has a download button for CSV or PNG. A widget with nothing to draw now says which of five reasons it
  is: no device chosen, not reported by this device, no data in this range, no access, or a failed source.
  `/dash?rotate=tokA,tokB&every=60` cycles two to 24 boards on a wall display, 10 to 3,600 seconds each
  (see [Rotating through several boards](../user-guide/viewing-dashboards-in-dash.md#rotating-boards)). All
  of it is additive: a saved dashboard reads and renders as before.
- **Versioning and security documents.** [Versioning and Upgrade Policy](./versioning-policy.md) says what
  is stable from `v1.0.0`, and the [security model](../concepts/security-model.md) describes the trust
  boundaries and what enforces each. The rolling-upgrade section no longer claims zero downtime.
- **A compatibility check runs on every change.** Every served GraphQL schema, the `dcctl` commands and
  flags, the device wire contract, the device topic layout, the C# SDK's public API and the npm packages'
  exports are compared with the last release, and a break must be declared. Against `v0.24.0` no break is
  declared in this release.
- **The demo player for the Sitepulse showcase sets a token of its own** on each device's credential,
  because it could no longer read one back.

### v0.24.0 — calculated metrics you can author, and an event path that carries more {#v0240-upgrade}

`v0.24.0` is an in-place upgrade from `v0.23.0`: `dcctl install` for the cluster, then
`dcctl upgrade` for each instance, as described [for v0.17.0](#v0170-upgrade). Give `dcctl install`
the settings the cluster was installed with, such as `--ha` and `--database-node-selector`, as well as
the new `--version`. **It cannot be reached in place from `v0.22.0` or earlier: upgrade to `v0.23.0`
first.** Its four migrations add two tables and two indexes and rewrite no existing row (see
[During the upgrade](#v0240-during)). **Roll forward from it, not back:** `v0.23.0` cannot start on
the configuration this release writes (see [Going back](#v0240-going-back)). Coming from `v0.22.0` or earlier, read the
[`v0.23.0` notes](#v0230-upgrade) as well; they apply to any upgrade that crosses that release.

The commands, with the `v` in the version and a rehearsal first:

```bash
dcctl install local --version v0.24.0
dcctl upgrade local devicechain --version v0.24.0 --dry-run
dcctl upgrade local devicechain --version v0.24.0
```

`--version` takes the tag with its `v`. On a cluster other than the default local one, pass
`--kube-context` to `dcctl install` and `dcctl upgrade`, and pass `--registry` when the images come
from a registry other than the default. If any MQTT device still sends a credential in the body of its
events, add `--mqtt-body-credential=strip` to the last command: see [Before you upgrade](#v0240-before).
`dcctl upgrade --dry-run` does not print the `--mqtt-body-credential` value, so confirm it took effect
after the upgrade (see the procedure under
[Devices must stop sending a body credential over MQTT](#v0240-mqtt-body-credential)). If the instance
was bootstrapped without root-key escrow, `dcctl upgrade` prints an escrow warning; that is expected
and unrelated to this upgrade.

This release makes calculated metrics something you author and fill in yourself: a checked formula
editor, per-device constants, formulas that read other calculated metrics, previews, and a
**backfill** that computes a metric for readings stored before it existed. The rest is about an
event path that carries more: events are written in a smaller form by default, detections are
published side by side, and alarm changes are applied several at a time. It also closes two places
where a secret rested where every platform service could read it: a device's credential in the body
of an MQTT event, and the sign-in state. What changes for somebody:

- **Devices must stop sending a credential in the body of an event published over the platform's MQTT
  broker.** Such an event is now refused. To keep a fleet reporting while its firmware is updated,
  upgrade with `--mqtt-body-credential=strip`. See
  [Devices must stop sending a body credential over MQTT](#v0240-mqtt-body-credential).
- **The C# SDK's `IDeviceEventCarrier` has a new abstract member,** `CarriesBodyCredential`; a custom
  carrier no longer compiles until it declares it. See the same section.
- **Everyone signs in once more, and the broker restarts twice.** Sign-in state moves to its own
  broker account and is not carried over, and the device authentication callout is encrypted. See
  [The broker's logins are narrowed](#v0240-broker-logins).
- **Upgrade through `v0.23.0`.** Events are now written in the `compact` form by default, which
  `v0.22.0` and earlier cannot read. See [The event wire format](#event-wire-format).
- **The broker moves to NATS 2.15.1** in the first of those restarts. See
  [The broker moves to NATS 2.15](#v0240-nats).
- **There is no going back to `v0.23.0`.** Roll forward: a `helm rollback` leaves services that
  cannot start. See [Going back](#v0240-going-back).
- **Four migrations:** a table for backfill jobs in `event-management`, a table for the profile
  activation log and an index in `device-management`, and an index in `command-delivery`. See
  [During the upgrade](#v0240-during).
- **`device` is a reserved name in calculated-metric formulas.** See
  [Calculated metrics](#v0240-calculated-metrics).
- **A calculated value's id changes for a JSON reading that sends no time of its own.** See
  [Calculated metrics](#v0240-calculated-metrics).
- **A numeric measurement that is NaN, infinite or too large is refused.** See
  [Measurements](#v0240-measurements).
- **Defaults moved:** the detection engine publishes 32 detections at a time, `device-management`
  applies alarm changes on several lanes, the MCP endpoint caps a batch and limits each caller, and
  the GKE reference module's services pool is a different machine type. See
  [Defaults that changed](#v0240-defaults).

#### Before you upgrade {#v0240-before}

1. **Coming from `v0.22.0` or earlier: stop and upgrade to `v0.23.0` first.** `v0.22.0` and earlier
   read only the `legacy` wire form. A pod on one of them drops a `compact` message, or sets it aside
   as failed in `device-management`, so a rolling upgrade straight to `v0.24.0` loses the events
   written while old and new pods run side by side. Upgrade every instance to `v0.23.0`, wait until
   every pod runs it, then upgrade to `v0.24.0`. On a chart install (not `dcctl`) you may instead set
   `infrastructure.messaging.wireFormat: legacy` for the upgrade and remove it once every pod has
   rolled. `dcctl` has no option to set it.
2. **If any MQTT device sends a credential in the body of its events, upgrade with
   `--mqtt-body-credential=strip`.** Under `deviceAuthMode: required` that is every MQTT device, because
   `v0.23.0` refuses an MQTT event that has no body credential. So firmware cannot be updated first: a
   device updated before the upgrade has its events refused until the upgrade is done. Run
   `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=strip`, then follow the
   steps in [Devices must stop sending a body credential over MQTT](#v0240-mqtt-body-credential).
   **Do not set `deviceAuthMode: optional` to bridge the gap.** It makes the instance accept an HTTP
   event that carries no credential, on the device's token alone.
3. **If a `terraform.tfvars` beside the instance's infrastructure state pins `nats_chart_version`,
   raise it to `2.15.1` or remove it.** A NATS chart older than `2.15.0` is refused when the plan is
   made.
4. **Do not change the broker's replication (`streamReplicas`, `nats_cluster_replicas`) in the same
   upgrade.** While the servers restart, some run `2.14.4` and some `2.15.1`, and a replica or
   placement change started in that window can be left stuck. Make it before or after. See
   [The broker moves to NATS 2.15](#v0240-nats).
5. **If a published calculated-metric formula reads a measured metric whose key is `device` as a bare
   identifier, rewrite it** as `double(m["device"])` (or the conversion for its type) before you
   upgrade. After the upgrade such a formula no longer compiles, so it produces no value on any
   reading. Calculated metrics are off until a tier's `calculatedMetricsPerProfile` is above 0, so
   this affects only tiers where you already raised it.
6. **If you installed the `nats` module yourself, keep `jetstream.limits.default_max_consumers` at
   `-1`.** NATS 2.15 otherwise caps every stream at 1,000 consumers, and the 1,001st QoS 1 or 2 MQTT
   subscription is refused while its device stays connected.

#### During the upgrade {#v0240-during}

- **Four migrations run, one set per first start of the service that owns it.** None writes or
  rewrites a row, and each can be run again if it is interrupted.
  - `event-management` creates the `calculated_backfills` table and its indexes.
  - `device-management` creates `device_profile_activations`, the log of which profile version became
    active and when (it starts empty: nothing is reconstructed for activations before the upgrade),
    and an index on `devices (tenant_id, device_type_id)` that serves the alarm table's device-type
    tabs.
  - `command-delivery` creates `idx_commands_device_time`, which serves the chart markers that show a
    device's commands on its time axis.

  Each index is built with a plain `CREATE INDEX`, which holds a lock against writes to the table
  while it runs, so on a table with millions of rows start that service in a quiet period.
- **The broker restarts, and on the first upgrade that adds the encrypted callout it restarts twice.**
  The first restart carries the move to NATS 2.15.1 and the new sign-in account; the second turns
  callout encryption on once the services have rolled onto a responder that can read it (an instance
  with no device authentication callout has nothing to encrypt and restarts once). Under `--ha`, one
  server at a time, each
  Ready before the next stops; without `--ha` the single server restarts and the broker is
  unavailable for about a minute each time. See [The broker's logins are narrowed](#v0240-broker-logins).
  Devices reconnect on their own, and a persistent MQTT session keeps its subscriptions and the QoS 1
  messages held for it. Devices reconnect without losing messages only under `--ha`. On `kind`
  with `--ha` (two rolling broker restarts) the upgrade took about 4 to 5 minutes in all: about 90 seconds
  for the first roll, about 40 seconds with nothing happening, and about 80 seconds for the second.
  **Do not restart a broker server by hand while it rolls.** See
  [The broker moves to NATS 2.15](#v0240-nats).
- **Services start writing `compact` events as they roll.** Pods of `v0.23.0` read both forms, so a
  rolling update between the two releases is safe.
- **Revoking a device's authority during the roll can fail, or leave a session open.** Disabling or
  deleting a device, or changing a credential, now also ends the device's open MQTT sessions by
  asking `event-sources` to do it, and only an `event-sources` pod of this release answers. Until one
  is running, the change is saved and the call returns an error ("the change was saved, but the
  device's live broker sessions could not be confirmed ended ... repeat the operation"). A change
  that a `device-management` pod of the previous release handles is saved and ends no session, as
  before the upgrade. Repeat such changes after the roll finishes; repeating is safe and asks again.
- **A pod of the previous release that restarts during the roll does not come back.** Before the
  services move, `dcctl` writes the instance configuration for this release, and `v0.23.0` refuses
  it at startup (see [Going back](#v0240-going-back)). A `v0.23.0` pod that is evicted, OOM-killed
  or loses its node in that window stays down until the new release replaces it, so let the
  upgrade finish rather than restarting pods by hand.
- **A sign-in or token refresh that reaches a `user-management` pod of the previous release can
  fail for a few seconds** while the sign-in state moves. It succeeds when retried.
- **A derived value stored by a pod of the old release and one stored by a pod of the new release
  can carry different ids** for a reading that has sub-microsecond digits in its instant. See
  [Calculated metrics](#v0240-calculated-metrics).

#### Going back {#v0240-going-back}

**Roll forward. Do not go back to `v0.23.0`, with `helm rollback` or any other way.** If the upgrade
fails partway, fix the cause and run `dcctl upgrade` again with the same `--version`. Going back
leaves an instance that does not work:

- **The services of `v0.23.0` cannot start.** `dcctl` writes the instance configuration to the
  Secret `dci-<instance>-config`, outside the chart's release, so `helm rollback` restores the
  previous release's workloads and leaves the configuration as this release wrote it. That
  configuration carries keys `v0.23.0` does not know, such as `identityAccount`,
  `calloutIssuerPublic` and `calloutXKeyPublic`, and `v0.23.0` refuses any key it does not know in
  its configuration at startup. Every service it starts fails, and the callout's signing seed is no
  longer in that configuration at all.
- **Devices cannot connect.** The broker is not part of the chart release, so it stays on NATS 2.15
  and keeps encrypting its device authentication callout. The callout's seeds live in a Secret that
  `v0.23.0` does not read, so there is no responder that can serve the broker's requests, and every
  device is refused.
- **The broker cannot go back either.** NATS supports going back from 2.15 only to 2.14.7 or later,
  and the previous release runs 2.14.4.
- **Sign-in state is not restored.** The previous release's sign-in buckets were deleted, so
  everyone would sign in once more.

The new tables and indexes would not be in the way: `v0.23.0` reads the same database and leaves
them unused. Going back to `v0.22.0` or earlier needs `legacy` set first and every `compact` message
drained from the streams (see [The event wire format](#event-wire-format)).

#### After the upgrade {#v0240-after}

- **Nothing is required.** Every new setting has a default.
- **The broker move is one-way.** NATS supports going back from 2.15 only to 2.14.7 or later, and the
  previous release runs 2.14.4.
- **Keep `metrics.enabled` and `metrics.alerts` on** (both are by default) to install the new
  `MqttGatewayStreamUnbounded` alert.
- **Find the devices that still send a body credential.** `devicechain_eventsources_total_msg_credential_in_mqtt_body`
  counts the refused events by source, and under `strip` the ones that were stripped. Update the
  firmware or SDK of each, and rotate the credential of any device that was counted (see the next
  section).

##### Devices must stop sending a body credential over MQTT {#v0240-mqtt-body-credential}

A device on the platform's MQTT broker is authenticated when it connects, through the device
authentication callout. The published contract also had it repeat its credential in the body of every
event, and the broker wrote that body into its own streams before any platform code ran, so a
plaintext password or access token rested in a stream readable by every platform service, by anyone
with the volume and by its snapshots. The body credential is now refused instead:

- **An event published over the platform's MQTT broker whose body carries `credentialType`,
  `credentialId` or `credentialSecret` is refused** unless you have set `capture.bodyCredential` to
  `strip` for the upgrade (below). It is counted in
  `devicechain_eventsources_total_msg_credential_in_mqtt_body`, set aside on the failed-decode stream with the credential
  redacted, and never forwarded. The broker has already acknowledged the message, so the refusal is
  visible to you and not to the device: a device that keeps sending the old body goes quiet while it
  believes it is publishing.
- **An event without a credential is accepted** and attributed to the device that connected. Under
  `deviceAuthMode: required` it is accepted for every event type.
- **HTTP ingest and an MQTT broker you connect as a source are unchanged.** They keep requiring the
  credential in the body.
- **Treat a device that was refused as exposed.** The plaintext existed on the broker for the
  moments before the message was acknowledged, in the MQTT gateway's store while a subscription was
  interested, and in unreclaimed file blocks until compaction. Rotate the credential of every device
  that was counted.
- **Upgrade with `strip`, update the SDKs and firmware, then switch to `refuse`.** `v0.23.0` refuses an
  MQTT event that has no body credential, and this release refuses one that has it, so a fleet cannot
  update its firmware before the upgrade. The bridge is `capture.bodyCredential: strip`, which accepts
  an event from the platform's MQTT broker with the credential removed. In this order:
  1. Upgrade with `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=strip`.
     Events keep flowing, and each one that carried a credential is counted. The command's `--dry-run`
     does not print this flag's value; to confirm the upgrade took it, look for the `event-sources`
     startup warning that says the setting is transitional, or check that
     `devicechain_eventsources_capture_body_credential_strip` is `1`.
  2. Roll out the SDK or firmware that stops sending the body credential.
  3. Watch `devicechain_eventsources_total_msg_credential_in_mqtt_body` until it has stopped rising for
     longer than your slowest device's reporting interval. **Collect the list of devices as you go.** A
     warning in the `event-sources` log names the tenant and the device of each one that still sends a
     credential (once an hour for each device, on each `event-sources` pod); copy those tenant and device
     pairs out of the log during this phase, because it is the only place they are named.
  4. Run `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=refuse`. From here the
     `event-sources` log names no device and the metric has no device label. The only per-device record
     left is the failed-decode stream, which keeps the device name with the credential redacted.
  5. Rotate the credential of every device on the list you collected in step 3 (and any the failed-decode
     stream names after step 4).

  `strip` does not remove the exposure: the broker stored the body before either setting ran, so `strip`
  and `refuse` leave the same plaintext at rest, and step 5 is needed with both. `strip` applies only
  to the platform's MQTT broker. A later `dcctl upgrade` without the flag keeps the value. It is
  transitional: its removal is planned for `v0.26.0`, which is to refuse to start `event-sources` while
  it is still set. On a chart install (not `dcctl`) set
  `functionalAreas.event-sources.config.capture.bodyCredential`.

  While the upgrade rolls, an `event-sources` pod of this release can strip an event before
  `device-management` has rolled. A `v0.23.0` `device-management` refuses that event, because it no
  longer carries a credential, and leaves it to be delivered again a minute later, up to five deliveries
  in all; a `device-management` of this release accepts it. So nothing is lost when `device-management`
  finishes rolling within about four minutes of the first new `event-sources` pod, which an ordinary
  upgrade does. An event still refused on its fifth delivery is set aside on the failed-events stream as
  unauthenticated, without the credential.
- **What to change in a device.** The C# SDK (and the Unity package, which rides it) stops
  sending the credential over MQTT in this release, and still sends it over HTTP. The envelope JSON
  Schema now marks the three members HTTP-only. A custom `IDeviceEventCarrier` must declare the new
  `CarriesBodyCredential` member (`true` for a carrier that goes over HTTP, `false` for the platform's
  MQTT); it is a compile error, deliberately, rather than a default. Firmware that writes the body
  by hand must drop the three members. The edge agent forwards bodies unchanged, and its uplink to a
  platform broker that authenticates devices is not supported in this release.
- **A session no longer outlives its credential.** The connection's token now expires with the
  credential (at the earlier of 12 hours and the credential's own expiry), and revoking a credential,
  rotating its secret, disabling or deleting a device, or replacing it ends the device's open MQTT
  sessions. The device reconnects and is authenticated again against the credential it presents,
  which the database refuses if it was revoked. All of a device's sessions end, so revoking one of
  two credentials briefly drops the session on the other. If the platform cannot confirm the sessions
  ended, the change is still saved and the call returns an error that says so; repeat it.
- **Rotate the tokens of access-token devices whose messages were refused or dead-lettered.** The
  failed-decode and dead-letter records used to keep an access token's `credentialId`, which for that
  credential type is the token itself (for MQTT_BASIC it is the login name). They are now redacted,
  but nothing rewrites a record written by an earlier release: those streams age out in 7 days and
  the dead-letter table is pruned by its retention sweep, and a database backup holds them until it
  expires.
- **`event-sources` needs the broker's system-account login to start** wherever the broker runs the
  device authentication callout (`dcctl bootstrap` mints it), and that login is now limited to listing
  and ending connections. `dcctl upgrade` refuses an instance that has the callout and no system login
  rather than roll `event-sources` into a crash loop.
- **The shared service login can no longer publish on a device's events subject.** Anyone who held it
  could previously forge an event for any device with one publish. This is not a boundary against
  someone who holds the login, which still administers the services' streams.

See [Connecting a device](../guides/connecting-a-device.md),
[Device credentials](../guides/device-credentials.md) and the
[device protocol](../reference/device-protocol.md).

##### The broker's logins are narrowed {#v0240-broker-logins}

Two secrets that rested where every service could read them now rest where only one can. The
mechanism and the restarts are described under
[What an upgrade applies to the infrastructure](#upgrade-infrastructure); what changes for somebody:

- **The device authentication callout is encrypted.** A device's password used to cross the broker in
  the clear to any service that listened for it. `dcctl upgrade` creates the Secret
  `dci-<instance>-callout` (the signing seed and a new encryption seed), mounted into
  `device-management` only. The instance configuration keeps the public halves. A chart-only install
  must create that Secret itself.
- **Sign-in state moves to its own broker account, and everyone signs in once more.** Refresh tokens,
  OAuth authorization codes, single sign-on records and the sign-in throttle move to an `IDENTITY`
  account that only `user-management` can enter, with the password in the Secret
  `dci-<instance>-identity`. The previous release's copies are **deleted, not carried over**, so every
  session from before the upgrade ends: people sign in again in the console and dashboards, and MCP
  and OAuth clients authorize again. An access token already issued works until it expires, 15 minutes
  by default. A chart-only install must add the account to its own broker, or nothing changes.
- **The broker restarts twice on the first upgrade that does both**, as described above. Devices
  reconnect within seconds under `--ha`, and in about a minute without it. If the second step fails,
  devices keep connecting; run `dcctl upgrade` again to finish it.
- **The signing seed is moved, not replaced.** The callout's signing seed used to be in the instance
  configuration, and `dcctl upgrade` carries the same seed into the new Secret. `dcctl` keeps the
  configuration out of Helm's values, so its release history holds no copy. A chart install that
  supplies the configuration inline still has the seed in the Helm releases stored before the
  upgrade; delete those release Secrets if that matters to you.
- **A restored instance gets a new sign-in login,** and sign-in state is not restored from a backup.
- **In the cluster, those Secrets are the only copy of the seeds.** The machine that ran
  `dcctl bootstrap` also keeps them in `~/.devicechain/instances/<instance>/`. There is no rotation
  verb, and editing either Secret outside `dcctl` is unsupported. Anyone who can read Secrets in the
  instance namespace can still read them.

##### The broker moves to NATS 2.15 {#v0240-nats}

The instance's message broker moves from NATS `2.14.4` to `2.15.1` (the official `nats` chart, same
version). `dcctl upgrade` applies it in its first broker apply, the one that also adds the sign-in
account, so the move costs no restart of its own: under `--ha` its servers restart one at a time,
each Ready before the next stops; without `--ha` the single server restarts and the broker is
unavailable for about a minute. The second restart, which turns callout encryption on, follows the
services (see [During the upgrade](#v0240-during)). Devices reconnect on their own, and a persistent MQTT
session keeps its subscriptions and the QoS 1 messages held for it while the device was away.

- **Do not change the broker's replication in the same upgrade.** While the servers restart, some
  run `2.14.4` and some `2.15.1`. NATS 2.15 changed how a stream or consumer changes its replica
  count or placement, and only NATS `2.14.7` and later understand the new mechanism. This upgrade
  starts from `2.14.4`, so a replica or placement change started while any server still runs it
  can be left stuck rather than merely delayed. Streams stay readable and writable throughout, and
  creating or deleting them is unaffected. Raise `streamReplicas` or `nats_cluster_replicas` before
  or after this upgrade, not during it.
- **Do not restart a broker server by hand while the upgrade rolls.** The new configuration reaches
  every server at once, but a server moves to `2.15.1` only when its turn comes. Until then it keeps
  running on the configuration it started with, and if it restarts in that window (by hand, or
  because its node fails) it cannot start on the new configuration and stays down until the
  upgrade replaces it. Under `--ha` that leaves the cluster a server short for the rest of the
  roll, and replicated streams stop taking writes while the next server takes its turn.
- **A NATS chart older than `2.15.0` is refused.** The broker configuration is now written for
  NATS 2.15 and a 2.14 server refuses it at startup, so `nats_chart_version` below `2.15.0` fails
  the plan rather than crash-looping the broker. If a `terraform.tfvars` beside the instance's
  infrastructure state pins `nats_chart_version`, raise it to `2.15.1` or remove it before you
  upgrade. Treat the move to 2.15 as one-way: NATS supports going back from 2.15 only to 2.14.7 or
  later, and the previous release runs 2.14.4.
- **The 1,000-consumer limit NATS 2.15 adds to every stream is lifted.** Each MQTT subscription at
  QoS 1 or 2 is a consumer on the broker's own message stream, so with the new default the
  1,001st such subscription across the fleet would be refused while its device stayed connected,
  and that device would never receive a command. The count is of subscriptions, not devices: a
  device holding two QoS 1 subscriptions uses two. If you install the `nats` module yourself, keep
  `jetstream.limits.default_max_consumers` at `-1`.

What gets better:

- **QoS 1 publishes on one MQTT connection no longer wait for each other.** The broker now stores
  up to 1,024 of a connection's QoS 1 messages at once instead of one at a time. On a three-server
  test cluster, one connection sending 5,000 messages went from about 1,100 to about 20,000
  acknowledged a second.
- **A broker configuration reload no longer stops QoS 1 and 2 delivery to devices already
  connected.** With TLS on the MQTT listener, which is the default, every reload re-applied the
  MQTT settings and left each connected session unable to receive QoS 1 or 2 messages, commands
  included, until it reconnected. A certificate renewal could trigger one.
- **Fixes to device sign-in and permissions**, among them how the broker decides which logins skip
  the device sign-in check, and a sign-in refusal now reported as an authentication error.
- **The edge agent's local broker no longer refuses a site's 1,001st QoS 1 or 2 subscription.**
  `dc-edge-agent` embeds NATS 2.15, and from `v0.20.0` through `v0.23.0` its local MQTT listener
  had the same 1,000-consumer limit, answering a site's 1,001st QoS 1 or 2 subscription with a
  refusal while the device stayed connected. The limit is lifted there too. Upgrade the agent on any
  site whose devices hold more than 1,000 such subscriptions on its listener between them.

##### Calculated metrics {#v0240-calculated-metrics}

Calculated metrics stay off until a tier's `calculatedMetricsPerProfile` is above 0 (set in
`v0.23.0`). New in this release, wherever they are on:

- **Authoring in the console.** The device profile's Metrics tab has a **Kind** switch (Measured or
  Calculated) and a checked **Formula** editor. The draft is compiled by the same compiler the
  publish gate and the resolver use, and shows its cost against the ceiling of 100. The
  [user guide](../user-guide/calculated-metrics.md) has a chapter on it.
- **Per-device numeric constants.** `device.calibration` or `device["ct-ratio"]` read a number from
  the device's metadata. Only a JSON number counts; a string, boolean, null, object or list is
  present but unusable and is never coerced. **`device` is now a reserved name**: a measured metric
  keyed `device` is read as `m["device"]`, with its type conversion. A published formula that read
  such a metric as a bare identifier no longer compiles; it produces no value on any reading, is
  counted as `eval_error` in `devicechain_devicemanagement_calcmetric_skipped_total`, and is
  reported in the `device-management` log. A value the device sends under that calculated metric's
  key is still dropped.
- **Chaining.** A formula may read another calculated metric, up to four deep. A ring (`a -> b -> a`)
  or a chain deeper than four is refused at publish, naming the chain.
- **Previews.** `previewRule` accepts the draft profile's metrics and re-derives calculated values
  under the draft, and a **Try it on recent history** panel in the console evaluates a draft formula
  over a device's stored readings.
- **Backfill.** A job that computes a calculated metric for readings stored before its version was
  published, and writes the values that are missing. See [Backfilling history](#v0240-backfill).
- **Metrics from Sparkplug and LwM2M devices now compute.** A device that sends one metric per entry
  (Sparkplug, LwM2M, or a JSON batch) used to skip every multi-input formula as `missing_input`,
  because each entry was its own reading. A reading is now every measurement of one message that
  carries the same exact instant, so `power = voltage * current` computes when the two share an
  instant. Sparkplug and LwM2M stamp to the millisecond, so a node that stamps each metric on its own
  (values a few milliseconds apart), or reports only what changed, still gets no value for a formula
  over several inputs. If one metric appears twice at one instant, the formula reads the first.
  LwM2M readings of one SenML pack now share one receipt instant. The user guide describes
  [how a reading is formed](../user-guide/calculated-metrics.md#what-a-calculated-metric-is).
- **Two calculated samples of the same name at the same instant keep one stored row**, the first
  written.
- **The id of a derived value now follows its instant in UTC at microsecond precision.** Readings
  whose instant is exact to the microsecond (every Sparkplug and LwM2M reading, and a JSON reading
  that sends its own time with up to six fractional digits) keep the same ids. A reading with
  sub-microsecond digits gets a new derived id, which includes every JSON reading that sends no time
  of its own, because the platform dates it at receipt with nanoseconds. If that same reading is
  stored again after the upgrade, because event storage was handed it again (it was not yet
  acknowledged when the pods rolled) or the device resent a reading from before it, its derived value is stored as a second row. Raw rows are
  unaffected, and old rows are not rewritten.
- **A message that carries none of a formula's inputs is no longer counted as a skip.** A battery
  formula used to record a `missing_input` skip for every temperature message of a device that sends
  the two in separate messages (about 3,600 an hour for a message a second). Such a message is now
  simply not evaluated, and a message that carries some of the inputs but not all still counts as
  `missing_input`. After the upgrade, `devicechain_devicemanagement_calcmetric_skipped_total` with
  `missing_input` and the matching log lines drop, so a dashboard or alert built on it reads lower.
  No stored value changes.
- **Formulas that read a device constant now compute on live readings from devices that authenticate
  with a credential** (MQTT login or access token, over any transport). They were skipped as
  `missing_input` on every such reading, while the console preview, which takes constants from the
  request, worked. A constant you edit reaches the next reading.
- **In the console,** the profile's Versions tab shows, for the active version, whether each
  calculated metric is Computing, Not computing or has no input readings over the last 24 hours or
  7 days, counted from stored readings; Backfill is disabled up front, with the reason, when the
  instance cannot run it (`calculatedMetricBackfillAvailability`); and `calculatedMetricPlan` marks a
  metric whose name another profile's active version also uses (`nameSharedWithOtherProfiles`).
- **A device profile records which version became active, and when.** `DeviceProfile.activations`
  lists publishes and rollbacks, newest first. The log starts at the upgrade; a profile published
  earlier has no entry until its next publish or rollback, and a missing entry means unknown, not
  inactive.

##### Backfilling history {#v0240-backfill}

A backfill fills a calculated metric into the history stored before the version that defines it was
published, so charts and exports of past time show it. It is available wherever calculated metrics
are, and it needs nothing to turn on beyond a published profile version that has a calculated metric.

- **Starting and cancelling a backfill need `device:write`; reading and listing jobs need
  `event:read`.** A customer-scoped caller is refused by all four operations. The console starts one
  from the profile's Versions tab and offers it once after you publish a version that adds a
  calculated metric.
- **A job is bounded.** At most 90 days per job, ending no later than now, over one profile's
  published version, for up to 1,000 named devices or every device of the profile. A range older than
  retention allows is refused rather than shortened. One job is active (queued or running) per
  tenant.
- **It only adds values and is silent.** A stored value is never changed. A backfilled value appears
  in dashboards, history and exports; it does not change a device's latest values, and detection
  rules never see it.
- **A reading that holds one input twice with different values is skipped as `ambiguous`**, where
  live calculation reads the first. The job counts every skip by reason.
- **Compressed history is skipped** in this release (the event store compresses chunks older than 7
  days by default), and the job counts what it skipped.
- **One worker per instance runs the jobs, at a bounded rate.** `calculatedBackfill.enabled` in the
  `event-management` configuration has three states. Unset, the default, the worker runs when the
  connection pool has room for it beside the persistence writers and stays off, with a startup
  warning naming the settings, when it does not, so an instance tuned before this release starts as
  it did. `true` stops the service at startup if the pool cannot hold the worker. `false` turns the
  worker off, and starting a backfill is then refused as unavailable with the reason.
  `calculatedBackfill.rowsPerSecond` defaults to 20000, from 1 to 200000, and the default is
  provisional. See [Calculated-metric backfill](./observability.md#calculated-backfill), which also
  lists the metrics and the SQL that cancels a stuck job, and the user guide's
  [Backfilling history](../user-guide/calculated-metrics.md#backfilling-history).

##### Measurements: NaN, infinity and huge values are refused {#v0240-measurements}

A numeric measurement that is NaN, infinite, or 1,000,000,000,000 or more in magnitude is now refused
when the event is resolved.

- For a metric **declared on the profile**, the whole event is rejected as invalid. Before, a value
  of 1e12 or more was already lost from history (the column refused it); it is now also kept out of
  live state and rule evaluation, and NaN no longer slips past a metric's Min and Max.
- For an **undeclared key**, only that one value is dropped, with a warning in the `device-management`
  log. Before, the whole event failed to store.
- A NaN or infinite value no longer makes event storage retry the message for about five minutes
  (five deliveries) and then report it as a downstream failure.

##### Defaults that changed {#v0240-defaults}

| Setting | Was | Now | Restore the old behaviour |
| --- | --- | --- | --- |
| `infrastructure.messaging.wireFormat` | `legacy` | `compact` | `legacy` (chart install only; see [Before you upgrade](#v0240-before)) |
| `event-processing` `derivedPublishing.maxInFlight` (new) | one detection at a time | 32 | Set `1`. Still at most one outstanding detection per device, so each device's detections arrive in order. Publishing again without duplicates holds within the derived-events stream's 30-minute duplicate window. |
| `device-management` `raiseAlarm.workers` (new, fixed, read at startup) | one change at a time | `max(1, min(8, pool - resolution.workers - 4))`, which is 6 on the default pool of 20 | Set `1` for the old inline path. Changes to one alarm are always applied in order. An explicit value is validated against the connection pool and never adjusted. |
| `mcp` `maxBatchEntries`, `callsPerSecond`, `callsBurst` (under `functionalAreas.mcp.config`) | no cap | 16, 20 and 40 | Raise them; `callsBurst` may not be below `maxBatchEntries`. |
| `event-sources` `capture.bodyCredential` (new, fixed, under `functionalAreas.event-sources.config`) | none: a body credential on the platform's MQTT broker was accepted | `refuse` | Set `strip` for the upgrade only (transitional, removal planned for `v0.26.0`). See [Devices must stop sending a body credential over MQTT](#v0240-mqtt-body-credential). |
| GKE reference module `services_machine_type` (`deploy/gke`) | `n2-custom-4-8192` | `t2d-standard-4` | Set it explicitly. Changing an existing pool's machine type recreates its nodes. |

- **Detection:** detections of different devices are published side by side, up to 32 at a time,
  instead of waiting for each acknowledgement. On a GKE test cluster under a firing-heavy load of
  about 2,500 detections a second offered, a window of 32 published 2,441 detections a second against
  528 for a window of 1, and the watermark lag was 5.1 s instead of 139 s. The byte budget
  `derivedPublishing.maxBufferedBytes` now counts every prepared payload that is not yet
  acknowledged. See [the detection engine page](./detection-engine.md#configuration).
- **Alarms:** `device-management` used to apply each raise-alarm change in its read loop, about a
  hundred a second on a local database however idle the service was. It now applies them on
  `raiseAlarm.workers` lanes, and changes for one alarm go to the same lane. In a local test
  with 8 lanes that was four to seven times as many. The default leaves four pooled connections free beside
  the resolvers; both pools share the connection pool, so size them together. See
  [How fast alarms are applied](./detection-engine.md#how-fast-alarms-are-applied).
- **MCP:** a batch over the cap is refused whole (HTTP 400, JSON-RPC invalid request, nothing runs),
  and a caller over the rate gets HTTP 429 with `Retry-After`. A batch of N counts as N messages, and
  the caller is the verified token's subject (tenant and email). See
  [the MCP server's limits](../concepts/mcp.md#limits-and-boundaries).
- **GKE:** on a 3+3 HA install, T2D services nodes persisted about 18% more events a second (about
  17,200 against 14,500 and 14,700, offered 18,000 over HTTP), and the MQTT QoS 1 acknowledgement p99
  at 9,000 events a second was 38 ms against 578 and 171 ms. The fallback `n2d-custom-4-8192` equals
  the N2 shape within noise and is about 13% cheaper per node. `T2D_CPUS` is its own quota, and not
  every zone offers T2D (`us-east4-a`, `-b` and `-c` do). The database pool stays `n2-standard-4`.

##### MQTT fail-closed acknowledgement {#v0240-mqtt-failclosed}

`infrastructure.nats.mqttFailClosed` is accepted by the configuration (`v0.23.0` refused it) and
**remains off by default**: nothing changes unless you set it. With it on, a QoS 1 PUBACK means the
platform holds the message. The broker's own MQTT message store (`$MQTT_msgs`) is declared bounded and
discard-new, so a full store refuses a QoS 1 publish (the device gets no PUBACK and resends) instead
of discarding one it has already acknowledged. `event-sources` forwards from the store, and the store
is handed between pods safely during a rolling update. A store that is nearly full backs devices off:
`device-management` refuses new MQTT connections once it is 80% full and until it is below
60%, which a device sees as CONNACK code 5 (not authorized), so firmware must keep retrying on it. The setting is refused
on a broker that also serves another instance.

Limits, described on the [observability](./observability.md#mqtt-fail-closed) and
[transport matrix](../reference/transport-matrix.md) pages: QoS 0 is never stored; a broker crash
with a large backlog is not covered; PUBACKs are slower with it on (p99 336 ms at 6,000 events a
second in one test, against 35 ms with it off); and memory headroom near 10,000 events a second is
thin in both modes, so raise `nats_memory_limit` if a server is OOM-killed.

##### The MQTT gateway's streams are bounded on a new install {#v0240-gateway-streams}

On a fresh `v0.22.0` or `v0.23.0` install, the MQTT gateway's `$MQTT_msgs`, `$MQTT_out` and
`$MQTT_qos2in` streams were created by the broker with no size limit when the first MQTT device
connected, and stayed unbounded until `event-sources` restarted. An unbounded `$MQTT_msgs` can fill
the broker's volume. Every `event-sources` pod now checks these streams every 30 seconds and sets the
ceiling, so on an existing, running instance they are bounded as soon as the new pods start. A new
alert, `MqttGatewayStreamUnbounded` (warning, 10 minutes), fires when the ceiling could not be set,
usually because the broker has no storage left to reserve it. New metrics:
`devicechain_eventsources_mqtt_gateway_stream_unbounded{stream}` and
`devicechain_eventsources_mqtt_gateway_stream_bounded_late_total{stream}`.

#### What else changed {#v0240-what-changed}

##### Known limitations {#v0240-known-limitations}

- **Carried from `v0.23.0`:** with `mqttFailClosed` off, a change of the MQTT capture stream's leader
  under heavy load can lose acknowledged QoS 1 events without counting them
  (`JetStreamCaptureLeaderChanged` warns). The documented MQTT ceiling is about 6,000 events a second
  on three 4-vCPU nodes, bound by the broker.
- **A broker crash with a large backlog is not covered by `mqttFailClosed`.** In one load test at
  9,000 QoS 1 events a second, before the store's reader was made faster, two of the three NATS
  servers ran out of memory and about a third of the acknowledged events never reached the event
  store. Where they were lost has not been established. The setting is opt-in and off by default.
- **Backfill skips compressed history,** and its default rate (`rowsPerSecond`) has not been measured
  on a loaded instance.

##### Also in this release {#v0240-also}

- **`dcctl upgrade` checks the object-store mount before it records the new version.** A
  ReadWriteOnce store that a second pod would mount is refused before the version is recorded, as
  `--enable-area` already did. The "recorded binding" message no longer names the context twice.
- **A `sendCommand` rule action can bound how long its command waits for the device.** The optional
  `expiresAfter` takes a duration from `1m` to `168h`; without it the command waits for the 7-day
  default as before. A value outside the range, or one that cannot be read, is refused when the rule
  is saved or published, and a stored rule with an invalid value is dropped (counted with the reason
  `invalid_action`) rather than sent without a bound.
- **Dashboards and widgets.** The alarm table can group its alarms into tabs by device type, each with
  its own count and highest severity; the stat card can show a trend sparkline; a new fleet count
  card counts a group's devices, or those needing attention, and leaves out devices whose profile
  does not declare the measurement; a device status card shows whether one device is online, with
  when it was last seen, and an online doughnut splits a group into online, offline and no status
  yet (both read the platform's device status and need permission to read device state); the
  command slider has a stepper mode; a click on a map marker selects the device, and a link can open
  a board with a device bound; an alarm severity breakdown shows active alarms by severity; the
  timeseries chart can mark alarms and commands on its time axis, limited to the alarms about the
  measurement it plots with the `markAlarmsScope` option (a new chart starts limited; a chart saved
  without the option marks every alarm of the device), and draw one line per device across a group
  with `groupBy: device` (5 lines by default, at most 12); the widget editor offers a device's
  metrics and marks the calculated ones; and **Create dashboard** on a
  device type's Profile tab drafts a board from what the profile declares. All of it is additive: a
  saved dashboard reads and renders as before. See the [user guide](../user-guide/dashboards.md).
- **Console: forms no longer save onto the wrong entity.** After switching registry, tenant, tier,
  role or device, a form could keep the previous entity's data and save it onto the new one. Display
  data and data that is acted on are now kept apart, across every query in the console.
- **GraphQL.** The placeholder `_empty` mutation on `event-management` is gone, because the service
  now has a real mutation (starting and cancelling a backfill). A client that called `_empty` gets an
  unknown-field error.
- **Detection dashboard.** Two latency panels (the p50 and p99 of the rule-health write and of the
  derived-publish acknowledgement) join the rule-health write panel.
- **A task that keeps failing no longer fills the log.** The "Maintenance pass failed" line of a
  periodic task, such as the detection tick that runs every second, is logged once when a streak of
  failures starts, then at most once a minute with a count of the lines held back, and once when the
  task recovers. Each Sparkplug source keeps its own streak, so one failing source does not hide
  behind a healthy one. Seven loops
  that recorded no pass duration, outcome or last success now do, under the existing
  `devicechain_<area>_<task>_passes_total` series.
- **Stream and bucket access is declared per service.** Each service may open only the streams and
  key-value buckets it is declared to read or write, and refuses to start naming the stream and the
  fix if it asks for another. A broker start abandoned by the caller (shutdown) no longer spends about
  a minute per stream retrying.
- **Rule health.** Rule-health fire counters are batched off the detection loop (about 2.6 ms saved
  per published detection on PostgreSQL), and the detection dashboard gains a panel for
  `detect_rule_stat_write_failures_total` and the new `detect_rule_stat_admission_contended_total`.
- **Device ownership.** The times on ownership facts come from the database's clock, and a device
  re-queued repeatedly keeps its place in the backlog, so it can no longer starve.
- **A fail-closed durable that is created again starts at the store's first sequence,** so a
  consumer that was deleted does not leave acknowledged messages unread (only with `mqttFailClosed`
  on). A repeat is folded by the destination stream's duplicate window and stored again beyond it.
- **Customer users.** A member limited to some customers sees their Updates list again, and the
  customer Users tab beyond 10,000 members shows the first 10,000 with a notice instead of an error.
- **Go 1.27.3.** The toolchain moves to Go 1.27.3, which carries the fix for a `crypto/x509` advisory.
- **Replacing the instance root key means recreating the instance.** There is no rotation verb;
  see [Disaster recovery](./disaster-recovery.md#replace-root-key-losses).
- **Building from source.** The C# SDK now builds with the .NET SDK pinned in `global.json` (8.0.425),
  so a locked restore no longer breaks when a runner image moves a patch.
- **Documentation.** A user-guide chapter on calculated metrics (English, Spanish and Chinese), and
  the quickstart's escrow options, MQTT CA extraction and local TLS testing.

### v0.23.0 — customer users with scoped access, and an MQTT ingest that loses less {#v0230-upgrade}

`v0.23.0` is an in-place upgrade from `v0.22.0`: `dcctl install` for the cluster, then
`dcctl upgrade` for each instance, as described [for v0.17.0](#v0170-upgrade). Give `dcctl install`
the settings the cluster was installed with, such as `--ha` and `--database-node-selector`, as well as
the new `--version`: it refuses to change a cluster's settings while an instance runs on it. Unlike
[`v0.22.0`](#v0220-upgrade) it **does run migrations**: seven services add columns, indexes or
tables, and `user-management` replaces the unique index on role tokens with two, one for the
operator's catalogue and one for each tenant's own roles. None of them rewrites existing rows, the
only backfills record the tokens of customers and groups that already exist, and each is safe to
run again if it is interrupted. Coming from
`v0.21.x` or earlier, read the [`v0.22.0` notes](#v0220-upgrade) first; they apply to any upgrade
that crosses that release. An instance built by `v0.16.0` or earlier still has to be
[destroyed and built again](#pre-declaration-recreate).

This release adds **customer users**: members of your tenant who are limited to some of your
customers or to groups of devices and see only that equipment, with roles your tenant defines and
invitations you send yourself. Most of the rest is about not losing events and not raising the
wrong alarm: the stream that holds MQTT device events until `event-sources` forwards them drains
twice as deep and has room you can size, a threshold alarm follows the device's newest reading, and
a change of a device's owner reaches device-state even if a pod dies at the wrong moment. What
changes for somebody:

- **The MQTT capture stream's ceiling grows.** It was capped at 256 MiB and now takes the full hot
  stream ceiling, so a JetStream volume sized tightly around the old figure can leave
  `event-sources` unable to start. See [The capture stream's ceiling grows](#v0230-capture).
- **A clustered broker (`--ha`) restarts its servers one at a time, once**, to turn on compression
  between them. See [Broker route compression](#v0230-route-compression).
- **Calculated metrics are computed**, but stay off until you raise a tier's
  `calculatedMetricsPerProfile` above 0. Do not do that during the rollout. See
  [Calculated metrics](#v0230-calculated-metrics).
- **Threshold alarms follow the device's newest reading**, not the last reading to arrive. See
  [Threshold alarms and late readings](#v0230-threshold).
- **Two new permissions, and `role:read` and `role:write` now also act inside a tenant.** See
  [Customer users, scoped roles and the permissions behind them](#v0230-customer-users).
- **A deleted customer's or group's token can no longer be reused**, and a member limited to some
  customers is refused, rather than answered, by operations that cannot be limited. See
  [Customer users, scoped roles and the permissions behind them](#v0230-customer-users).
- **Two message identifiers on the event path changed format**, which opens a small window for a
  duplicate during the rollout. See [Event ids and the correlation header](#v0230-ids).
- **The next release changes how events are written on the wire**, and it can only be reached
  through this one. See [Upgrade through this release before the next](#v0230-next-wire).
- **A few defaults move:** the console cannot be framed by other sites, the capture stream's
  publish window is 1024, and writers publish to their stream's leader. See
  [Defaults that changed](#v0230-defaults).
- **Some limits measured on a test cluster** apply to MQTT-heavy installs. See
  [Known limitations](#v0230-known-limitations).

#### Before you upgrade {#v0230-before}

1. **If you raised `streamMaxBytes`, or you sized or capped the JetStream volume yourself, check
   the headroom first.** The capture stream's reservation grows by `streamMaxBytes` minus 256 MiB,
   which is 768 MiB at the default, and a new `device-ownership` stream reserves 128 MiB. See
   [The capture stream's ceiling grows](#v0230-capture).
2. **If an application or script deletes a customer or a group and creates one at the same token
   again**, plan to choose a new token. See
   [Customer users, scoped roles and the permissions behind them](#v0230-customer-users).
3. **If you rely on threshold rules**, read [Threshold alarms and late readings](#v0230-threshold)
   so an alarm that now raises is not a surprise.
4. **Plan to leave every tier's `calculatedMetricsPerProfile` at 0 until the rollout is over.**
   `v0.22.0` has no such setting, so every tier starts at 0, and nothing needs changing before the
   upgrade. Do not raise it, or publish a calculated metric, until every `device-management` and
   `event-management` replica runs this release. See [Calculated metrics](#v0230-calculated-metrics).

#### During the upgrade {#v0230-during}

- **Migrations run on first start** in `user-management` (a scope on each membership, a tenant on
  each role, invitations), `device-management` (the customers of each alarm, access scope on groups,
  retired customer and group tokens, a queue of ownership changes, a formula on a metric
  definition), `command-delivery`, `device-state`, `update-management`, `dashboard-management`
  (dashboard shares) and `notification-management` (quiet hours, the cycle of an alarm). The new
  columns are empty on every existing row, which is intended and is described
  [below](#v0230-customer-users). The new indexes are built without `CONCURRENTLY`, so writes to
  the table being indexed wait for the build, a short time unless the table is very large.
- **On a clustered broker, the broker's servers restart one at a time, once**, because the new
  default compression is part of the broker's configuration.
- **`event-sources` raises the capture stream's ceiling in place** on its first start. No stream
  is recreated and no message is lost.

Going back to `v0.22.0` after the upgrade reads the same database: the changes are new columns,
indexes and tables, and the split of the role-token index. Do not do it while any member is limited
to customers or groups, or while a tenant has defined roles of its own. `v0.22.0` reads neither a
membership's scope nor a role's tenant, so such a member would no longer be held to their scope,
and a tenant's own roles would look to it like roles of the operator's catalogue. Disable or remove
those members, and delete the roles your tenants defined, first. Rolling `event-management` back
after calculated metrics are in use can store a redelivered message twice (see
[Calculated metrics](#v0230-calculated-metrics)).

#### After the upgrade {#v0230-after}

- **Nothing is required.** Every new setting has a default, and no member has a scope until you
  give them one.
- **Check the capture stream's headroom** if you did not before, and keep `metrics.enabled` and
  `metrics.alerts` on (both are by default) so `JetStreamCaptureBacklogWillOverflow` is installed.
- **Alarms, commands and update assignments from before the upgrade are not visible to a member
  limited to customers or groups.** The server records their customers from this release on. A device's live
  state is recorded the next time it reports or changes owner.

##### The capture stream's ceiling grows {#v0230-capture}

Events from MQTT devices wait in a capture stream until `event-sources` forwards them. The broker
has already acknowledged each of them to its device, so when the stream is full it discards the
oldest unforwarded events and the devices are never told. In a load test at 12,000 events a
second, 7.2% of the acknowledged events were lost this way: the stream held 256 MiB and the
platform kept up with about 7,100 a second.

- **The drain is twice as deep.** `capture.publishWindow` is 1024, up from 512. In a benchmark that
  delays only the acknowledgements, the forwarding rate at a given delay doubled. It is a fixed
  setting of `event-sources`, from 1 to 1024; a value outside that range stops the service.
- **The stream takes the full hot ceiling.** It was capped at 256 MiB, or the hot ceiling if that
  was lower, and no setting could raise the cap. It now follows `streamMaxBytes` like the other hot
  streams: 1 GiB by default, and 64 MiB on a `--compact` instance, where nothing changes. At the
  defaults the JetStream disk reservation goes from 10,696 MiB to 11,592 MiB, of the 14 GiB the
  default volume allows: 768 MiB for the capture stream and 128 MiB for the new `device-ownership`
  stream.
- **If you raised `streamMaxBytes` and sized the volume tightly, the reservation grows by that
  value minus 256 MiB.** JetStream reserves a stream's ceiling when it is created or raised. A
  reservation over the broker's limit makes `event-sources` stop at startup with `insufficient
  storage resources available`, and it keeps stopping until you add room (see
  [Bootstrap an Instance](./bootstrap.md)) or lower the ceiling.
- **A stream edited to discard new messages is put back.** A full capture stream that refuses new
  messages reaches no device, because the broker acknowledges the publish first, so the newest
  events would be lost and nothing would count them. Every declared stream is returned to
  discarding the oldest, and the service logs it.
- **A new critical alert, `JetStreamCaptureBacklogWillOverflow`,** fires when the capture backlog
  holds at least a tenth of the stream and will fill it within ten minutes at the rate it grew over
  the last three, for one minute. It is installed with the other alerts when `metrics.enabled` and
  `metrics.alerts` are on. See [Observability](./observability.md).

##### Broker route compression {#v0230-route-compression}

The servers of a clustered broker copy every stored event to each other. `nats_route_compression`
compresses that traffic, and it is now `s2_fast` by default on a clustered broker (`--ha`, or
`nats_cluster_replicas` above 1). A single-server broker is unaffected. On a three-node cluster it
cut the traffic between servers by 60% per event; compressing cost each server 2.6–2.9% of its
CPU, and the broker's total CPU per event still fell about 4%. `dcctl upgrade` restarts the
broker's servers one at a time, once, to turn it on. A value you set yourself, `off` included, is
kept. See
[Compression between broker servers](./bootstrap.md#route-compression).

##### Threshold alarms and late readings {#v0230-threshold}

A threshold alarm used to follow the last reading to arrive. A reading the platform received late
could raise an alarm that newer readings had already cleared, or clear one they kept raised. Now:

- **While an alarm is raised, a reading up to ten minutes older than the device's newest one is
  discarded and counted**, so it cannot clear the alarm. A reading older than that is evaluated, so
  a device clock set back cannot hold an alarm raised.
- **While no alarm is raised, a matching reading further behind than the lateness tolerance is
  discarded only if it was queued for detection more than 30 seconds after the platform received
  it**, as a retried publish is, or if either time is unknown. The tolerance is measured from the
  newest reading the engine has seen. Any other matching reading raises, so a device whose clock runs
  slow, or one uploading readings it stored during an outage, now raises an alarm from them as they
  arrive.
- **Count-window and rate rules place a reading by its own time**, the way duration rules do. They
  raise and clear one lateness tolerance behind the newest reading the engine has seen, and the last reading before a
  quiet spell is decided on wall-clock time once the engine has been idle.
- **Sliding-window rules read their window as of the device's newest reading**, so a late reading
  counts only alongside the readings that were inside one window with it.

`devicechain_eventprocessing_detect_late_samples_total` counts these discards. See
[the detection engine page](./detection-engine.md).

##### Customer users, scoped roles and the permissions behind them {#v0230-customer-users}

A member can now be limited to some of your customers, to dynamic groups of devices, or to both (up
to 16 in all), and sees only the devices, alarms, events, commands and dashboards that belong to
them. Tenant administrators invite members, define roles and share dashboards from the console; the
whole chapter is
[Members, roles and customer users](../user-guide/members-roles-and-customer-users.md). What
matters when you upgrade:

- **There are two new permissions.** `member:read` lists members and invitations, and `member:write`
  invites, assigns roles and scope, enables, disables and removes. They are tenant permissions and
  are not in the read-only baseline. The built-in tenant administrator role holds `*` and has both.
- **`role:read` and `role:write` now act on two planes.** On the instance admin plane they still
  reach the operator's role catalogue. Held on a tenant role, they now let the member list and
  define the roles that tenant owns.
- **A change to a member's access takes effect at their next refresh**, within the access-token
  lifetime (15 minutes by default) or 5 minutes for a member limited to customers or groups. It
  does not sign the person out of their other tenants.
- **Operations that cannot be limited are refused** to a limited member, with `FORBIDDEN` and
  `extensions.reason` set to `SCOPE_UNSUPPORTED`, whatever their role says. The operations that can
  be limited answer only with what the member owns, and a record outside it is answered as
  `NOT_FOUND`. See
  [When your access is limited to some customers](../reference/graphql-api.md#limited-access).
- **A deleted customer's or group's token is retired in its tenant.** `createCustomer` or
  `createEntityGroup` at that token answers `INVALID_VALUE` with a message that says why, and only
  deleting the whole tenant frees it. A customer that still exists at the token still answers
  `CONFLICT`. Records that name a customer only by its token would otherwise show the old customer's
  history to a new one. Tokens of customers and groups deleted before this release are not retired.
- **A device's ownership changes always reach device-state.** The change is committed together
  with a queued announcement that is retried until the broker acknowledges it, where it used to be
  published after the commit. A pod that died in between used to leave the old owner's members
  seeing the device's live state until its next event, which for a silent device never came.
- **Dashboards can be shared with customers and groups.** A customer user sees only published
  dashboards shared with them. A widget bound to an area or an asset reads the relationship graph,
  which they are not given, and shows **Not available for customer users**.

##### Event ids and the correlation header {#v0230-ids}

The id the broker uses to drop a duplicate publish, and the header that carries a correlation id
between services, are shorter: the header is `Dc-Cid` (was `Dc-Correlation-Id`) and the ids use
base 36. A new id can never equal an old one. A rolling upgrade has pods of both builds at once, so
an event whose publish acknowledgement was lost, or whose pod stopped without draining, while the
rollout was running can be delivered twice. Stored telemetry is not duplicated, because an event's
key is a digest of its content, but a counting, repeating or sliding-aggregate rule may count that
event twice. Log correlation is not carried for messages in flight across the upgrade. The exposure
ends 10 minutes after the last old pod is gone.

##### Upgrade through this release before the next {#v0230-next-wire}

Events cross two retained streams as protobuf messages, and this release can read a smaller form of
them: instants as a count of nanoseconds and device-credential seals in binary, about a third
smaller per message. **It still writes the old form by default**, so a pod of the previous release
can read everything. The next release writes the smaller form by default, and a pod that predates
this release would drop or dead-letter those messages. Upgrade every instance to `v0.23.0` first,
and only then to the next release. `infrastructure.messaging.wireFormat` (`legacy` or `compact`,
exactly as written) selects the form. Set `compact` only after every pod runs this release, and set
`legacy` again before rolling back to an earlier one.

##### Defaults that changed {#v0230-defaults}

- **The console cannot be framed by another site.** Its pages are served with
  `Content-Security-Policy: frame-ancestors 'self'` and `X-Frame-Options: SAMEORIGIN`. The `/dash`
  viewer stays frameable, because embedding it on a wall display is a supported use. A page of
  another origin that embeds the console itself stops rendering it.
- **`capture.publishWindow` is 1024** (was 512).
- **Writers publish to their stream's leader.** `infrastructure.nats.affinity.publishToLeader` is on
  by default: the pipelined writers in `device-management`, `event-sources` and `sparkplug-ingest`
  open a second connection to the server that leads their stream, and fall back to the usual one
  if it fails. On a three-node cluster a single `device-management` pod's
  ceiling rose about 6%. Set it to `false` to turn it off.

##### Calculated metrics {#v0230-calculated-metrics}

A metric definition can carry a formula over the profile's other metrics, and `testMetricFormula`
previews one. A published formula is now computed when each reading is resolved and stored as an
ordinary measurement, so history, the hourly rollup, latest state, detection and the live stream all
receive it. A value a device sends under a calculated metric's key is dropped in favour of the
computed one, and a formula that cannot produce a value skips only that metric for that reading.

**Calculated metrics stay off while every tier's `calculatedMetricsPerProfile` is 0**, which is
the default; `v0.22.0` had no such setting. Publishing a profile with a calculated metric is
refused with `LIMIT_EXCEEDED` until its tenant's tier allows it, so the upgrade has no effect until
then. Before upgrading, check your tiers. If any is above 0, set it to 0, or do not publish a
calculated metric, until every `device-management` and `event-management` replica runs this release,
and then wait at least `metricDefCacheTtlSeconds` (300 seconds by default) after the last old
`device-management` pod stops. Publishing during a mixed rollout can store a device-sent value
under a calculated metric's key permanently, and messages redelivered across old and new
`event-management` replicas can be stored twice. Rolling `event-management` back after calculated
metrics are in use carries the same duplicate risk.

##### New alerts and metrics {#v0230-observability}

- `JetStreamCaptureBacklogWillOverflow` (critical), described above;
- `JetStreamCaptureBacklogGrowing` (warning), its early warning: the same backlog check over a
  twenty-minute horizon;
- `JetStreamCaptureLeaderChanged` (warning): the capture stream changed leader while events were
  arriving, so MQTT events published during the change may have been acknowledged and lost (see
  [Known limitations](#v0230-known-limitations));
- `DeviceStateDroppingEvents` (warning): device-state discarded an event after its last delivery
  attempt, which leaves a device's connectivity, a reading or its position stale until a newer
  event arrives;
- `devicechain_notificationmanagement_quiet_hours_total{outcome}` for notifications held, released
  or dropped by quiet hours;
- `devicechain_devicemanagement_calcmetric_evaluations_total`,
  `devicechain_devicemanagement_calcmetric_skipped_total{reason}` and
  `devicechain_devicemanagement_calcmetric_device_key_dropped_total` for calculated metrics;
- `devicechain_commanddelivery_command_delivery_window_commands_undefined_total` for commands
  failed because their profile no longer defines them;
- `devicechain_sparkplugingest_presence_probes_total` and
  `devicechain_sparkplugingest_presence_probe_disconnects_total` for the Host's liveness probes;
- `jetstream_consumer_leader_local`, `jetstream_publish_leader_local` and
  `jetstream_consumer_leader_moves_total` for leader placement, and
  `jetstream_stream_leader_changes_total` for a change of a stream's leader.

Every result of the shared processor counters is now exported at zero, so the first discard after
a pod's first scrape is visible to an alert. No metric was renamed or removed.
`jetstream_publish_window_full_seconds_total`, `jetstream_publish_window_full_total` and
`jetstream_publish_duplicates_total` gained a `writer` label, which splits their series; a query
that sums over it reads as before.

#### What else changed {#v0230-what-changed}

##### Known limitations {#v0230-known-limitations}

These were measured on one test cluster: three service nodes of 4 vCPUs and 8 GiB each, broker TLS
on, the default wire format, in runs of 300 to 600 seconds. They describe that cluster, not a
guarantee for yours.

- **MQTT QoS 1 publishes can be acknowledged and then lost, uncounted, while the capture stream
  changes leader under heavy load.** At 12,000 events a second the stream's leader stepped down
  once, and the events published in the five seconds the stream had no leader were lost after the
  devices had their acknowledgement. No counter records the lost events. `JetStreamCaptureLeaderChanged`
  reports that a leader change happened while events were arriving, and its time bounds the window to
  check in your devices' own records. Work to remove this is in progress.
- **The sustained MQTT rate that cluster kept up with was about 6,000 events a second**, bound by
  the broker. At 12,000 a second the backlog grew; above the capture stream's ceiling the oldest
  events are discarded, and that loss is counted and alerted. HTTP ingest on the same cluster
  reached about 14,000 a second.
- **The broker's memory can approach its limit under MQTT-heavy load.** Each server used 3.0–3.2 GB
  of its 3,276 MiB `GOMEMLIMIT`. For an MQTT-heavy install, raise `nats_memory_limit` (4Gi by
  default, set in the instance's `terraform.tfvars` and applied with `dcctl upgrade`; `GOMEMLIMIT`
  follows at 80% of it). See [The message broker](./bootstrap.md#broker-sizing).

##### Also in this release {#v0230-also}

- **Quiet hours on a notification policy.** Weekly slots in an IANA time zone hold notifications
  below a severity you choose, and send them when the quiet stretch ends. See
  [Quiet hours](../guides/notification-channels.md#quiet-hours).
- **A re-raised alarm escalates again.** An alarm that cleared and was raised again used to keep the
  first cycle's acknowledgement, so its second cycle was never escalated.
- **Boards read back what a live feed missed.** After a connection outage, a line chart bound to
  one device, a fleet table, a bar chart with one bar per device and a stat card read back the
  readings sent during the outage, so a chart no longer draws a straight line across the gap. A
  chart of several devices shows a break instead. This lifts most of the known limitation of
  `v0.22.0`.
- **A Send data tab** on the device page writes a `mosquitto_pub` or `curl` command for the
  device's first reading, with placeholders for what the console cannot know, such as the
  instance id, the hosts and a password.
- **`dcctl enable-area`** turns on a functional area on a running instance. There is no command to
  turn one off.
- **Sparkplug.** A connected Host re-probes its asserted devices for liveness every
  `presence.probeSeconds` (1800 by default), so a device whose death certificate never arrived stops
  reading online. A message with more than 16,384 metrics has none of its readings stored.
- **MCP.** Every tool call writes one structured log line, `audit=mcp_tool_call`, with the tenant,
  actor, `client_id`, tool and outcome.
- **Commands.** A command held for a maintenance window stays held when a profile change makes it
  ordinary, and a command waiting for a window that its profile no longer defines is failed with a
  reason, never sent.
- **Event-store migrations** find compressed chunks in a way TimescaleDB 2.29 and later can plan.
- **Several inbound readers per `device-management` pod:** `resolution.inboundReaders`, from 1 to 4,
  defaults to 1.
- **Calculated metrics**, off by default; see [Calculated metrics](#v0230-calculated-metrics).
- **Clustered brokers** can also move consumer leaders beside their readers:
  `infrastructure.nats.affinity.consumerLeaders`, off by default.
- **A re-publish that races its own original** is retried, and no longer counted as a failed
  publish or a lost event.
- **Dashboards load faster.** The chart engine, widget descriptors and catalogs load when first
  needed, so the first download is about 173 kB (gzip) smaller.
- **The console** no longer refreshes its token twice just before it expires, and a member without
  `device:write` sees a device's Basic tab read-only.

### v0.22.0 — dashboards on a square grid, and an event path that waits less {#v0220-upgrade}

`v0.22.0` is an in-place upgrade from `v0.21.0` or `v0.21.1`: `dcctl install` for the cluster, then
`dcctl upgrade` for each instance, as described [for v0.17.0](#v0170-upgrade). It adds no
migration: no service changes its database schema. It includes the four fixes
[`v0.21.1`](#v0211-upgrade) shipped, so coming from `v0.21.0`, read that section too for the one
check it asks of you. Coming from `v0.20.0` or earlier, read the [`v0.21.0` notes](#v0210-upgrade)
as well. An instance built by `v0.16.0` or earlier still has to be
[destroyed and built again](#pre-declaration-recreate).

Most of this release is dashboards: a square-cell grid that adapts to the screen, an editor that
snaps to cells, a board time range with pause, and thirteen new widgets, among them a fleet table
with one row per device. The rest makes the event path wait less: several services keep more
publishes in flight, device-state does less database work per event, and the platform reads 256
messages per pull. What changes for somebody:

- **Dashboards are stored in a new format.** Older boards convert when they load, approximately
  for the layout. Nothing is lost from a widget's settings except a time-series chart's sample
  count. See [Dashboards are stored in a new format](#v0220-dashboard-format).
- **The npm packages `@devicechain/dashboards` and `@devicechain/widgets` have breaking changes**,
  and a `0.21` package draws a board saved by `v0.22.0` wrongly, without an error. See
  [If you build against the npm packages](#v0220-packages).
- **A single-device widget bound to a device group now refuses the group** instead of showing
  whichever device reported last. See [A single-device widget refuses a group](#v0220-group-refusal).
- **Three defaults move:** the fetch batch (128 to 256), device-management's CPU request (800m to
  450m), and how many events external MQTT, Sparkplug and presence keep in flight, which also
  raises how many a pod that dies suddenly can lose on those at-most-once paths. See
  [Defaults that changed](#v0220-defaults).
- **`dcctl upgrade`, `destroy` and `sim` refuse a `--kube-context` that names a different cluster
  than the instance is recorded in.** It used to override the record silently. See
  [dcctl refuses a conflicting `--kube-context`](#v0220-kube-context).
- **Commands held for a maintenance window follow the device type's current window**, and
  unassigning a window releases them at once. See
  [Held commands follow the current maintenance window](#v0220-window-follow).

#### Before you upgrade {#v0220-before}

1. **If an application of yours renders dashboards with the npm packages, plan to move it to
   `0.22.0` before anyone saves a board on the upgraded instance.** Boards stored today keep
   rendering correctly with `0.21`; a board saved or published after the upgrade does not. See
   [If you build against the npm packages](#v0220-packages).
2. **If you read external MQTT or Sparkplug sources and a sudden pod loss must lose as little as
   before**, set their publish windows down before you upgrade. See
   [More publishes in flight](#v0220-publish-windows).
3. **If your `terraform.tfvars` sets `nats_memory_limit` below 4Gi, raise it to 4Gi or remove the
   line.** The upgrade keeps your value, and at 2Gi a server sustains only about 3,000 events a
   second. See [The broker remembers redelivered events for 10 minutes](#v0220-dedup).

#### During the upgrade {#v0220-during}

- **No migration runs**, so the database is untouched and the pods roll as for any upgrade.
- **The console and `/dash` read the new dashboard format as soon as they roll**, because they
  ship with the release. Nothing converts a stored board during the upgrade; each converts when
  it is next loaded.
- **device-management pods are rescheduled with the lower CPU request**, and each NATS server's
  memory limit rises from 2Gi to 4Gi. Those are the two changes to rendered resource values. See
  [The broker remembers redelivered events for 10 minutes](#v0220-dedup).
- **Until event-processing has rolled too, a disabled device's connection change can raise or
  clear a connectivity alarm.** device-management now passes a disabled device's connects and
  disconnects on, and an event-processing pod still on `v0.21.x` does not know to ignore them.
  `dcctl upgrade` rolls the services together, so this lasts only for the rollout.

Going back to `v0.21.x` after the upgrade reads the same database. Dashboards are the exception
to look at: a board saved or published under `v0.22.0` is in the new format, which `v0.21.x` draws
with every widget one cell wide and one cell high, and without the new widgets. Do not edit such a
board in the older console, because saving it there writes that layout.

#### After the upgrade {#v0220-after}

- **Review each board you care about once.** Open it in the editor: a banner says it was
  converted and names any chart whose sample count was dropped. Check the layout, then save, and
  publish a new version if the board is published.
- **Look for widgets that now refuse a group**, and move them to the fleet table or to a bar chart
  with one bar per device.
- **Nothing else is required.** The new settings all have defaults.

##### The broker remembers redelivered events for 10 minutes {#v0220-dedup}

`resolved-events` and `failed-events` now drop a re-published event for 10 minutes, up from the
broker's 2-minute default. Before, an event whose source was redelivered after two failed
publishes could be stored, and counted by detection, twice. The incoming-event stream's window
goes from 30 to 10 minutes. It still covers every redelivery while a consumer runs, and an
`event-sources` or `device-management` pod that comes back within about 8 minutes. If a pod is
down longer, or a broker stores a publish, loses its acknowledgement and then fails publishes for
more than about 8 minutes, the events in flight are stored twice. Run `event-sources` and
`device-management` with 2 or more replicas to avoid the pod case. Each NATS server's memory
limit goes from 2Gi to 4Gi (`GOMEMLIMIT` 1638MiB to 3276MiB), because every server holds each id
inside these windows: about 2.6 GiB at 10,000 events a second sustained. The 768Mi request is
unchanged, so no server is placed differently. `dcctl upgrade` applies the broker first and
restarts its servers, one at a time under `--ha`. Each service then moves the windows on the
existing streams in place on its first start; no stream is recreated and no message is lost.
**If your `terraform.tfvars` sets `nats_memory_limit` below 4Gi, the upgrade keeps your value.**
At 2Gi a server can sustain only about 3,000 events a second before the id tables leave it no
headroom. Raise it to 4Gi, or remove the line, before you upgrade.

#### What changed {#v0220-what-changed}

##### Dashboards are stored in a new format {#v0220-dashboard-format}

A board's definition moves from `schemaVersion` 1 to 3 in two steps, and a board with no
`schemaVersion` is read by its shape. Every place that loads a definition converts it: the editor,
`/dash`, cloning, import, version comparison and rollback. A load never writes the stored
definition back.

**The grid (format 2).** The old grid had fluid columns (24 by default) and fixed-height rows (40
px). The new one has square cells, measured in cells in both directions, and two grid widgets may
not overlap. No exact mapping exists between the two, so the conversion is an approximation:

- column lines scale to the new column count and round, so widgets that met at a line still meet;
- row lines are converted at a reference width of 1366 px;
- the old gap becomes the gutter;
- `z` and the pixel `offset` are dropped, because the grid has no layering;
- whatever overlaps after rounding, or overlapped through `z`, is pushed down until it fits, and
  each widget stays below the widgets it was below.

Every widget is kept, with its type, binding and options, including a widget of a type this build
does not know.

**Chart windows (format 3).** A `timeseries-chart` used to keep a sample count, `options.window`
(300 by default). It now draws a span of time: `windowSeconds`, or the board's time range when it
names none. A sample count has no honest span, because it depends on how often the device reports,
so the conversion drops `window`, and the chart follows the board's time range. That is one hour
unless the board names another, which is the hour every chart already loaded, so what a chart shows
on load does not change. A chart that was meant to be shorter or longer needs a span set by hand.
Other widgets keep their `window`.

**What authors see.** In the editor a converted board shows a banner: "Converted from the previous
grid. Review the layout, then save to keep it in the new format." When charts lost a sample count,
it also names them by title. Save is enabled even with no other edit, and saving writes the new
format and retires the banner. A published version is immutable, so it stays in the old format
and converts each time it is viewed, until you publish a new version. A dashboard file exported
from `v0.21.x` imports and converts the same way.

##### If you build against the npm packages {#v0220-packages}

`@devicechain/dashboards`, `@devicechain/widgets`, `@devicechain/widget-sdk`, `@devicechain/client`
and `@devicechain/brand` publish as `0.22.0`.

**Move to `0.22.0` before boards are saved in the new format.** The `0.21` parser ignores
`schemaVersion` and reads a new placement as a box with no span, so a board saved by `v0.22.0`
renders with every widget one cell wide and one high. It shows no error. Boards stored before the
upgrade keep rendering correctly with `0.21` until they are saved again.

**From `0.22.0` on, a board saved by a newer version is refused instead of drawn wrong.**
`parseDashboardDefinition` throws a `DashboardDefinitionError` with `code: 'schemaTooNew'` and the
version it found in `found`. Check `code` rather than `instanceof`, which fails when an
application bundles two copies of the package. `/dash`, board-replay and the console's detail
page, rollback, import and clone show a message saying to upgrade instead of the board. This
protects `0.22.0` and later viewers from boards saved by a future release only: a `0.21` viewer
still draws a `v0.22.0` board wrongly, so upgrade the packages first.

**`@devicechain/dashboards`** changes the layout model:

- removed: `WidgetBox`, `resolveWidgetBox`, `baseBox`, `setWidgetBox`, `bringToFront`,
  `gridBoxToPx` and `pxToGridBox`. Their replacements are `Placement`, `resolvePlacement`,
  `basePlacement`, `setWidgetPlacement`, `gridPlacementToPx` and `pxToGridPlacement`; there is no
  layering, so nothing replaces `bringToFront`;
- `WidgetLayout` maps a breakpoint to a `Placement`, `Breakpoints` maps a name to a
  `BreakpointSpec`, `CanvasGrid` replaces `gap` and `rowHeight` with `minCellSize`, `gutter` and
  `padding`, and `GridGeometry` replaces its column and row sizes with `cell` and `gutter`;
- `SYNTHETIC_GENERATORS` lists generator ids only. Supply your own labels; stored ids are
  unchanged;
- `parseDashboardDefinition` now refuses overlapping grid widgets. An older definition is
  converted before that check.

**`@devicechain/widgets`:**

- `severityColor(severity)` is now `severityColor(colors, severity)`;
- the `timeseries-chart` option `window` is replaced by `windowSeconds`, and the chart's options
  version moves from 1 to 2.

**`@devicechain/widget-sdk`** removes no export and adds many: size tokens and board density,
fit-to-box text, freshness and the hub connection badge, severity levels and colours, thresholds,
and the board time range (`useWidgetTimeRange`, `MAX_SPAN_SAMPLES`, `withinSpan`), plus new
chart builders on `/echarts`. `useElementSize` gains an optional `step` parameter and
`WidgetFrame` the optional `flush` and `sampleTime` props. Three changes a TypeScript build can
notice:

- `ChartTheme` has a required `severity` field. This matters only if you build a theme by hand
  instead of calling `useChartTheme` or `resolveChartTheme`;
- the unions `WidgetChannel` (now with `aggregate`), `WidgetId`, `BuiltinWidgetId`, `ThemeVar` and
  `OptionSpec` gained members, so an exhaustive `switch` over one of them needs a new case;
- `formatValue` with no precision now rounds to 3 decimals, or 4 significant digits below 1,
  where it used to print the number unrounded.

`SDK_API_VERSION` is unchanged, and the example third-party widget in the repository builds
against `0.22.0` without a change. The `Upgrading from 0.21` section of each package's README,
on [`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards), [`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) and
[`@devicechain/widget-sdk`](https://www.npmjs.com/package/@devicechain/widget-sdk), lists the changes with their replacements.
**`@devicechain/client`** only adds: `createSessionTokens`, and
`update-management` as an area. **`@devicechain/brand`** is unchanged.

##### A single-device widget refuses a group {#v0220-group-refusal}

The measurement stream keyed its latest values by measurement name. Bound to a device group, every
device that reported `temperature` overwrote the others, so a card, gauge, chart or table showed
whichever device spoke last as though it were the answer. Those widgets now refuse a group with a
sentence that points to the fleet table, or to the bar chart's one-bar-per-device mode. This
covers the latest-value card, the gauges, the time-series chart, the table, the pie chart, the
stat card and a bar chart in its per-measurement mode. The editor offers a group only to the fleet
table, the bar chart, the alarm widgets and the map.

##### dcctl refuses a conflicting `--kube-context` {#v0220-kube-context}

`dcctl upgrade` with a `--kube-context` that names the instance's own recorded cluster now renders
exactly the same instance as without the flag. Before, it dropped the local blob-storage
colocation setting, and the chart refused the render.

A `--kube-context` that names a **different** context than the one recorded for the instance is
now refused by `dcctl upgrade`, `dcctl destroy` and `dcctl sim`. It used to override the record
silently. Drop the flag to use the recorded cluster. If the record is what is wrong, because the
cluster was rebuilt or its context renamed, remove
`~/.devicechain/instances/<instance>/instance.json` and re-run with `--kube-context`.

##### A disabled device's connection status keeps updating {#v0220-disabled-presence}

A disabled device's connects, disconnects and demotions now reach device-state and the event
history, so its connection status stays true while it is disabled. Detection still ignores
them, so no alarm fires for a disabled device, and its measurements, locations and commands are
still refused. A Sparkplug edge node can make a disabled child device show as connected, because
presence the node reports for its devices is trusted by design. A new counter, `devicechain_eventsources_presence_reconcile_unapplied_total`,
counts reconciliation repairs that were sent but not seen to apply.

##### Held commands follow the current maintenance window {#v0220-window-follow}

A command held for a maintenance window now follows its device type's **current** window. If you
move a type from window A to window B, the commands already waiting move to B's next opening.
If you **unassign** a type's window, its waiting commands are **released at once**. The
`v0.21.0` notes said they stayed held; that is no longer true. A disabled device's commands
stay held either way. Before you unassign a window, check what is waiting on it.

Also fixed: a disabled device's queued commands no longer keep the LwM2M sender polling in a
tight loop, and a command whose confirmation is refused because its window has just closed now
parks for the next opening instead of being left as `SENT`. A claim refused because the device
is disabled is counted on `devicechain_commanddelivery_command_delivery_claims_refused_disabled_total`.

##### Defaults that changed {#v0220-defaults}

###### More publishes in flight {#v0220-publish-windows}

On a replicated broker an acknowledgement takes about 8 to 11 ms, and a path that waits for each
one before sending the next is capped by that. Five paths now keep a window of publishes in
flight, each with a setting. Each is read at startup; a value outside 1 to 1024, or an explicit
`0`, stops the service.

| Setting | Service | Default | Before |
| --- | --- | --- | --- |
| `resolution.publishWindow` | `device-management` | 512 | 128, fixed |
| `capture.publishWindow` | `event-sources` | 512 | 128, fixed |
| `externalMqtt.publishWindow` | `event-sources` | 128 per source | 5 per source, about 440 events a second |
| `brokerPresence.publishWindow` | `event-sources` | 128 | one per subject, about 88 transitions a second |
| `ingest.publishWindow` | `sparkplug-ingest` | 32 per connection | one, about 88 messages a second |

The first two read from streams that redeliver, so a deeper window costs only memory. The last
three are at-most-once: the external broker, or the presence notice, is not replayed. A pod that
stops cleanly, or hands a source to another pod, waits up to 8 seconds for what it has in flight.
**A pod that dies suddenly loses up to a window's worth on top of what it lost before**: up to 128
events per external MQTT source (it was 5), 32 messages per Sparkplug connection (it was 1), and
128 presence transitions, which the next reconciliation pass repairs. If that matters more to you
than the rate, set the window lower. The settings are described in
[Observability](observability.md) and [Edge services](edge-services.md).

###### The fetch batch is 256 {#v0220-fetch-batch}

When `infrastructure.nats.fetch.batch` is not set, a service now pulls 256 messages at a time
instead of 128. A value you set still wins. In GKE measurements device-management, which was
limited by the round trip of each pull, resolved 10 to 12% more events per second at the larger
batch. Each message's acknowledgement window still starts when it is delivered, so a reader that
handles messages one at a time now has about 234 ms per message, not 469 ms, before the last one
in a full batch is redelivered. A redelivered message is handled again, not lost. If you run a
reader that is that slow, set `batch: 128`. See [Fetching ahead](observability.md#fetch-ahead).

###### device-management requests less CPU {#v0220-dm-cpu}

`functionalAreas.device-management.measuredRequests.cpu` falls from 800m to 450m, which is
device-management's measured use at 6,000 events a second on this release. The CPU limit stays at
2 cores. A value you set in your own values is kept.

##### device-state does less work per event {#v0220-device-state}

A batch of plain data events now writes all of its device rows in one upsert instead of a locked
read and a write-back per batch. The queue between device-state's reader and its writers is sized
from the writer pool (640 at the defaults, was 100), so the writers stay busy when the service is
behind. The writers also take row locks in one order, which removes a rare deadlock between a
writer creating device rows and one claiming presence on the same devices. At 6,000 events a
second, device-state used 21% less CPU per event than on `v0.21.0`; part of that is the
database-read fix `v0.21.1` already shipped.

##### Measured on GKE {#v0220-performance}

On the same GKE cluster shape as the previous round of measurements, a build with this release's
device-state, device-management and event-sources changes sustained 10,000 events a second for 10
minutes through every stage, with service placement held, and stored each of the 6 million
events exactly once, checked event by event (see
[Measured throughput](./bootstrap.md#measured-throughput)). That is
the same sustained rate as before, not a higher one: with ingest now faster, device-management's
read side became the first stage to fall behind, at about 10,300 events a second for a single
replica. The 256 fetch batch is the change aimed at it, and gave 12% more in a 3-minute run. Across
the five event-path services, CPU at 10,000 events a second fell 6% against that round, which ran
before the `v0.21.1` database-read fix.

##### New alerts and metrics {#v0220-observability}

New alerts, with `metrics.alerts` on:

- `ExternalMqttSourceLosingEvents`: an external MQTT source's events failed to publish for 10
  minutes, so they are lost;
- `BrokerPresenceLosingTransitions`: presence transitions failed to reach the event stream for 10
  minutes;
- `DeviceCredentialCacheTooSmall` and `DeviceLookupCacheTooSmall`: more than 5% of
  device-management's lookups evicted another entry from a full cache for 15 minutes;
- `DetectRuleStatWriteFailing`: the rule-health fire counters cannot be written. Detection itself
  is unaffected.

New metrics: `devicechain_<area>_jetstream_publish_window_full_seconds_total`,
`devicechain_<area>_jetstream_publish_window_full_total` and
`devicechain_<area>_jetstream_publish_duplicates_total` for the publish windows above,
`devicechain_eventsources_total_msg_publish_failed{source}` for external MQTT, and
`devicechain_eventprocessing_detect_rule_stat_write_failures_total`, and `devicechain_eventsources_presence_reconcile_unapplied_total`. No metric was renamed or
removed.

##### Fixes from an end-to-end review {#v0220-review-fixes}

- **Live widgets recover after an outage of any length.** Before, after about 40 seconds without
  the network or the server, they showed "Data unavailable" until the page was reloaded, and a
  stream the server ended could stop updating while the badge still read live. A widget whose code
  failed to download can now be retried with **Reset widget**.
- **Dashboard data shows what it says.** A slot pointed at another device while the board is
  paused shows the new device. Bars with one bar per device keep updating. A stat card with no
  device says "Choose a device for this widget" instead of "0 Readings". A fleet-table cell for a
  device that reports rarely never shows a span's average as its latest reading, and a cell past
  the read limit says "not loaded".

##### Known limitation: history after a reconnect {#v0220-known-limitation}

When a board reconnects, the history it missed is not read again. A line chart draws a straight
line across the outage, and fleet and aggregate figures leave out the readings sent during it.
Fixed in the next release: a board reads the outage back from stored history when it reconnects
(see [after a reconnect](../concepts/dashboards.md#after-a-reconnect)).

##### Also in this release {#v0220-also}

- **Dashboards.** Cells stay square and the column count follows the container, so a phone shows
  one widget per row and a wall display gets bigger cells. The editor snaps to cells, pushes
  neighbours aside, works from the keyboard, can anchor a widget to another, and shows layout
  problems on each widget; a layout error blocks publishing until **Fit to limits** clears it. It
  also has undo and redo, duplicate, and copy and paste between boards; a widget pasted where its
  device binding does not exist, such as another tenant's board, gets a new unbound slot and keeps
  no device reference. A
  board has a time range (15 minutes to 7 days) and a pause; commands still send while paused.
  New widgets: linear and level gauges, battery, status light, signal bars, compass, progress
  ring, command slider and toggle, stat card, bar and pie charts, and the fleet table. Widgets
  gain thresholds, units and precision, show how old their value is, and say why they are empty.
  `/dash` keeps its session alive and has a kiosk mode for wall displays.
- **Over-the-air updates in the console:** Artifacts and Signers pages, and an Updates tab on the
  device page. GraphQL adds `Artifact.signatures`.
- **Tier limits.** The tier form sets the held-command ceiling, the shed priority and the three
  geofence limits, and `AdminTenant.effectiveScalarSettings` reports where each value comes from.
- **The console user guide** has 17 chapters in English, Spanish and Chinese.
- **The alarm table wraps alarm keys and originators at hyphens** (`engine-` / `overheat`)
  instead of in the middle of a word.
- **board-replay no longer ships Vite's module-preload polyfill.** On Safari before 17 and Firefox
  before 115, the shared chunks of a widget that loads on first use arrive one round trip later.
  Nothing else changes.

### v0.21.1 — a patch, nothing to do {#v0211-upgrade}

`v0.21.1` is a drop-in upgrade from `v0.21.0`: `dcctl install` for the cluster, then `dcctl
upgrade` for each instance, as for [v0.21.0](#v0210-upgrade). It adds no migration, so the
database is untouched, and it changes no API, topic, permission, chart value or configuration
key. Everything the v0.21.0 section below describes is still exactly what you are running.
Coming from `v0.20.0` or earlier, follow the [v0.21.0 notes](#v0210-upgrade) first; they apply
unchanged.

Four fixes:

- **Database reads cost what they did before v0.21.0 again.** v0.21.0 was the first release built
  with the Go 1.27 toolchain, and with it the PostgreSQL driver did extra type inspection for
  every column of every row it read. Services that read a lot, device-state above all, spent
  noticeably more CPU on ingest as a result. Rows are now read the way they were on `v0.20.0`,
  and device-state's ingest throughput is restored. In a scan-heavy benchmark the same read went
  from 85–95 ms to 31–35 ms.
- **The Rule Health view counts fires on PostgreSQL.** Until now it showed no fires and no
  last-fired time for every rule, because PostgreSQL refused the statement that records a fire.
  Detection itself was not affected: rules raised and cleared alarms normally throughout. Counts
  start from the upgrade; fires before it were never recorded.
- **Creating an identity with `enabled: false` creates a disabled identity.** Before, it was
  stored enabled. The same fix covers the other records that carry an enabled flag. An identity
  created disabled under `v0.21.0` or earlier is still enabled: check any you meant to create
  disabled, and disable them.
- **Stopping the HTTP event source waits for its connections to finish closing**, so a stopped
  source no longer updates its metrics after it has stopped.

### v0.21.0 — a device can be shut out whole, and updates are signed {#v0210-upgrade}

`v0.21.0` is an in-place upgrade from `v0.20.0`: `dcctl install` for the cluster, then `dcctl
upgrade` for each instance, as described [for v0.17.0](#v0170-upgrade). Coming from `v0.19.0` or
earlier, read the [`v0.20.0` notes](#v0200-upgrade) as well: their row and chunk counts and their
steps apply to this upgrade too. An instance built by `v0.16.0` or earlier still has to be
[destroyed and built again](#pre-declaration-recreate).

Most of this release is new capability that changes nothing until you use it: a device can be
disabled as a whole, update artifacts can be signed, a fleet can be imported from a CSV file,
alert presets and a notification check join the detection-rule form, and a single sign-on
connection can trust a private certificate authority. What changes for somebody: a device built to
understand only unsigned update assignments refuses a signed one, an artifact can be assigned only
when an active signer has signed it, a raw LwM2M Factory Reset is refused, a disabled device is
cut off from configuration, updates and LwM2M registration with its queued commands held, and the
console's built files move to a new path. Every binary is now built with Go 1.27.2.

#### Before you upgrade {#v0210-before}

1. **Devices that take over-the-air updates: ship an updater that verifies signatures first.** An
   assignment can now carry `trust: "SIGNED"` with a signature, a key id and an algorithm, and a
   device that understands only `UNVERIFIED` refuses it. Nothing signs an assignment until a
   tenant registers a signer and signs an artifact, so deploy the verifying updater before you do.
   See [Update assignments can be signed](#v0210-signed-updates).
2. **Decide how update artifacts are trusted.** With `updates.allowUnverifiedArtifacts` left at
   `false`, only an artifact signed by an active signer of its tenant can be assigned. If you set
   the flag to assign unsigned artifacts, it still applies. See
   [Update assignments can be signed](#v0210-signed-updates).
3. **Stop sending a raw LwM2M Factory Reset.** A command that executes `/3/0/5` is refused when it
   is enqueued, from a rule or a batch too. See
   [A raw LwM2M Factory Reset is refused](#v0210-factory-reset).
4. **Check what depends on the console's static path.** Its built files are now served from
   `/_app/` rather than `/assets/`. See [The console's built files move](#v0210-console-app-path).

#### During the upgrade {#v0210-during}

- **Upgrade `device-management` and `command-delivery` together.** `command-delivery` keeps its own
  record of which devices are disabled and fills it from `device-management`. A tenant's commands
  are not dispatched until that tenant's first successful sync, so a new `command-delivery` pod
  that cannot yet reach a `device-management` pod with the new query holds them. They are not
  lost, and they go out once the sync succeeds.
- **Four areas apply a migration** on their first start: `device-management` adds a nullable
  `disabled_at` column and a partial index to the devices table, the maintenance-windows table, a
  window column on device types and an impact column on command definitions; `command-delivery` creates two small tables and adds outcome,
  window and retry columns to its commands and batches; `update-management` creates the signer
  registry; and `user-management` adds a CA-bundle column to the identity providers. Each is
  individually re-runnable, and none needs a recreate. See
  [New tables and columns](#v0210-migrations).
- **`command-delivery`'s migration rewrites existing rows.** It fills the new `outcome` column for
  every command that has already finished, in one `UPDATE` over the commands table. A successful
  command is recorded as succeeded, an expired or cancelled one as not delivered, and a failed or
  timed-out one as unknown, because the old row does not say which it was. On a large commands
  table this makes the first start of `command-delivery` take longer, in proportion to the number
  of finished commands, and PostgreSQL writes a new version of each of those rows, so expect
  write-ahead log and dead rows for autovacuum on the same scale. The statement only writes rows
  whose `outcome` is still empty, so running it again never overwrites a value. A pod's startup
  probe allows about 150 seconds by default (`startupProbe` in the chart values); if your table is
  large enough for the backfill to take longer, raise `startupProbe.failureThreshold` for the
  upgrade, or the container is restarted before the migration finishes and starts it again. The
  other migrations in this release add tables, columns and indexes and write no rows.
- **`update-management` starts reading the device roster** from new facts only, so it needs no
  catch-up and no operator step.

Going back to `v0.20.0` after the upgrade keeps the new columns and tables, which `v0.20.0`
ignores. It does not know signers, so `allowUnverifiedArtifacts` alone decides what it can assign.

#### After the upgrade {#v0210-after}

- **Reload the console once.** A browser holding the previous `index.html` asks for the old file
  paths until it reloads. `index.html` is served with `no-cache`, so that is one navigation.
- **Give custom roles the new authority if they manage signers.** Tenant admins hold
  `artifact:trust`; a role you defined does not. See
  [Update assignments can be signed](#v0210-signed-updates).
- **Nothing else is required.** Disabling a device, signing, importing, presets and the CA bundle
  all start unused.

#### What changed {#v0210-what-changed}

##### A disabled device is cut off whole, and its queued commands are held {#v0210-disabled-device}

A device can be disabled as a whole instead of finding and revoking each credential, with
`updateDevice(enabled: false)` or the Disable action on the device page. While it is disabled:

- its events are refused at resolution, its broker connects are refused, and new commands for it
  are refused with `DEVICE_DISABLED`;
- its configuration sync and status reports are refused, as are its update progress and inventory
  reports, its download grants and its downloads, and a download link issued before the disable
  answers 403; `assignArtifact` refuses it with `DEVICE_DISABLED`;
- an LwM2M registration answers 4.03 Forbidden, and a registered device is ended at its next
  Update so that it re-registers and is refused;
- its absence rules are disarmed, so no "device silent" alarm is raised or kept for it;
- commands already queued are **held**: they are neither sent nor cancelled. A held command still
  lapses by its own TTL. Enabling the device releases them oldest first, and deleting a disabled
  device cancels them. `deviceCommandHold` and `cancelDeviceCommands` show and clear them.

Enabling the device sends its latest configuration once and re-offers its current update
assignments. An already open session stays open until the connect is next checked. See
[Disabling a device](../guides/device-credentials.md#disabling-a-device).

If you disable devices today and rely on their queued commands going out, they now wait.

##### Update assignments can be signed {#v0210-signed-updates}

A tenant can register the public keys it trusts to sign update artifacts: ECDSA P-256 or Ed25519,
as a single `PUBLIC KEY` PEM. A signature over an artifact's SHA-256 digest is verified when it is
added, and an artifact counts as **signed** while at least one active signer of the same tenant has
signed it. Revoking a signer, which is terminal, returns its artifacts to unverified without
touching them. Anything containing a private key is refused.

What this changes:

- **The new authority `artifact:trust`** registers and revokes signers. It is held by tenant
  admins and by no role you defined; `artifact:write` adds a signature.
- **Assignment needs a signed artifact**, unless `updates.allowUnverifiedArtifacts` is `true`. That
  setting is unchanged and still `false` by default; what moves is that a signed artifact no longer
  needs it.
- **The device contract grows.** An `update.assignment` for a signed artifact carries
  `trust: "SIGNED"`, a `signature` (standard base64), a `keyId` (the lowercase hex SHA-256 of the
  key's DER-encoded public key) and an `algorithm` (`ECDSA_P256_SHA256` or `ED25519`); both sign the
  raw 32-byte digest. `"v":1` is unchanged and an unsigned assignment is byte-identical to before.
  A device that understands only `UNVERIFIED` refuses a `SIGNED` assignment, which fails closed.
  A device reports `UNKNOWN_SIGNER` or `SIGNATURE_INVALID` under `FAILED` at the downloaded stage.
- **A signature does not bind the version.** Signing the digest says who produced the bytes, not
  which version they are; see [Over-the-air updates](../concepts/over-the-air-updates.md).

When an artifact is assigned, the oldest signature by an active signer is **pinned** to the
assignment (`UpdateAssignment.signedBy` names the signer), and that signature is what the device
receives. Two things follow:

- **A device must hold the key of every signer that could be pinned.** Put a new signer's key on
  your devices before you revoke the old one. Revoking a signer after the pin does not change a
  message already sent.
- **Turning `allowUnverifiedArtifacts` off stops unsigned assignments from being sent**, including
  a re-send when a device syncs. An assignment made under `v0.20.0` has no signature, so with the
  setting off its device keeps what it has until you assign a signed artifact. An assignment whose
  pinned signature cannot be read is not sent; it is never sent as `UNVERIFIED`.

To register a signer and sign an artifact, follow [Signing updates](../guides/signing-updates.md).

##### A raw LwM2M Factory Reset is refused {#v0210-factory-reset}

An `lwm2m.execute` on the Device object's resource 5 (`/3/0/5`) is refused at enqueue with
`COMMAND_DESTRUCTIVE_REFUSED`, for a single command, a batch and a command sent by a detection rule,
in every spelling of the path, and again before it reaches the device. Detection rules treat the
code as permanent and do not retry. Reboot (`/3/0/4`), other executes and reads are unaffected. A
confirmed, single-device named operation for Factory Reset is planned; until then the raw route is
closed. The executor also refuses any read, write or execute payload whose path key appears more
than once.

##### Batches can be retried by outcome, and an LwM2M request with no answer is a TIMEOUT {#v0210-batch-retry}

Every command that finishes now carries a stored `outcome`, written by the write that finished it:
`SUCCEEDED`, `NOT_DELIVERED` (the platform can prove it never reached the device), `REJECTED` (the
device said it did not do it) or `UNKNOWN` (it may have reached the device and acted). A migration adds
the column and fills it for the commands already finished: `SUCCESSFUL` becomes `SUCCEEDED`, `EXPIRED`
and `CANCELLED` become `NOT_DELIVERED`, and every other finished command, including one that
failed or timed out, becomes `UNKNOWN`. `commands` can be searched by `outcomes`.

`retryCommandBatch` issues a new batch, linked both ways to the original (`retryOf`, `retries`), for the
devices you name that did not succeed. The command is copied from the original, never retyped. Devices
with an `UNKNOWN` outcome are refused with `RETRY_UNKNOWN_NOT_ACKNOWLEDGED` unless the request sets
`includeUnknown`, so a command from before the upgrade that failed or timed out needs that flag to be
retried. A device that already has a live or successful command anywhere in the retry family is refused,
so no device is sent a command twice because two retries were open. It needs `command:write`. The batch
page in the console has a **Retry…** action. See
[Commanding a fleet](../guides/commanding-a-fleet.md#retry-the-devices-that-did-not-succeed).

**Behaviour change.** An LwM2M request that gets no answer from the device is now reported as `TIMEOUT`,
as on the other transports. If you read an LwM2M command's status to decide what to do next, expect
`TIMEOUT` there.

##### Disruptive commands can wait for a maintenance window {#v0210-maintenance-windows}

A device type can be given a recurring weekly **maintenance window**: a time zone and 1 to 56 weekly
slots, managed in the `device-management` API (`device:write`). Each command definition gains an
`impact` of `NORMAL`, `DISRUPTIVE` or `DESTRUCTIVE`; a definition that already exists reads as `NORMAL`,
and `lwm2m.reboot` is created `DISRUPTIVE` from now on. Nothing is gated until a window is assigned to a
device type and a command of that type is `DISRUPTIVE` or `DESTRUCTIVE`.

Then, for a device of that type, such a command enqueued while the window is closed is accepted and
**waits**, still `QUEUED`, until the next opening. One enqueued while the window is open is bound to that
opening: if it has not been sent when the window closes, it waits for the next one rather than going out
whenever the device returns. This holds for a single command, a batch and a command sent by a
detection rule, and on every path that sends a command or confirms one in flight. A command whose
`expiresAt` falls before the next opening is refused with `WINDOW_AFTER_EXPIRY`, in a batch as a
per-device refusal. A batch reports how many of its commands are `waiting`.

Two behaviours to know. A device that confirms a command in flight after the window has closed has the
confirmation refused; the command then parks and moves to the next opening. And unassigning a window from
a device type does not release commands already stamped from it.

To set one up, see [Maintenance windows](../guides/maintenance-windows.md).

##### The console's built files move to `/_app/` {#v0210-console-app-path}

A link to an asset's page (`/assets/<token>`) returned 404 on load or refresh, because the console's
built JavaScript and CSS lived under `/assets/` and the web server answered 404 for anything there
it did not hold. The built files are now under `/_app/`, with the same one-year immutable cache, and
the asset page loads from a direct link. The dashboard app under `/dash` is unchanged. If you proxy,
cache or allow-list the console's paths, allow `/_app/`.

##### A missing record is answered with `NOT_FOUND` {#v0210-not-found}

A request that names a role, tenant tier, identity, membership, tenant, OAuth client, identity
provider or SSO link request that does not exist, or a device `claimDevice` cannot find, now
answers with `extensions.code` `NOT_FOUND`, as the [GraphQL reference](../reference/graphql-api.md)
says it does. It used to carry only a message such as `role not found` and no code. The messages are
unchanged, so a client that matched the message still works; a client that treated a missing code as
"other error" should handle the code.

##### Go 1.27.2 accepts event times written with JSON escapes {#v0210-go127}

Every binary is built with Go 1.27.2. A device that wrote an event time with JSON escapes in the
string, such as `"2026-10-13T12:00:00Z"` for `...Z`, had the event dead-lettered when decoded
by Go 1.26. It is correct JSON, and it is now accepted and stored. Nothing needs doing.

##### New tables and columns {#v0210-migrations}

| Area | Change |
| --- | --- |
| `device-management` | `devices.disabled_at timestamptz` (null means enabled) and the partial index `idx_devices_disabled`; the `maintenance_windows` table, `device_types.maintenance_window_id` and `command_definitions.impact` |
| `command-delivery` | `device_gates` and `device_gate_syncs`, with their unique and partial indexes; `commands.outcome` (filled for finished commands), `command_batches.retry_of` and a partial index on it; `commands.dispatch_after`, `dispatch_until` and `window_token` with a partial index, and `command_batches.waiting` |
| `update-management` | `artifact_signers` and `artifact_signatures` (public keys and signatures only); artifact trust is now derived from them, and the old stored trust column is kept, written no more, so a rolling upgrade is safe; each assignment also gains a nullable `signature_id`, the signature it is pinned to |
| `user-management` | `ca_bundle` on identity providers |

##### Single sign-on can trust a private certificate authority {#v0210-sso-ca}

An identity-provider connection can carry its own CA bundle, set at create or with
`setIdpCaBundle` (an empty string clears it; both need `user:write` on the instance-admin API). Calls
to that provider trust the system roots plus the bundle, only for that connection, and still go
through the outbound guard. Discovery must succeed with the new bundle before it is saved. See
[Configuring single sign-on](../guides/configuring-sso.md). Nothing changes for a connection without
a bundle.

##### Other behaviour worth knowing about {#v0210-other}

- **Fleet import.** `importDevices` takes up to 1000 rows and answers each as `CREATED`, `EXISTS`,
  `CONFLICT`, `INVALID` or `REFUSED`; it only creates, so running a file again is safe, and a dry
  run writes nothing. It needs `device:write`.
- **Device configuration.** `restoreDeviceConfiguration` and `copyDeviceConfiguration` re-apply an
  earlier revision or another device's configuration through the device's declared shared
  attributes, minting a new revision. They need `device:write` and add the codes
  `CONFIGURATION_KEY_UNDECLARED` and `CONFIGURATION_CONFLICT`.
- **Thing Model export.** A published profile version is available as a W3C Web of Things Thing
  Model through `DeviceProfileVersion.thingModel`.
- **LwM2M.** `lwm2m.reboot` and `lwm2m.readDeviceInfo` are named operations, and a device's
  manufacturer, model, serial number, firmware version and battery level are read when it
  registers (at most once an hour) and kept as client attributes.
- **Detection rules.** The console form offers presets for a silent device, a value held above a
  limit and a low battery, and shows whether the rule's severity reaches anyone. An absence alarm
  clears when its device is disabled, deleted or given another type.
- **Messaging.** Two replicas that migrate a stream's retention at the same time no longer make one
  refuse to start.
- **Batch retry.** `retryCommandBatch`, the `outcome` on every finished command and the `outcomes` search are described under [Batches can be retried by outcome](#v0210-batch-retry).
- **Maintenance windows.** Windows, `impact` and the hold are described under [Disruptive commands can wait for a maintenance window](#v0210-maintenance-windows).

### v0.20.0 — sign in through your own identity provider; widgets ship as packages {#v0200-upgrade}

`v0.20.0` is an in-place upgrade from `v0.19.0`: `dcctl install` for the cluster, then `dcctl
upgrade` for each instance, as described [for v0.17.0](#v0170-upgrade). Coming from `v0.18.0`, read
the [`v0.19.0` notes](#v0190-upgrade) as well: their row and chunk counts and their steps apply to
this upgrade too. An instance built by `v0.16.0` or earlier still has to be
[destroyed and built again](#pre-declaration-recreate).

Most of this release is new capability that changes nothing until you use it: people can sign in
through your own OpenID Connect identity provider, dashboards can show widgets from your own
packages, devices can receive configuration, and over-the-air updates arrive as a first slice. What
changes for everyone: every user signs in again once, the rate ceilings are renamed after what they
count, X.509 device credentials are refused, a device password is kept only as a one-way digest,
dashboards published before the upgrade have to be published again, and many API inputs gain a
bound. The services that handle every event run with a tuned garbage collector, and
`device-management` keeps its lookups for minutes rather than seconds.

#### Before you upgrade {#v0200-before}

1. **Rename the rate ceiling keys** in any service `config` you set:
   `ingestRateLimit.messagesPerSecond` becomes `ingestRateLimit.readingsPerSecond` (`event-sources`,
   `lwm2m-ingest`, `sparkplug-ingest`) and `outboundMessagesPerSecond` becomes
   `outboundCallsPerSecond` (`event-processing`, `outbound-connectors`). A service that still finds
   an old key refuses to start. Scripts that read or set tenant ceilings through the API need the
   new field names too. See [The rate ceilings are renamed](#v0200-rate-key-rename).
2. **Move devices off X.509 credentials.** A credential of type `X509_CERTIFICATE` stops
   authenticating at the upgrade. Give each such device an `ACCESS_TOKEN` or `MQTT_BASIC` credential
   first. See [X.509 device credentials are no longer accepted](#v0200-x509).
3. **On an S3 object store, grant the credential `s3:ListBucket`** on the bucket, or a deleted
   tenant's erasure does not complete. See
   [Deleting a tenant now erases everything stored under its object-store prefix](#v0200-object-prefix-erase).
4. **On a large event store, consider building two indexes first.** The first start of
   `event-management` builds two indexes over the base events not yet compressed, and refuses,
   changing nothing, when there are more than 4,000,000 rows to index or a table has more than
   500 chunks. Both sections give the statements that build the index by hand, before or after the
   upgrade: [A device's event list reads only its own tenant's rows](#tenant-device-index) and
   [Two devices can now send the same `altId` at the same instant](#device-alt-id-key).
5. **If you set `gogc` or `goMemLimitPercent` at the top level of your chart values**, the five
   services that handle every event now set their own, which win. See
   [The event-path services run with a tuned garbage collector](#v0200-gogc).
6. **Check `device-management`'s memory for a large fleet.** Its lookups are now kept for 5 minutes,
   and each replica needs room for the whole active fleet. See
   [`device-management` keeps its lookups for minutes](#v0200-resolve-caches).
7. **Stop anything else that reads the `device-events-capture` stream**, such as `nats stream view`.
   It now accepts only the consumer `event-sources` owns. See
   [The device intake stream deletes each message once it has been forwarded](#v0200-capture-stream).
8. **Recreate any identity whose email address has a non-ASCII domain** (`user@bücher.example`), or
   is not a valid `local@domain`. After the upgrade it cannot sign in until an administrator
   recreates it. Delete it, create it again with the domain in its `xn--` form
   (`user@xn--bcher-kva.example`), and restore its memberships. See
   [An email's domain now has one spelling](#v0200-email-idna).
9. **Check your API clients and the packages you build against** against
   [Requests that are now refused](#v0200-input-bounds),
   [Dashboards: publishing makes a version live](#v0200-dashboard-publishing) and
   [The published packages](#v0200-packages).

#### During the upgrade {#v0200-during}

- **Device passwords are converted.** The first start of the new `device-management` replaces every
  stored `MQTT_BASIC` password with a one-way digest. Until every `device-management` pod runs the
  new release, an older pod finds no password for a converted credential, so it refuses that
  device's MQTT connects and the password checks on its events; the device retries and reaches a new
  pod. A password set on either version during the rollout is the one that survives.
- **Upgrade `event-sources` and `device-management` together.** While they run different versions,
  events that carry a device password or access token in their body can be refused. See
  [Events that carry a device password or access token in the body](#v0200-body-password).
- **Avoid changing tenants or tiers until the rollout finishes.** An older ingest or outbound pod
  meters every tenant at the platform default, and an older `user-management` pod fails the tenant
  reads and writes it serves, until it is replaced. See [The rate ceilings are renamed](#v0200-rate-key-rename).
- **`event-management` builds two indexes** on its first start; writes of base events wait while
  each builds, and reads do not.
- **`event-sources` forwards what the device intake stream holds, then recreates it.** If the stream
  is not empty within five minutes, `event-sources` stops with an error and deletes nothing.
- **Detection moves to a new consumer of the resolved events.** See
  [Detection reads its events through a new consumer](#v0200-ack-floor).

Going back to `v0.19.0` after the upgrade leaves every device that uses an `MQTT_BASIC` password
unable to authenticate, because `v0.19.0` cannot read a digest; set new passwords and load them onto
those devices if you do. The index and stream changes above each say what going back needs.

#### After the upgrade {#v0200-after}

- **Everyone signs in again once**, and OAuth clients such as MCP agents authorize again. See
  [Signing out now ends the session on the server](#v0200-server-side-logout).
- **Publish again every dashboard that viewers should see.** Until then, viewers are told it is not
  published. See [Dashboards: publishing makes a version live](#v0200-dashboard-publishing).
- **New alerts may fire.** `SafetyGateDisabled` fires for an optional safety gate a service runs
  without. The others are listed under [New alerts](#v0200-alerts).
- **An instance bootstrapped before this release has no object store, and the upgrade does not add
  one.** Tenant logos and update artifacts need one. See
  [`dcctl bootstrap` can give an instance an object store](#v0200-object-store).

#### What changed {#v0200-what-changed}

##### Signing out now ends the session on the server, and everyone signs in again once {#v0200-server-side-logout}

Signing out of the console or the embedded dashboard app used to clear only the browser: the refresh token stayed valid for up to 7 days, and the sign-in token the console keeps could still start a new session for up to 15 minutes. A new `logout` mutation ends the sign-in on the server. From that moment the sign-in's exchanges are refused: its refresh tokens no longer refresh, in any tenant, its sign-in token can no longer choose a tenant, and an OAuth grant authorized during that sign-in no longer refreshes. The MCP authorization page signs in on its own, so a grant made there is not ended by signing out of the console; it ends when that sign-in runs out (7 days by default). Other sign-ins of the same account are not affected. Access tokens and sign-in tokens already issued, including on the admin API, cannot be recalled and stay valid until they expire, within 15 minutes. If the server cannot be reached when a user signs out, the app still signs them out locally and tells them the server-side session could not be ended.

Each sign-in is now a record that lasts as long as a refresh token (7 days by default), so a session that keeps refreshing ends 7 days after the sign-in and the user signs in again. That includes an OAuth grant such as an MCP agent's: it ends 7 days after the sign-in on the authorization page, and the client authorizes again.

Tokens issued before the upgrade carry no sign-in record and are refused, so **everyone signs in again once**: console, dashboard app, SDK and OAuth sessions end at their next refresh, within 15 minutes of the upgrade unless you changed the access-token lifetime. OAuth clients such as MCP agents authorize again. Nothing else needs doing.

##### Events that carry a device password or access token in the body are affected while versions are mixed {#v0200-body-password}

From this release, `event-sources` and `device-management` pass a device password or access token that
arrives in an event body in a different form, and each needs the new version to read the other's. While
old and new versions of the two run side by side during a rolling upgrade:

- Events that carry a device password (`MQTT_BASIC`) in the body, over HTTP or MQTT, are refused as
  unauthenticated, and they are not replayed.
- Events that carry an access token (`ACCESS_TOKEN`) in the body are refused under
  `deviceAuthMode: required`. Under `deviceAuthMode: optional` they are accepted on the device token they
  name, without their credential being checked.
- MQTT connects are not affected.

Upgrade `event-sources` and `device-management` together, or accept the brief loss of those events.

##### An `MQTT_BASIC` device password is shown once, and cannot be shown again {#v0200-device-password-show-once}

The platform now keeps only a one-way, keyed digest of an `MQTT_BASIC` device password, not the password. The API never returned the password on read, so no query result changes. What changes is that nobody can recover it from the platform either: the console shows a password once, right after you add the credential or set a new one, with a copy button, and afterwards only says that a password is set. Its credential row gains a **Set new password** action, and the form offers **Generate strong password**.

Existing passwords are converted to digests the first time device-management starts on the new release, and devices keep authenticating with the password they already use. A password you did not record cannot be displayed afterwards; set a new one and load it onto the device. See [Show-once passwords and rotation](../guides/device-credentials.md#show-once).

Nothing needs doing.

##### A device's event list reads only its own tenant's rows {#tenant-device-index}

The index that serves a device's event list now starts with the tenant. In `v0.19.0` the list's
total, and a device's list filtered by event type, visited every row with that device's token that
was not yet compressed, including the rows of a device with the same token in another tenant, so a
busy `gateway-1` in one tenant slowed the list of `gateway-1` in another. They now visit only the
device's own rows, and a SQL or BI query on a device's events gains the same. Each stored base event
still updates the same number of indexes: the new index replaces the old one, and is a little wider
because it carries the tenant.

- **At the upgrade.** The first start of the new `event-management` builds the index over the base
  events that are not yet compressed (by default, about the last week), then removes the index it
  replaces. While it builds, writes of base events wait and reads do not; the build is bounded to
  40 seconds once the table is locked. It refuses, changing nothing, when there are more than
  4,000,000 rows to index or the table has more than 500 chunks, and it stops for good (until the
  index exists) if the build runs out of its time. None of these needs a recreate: the error carries
  the statements that build the index by hand. The previous `event-management` keeps storing events
  meanwhile.
- **To build it by hand, at a quiet time.** Run these on the event store's primary, then restart
  `event-management`, which finds the index and only removes the one it replaces. The first
  statement removes an unfinished index of the same name that an interrupted build leaves behind,
  and does nothing when there is none:

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_time;
  CREATE INDEX idx_events_tenant_device_time
    ON "event-management".events (tenant_id, device_token, occurred_time DESC)
    WITH (timescaledb.transaction_per_chunk);
  ```

  The build goes one chunk at a time, so a write waits only while the chunk it targets is being
  indexed: about 3 microseconds for each of that chunk's base events that is not yet compressed.
  While a chunk is indexed, the previous `event-management` and the new one both wait to store
  events into it, and an event held longer than the broker's 60 second acknowledgement wait is
  delivered again, which is safe. On a busy instance, choose a quiet time.
- **To avoid the refusal, build the index before you upgrade.** On a `v0.19.0` instance with
  more than `4000000` uncompressed base events, run the two statements above first; `v0.19.0`
  works with them, and the upgrade then only removes the old index. To count the base events, use
  the query under [Check the row count](#v0190-row-count) with `hypertable_name IN ('events')`.
- Going back to `v0.19.0` keeps the new index, and `v0.19.0` works with it.

##### Two devices can now send the same `altId` at the same instant, and both events are stored {#device-alt-id-key}

An event's `altId` is the sender's own message id, and the write side used it, with the event's
`occurredTime`, to drop a redelivered copy. That key was per tenant, so when two devices of one tenant
sent the same `altId` at the same instant, the second event was taken for a redelivery of the first
and silently discarded. The key now includes the device: both events are stored, and a device that
sends the same `altId` at the same instant again is still deduplicated. If you relied on one device's
`altId` suppressing another's, that no longer happens; make the `altId` unique across your devices
yourself if you need that.

- **At the upgrade.** The first start of the new `event-management` builds the new unique index over
  the base events that are not yet compressed, then removes the tenant-wide index it replaces; there
  is no moment without a deduplication guard. While it builds, writes of base events wait and reads
  do not; the build is bounded to 40 seconds once the table is locked. It refuses, changing nothing,
  when there are more than 4,000,000 rows to index or the table has more than 500 chunks, and a start
  that cannot lock the table or finish in time changes nothing and the next start tries again. None
  of these needs a recreate: the error carries the statements that build the index by hand. The
  previous `event-management` keeps storing events meanwhile.
- **To build it by hand, at a quiet time.** Run these on the event store's primary, then restart
  `event-management`, which finds the index and only removes the one it replaces. The first
  statement removes an unfinished index of the same name that an interrupted build leaves behind, and
  does nothing when there is none:

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_alt_id;
  CREATE UNIQUE INDEX idx_events_tenant_device_alt_id
    ON "event-management".events (tenant_id, device_token, alt_id, occurred_time)
    WHERE alt_id IS NOT NULL;
  ```

  A unique index is built in one go, so writes of base events wait for the whole build while reads
  do not. An event held longer than the broker's 60 second acknowledgement wait is delivered again,
  which is safe. On a busy instance, choose a quiet time.
- **To avoid the refusal, build the index before you upgrade.** On a `v0.19.0` instance with more
  than `4000000` uncompressed base events, run the two statements above first; `v0.19.0` works with
  them, and the upgrade then only removes the old index. To count the base events, use the query
  under [Check the row count](#v0190-row-count) with `hypertable_name IN ('events')`.
- Going back to `v0.19.0` keeps the new index, and `v0.19.0` works with it: redelivered events are
  still deduplicated, but that release still checks per tenant, so it goes back to discarding the
  second device's event.

##### The device intake stream deletes each message once it has been forwarded {#v0200-capture-stream}

The stream that holds every device publish until `event-sources` has forwarded it
(`device-events-capture`) now deletes a message when `event-sources` acknowledges it. In `v0.19.0`
it kept every message for up to seven days after it was forwarded. A publish that has not been
forwarded yet is kept exactly as before, so an outage of `event-sources` still loses nothing.

- **At the upgrade.** The first start of the new `event-sources` forwards what the existing stream
  still holds, waits until it is empty, then deletes the stream and creates it again with the new
  retention. Sequence numbers jump forward past the old stream's, so none is ever reused. A device publish that
  arrives in the instant between the empty check and the recreate can be lost after the broker
  acknowledged it. If the stream is not empty within five minutes, `event-sources` stops with an
  error that names the stream and does not delete anything; start it again once it can forward.
- **Nothing else may read this stream.** It accepts one consumer, the one `event-sources` owns, so
  `nats stream view` and any other consumer on `device-events-capture` stop working. To look at
  what devices send, read the `inbound-events` stream, which holds the decoded events.
- Going back to `v0.19.0` needs the stream deleted first, with `event-sources` stopped, because
  `v0.19.0` refuses to start against a stream with the new retention; it creates the stream again
  on its next start. Anything not yet forwarded when you delete it is lost.

##### An inbound event whose failure record cannot be encoded is no longer stored as an empty record {#v0200-failed-record-encode}

When device-management could not encode the record of an inbound event that failed, it used to
store an empty record anyway and acknowledge the event. The record read back as a failure with no
reason and no text, and the real failure was recorded nowhere. It now publishes nothing for that
event, acknowledges it, and counts the loss on `dead_letter_lost_total`, so `DeadLetterWriteLost`
fires and the `LOST` line in the pod's log says which event. No event a device can send reaches
this today; it is a defect path, and the alert is how you would learn of one. `DeadLetterWriteLost`
therefore has a fourth cause, listed in [the alert table](./detection-engine.md#what-to-watch).

Nothing needs doing.

##### A service that cannot reach the message broker at startup stops after a minute {#v0200-broker-unreachable-at-start}

Every service waits for the message broker (NATS) when it starts. In `v0.19.0` it waited 30
seconds and then started anyway. Services that create a bucket at startup then stopped with an
error that blamed the broker's version (`key-value requires at least server version 2.6.2`), and
the others reported ready while unable to send or receive anything. A service now waits up to a
minute, and if the broker has not answered by then it stops with an error that names the broker
address and the last connection error, and Kubernetes restarts it. A broker that comes up within
the minute is handled as before.

- **A pod that restarts with `the NATS broker was not reachable before the startup connection wait
  ran out`** could not connect to the broker at the address in the instance configuration. Check
  that the broker's pods are running and ready, and read the connection error at the end of the
  message: a name that does not resolve, a refused connection and a rejected certificate or
  credential each point somewhere different.
- **A broker that is away for longer costs restarts, not a service that stays down.** Each restart
  tries again, under Kubernetes' usual restart backoff, and the service starts once the broker
  answers.

Nothing needs doing.

##### `dcctl install --dry-run` makes the re-install refusals the install makes {#v0200-dry-run}

On a cluster that answers, a dry run now reads the install record, asks which instances run on
the cluster when the settings change, and fails with the install's own message when the install would be refused for what
the cluster already holds: a re-install from a machine that holds no state for the cluster, changed
settings while instances run on it, or a `--backup-snapshot-class` that cannot work. Before, it
printed a plan and exited 0. A dry run aimed at a cluster that does not exist yet, or that it cannot
reach, still prints its plan, and says those checks were not made. A dry run does not check
everything the install refuses; for example, a cluster whose relational store was built by an older
release is still refused only when the install runs.

Nothing to do.

##### `dcctl install` and `dcctl bootstrap` wait until every database instance has joined {#v0200-wait-database-instances}

Before, `dcctl install` returned once the relational database's primary accepted a connection, and
`dcctl bootstrap` waited only for the services, so either could report success, and bootstrap could
print that both databases were replicated, while a replica was still being created. Install now
records itself, releases the cluster lock, and then waits up to 15 minutes until every instance of
the relational database has joined. Bootstrap waits for the services and then, for up to 15 minutes,
for every instance of the instance's event store. If one has not joined in time, the command exits
with an error that names the database and how many of its instances are ready. Run the same command
again to keep waiting (for install, that re-applies the prerequisites and refuses bootstraps while
it runs, so watch the database with `kubectl` if you only want to follow it); a bootstrap that
ended this way is not undone, and the install is already recorded, so instances can be
bootstrapped meanwhile. A healthy install adds a few seconds.

Nothing else needs doing.

##### The rate ceilings are renamed after what they count {#v0200-rate-key-rename}

The ingest ceiling has counted readings for some time, and the outbound ceiling counts connector
calls, but both were still named for messages. They are renamed everywhere, with no old spelling
kept alongside:

| Was | Now |
| --- | --- |
| `ingestRateLimit.messagesPerSecond` (`event-sources`, `lwm2m-ingest`, `sparkplug-ingest` config) | `ingestRateLimit.readingsPerSecond` |
| `outboundMessagesPerSecond` (`event-processing`, `outbound-connectors` config) | `outboundCallsPerSecond` |
| `ingestMessagesPerSecond` (GraphQL field, tier config key) | `ingestReadingsPerSecond` |
| `outboundMessagesPerSecond` (GraphQL field, tier config key) | `outboundCallsPerSecond` |

- **Before you upgrade, rename the service configuration keys.** If your values set any of the old
  keys under a service's `config`, change them to the new names. A service that still finds an old
  key refuses to start, and its error names the key to use instead. It does not fall back to the
  platform default, because that would quietly replace the ceiling you set.
- **API clients.** The fields on `tenantGovernance`, on the admin API's tenant type and on its
  create and update inputs are renamed. A request that selects or sends an old name now fails, so
  update any script or integration that manages tenant ceilings. A tier config that uses an old key
  is refused, and its error lists the keys that are accepted.
- **Stored data is converted for you.** The upgrade renames the tenant override columns and
  re-keys every stored tier config, including the shipped tiers and deleted ones, at the same
  values.
- **During the rollout.** Until every pod runs the new release, an older pod and a newer
  `user-management` do not understand each other. An older ingest or outbound pod asks for the old
  `tenantGovernance` fields, the request fails, and that pod meters every tenant at the platform
  default; `TenantsMeteredAtPlatformDefault` can fire for it. An older `user-management` pod still
  uses the old column names once the new pod has renamed them, so the tenant reads and writes it
  serves fail until it is replaced. Both end when the rollout finishes; avoid changing tenants or
  tiers until it has.

##### Deleting a tenant now erases everything stored under its object-store prefix {#v0200-object-prefix-erase}

Erasing a deleted tenant used to remove only the objects that a user-management row named. It now
also removes every object under that tenant's prefix in the object store, whichever service stored
it, so update artifacts and any object whose reference was lost are erased with the tenant.

- **On the S3 backend, the credential now needs `s3:ListBucket` on the bucket**, in addition to
  `s3:GetObject`, `s3:PutObject` and `s3:DeleteObject`. Without it the object step of a tenant
  erasure fails and is retried on every pass, so the erasure does not complete. Grant it before
  you upgrade. The filesystem backend needs nothing.

##### A deleted tenant's token is held until everything issued for it has expired, and a new tenant with that token starts clean {#v0200-token-hold}

A deleted tenant's token used to be released a fixed time after the deletion began. The hold
(`tokenHoldSeconds`, 12 hours by default) now runs from when the last storage system finished erasing
the tenant's data, plus a one-minute margin, so a credential minted for the deleted tenant has
expired before the token can be reused. A deletion therefore holds its token a little longer than it
did. Three changes support it:

- **No device credential is minted once the erasure has begun.** A device's connect for that tenant is
  refused from then on.
- **The LwM2M and Sparkplug services forget a cached device lookup after an hour**, and an LwM2M
  registration of a deleted tenant is ended instead of extended.
- **A refresh token issued in an earlier second than the tenant was created is refused**, for every caller. When a tenant
  is deleted and another is created with the same token, people who were signed in to the first one
  sign in again.

The audit entries kept for a deleted tenant are now filed under the deletion rather than under the
tenant's token, so the new tenant's own journal starts empty. The admin API's `TenantDeletion` type
gains `retainedAuditTenant`, which is the `tenant` filter of `auditEvents` that reads those entries;
in the console, **Admin → Audit** lists each completed deletion as `<token> (deleted <date>)` in its
tenant filter. Nothing needs doing. See [Tenant deletion](./tenant-deletion.md#audit).

##### A record that does not exist is answered with `NOT_FOUND` {#v0200-not-found}

A request that names a record which does not exist used to reach the client as a bare `record not
found` with no `extensions.code` in most services. It now carries `extensions.code` `NOT_FOUND`, and
the message is unchanged. A refusal that already had its own code for a miss, such as
`ARTIFACT_NOT_FOUND`, keeps it. In this release the user-management APIs still answer some misses (a
tenant, role, identity or membership that does not exist) with a message such as `tenant not found` and
no code. A client that told a missing record from other failures by reading the message can switch to
the code. See the
[GraphQL reference](../reference/graphql-api.md#not-found).

##### Single sign-on is new, and needs nothing from you {#v0200-sso}

People can now sign in through an external OpenID Connect identity provider, and a tenant can require it. Nothing is on until a superuser creates a connection, so an upgraded instance behaves as before. There is nothing to migrate and password sign-in keeps working. The one-time re-sign-in that [server-side sign-out](#v0200-server-side-logout) causes is already described.

- **Setting up a provider** is described in [Configure single sign-on](../guides/configuring-sso.md), and how it behaves in [Single sign-on](../concepts/single-sign-on.md).
- **The sign-in screen opens on the email address alone.** An address whose domain has an enabled, approved connection is offered **Continue with single sign-on**, with **Use a password instead** beside it; every other address goes to the password field. `beginSso` called with an email answers `null` for a domain that has no connection, and for one whose connection is disabled or awaiting approval. A connection named by token still refuses with "single sign-on is not available". Password sign-in is never removed, so an account that has only a password is not stranded.
- **People link their own account.** A person signed in with a password can link a single sign-on account from the user menu. `beginSsoLink` takes no connection token: the server uses the connection a superuser verified the person's own email domain for. Every refusal carries a code (`SSO_LINK_SESSION_REQUIRED`, `SSO_LINK_PASSWORD_REQUIRED`, `SSO_LINK_NOT_ALLOWED`, `SSO_LINK_NO_CONNECTION`, `SSO_LINK_ALREADY_LINKED`), and a refusal at the provider's return says which check failed.
- **A connection can also sign people out at the identity provider.** It is off until an administrator turns it on, because signing out at the provider ends the session there for every application it serves. The upgrade adds the setting (`providerSignOut`, default off) to each connection; nothing changes until it is set. `logout` takes an optional `returnTo` (`CONSOLE` or `DASHBOARD`) and answers `SignOut { providerSignOutUrl }`, which is `null` when there is nothing more to do. The addresses to register at the provider are on the connection (`signOutReturnUrls`). See [Sign out at the provider](../guides/configuring-sso.md#provider-sign-out).
- **Ending a session does not recall tokens already issued.** After a sign-out, a revoke, an unlink or a disabled connection, the access and identity tokens already issued keep working until they expire, 15 minutes by default.

##### Single sign-on needs the instance's public address, so the chart now derives it {#v0200-sso-issuer}

Signing in through an external OpenID Connect identity provider, and the OAuth authorization server
that MCP clients use, both need to know the address this instance is reached at, because a sign-in
has to return to somewhere. user-management's `auth.issuerUrl` used to be empty unless you set it.
The chart now derives it from the ingress, as `https://<host>/api/user-management`, when the
instance is served over https or on a loopback host (`localhost`, `127.0.0.1`, `::1`), and your own
value always wins. Three things follow:

- **The OAuth authorization server turns on** for those instances. On a default install MCP clients
  could not obtain a token before, because it was off until you set this value. To keep it off, and
  single sign-on with it, set `functionalAreas.user-management.config.auth.issuerUrl` to `""`.
- **Tokens issued after the upgrade carry that address as their issuer**, instead of the
  per-instance identifier. The services' token validators check neither the issuer nor the
  audience, so tokens issued before the upgrade keep working until they expire. Nothing needs doing.
- **A plain-http instance on a real hostname gets no issuer**, because user-management refuses an
  http issuer on anything but a loopback host. The authorization server and single sign-on stay off,
  and `helm install` and `dcctl bootstrap` say so. Serve the instance over https to turn them on.

##### Single sign-on alerts {#v0200-sso-alerts}

Three alerts report the failures of single sign-on. If you route or silence alerts by name, these
are new.

| Alert | Severity | What it reports | What to do |
| --- | --- | --- | --- |
| `SsoIdpUnreachable` | warning | For ten minutes, user-management has failed to reach a configured identity provider's token endpoint or signing keys, so people who sign in through it are refused. Password sign-in is unaffected. | Check that the provider is up and its address resolves from the cluster, that the egress allow list (`instance.config.infrastructure.egress`) permits an on-premises provider's range, and, if the provider moved an endpoint, refresh and approve the connection's discovery snapshot. |
| `SsoUnverifiedIdentityRefused` | warning | A sign-in was refused although the provider vouched for it, with the outcome in the `outcome` label: `refused_unverified_email`, `refused_unverified_domain`, `refused_required_claim` or `refused_system_role_identity`. Nobody was signed in. | Usually a misconfigured connection or someone probing one. Look in the audit journal for the address the provider asserted, and at the provider's own sign-in log. |
| `SsoStateStoreFull` | critical | The store that holds begun sign-ins is full, so user-management refuses to begin any single sign-on through any provider until entries expire, ten minutes later. Password sign-in is unaffected. | Someone is almost certainly calling the public sign-in start in a loop: find the traffic in the ingress logs and block it upstream. If the volume is legitimate, raise `instance.config.infrastructure.nats.kvStateMaxBytes`. |

##### An email's domain now has one spelling, and a stored address with a non-ASCII domain no longer matches {#v0200-email-idna}

An email address is now stored, looked up and compared in one canonical form: the local part trimmed and lower-cased as before, and the domain converted to lower-case ASCII (the `xn--` form for an internationalised name) with no trailing dot. `user@bücher.example` and `user@xn--bcher-kva.example` are therefore the same identity at password sign-in, at single sign-on and in a link request, and the domain a connection claims is matched in the same form. An address that is not exactly `local@domain`, or whose domain is not a valid hostname, is refused.

- **An address with an ASCII domain is unchanged.** Nothing needs doing for it.
- **An identity stored with a non-ASCII domain (`user@bücher.example`) keeps its old form, and is refused until an administrator recreates it.** It cannot sign in with a password or through single sign-on, and a session it already holds ends at its next exchange; selecting a tenant or refreshing then reports the code `IDENTITY_EMAIL_NOT_CANONICAL`. Administrator actions still find it by the address as it is stored, so you can disable it, end its sessions, unlink it and delete it. To restore access, unlink and delete the old identity, then create it again (under either spelling of the domain: it is stored in the `xn--` form) and restore its memberships and links. Doing this before upgrading, with the domain in its `xn--` form, avoids the interruption: see [Before you upgrade](#v0200-before). The same applies to an identity whose stored address is not a valid `local@domain`.

##### X.509 device credentials are no longer accepted {#v0200-x509}

The `X509_CERTIFICATE` credential type is not accepted until certificate verification is supported.
Creating a credential of that type, changing one to it, and replacing a device with one are refused
with `UNSUPPORTED`, and nothing is written. The console no longer offers it. A credential of that
type stored before the upgrade no longer authenticates, and nothing converts it: before you upgrade,
give each such device an `ACCESS_TOKEN` or `MQTT_BASIC` credential. See
[Device credentials](../guides/device-credentials.md).

##### An `MQTT_BASIC` password must be at least 16 bytes {#v0200-mqtt-password-length}

Creating an `MQTT_BASIC` credential, or setting its password, now refuses a password shorter than 16
bytes, or longer than 4096, counted as sent. A short password is refused with `INVALID_VALUE`. A
device password is provisioned rather than remembered, so the floor is set for a generated secret; the
console's **Generate strong password** makes one. Existing shorter passwords keep working until they
are set again. Changing a credential to `MQTT_BASIC` must set its password in the same request.

##### Dashboards: publishing makes a version live {#v0200-dashboard-publishing}

Publishing a dashboard now makes the version it creates the one viewers are served, and
`activateDashboardVersion` makes an earlier version live again; there is no unpublish. Viewers read
the live version with `publishedDashboard`, and `/dash` opens it by its token. The draft is for the
dashboard's authors.

- **Dashboards published before the upgrade are not live.** Nothing recorded which version they
  served, so viewers are told the dashboard is not published (`NOT_PUBLISHED`) until an author
  publishes it again.
- **`publishDashboard` returns a `DashboardPublication`**, with the new `version` and the `dashboard`.
- **Reading a dashboard's `definition`, or a version's body with `dashboardVersion`, needs
  `dashboard:write`.** A caller with only `dashboard:read` reads `publishedDashboard`.
- **The `dashboards` search returns `DashboardSummary`**, which has no `definition`.
- **`dashboardVersions` is paged**, newest first: 100 versions unless you pass `limit` (at most
  1000) and `offset`, and without each version's body.
- `rollbackDashboard` takes an optional `expectedUpdatedAt` and refuses a stale rollback.
- The name filter of the `dashboards` search matches `%`, `_` and `\` as themselves, not as
  wildcards.
- Changing a dashboard of a tenant being deleted is refused with `TENANT_DELETED`; deleting it is not.
- The console will not publish a version while a widget on it is unresolved. See
  [Publishing while a widget is unresolved](../guides/adding-widget-packages.md#publishing-while-a-widget-is-unresolved).

##### Measurement aggregation needs a start time, and is bounded {#v0200-aggregation-bounds}

`bucketedMeasurements` now requires `startTime`, and is refused with `INVALID_VALUE` without one;
`endTime` defaults to now. A read is refused with `LIMIT_EXCEEDED`, rather than truncated, when it
would produce more than 10,000 buckets, or when its buckets times its measurement names exceed
50,000. Without a `name` filter, the names counted are the distinct names the read matches. The MCP
`aggregate_measurements` tool has the same requirement and bounds.

##### Requests that are now refused {#v0200-input-bounds}

Inputs that were accepted at any size are now refused above these bounds, with `LIMIT_EXCEEDED`
unless another code is named. A value exactly at a bound is accepted. The
[GraphQL reference](../reference/graphql-api.md#input-validation) lists them.

| What | Bound |
| --- | --- |
| Keys in one lookup by token, id or external id, in every service, and the tokens of one `removeEntityRelationships` | 1,000 |
| A JSON input: metadata, properties, configuration, a payload, recipients and the like | 64 KiB |
| A command's payload or metadata, when it is enqueued or batched | 64 KiB (`PAYLOAD_TOO_LARGE`, `METADATA_TOO_LARGE`) |
| A command name | 128 bytes (`COMMAND_NAME_TOO_LONG`) |
| An attribute key, and an attribute value | 128 bytes, and 64 KiB |
| A detection rule's definition, and its authoring graph | 256 KiB each |
| Tracked relationships of one device | 256 |
| Metric definitions, command definitions and detection rules of one profile | 1,000 of each |
| Relationships created by one `createEntityRelationships` | 1,000 |
| Versions returned by `deviceProfileVersions`, `entityGroupVersions`, `assetTypeVersions` and `connectorVersions` | the newest 1,000, unless paged with their new `limit` and `offset` |
| Recipients of one notification rule | 100 |
| An OAuth client's `redirectUris`, and its `scopes` | 16 entries each, of at most 2,048 and 64 characters |
| Tenant tiers, and the tokens of one `reorderTenantTiers` | 100 |
| A password set through the admin API | 72 bytes |
| Roles, authorities or role tokens in one admin request | 100 |
| Running one GraphQL query or mutation | 60 seconds |
| Receiving a request body, on GraphQL and on the OAuth and MCP endpoints | 30 seconds; a stalled body is answered `400` and the connection closed |

Three values are also refused when a record is created or updated, where before they were stored and
failed later or did nothing:

- a connector of type `gcp_pubsub`, which has no delivery client yet (`UNSUPPORTED`); one stored
  earlier is still dead-lettered when it is dispatched;
- AI provider `params` that set anything (`UNSUPPORTED`), since no provider client applies them;
  empty, null and `{}` are accepted;
- a webhook notification channel's custom headers that delivery would refuse: an invalid name,
  control characters in a value, a reserved `Authorization` or `X-DC-*` header, or a duplicate.

##### The event-path services run with a tuned garbage collector {#v0200-gogc}

`event-sources`, `event-management`, `device-management`, `device-state` and `event-processing` now
run with `GOGC=400` and a soft memory limit (`GOMEMLIMIT`) at 75% of their memory limit, and
`event-processing`'s memory limit is at least 384Mi. On Google Kubernetes Engine at 6,000 events a
second, against a control run in the same session, CPU per event fell 18% in `device-management` and
`event-management`, 15% in `device-state`, 4% in `event-sources` and 5% in `event-processing`.

They are chart values, `functionalAreas.<area>.gogc` and `functionalAreas.<area>.goMemLimitPercent`,
and a new top-level `gogc` (default `0`, off) beside the existing top-level `goMemLimitPercent`. An
area's own value wins over the top level, so a top-level `0` no longer turns the soft limit off for
these five: set the area's own value to `0` to turn either off for that area.

##### `device-management` keeps its lookups for minutes, and a change reaches every replica {#v0200-resolve-caches}

`device-management` looks up each event's credential, device, tracked relationships, profile and
group memberships, and kept them in each replica's memory for 5 seconds, so a device that reported
less often than that was looked up afresh every time. They are now kept for 5 minutes by default:

| `device-management` config key | Default | Range |
| --- | --- | --- |
| `inMemoryCache.ttlSeconds` (new) | 300 | 1-3600 |
| `inMemoryCache.credentialCacheMiB` (new; was a fixed 16 MiB) | 32 | 1-256 |
| `deviceCacheTtlSeconds`, `relationshipCacheTtlSeconds`, `metricDefCacheTtlSeconds`, `membershipCacheTtlSeconds` (were 60) | 300 | 1-3600 |

A value outside its range stops the service from starting. A change now drops the stored copy on
every replica, not only on the one that made it; a replica whose broker connection comes back drops
everything it holds, and erasing a deleted tenant drops that tenant's copies on every replica.
Updating a device, including changing its type, and creating a relationship, in one call or many,
by redeeming or reopening a claim, or by changing a relationship type, did not drop the copies they
changed, and now do.

- **Size each replica for the whole active fleet**, not the fleet divided by replicas: every replica
  sees nearly every device. For 60,000 devices with one tracked relationship each, that is about 80 for
  `inMemoryCache.credentialCacheMiB`, 64 for `inMemoryCache.perDeviceCacheMiB` and a memory limit of
  512Mi. A bound smaller
  than the fleet makes the cache answer almost nothing.
- **A revocation is bounded by the same 5 minutes.** A replica that misses the message telling it
  to drop a revoked credential, without losing its broker connection, can go on authenticating that
  credential's events until its copy expires. Connections are always checked against the database.
  See [How quickly a revocation takes effect](../guides/device-credentials.md#revocation-timing).

##### Readers fetch ahead by default {#v0200-fetch-ahead}

A service that reads a stream now asks for its next batch while it hands out the current one, and
asks for 128 messages at a time instead of 64. Measured on Google Kubernetes Engine, this kept the
detection engine's backlog under heavy load at a few hundred messages instead of tens of thousands;
the throughput ceiling and every service's CPU per event were the same with it off. Set
`instance.config.infrastructure.nats.fetch.ahead: false` to turn it off. See
[Fetching ahead](./observability.md#fetch-ahead).

##### Detection reads its events through a new consumer {#v0200-ack-floor}

`event-processing` now acknowledges the resolved events once per checkpoint, through a new durable
consumer whose name ends in `_ackfloor`
(`<instance>_event-processing_resolved-events_ackfloor`). On its first start the new consumer begins
just after the last event the saved state covers, and the first leader of the new release deletes
the old consumer. Nothing needs migrating. A dashboard or alert of your own that selects
`event-processing`'s resolved-events consumer by its `durable` label needs the new name; the shipped
alerts match both. Going back to `v0.19.0` makes detection read the stream's retained events once
more, and it skips those it has already seen.

Live detection also reads back a range of events it was not delivered, instead of skipping it, and
two alerts report it (`DetectLiveGapFillFailing`, `DetectLiveDeliveriesRecovered`). A detection loop
that stops making progress raises `DetectLoopStalled`, and each checkpoint now runs under a deadline
whose floor is the new `checkpointTimeoutSeconds` (default 10, at most 120).

##### The AI drafting timeout {#v0200-inference-timeout}

The default ceiling on one call from `ai-inference` to a model provider falls from 60 to 40 seconds
(`inferenceTimeoutMs`, at most 120000). Drafting a rule from a description now waits for that
ceiling, with an overall budget of 50 seconds, and a model that does not answer in time is reported
as that rather than as an unavailable provider. If you relied on the 60 second default, set
`inferenceTimeoutMs` yourself.

##### `dcctl bootstrap` can give an instance an object store {#v0200-object-store}

Tenant logos and update artifacts are kept in an object store, and an instance built by `dcctl` had
none, so both were refused with `OBJECT_STORE_NOT_CONFIGURED`. `dcctl bootstrap` now takes
`--object-store none|filesystem|s3`, with flags for the volume or the bucket. Without the flag, an
instance on a cluster `dcctl` creates gets a 5Gi filesystem store, and one on a cluster named with
`--kube-context` gets none. The choice is recorded and kept by `dcctl upgrade`, which refuses a change
that would strand stored objects. **`dcctl upgrade` does not add a store to an instance built
without one**: before `v1.0.0`, destroy it and bootstrap it again with the flag. See
[The object store](./bootstrap.md#object-store).

##### Devices can receive configuration {#v0200-device-configuration}

A device profile version declares which of its devices' shared attributes a device may see, in the
profile editor's **Configuration** tab or with `setDeviceProfileConfigurationDeclaration`. Writing a
declared shared attribute creates a numbered configuration revision, which is sent to the device on
its own configuration topic; the device reports `RECEIVED`, `APPLIED` or `REJECTED`, and asks for the
current revision with a `sync`, which is also how a newly published profile reaches it. The device's
page in the console shows the declaration, the current revision and what the device last reported,
and `deviceConfiguration` and `deviceConfigurationRevisions` read them. Writing a declared attribute
is refused with `CONFIGURATION_TOO_LARGE` when the document would pass 64 KiB, and with
`CONFIGURATION_VALUE_TYPE_MISMATCH` when the value does not fit the declared type. MQTT devices only,
for now. See [Configuration delivery topics](../guides/connecting-a-device.md#reserved-device-topics).

##### Over-the-air updates: a first slice {#v0200-updates}

The new `update-management` service keeps a per-tenant catalogue of firmware, software and
configuration images, with an upload checked against its declared size and SHA-256 digest. An
artifact is assigned to one device at a time; the device is told on its configuration topic,
downloads the image with a short-lived grant it can resume, and each attempt is tracked through to
the running version. It is opt-in: it runs in the `full` profile, or with
`--enable-area update-management`. It needs an object store, and:

- **No artifact can be assigned until you set `updates.allowUnverifiedArtifacts: true`**, because
  nothing yet authenticates who produced an artifact.
- **Devices get no download grant until `updates.downloadBaseUrl` is set.**
- **Keep the download path's query string out of access logs.** The chart serves it on an ingress of
  its own with the access log off; behind another proxy, do the same.
- There are no fleet rollouts yet, and only MQTT devices can be updated.

##### The published packages {#v0200-packages}

- **`@devicechain/widget-sdk` is new.** It defines a widget (`defineWidget`) and builds a registry
  of them (`createWidgetRegistry`); see [Writing a custom widget](../guides/writing-a-custom-widget.md).
  `@devicechain/widgets` declares it as a peer dependency, so install it beside `@devicechain/widgets`.
- **`@devicechain/widgets` no longer exports `WIDGET_REGISTRY`, `ALARM_WIDGET_REGISTRY`,
  `CONTROL_WIDGET_REGISTRY`, `SELECTION_WIDGET_REGISTRY` or `LOCATION_WIDGET_REGISTRY`.** Look a
  widget up through a registry, such as the one `createBuiltinWidgetRegistry` builds.
- **`echarts` is a peer dependency of `@devicechain/widgets`, not a dependency:** add `echarts`
  (`^6.0.0`) to your application. The chart helpers are at `@devicechain/widget-sdk/echarts`.
- **Peer ranges rose:** `react` and `react-dom` to `^19.3.0`, `maplibre-gl` to `^6.11.2`.
- **`DashboardHubConfig.bindings` and `DashboardHub.setBindings` are removed from
  `@devicechain/dashboards`.** Pass bindings to `DashboardRenderer`, and use `useDashboardHub`.
  `fetchWidgetHistory` and `widgetSubjectLabel` take a widget whose data source is already resolved.
- **`parseDashboardDefinition(raw, { catalog })`** takes the widget catalog to check against. A
  widget of an unknown type, or one the catalog cannot read, is kept as a placeholder
  (`UnknownWidgetInstance`, part of the `WidgetInstance` union) and written back exactly as stored,
  where it used to fail the whole definition. A stored data source of kind `unbound` is refused.
- **The .NET SDK** opens subscriptions itself and no longer goes through the system HTTP proxy. A
  device can opt in to handling commands concurrently (`MaxConcurrentCommands`), each command carries
  its arrival order (`DeviceCommand.Sequence`), and a response finished during a reconnect is sent
  after it instead of being dropped.

##### New alerts {#v0200-alerts}

If you route or silence alerts by name, these are new, besides the
[single sign-on alerts](#v0200-sso-alerts):

| Alert | What it reports |
| --- | --- |
| `SafetyGateDisabled` | A service runs without one of its optional safety gates: the tenant lifecycle gate, command presence, or validating detection rules at publish. |
| `DetectLoopStalled` | The detection leader has not made progress for minutes. |
| `DetectLiveGapFillFailing`, `DetectLiveDeliveriesRecovered` | Live detection could not read back, or did read back, events it was not delivered. |
| `SparkplugIngestLeaderCountNotOne`, `Lwm2mIngestLeaderCountNotOne`, `Lwm2mIngestLeaderNotServing` | An edge ingest service does not have exactly one leader, or its leader is not serving. A `sparkplug-ingest` with no sources has no leader and fires. |
| `SparkplugIngestDroppingData`, `SparkplugIngestSubscriptionRefused`, `SparkplugIngestBrokerUnreachable`, `Lwm2mIngestDroppingNotifications` | Sparkplug or LwM2M ingest is losing data or cannot reach its broker. |
| `CNPGCollectorAbsent` | Prometheus has stopped scraping a database's metrics. |

`dcctl install` also stops scraping, and alerting on, the control-plane components a managed
cluster such as Google Kubernetes Engine does not run, so their `...Down` alerts no longer fire
there.

##### Other behaviour worth knowing about {#v0200-other}

- **Sparkplug.** A node birth that replaces an online session now marks the old session's child
  devices disconnected; they used to stay online.
- **Notifications.** An alarm event of a type `notification-management` does not know is retried and
  then dead-lettered, instead of being dropped.
- **Dead letters and failed decodes** keep a payload with its credential fields replaced by
  `[redacted]`. A body that is not JSON is kept as its length and SHA-256.
- **Command responses** may carry any JSON value as their `payload`, not only a string.
- **Correlation ids** that the platform generates are 22 characters instead of a 36-character UUID;
  treat them as opaque strings.
- **GraphQL descriptions** come only from the schemas' `"""` strings, and every element now has one;
  introspection no longer shows `#` comments.
- **`dcctl install` and `dcctl bootstrap` with `--kube-context`** no longer need Docker or `kind`, and
  check the Kubernetes version of the context they target. A named context missing from the
  kubeconfig fails the preflight.
- **Detection can split its state** across `detectShards` (1-64, default 1). The shards still run one
  after another, so more than one does not make detection faster yet.

### v0.19.0 — sized from measurement at 6,000 events a second; a full stream refuses {#v0190-upgrade}

`v0.19.0` is an in-place upgrade from `v0.18.0`: `dcctl install` for the cluster, then `dcctl
upgrade` for each instance, as described [for v0.17.0](#v0170-upgrade). **Count your event store
before you run this release's `dcctl install`.** An instance whose event store holds more than
4,000,000 rows that are not yet compressed
cannot take it in place and has to be recreated, which discards its data, and most instances with
real traffic are over that line ([Check the row count](#v0190-row-count)). A table with more than
500 chunks is refused too, unless you first remove its oldest events
([Count the chunks](#v0190-chunk-count)). An instance built by `v0.16.0` or earlier still has to be
[destroyed and built again](#pre-declaration-recreate).

Most of this release is about keeping up. The services that handle every event now request the CPU
they were measured to use, spread across nodes, store and merge events in fewer statements, and
answer repeated lookups from memory; under `--ha` without `--compact`, `event-management` runs as
two pods. On Google
Kubernetes Engine, a default HA install accepted 6,000 events a second for 10 minutes, twice, and
stored every accepted event exactly once, checked event by event ([Performance](#v0190-performance)). Two failure paths change from
silent to visible: a full ingest stream now refuses new events instead of discarding unread ones,
and the backup alerts warn before archiving can stop a database. And `dcctl upgrade` now applies an
instance's message broker and event store settings, which it never did before, so an upgraded
instance gets the same broker and event store as a new one.

**Who has to do something:**

- **Every instance with real traffic:** count the event store's uncompressed rows and its chunks
  before you upgrade. Over 4,000,000 rows, the instance must be recreated, and recreating discards
  its data ([Check the row count](#v0190-row-count)).
- **Every cluster:** re-run `dcctl install` with this release before any `bootstrap`, `upgrade` or
  `destroy`. This `dcctl` refuses an install record written by an earlier one. Upgrade every copy
  of `dcctl` you use too: an earlier one can leave an instance this release built marked as
  unfinished, and a plain `dcctl bootstrap` would then run over it
  ([Finishing a failed bootstrap](#v0190-bootstrap-resume)).
- **Every instance installed without `--compact`:** the nodes need room for larger CPU requests
  and for the message broker's first requests ([Make room](#v0190-room)).
- **Anyone with devices that send more than 256 readings in one message:** those messages are now
  refused ([256 readings](#v0190-reading-limit)).
- **Any tenant that sends more than 1,000 readings a second**, including a fleet that puts several
  readings in one message, and **every LwM2M tenant**: the ingest ceiling now counts readings, and
  readings over it are refused with `429` over HTTP and dropped, without telling the device, on
  every other transport ([Readings, not messages](#v0190-ingest-readings)).
- **Anyone with devices that send readings dated more than 366 days back**, or that report times
  near 1970 because their clock is not set: those readings are now refused
  ([Old readings](#v0190-event-age-limit)).
- **Anyone who set `backup_retention`, `tsdbConfiguration.maxOpenConnections`,
  `rdbConfiguration.maxOpenConnections`, `maxReadingsPerMessage`, or analytics readers' connection
  limits:** see [Before you upgrade](#v0190-before).
- **Anyone who relies on revoking a device credential taking effect at once:** a revocation can
  now take up to five seconds on one `device-management` replica
  ([Credential checks](#v0190-credential-cache)).
- **Anyone who calls the HTTP ingest API:** retry on `503` with `Retry-After`
  ([Backpressure](#v0190-backpressure)).
- **Anyone with an event source that reads from their own MQTT broker:** the source connects under
  a new client id, which the broker must allow, and can be read twice while the upgrade rolls
  ([Your own MQTT broker](#v0190-mqtt-client-id)).
- **Anyone who routes or silences alerts by name or severity:** alerts are added, and
  `JetStreamStreamNearFull` is now `info` ([Alerts](#v0190-alerts)).
- **Anyone who installs the chart with their own values:** several top-level resource values no
  longer reach the event-path services ([Your own chart values](#v0190-chart-values)).

Everything else is under [What changed](#v0190-what-changed), by area.

#### Before you upgrade {#v0190-before}

Do these in order.

##### 1. Check the row count {#v0190-row-count}

The first start of the new `event-management` rebuilds the event store's keys over every row
of five tables that is not yet compressed: by default, the last week or so. **An instance with
more than 4,000,000 such rows cannot be upgraded in place.** A base event is one row, plus one
for each reading, location or alert it carries, plus one for each relationship anchor. At one
reading and one anchor an event, 4 million rows is a week at an average of about 2 events per
second, so most instances with real traffic are over the line.

Open `psql` on the event store (the namespace is `dci-` plus the instance id; the database is
the instance id on its own). The count only reads, so any of the store's pods will do; if
`dc-tsdb-1` is not there, `kubectl -n dci-<instance-id> get pods` lists the others:

```bash
kubectl -n dci-<instance-id> exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d <instance-id>
```

Then run the count the upgrade makes. It reads every uncompressed row, so on a large store it
takes a while:

```sql
DO $$
DECLARE c record; n bigint := 0; k bigint;
BEGIN
  FOR c IN SELECT format('%I.%I', chunk_schema, chunk_name) AS chunk
           FROM timescaledb_information.chunks
           WHERE hypertable_schema = 'event-management'
             AND hypertable_name IN ('events', 'measurement_events', 'location_events',
                                     'alert_events', 'event_anchors')
  LOOP
    EXECUTE 'SELECT count(*) FROM ONLY ' || c.chunk INTO k;
    n := n + k;
  END LOOP;
  RAISE NOTICE 'rows to rebuild: %', n;
END $$;
```

If it prints more than `4000000`, do not upgrade this instance in place: take what you need out
of it, then recreate it with `dcctl destroy` and `dcctl bootstrap` on the new release. Count while
the cluster still runs `v0.18.0`, before this release's `dcctl install`, so that an instance over
the line can still be read and destroyed with the `dcctl` that built it.

:::caution Recreating discards the instance's data
`dcctl destroy` removes the instance's databases: every tenant, device, device definition,
dashboard and user, and all of its events. DeviceChain has no export tool. Take what you need
before you destroy. A tenant's events, readings, locations and alerts can be read through
[SQL and BI access](../guides/sql-and-bi-access.md). PostgreSQL's `pg_dump` can copy the instance's
two databases, each named after the instance id: one on the event store, in the instance's own
namespace, and one on the shared relational database (`dc-rdb`, in `dc-system`), which holds every
instance's data, so dump only that database. Write the dump to your own machine, for example
`kubectl -n dc-system exec dc-rdb-1 -c postgres -- pg_dump -U postgres -Fc <instance-id> > rdb.dump`,
and the same with `-n dci-<instance-id>` and `dc-tsdb-1` for the event store. Secrets in the dump
stay sealed by the instance's root key. A dump is a copy to read, or to load into a database of
your own: no `dcctl` loads it into the new instance.

Keep the backups, so that you can go back. Destroy with the `v0.18.0` `dcctl` before you run this
release's `dcctl install`: it leaves the in-cluster backups in place. After the install, use this
release's `dcctl destroy --keep-backups`; without the flag it deletes the instance's event-store
archive. Either way, what is kept is the **event store's** archive, beside the cluster's relational
archive, which destroy never deletes. In the same cluster, the event-store archive brings back only
the instance's event history, under an empty control plane with no tenants, devices, users or
secrets. Getting the whole instance back on `v0.18.0` means recovering onto a
new cluster, from the relational archive at a time before the destroy (`--restore-rdb-at`), which
rewinds every instance on that cluster, with the instance's root-key escrow and its event-store
archive, as [Recovering an instance](./disaster-recovery.md#recover) describes. Restoring the
event-store archive onto `v0.19.0` is not a way forward: it brings back the same rows, and the
upgrade refuses them again.

The kept event-store archive holds the events of every tenant the instance had, including tenants
deleted within its recovery window, and nothing prunes it once the instance is gone. Remove it
when you no longer need it, as
[What happens to the instance's backups](./bootstrap.md#destroy-backups) shows.

Before you run `dcctl bootstrap` under the same name, move the instance's root-key escrow artifact
(`~/.devicechain/escrow/<instance>-rootkey.escrow`) aside and keep it: bootstrap will not overwrite
it, and it is the only key to the relational backups taken before the destroy.

No in-place path has been tested for an instance over the line.
:::

##### 2. Count the chunks {#v0190-chunk-count}

The rebuild, and the [index removal](#v0190-event-store-keys), also refuse a table with
more than **500 chunks**. Each locks every chunk of the table it changes, and 500 is half the most
this release has been measured to rebuild within one start, which leaves room for slower storage.
Above it, `event-management` refuses before locking anything, rather than risk a rebuild that
cannot finish and then blocks every later start. A chunk covers one chunk interval of one table: a
day by default (`lifecycle.chunkIntervalHours`), so at the default 500 chunks is about a year and
four months of history with retention off. A shorter interval reaches 500 sooner, and so does a
device that sent readings dated far in the past. Count them, on any of the event store's pods:

```sql
SELECT hypertable_name, count(*) AS chunks, min(range_start) AS oldest, max(range_end) AS newest
FROM timescaledb_information.chunks WHERE hypertable_schema = 'event-management'
GROUP BY 1 ORDER BY 1;
```

If a table has more than 500 and you can do without its oldest events, remove them before you
upgrade. **This deletes every tenant's events in those chunks, for good.**

1. **Stop whatever sent them.** If a device is still sending readings dated far in the past, revoke
   its credential first: the previous release accepts them, and they would create the chunks again.
2. **Choose a cutoff.** Chunks that end on or before it are removed whole; the chunk that spans it is
   kept. The date the instance was installed removes only events dated before the instance existed.
   The same cutoff applies to all six event tables, so an event is not left without its readings.
3. **Run the removal on the event store's primary.**
   `kubectl -n dci-<instance-id> get pods -l cnpg.io/instanceRole=primary` lists the primaries; use
   the `dc-tsdb-` one, open `psql` on it as above, and run the statement below with your cutoff in
   place of `2024-01-01`. Run it on its own, not inside `BEGIN`: it removes at most 100 chunks of one
   table at a time and commits after each batch, so the database never has to lock all of them at
   once.

```sql
DO $$
DECLARE
  cutoff constant timestamptz := TIMESTAMPTZ '2024-01-01';
  tbl text;
  step timestamptz;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['events', 'measurement_events', 'location_events',
                             'alert_events', 'event_anchors', 'state_change_events'] LOOP
    LOOP
      SELECT max(range_end) INTO step FROM (
        SELECT range_end FROM timescaledb_information.chunks
        WHERE hypertable_schema = 'event-management' AND hypertable_name = tbl
          AND range_end <= cutoff
        ORDER BY range_end LIMIT 100) oldest;
      EXIT WHEN step IS NULL;
      PERFORM drop_chunks(format('%I.%I', 'event-management', tbl)::regclass, older_than => step);
      COMMIT;
    END LOOP;
  END LOOP;
END $$;
```

Then count the chunks again, and upgrade. If you cannot do without those events, recreate the
instance as in [Check the row count](#v0190-row-count).

##### 3. Run the upgrade where the instance was bootstrapped {#v0190-where}

`dcctl upgrade` now applies the instance's OpenTofu configuration, so it needs the instance's
state: run it on the machine that bootstrapped it, or copy `~/.devicechain/instances/<instance>/`
there first and keep it private (mode `0700`), because it holds credentials. It also needs `tofu`
on `PATH`, at OpenTofu 1.9 or later (or Terraform 1.9 or later). Without the state, the upgrade
refuses and changes nothing. `--skip-infrastructure` moves only the services, as before, and says
what it left.

##### 4. Make room {#v0190-room}

Once upgraded, an instance installed without `--compact` requests more CPU, and the rolling update
needs more still while old and new pods run side by side:

| | More CPU requested once upgraded | Free CPU the services' rolling update needs at once |
| --- | --- | --- |
| `--ha` | 4.45 for the services, plus 500m and 768Mi of memory on each of the three nodes running a NATS server | 4.85, beyond the NATS servers': 4.55 for the pods that start beside their old ones (`device-management`, both `event-management` pods, `device-state` and `event-sources`), and 0.3 for `event-processing`, whose new pod starts after its old one stops |
| without `--ha` | 3.55 for the services, plus 500m and 768Mi for the one NATS server | 3.95 (3.65 and 0.3), beyond the NATS server's |
| `--compact` | none for the services; 25m and 64Mi on each node running a NATS server | 0.1, as before (25m for each pod that starts beside its old one), beyond the NATS servers' |

These figures are for an instance that runs all five of these services. The services' new requests
are `device-management` 800m, `event-management` 900m (two pods under `--ha`, which also adds the
second pod's 128Mi of memory), `device-state` 950m, `event-sources` 1 core and `event-processing`
400m; in `v0.18.0` each requested 100m. Each new pod needs its whole request free on a single
node. Each NATS server now requests 500m of CPU and 768Mi of memory and is limited to 2Gi of
memory; before, it requested nothing. Compare the nodes' free allocatable CPU and memory
(`kubectl describe nodes`, "Allocated resources") with [Service sizing](./bootstrap.md#service-sizing).
A kind cluster on a laptop is the most likely not to have it.

A pod that cannot be placed stays `Pending`, and the upgrade fails after waiting (up to 15
minutes for the broker), with the instance partly upgraded: services whose new pods started are
on the new release, and the rest are still on the old one. A NATS server left `Pending` without
`--ha` leaves the broker down until there is room. Make room and run `dcctl upgrade` again to
finish. An instance built with `dcctl` has no way to keep the old requests; the remedy is
capacity, or recreating it with `--compact`. Instances installed with `--compact` keep their 25m
and 64Mi requests, and their NATS servers request the same.

A `ResourceQuota` or `LimitRange` on the instance's namespace can refuse the larger requests,
the 2-core limits of `event-sources` and `device-state`, or `event-processing`'s 1-core limit.

##### 5. Check for a quota on cross-namespace placement {#v0190-quota}

The database pods now carry a placement preference that looks at other namespaces
([Database primaries](#v0190-primary-spread)). A `ResourceQuota` with the
`CrossNamespacePodAffinity` scope refuses such pods, preferred or not: the relational database's
restarted instances in `dc-system`, and each instance's event store in its own namespace. Nothing
DeviceChain installs creates one. To list any that exist:

```bash
kubectl get resourcequota -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,SCOPES:.spec.scopeSelector
```

An empty list does not settle it. The API server's quota admission configuration can name
`CrossNamespacePodAffinity` under `limitedResources`, and then such pods are refused in every
namespace that has **no** quota with that scope admitting them. That configuration lives in the
control plane, so ask whoever runs the cluster; if it is set, give `dc-system` and each
instance's namespace a quota with that scope before upgrading. A refused event-store pod leaves
the store short of an instance, or, without `--ha`, down.

##### 6. Check the settings that now stop a service from starting {#v0190-settings}

- If you set `tsdbConfiguration.maxOpenConnections` for `event-management`, or
  `rdbConfiguration.maxOpenConnections` for `device-state`, to `10` or less and did not set
  `persistence.writers` or `projection.writers`, the new pod refuses to start, naming the
  setting and the pool size. Set the writers below your pool (the previous default was `5`), or
  remove the pool setting to use the default of 20. Pools of 11 to 19 start and log that more
  than half the pool is given to writers.
- **Analytics readers** (`--ha` instances): their `connection_limit` values together may now
  take at most 17 of the event store's connections, not 57, because the platform keeps 80 for
  two `event-management` pods instead of 40. Above 17 the upgrade stops before it changes
  anything. If you raised `timescale_analytics_reserved_connections` to run a second
  `event-management` pod yourself, set it back to 40: it is now the reserve for each pod. See
  [Connection cap](../guides/sql-and-bi-access.md#connection-cap).
- **`maxReadingsPerMessage`** is retired: the service ignores it with a warning. See
  [256 readings](#v0190-reading-limit) to find the devices it would refuse.

##### 7. Check what your devices send {#v0190-device-traffic}

- **More than 256 readings in one message** are refused. Find those devices as
  [256 readings](#v0190-reading-limit) shows.
- **More than 1,000 readings a second from one tenant** is now over the default ceiling, which
  counts readings rather than messages. Find those tenants and raise the ingest rate on their tier
  first, as [Readings, not messages](#v0190-ingest-readings) shows. Readings over the ceiling are
  dropped, not delayed: HTTP answers `429`, MQTT, LwM2M and Sparkplug devices are not told, and an
  MQTT message dropped this way was already acknowledged.
- **Readings dated more than 366 days before they arrive** are refused. A device with no clock
  should leave `occurredTime` out. Messages already waiting in the platform are judged against the
  time they arrived; an edge agent's spool has no age limit of its own
  ([Old readings](#v0190-event-age-limit)).

##### 8. Rename `backup_retention` {#v0190-rename-retention}

If you set it: `backup_retention_rdb` in the cluster configuration, `backup_retention_tsdb` in the
instance configuration. A `-var backup_retention=…` is refused; a `.tfvars` line draws only a
warning and the default is used instead of your value; a `TF_VAR_backup_retention` is ignored
without a warning.

##### 9. Check the backup store's headroom {#v0190-headroom}

The relational database now keeps 30 days of backups instead of 7
([Recovery windows](#v0190-backup-retention)), and an existing store keeps its size, so a store
created before this release is still 20 GiB unless you grew it. Grow it first if it has little
room, as [Backup store size](./bootstrap.md#backup-store-size) shows.

##### 10. Keep the OpenTofu values you set yourself {#v0190-tfvars}

The upgrade keeps a value declared in a `terraform.tfvars` beside the instance's state
(`~/.devicechain/instances/<instance>/infra/instance/`) or in `TF_VAR_<name>`, and not one you
passed with `-var`. It refuses rather than shorten the event store's recovery window or stop
declaring an analytics reader, and warns for a broker request or limit it would lower. Move such
values into that file first. See
[Values you set on the configuration yourself](#upgrade-infrastructure).

##### 11. If you replaced the event store's database image {#v0190-image}

Check it can use `lz4` for [write-ahead log compression](#v0190-wal); an instance that restarts on
an image that cannot may not start. Upgrade only if every line prints `t`:

```bash
pods=$(kubectl -n dci-<instance> get pods -l cnpg.io/cluster=dc-tsdb,cnpg.io/podRole=instance -o name)
for p in $pods; do
  kubectl -n dci-<instance> exec "$p" -c postgres -- psql -U postgres -tAc \
    "SELECT 'lz4' = ANY (enumvals) FROM pg_settings WHERE name = 'wal_compression'"
done
```

##### 12. If an event source reads from your own MQTT broker {#v0190-mqtt-client-id}

Each such source now connects as `devicechain:<instance>:<source>:<pod>` instead of `devicechain`
([Its own client id](#v0190-external-mqtt-client-id)). If your broker's ACL, allowlist or client-id
rule names `devicechain`, allow ids that begin with `devicechain:`. The id is also longer than 23
characters, the shortest limit MQTT requires a broker to accept. A broker that refuses the new id
makes the new pod fail to start with the broker's reason in its log, and the upgrade stalls with
the old pods still running and reading the source as before.

To avoid storing messages twice while the upgrade rolls (see [During the upgrade](#v0190-during)),
plan to pause the publishers on your broker for the length of the rollout.

#### During the upgrade {#v0190-during}

**`dcctl install`** gives the shared relational database a 30-day recovery window, zstd for its
archived log and the placement label, and restarts its instances once: under `--ha` the standbys
first, then a switchover to one of them on another node. A single-instance relational database is
unavailable until it has restarted; writes made meanwhile are retried.

**`dcctl upgrade`** then applies the instance's NATS servers and event store before it moves the
services ([details](#v0190-upgrade-infrastructure)):

- The NATS servers restart for their new requests and limit, one at a time under `--ha`. Without
  `--ha` the broker is unavailable while its one server restarts, usually about a minute. Under
  `--ha`, HTTP ingest can briefly answer `503` while a server is replaced; retry it as for any
  `503` ([quality of service](../guides/connecting-a-device.md#quality-of-service)).
- Without `--ha`, detection stops while the broker restarts, for about that minute plus up to 25
  seconds, and then replays from its last checkpoint
  ([Detection's partition](#v0190-detect-lease-release)).
- On an instance created by `v0.18.0`, the event store's instances can restart once for the
  placement preference: standbys first, then a switchover under `--ha`. Without `--ha`, events are
  not stored until the one instance is back, and wait in the ingest stream meanwhile. Its
  write-ahead log compression and zstd archiving need no restart.
- The broker and event store can restart at the same time; the upgrade waits for both.

**While `event-sources` rolls, a source on your own MQTT broker can be read twice.** Until the last
pod of the previous release stops, it still reads the source under the old id, beside the new pod
that holds the source, and each stores what it receives. Messages arriving in that window can be
stored twice. This happens once, and the same happens on a rollback; pausing the publishers on
your broker for the length of the rollout avoids it. Leases a rolled-back release leaves behind
expire on their own within 30 seconds.

**The new `event-management`, on its first start,** removes sixteen indexes and rebuilds the
event store's keys, one table at a time ([The event store's keys](#v0190-event-store-keys)).
While it works on a table, reads and writes of that table wait: a few seconds in the expected
case, and at most 45 seconds a table for the key rebuild. The previous `event-management` keeps
receiving and storing events; a batch held long enough is delivered again and stored once. Under
`--ha`, the pods roll one at a time, and whichever new pod starts first does the work before it is
ready. A second new pod that starts while that work is still running waits for it and may be
restarted once by its startup check; one that starts after it has nothing to wait for. Both are
expected. If the work stops (a table kept busy,
a minute run out, too many rows, more than 500 chunks, a table that takes more than 40 seconds),
`event-management` stops with an error that names the table and what to do, every other service
runs the new release, and the previous `event-management` keeps storing events. See
[If the event store's change stops](#v0190-event-store-keys-stops).

##### If the event store's change stops {#v0190-event-store-keys-stops}

- **Too many rows:** `event-management` stops before changing anything, and says why.
  `dcctl upgrade` reports that it did not finish rolling out; recreate the instance as in
  [Check the row count](#v0190-row-count).
- **A table with more than 500 chunks:** `event-management` stops before locking anything; its log
  names the table, its chunk count and the query in [Count the chunks](#v0190-chunk-count). That
  start changes nothing and marks nothing (its log lists what earlier starts already did): once the
  chunks are removed, the next start goes ahead.
- **A table busy, or the minute run out:** each lock attempt gives up after at most 5 seconds and a
  busy table is retried every 2 seconds for up to a minute. `event-management` then stops with an
  error that names the table, lists what is already done, and carries a query that lists the
  sessions holding it; it continues from there when it restarts. If it keeps stopping, look for
  long-running SQL or BI queries, a tenant erasure, or the store's compression or retention job.
- **One table's key rebuild takes longer than 40 seconds** once locked: it is undone, the table
  keeps its previous key, and `event-management` stops at once on every later start without touching
  it, so ingest does not stall on each restart. Recreate the instance, or to try once more (for
  example on faster storage), run the `COMMENT ON INDEX` statement the error gives. If the table
  holds events dated far in the past, removing them as in [Count the chunks](#v0190-chunk-count)
  shortens the rebuild; then run the `COMMENT ON INDEX` statement.
- **Out of lock slots:** removing an index or rebuilding a key locks every chunk of its table; the
  error says so and names the setting to raise.

#### After the upgrade {#v0190-after}

- **Check where the two database primaries are.** The switchovers above can leave both on one node.
  [Where the database primaries run](./bootstrap.md#ha-database-primaries) shows how to check and
  move one.
- **Watch the backup store for three weeks.** Nothing ages out of the relational database's new
  30-day window until it is 30 days old, so its share of the store grows for about three weeks,
  toward about four times the log it keeps today plus about 23 more nightly base backups, then
  levels off.
- **Watch the readings refused at each tenant's ceiling:**
  `devicechain_eventsources_total_readings_rate_limited`,
  `devicechain_lwm2mingest_ingest_samples_shed_total` and
  `devicechain_sparkplugingest_ingest_samples_shed_total`. A tenant that sheds there is sending
  more readings than its tier allows. Raise its tier's ingest rate, or have its devices send less.
- **An existing instance keeps its volume sizes.** The upgrade says that this release creates a
  32Gi event store; to grow an 8Gi one, see [Event store volume](./bootstrap.md#event-store-volume).
  An existing backup store keeps its size too.

Going back to `v0.18.0` needs no data change: it stores and reads events with the new keys and
without the removed indexes, and returns `event-management` to one pod and the ingest ceiling to
counting messages. One read is slower there: `v0.18.0` lists the events recorded against a
relationship anchor by reading through the tenant's events in the time range asked for, compressed
ones included. A `device-management` configuration that sets `inMemoryCache.perDeviceCacheEntries`
or `inMemoryCache.perDeviceCacheMiB` must drop them first: `v0.18.0` refuses to start with either.

#### What changed {#v0190-what-changed}

##### Ingest {#v0190-ingest}

###### A full ingest stream refuses new events instead of discarding unread ones {#v0190-backpressure}

When a consumer fell so far behind that its unread backlog filled `inbound-events` or
`resolved-events`, the stream discarded its oldest events to make room, and those were events
nobody had processed yet. The device had already been told they were accepted.

Now, when `device-management`'s unread backlog on `inbound-events`, or `event-management`'s on
`resolved-events`, reaches 90% of what the stream can hold, the platform stops accepting new
events until the backlog drops below 80%. The two streams keep their week of already-processed
events, and that history does not count towards the 90%: only unread events do. A full stream also
refuses before it would discard that history fast enough to reach unread events; see
[A full stream also refuses before large events push out unread ones](#v0190-ingest-history-runway).

- **HTTP** ingest answers `503` with `Retry-After: 10` while it refuses. Retry on `503`. A `503`
  without a `Retry-After` still means the publish itself failed. A request over its tenant's own
  ceiling gets `429` first; only one within it can get the `503`
  ([Readings, not messages](#v0190-ingest-readings)).
- **MQTT** devices were already acknowledged by the broker. Their messages wait in the capture
  stream until ingest resumes.
- **Sparkplug and LwM2M** readings, and messages from an external MQTT broker, are dropped and
  counted, because those protocols give the platform no way to make the device retry. Connect and
  disconnect transitions are still accepted. The backpressure gate does not limit how many it
  admits (the tenant ceiling still applies to the broker tap), so a fleet that reconnects in a loop
  can still push the stream to its ceiling, where it discards its oldest events as before.
- The refusal applies to **every tenant**, because the streams are shared. A slow `device-state`
  or `event-processing` does not cause it.
- The simulator and load harness count a `503` with a `Retry-After` as a refusal, not a failure.
  See [the load-test reports entry](#v0190-loadtest-refusals) for how they report it.

See [Backpressure on the ingest path](./observability.md#ingest-backpressure). No stream is
reconfigured, and a service still on the previous release keeps its previous behaviour until it is
upgraded.

###### A full stream also refuses before large events push out unread ones {#v0190-ingest-history-runway}

The refusal above priced every unread event at the stream's average size, because the broker
reports a stream's total bytes and not those of the events a consumer has not read. On a full
stream that is the unread share of the event COUNT. A burst of large events over a history of
small, already-processed ones could therefore fill `inbound-events` or `resolved-events` while the
unread backlog read about half of the ceiling, and the stream then discarded events nobody had
processed. Measuring after every publish would not have caught it.

- **The stream also refuses when its processed history is running out.** A full stream discards
  its oldest events first, so the events already processed that it still holds ahead of the
  unread ones are what runs out before an unread one is lost. The services that write to the
  stream now refuse new events when that history would all be discarded within 30 seconds at the
  rate the stream is discarding it, and accept them again once it would last a minute, or the
  consumer has read everything. Both are built from sequence numbers the broker reports, so the
  rule does not depend on how large the events are. A full stream whose consumer keeps pace
  refuses nothing. Deleting a tenant does not by itself make a stream refuse either, unless its
  events are the oldest a full stream holds: then the stream can refuse, losing nothing, until the
  consumer catches up. See [Backpressure on the ingest path](./observability.md#ingest-backpressure)
  for that case and for a full stream that holds under a minute of its own traffic.
- **An event the consumer received but has not acknowledged counts as unread**, including one
  waiting to be delivered again after a failure. While such an event sits near the front of a full
  stream, the stream can refuse new events until it is acknowledged or given up on.
- **Each service also measures the stream as soon as it has itself written about a thousandth of
  the stream's ceiling** since its last measurement, at most every 100 ms, as well as every
  5 seconds.
- A new series, `devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`,
  shows how long that history would last. `JetStreamIngestBackpressureEngaged` fires for this
  refusal too, and its description now says how to tell the two causes apart.
- An unread event can still be lost when the services writing to a stream use up, between two of
  their measurements, more processed history than the stream was seen to discard in the 30 seconds
  before, or when a stream reaches its ceiling for the first time holding almost no processed
  history.

No stream is reconfigured and no setting is added.

###### A tenant's ingest ceiling counts readings, and HTTP checks it before the shared backpressure gate {#v0190-ingest-readings}

The per-tenant ingest ceiling now counts **readings** on every device transport. A reading is one
stored value: one key of a measurement entry, one location or one alert. Before, HTTP and MQTT
charged one unit per message however many readings it carried. A tenant at the default of 1000 per
second could therefore send up to 256,000 readings per second, far more than an installation can
store, and the backpressure gate then refused every tenant's events.

- **HTTP and MQTT:** a message is charged once before it is decoded, as before, and then once per
  reading it carries. Over HTTP, a tenant over its ceiling gets `429`. Over MQTT, the message is
  acknowledged and dropped, as an over-ceiling message already was. Such a message is counted in
  the new `devicechain_eventsources_total_msg_reading_limited` and its readings in the new
  `devicechain_eventsources_total_readings_rate_limited`. It was received, so it is also counted in
  `devicechain_eventsources_total_inbound_messages`, and not in
  `devicechain_eventsources_total_msg_rate_limited`, which still counts only messages shed before
  decode.
- **HTTP checks the tenant's own ceiling first.** A request over its tenant's ceiling is answered
  `429` even while the platform is refusing events. Only a request within its tenant's ceiling can
  get the backpressure `503`. The request body is now read and decoded before the backpressure
  check, and a request refused by backpressure is now counted as received.
- **External MQTT brokers** check the tenant's ceiling before the backpressure gate too, so a
  message over its tenant's ceiling while the platform is refusing is counted in
  `devicechain_eventsources_total_msg_rate_limited` rather than
  `devicechain_eventsources_total_msg_backpressured`.
- **LwM2M** already charged readings, at 25 times the ceiling. It now charges them at the ceiling
  itself, so a tenant's LwM2M reading budget is **25 times smaller** than before.
- **Sparkplug now has a per-tenant ingest ceiling**, counted in readings:
  `ingestRateLimit.messagesPerSecond` (default 1000) and `ingestRateLimit.burst` (default 2000).
  The readings of each DATA message over it are dropped and counted in
  `devicechain_sparkplugingest_ingest_samples_shed_total`. Births, deaths and the message rate
  are not metered. See [What Sparkplug ingest bounds](./edge-services.md#unbounded-sparkplug-ingest).
- **The default stays 1000 per second with a burst of 2000, now in readings.** A fleet that sends
  one reading per message sees no change. A fleet that batches several readings into a message is
  now limited to 1000 readings per second per tenant. If that traffic is legitimate, raise the
  ingest rate on the tenant's tier. The console now shows the ingest rate in readings per second.

At the default, a tenant's own devices are admitted at most 5,000 readings per second, even with a
backlog draining and both edge services running, which is below the 6,000 per second a default
high-availability installation was measured to store, with `event-management` at two pods, on the
cluster described in [what the default allows](../concepts/governance.md#ingest-default). On a
smaller cluster, or with more than one `event-sources` replica, lower the default.

To find the tenants that send more than 1000 readings per second before you upgrade:

- For JSON devices over MQTT and HTTP, count the tenant's stored measurements per second over a
  busy hour.
- For LwM2M, compare the tenant's current reading rate with its ceiling, not 25 times its ceiling.
- For Sparkplug, `rate(devicechain_sparkplugingest_measurements_emitted_total[5m])` is the
  readings of the tenants on that pod's sources, since each source belongs to one tenant.
- An [edge agent](./edge-services.md) re-publishes a site's buffered readings as fast
  as it can after a reconnect. A site that batches several readings into a message can exceed its
  tenant's ceiling while it catches up.

Going back to the previous release restores per-message charging. No stored data or schema
changes, and no configuration key is renamed.

###### An event carries at most 256 readings, and gateways split larger messages {#v0190-reading-limit}

**One event now carries at most 256 readings, on every transport, and the limit is not
configurable.** A reading is one metric value of a measurement, or one location or alert entry.
Before, the JSON device event on HTTP and MQTT accepted up to 1000 by default, and an operator
could raise or lower that.

To find the devices it affects before you upgrade, set `maxReadingsPerMessage: 256` in the
event-sources configuration on `v0.18.0` and watch `total_msg_too_many_readings`: every message it
counts is one this release refuses. Change those devices to send at most 256 readings per message.

- **HTTP and MQTT:** a message with more than 256 readings is refused whole, never trimmed. HTTP
  answers `400`, naming the count and the limit. On MQTT the device is not told, because the broker
  acknowledges before decoding; the refusal is counted on `total_msg_too_many_readings` and the
  message goes to the failed-decode stream. Messages captured before the upgrade and decoded after
  it, including any still spooled at an edge agent, are judged on the new limit.
- **`maxReadingsPerMessage` is retired.** The service starts, logs a warning and ignores it; a
  value lower than 256 is no longer honoured either. Remove the key.
- **Sparkplug B:** a message with more than 256 metric values used to become one event. It now
  becomes consecutive events of at most 256, each value at its own timestamp. Queries that count
  *events* see more of them; the stored readings are the same. Rules see each event on its own, so
  a hold-time or absence rule can now fire between two events of one wide message.
- **LwM2M:** a Notify with more than 256 numeric values used to keep the first 256 and drop the
  rest. It is now stored as several events, and the tenant's sample budget is charged one event at
  a time; events it cannot admit are counted on `ingest_samples_shed_total`.
  `notify_samples_truncated_total` is removed: take it out of any dashboard or alert.

###### An event already queued with thousands of readings is stored {#v0190-large-events}

An event with more readings than fit in one database statement (more than about 5,950
measurements, 5,450 locations or 6,550 alerts, or 9,350 relationship anchors) could never be
stored. The database driver refused the statement, `event-management` retried the event until
its deliveries ran out, and then recorded it on the `failed-events` stream as a downstream
failure rather than as a problem with the event. Before the
[256-reading limit](#v0190-reading-limit), such an event could come from a Sparkplug message with
thousands of metrics, or from a JSON transport whose `maxReadingsPerMessage` had been raised above
its default of 1000, and one may still be waiting in a stream at the upgrade. The event store now
writes a large event in as many statements as it needs, inside the same transaction, so it is
stored whole or not at all like any other, and a redelivery of it adds nothing. The same
retry-then-downstream-failure path was taken by a state-change event whose session id is too large
for the database's signed 64-bit column; that event is now recorded as invalid on its first
delivery.

###### A reading dated more than 366 days before it arrives is refused {#v0190-event-age-limit}

A reading's `occurredTime`, on the envelope or on any entry, may now be at most 366 days before the
platform received it. An older one is refused, never moved to a later time:

- **HTTP and MQTT:** the whole message is refused, as for every other timestamp refusal. HTTP
  answers `400`, and an MQTT publish is dead-lettered. Both are counted in
  `total_msg_invalid_event_time` on `event-sources`.
- **Sparkplug and LwM2M:** only the readings that are too old are dropped, and the rest of the
  message is stored. A gateway's message is assembled by the gateway, not sent by one device as a
  unit, and a Sparkplug metric's timestamp is when its value last changed, so a value unchanged for
  more than 366 days is dropped from every birth until it changes. A node with no clock set, which
  reports times near 1970, has all its readings dropped. They are counted in
  `samples_too_old_dropped_total` on `sparkplug-ingest` and `telemetry_too_old_dropped_total` on
  `lwm2m-ingest`, and the service logs a warning naming the device.
- Any other event dated that far back is dead-lettered with reason `Invalid` on its first delivery,
  and counted in `resolve_event_time_too_old_total` on `device-management`.

Before, any past time was accepted, and the event store keeps one partition (chunk) per chunk
interval, a day by default. A device could create a partition for every day back to year 1, and
every later operation that touches each partition, such as an upgrade that rebuilds a key, slowed
down with them. [Count the chunks](#v0190-chunk-count) says what this release's upgrade does about
a table that already has too many.

- A device with no clock should leave `occurredTime` out; the platform then dates the reading when
  it arrives. The epoch, `1970-01-01T00:00:00Z`, is refused.
- Messages already waiting in the platform at the upgrade are judged against the time they arrived,
  so one dated more than 366 days before that is refused by the new release.
- The edge agent's spool has no age limit of its own; see [the spool](./edge-services.md#the-spool).
- The limit is fixed. There is no setting for it.

###### An MQTT source on your own broker connects under its own client id, from one pod at a time {#v0190-external-mqtt-client-id}

An event source that reads from an MQTT broker you run connected with the client id `devicechain`,
whatever instance, source or pod it belonged to. A broker keeps one session per client id and drops
the older connection when a second one arrives. So whenever two connections used that id, each took
the session from the other in a loop, and messages that arrived while either side reconnected were
lost without being counted. That happened with two `event-sources` pods, during every rolling
update (the new pod starts before the old one stops), with two such sources on one broker, and with
two instances reading one broker. The default install's own gateway source reads the platform's
stream and was not affected.

Each source now connects as `devicechain:<instance>:<source>:<pod>`, and only one pod reads a given
source at a time. The pods agree on which one through a lease held in the platform's message
broker. The others connect nothing for that source until it is released. See
[Transport matrix](../reference/transport-matrix.md#external-mqtt-broker). What to allow on your
broker first is in [step 12](#v0190-mqtt-client-id), and the one double read during the rollout
under [During the upgrade](#v0190-during).

- A pod that stops hands the source to another pod within about two seconds. After an abrupt loss
  (a node failure, a `SIGKILL`, an out-of-memory kill), the source is unread for about 30 seconds
  plus the time to reconnect. The source remains at-most-once: your broker does not keep what it
  delivers in either gap.
- A pod that takes a source over and cannot reach your broker releases it and tries again every
  15 seconds, instead of stopping the service. A broker that refuses the subscription still stops
  the service.
- Running more `event-sources` pods no longer means more readers of an external source. It did not
  add throughput before either, because the pods took one session from each other. It now adds
  standbys.
- New gauge: `devicechain_eventsources_external_mqtt_owner{source}`, 1 on the pod reading the
  source and 0 elsewhere, and a new counter, `devicechain_eventsources_total_msg_not_owner{source}`,
  for messages a pod drops because it has just lost the source. New alert:
  `ExternalMqttSourceNotReadByOnePod` (warning), when a source has been read by no pod, or by more
  than one, for two minutes. See [An external MQTT source nobody reads](./observability.md#external-mqtt-owner).

##### Resolution {#v0190-resolution}

###### device-management answers repeated lookups from memory {#v0190-local-cache}

- **Each `device-management` replica keeps what it read from its key-value caches in memory for up
  to five seconds**, and answers repeated lookups for the same device, device type or tenant from
  there. A cache time to live below five seconds shortens it too.
- **A change can take up to five seconds longer to reach events other replicas resolve.** A device
  deleted, or re-created under the same token, can still resolve through its old record on another
  replica for those seconds, and a rule whose group scope just changed can be evaluated there
  against the previous scope. An alarm edge for a just-deleted device is still dropped at once on
  every replica.
- **Up to 131,072 entries or 24 MiB per replica** for each of the three caches kept per device (a
  device by token, its tracked relationships, its group memberships); 4,096 entries or 4 MiB for
  the per-device-type and per-tenant caches. `inMemoryCache.perDeviceCacheEntries` and
  `inMemoryCache.perDeviceCacheMiB` change the bound; raise the service's memory limit with them.
- **A device that reports less often than every five seconds is not answered from memory**,
  however large the cache.
- **An event's profile, relationships and group-scope lookups are made at the same time**, and so
  are its group-membership lookups, so an event that misses memory waits for one round trip to
  NATS instead of three. The database is still read one lookup at a time, so `resolution.workers`
  still counts connections as before.
- **New metrics** count lookups answered from memory, entries dropped, size and bounds
  (`kv_cache_local_max_entries`, `kv_cache_local_max_bytes`); `kv_cache_request_duration_seconds{op="get"}`
  now counts only the lookups memory could not answer. See
  [Caches that stop answering](./observability.md#kv-caches).

###### device-management checks a repeated device credential from memory {#v0190-credential-cache}

- **Each replica keeps a device credential it has just verified in memory for up to five seconds**,
  and checks the device's next events against that copy instead of reading the database. The copy
  is checked exactly as the stored credential is: an `MQTT_BASIC` password is still compared on
  every event, and an expiry still takes effect at its time. A credential that failed to verify is
  never kept.
- **A revocation can now take up to five seconds on a replica that missed the news.** Disabling,
  deleting, re-pointing or otherwise changing a credential, and replacing, editing or deleting its
  device, drop the copy on the replica that made the change before it returns, and tell the other
  replicas to drop theirs. If that message is lost (a replica reconnecting to NATS, or replicas
  still on the previous release during the upgrade), that replica's copy expires within five
  seconds. Until now a revocation took effect on the next event on every replica. See
  [How quickly a revocation takes effect](../guides/device-credentials.md#revocation-timing).
- **MQTT connects still read the database every time**, so a revoked credential cannot open a new
  connection on any replica.
- **At most 65,536 credentials or 16 MiB per replica**, a fixed bound; at the defaults the
  service's in-memory caches hold at most 96 MiB rather than 80. New metrics count checks answered
  from the copy, credentials dropped, its size, and the drop messages sent and received.

##### Storage {#v0190-storage}

###### The event store's keys lead with time, and sixteen indexes are gone {#v0190-event-store-keys}

The keys that stop an event being stored twice now start with the tenant and the event's time,
instead of the tenant and a digest of the event, so a new key lands next to the previous one and
the database rewrites far fewer index pages. Sixteen indexes are removed: twelve that repeated
another index or were read by no query, and the tenant-and-time indexes of base events,
measurements, locations and alerts, which the new keys now answer. Each stored row updates fewer
indexes than in `v0.18.0`: a base event row two instead of five (three instead of six with an
alternate id), a measurement row three instead of five, a location or alert row one instead of
four, a relationship-anchor row two instead of four, and a presence-change row one instead of four.
A measurement event with one reading and no anchors updates five indexes instead of ten.

If `event-management` refused to start because a table has more than 500 chunks, the batched
`drop_chunks` procedure its log points to is under [Count the chunks](#v0190-chunk-count).

- Events are still stored once, and every read `event-management` serves is still served by an
  index.
- **A device's event list does more work on recent data.** The total shown with the list, and a
  list filtered by event type, now visit every one of the device's rows that is not yet compressed
  (the last week, by default), including the rows of a device with the same token in another
  tenant. When the store holds few distinct device tokens (in our tests, 40 rather than 100), the
  first page for a device that has been quiet while others kept sending can take noticeably longer.
- **SQL and BI access.** A query on a tenant's events, readings, locations, alerts or anchors that
  filters on time alone is served by an index. A time-only query on
  `analytics.state_change_events` reads all of your tenant's rows in each uncompressed chunk it
  touches; add `device_token` to use an index. To join `analytics.event_anchors` to
  `analytics.events`, join on both `event_id` and `occurred_time`. See
  [Practical notes](../guides/sql-and-bi-access.md#practical-notes).

###### Batches are written in fewer statements {#v0190-batch-writes}

- **`event-management` writes each batch with one statement per table for each tenant in it**,
  inside the same transaction, instead of several per event. What is stored is unchanged, an event
  is still acknowledged only after its batch commits, and a redelivered event adds nothing. When
  the database refuses a row in a statement that carries several events, the batch is written again
  one event at a time to find it; `persist_batch_fallbacks_total` counts that. Connect and
  disconnect events are still written one at a time. See
  [Event persistence](./observability.md#event-persistence).
- **`device-state` writes the state of every device that already has one in a single statement for
  each tenant in the batch.** A device seen for the first time is still created on its own, and
  `updatedAt` still advances whenever its state is written.
- **Every pooled database connection now stays open between uses**, up to the pool size; before,
  only half the pool did, and a service using more than half reconnected for every query. A
  connection is still closed an hour after it was opened. The database can show more idle
  connections from each service after a busy period; at the default configuration the
  [connection budget](./bootstrap.md#connection-budget) covers it, but if you run a service at
  `replicas` above 1 or raised its `maxOpenConnections`, check the instance's connection limit.
  `maxIdleConnections` is still honoured when set.

###### New pipeline defaults {#v0190-pipeline-defaults}

| Setting | `v0.18.0` | `v0.19.0` |
| --- | --- | --- |
| `event-management` `persistence.writers` | 5 | 10 |
| `event-management` `persistence.maxBatch` | 32 | 64 |
| `event-management` `persistence.lingerMillis` | 0 | 10 |
| `device-state` `projection.writers` | 5 | 10 |
| `event-processing` CPU limit | 500m | 1 core |
| `event-management` pods under `--ha` (not `--compact --ha`) | 1 | 2 |

An installation that sets any of these keeps its value; `persistence.lingerMillis: 0` still means
no wait. A writer that finds fewer events than a full batch now waits up to 10 milliseconds for
more, so below a few hundred events per second per pod each event is committed up to 10
milliseconds later than before and `persist_duration_seconds` rises by about that much; under a
backlog nothing changes. In the release benchmark at 3,000 events per second, transactions per
stored event fell by 37% (from 0.126 to 0.080), the median time to store an event rose by about 5
milliseconds, and the slowest 1% fell by 18%. That comparison also changed the event store's keys
and its archive compression, the services' CPU requests and their placement, `device-state`'s writers and detection's CPU limit, and the NATS servers' requests and memory limit, so it does not isolate the wait. In a five-minute run at 6,800 events
per second offered, the path that commits one event at a time took 4.8% of `event-management`'s
CPU, at 0.050 transactions per event, against 24% in the previous benchmark, on the build before
the wait and on different service nodes. `--compact` installations get the same defaults except
the second pod; their requests do not change.

**Two `event-management` pods under `--ha`.** On a three-node services pool, one node also runs the
NATS server that leads the incoming-event stream. In the release benchmark the scheduler put the
single `event-management` pod there with `device-state`; that node ran at 94 to 95% CPU, and with
one pod, storage fell to 6,592 events a second at 6,800 offered, the first stage to fall behind. A
second pod lets storing use another node's CPU. The figure under [Performance](#v0190-performance)
was measured with two pods. The pods prefer different nodes,
but that is a preference, not a guarantee. Each fills its own batches, so the event store commits
about twice as many transactions per event; its node stayed below 70% CPU. If you install the chart
yourself, its default is still one replica: set `functionalAreas.event-management.replicas: 2`,
and on an event store built from this repository's OpenTofu set `event_management_replicas = 2`,
which reserves the second pod's connections.

##### Sizing and placement {#v0190-sizing}

###### The event-path services request what they use, and spread across nodes {#v0190-service-sizing}

The five services that handle every event request the CPU they were measured to use at 6,000
events per second (values under [Make room](#v0190-room)); before, at 100m each, they requested far
less than that, and the scheduler, which places pods by their requests, put the busiest together.
`event-sources` and `device-state` may now use up to 2 cores, like `device-management` and
`event-management`. The five prefer different nodes (on three nodes, usually no more than two to a
node); `functionalAreas.<service>.eventPathSpread: false` turns that off for one service.
`device-management`, `event-management` and `event-sources` also prefer a node not running the
event store's primary. A preference applies only when a pod is scheduled: after a failover, a pod
already running on the new primary's node stays until it is next rescheduled.

A new instance's event store gets a 32Gi volume instead of 8Gi. Its NATS servers request 500m of
CPU and 768Mi of memory and are limited to 2Gi; before, they requested nothing, which made them the
first pods evicted when a node ran short of memory. The 2Gi limit was sized from steady ingest; a
server catching up after a restart was not measured, so if one is `OOMKilled` during the upgrade,
raise `nats_memory_limit` in the instance's `terraform.tfvars` and run `dcctl upgrade` again. See
[Service sizing](./bootstrap.md#service-sizing) and [The message broker](./bootstrap.md#broker-sizing).

###### A service that runs more than one pod keeps its pods on different nodes {#v0190-own-pods-apart}

Above one replica, each of the five event-path services (`device-management`, `event-management`,
`device-state`, `event-sources` and `event-processing`) prefers a node that is not already running
another of its own pods. A service with [the event-path spread](#v0190-service-sizing) no
longer gets the cluster's default spread, and this gives back the half of it that keeps one
service's replicas on different nodes; the other half, different zones, is not given back. It is a
preference the scheduler weighs with the service's others (fewer event-path services on a node and,
for `device-management`, `event-sources` and `event-management`, no event-store primary), not a
guarantee.

- Under `--ha`, it applies to `event-management`'s two pods.
- **If you install the chart yourself**, it applies to any of the five you run with `replicas`
  above one, and it sits in the pod's anti-affinity, beside the event-store primary preference on
  the three services that carry one. A service with `eventPathSpread: false` keeps the cluster's
  default spread instead, which also prefers different zones, and gets no such preference.

###### Your own chart values {#v0190-chart-values}

If you install the chart yourself:

- A top-level `resources.requests.cpu` no longer reaches `device-management`, `event-management`,
  `device-state`, `event-sources` or `event-processing`: their measured request wins. Set theirs
  under `functionalAreas.<service>.resources.requests`, or set `useMeasuredRequests: false` to apply
  the top-level requests to every service again. With `useMeasuredRequests: false`, a top-level
  request above 1 core now fails the render for `event-processing`, because it is above that
  service's own limit.
- A top-level `resources.limits.cpu` above 2 cores now lowers `event-sources` and `device-state` to
  2, and no longer reaches `event-processing`, which is always 1 core unless set under
  `functionalAreas.event-processing.resources.limits`.
- A service's own CPU limit below its measured request is now refused, naming `measuredRequests`.
- The chart's top-level values and each service's block under `functionalAreas` now refuse a key
  the chart does not read, so a misspelled key fails the render.
- `eventPathSpread` is on for the five services; setting it on another service spreads it with them.
- A service you run with `replicas` above one prefers nodes not running another of its own pods
  ([Own pods apart](#v0190-own-pods-apart)).

###### The database primaries prefer different nodes {#v0190-primary-spread}

Each database prefers a node that is not running another DeviceChain database's primary. In
testing on three 8-vCPU nodes, the node running both primaries was at 94 to 98% CPU while the other
two were at 45 to 51%. It is a preference: a smaller cluster still schedules every database
instance. It acts when a database pod is scheduled, so a failover or switchover can still put both
primaries on one node; see [Where the database primaries run](./bootstrap.md#ha-database-primaries).
A quota that limits cross-namespace placement can refuse these pods: see [step 5](#v0190-quota).

###### The databases can run on nodes you choose {#v0190-database-placement}

`dcctl install` takes `--database-node-selector` and `--database-toleration`. They place the shared
relational store and every instance's event store on labelled nodes, including nodes tainted to
keep other workloads off. `dcctl bootstrap` has no such flags: every instance follows the install.
Install refuses, before it installs anything, a placement with fewer usable nodes than a database
has instances, and each bootstrap checks again. NATS, the services and the backup object store are
not placed. Adding a placement to a cluster that already runs instances is refused; with none
running it moves the relational store, whose volumes may not follow. See
[Database placement](./bootstrap.md#database-placement).

##### Backups {#v0190-backups}

###### `dcctl destroy` removes an instance's in-cluster backups {#v0190-destroy-backups}

Once the instance's namespace is gone, destroy deletes everything under the path its event store
was archiving to in the cluster's own object store, and checks the path is empty. It reads and
prints that path before it changes anything. Backups in an object store you supplied are never
deleted: destroy prints where they are. Pass `--keep-backups` to keep the in-cluster backups too,
and do pass it before you rebuild an instance from its own backups with `--restore-tsdb-from`,
because a destroy without it deletes the archive that restore reads. If the store cannot be
reached, destroy still finishes and says what it left. An archive destroy did not write in this
run (one left by an earlier release, or kept with `--keep-backups` and then restored from) is
listed, not removed; [What happens to the instance's backups](./bootstrap.md#destroy-backups) shows
how to remove it. Destroy no longer prints OpenTofu's **Changes to Outputs** list, whose values were
the configuration's defaults rather than the instance's.

###### Backups that stop shipping now warn first {#v0190-backup-alerts}

`PostgresWALArchiveBacklog` fires when a database holds write-ahead log it has not shipped,
including when the archiver is slow or hung. `BackupDestinationFillingFast` and
`DatabaseVolumeFillingFast` fire on how fast the backup store or an event-store volume is filling.
See [Backups that stop shipping](./observability.md#backup-archiving).

###### The backup store is sized so the event store fills first {#v0190-backup-store-size}

A new cluster's in-cluster backup store is **160 GiB** instead of 20 GiB, and **20 GiB** instead of
8 GiB under `--compact` when TLS is kept. At 20 GiB, sustained ingest filled the store after 12 to
16 million events, well before a 32 GiB event store; archiving then stopped and the event store's
primary filled its own volume. The sizing assumes up to about 1.9 KB of archive per event for both
databases, measured before this release compressed the write-ahead log, re-keyed the event store
and archived with zstd, so it errs large: in the release benchmark the event store's archive alone
took about 0.47 KB per stored event. **An existing cluster keeps its store's size**: the volume is
sized only when it is created, and `backup_object_store_storage` on an existing store does nothing.
To give an existing cluster the new size, grow the volume on a StorageClass that allows expansion;
see [Backup store size](./bootstrap.md#backup-store-size).

###### Each database keeps its own recovery window: 30 days for core data, 7 for event data {#v0190-backup-retention}

`backup_retention` is replaced by `backup_retention_rdb` in the cluster configuration (default
`30d`) and `backup_retention_tsdb` in the instance configuration (default `7d`). The relational
database, which holds tenants, users, devices, rules, secrets and last-known state, can now be
recovered to any point in the last 30 days instead of 7, and a deleted tenant's core data stays
restorable for 30 days ([What is deliberately kept](./tenant-deletion.md#retained)). A window is a
whole number and `d`, `w` or `m`; anything else is refused before the apply. Where a retention
window bounds the event store, the sustained ingest rate that fills a 20 GiB store falls from about
19 to about 13 events per second, and the 160 GiB store's from about 150 to about 100. Those rates
come from the per-event cost measured before this release's log compression, new keys and zstd,
so on that count they err low. They also use the relational database's share of the log measured
once, about 14% for a small fleet reporting fast; for larger or slower fleets it is likely higher,
and if the relational log were all of it, the 160 GiB figure would be about 35. To keep 7 days, set
`backup_retention_rdb = "7d"` if you apply the OpenTofu configuration yourself; `dcctl install` has
no option for it. See [Recovery windows](./bootstrap.md#backup-retention).

###### Database base backups can be volume snapshots {#v0190-snapshot-backups}

On a cluster whose storage driver takes CSI volume snapshots, `dcctl install
--backup-snapshot-class <class>` takes each database's daily base backup as a volume snapshot
instead of a full copy in the backup store. Nothing changes unless you pass it. See
[Volume-snapshot base backups](./bootstrap.md#snapshot-base-backups).

- Log archiving is unchanged, and a full base backup still goes to the backup store every Sunday at
  04:00. Every restore reads the store, not the snapshots, so it can replay up to a week of log.
- The snapshots are kept at your cloud provider and outlive the cluster.
  [Volume-snapshot base backups](./bootstrap.md#snapshot-base-backups) says what to check before
  and after deleting one.
- The class must exist, have `deletionPolicy: Delete`, and belong to the driver that provisions the
  database volumes; install and each bootstrap check all three first. GKE and AKS include a
  snapshot controller; on EKS install the snapshot controller add-on first.
- The DeviceChain operator prunes old snapshots every ten minutes, keeping every snapshot inside
  the recovery window and the newest one before it. Its ClusterRole gains, in every namespace:
  `get` on namespaces; `get`, `list` and `delete` on CloudNativePG Backups; `get`, `list` and
  `patch` on CloudNativePG ScheduledBackups; and `create` and `patch` on `events.k8s.io` Events. It
  acts only in namespaces DeviceChain created, on the schedules its own configuration renders, and
  records each pass as the `devicechain.io/snapshot-retention-checked-at` annotation.
- The backup store keeps up to a week more log for each database, so it fills sooner: with the
  default windows and store, at about 60 events per second of sustained ingest rather than about
  100 (from the same measurement made before compression, so conservative).
- `PostgresNoRecentSnapshotBackup`, `DatabaseSnapshotPruningStalled` and
  `DatabaseSnapshotBackupsUnobserved` are new, and `PostgresNoRecentBaseBackup` waits 8.5 days
  instead of 36 hours on such a cluster.
- The setting belongs to the cluster; changing it is refused while instances run on it.

###### The write-ahead log is compressed, and archived with zstd {#v0190-wal}

New and upgraded event stores compress the page images in their write-ahead log
(`wal_compression = lz4`), which reloads without a restart. Both databases now archive their log
with zstd instead of gzip; base backups stay gzip. Restores and backup expiry read both, by each
segment's name (`.gz` or `.zst`), so an archive that changes compression part-way restores as
before; the release's upgrade test restored such an archive and got every event back. In the release
benchmark at 3,000 events per second, this release's key change and zstd archiving together cut the
event store's log per stored event by 22% and its archiver's CPU by 53%, against the development
build before them. See [Event store volume](./bootstrap.md#event-store-volume).

##### Operations {#v0190-operations}

###### `dcctl upgrade` applies an instance's message broker and event store settings {#v0190-upgrade-infrastructure}

`dcctl upgrade` now applies the instance's NATS servers and event store from the OpenTofu
configuration this release ships, before it moves the services. Until now only `dcctl bootstrap`
applied them, so an upgraded instance kept its old broker and event store settings without saying
so. This upgrade gives an instance created by `v0.18.0` the NATS requests and limit, the
compressed write-ahead log, zstd archiving and the primaries' placement preference; on such an
instance the event store's instances can restart once for the placement preference. Volume sizes
are kept. See [What an upgrade applies to the infrastructure](#upgrade-infrastructure). The
instance's OpenTofu configuration now declares that it needs OpenTofu or Terraform 1.9 or later.

###### Running `dcctl bootstrap` again finishes an instance whose first bootstrap failed late {#v0190-bootstrap-resume}

A bootstrap that failed after it had written the instance's configuration document (while
installing the chart, or while waiting for the services to become ready) left an instance that
`dcctl bootstrap` refused to run on again, as if it were already running.

Now, when `dcctl bootstrap` declares an instance it is building, it records on the declaration
that the first bootstrap has not finished (the annotation
`core.devicechain.io/bootstrap-unfinished`). A bootstrap or upgrade that ends successfully
removes it, in the same write that records the instance as `Ready`. Until then, running the same
`dcctl bootstrap` command again finishes the instance. It reuses the root key, broker credentials
and database passwords the earlier run left in the cluster, and shows the superuser's generated
password, which the failed run never reached. Once the record is gone, a re-run is refused as
before.

- If a run ends successfully but cannot remove the record (the write is refused, or the cluster
  lock was taken over first), it exits with an error that says to run the same command again,
  rather than reporting success over an instance a later bootstrap would run over.
- An instance whose first bootstrap was started by an earlier release has no record, so it is
  treated as running, exactly as before; the refusal names `dcctl upgrade` and `dcctl destroy`.
  See [Finishing a bootstrap that failed partway](./disaster-recovery.md#resuming-a-bootstrap).
- Upgrade every copy of `dcctl` you use before running it against an instance this release
  built. An earlier `dcctl` does not remove the record, so a later upgrade with it that fails can
  leave the record in force over a running instance, and a plain `dcctl bootstrap` would then
  run over that instance.

###### Detection retries releasing its partition when the broker does not answer {#v0190-detect-lease-release}

When the detection engine's pod stops, it releases its partition so that the next pod can start
detecting at once. Until now, if the broker did not answer that release, for example because a
broker server was restarting at that moment, the pod gave up after one try. The next pod then
waited for the partition to expire, up to 30 seconds, and for a further 20-second handover period
before it detected anything. The pod now retries the release until the broker answers, for as
long as its shutdown time allows, keeping back enough of it for one more broker timeout and to
finish stopping. A renewal or a release whose reply was lost while a broker server restarted also
no longer costs the engine its partition. The retry is done by the pod that stops, so it takes
effect from the upgrade after the one that installs this release: during that upgrade, the pod
being replaced still runs the previous release and makes one attempt.

What still pauses detection:

- **Without `--ha`, an upgrade that restarts the broker stops detection for the whole restart**,
  usually about a minute (see [the broker and event store entry](#v0190-upgrade-infrastructure)).
  The engine cannot keep its partition through a broker outage longer than 30 seconds, and
  nothing records that it stopped cleanly, so once the broker is back it waits out the 20-second
  handover period, plus up to 5 seconds before it retries, and then replays from its last
  checkpoint as after any restart. The roll of the services that follows then moves the partition
  to the new pod, which replays once more but does not wait.
- **A pod that stops while the broker stays unreachable past its shutdown** still leaves its
  partition to expire, and the next pod waits up to 30 seconds plus the handover period.

Under `--ha` the broker stays available while its servers restart one at a time. The engine loses
its partition only if its renewals fail for 30 seconds.

###### Alerts {#v0190-alerts}

- **New:** `JetStreamUnreadBacklogNearFull` (warning) and `JetStreamIngestBackpressureEngaged`
  (critical) for [backpressure](#v0190-backpressure), which also fires for
  [the history refusal](#v0190-ingest-history-runway); `JetStreamDurableUnreadNearFull` (warning),
  when a consumer has not read more than 80% of what its stream can hold, for 5 minutes; the three
  [backup alerts](#v0190-backup-alerts); the three [snapshot alerts](#v0190-snapshot-backups);
  `ExternalMqttSourceNotReadByOnePod` (warning) for
  [an MQTT source on your own broker](#v0190-external-mqtt-client-id);
  `BrokerConnectionDiedSilently` (warning) for
  [a broker connection that died without closing](#v0190-broker-liveness); and
  `InstanceContainerRestarted` (warning) for
  [a container that Kubernetes restarted](#v0190-read-loop-restarts).
- **Changed:** `JetStreamStreamNearFull` is now `info`, fires only for streams that hold records for
  an operator (`failed-decode`, `failed-events`, `connector-dispatch.dead`, `max-deliveries`, and
  `dead-letters` while `user-management` does not report reading it), and counts a stream's message
  ceiling as well as its bytes. The default Alertmanager configuration of kube-prometheus-stack does
  not deliver `info` alerts. See [Messages a consumer never read](./observability.md#unread-loss).
- **New series:** `devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}` and
  `devicechain_<area>_jetstream_stream_sink{stream}`, plus those named in the items above, among
  them, by their full names:
  - `devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`
    ([the history refusal](#v0190-ingest-history-runway));
  - `devicechain_eventsources_total_msg_reading_limited` and
    `devicechain_eventsources_total_readings_rate_limited`
    ([Readings, not messages](#v0190-ingest-readings));
  - `devicechain_sparkplugingest_samples_too_old_dropped_total`,
    `devicechain_lwm2mingest_telemetry_too_old_dropped_total` and
    `devicechain_devicemanagement_resolve_event_time_too_old_total`
    ([Old readings](#v0190-event-age-limit));
  - `devicechain_eventsources_external_mqtt_owner{source}` and
    `devicechain_eventsources_total_msg_not_owner{source}`
    ([MQTT client id](#v0190-external-mqtt-client-id));
  - `devicechain_<area>_nats_connection_dead_total{detected_by}` and
    `devicechain_<area>_nats_connection_dead_last_timestamp_seconds`
    ([A broker server that stopped answering](#v0190-broker-liveness)).

  **Removed:** `devicechain_lwm2mingest_notify_samples_truncated_total`.

###### Any service can serve Go runtime profiles, off by default {#v0190-profiling}

`functionalAreas.<service>.profiler.enabled: true` serves CPU, heap, allocation, goroutine and
execution-trace profiles for that service, on a listener of its own on the pod's loopback address,
reached with `kubectl port-forward`. It is never a container port, Service port or ingress route.
Only that service's pods restart. See [Profiling a service](./observability.md#profiling).

###### A Google Kubernetes Engine configuration (new) {#v0190-gke}

`deploy/gke` is new: an OpenTofu configuration that creates a GKE cluster for DeviceChain, with a
tainted `database` pool of three 4-vCPU, 16 GB nodes and a `services` pool of three 4-vCPU, 8 GB
nodes, each booting from a standard persistent disk. Install with `--database-node-selector` and
`--database-toleration` to place the databases on the `database` pool. A default `--ha` install with
one instance fits a new Google Cloud project's SSD quota; for more instances, or a cluster built
another way on any cloud, check the disk quota first: [Prerequisites](./bootstrap.md#prerequisites)
gives the volume sizes and the guide in `deploy/gke` the quota to request.

###### The load-test reports count refused events {#v0190-loadtest-refusals}

The simulator and the load-test harness now count the two ways the ingress refuses an event
separately:

- `shed` is a `429`, refused at the tenant's ingest ceiling.
- `backpressured` is a `503` with a `Retry-After`, refused because the platform is behind. This
  refusal applies to every tenant.

A `503` without a `Retry-After` is still counted as `failed`. Where the counts appear:

- The simulator's `GET /status` shows `emitted`, `shed`, `backpressured` and `failed`.
- The `drive` section of the L1, monitor, detection, command, presence and batch reports shows
  `accepted`, `shed`, `backpressured` and `failed`.
- The contention report shows all four for each probe tenant.
- The self-test report still shows `accepted` only.

What changes:

- Before, most load-test reports always showed `shed 0`, however many events the ingress refused.
  They now show the real count.
- The simulator's `stats.shed` now counts `429`s only. It used to include the `503`s.
- The contention test fails a run in which the backpressure refused the never-shed tenant, or
  refused either tenant when no contention floor is set. Such a run says nothing about shed
  priority. Before, the test reported it as the never-shed tenant being shed. With a floor set, it
  could also pass a run in which the floor refused nothing.

Nothing to do at upgrade.

###### Services notice a broker server that stopped answering within 40 seconds {#v0190-broker-liveness}

When a broker server's machine stopped abruptly (a node that was reset or lost its power), the
services connected to that server could take up to six minutes to notice, because nothing closed
their connections. If one of them was `event-sources`, HTTP ingest stopped for that long, and if
the machine came back quickly Kubernetes recorded nothing. In testing, a hard reset of the node
holding `event-sources`' broker connection stopped ingest for about five and a half minutes.

- Each service now pings its broker server every 10 seconds and gives the connection up after
  three unanswered intervals, or once a write to it has made no progress for 10 seconds, so a dead
  connection is noticed within 40 seconds; the service then reconnects to a server that answers.
  The second broker connection `event-sources` holds, for device presence, is bounded the same way.
- New counter `devicechain_<area>_nats_connection_dead_total{detected_by}` and gauge
  `devicechain_<area>_nats_connection_dead_last_timestamp_seconds`, and a new alert,
  `BrokerConnectionDiedSilently` (warning). See
  [A broker connection that died silently](./observability.md#broker-connection-dead).
- The `JetStreamIngestBackpressureEngaged` runbook now covers a writing service that cannot reach
  the broker, and the [node-loss](./bootstrap.md#ha-node-loss) description states how long a
  service connected to the lost server takes to reconnect.

Nothing to do at upgrade.

###### A broker disturbance minutes after another no longer restarts services, and a restart raises an alert {#v0190-read-loop-restarts}

A service ends itself, so that Kubernetes restarts it, when one of its message-read loops keeps
failing (see [Read loops that keep failing now restart their service](#v0180-read-loops)). A loop
on a quiet stream, one that can go a long time without a message, timed those two minutes from
its first failure and started again only when a message arrived. Two broker disturbances minutes
or hours apart, such as a broker server restart and a later change of a consumer's leader, were
therefore counted as one failure longer than two minutes, and the service exited on the second.
Stopping two of the three broker servers at once restarted several services together this way,
`event-sources` among them, which lengthened the gap in ingest.

- A loop now starts counting again whenever the broker has answered it since its last failure:
  a delivery, the check a quiet loop makes every few seconds that its consumer still exists, a
  re-attach to its consumer, or a reconnect. A loop whose reads keep failing for two minutes with
  no answer from the broker in between still ends the process, as before.
- A broker outage longer than two minutes no longer restarts every service the moment the broker
  comes back. The reconnect counts as the broker answering.
- An empty read does not count as an answer, because the client reports one even while it is
  disconnected. A second failure within a few seconds of the broker coming back can therefore
  still be counted with the first.
- A fault that keeps recurring while the broker answers in between, such as a consumer leader
  that moves every minute, is retried and logged each time but no longer restarts the service.
- New alert: `InstanceContainerRestarted` (warning), for each container in the instance's
  namespace that Kubernetes restarted in the last 15 minutes. Before this, nothing reported a
  single restart: the monitoring stack's `KubePodCrashLooping` fires only for a pod that is still
  restarting after 15 minutes. The alert reads kube-state-metrics, which the bundled monitoring
  stack installs. See [A container that restarted](./observability.md#container-restarts).

Nothing to do at upgrade.

###### The load-test reports check each event, not only the totals {#v0190-loadtest-identity}

The L1 and contention load tests now check every event they sent, not only that the number stored
equals the number accepted. Equal totals can hide a lost event offset by a duplicate or by an event
that should not be there.

Each event is identified by its device and the `occurredTime` the simulator sent with it, which
the simulator now writes to the microsecond, the precision the event store keeps. After the run's
settle interval, the harness reads each device's stored events back through the tenant `events`
query and checks that:

- every accepted event is stored exactly once;
- no refused event is stored: a `429`, a `503` with a `Retry-After`, or another `4xx`;
- an event whose outcome is unknown is stored at most once: a timeout or dropped connection, a
  `503` without a `Retry-After`, or another `5xx`;
- nothing else is stored in the run's window.

Where it appears:

- The L1 report has an `identity` section with `missing`, `duplicateKeys`, `unexpected`,
  `refusedStored`, `ambiguousStored` and `ambiguousAbsent`, up to ten example events for each,
  how long after the run the check last looked (`observedUntilAfterDriveSeconds`), and a
  `reconciled` verdict. A new `ingest-identity` check fails the run when any of the first three is
  not zero, or when the evidence cannot decide, for example because events were still arriving
  while they were read. A read-back request that fails is retried twice, two seconds apart; if it
  still fails, `ingest-identity` fails as inconclusive with the error recorded in the report, and
  the report is still written. A report without this section no longer passes.
- The contention report has the same section for each probe tenant, checked as `gold-identity` and
  `shed-identity`.
- The self-test now also removes one stored event and inserts a duplicate of another, so the totals
  match again, and passes only if the count check still passes and the identity check names both
  events.
- The `ingest-completeness` check still compares the totals, and its message now says that this is
  all it compares.

A redelivered copy of an event is stored at least a minute after the first one: the broker resends
an event 60 seconds after a delivery whose acknowledgement was lost, the copy then still has to be
processed, and a copy whose own acknowledgement is lost is resent again. A minute is therefore the
least a copy can take, not the most. A run whose result is to be published should settle with a
margin above it, for example `--quiesce-settle 90s`.

The simulator does not send an `altId`. An `altId` would make the event store skip a second copy
before storing it, which would hide the duplicates this check looks for and change the work being
measured.

Nothing to do at upgrade.

#### Performance {#v0190-performance}

On Google Kubernetes Engine, on three 4-vCPU, 16 GB database nodes and three 4-vCPU, 8 GB service
nodes, a default HA install accepted 6,000 events a second for 10 minutes, twice, and stored every
accepted event exactly once. That was checked event by event: none missing, none stored twice, none
unexpected.

Resolution, storage and live device state each kept pace over those 10 minutes, and the backlog
had drained within 3 seconds of the load stopping. Detection, which that check does not cover, kept
up at 6,000 (peak backlog under 1,000), and fell behind from 7,600 offered. No sustained rate above
6,000 is claimed. See [Measured throughput](./bootstrap.md#measured-throughput).

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
