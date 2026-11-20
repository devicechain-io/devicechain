---
sidebar_position: 4
title: Releases & Upgrades
---

# Releases & Upgrades

DeviceChain ships as a set of prebuilt, versioned container images plus a Helm chart.
You do **not** need to build anything to run it — pull a released version, install the
chart, and upgrade in place with a rolling update.

:::warning Some versions cannot be upgraded into

- An instance built by **`v0.16.0` or any earlier release** cannot be upgraded onto a later one, because it recorded no declaration of what the instance is. See [Upgrading from an older release](#pre-declaration-recreate).
- **`v0.19.0`, for an instance whose event store holds more than 4,000,000 rows not yet compressed**, or a table of more than 500 chunks whose oldest events you cannot remove: its first start rebuilds the event store's keys over those rows and refuses above those bounds. Upgrading from `v0.17.x` or `v0.18.x`, read [its notes](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/docs/deployment/releases-and-upgrades.md#v0190-upgrade) first.

If you are on either, read the matching section before you do anything else.
:::

:::caution Upgrade through v0.23.0
Releases after `v0.23.0` write events in a form that `v0.22.0` and earlier cannot read. Coming
from `v0.22.0` or earlier, upgrade every instance to `v0.23.0` first, and only then to a later
release. See [The event wire format](#event-wire-format).
:::

## Release lines {#release-lines}

Each minor release starts a release line, and its patch releases belong to that line. The three most recent lines have their full notes below, newest first. Older lines have a short entry under [Older releases](#older-releases).

- **[v1.8](#v180-upgrade)**: [v1.8.0](#v180-upgrade)
- **[v1.7](#v170-upgrade)**: [v1.7.1](#v171-upgrade) · [v1.7.0](#v170-upgrade)
- **[v1.6](#v160-upgrade)**: [v1.6.0](#v160-upgrade)

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

:::warning This section describes releases before v1.0.0

`v1.0.0` is the first stable release, and the [versioning and upgrade policy](./versioning-policy.md) applies from it. Until **v1.0.0**, any release — including a patch release — may change APIs, schemas, or
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
500 chunks whose oldest events you cannot remove ([Check the row count](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/docs/deployment/releases-and-upgrades.md#v0190-row-count)).
Check the release notes for the version you are moving to before running it:

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

Run both with the dcctl of the release you are moving to: a dcctl installs and upgrades only to its own release, and `dcctl upgrade` refuses when the cluster's install was made by an older one. Give `dcctl install` the same settings the cluster was installed with, such as `--ha` or `--compact`. It refuses to change them while an instance runs on the cluster; see [Re-running install](./bootstrap.md#re-running-install).

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

There is one case the operator check lets through with a warning rather than refusing. `dcctl install`
stamps the definitions it applies with an identity; an operator put on the cluster **by hand**
carries none, and `dcctl` cannot tell a deliberate hand-install from one an older `dcctl`
overwrote. Rather than overrule a choice it cannot see, that check prints a note naming the
install command and continues. If you did not install the operator by hand, treat that note
as the refusal it would otherwise have been and run `dcctl install` before going further.
The install check below is separate and still applies: a cluster `dcctl install` never finished
has no install record, so an upgrade of it is refused until `dcctl install` has run.

**The upgrade also checks that the install ran, and only moves to its own dcctl's release.** An operator's definitions can stay the same across a release while its controller changes, so the operator check alone cannot tell that `dcctl install` was skipped. `dcctl upgrade` therefore also reads the install record in the cluster, which names the `dcctl` that last finished `dcctl install`, and **refuses** when that is older than the `dcctl` doing the upgrade, when an install started and did not finish, or when the cluster has no usable record. The refusal names the install command to run first and changes nothing. Running the upgrade again at the version that was installed is not refused, and neither is a cluster installed by a newer `dcctl`. A `dcctl` built from source is not compared. From this release, a plain `dcctl upgrade` with a newer patch release of `dcctl` also needs `dcctl install` first.

An explicit `--version` is held to the same rule, in both directions, for `dcctl upgrade` and `dcctl install`. A release **older** than the `dcctl` running it is refused, because its chart and infrastructure configuration are its own release's and would be planned around images that have never seen them; to go back, where that release's notes allow it, use the `dcctl` of the older release. A release **newer** than the `dcctl` running it is refused as well, and installing it with this `dcctl` would not help: the install record names the `dcctl` that ran the install, not the `--version` it was given. Get the `dcctl` of the newer release, run `dcctl install` with it, then upgrade. `--version` equal to the running `dcctl`'s release, and a tag on a registry of your own (`--registry`) are unaffected. An upgrade with no `--version` is refused only when the instance is declared at a release newer than the `dcctl` running it. Neither refusal has an override.

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

### Provisioned credentials are carried {#upgrade-provisioned-credentials}

Credentials you gave `dcctl` with a file, the [LwM2M pre-shared keys](./edge-services.md#lwm2m-identities) and the [Sparkplug broker passwords](./edge-services.md#sparkplug-sources), are kept in Secrets that `dcctl` writes itself, outside the Helm release. An upgrade leaves them alone and reads nothing secret out of the release. The first upgrade of an instance provisioned with LwM2M keys by an earlier version moves them into their own Secret before the release changes; the earlier revisions in Helm's history still hold the old values, so rotate the keys afterwards if that matters to you. Settings you put on the `lwm2m-ingest` block of the release by hand are no longer kept across upgrades (only the identities and their projection are); put them in a [values file](./tuning-an-instance.md) given with `--values`. Removing a key, or a whole Secret on a withdrawal, waits until the release has succeeded, so a failed upgrade never leaves a pod that is still running without a key it reads when its container restarts; if the release succeeds and only that cleanup fails, `dcctl` warns with the command that finishes it.

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

#### The broker's leadership is evened out {#upgrade-broker-leadership}

Rolling the broker's servers leaves the one restarted last leading no streams or consumers while the others
lead everything, and at a measured load that roughly doubled publish acknowledgement latency. After the
services have rolled, an upgrade that touched the broker asks the leader of each over-loaded stream and
consumer to hand over to a replica that is up to date, and prints how many each server leads before and
after. Moves run one at a time, each waiting for the new leader to be current before the next. The step is bounded and best effort: if it cannot finish it warns and the upgrade still succeeds.
Streams that hold device data already acknowledged to the device are deliberately never rebalanced: the broker's MQTT streams, the device capture stream and every stream an ingest service writes. A leader change on them has lost messages that devices had been told were safe, so no latency gain is worth it and there is no option to include them. Their imbalance clears at the next broker roll, or on its own.
Before the balancing, the stream that carries every resolved event is kept off the server that leads the device capture or the inbound stream, which the balancing alone could leave sharing one server. The summary ends with where those streams are led: the resolved stream reads "separated"; "SHARED" when no current replica sits on a free server (fewer servers than such streams, a free server that holds no replica of it as with five servers at three replicas, or replicas that are behind); "NOT CONFIRMED" when the move failed or was skipped. The capture and inbound streams are never moved, so when one server leads both the line says so as information, and it clears at the next broker roll.
`dcctl broker rebalance <instance>` runs the same step on demand, for a broker restarted some other way.

### What else an upgrade checks {#upgrade-checks}

Two things ride along with it, because a version bump is the thing that reliably happens to a
live instance and a calendar is not.

**The broker's certificate.** The messaging broker serves a certificate valid for a year,
issued by an authority `dcctl` mints at bootstrap and keeps in the cluster. An upgrade
re-issues that certificate when it is inside its last 30 days, or when it no longer covers
every name the brokers dial each other by — which is what scaling an instance up does to a
certificate that is otherwise still comfortably in date. The re-issue is under the **same**
authority, so nothing has to re-trust anything. When only the certificate changes, the broker
is **not** restarted: it swaps the certificate in place, and the upgrade then waits, up to five
minutes, until every broker reports the new certificate's expiry. If a broker has not switched
in that time, the upgrade warns and restarts the brokers so none is left serving the old
certificate. The brokers are restarted straight away when more than the certificate changed:
the authority, or the number of servers. When the upgrade also changes the broker's image or
configuration, the brokers are rolled by that change in any case. Outside those conditions the
check runs and does nothing.

That certificate is minted and renewed by `dcctl`, not by cert-manager, which issues the
instance's web certificates and has no part in the broker's. The broker's certificate is in
the `dc-nats-tls` Secret of the instance namespace, and `dcctl upgrade` is what rewrites it. A
broker handed a new certificate under the same authority serves it to new connections without
reloading its configuration, so the MQTT sessions it already holds are not dropped by the
certificate change. A restart the upgrade does make is an ordinary rolling restart, one broker
at a time, and reconnects devices the way any broker roll does.

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

### Event envelopes {#event-envelopes}

An event envelope carries several events of one tenant in a single message on the resolved-events
stream (and, for the events as received, on the inbound-events stream), in place of one message
each. It cuts the work the broker does per event. Two settings switch it, and **both are off by
default**: `infrastructure.messaging.envelopes.resolved` (device-management writes resolved events
as envelopes) and `infrastructure.messaging.envelopes.inbound` (event-sources writes inbound events
as envelopes, waiting at most `inboundLingerMillis`, default 5 and at most 10, for a second event
of a busy tenant). Set them in a values file under `instance.config.infrastructure.messaging.envelopes`,
see [Tuning an instance](./tuning-an-instance.md). Reading is not a switch: from `v1.1.0` every
service reads envelopes and single-event messages alike, whatever the settings; the settings
decide only whether a service writes them. Every rule below applies to either one, and to both.

- **When it is safe to turn on.** Once every pod of the instance runs `v1.1.0` or later, and not
  before. A pod on an earlier release cannot read an envelope and sets it aside as failed, and the
  events in it are lost to that pod. Upgrade first, wait until every pod has rolled, then set it.
- **What the detection engine does with a duplicate.** In rare cases, such as a lost
  acknowledgement during a retry, one event reaches the stream twice. Event storage and device
  state already absorb that. With the setting on, the detection engine also remembers the identity
  of every event it has applied for about the last ten minutes of broker time (the platform's own
  redelivery span), skips a second copy and counts it on `detect_duplicate_events_skipped_total`.
  A sustained non-zero rate means something upstream is storing events twice. With the setting off
  the detection engine keeps no such memory and its cost and behavior are unchanged. The identity
  is the one the event store uses, so two identical readings from one device at the same nanosecond
  are counted once by both.
- **What it costs.** At about 20,000 events a second the memory holds roughly 12 million
  identities, about 540 MB (extrapolated), in the detection leader only (a standby builds it when
  it takes over). Measured, it added about 190 MB at 6,000 events a second and about 300 MB at
  12,000, roughly 45 to 55 bytes for each identity held; at 5,000 events a second expect about 150 MB.
  With a switch on, size `event-processing` as described in
  [Service sizing](./bootstrap.md#service-sizing). Above about 25,000 events a second a cap applies and the
  memory covers less than ten minutes (about 2.7 minutes at 100,000 a second). After a restart or a
  change of leader the detection engine reads the last ten minutes of the stream again before it
  resumes, so that step takes longer on a busy stream; it is reported on
  `detect_guard_prime_seconds` and `detect_guard_prime_events_total`, and
  `detect_guard_prime_partial_total` counts the times the stream no longer reached back that far.
- **What it does not cover.** A copy that arrives more than ten minutes after the first, for
  example when an inbound consumer is recreated and redelivers from its start, is applied by the
  detection engine a second time, which can count it twice in a counting or windowed rule.
- **Turning it off.** Set the value to `false`. Envelopes already on the stream stay until they
  expire or are consumed, and are still read.
- **Moving to a release below `v1.1.0`.** `dcctl upgrade` records when the setting is turned on,
  and when it is turned off records the position of the newest message of the inbound and resolved
  streams. A move below `v1.1.0` is refused while the setting is on, while either stream still
  holds a message from before the setting was turned off, while the record of when it was turned
  off is missing (run `dcctl upgrade` once more at the current version to make it), and when an
  envelope is found in a sample of the streams (the oldest retained message and the newest 256 of
  each, which also catches a setting changed outside `dcctl`). It also refuses when it cannot
  check, or when the target is not a release tag. A run that moves no version is never refused.
  `--allow-envelope-downgrade` skips the refusal, with a warning: a service on the earlier release
  sets an envelope aside as failed and the events in it are lost to that service.

## Upgrading from an older release {#pre-declaration-recreate}

<a id="v090-baseline-squash"></a>
<a id="v0100-event-key"></a>

An instance bootstrapped by **`v0.16.0`, or by any release before it, cannot be upgraded onto a later release**. `dcctl upgrade` refuses rather than trying, and the refusal changes nothing: the Helm release, the operator and every row stay as they were. The same holds for the two earlier releases that could not be reached in place, `v0.9.0` (every service's migration chain replaced by one frozen baseline) and `v0.10.0` (a new primary key on the event tables): an instance old enough to need either is below this line.

`dcctl bootstrap` records a **declaration**, a cluster-scoped object saying what the instance is: its profile, topology, exposure and functional areas. `dcctl upgrade` reads it to know what to deploy. Releases up to and including `v0.16.0` wrote none, so there is nothing to read, and the refusal says so:

```
instance "devicechain" IS in this cluster — named by the DeviceChain Helm releases in this
cluster — and it carries no declaration, so it was built by a release older than the one
that began recording them.
```

There is no compatibility shim, and before `v1.0.0` there will not be one: a declaration invented after the fact would be a guess applied over a live instance. If a later release raises this line, its notes say so.

**To move onto a later release, recreate the instance and the cluster under it:**

```bash
# Export anything you need first — this discards the databases.
dcctl destroy local devicechain --without-state   # removes the instance; the old cluster goes next
kind delete cluster --name devicechain     # and the cluster the older release prepared
dcctl install local                        # prepares a fresh cluster
dcctl bootstrap local devicechain
```

For a cluster reached with `--kube-context`, which `dcctl` never deletes, pass that `--kube-context` to `destroy`, recreate the cluster with whatever created it, and pass it to `install` and `bootstrap` as well. `dcctl upgrade` prints this recipe when it refuses.

`dcctl` now keeps its local state under `~/.devicechain/instances/<instance>/`. Older releases kept it at `~/.devicechain/<instance>/`, which the new `dcctl` does not read, list or remove, so `destroy` guesses the cluster from the instance name and needs `--without-state`. Once the instance is gone, remove that old directory yourself (`rm -rf ~/.devicechain/<instance>`; it holds the infrastructure state in cleartext) and leave `~/.devicechain/escrow/` alone: the root-key escrow artifact lives there and still opens that instance's database backups ([Disaster Recovery](./disaster-recovery.md#after-destroy)).

:::caution Export first — recreation discards your data
The [destroy guard](#data-durability) protects the databases from an ordinary `helm`
operation, not from a deliberate `dcctl destroy`. If the instance holds telemetry, device
definitions or dashboards you care about, dump them before you start.
:::

Once you are on a release that records a declaration, ordinary in-place upgrades resume.

## Recent releases {#recent-releases}

The three most recent release lines are documented in full, newest first. Within a line, a patch release's notes come before those of the release it patches.

### v1.8.0 — in development {#v180-upgrade}

These notes are for the release in development and grow until it ships.

#### Before you upgrade to v1.8.0 {#v180-before-upgrade}

- **Alarm suppression windows are available.** Declare a one-shot or recurring window over devices, device types, groups, areas, assets, customers or the whole tenant. Alarms are still raised, recorded and visible, marked as suppressed; pages are held until the window ends, then sent if the alarm is still active and unacknowledged. Narrow device windows need `alarm:write`; broader windows also need `notification:write`. Ending a window needs only `alarm:write`. During the upgrade, alarms under a window may still page; rely on suppression once device-management and notification-management have finished rolling out.
- **Alarm notifications are queued in `device-management`'s database before they reach the broker.** An earlier release does not send what is queued there. If you ever have to run an earlier `device-management` again, first wait until the `alarm_events_pending` gauge reads 0 on every replica; anything still queued when the earlier release takes over is not sent.
- **Local installs refuse undersized Docker engines.** A non-compact `dcctl install local` that creates or reuses a local kind cluster now refuses to proceed when Docker reports fewer than 4 CPUs or less than 7.5 GiB of memory. Between 7.5 GiB and 8 GiB it still only warns, so a Docker Desktop set to 8 GB, which reports slightly less, is not refused. Operators who previously continued past the warning with a smaller engine must enlarge Docker or pass `--compact` (or `--size compact`) for the small-footprint preset. Compact installs, `dcctl preflight` and `dcctl bootstrap` keep the warnings.
- **Location permission for notification history.** Members with `notification:read` but without `location:read` no longer see notification states or delivery attempts for geofence-derived alarms or alarms whose position classification is unknown. Rows written before the upgrade and events from old device-management replicas have no verdict and are withheld until the alarm next changes state with a new producer. These history reads consult device-management to verify cached classifications. They fail with an error while device-management is unreachable or its endpoint or the service secret is not configured. History for alarms device-management no longer has is withheld from these members. Grant `location:read` to members who need this history. Notifications sent to administrator-selected channels are unchanged.
- **Policy refusals are counted as `unwanted`, not `dropped`.** The `resolve`, `device_reports` and `update_reports` processing loops no longer count traffic from a disabled device, or decode failures suppressed past their record budget, as `result="dropped"`. That traffic is now counted as the new `result="unwanted"`, and the overview dashboard's failure panels leave it out. `dropped` now always means a message was given up on. No metric is renamed. During a rolling upgrade, pods still on the previous version count this traffic as `dropped` until they are replaced.

#### What is new in v1.8.0 {#v180-whats-new}

- The console can start and manage alarm maintenance windows: a Start maintenance action on device, area, group and asset pages, a Maintenance chip on alarms, and an Alarm maintenance settings page.
- The MCP server shows when an alarm is under a maintenance window, and a new `list_suppression_windows` tool lists windows.
- **Alarm notifications survive a broker outage or a restart of `device-management`.** An alarm state change (raised by detection, escalated, cleared, or acknowledged or cleared by an operator) is now recorded together with the change itself and sent to the broker from that record until the broker accepts it. Previously a change made while the broker was unreachable, or just before `device-management` restarted, could be saved with no notification sent. While the broker is unreachable, acknowledging or clearing alarms now answers within about two seconds, even for a bulk acknowledgement, where each alarm used to hold the call for about 30 seconds. The notification follows once the broker is back. Maintenance-window suppression is decided when the change is made, not when the notification is finally sent. Three new gauges report the backlog: `alarm_events_pending`, `alarm_event_oldest_age_seconds` and `alarm_events_quarantined`. A quarantined event could not be decoded or encoded; it is kept for inspection and listed with the dead letters instead of being retried. Two new alerts watch them: `AlarmEventQuarantined` (critical) and `AlarmEventRelayStalled` (an event waiting more than two minutes, for five minutes). `alarm_event_dead_lettered_total` stays registered but no longer increases.
- **System settings keep what you just saved.** On **System settings**, a setting saved or reset while the list was still reloading briefly showed its previous value, and an edit made in that moment was built on it, so the next save quietly undid the first. The page now shows the value the server stored as soon as the save answers.
- **System settings in Simplified Chinese.** With the console set to 简体中文, the **System settings** screen is now translated, including its token-mask, language, map and branding editors. Its translation already existed but was never loaded, so the screen showed in English.
- **Device deletes and authority changes answer sooner while `device-management` cannot reach the broker.** Deleting or disabling a device, or changing a credential, while `device-management` was cut off from the broker used to hold the call for about a minute, so a client behind the ingress saw a gateway timeout for a change that had been saved. Once `device-management` has detected the lost connection, which takes up to about 40 seconds, the call now answers within a few seconds. A call made before that answers once the loss is detected. A delete answers `true` once it is saved, and the deletion is announced when the broker is reachable again. Where the device's open MQTT sessions cannot be confirmed ended, the call returns the existing error ("the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"). Repeating the operation is safe, and the platform also retries in the background. Each cache refresh a saved change owes now holds the call for at most two seconds and then completes in the background.
- **Changing a device type's profile, or creating devices in bulk, no longer stalls while `device-management` cannot reach the broker.** These changes announce each affected device to the detection engine, one message per device. With the broker unreachable, each message waited about five seconds before failing, so a type with a thousand devices held the call for over an hour. Once `device-management` has noticed the lost connection, the change now stops announcing devices at the first refused message and answers. The change itself is saved either way, and the detection engine picks up the devices it was not told about at its next comparison with `device-management`, which runs every five minutes.
- **Deleted entity events stay detached.** Events received before a customer, area, asset, group or device was deleted no longer reappear under a re-created entity with the same token when they are stored after the deletion was processed.
- **Captured messages wait for shard placement.** With shard groups on, a captured message whose final delivery attempt met unavailable shard placement is now held and forwarded once placement returns, instead of being recorded as a dead letter without its payload. This applies to both the capture stream and the MQTT message store. The new `placement_held_messages` gauge shows how many final deliveries are being held. The hold is bounded per source and lasts for the current run; shutdown abandons held messages unacknowledged, so exhausted deliveries can still be recorded. Rolling upgrades need no wire or configuration change; old pods retain the previous behavior.
- **Live subscriptions follow placement moves.** With shard groups enabled, a live subscription opened after a tenant's placement moves follows the tenant's new group.
- **`resolved-events` can be sharded on a new installation.** With `resolved-events` in `infrastructure.nats.shardGroups.suffixes`, event-processing now runs one detection worker per shard group, each with its own checkpoint, and device-state reads every group. This is supported only when the instance is first installed: on an existing instance where event-processing is deployed, `dcctl upgrade` refuses to start sharding `resolved-events` or to change `count` while it is sharded, and event-processing refuses to start on an instance that already has detection history, on a changed group count or suffix, or with sharding turned off after it was on. If you upgrade with Helm directly, nothing checks this before the upgrade: do not add `resolved-events` to an existing instance's configuration. Keep tenant placement fixed once `resolved-events` is sharded. Within each group, detection watermarks and timer finality advance on that group's own traffic. Sharding is still off unless you configure it.
- **Two generic warnings cover every maintenance task and processor loop.** `PeriodicTaskStalled` fires when a maintenance task has not succeeded on any replica within three times its declared pass interval plus five minutes, or has never succeeded on a replica that declares an interval. `ProcessorLoopDropping` fires when a processor loop gave up on a message in the last 15 minutes; the policy refusals counted as `unwanted` do not trigger it, and device-state's state loop stays covered by `DeviceStateDroppingEvents`. Both are warnings, and a new loop is covered without a rule of its own. During a rolling upgrade, pods still on the previous version count a disabled device's traffic as `dropped`, so `ProcessorLoopDropping` can fire for up to about 15 minutes on an instance where a disabled device is still transmitting.
- **`PeriodicTaskStalled` no longer fires on a healthy task that completes less often than it polls.** The rollup tail sweep wakes every five minutes but deliberately finishes a sweep only about once an hour, so the alert read a healthy task as stalled roughly thirty minutes after each success. A task of that kind now declares the cadence it expects between successes as `<task>_expected_success_interval_seconds`, and the alert measures against the larger of that and the polling interval. The alert name, severity and ten-minute hold are unchanged, and a task that declares no such interval is judged exactly as before.
- **`dcctl broker facts` lists the deletion facts the platform announced.** When an entity is deleted, `device-management` announces the deletion on the broker so that every service holding a reference to it can remove that reference. `dcctl broker facts <instance>` reads those announcements from the broker as the broker administrator. It shows when each one was stored, the tenant, the entity's type, id and token, the deletion time and the message id, and takes `--tenant`, `--token` and `--since` filters. It answers "was the deletion of X announced, and when?" without access to the broker's disks. The broker administrator login gains read access to the deletion announcements and nothing more: the new access adds no right to delete, purge or publish a message. An instance gains the access when it is upgraded with this release's `dcctl`.
- **Browser MCP clients.** The MCP endpoint now answers CORS preflights and exposes session and authentication challenge headers, allowing browser clients to connect with bearer tokens. No operator action is required.
- **The waterfront scenario delivers out of the platform and decodes LoRaWAN.** A `harborpulse` sim now also calls a signed emergency webhook when a gauge passes the critical flood level, publishes a flood bulletin through an MQTT connector, mails a duty officer about every major or critical alarm and mails again if nobody acknowledges it, raises an alarm while its response truck is inside the flood plain geofence, calculates each pump's well fill from its level and well depth, and plays a LoRaWAN network server for four tide gauges whose uplinks arrive through a ChirpStack integration and are decoded by tenant payload decoders, one of them deliberately broken. `dcctl sim create --manifest harborpulse` packages the tenant at a tier of its own, `hp-showcase`, which allows the calculated metric. The deliveries go to an in-cluster receiver, the new `scenario-sink`, at a fixed cluster address: to deliver, the instance needs the `outbound-connectors` area and that address admitted to its egress allowance. `scenario-verify` judges nine more invariants, and five of them read the receiver's record when it is given `--sink`. Re-create an existing `harborpulse` sim with this release's dcctl, because its record lacks the new endpoints.
- **The waterfront scenario runs its devices as a fleet.** A `harborpulse` sim now tags its street lights and pumps with shared attributes and publishes four dynamic device groups over them, scopes an early wet-well warning to the critical pumps only, follows the sun with two SOLAR campaigns planned per lamp from each lamp's installed position (the dusk one declares the next day a public holiday), creates a CALENDAR campaign each sim day that dims one quay's lights and another that sends a pump a disruptive flush, holds that flush with the pump controllers' maintenance window until the crew's slot opens, and models its district as an asset hierarchy with the customer meters under their buildings and a typed `hp-feeds` relationship from each feeder to the devices it powers. Campaigns and maintenance windows run on the wall clock, so the sim writes each day's CALENDAR jobs and window slot as the compressed day runs, and the solar campaigns are judged on what the platform planned for each lamp. `scenario-verify` judges eight more invariants, and the simulator takes eight more `--scenario-defect` values, one to turn each of them red. The scenario's map coordinates now lie in open sea, because the fictional town must not sit on a real one.
- **Harborpulse now demonstrates signed charger updates, governed restore and physical replacement.** Its six chargers take a synthetic 2.4.1 firmware rollout at 02:00 the next night, with two updates in flight and one deliberate verification failure. The East Quay restore at 21:15 retries real ingest rate refusals while preserving original readings. At day 0 12:30, a gauge receives a new physical unit and credential while retaining its logical identity. `scenario-verify` checks eight new invariants against platform read-backs. `dcctl sim create --manifest harborpulse` now defaults to `hp-governed` (40 readings/second, burst 256, one calculated metric per profile); manually packaged sims need the same tier for a clean verdict. Handshakes add optional `endpoints.updateMgmtGraphQL`; older records derive it from the chart's `/api/device-management/graphql` endpoint; custom routes must supply it explicitly. Each charger logs about 90 outage entries independently of uplinks, keeping the restore bounded at every supported day length.
- **Harborpulse checks shared thresholds, LoRaWAN commands and metric backfill.** Pump alarms follow per-device shared attributes and a mid-day threshold change. A typed gauge command travels through the platform's LPWAN downlink adapter to the scenario sink's ChirpStack queue, returns Class A acknowledgements and changes stored sampling cadence. A calculated-metric backfill fills a measured span whose depth constant was initially missing. Three new verifier invariants each have a planted defect and offline controls. The rig enables the downlink adapter and backfill worker; it still admits only the sink's address for platform egress.
- The harborpulse scenario demonstrates an alarm maintenance window: an East Quay lamp's flicker is held during its driver replacement and paged when the window ends, while the same flicker on a North Quay lamp pages at once.
- **The waterfront scenario's flood plain is where its name says.** In a `harborpulse` sim, the "East Quay flood plain" geofence and the works depot now stand behind East Quay, inland of its street lights on the town side, so the console's map shows them where the scenario describes them. The truck still enters the flood plain at 17:45 and leaves at 21:45. An existing sim moves both geofences the next time it provisions. The 3D scene's layout file now also carries the scenario's planned cue times, the truck's route and a bounding frame, and the simulator's `/status` reports `scenario.layoutDigest`, the SHA-256 of the layout it generates.
- **A 3D scene for the waterfront scenario (first milestone).** `demos/harborpulse-unity` is a Unity 6 (URP) project that draws the `harborpulse` simulator's harbour district, laid out from the scenario's own coordinates, under a sky whose sun, moon, stars and storm follow the sim's compressed day on its `/status`. The harbour water is a custom shader with Gerstner waves, foam and rain, and its height on each quay is the median of that quay's tide gauges as the platform reports them. The street lights show what the platform last heard from each one: on, off, or silent. The scene reads the platform through the DeviceChain C# SDK and never writes to it. An offline preview, clearly labelled, runs without an instance.
- **The waterfront 3D scene, second milestone.** `demos/harborpulse-unity` now builds its quays, pump stations (with a cut-away wet well), tide gauges, EV chargers, street lights, buildings and the works truck in code, and adds colour grading and a planar reflection on the harbour water. It shows the scenario's operations as the platform records them: wet-well alarms escalating in place, the pump's start commands with the platform's own timestamps, the truck at its last reported position, the feeder outage and its silent devices, the gauge replaced behind the same device, the lamp-service suppression window, and the chargers' signed firmware rollout with each charger's outcome. Weather, crews and the outage itself are drawn as the scenario's world and never as platform alarms. The scene still only reads. The simulator's `/status` now also lists the story's beats with their hours.
- **Concurrent simulator creation.** `dcctl sim create` now reserves automatic control ports across processes, preventing concurrent creates from selecting the same port. Failed creates release their reservations; abandoned reservations without a saved simulator expire after 24 hours. Explicit `--control-addr` values are unchanged.
- **Set harborpulse's day length from dcctl.** Run `dcctl sim run <name> --day-length 48m` to stretch one compressed sim day to 48 minutes. The default `0` keeps the scenario's own 24-minute day; negative values are refused, and the simulator validates its minimum day length.

### v1.7.1 — updates and campaign commands reach only the devices they were meant for {#v171-upgrade}

`v1.7.1` is a patch release and an in-place upgrade from `v1.7.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described in [Rolling upgrades](#rolling-upgrades), using the v1.7.1 `dcctl` and `--version v1.7.1`. `update-management` appends one migration at its first start. It adds a column that may be empty, so `v1.7.0` pods keep working while the upgrade rolls. As for every release, there is no downgrade.

```bash
dcctl install local --version v1.7.1
dcctl upgrade local devicechain --version v1.7.1 --dry-run
dcctl upgrade local devicechain --version v1.7.1
```

This release changes no covered interface. It adds one refusal and skip code, `DEVICE_REPLACED`, to the existing code vocabulary.

#### Before you upgrade to v1.7.1 {#v171-before-upgrade}

- While old and new pods overlap during the rolling upgrade, a `v1.7.0` pod can still step a running rollout the old way until it is replaced. Finish the upgrade before you delete and re-create devices that a running rollout targets.
- A rollout started before the upgrade recorded only its devices' tokens. For those rollouts, `update-management` compares when each device was created with when the rollout started (see [Rollouts target the devices they started with](#v171-rollout-identity)). A device whose creation time is within five minutes of the rollout's start is **held**: it is not told to update, and the rollout stays `RUNNING`. If a rollout stays running with devices waiting, abort it and start a new one.

#### What is new in v1.7.1 {#v171-whats-new}

- **An update assigned to a re-created device stays in place.** An assignment request that looked a device up before the device was deleted and re-created with the same token is now refused with `DEVICE_REPLACED`. It no longer replaces the new device's update. Before, the new device stopped being offered its update until it was assigned again.
- **Campaign commands re-check the group when they are retried.** When creating a campaign's batch fails for a passing reason, the commands are retried later. They are now checked against the campaign's group first: a device that left the group meanwhile is recorded `NOT_IN_GROUP` and gets no command. If the group cannot be read, the commands wait; if they are still waiting past the campaign's lateness bound, they are recorded as missed.
- **Backups of sharded instances include every shard-group stream.** On an instance with `infrastructure.nats.shardGroups` configured, `dcctl backup` now exports each group stream of a sharded stream (`<stream>-g1`, `<stream>-g2`, and so on) alongside its base stream and records the layout in the manifest. A configured group stream that is missing or unreadable, or a group stream the deployed configuration does not declare, fails the export instead of producing a partial backup. `dcctl restore --plan` lists every group stream and refuses a target whose shard-group layout differs from the backup's. Backups of a sharded instance taken with an earlier dcctl contain only the base streams (group zero), not the group streams; take a new backup after upgrading dcctl. Instances without shard groups are unaffected, and their manifests are unchanged.

#### Rollouts target the devices they started with {#v171-rollout-identity}

- `startUpdateRollout` now records which device each member of the group is, not only its token. A device later created with the same token is a different device: it is skipped with `DEVICE_REPLACED`, even if it is in the group, and it is never told to update. A member that leaves the group while the rollout starts is not part of the rollout.
- When a device is deleted, a rollout target that is still waiting for that device is skipped with `DEVICE_NOT_FOUND`.
- Rollouts started before v1.7.1: a device created more than five minutes after the rollout started is skipped with `DEVICE_REPLACED`, one created more than five minutes before it is updated as usual, and one created in between is held, as described under [Before you upgrade to v1.7.1](#v171-before-upgrade).

### v1.7.0 — deletions reach every service, and sessions ride out outages {#v170-upgrade}

`v1.7.0` is an in-place upgrade from `v1.6.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described in [Rolling upgrades](#rolling-upgrades). Use **the v1.7.0 `dcctl` for both commands** with OpenTofu or Terraform 1.10 or later, and give `dcctl install` the cluster's settings, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. `device-management`, `ai-inference` and `update-management` append migrations at their first start, and the upgrade is forward-only: there is no downgrade to `v1.6.0`. Coming from `v1.5.x` or earlier, upgrade through each intervening minor first, including `v1.6.x`, following each release's notes with that release's own `dcctl` and `--version`: skipping a minor is not a tested path (see [Supported upgrade paths](./versioning-policy.md#upgrade-paths)). For `v1.6.0`, those are the [`v1.6.0` notes](#v160-upgrade).

```bash
dcctl install local --version v1.7.0
dcctl upgrade local devicechain --version v1.7.0 --dry-run
dcctl upgrade local devicechain --version v1.7.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds covered interfaces and removes or deprecates none. Geofence alarms that need `location:read` and the `MANAGED_BY_IDP` refusals are security fixes permitted by the policy; the other new refusals and limits are listed under [Before you upgrade to v1.7.0](#v170-before-upgrade). Helm values and instance configuration are outside the covered compatibility surface.

Highlights: deleting a device, customer, area, asset or group is now announced reliably, and each service that held something for it acts on the announcement: undelivered commands are cancelled, live state and pending updates are removed, alarms end and stop escalating, and stale dashboard shares are removed. A network outage no longer signs anyone out of the console or `/dash`. Instances with NATS shard groups hold ingest instead of dead-lettering it. MCP, live subscriptions, OTA transfers and AI inference gain limits per tenant, device or pod.

What is new:

- [Before you upgrade to v1.7.0](#v170-before-upgrade)
- [What is new in v1.7.0](#v170-whats-new)
- [Deletions are announced reliably](#v170-deletion-announcements)
- [Shares naming a deleted customer or group are removed](#v170-deleted-anchor-shares)
- [Alarms of a deleted device end, and stop escalating](#v170-deleted-device-alarms)
- [A deleted device's undelivered commands are cancelled](#v170-deleted-device-commands)
- [A deleted device's live state is removed](#v170-deleted-device-state)
- [An update assigned to a deleted device is ended](#v170-deleted-device-updates)
- [Event anchors of a re-created entity are kept](#v170-deleted-entity-anchors)
- [Shard groups hold work instead of dead-lettering it](#v170-shard-groups)

#### Before you upgrade to v1.7.0 {#v170-before-upgrade}

What needs action, in the order to do it:

- **`dcctl install`, `dcctl bootstrap` and `dcctl preflight` check the OpenTofu or Terraform version.** The preflight now refuses a binary older than the `required_version` that dcctl's bundled OpenTofu configuration declares (currently `>= 1.10`), a prerelease, or one whose version it cannot read. Before, it checked only that the binary was present, so an install on OpenTofu 1.9 failed at `tofu init` after it had already updated the operator. OpenTofu is still preferred when both are installed, and it is the binary checked. Install a stable OpenTofu or Terraform 1.10 or later before you run the v1.7.0 `dcctl install`. `--skip-preflight` still skips the check.
- **Upgrade note: the broker restarts.** The upgrade replaces the broker image, so every broker pod restarts. With `--mqtt-fail-closed` off (the default), a QoS 1 message the broker acknowledges while a stream changes leader can be lost. Turn it on before you upgrade (`dcctl upgrade --mqtt-fail-closed`) if devices must not lose acknowledged messages ([fail-closed MQTT](./observability.md#mqtt-fail-closed)).

**Instances with shard groups.**

- **Instances with shard groups: ingest is held, not dead-lettered.** Nothing in [Shard groups hold work instead of dead-lettering it](#v170-shard-groups) changes an instance that has not set `infrastructure.nats.shardGroups`. On one that has, a backlogged group or an unusable placement now holds ingest and retries it instead of dead-lettering it, a refused HTTP or integration request answers `503` with `Retry-After`, and a service that cannot route a sharded stream refuses it. Read that subsection before you upgrade.
- **Instances with shard groups: a placement must not name a group at or above `count`.** With [NATS shard groups](#v160-nats-shard-groups) configured, assigning a vpartition to a shard group at or above `nats.shardGroups.count` is now refused, and user-management's placement mirror refuses to publish a table that names one: each such pass fails and is counted in its `placement_mirror` pass metrics. A table that already names such a group is not partly honoured: it is an unusable placement, so sharded ingest is held for every tenant on the instance until the table fits, as [Shard groups hold work instead of dead-lettering it](#v170-shard-groups) describes. Before you upgrade, make sure no vpartition is assigned to a group at or above `count`; if one is, raise `count` to cover it first. Instances without `infrastructure.nats.shardGroups` are not affected.

**Correct what is now refused.**

- **Token masks that generate nothing are refused.** A token mask whose only placeholders have an explicit width of 0, such as `dev{numeric-0}`, gives every entity the same token, and saving the token-masks setting with one is now refused. A stored mask like this keeps its value, but the next save of the setting fails until it is fixed, so change it to a positive width (`{numeric-4}`), an omitted width (`{alphanumeric}`), `{slug}` or `{uuid}` before you upgrade.
- **A facet's value type cannot change while values of the old type exist.** Changing a facet's value type is now refused while live SHARED attributes in the same tenant, member family and key hold non-null values tagged with its old type. Clear or migrate those values before changing the declaration. Same-type label and vocabulary edits remain available. No schema migration or configuration change is needed. The check takes effect as device-management pods are replaced, and older pods still accept these changes during the rolling upgrade, so pause facet type changes until the rollout finishes.
- **Marking a relationship type tracked is refused while devices created relationships of it.** `updateEntityRelationshipType` refuses to make a type tracked while relationships of that type that devices created themselves exist, because they would become ownership at that moment; the refusal names them. Delete them, then mark the type tracked.

**Review permissions and callers before rolling.**

- **Geofence alarms need `location:read`.** An alarm raised by a rule that tests geofence containment is now read only by a caller that also holds `location:read`, as `detectionStream` already withholds that rule's detections: the alarm's raise and clear times are the device entering and leaving the fence. Without it, the alarm queries, the alarm counts, `alarmDispositions` and the `alarmStream` subscription leave the alarm out, and acknowledging, clearing or labelling it answers as for an unknown alarm. Other alarms are unchanged. The upgrade classifies every existing alarm, judging a rule by whether its definition, in the draft or in any published version, mentions `inFence`. The classification runs over every alarm in the migrations at the first start of `device-management`, and a second statement checks again every alarm the first found not to be a geofence alarm, so on a large alarm table that start can take noticeably longer. An alarm whose rule can no longer be found, for example because its device profile was deleted, is treated as a geofence alarm and withheld too. During the rolling upgrade, an alarm raised or updated by a replica of the previous release can be withheld for up to a minute until it is classified; that includes an alarm already classified as not a geofence alarm, which is withheld from the moment such a replica updates it, so a geofence rule that replica adds to it is never shown. While old and new pods overlap during the rolling upgrade, a `v1.6` pod still shows geofence alarms to every caller until it is replaced. The read-only baseline and tokens minted through the read-only OAuth scope do not include `location:read`, so a role or token that monitors or acknowledges geofence alarms needs it added. Notifications sent to configured channels are not affected.
- **Operator edits to an IdP-managed membership are refused with `MANAGED_BY_IDP`.** Operator mutations that disable, remove or change the roles of a membership now return `MANAGED_BY_IDP` while its connection exists and still serves the tenant; SSO sign-in would otherwise undo those edits. To revoke access, disable the identity, remove the person from the provider's groups, or stop the connection serving the tenant. Unmanaged memberships and managed memberships whose connection is gone or no longer serves the tenant remain editable.
- **Enabling an IdP-managed membership is refused with `MANAGED_BY_IDP` too.** The operator mutation that enables a membership now returns `MANAGED_BY_IDP` while the membership's connection exists and still serves the tenant, as disable, remove and role changes already do. Each SSO sign-in sets that membership's enabled state from the person's groups, so an enable made by an operator would contradict the provider until the next sign-in disabled it again. To give the person access, add them to a group the provider maps to a role in the tenant; the membership is enabled at their next sign-in. Enabling an unmanaged membership is unchanged, and a managed membership whose connection is gone or no longer serves the tenant still cannot be re-enabled: remove it and add a membership instead.
- **Dynamic-group access scopes can widen at the first start.** On its first start after the upgrade, `device-management` adds the existing devices, assets, areas and customers that should already have been members of a dynamic group in use by a detection rule or as an access scope, where the group's selector matches an entity with no attributes (for example a negated condition such as `!("retired" in attr)`). Members of such an access scope may then see devices they could not see before, and group-scoped rules start evaluating them. Review the dynamic groups with such selectors that serve as access scopes before you upgrade.
- **SSO, MFA and step-up limits count an IPv6 /64 as one source.** SSO begin, MFA verification, and step-up requests from IPv6 addresses in the same /64 now share a per-source rate limit, preventing address rotation within that block from draining the shared global allowance. People who sign in from one IPv6 /64, such as one office or carrier network, now share that limit and can be refused sooner. IPv4 limits and all burst sizes and refill intervals are unchanged.

**Clients that meet new limits.**

- **A command past its expiry is no longer dispatched.** Before, a command whose expiry had passed but which the expiry sweep had not yet settled could still be claimed and published to its device: when a device disabled for longer than its commands' lifetime was enabled again, by a nudge, or by a dispatch sweep whose expiry step had failed. The dispatch claim now refuses such a command and leaves it for the expiry sweep, which records it as expired. A command exactly at its expiry time, or with no expiry, is still dispatched. `markCommandSent` answers null for an expired command, as it does for any claim it does not win.
- **HTTP command polling per device.** Each device can hold at most two waiting command polls per replica (one, plus a reconnect whose earlier connection the server has not yet seen close). Further waiting polls receive HTTP 429 with `Retry-After`, leaving capacity for other devices. Keep one waiting poll per device and retry as directed; `wait=0` requests and the tenant cap are unchanged.
- **Wrong device passwords count once, whichever way they arrive.** A wrong password presented on the HTTP device-uplink route now counts toward the same per-credential hold as a wrong MQTT password, across all replicas, so guesses can no longer be split between the two to get twice the attempts. While a credential is held, an HTTP uplink with it is answered `503` with `Retry-After` and an MQTT connect with it is refused, as before.
- **Event list queries stop after 60 seconds.** `events`, `locationEvents`, `measurementEvents`, `alertEvents`, `stateChangeEvents` and `bucketedMeasurements` reads that do not finish within 60 seconds are now cancelled with an error asking for a narrower time range, instead of holding a database connection until they finish. A scoped caller's read that names an anchor keeps its 15-second bound.
- **Live subscriptions have a ceiling per tenant and pod.** `measurementStream`, `alarmStream` and `detectionStream` each accept at most 5,000 concurrent subscriptions per tenant on one service pod; the next one is refused with `LIMIT_EXCEEDED`. Close unused live views. The ceiling is per pod, so more replicas raise the instance's total.
- **The MCP server's limit of 64 requests in flight per process is reachable, and the request past it receives HTTP 429.** The shared request-body budget held only 63 maximum-size bodies, so the 64th concurrent request received HTTP 503 with `Retry-After: 1` before the MCP limit was reached. The budget now holds 64 (it grows by 64 bytes), and requests beyond the limit receive HTTP 429 with `Retry-After: 1`, as documented. A client that retried the 503 should retry the 429 the same way. The limit of 4 in flight per caller and the 1 MiB request-body ceiling are unchanged.
- **MCP clients must send `initialize` on its own.** A request that opens a new MCP session (a POST without an `Mcp-Session-Id` header) is now refused with HTTP 400 and JSON-RPC error `-32600` when its body is a JSON-RPC batch. Only clients on protocol versions before 2025-06-18 can send batches; if one of yours puts `initialize` in a batch with other calls, change it to send `initialize` alone and the other calls on the session it opens before you upgrade. Batches on an existing session are unchanged.
- **MCP session limits.** New sessions are refused with HTTP 429 and `Retry-After` once a caller has 16 live sessions or the MCP process has 1,024. Agents should reuse sessions, DELETE unused sessions and honor `Retry-After`. Existing sessions remain usable; deleted sessions and sessions reaped after 30 idle minutes free capacity. Review clients that repeatedly initialize before upgrading; no configuration change is required.
- **A device asking for an update download in a loop is now throttled.** `update-management` answers a device's `download.request` up to five times at once and then once a second, per device, as it already answers a `sync` at most once every five seconds. A request over that budget is acknowledged without an answer and counted on `devicechain_updatemanagement_update_reports_rejected_total{reason="throttled"}`, so one looping device no longer delays every other tenant's update reports. A device that asks again after a restart or a failed download is still answered.
- **One tenant can no longer take every artifact-upload slot of a replica.** A tenant holds at most half of a replica's concurrent artifact uploads (`updates.maxConcurrentUploads`), rounded up and never fewer than one. An upload over its tenant's share is refused before its body is read, with the same `503` and `Retry-After` an upload gets when the replica is full; retry once one of that tenant's uploads finishes. The replica's total is unchanged.
- **AI inference rate limits are shared across replicas.** AI inference now keeps each tenant's rate and burst budget in its own service database, so every ai-inference replica spends from the same budget and a restarted replica no longer starts with a fresh burst. The upgrade appends one migration that creates the admission-state table; configuration and APIs are unchanged. The shared limiter fails closed: if the database cannot be read or updated, the request is refused rather than admitted against a local allowance. The per-tenant ceiling itself is still looked up from user-management and still falls back to the platform default when user-management is unreachable. During a rolling upgrade, old pods keep their process-local budgets until they are replaced, so the shared ceiling is fully in force once every ai-inference pod runs `v1.7.0`.
- **Campaign error details are sanitized.** `Campaign.lastError` keeps actionable configuration messages; internal failures show a generic retry message, with full details logged server-side. Messages remain capped at 500 bytes and truncate at a UTF-8 boundary. The GraphQL field and type are unchanged.

**What the first start after the upgrade does.**

- **Shares left behind by earlier deletions are removed after the upgrade.** On its first sweep, 15 minutes after its first start after the upgrade, `dashboard-management` removes the dashboard shares that name a customer or access group deleted earlier, including deletions from before the upgrade. The sweep needs the service secret and the `device-management` endpoint configured; without them it does not start, and a warning is logged at startup. See [Shares naming a deleted customer or group are removed](#v170-deleted-anchor-shares).
- **The first start cancels commands left behind by deleted devices.** On its first start after the upgrade, `command-delivery` reads the deletions still retained on the message broker and cancels the queued, held or parked commands of those devices, including deletions from before the upgrade. See [A deleted device's undelivered commands are cancelled](#v170-deleted-device-commands).
- **The first start removes live state left behind by deleted devices.** On its first start after the upgrade, `device-state` removes the live state, latest readings, latest position and dropped-reading records of devices that no longer exist, including devices deleted before the upgrade, and then repeats the check every 10 minutes. A device that was deleted and created again with the same token before the upgrade shares one live-state record with the deleted device, so that record is removed too: the new device's last-known state and latest readings are empty until it next reports. No stored events are affected. See [A deleted device's live state is removed](#v170-deleted-device-state).
- **The first start ends updates assigned to deleted devices.** On its first start after the upgrade, `update-management` reads the deletions still retained on the message broker and ends the pending update assignments of those devices, including deletions from before the upgrade. Their rollout targets show as cancelled. See [An update assigned to a deleted device is ended](#v170-deleted-device-updates).
- **Measurement aggregates now age out with retention.** With `lifecycle.retentionDays` set in event-management, the per-minute measurement aggregates behind charts and bucketed reads are now dropped one chunk interval (rounded up to a day) after the raw readings, and bucketed reads no longer return aggregates older than that window. From the first start after the upgrade, bucketed reads stop returning aggregate history older than the window, so charts over that range show no data; a background job then deletes that history a whole storage chunk at a time. On an instance upgraded from an earlier release, each storage chunk of the aggregate covers 70 days, so older aggregates are hidden at once but deleted only when the newest data in their 70-day chunk is older than the window. Instances with `retentionDays: 0` (the default) are unaffected. During the rolling upgrade, pods still on v1.6 keep serving older aggregates until they are replaced.
- **A changed refresh-token lifetime now reaches a running instance.** Raising or lowering `auth.refreshTokenTtlSeconds` used to change only fresh installs: an existing refresh-token bucket kept expiring entries at the age it was created with. `user-management` now brings the bucket to the configured lifetime when it starts, and the broker administrator does the same for the buckets whose lifetime it declares. Lowering the lifetime expires stored entries older than the new value at that restart, so the sessions they back end and those people sign in again.

#### What is new in v1.7.0 {#v170-whats-new}

**APIs and SDKs.**

- **A stale save carries `extensions.code: STALE_WRITE`, and `@devicechain/client` exports `isStaleWrite`.** A write refused because the record changed after the caller read it (an optimistic-concurrency precondition such as `expectedUpdatedAt` that no longer holds) now answers with `extensions.code` `STALE_WRITE` in every service; before, only connector updates carried a code and every other such refusal had none. The message is unchanged ("… was modified by another writer; reload and try again"), so clients that match it keep working, and it is still never `CONFLICT`, which stays reserved for a value that is already taken. `isStaleWrite(err)` in `@devicechain/client` is true for the code, falls back to the phrase for older servers, and is never true for a `CONFLICT`. The console uses it wherever it saves with a precondition.
- **`updateCustomerType` can refuse a stale save.** Customer type edits now accept optional `expectedUpdatedAt` on `updateCustomerType`. Pass the last read `updatedAt` to reject stale saves; on "modified by another writer", reload and retry. Omitting the argument preserves last-write-wins behavior. This protection is available after all device-management replicas finish upgrading; other registry edit operations are unchanged. No database migration is required.
- **Event lists without a total.** `EventSearchCriteria.includeTotal: false` skips the exact count of matching events, which dominates a page read on a large history; `totalRecords` is then null and the new `SearchResultsPagination.hasMore` says whether another page follows. `hasMore` is also set with the default. During the rolling upgrade, a pod still on v1.6 rejects both `includeTotal` and a selection of `hasMore` as unknown fields, so use either only once every event-management pod runs v1.7.
- **Many devices on one measurement subscription.** `measurementStream(deviceTokens:)` streams readings from up to 1000 devices on one subscription. A pod still on v1.6 rejects the argument during the rolling upgrade, so clients should send it only once every event-management pod runs v1.7, or fall back to one subscription per device when it is refused.
- **Live measurement, alarm, and detection subscriptions share feeds per tenant.** Each service pod now reads each tenant's live feed once for `measurementStream`, `alarmStream`, and `detectionStream`, decoding each message once and dispatching by device, alarm originator, or detection profile before applying each subscriber's filters and access scope. A dashboard with many subscriptions no longer multiplies the decoding work per event. What a subscription receives is unchanged. The new per-pod subscription ceiling is under [Before you upgrade to v1.7.0](#v170-before-upgrade).
- **List totals saturate instead of wrapping.** A list whose matching rows exceed 2,147,483,647 now reports `totalRecords` as 2,147,483,647 instead of a wrapped, possibly negative number, and `pageStart`/`pageEnd` no longer overflow on very large page numbers.
- **`@devicechain/client` can recover from an HTTP 401.** Register `setUnauthorizedHandler({ refresh, onEnd })` and a request whose access token the server rejects gets one forced refresh and one replay before the session ends; use the new `session.forceRefresh(token)` from `createSessionTokens` as `refresh`. A temporary refresh failure (network error, 5xx or 429) keeps the session and surfaces the error; a refused refresh, or a second 401 on the replay, ends it. Without a handler the SDK behaves as before.
- **`@devicechain/client` can run an operation that spends the refresh token alongside its own refreshes.** `createSessionTokens` now returns `session.exchange(spend)`: `spend` receives the held refresh token and returns the pair that replaces it, and it runs as the one use of that token, after any refresh already running and before any refresh asked for meanwhile, which takes its pair instead of spending the token again. The console's step-up runs through it, so a forced refresh after an HTTP 401 and a step-up can no longer spend one refresh token between them and sign the user out after a correct code.
- **A network outage no longer signs anyone out.** A refresh that cannot reach the server (no response, a 5xx or a 429) now keeps the session for as long as its refresh token is valid, instead of ending it once the access token expires. In the console and `/dash`, requests made while the access token is expired fail with the outage and succeed again once the server is back, and `/dash` wall displays keep retrying on their own. Only a refusal from the server, or the refresh token lapsing, ends a session. For `@devicechain/client` users: in that window `getAccessToken()` still resolves to `null`, as before, but the session is no longer ended (`onEnd` is not called) and the next call tries again; `startAutoRefresh()` keeps retrying until the refresh token lapses. To have `getAccessToken()` reject with the transient error instead, so a request fails with the outage rather than going out without a token, pass `transientRefreshErrors: 'throw'` to `createSessionTokens`, as the console and `/dash` do.
- **`createSessionTokens` returns `ManagedSessionTokens`.** It extends `SessionTokens`, on which `forceRefresh` is now optional, and keeps `forceRefresh` required, so code that uses the result is unaffected. If you type an object of your own as the result, such as a test mock declared as `ReturnType<typeof createSessionTokens>`, type it as `SessionTokens` instead: that is the interface meant for objects you build, and a member added to `ManagedSessionTokens` later would otherwise stop the mock compiling.
- **Live subscriptions ride out a token-refresh outage.** When a subscription's socket reconnected after the access token had expired and the token refresh got no response, a 5xx or a 429, every live subscription in that area ended with an error, and dashboards showed their data as unavailable until reloaded. `@devicechain/client` now retries a reconnect whose token getter rejects with that transient error (as `getAccessToken()` does under `transientRefreshErrors: 'throw'`, which the console and `/dash` use) with the same back-off as a dropped network, and reports it as a reconnect once a token is available. A refused refresh still ends the session.

**Concurrent writes.**

- **AI-provider updates and deletes act on the provider they checked.** An `updateAiProvider` without an `expectedUpdatedAt` precondition now answers not-found when the provider was deleted between the read and the write, instead of re-creating it. It also no longer reverts a concurrent rename. Deleting a provider removes the row that was checked for grants, even when a rename and a new provider with the old token land in between, and the delete's audit-journal entry records the provider's id and label.
- **Concurrent asset-hierarchy writes can no longer give an asset two parents or form a cycle.** Every way of creating a containment edge (`setAssetParent`, a single or bulk relationship create, and claim redemption) now takes a per-tenant lock before it checks the hierarchy, so concurrent writers are checked one after another. A relationship create that races another writer and would give an asset a second parent is now refused, as it already was when the two did not overlap; `setAssetParent` still replaces the current parent.
- **Device claims preserve concurrent redemptions.** Canceling a claim that has just been redeemed returns `false` and leaves it claimed. Reopening a claim revokes its current owner's relationship before making it available again; a concurrent change during reopening is refused and can be retried.
- **Artifact upload ownership.** An expired upload can no longer release or complete a newer upload's lease, preserving one upload per artifact across replicas. No operator action is required.

**Detection and campaigns.**

- **The detection engine spaces its snapshots by their cost.** Reaching `checkpointEvents` no longer triggers a snapshot immediately: the engine also waits until four times the last snapshot's cost has passed, so with large detection state the engine snapshots less often, never more than `checkpointIntervalSeconds` apart, and always when buffered acknowledgements reach half the consumer's limit. After a crash it may replay more than `checkpointEvents` events. No setting changes.
- **Detection checkpoints carry a falling-edge memo.** The detection engine now records each rule's recent falling edges (for ten minutes past the point it has recorded up to) in its checkpoints, and reads a raise's input time when a checkpoint carries one. Every raise and resolve is decided exactly as in `v1.6`, and a `v1.6` event-processing pod can still restore this release's checkpoints during the rolling upgrade. A later release relies on these fields, so upgrade through `v1.7` rather than skipping it: skipping a minor is not a tested path ([Supported upgrade paths](./versioning-policy.md#upgrade-paths)).
- **A restart loads recent fence-set versions before replay.** During startup, event-processing now loads the current fence-set version and up to three preceding versions from device-management before replay, so recent historical events can use their stamped geometry without waiting for a fence fact or preview. Older versions and failed archive reads remain unresolved and produce counted containment errors; this does not preload all history. Startup reconciliation retries missing versions and skips versions already held. While old and new pods overlap during the rolling upgrade, an old pod still loads only the current version at startup until it is replaced.
- **Fence-set snapshots record each fence's identity.** New fence-set snapshots include an optional `fenceId` matching `GeoFence.id`, so deleting and recreating the same token can be distinguished even when its geometry is unchanged. The snapshot GraphQL entry exposes this as nullable `fenceId: ID`. Older snapshots, including those minted by older pods during a rolling upgrade, return null: identity is never inferred from today's token. History is not rewritten. The next fence mutation handled by a new pod captures identity in a new version, even if only presentation fields changed. No migration or operator action is required.
- **Campaigns re-check group membership before later steps.** Before a later step or a per-device solar instant that falls due more than a minute after the last membership check, a campaign checks group membership again. A device that has left the group is skipped only for its steps that are due (or due within the next minute); its later steps stay pending and membership is checked again when they fall due, so a device that rejoins in the meantime receives them. An unavailable group blocks sending, and devices that join after planning still enter the next planned cycle. No operator action is required.
- **Campaign cron steps.** A step far larger than its field's range, such as `40-59/9223372036854775803`, no longer overflows into minutes outside the range it names. It selects only the range's first value, as any step larger than the range already did. Every expression accepted before is still accepted.
- **New entities join matching dynamic groups when they are created.** A device, asset, area or customer created after a dynamic group was put in use by a detection rule or as an access scope is now enrolled at creation when the group's selector matches an entity with no attributes (for example a negated condition such as `!("retired" in attr)`). Members of such an access scope now see devices created after the group was put in use, and group-scoped rules evaluate them. A group put in use before the upgrade is brought up to date within a minute of the first start of `device-management`: every entity its selector matches, including those created before the upgrade, is enrolled, and from then on new entities are enrolled when they are created. The same holds for a group a v1.6 pod puts in use while old and new pods overlap during the rolling upgrade. An entity created through a v1.6 pod in that window is not enrolled at creation.

**Devices, ingest and connectors.**

- **Undecodable HTTP bodies addressed to a tenant that does not exist are no longer recorded as failed decodes.** The failed-decode record is shared by the whole instance, so a sender inventing tenant names could push real tenants' records out of it. Such a body is now counted on `total_http_ingest_refused` with the `unknown_tenant` reason instead, and the sender gets the same answer as for any other malformed body.
- **LwM2M registration lifetimes.** A Register or Update with an extremely large `lt`, past about 292 years, now gets the configured maximum lifetime. Before, the value overflowed, the lifetime fell to the one-minute minimum, and the device was marked disconnected a minute later.
- **A tenant over its outbound rate no longer floods the dead-letter record.** `outbound-connectors` now budgets the dead letters it writes for rate-shed and terminally undeliverable dispatches, per tenant and across all tenants, as REACT already does for the actions its own gate sheds. A dispatch over that budget is still acknowledged and counted on `devicechain_outboundconnectors_connector_dispatch_total` with its usual outcome, so the `ConnectorDispatchRateLimited` alert reads the same; it is also counted on the new `devicechain_outboundconnectors_connector_dispatch_unlettered_total`, and each tenant gets one summary letter per minute in place of the individual ones. Dispatches dead-lettered after their last delivery attempt are recorded individually, as before.

**Operations, metrics and dcctl.**

- **Cross-service query, service-token and sign-in metrics.** `devicechain_svcclient_queries_total{result}` (`ok` or `error`) and `devicechain_svcclient_query_duration_seconds` cover cross-service calls past the scoped-caller gate, including validation and token acquisition. Service-token mints are counted by result in `devicechain_svcclient_service_token_mints_total{result}` (`ok` or `error`, including HTTP, transport and invalid-response failures) and timed by `devicechain_svcclient_service_token_mint_duration_seconds`; neither counts cache hits or callers waiting on the same mint, and the count excludes refusals before minting. `devicechain_userclient_logins_total{result}` (`ok`, `error` or `mfa_challenge`) counts login exchanges, not MFA verification or tenant selection, and `devicechain_userclient_refreshes_total{result}` (`ok` or `error`) counts refresh exchanges before any login fallback. Every series starts at zero and carries no tenant, identity, credential, query, endpoint or URL label. No configuration or database change is needed; during the rolling upgrade only upgraded pods expose them.
- **Use of the legacy text seal is counted.** Two process counters, `devicechain_credential_legacy_text_seal_reads_total` and `devicechain_credential_legacy_text_seal_writes_total`, count use of the legacy text form of sealed device credentials on the event wire. Writes count text seals encoded when `infrastructure.messaging.wireFormat` is `legacy`; reads count every conversion of a text seal found in a message, including malformed or empty values and redeliveries. Binary-only traffic increments neither. Both start at zero and carry no labels, so they expose no credential or tenant data. The legacy text form is still read and written exactly as before; nothing is removed. No configuration or migration is needed; during a rolling upgrade only updated pods expose the counters.
- **The service-token authority ceiling no longer flags six callers that were within it.** `devicechain_usermanagement_service_token_checks_total` counted mints by `command-delivery`, `event-sources`, `outbound-connectors` and `event-management` as `over_ceiling`, and mints by `device-state` and by `user-management` itself as `unknown_caller` under `caller="other"`, although each asked only for what it uses. The ceiling table now matches every service that mints a token, so those series stay at zero. The check still only observes: no mint is refused.
- **The bundled boards break ingest shedding down by priority class.** The Platform board now shows the messages and readings turned away at a tenant's ingest ceiling by priority class (`shed_class`: `best-effort`, `bronze`, `silver` or `gold`) as well as its totals by source, and the Ingest board adds the per-class series for messages shed at the readings ceiling. Lower-priority traffic is shed first, so the breakdown shows how important the shed traffic was.
- **`JetStreamClusterUnused` no longer fires on a shard-group instance.** An instance with [NATS shard groups](#v160-nats-shard-groups) runs a multi-server broker with every stream at one replica on purpose, and the warning fired on it continuously. The alert now stays quiet while any service reports a shard-group stream. A clustered single-replica broker with no shard groups still raises it. The alert's name is unchanged.
- **The JetStream reservation check judges one broker server.** With [shard groups](#v160-nats-shard-groups) on, `dcctl` now counts what a single server can be asked to hold: every unsharded stream, plus each sharded stream once per group that can share a server. The chart pins each group to its own `dc-nats` server, so a three-group install no longer has three copies summed against one server's volume; a `perf-20k` install with three groups fits the preset's 40Gi volume. The check before the infrastructure apply also uses a `nats_jetstream_storage` you state in `terraform.tfvars`, an `*.auto.tfvars` file or `TF_VAR_nats_jetstream_storage`, instead of the preset's size. A volume too small for one server's share is still refused before anything is created when its size is a literal. A size given as an expression or a reference cannot be read before the apply, so `dcctl` prints a notice, runs the infrastructure apply, and checks the provisioned volume after it, before the chart is installed.
- **Clearer operator manifest errors.** When the Kubernetes operator cannot decode a section of a manifest it applies, the error names the section by its 1-based position and carries the decoder's own message. Before, the cause was lost and the error ended in `<nil>`.
- **`dcctl ha verify --probe-mqtt` authenticates again.** Since `v1.1.0`, event-sources' login is the only per-area broker login the broker accepts over MQTT, but the probe presented the broker administrator's login, so it was refused on every instance with per-area broker logins. It now presents the event-sources login, which the broker already accepts over MQTT; no broker permission changes. A refused probe now names the login it presented. You rarely need the flag: event-sources' own connection creates the broker's MQTT streams.
- **A refused `dcctl bootstrap` leaves the local instance record as it found it.** When bootstrap refuses to run over an instance that is already running, `~/.devicechain/instances/<instance>/instance.json` is restored byte for byte, including its `createdAt`. Before, the refused run rewrote it with its own creation time and dcctl version.
- **`dcctl` says when it overrides a stated broker topology.** `dcctl` always sets `ha` and `nats_cluster_replicas` itself, so the broker's server count and its stream replica count stay together. A value you state for either in `TF_VAR_*` or a tfvars file is not used. `dcctl bootstrap` and `dcctl upgrade` now print a note when you state one, and [Which value wins](./sizing.md#precedence) documents the exception.
- **Issue integration bearer tokens from dcctl.** Run `dcctl product rotate-integration-token <integration-token>` after creating a LoRaWAN or webhook integration. Capture the new token from stdout; it is shown only once. Each event-sources replica can still accept the previous token for up to 60 seconds after the rotation, or for up to 15 minutes while it cannot reach device-management. Device-management still checks every event, so this window authorizes nothing extra.

**Console.**

- **The console's MQTT command uses port 8883.** The device page's connect command for MQTT now dials port 8883, the port the broker is published on outside the cluster, instead of 1883, which is the broker's port inside the cluster and is never published. On a local kind cluster, keep using `localhost` and port 1883.
- **Identities with a `%` in their email open in the console.** The identity page decoded its address twice, so such an identity could not be opened, or opened another one.
- **IdP-managed memberships say what to do instead.** When the server refuses changing the roles of, enabling, disabling or removing a membership because the identity provider manages it, the identity page stops offering those edits for it and names the remedies: change the person's groups at the provider (a person in no group it maps to a role in the tenant is disabled at their next sign-in, and enabled again once they are), disable the identity, or stop the connection serving the tenant.
- **Console detail pages open tokens that contain `%`.** The device, tenant, tier, role, AI provider, identity provider, integration, provisioning profile, trust anchor and registry detail pages decoded the token in their address a second time. A token holding a `%` opened a different token, or the page failed to load. Each page now reads the token once.
- **The console formats dates and numbers in its own language.** Timestamps and counts followed the browser's locale, so a console set to Spanish could show English dates. They now follow the language the console is set to.
- **More of the console in Simplified Chinese.** With the console set to 简体中文, the device screens and the identity directory are now translated; dashboards already were. Other screens still fall back to English.

**Simulators.**

- **A waterfront simulator scenario.** `dcctl sim create --manifest harborpulse` runs a fictional harbour district (tide gauges, stormwater pumps, street lights, EV chargers, two metered buildings and a response truck, 33 devices) through a compressed 24-minute day with a storm, a king tide, a feeder outage and a store-and-forward burst. It authors seven kinds of detection rule, one flood alarm per gauge that escalates in severity, and two rules whose commands the simulated devices obey before answering.
- **The waterfront scenario checks itself.** A `harborpulse` sim now publishes an operator board and a read-only board for the Quayside Lofts owner, reports where it is in its compressed day on its `/status`, and takes `--day-length` to stretch the day. The new `scenario-verify` tool watches one day of a running `harborpulse` sim and reads the platform back to confirm that each flood alarm escalates and clears, that the pump and charger commands are answered and change the readings, that the air-locked pump and the feeder outage are detected for exactly the right devices, that the quay correlation fires, and that buffered readings are stored at their original times. It exits 0 when all of that holds, 1 when something fails and 2 when it cannot decide.
- **Separate simulator control ports.** `dcctl sim create` now gives a new sim the lowest port from 8090–8189 that no other saved local sim uses, allowing multiple sims to run together. Re-creating an existing sim keeps the address it already has, unless another saved sim now uses that port, so `dcctl sim status` still reaches a sim that is running. The chosen address is printed; pass `--control-addr` to choose one explicitly.

#### Deletions are announced reliably {#v170-deletion-announcements}

- Deleting a device, customer, area, asset or group now records the announcement to the other services in the same database transaction as the delete, and `device-management` keeps publishing it until the message broker accepts it. Before, an announcement lost to a broker outage or a restart at the wrong moment was never sent, so dashboard shares, event anchors and detection state of the deleted entity could stay behind.
- The announcement carries the time of the delete, however late it is published.
- While old and new pods overlap during the rolling upgrade, deletes handled by a `v1.6` pod are announced the old way.
- No configuration change is needed.

#### Shares naming a deleted customer or group are removed {#v170-deleted-anchor-shares}

- When a customer or access group is deleted, `dashboard-management` removes the dashboard shares that name it. If that cleanup keeps failing, the announcement is now kept on the dead-letter stream instead of being discarded.
- `dashboard-management` also checks every 15 minutes, with the instance's service credentials, that each share's customer or group still exists, and removes shares left behind by earlier deletions, including ones from before the upgrade. It never removes anything when that lookup fails. Without the service secret or the `device-management` endpoint configured, the check is not started and a warning is logged at startup.

#### Alarms of a deleted device end, and stop escalating {#v170-deleted-device-alarms}

- When a device or other entity is deleted, each of its active alarms now ends with a `CLEARED` event carrying the new `clearReason: ORIGINATOR_DELETED` (also a new nullable `clearReason` field on the `alarmStream` subscription's `AlarmEvent`). Escalation stops, and policies with `notifyOnClear` say the alarm ended because its device was deleted.
- `notification-management` also checks with `device-management`, using the instance's service credentials, before re-paging, and ends open notifications for alarms that no longer exist, including ones left by deletes before the upgrade. If the check fails it pages anyway; failures are counted by `escalation_lookup_errors_total`. See [Alarms of a deleted device](../guides/notification-channels.md#deleted-originator).
- While old and new pods overlap during the rolling upgrade, a `v1.6` `notification-management` pod treats such a clear as an ordinary one, and a delete handled by a `v1.6` `device-management` pod emits no clear; the check before each re-page stops their escalation, and the periodic check ends those notifications once they are an hour old.

#### A deleted device's undelivered commands are cancelled {#v170-deleted-device-commands}

- When a device is deleted, `command-delivery` cancels the commands it still held for it (queued, held or parked) and refuses to send any command created before the delete, including one already handed to a transport and not yet confirmed. A device later created with the same token does not receive them; commands created for it after the delete are sent as usual.
- On first start after the upgrade, `command-delivery` also reads the deletions still retained on the message broker, so commands left behind by any device whose deletion is still retained there are cancelled the same way.
- While old and new pods overlap during the rolling upgrade, a `v1.6` pod can still send such a command until it is replaced.

#### A deleted device's live state is removed {#v170-deleted-device-state}

- When a device is deleted, `device-state` removes its live state, latest readings, latest position and dropped-reading records. A device later created with the same token starts with none of them, so members of its new owner do not see the old device's last position or readings.
- `device-state` now also asks `device-management` every 10 minutes which devices still exist, using the instance's service credentials, and removes live state left behind by devices deleted earlier, including before the upgrade. It never removes anything when that lookup fails. Without the service secret or the `device-management` and `user-management` endpoints configured, the check is skipped and a warning is logged at startup.
- A reading the platform accepted before the delete but processed after it can still re-create the deleted device's live state for a short time; the periodic check removes it if no device holds the token.

#### An update assigned to a deleted device is ended {#v170-deleted-device-updates}

- When a device is deleted, `update-management` ends its pending update assignments, and their rollout targets show as cancelled. A device later created with the same token is not offered them and cannot download their artifacts, even on another device profile.
- Assignments made before the upgrade are matched by time: one older than the device now holding its token is not offered to that device.
- While old and new pods overlap during the rolling upgrade, a `v1.6` pod can still serve such an assignment until it is replaced.

#### Event anchors of a re-created entity are kept {#v170-deleted-entity-anchors}

- When a device, customer, area, asset or group is deleted, `event-management` removes the anchors of the events it received before the delete. It used to decide by the time the device reported, so a device re-created with the same token that uploaded buffered readings dated before the delete could lose the anchors of those readings, and they then no longer appeared under their customer, area or asset. It now decides by the time the platform received each event.

#### Shard groups hold work instead of dead-lettering it {#v170-shard-groups}

- **A backlog in any group pauses MQTT forwarding for every group.** The capture reader stops fetching while any group's `inbound-events` stream is refusing, so the backlog waits in the capture stream undelivered. Earlier, events for a backlogged group other than group zero were retried and, after five deliveries, routed to `failed-decode`. Per-group isolation of MQTT capture is not provided.
- **HTTP ingest is admitted per tenant.** With shard groups enabled, an HTTP or integration request is refused only when its own tenant's group is refusing, and a refusal, including one caused by an unavailable placement, answers `503` with a `Retry-After` header. Other publish errors still answer a plain `503`.
- **A missing or unusable placement holds ingest.** While the placement table is absent, cannot be adopted, or names a group at or above `nats.shardGroups.count`, sharded ingest is held and retried rather than dead-lettered. The new gauge `jetstream_shard_placement_unavailable` reads 1 in each affected service, and the `JetStreamShardPlacementUnavailable` alert fires after two minutes. Restore a compatible placement table to resume.
- **Every writer follows the placement.** The `inbound-events` writers in device-state, sparkplug-ingest and lwm2m-ingest now publish each tenant to its own group. Before, they published every tenant to group zero, bypassing the placement and the backpressure above.
- **A service that cannot route a sharded stream refuses it.** A service that has not enabled shard routing refuses to read or write a sharded stream with a named error, instead of silently using only group zero. `dcctl` and the chart still refuse to shard `resolved-events` while device-state or event-processing is deployed, so shard only `inbound-events` on an instance that runs them.
- **Groundwork for sharding `resolved-events` ships unused.** device-state can read every group of a sharded `resolved-events`, the broker client can replay one group or the tenant's own group, and event-processing's rule preview and version compare read the tenant's own group. Per-group detection workers are built but the service does not start them yet. None of it runs while the refusal above stands.

### v1.6.0 — tighter authority checks and devices connected through broker reloads {#v160-upgrade}

`v1.6.0` is an in-place upgrade from `v1.5.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described in [Rolling upgrades](#rolling-upgrades). Use **the v1.6.0 `dcctl` for both commands**, and give `dcctl install` the cluster's settings, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. One appended migration in `device-management` makes live entity attribute keys unique and queues repairs. See [Schema changes in v1.6.0 and going back](#v160-going-back). Coming from `v1.4.x` or earlier, upgrade through each intervening minor first, including `v1.5.x`, following each release's notes with that release's own `dcctl` and `--version`: skipping a minor is not a tested path (see [Supported upgrade paths](./versioning-policy.md#upgrade-paths)). For `v1.5.0`, those are the [`v1.5.0` notes](#v150-upgrade).

```bash
dcctl install local --version v1.6.0
dcctl upgrade local devicechain --version v1.6.0 --dry-run
dcctl upgrade local devicechain --version v1.6.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds covered interfaces, removes none, and deprecates legacy npm exports, which keep working ([details](#v160-npm-deprecations)). The device relationship refusal and stricter authority checks are security fixes permitted by the policy. Agent actions, rule evaluation and the new backup and restore-planning commands are [Preview](./versioning-policy.md#preview-interfaces). Helm values and instance configuration are outside the covered compatibility surface; keep shard groups absent until the rollout completes.

What is new:

- [Before you upgrade to v1.6.0](#v160-before-upgrade)
- [New values and commands](#v160-values-commands)
- [A full broker reload keeps the devices the auth callout admitted](#v160-broker-reload)
- [Live detections of geofence rules need location access](#v160-detection-stream-location)
- [Solar campaigns need installed-position access](#v160-solar-campaign-position)
- [Automated action authorities](#v160-action-authorities)
- [The tier's rule limit applies when a profile is published](#v160-rule-ceiling)
- [Draft authors after definition deletes](#v160-review-deleters)
- [Delegated administrators cannot grant or act above what they hold](#v160-no-escalation)
- [Security: a device can no longer assign itself to a customer, area or asset](#v160-device-relationship-refused)
- [A new relationship's event is stored](#v160-new-relationship-event)
- [The HTTP device-uplink route limits guessing](#v160-device-uplink-guessing)
- [Stricter checks on HTTP credentials](#v160-http-credentials)
- [Re-pointing a stored credential requires supplying it again](#v160-credential-resupply)
- [Signing-key rotation reaches every replica](#v160-signing-key-follow)
- [ingress-nginx no longer accepts configuration snippets](#v160-nginx-snippets-off)
- [Metrics and health endpoints moved to the ops port](#v160-ops-port)
- [`dcctl destroy` refuses a path as an instance and no longer trusts a guessed cluster](#v160-destroy-instance-name)
- [MCP memory limits](#v160-mcp-memory)
- [MCP discovery advertises the scopes the server enforces](#v160-mcp-discovery)
- [API validation and administration](#v160-api-checks)
- [Command holds](#v160-command-holds)
- [Campaign planning yields between tenants](#v160-campaign-planning)
- [Downloads, sends and notifications are shared between tenants](#v160-tenant-fairness)
- [Escalation timing](#v160-escalation-clock)
- [Bounded Sparkplug tracking](#v160-sparkplug-limits)
- [Entity attributes are unique per key](#v160-entity-attributes)
- [Optional NATS shard groups](#v160-nats-shard-groups)
- [Stream and object backups (Preview)](#v160-backup)
- [Console and dashboard authoring](#v160-console-dashboards)
- [A console tab no longer continues under another tab's sign-in](#v160-console-tab-session)
- [Authoring tools and SDKs](#v160-authoring-tools)
- [Legacy npm exports are deprecated](#v160-npm-deprecations)
- [A redelivered decoder failure uses the record budget](#v160-decoder-redelivery-budget)
- [Invalid ingest failures name the device](#v160-invalid-failures-name-the-device)
- [Metrics and an alert for agent actions (Preview)](#v160-agent-metrics)
- [Other changes in v1.6.0](#v160-other-changes)
- [Schema changes in v1.6.0 and going back](#v160-going-back)

#### Before you upgrade to v1.6.0 {#v160-before-upgrade}

What needs action, in the order to do it:

- **Upgrade note: tooling.** Install OpenTofu 1.10 or later (or Terraform 1.10 or later) on the machine running `dcctl`; preflight checks presence, not version. Use the v1.6.0 tool, install with the cluster's settings, then upgrade each instance. There is no downgrade.
- **Upgrade note: the broker restarts.** The upgrade replaces the broker image, so every broker pod restarts. With `--mqtt-fail-closed` off (the default), a QoS 1 message the broker acknowledges while a stream changes leader can be lost. Turn it on before you upgrade (`dcctl upgrade --mqtt-fail-closed`) if devices must not lose acknowledged messages ([fail-closed MQTT](./observability.md#mqtt-fail-closed)).
- **Upgrade note: `dcctl install` preflight.** On a cluster that kept its `ingress-nginx` controller, install refuses while any Ingress of that class carries a `*-snippet` annotation. Upgrade the owning instance or remove the annotation first ([details](#v160-nginx-snippets-off)).

**Correct configuration before restarting.** Review hand-edited instance configuration; invalid service settings stop startup. Service keys below live under `functionalAreas.<area>.config`.

- **Signing-key retention must cover refresh tokens when rotation is on.** In `user-management`, when `auth.signingKeyMaxAgeDays > 0`, `auth.signingKeyRetentionDays × 86400` must be at least `auth.refreshTokenTtlSeconds` after defaults are applied; equality is allowed. Previously rotation silently lengthened a shorter retention. `0` retention takes 8 days, and the default refresh TTL is 604800 seconds (7 days). Increase retention or reduce the refresh TTL before upgrading. With rotation off, this comparison does not apply. The chart does not compare these fields, and `dcctl` already refuses overlays of this auth block.
- **Token TTL overflow is refused.** `user-management` now rejects `auth.accessTokenTtlSeconds` or `auth.refreshTokenTtlSeconds` above **9223372036 seconds**, before conversion to a duration. `0` still takes the defaults (900 and 604800 seconds respectively); negative TTLs were already invalid. The Helm schema also enforces the upper bound.
- **Tenant-purge overflow is refused.** In `user-management`, `tenantPurge.intervalSeconds`, `tenantPurge.settleSeconds` and `tenantPurge.tokenHoldSeconds` cannot exceed **9223372036 seconds**. Existing rules still apply: a negative interval disables the coordinator; negative settle/hold values are refused, and the settle and token-hold safety floors remain.
- **Dead-letter overflow is refused.** In `user-management`, `deadLetters.retentionDays` cannot exceed **106751 days**, and `deadLetters.sweepSeconds` cannot exceed **9223372036 seconds**. Existing default, disable and minimum-retention rules remain.
- **Sparkplug presence-probe overflow is refused.** In `sparkplug-ingest`, `presence.probeSeconds` and `presence.probeWindowSeconds` cannot exceed **9223372036 seconds**. `0` still takes 1800 and 10 seconds respectively. Existing floors remain: a written interval is at least 60 seconds; a written window is at least 5 seconds and strictly below the interval.

**Review permissions and callers before rolling.**

- **Upgrade note:** Give geofence `detectionStream` consumers `location:read`; the read-only OAuth scope alone does not include it. Give roles managing `SOLAR` campaigns `location:installed:read` or `location:read`.
- **Upgrade note:** Automation authors need `command:write` for `sendCommand` and `connector:write` for `httpCall` or `publish`, in addition to `device:write`, including publish and rollback. Review delegated administrator roles: operations above their current authorities now return `ESCALATION`; use a superuser for those operations.
- **Upgrade note:** Replace device-authored tracked or `contains` `NewRelationship` assignments with claim codes or `createEntityRelationship`. Review existing tracked relationships; the upgrade does not remove them. Old pods still accept the events during the roll.
- **Upgrade note:** Narrow MCP queries that exceed 256 KiB responses or 1,000 time buckets and handle HTTP 429 with `Retry-After`. Keep draft `evaluateRules` graphs at or below 256 detection rules; oversized graphs are refused before compilation. Renew expired automation reviews before publishing.
- **Upgrade note: credentials and senders.** Clients that change the type or destination of a connector, notification channel or AI provider holding a stored credential must send `secret` in the same request ([details](#v160-credential-resupply)). A rule whose `httpCall` names a `connector/` secret handle is refused at publish, and dead-lettered if it is already published ([details](#v160-http-credentials)). HTTP uplink senders must back off on `429` and `503` with `Retry-After` ([details](#v160-device-uplink-guessing)).

**During and after the rollout.**

- **Keep `infrastructure.nats.shardGroups` absent throughout the v1.5 → v1.6 roll.** A `v1.5` pod rejects this new key as unknown if it loads the new configuration mid-roll. Finish upgrading all pods before opting in. When present, `count` must be 2–16; suffixes must be unique and limited to `inbound-events` and `resolved-events`; `placementTags` keys must be group numbers in `0..count-1`, including the existing group zero, with nonblank tag strings. `dcctl` derives a `dc-nats-<group>` placement tag for every group, including `dc-nats-0`. Invalid blocks are refused. What `dcctl` accepts is narrower: see [Optional NATS shard groups](#v160-nats-shard-groups).

- **Entity attributes are unique per key.** The upgrade collapses duplicate live rows from concurrent first writes, keeping the lowest row ID, and queues group membership and configuration repairs. The index build briefly blocks attribute writes. While old and new replicas overlap, an old replica contending on a concurrent first write may refuse it; retry the write. No configuration change is needed.

- **Upgrade note:** Add `AgentExecutionsFailing` to alert routing, and mirror `nats:2.15.3-alpine` if you keep the upstream-broker fallback ready. Freeze writers before using the new backup commands.
- **Upgrade note: scrape port.** If your own Prometheus configuration scrapes pods, change the target port from 8080 to 8083; the chart's ServiceMonitors and probes follow automatically ([details](#v160-ops-port)).

#### New values and commands {#v160-values-commands}

| Setting or command | What changes / operator action |
|---|---|
| `--size perf-20k` | New throughput sizing preset: broker request 6 CPUs / 6Gi, memory limit 10Gi, JetStream 40Gi and event storage 260Gi. It does not promise the 50 ms PUBACK latency target; the preset has not yet been measured as written. |
| GKE `nats_jetstream_storage_class` | New brokers default to `premium-rwo` (`pd-ssd`) when that GKE class exists; a GKE `premium-rwo` class with a different disk type is refused. Explicit overrides win. Existing brokers/PVCs and upgrades keep their storage class; this does not migrate disks. |
| GKE `backup_object_store_storage_class` | New backup PVCs use a dedicated `pd-standard` class; GKE's `standard-rwo` uses `pd-balanced` and still consumes SSD quota. Override it with this variable. Existing PVCs keep their size and class on upgrades; no automatic migration occurs. Other providers are unchanged. |
| `infrastructure.nats.shardGroups` | Opt-in tenant routing for the two ingest streams; absent preserves existing routing. Group streams multiply reservations by `count`, so budget capacity before enabling it. Enable only after the roll completes; see [Optional NATS shard groups](#v160-nats-shard-groups). |
| `dcctl backup`, `dcctl backup inspect --verify`, `dcctl restore --plan` | **Preview** commands for local stream/object backup sets, byte verification and read-only restore planning. There is no restore apply command or coordinated database checkpoint. See [Exporting streams and object content](./disaster-recovery.md#export-streams-objects). |

#### A full broker reload keeps the devices the auth callout admitted {#v160-broker-reload}

A full broker reload used to close every connection the auth callout had admitted, which includes every MQTT device that authenticates with a username. The broker is now built on `nats-server` `v2.15.3`, so a change to the CA bundle in the broker's TLS Secret (a CA rotation), or a manual reload, keeps those connections open. A reload without a restart happens only when the chart's config-reloader sidecar sends SIGHUP because the mounted TLS Secret changed, or when an operator runs `nats-server --signal reload` by hand.

When a reload changes the server's own authentication settings (its configured users, nkeys, tags, or TLS verify/map settings), `nats-server` still sends callout-admitted connections back through the auth callout. A connection the callout then refuses is closed.

- **Nothing needs doing.**
- **A configuration change still rolls the broker.** The broker's configuration is rendered into a ConfigMap, and the chart hashes it into the StatefulSet's pod template. Any change to that rendered configuration (settings, sizing or accounts) restarts the broker pods, so devices reconnect then, as before; `dcctl upgrade` rolls the broker for any upgrade that changes its image or configuration.
- **A renewed TLS certificate** under the same CA keeps its earlier behaviour, in place since `v0.27.0`: it is applied to new connections without a reload.
- **The upstream image for going back to the upstream broker is now `nats:2.15.3-alpine`**: set `server_image_repository = "nats"` and `server_image_tag = "2.15.3-alpine"`, as described in [Device certificates](./device-certificates.md). Add the new tag to the images you mirror if you keep that rollback ready.
- **A broker pod that is stopped while it is still starting now stops promptly.** The broker keeps upstream `nats-server` `v2.15.3`'s signal handling: a reload requested while JetStream is still recovering runs once startup completes, and a SIGTERM, or the lame duck request the pod's preStop hook sends, is handled in the meantime instead of waiting behind it. A lame duck request that arrives before the broker is accepting clients is ignored, so the SIGTERM that follows stops the pod. `nats-server -t --test_reload <file>` reports whether a configuration file can be reloaded without a restart (exit code 3 when it cannot).

#### Live detections of geofence rules need location access {#v160-detection-stream-location}

The `detectionStream` subscription no longer delivers detections of rules that test geofence containment to a caller without `location:read`, matching rule preview. Other detections are unchanged. A dashboard or agent that watches geofence rules needs a role or token that includes location access. A token minted through the read-only OAuth scope does not include `location:read`.

#### Solar campaigns need installed-position access {#v160-solar-campaign-position}

Creating a `SOLAR` campaign, reading its elements (`campaignElements`) or cycles (`campaignCycles`), and retrying its failures (`retryCampaignFailed`) now also requires `location:installed:read` (or `location:read`). A solar schedule's per-device times reveal each device's installed position; even cycle counts can reveal position-derived results for a group with one device. `CRON` and `CALENDAR` campaigns are unchanged. Add this authority to any role that manages solar campaigns.

#### Automated action authorities {#v160-action-authorities}

Authoring or publishing an automated action now requires the authority to perform it. Creating or updating a rule with a `sendCommand` action, and publishing or rolling back to a profile version whose enabled rules include one, requires `command:write` in addition to `device:write`. `httpCall` and `publish` actions require `connector:write`. A role that holds `device:write` alone can still author and publish rules that only raise alarms. Rules already published keep running; they are checked at the next publish or rollback. Review the roles that author automation before upgrading. Older `device-management` replicas do not enforce this until they are replaced.

#### The tier's rule limit applies when a profile is published {#v160-rule-ceiling}

- **Opt-in rule ceiling:** device-profile publish and rollback enforce `rulesPerTenant` only when the tenant tier explicitly sets it. Absent/null values and older user-management schemas preserve existing behavior; unchanged or reduced rule counts never read user-management. Other lookup failures fail closed only for rule increases.

#### Draft authors after definition deletes {#v160-review-deleters}

Change review normally excludes people who only deleted rules, metrics or commands on other profiles from draft authors. Deletes not attributed to a row, from before the upgrade or a not-yet-upgraded pod during a rolling upgrade, are handled conservatively: if a definition disappeared from the reviewed profile without an attributed delete explaining it, everyone who made an unattributed delete since that definition's last attributed change counts as an author. Reviews already run keep the author list they recorded; run a new review if an approver was excluded. Deleting a profile now writes one audit entry per definition it removes.

- **Nothing needs doing.** Run a new review if an approver was excluded by an unrelated deletion.

#### Delegated administrators cannot grant or act above what they hold {#v160-no-escalation}

The operator admin API now limits an identity whose system role is not superuser to the authorities its current system roles grant. The tenant API also checks the current role and its members before a tenant role is narrowed.

- **Operator admin API:** a delegated identity can assign only system roles whose authorities it holds; can act on another identity (password, sign-in links, sessions, enable or delete, system roles) only if it holds everything that identity's system roles grant; and can create, change or delete a role only within the authorities it holds.
- **Memberships and sign-in role mappings:** on the operator admin API, a delegated identity can add or change a tenant membership only with tenant roles whose authorities it holds, can change, disable or remove a membership only if it holds everything that membership's roles grant, and can map identity-provider groups or just-in-time sign-ins only to tenant roles it holds. Acting on another identity also requires holding everything its tenant memberships grant, and linking accounts to, re-scoping, or changing the groups claim of a tenant identity-provider connection requires holding every role that connection grants.
- **Tenant API:** a tenant administrator can change a tenant role only if they hold every authority it grants, and can remove authorities from it only if they hold everything its members hold.
- **What to check:** a delegated system role that needs to administer superusers or define roles beyond its own authorities now gets `ESCALATION`; make those changes as a superuser.

#### Security: a device can no longer assign itself to a customer, area or asset {#v160-device-relationship-refused}

- A `NewRelationship` event naming a tracked relationship type (the built-in `assigned`, or any type marked tracked) or the built-in `contains` is refused and recorded on the `failed-events` stream with reason `Invalid` on its first delivery, without retries. This changes what an accepted device event does; the [versioning policy](./versioning-policy.md) allows this for a security fix.
- Assign devices with a claim code or the `createEntityRelationship` mutation.
- Relationships devices created earlier are not removed. Review the tracked relationships of your devices.
- Untracked types, including `member`, are still created, unless your tenant marked that type tracked. The `NewRelationship` event itself no longer carries the new target as an anchor: an untracked relationship is not ownership, so the event is scoped by the device's existing anchors, as its next telemetry event is.
- During the rolling upgrade, pods still on v1.5 create the edge until they are replaced.
- New counter `resolve_relationship_refused_total` counts these refusals without tenant or device labels; the warning log names the tenant and correlation.

#### A new relationship's event is stored {#v160-new-relationship-event}

A `NewRelationship` event a device sends is now stored and listed by `events` (event type `0`) with its anchors. Before this release it was retried, then recorded as an ingest failure with reason `API_CALL_FAILED`, although the relationship had been created. An event of a type the event store does not hold is now refused on its first delivery with reason `INVALID` instead of being retried. Events sent before the upgrade are not restored.

- **Nothing needs doing.**

#### The HTTP device-uplink route limits guessing {#v160-device-uplink-guessing}

The opt-in HTTP device-uplink route (`httpIngest.deviceUplinks`) now limits requests per source address (`429` with `Retry-After`). After repeated credential failures for a device, it stops answering authentication guesses (`503` with `Retry-After`) instead of repeating `401`. While this device's credential is being guessed, every HTTP uplink for the device, including one with the correct credential, receives `503` with `Retry-After` until the hold ends (at most 60 seconds at a time).

- **Devices on the broker's MQTT connection are unaffected.**
- **Back off on `503`**, even when presenting a wrong credential: wait at least `Retry-After` before retrying.

#### Stricter checks on HTTP credentials {#v160-http-credentials}

An `httpCall` action can no longer name a connector's credential. A secret handle in the `connector/` namespace is refused when a rule is published, and a published rule that uses one is dead-lettered with outcome `invalid` instead of sending the connector's credential. Older `outbound-connectors` replicas do not enforce this until they are replaced.

#### Re-pointing a stored credential requires supplying it again {#v160-credential-resupply}

**Re-pointing a stored credential.** `updateConnector`, `rollbackConnector`, `updateNotificationChannel` and `updateAiProvider` refuse with `SECRET_RESUPPLY_REQUIRED` when a request changes the type or the destination of a connector, notification channel or AI provider that holds a stored credential and does not send `secret` in the same request. Send the credential again, or `secret: null` to remove it. Edits that do not move the destination (name, topic, QoS, custom headers, link base URL, model, enabled) are unaffected, and existing connectors, channels and providers keep delivering. To roll a connector back to a version with a different destination, re-point it with `updateConnector` and its credential instead. With `dcctl product apply`, set the connector's credential handle when a manifest changes its destination. Older replicas do not enforce this until they are replaced.

#### Signing-key rotation reaches every replica {#v160-signing-key-follow}

With `auth.signingKeyMaxAgeDays` set, user-management now rotates an over-age signing key while it runs: about 15 minutes after a replica starts, and whenever the key crosses the age afterwards, not only at startup. Every replica adopts the new key within 30 seconds, and validates and serves it straight away when it sees a token or a key request that needs it. Earlier releases applied a rotation only in the replica that made it, so with more than one replica some sign-ins failed until the others restarted.

- **Nothing needs doing.** Nothing changes when the setting is 0.

#### ingress-nginx no longer accepts configuration snippets {#v160-nginx-snippets-off}

`dcctl install` turns configuration snippets off on a cluster's retained `ingress-nginx` controller and returns its annotations risk level to the controller default. Before applying, install refuses while any Ingress of that controller's class carries a `*-snippet` annotation, naming each Ingress and annotation. Upgrade the instance that owns it with `dcctl upgrade`, or remove the annotation, then re-run `dcctl install`.

No chart since `v0.25.2` sets a snippet. Fresh clusters, which run Traefik only, are unaffected. This change reaches a cluster at its next `dcctl install`.

#### Metrics and health endpoints moved to the ops port {#v160-ops-port}

Service metrics and health endpoints moved off the API port. `/metrics`, `/healthz` and `/readyz` are served on port 8083 (`ops`) of each area's pods and Service and are no longer reachable through `/api/<area>/...`. The chart's ServiceMonitors and probes follow automatically. If you scrape pods with your own Prometheus configuration, change the target port from 8080 to 8083. During the rollout, pods not yet replaced are not scraped.

#### `dcctl destroy` refuses a path as an instance and no longer trusts a guessed cluster {#v160-destroy-instance-name}

`dcctl destroy` refuses an instance argument that is not a plain name (`.`, `..`, or anything containing a path separator). Earlier releases resolved it as a directory under `~/.devicechain/instances` and removed what it resolved to.

- **Local state needs evidence.** `dcctl destroy` clears local state for a missing cluster only when the instance has a record of its cluster and that cluster's recorded context no longer answers. An instance with no record goes through the normal uninstall, which fails if the cluster cannot be reached; pass `--kube-context`, or remove the instance's directory by hand if its cluster is really gone.

#### MCP memory limits {#v160-mcp-memory}

- **MCP memory limits:** downstream GraphQL responses over 256 KiB and aggregates over 1,000 time buckets are refused whole; narrow the time range, select one metric, or widen the interval. POSTs and requests carrying bodies are capped at 4 in flight per caller and 64 per process before reading or parsing. Message admission reserves up to 4 slots per batch; valid configured batches (16 entries by default) execute tools in waves within that cap. Every entry still counts toward the caller's rate limit; excess requests receive HTTP 429 with Retry-After. Tools are cancelled and execution slots released when their HTTP request ends. Body-read reservations are released before tool execution; the 1 MiB request ceiling remains. No configuration change is needed.

#### MCP discovery advertises the scopes the server enforces {#v160-mcp-discovery}

MCP discovery now offers `read-only` and `location`, plus `act:*` when `agentActuation.enabled` is true on MCP. Challenges let clients use this metadata instead of pinning `read-only`. Admission still requires only `read-only`; action tools retain their scope and permission checks. No configuration change is required; existing tokens remain valid. Reauthorize clients to grant newly discovered scopes, and enable actuation on the authorization server too when using proposals.

#### API validation and administration {#v160-api-checks}

- `evaluateRules` (Preview) refuses draft graphs above 256 detection rules with `LIMIT_EXCEEDED` before compiling any rule. Rule validation now preserves each diagnostic's field path so clients can point to the offending field.
- `existingEntityRefs` rejects unknown entity types or more than 1,000 lookup keys of one type, including duplicates, before running any lookup; previously the same refusal could follow earlier lookups.
- Admin `updateRole` accepts optional `expectedUpdatedAt`; a stale save is refused so the caller can reload. Omitting it retains unconditional updates.
- `setTenantEnabled` refuses a tenant whose purge has begun and rechecks its identity under lock, so a concurrent purge or reused token cannot toggle another tenant.
- Agent execution decisions (Preview) bound proposer and approver principals to 256 characters and supplied session IDs to canonical 36-character UUIDs before session reads; cancellation stops further reads. Service-account proposers may omit the session ID.
- Expired approved automation reviews read and filter as `EXPIRED`, including at the exact expiry time; publish returns `REVIEW_EXPIRED`, and rejection returns `REVIEW_NOT_PENDING`. Expiry is computed from stored expiry and current policy without a background ledger update.

#### Command holds {#v160-command-holds}

Deleted maintenance windows no longer release waiting commands unconditionally. Commands whose device type now has no window are released within about a minute; disabled devices or unresolved types stay gated until resolved and then follow their type. If a device's command channel cannot be resolved, its commands stay `QUEUED`, never published, until the first delivery sweep after recovery (30 seconds by default), instead of entering `HELD`. The hold reconciler continues while releasing commands, up to half its interval, and stops at a page with no releases. New counter: `command_delivery_dispatch_deferred_total`. Nothing needs doing. Older pods retain their previous behavior until the rolling upgrade completes.

#### Campaign planning yields between tenants {#v160-campaign-planning}

- **Campaign planning yields between tenants.** Each tenant plans at most 1,000,000 campaign elements and 128 cycles per pass, and starts no new cycle after a 2-second planning budget; a cycle already started always finishes, and progress is saved after each cycle so the remaining cycles resume next pass. Group and device-location lookups for planning time out after 2 seconds and are reported as a pass error without marking the campaign failed. Up to four tenants run concurrently. Large plans may fill their 36-hour window over multiple passes. These limits need no new configuration or migration and take effect as command-delivery pods roll forward.

#### Downloads, sends and notifications are shared between tenants {#v160-tenant-fairness}

- **OTA downloads:** OTA downloads now allow one in-flight stream per (tenant, device, assignment) on each replica. While another tenant holds a slot, a tenant at or above half of `updates.maxConcurrentDownloads` (rounded down, minimum one; eight with the default of 16) cannot start another stream. A lone tenant may use all slots. Excess requests receive the existing 503 busy response with `Retry-After: 5`; retry after that delay. No configuration change is required.
- **Connector sends and LoRaWAN downlinks:** Outbound connector dispatches and LoRaWAN downlinks now limit each tenant to half of each replica's send pool (rounded down, minimum one), derived from `maxConcurrentSends`. Excess messages wait for broker redelivery without occupying send workers. A pool of one still serializes tenants; use at least two workers for isolation. No new configuration is needed; the limit takes effect as each pod upgrades.
- **Notification scheduling is bounded per tenant.** Four workers serve tenants in rotating order. Each tenant stops starting releases and escalations after twice the whole-dispatch budget (80 seconds at the defaults) while another tenant is waiting for a worker; a tenant nobody is waiting behind keeps going; an alarm already started keeps its full budget, and remaining alarms stay due for the next tick. Clear announcements run within each tenant job under their own budget. Channels that exhaust their timeout attempts are skipped for the rest of that tenant job; an escalation routed only to those channels keeps its tier and is retried next tick. The first probe can still spend a tier. No configuration change is needed, and existing configuration works throughout rolling upgrades.

#### Escalation timing {#v160-escalation-clock}

The escalation window is now counted from when the page was sent. An alarm raised from a late-arriving reading no longer escalates on the next scheduler pass, and a device clock ahead of the platform no longer delays escalation.

- **Nothing needs doing.** The upgrade adds a nullable timestamp automatically; existing open alarms retain their previous timing until their next page.

#### Bounded Sparkplug tracking {#v160-sparkplug-limits}

**Upgrade note:** Each Sparkplug source now tracks at most 50,000 node sessions, including at most 1,024 nodes that have not sent NBIRTH. New identities past a limit are dropped without ingestion or a rebirth request; existing nodes can still rebirth, and online sessions are never evicted. Idle placeholders expire after 10 minutes, offline sessions after 24 hours; at capacity, births reclaim the oldest offline sessions. The liveness history holds at most 50,000 identities, prunes old entries on pressure, and refuses further new entries while full (while it is full, a probe does not declare an unrecorded identity silent). Group, node and device topic levels longer than 256 characters are refused, matching the existing device-registration ceiling. Capacity refusals are counted by `devicechain_sparkplugingest_session_refused_total` with `reason` values `placeholder_cap`, `session_cap` or `seen_cap`. Capacity and queue-full warnings are each limited to one per source per 10 minutes; per-message logs move to debug. No configuration change is needed for rolling upgrades.

#### Entity attributes are unique per key {#v160-entity-attributes}

- **Entity attributes are unique per key.** The upgrade collapses duplicate live rows from concurrent first writes, keeping the lowest row ID, and queues group membership and configuration repairs. The index build briefly blocks attribute writes. While old and new replicas overlap, an old replica contending on a concurrent first write may refuse it; retry the write. No configuration change is needed.

#### Optional NATS shard groups {#v160-nats-shard-groups}

NATS shard groups remain off by default. A new instance can opt in through `--values`
at `instance.config.infrastructure.nats.shardGroups` when it is installed. The setting
supports 2-5 groups with R1 streams on a cluster of at least three broker servers, and
refuses `--ha`. Placement tags use the corresponding `dc-nats` pod names; explicit tags
must match those names. The default profiles support sharding `inbound-events`;
`resolved-events` is refused while device-state or event-processing is deployed because
those consumers still read only group zero. Each selected stream reserves its full byte
ceiling in every group, and `dcctl` checks that reservation before it creates or changes
any infrastructure, so grow the JetStream volume first if the check refuses it.

`dcctl upgrade` refuses to enable shard groups on an existing instance whose broker is a
single server, because turning a standalone JetStream server that holds data into a
cluster is not supported. It also refuses an upgrade whose `--values` (or
`--reset-values`) would drop a recorded `shardGroups` block, lower its `count`, or remove a stream from its
`suffixes`, since the groups may hold data: carry the block forward in every replacement
values file.

#### Stream and object backups (Preview) {#v160-backup}

`dcctl backup <instance> --destination <dir>` exports checkpoint-preserved streams with consumers and object content into a new local backup set. Freeze writers first; this does not coordinate database cutoffs or maintenance mode. It works on every profile, recording streams absent because their area is not deployed. Each stream, KV bucket and object store declares its backup class.

`dcctl backup inspect <dir> --verify` verifies the manifest and each listed file's size and SHA-256 offline; it does not prove complete inventory or checkpoint consistency. `dcctl restore <instance> --from <dir> --plan` verifies bytes, identity and coverage before reading target streams and objects, and refuses a plan that could overwrite newer content. Freeze target writers while planning. `--force-foreign` permits a foreign identity but bypasses neither coverage nor newer-content checks. No restore is applied. For filesystem blobs, supply `--blob-directory` with a read-only mounted or exported PVC root. See [Exporting streams and object content](./disaster-recovery.md#export-streams-objects).

#### Console and dashboard authoring {#v160-console-dashboards}

- Dashboard authors can select a breakpoint and move, resize or place widgets for it; base edits still flow to layouts that inherit them. Existing layouts remain valid.
- The rule editor adds Filter, Exists and Math forms for supported expressions; custom expressions remain available.
- **Admin → Settings → Functional areas** shows deployed areas and instructions to enable other areas.
- One-time secret and invitation dialogs require **Done** to close; Escape and outside clicks leave the value visible. Detail pages ignore stale responses after navigation.
- Batch fire reuses its idempotency token on retries, and the device command panel ignores duplicate submits. Command-control widgets clear pending state when the device changes; alarm triage coalesces refreshes during event storms.
- A dashboard retries a device-type lookup after an earlier miss. Scoped chart palettes refresh when the host theme changes. GraphQL documents use direct imports to reduce the console bundle.
- The remaining text of the shared dashboard widgets is translated into Spanish and Simplified Chinese.

#### A console tab no longer continues under another tab's sign-in {#v160-console-tab-session}

A console tab whose session ended no longer continues under a session another tab opened for a different tenant or user; it returns to sign-in.

- **Nothing needs doing.**

#### Authoring tools and SDKs {#v160-authoring-tools}

- From `frontend`, `npm run create-widget -- acme/tank-level ../my-widget` creates a typed custom-widget package with SDK peers and refuses an existing destination. If peer versions are `0.0.0-dev`, set released versions before installing.
- Rule and connector authoring JSON Schemas can be emitted with `go run ./cmd/rule-schema` from `backend/services/event-processing` and `go run ./cmd/connector-schema mqtt` from `backend/services/outbound-connectors`.
- Published client and widget SDK packages now include their agent guidance.

#### Legacy npm exports are deprecated {#v160-npm-deprecations}

The following exports are deprecated in `v1.6.0` and keep working unchanged. Update imports or calls to the replacements below. No export is removed. Removal requires a major release after at least two minor releases of deprecation; with deprecation starting in 1.6, no removal is eligible before a major after 1.7. `BuiltinOptionIssue` names the existing built-in issue shape (`widgetType`); the general `@devicechain/dashboards.OptionIssue` (`widgetId`) remains supported.

- `@devicechain/widgets` / `pickSample` is deprecated; use `@devicechain/widget-sdk.pickSample`.
- `@devicechain/widgets` / `optBoolean` is deprecated; use `@devicechain/widget-sdk.optBoolean`.
- `@devicechain/widgets` / `WidgetProps` is deprecated; use `@devicechain/widget-sdk.WidgetProps`.
- `@devicechain/widgets` / `WidgetComponent` is deprecated; use `@devicechain/widget-sdk.WidgetComponent`.
- `@devicechain/widgets` / `WidgetFrame` is deprecated; use `@devicechain/widget-sdk.WidgetFrame`.
- `@devicechain/widgets` / `WidgetSubjectProvider` is deprecated; use `@devicechain/widget-sdk.WidgetSubjectProvider`.
- `@devicechain/widgets` / `WidgetSelectProvider` is deprecated; use `@devicechain/widget-sdk.WidgetSelectProvider`.
- `@devicechain/widgets` / `useWidgetSelect` is deprecated; use `@devicechain/widget-sdk.useWidgetSelect`.
- `@devicechain/widgets` / `WidgetTimeZoneProvider` is deprecated; use `@devicechain/widget-sdk.WidgetTimeZoneProvider`.
- `@devicechain/widgets` / `useWidgetTimeZone` is deprecated; use `@devicechain/widget-sdk.useWidgetTimeZone`.
- `@devicechain/widgets` / `WidgetThemeRootProvider` is deprecated; use `@devicechain/widget-sdk.WidgetThemeRootProvider`.
- `@devicechain/widgets` / `useWidgetThemeRoot` is deprecated; use `@devicechain/widget-sdk.useWidgetThemeRoot`.
- `@devicechain/widgets` / `WidgetCandidatesProvider` is deprecated; use `@devicechain/widget-sdk.WidgetCandidatesProvider`.
- `@devicechain/widgets` / `useWidgetCandidates` is deprecated; use `@devicechain/widget-sdk.useWidgetCandidates`.
- `@devicechain/widgets` / `useFlashOnChange` is deprecated; use `@devicechain/widget-sdk.useFlashOnChange`.
- `@devicechain/widgets` / `flashTextStyle` is deprecated; use `@devicechain/widget-sdk.flashTextStyle`.
- `@devicechain/widgets` / `flashBackgroundStyle` is deprecated; use `@devicechain/widget-sdk.flashBackgroundStyle`.
- `@devicechain/widgets` / `FlashValue` is deprecated; use `@devicechain/widget-sdk.FlashValue`.
- `@devicechain/widgets` / `fitValueFont` is deprecated; use `@devicechain/widget-sdk.fitValueFont`.
- `@devicechain/widgets` / `useMeasurementStream` is deprecated; use `@devicechain/widget-sdk.useMeasurementStream`.
- `@devicechain/widgets` / `useAlarmStream` is deprecated; use `@devicechain/widget-sdk.useAlarmStream`.
- `@devicechain/widgets` / `useCommandStream` is deprecated; use `@devicechain/widget-sdk.useCommandStream`.
- `@devicechain/widgets` / `useLocationStream` is deprecated; use `@devicechain/widget-sdk.useLocationStream`.
- `@devicechain/widgets` / `useDatasourceAvailability` is deprecated; use `@devicechain/widget-sdk.useDatasourceAvailability`.
- `@devicechain/widgets` / `useDashboardHub` is deprecated; use `@devicechain/widget-sdk.useDashboardHub`.
- `@devicechain/widgets` / `useHubConnectionState` is deprecated; use `@devicechain/widget-sdk.useHubConnectionState`.
- `@devicechain/widgets` / `HubConnectionBadge` is deprecated; use `@devicechain/widget-sdk.HubConnectionBadge`.
- `@devicechain/widgets` / `useResolvedBindings` is deprecated; use `@devicechain/widget-sdk.useResolvedBindings`.
- `@devicechain/widgets` / `useViewerBindings` is deprecated; use `@devicechain/widget-sdk.useViewerBindings`.
- `@devicechain/widgets` / `useSlotCandidates` is deprecated; use `@devicechain/widget-sdk.useSlotCandidates`.
- `@devicechain/widgets` / `useCandidates` is deprecated; use `@devicechain/widget-sdk.useCandidates`.
- `@devicechain/widgets` / `css` is deprecated; use `@devicechain/widget-sdk.css`.
- `@devicechain/widgets` / `FlashDirection` is deprecated; use `@devicechain/widget-sdk.FlashDirection`.
- `@devicechain/widgets` / `WidgetFrameProps` is deprecated; use `@devicechain/widget-sdk.WidgetFrameProps`.
- `@devicechain/widgets` / `WidgetSelect` is deprecated; use `@devicechain/widget-sdk.WidgetSelect`.
- `@devicechain/widgets` / `WidgetCandidates` is deprecated; use `@devicechain/widget-sdk.WidgetCandidates`.
- `@devicechain/widgets` / `MeasurementStreamState` is deprecated; use `@devicechain/widget-sdk.MeasurementStreamState`.
- `@devicechain/widgets` / `AlarmStreamState` is deprecated; use `@devicechain/widget-sdk.AlarmStreamState`.
- `@devicechain/widgets` / `CommandStreamState` is deprecated; use `@devicechain/widget-sdk.CommandStreamState`.
- `@devicechain/widgets` / `LocationStreamState` is deprecated; use `@devicechain/widget-sdk.LocationStreamState`.
- `@devicechain/widgets` / `CandidatesState` is deprecated; use `@devicechain/widget-sdk.CandidatesState`.
- `@devicechain/widgets` / `DatasourceAvailability` is deprecated; use `@devicechain/widget-sdk.DatasourceAvailability`.
- `@devicechain/widgets` / `HubConnectionBadgeProps` is deprecated; use `@devicechain/widget-sdk.HubConnectionBadgeProps`.
- `@devicechain/widgets` / `ThemeVar` is deprecated; use `@devicechain/widget-sdk.ThemeVar`.
- `@devicechain/widgets` / `EChart` is deprecated; use `@devicechain/widget-sdk/echarts.EChart`.
- `@devicechain/widgets` / `buildLineOption` is deprecated; use `@devicechain/widget-sdk/echarts.buildLineOption`.
- `@devicechain/widgets` / `buildGaugeOption` is deprecated; use `@devicechain/widget-sdk/echarts.buildGaugeOption`.
- `@devicechain/widgets` / `resolveChartTheme` is deprecated; use `@devicechain/widget-sdk/echarts.resolveChartTheme`.
- `@devicechain/widgets` / `useChartTheme` is deprecated; use `@devicechain/widget-sdk/echarts.useChartTheme`.
- `@devicechain/widgets` / `EChartOption` is deprecated; use `@devicechain/widget-sdk/echarts.EChartOption`.
- `@devicechain/widgets` / `LineChartOptions` is deprecated; use `@devicechain/widget-sdk/echarts.LineChartOptions`.
- `@devicechain/widgets` / `GaugeChartOptions` is deprecated; use `@devicechain/widget-sdk/echarts.GaugeChartOptions`.
- `@devicechain/widgets` / `ChartTheme` is deprecated; use `@devicechain/widget-sdk/echarts.ChartTheme`.
- `@devicechain/widgets` / `WIDGET_CHANNEL` is deprecated; use `builtinWidgetDescriptors[id].channel`.
- `@devicechain/widgets` / `WIDGET_BINDS_DATASOURCE` is deprecated; use `builtinWidgetDescriptors[id].datasource.mode !== 'none'`.
- `@devicechain/widgets` / `isTerminalStatus` is deprecated; use `@devicechain/dashboards.isTerminalCommandStatus`.
- `@devicechain/widgets` / `OptionIssue` is deprecated; use `@devicechain/widgets.BuiltinOptionIssue`.

#### A redelivered decoder failure uses the record budget {#v160-decoder-redelivery-budget}

A payload decoder failure that is redelivered (for example after a restart with a backlog of a failing device type's uplinks) now uses the per-device-type failure-record budget like any other failure. Once the budget is spent it is counted as suppressed and not written to the failed-events stream, even if its first delivery never produced a record. Previously a redelivered failure was always recorded, so a restart could record every uplink of a backlog.

- **Nothing needs doing.**

#### Invalid ingest failures name the device {#v160-invalid-failures-name-the-device}

Ingest failures with reason `INVALID` that device-management recorded for a readable event (a value that fails its metric's definition, an event older than 366 days, a decoder refusal) now name the device and the event type, and are bounded per device like every other reason. Before this release they were listed with no device and shared one bound of ten records an hour for the whole tenant. Records indexed before the upgrade are not rewritten.

- **Nothing needs doing.** There is no backfill. The existing limits now apply: ten records per device and reason per hour, capped at 1,000 itemized records per tenant and reason per hour. A fleet-wide invalid payload can reach that cap.

#### Metrics and an alert for agent actions (Preview) {#v160-agent-metrics}

`device-management` now reports agent actions: proposals by kind and outcome, approvals by outcome, executions by kind and result with their duration, and proposals that expired. The Platform board gains an **Agent actions (preview)** row, and a new warning alert, `AgentExecutionsFailing`, fires when final approvals keep failing to complete. Refusals do not raise it: a dependency change, a policy refusal, a closed kill fence or an approver refusal is counted under its own result. The metrics are listed in the [metrics reference](./metrics-reference.md), and the alert has an [alert runbook](./alert-runbook.md#agentexecutionsfailing) entry. Like agent actions, they are Preview: names and labels may still change in a minor release.

#### Other changes in v1.6.0 {#v160-other-changes}

- A bootstrap refused before its first write leaves the recorded phase unchanged; MQTT uplink overlay refusals name the conflicting choice.
- OpenTelemetry diagnostics and errors use the services' structured JSON logs. The broker closes and joins its metrics endpoint at shutdown.
- The PostgreSQL client dependency moves to `pgx` 5.12.0 with security fixes for `GHSA-j38q-h3ph-jjpq` and `GHSA-95fj-3fxw-g62g`. No configuration change is needed.

#### Schema changes in v1.6.0 and going back {#v160-going-back}

One appended `device-management` migration adds an entity-attribute repair queue and a unique index for live attribute keys. It keeps the lowest row ID among duplicates and queues group-membership and configuration repairs; see [Entity attributes](#v160-entity-attributes). Each entity and scope has one current attribute value per key after repair. No other area appends a schema migration in this release.

There is no downgrade. Retry a failed upgrade with the v1.6.0 tool, roll forward to a corrected release, or restore from a suitable backup. The Preview stream/object commands do not replace database backups or apply a restore.

## Older releases {#older-releases}

When a new minor release line is added, the oldest line documented in full moves here as a short entry. Each entry links to the full notes in the documentation for that version, as they stood at its release, and to its GitHub release. Every release is listed on [GitHub Releases](https://github.com/devicechain-io/devicechain/releases).

### v1.5.0 — reviewed automation changes and sign-in-bound agent approvals {#v150-upgrade}

<a id="v150-before-you-upgrade"></a><a id="v150-dcctl"></a><a id="v150-configuration"></a><a id="v150-input-limits"></a><a id="v150-tightenings"></a><a id="v150-automation-review"></a><a id="v150-agent-actions"></a><a id="v150-lorawan"></a><a id="v150-observability"></a><a id="v150-concurrent-edits"></a><a id="v150-other-changes"></a><a id="v150-going-back"></a><a id="v150-known-issues"></a>

Full notes: [v1.5 documentation](/1.5/deployment/releases-and-upgrades#v150-upgrade). GitHub release: [v1.5.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.5.0).

### v1.4.0 — confirmed LoRaWAN downlinks, a device's own payload over MQTT, and notifications that name the device {#v140-upgrade}

Full notes: [v1.4 documentation](/1.4/deployment/releases-and-upgrades#v140-upgrade). GitHub release: [v1.4.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.4.0).

### v1.3.0 — commands reach HTTP and LoRaWAN devices, and deliveries leave evidence {#v130-upgrade}

<a id="v130-token-audience"></a>

Full notes: [v1.3 documentation](/1.3/deployment/releases-and-upgrades#v130-upgrade). GitHub release: [v1.3.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.3.0).

### v1.2.0 — payload decoders a tenant writes, and revocation that ends sessions {#v120-upgrade}

Full notes: [v1.2 documentation](/1.2/deployment/releases-and-upgrades#v120-upgrade). GitHub release: [v1.2.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.2.0).

### v1.1.0 — ingest costs less per event, and devices outside the cluster connect over MQTT {#v110-upgrade}

<a id="v110-ingest"></a>

Full notes: [v1.1 documentation](/1.1/deployment/releases-and-upgrades#v110-upgrade). GitHub release: [v1.1.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.1.0).

### v1.0.0 — the first stable release: a database outage holds events, and Traefik is the default front end {#v100-upgrade}

Full notes: [v1.0 documentation](/1.0/deployment/releases-and-upgrades#v100-upgrade). GitHub release: [v1.0.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.0.0).

<a id="v0270-upgrade"></a><a id="v0260-upgrade"></a><a id="v0252-upgrade"></a><a id="v0251-upgrade"></a><a id="v0250-upgrade"></a><a id="v0240-upgrade"></a><a id="v0230-upgrade"></a><a id="v0220-upgrade"></a><a id="v0220-packages"></a><a id="v0211-upgrade"></a><a id="v0210-upgrade"></a><a id="v0200-upgrade"></a><a id="v0190-upgrade"></a><a id="v0180-upgrade"></a><a id="v0170-upgrade"></a><a id="v0160-upgrade"></a><a id="v0150-upgrade"></a><a id="v0120-upgrade"></a>

Releases before `v1.0.0` are not listed here. Their notes are on [GitHub Releases](https://github.com/devicechain-io/devicechain/releases) and in the repository at each release's tag.

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
