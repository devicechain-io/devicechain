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

- **[v1.10](#v1100-upgrade)**: [v1.10.0](#v1100-upgrade)
- **[v1.9](#v190-upgrade)**: [v1.9.0](#v190-upgrade)
- **[v1.8](#v180-upgrade)**: [v1.8.0](#v180-upgrade)

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

### v1.10.0 — in development {#v1100-upgrade}

These notes are for the release in development and grow until it ships.

#### Before you upgrade to v1.10.0 {#v1100-before-upgrade}

- **The database now refuses device attribute writes that skip the writer protocol.** The device-management database carries a trigger on its attribute table that rejects, with SQLSTATE `DCAP1`, any write to a device's `SHARED` attribute that is not made inside a transaction that set `devicechain.attr_protocol` to `1`. Every v1.9 pod already does, so a normal upgrade from v1.9 is unaffected. Across a skipped minor (an upgrade from v1.8 or older straight to this release, which is not supported), a pod that has not been upgraded yet fails its device `SHARED` attribute writes with `DCAP1` until the rolling update finishes, then works again once every pod runs this release; nothing is written wrongly in between. Other attribute scopes and other owners (assets, areas, customers) are not affected. SQL run by hand against a device's shared attributes must go through the API instead, or set the marker for its own transaction with `SELECT set_config('devicechain.attr_protocol', '1', true)`. `bulkChangeReadiness` (and the deprecated `fleetOperationReadiness`) now reports `enforcement: ENFORCED` on an upgraded instance.
- **Credentials embedded in backup endpoints are deprecated.** `dcctl install` still accepts an `endpointUrl` with userinfo, a query or a fragment, but now warns without displaying the credential. These values will be refused in a later major release, after the deprecation period. Use a credential-free `endpointUrl` and the `accessKeyId` and `secretAccessKey` fields in `--backup-credentials-file`. The endpoint continues to be stored unchanged in the `dc-infra/dc-install` ConfigMap because later bootstrap and destroy operations need it.
- **Device bulk changes (Preview).** In v1.10, device “fleet operations” are now called “bulk changes”. Use GraphQL `bulkChangeReadiness` (`BulkChangeReadiness`, `BulkChangeEnforcement`) and `dcctl bulk-change readiness`. The deprecated `fleetOperationReadiness` field, its `FleetOperationReadiness` / `FleetOperationEnforcement` types, and `dcctl fleet-op readiness` keep working, including for v1.9 clients. The old command prints a one-line deprecation notice to stderr. The `fleet:operate` authority is unchanged. The Preview clock still starts at v1.9.
- **Bulk changes apply lines only on upgraded pods.** While device-management rolls from v1.9 to v1.10, a pod still on v1.9 answers a bulk-change call as an unknown field, and `dcctl bulk-change` reports **still upgrading; retry**. Devices keep accepting ordinary attribute writes from both releases throughout. There is nothing to do: once the roll has finished, `dcctl bulk-change readiness` lists `config.set` when enforcement reads `ENFORCED` and the tenant has no orphaned attribute rows.
- **Rotate a credential you passed in a URL to dcctl, and read two OpenTofu outputs differently.** If you gave dcctl a restore source, image registry, backup endpoint, object-store endpoint or broker URL with a user name, password or token in it, treat logs and CI output from earlier runs as having recorded it, and rotate it. Separately, the `backup_endpoint_url` and `database_restored_from` outputs of the OpenTofu roots, and the restore source in a plan, are now sensitive and show as `(sensitive value)`. dcctl reads them as before; a script that reads plain `tofu output` must use `tofu output -raw <name>` or `tofu output -json`.
- **The `Instance` resource is now also served as `core.devicechain.io/v1` (preview).** `dcctl install` applies a CRD that serves `v1` beside `v1beta1` with an identical schema, and the stored form is still `v1beta1`, so no object is rewritten and nothing needs converting. `dcctl` and the operator keep reading and writing `v1beta1`, and existing manifests keep applying unchanged. What does change is discovery: Kubernetes prefers the newer version, so an unqualified `kubectl get dci`, and any generic client that asks the API server for the preferred version, now speaks `v1`. Output that prints the `apiVersion` (for example `kubectl get dci -o yaml`) shows `core.devicechain.io/v1`. Only `Instance` gains `v1`: the group's other kinds (`InstanceOperation`, `ExecutorEnrollment`, `InstanceConfiguration`) are still served only as `v1beta1`, so a tool that applies the group's preferred version to every kind in it must ask per kind. Pin a version with `kubectl get instances.v1beta1.core.devicechain.io` if a script depends on the old one. Both versions stay structurally identical for as long as both are served: a field is added to both or to neither.
- **Instance and executor compatibility policy.** The versioning policy now commits to covering `core.devicechain.io/v1 Instance` from v2.0; v1 is Preview in v1.10. Instance `v1beta1` stays served throughout v2.x. v2.0 will not remove `spec.grafanaSSO`, `spec.compact` or `InstanceConfiguration`. `ExecutorEnrollment` and `InstanceOperation` stay v1beta1 through v2.0; their Preview promotion is triggered by enabling action execution in a released build after the checks that refuse unsafe instance actions have shipped and explicit provider privileges are in place, rather than by the usual two-minor limit. Storage migration and executable actions have not shipped. See [Versioning and Upgrade Policy](./versioning-policy.md#instance-api-compatibility).
- **Notification-policy `deviceTypeToken` is deprecated in v1.10.** Device-type scoping was never applied. The field remains available on the create input, policy output and search criteria, working exactly as before until it is refused in a later major release, after the deprecation period: non-blank writes are refused, accepted policies read null, and any supplied filter value matches no API-created policy. Leave the field and filter unset for tenant-wide policies. Scoping by anchor or group is planned, with no release version promised.
- **Replays are now capped by default.** Each event-processing process runs at most 8 rule previews, version comparisons and `dcctl test` evaluations at once (`preview.maxConcurrent`), with no setting needed. A busy instance may therefore see BUSY refusals or degraded preview answers after the upgrade where it saw none before; the author retries. Raise `preview.maxConcurrent` once every pod runs this release if your tenants need more.
- **Preview promotion conditions.** The Preview rule now uses per-surface promotion conditions, with two minors after joining as the default. Rule testing and product as code are covered from v1.10. Agent actions and automation review are covered from v2.0, when approvals move to a common approval model. Newly listed surfaces are command-delivery's proposal operations, including `proposeCommand`; MCP's `propose_alarm_action`, `propose_command`, `get_proposal` and `list_my_proposals`; device-management's automation review operations; and event-processing's service-only `compareRuleVersions`. Read each surface's condition in the versioning policy before upgrading.
- **Commands sent by rules now count against the tenant's outbound rate.** A `sendCommand` action now gets a separate budget of the same size as connector calls (`httpCall` and `publish`), using the same outbound settings and tenant overrides: 100 actions a second with a burst of 200 unless the tenant has its own override. A tenant whose rules send more commands than that will have the excess refused where the previous release sent it. Before upgrading, check each tenant's sendCommand peak alone against its ceiling. `react_actions_dispatched_total` filtered to `sendCommand` can help. It has no tenant label, so it is an upper bound on any one tenant's rate. Where a tenant may exceed its ceiling, raise that tenant's override, or the platform default (`outboundCallsPerSecond` and `outboundBurst` in the detection engine's settings, kept equal to the outbound-connectors values). A rule may also send at most 120 commands to the same device in one minute, which cuts off a rule that keeps re-triggering itself through the device it commands; that limit is fixed. Both limits are kept in memory by each replica and start afresh on a restart or failover. Commands that the detection engine re-derives after a restart are still sent, exactly once downstream, and do not use either allowance. A refused command is not sent. It is recorded as a dead letter with reason `shed` and detail `command_rate_limited`, `command_loop` or `command_guard_full` (see `dcctl dead-letters`), and counted on `react_command_refused_total` with `result="unwanted"`. Command admissions made at the platform default because the tenant ceiling is unresolved are counted separately on `react_command_governance_unresolved_admissions_total` by `cause`. Refusals beyond the dead-letter budget are counted and summarised in one dead letter per tenant per minute.
- **`STRING` metric definitions that already exist start storing readings on upgrade.** Declaring one used to be refused, so a definition can exist only if it was created some other way; once the upgrade is finished its readings are stored as text instead of being dropped.
- **During a rolling upgrade**, pods of the previous release treat a `STRING` metric differently, so declare `STRING` metrics after the upgrade has finished. A device-management pod that has not been upgraded drops a declared `STRING` value as `declared_not_storable`; if a message is redelivered between an old and a new device-management pod, its numeric readings can be stored twice, doubling the hourly rollup's sum and count for that message. A device-state pod that has not been upgraded stores a `STRING` value that looks like a number (`"12"`) as the number 12 and leaves any earlier text beside it until a newer reading replaces the row. An event-processing pod that has not been upgraded lets a rule read such a value as a number.

#### What is new in v1.10.0 {#v1100-whats-new}

- **The analytics views survive a rolling upgrade.** An event-management pod of an older release that finds the analytics (SQL/BI) views already at a newer generation leaves them as they are instead of rebuilding them, logs the skip at info level and counts it in `devicechain_eventmanagement_analytics_reconcile_newer_surface_skips_total`.
- **Command proposals show their reasons in the approvals inbox.** On the Commands tab, each row now shows the first two reasons for the proposal, with a "+N more" count when there are more; the detail page still lists them all. Alarm rows are unchanged.
- **Bulk changes finish, resume and report their errors reliably.** A bulk change whose lines have all been applied now always reaches `DISPATCHED`, even when its failure or ambiguity limit is reached by the last lines and the pass that applied them was interrupted; before, it could be paused for good. A resumed bulk change now always gets its own 24 hours before it is paused as `DEPENDENCY_UNAVAILABLE`, so a wait left from before the pause can no longer pause it again at once, and a resume that took effect is reported as done. `lastErrorCode` now reports the last retryable error any line met, as documented. `bulkChangeLines` accepts a new `afterOrdinal` criterion, and `dcctl bulk-change lines` pages on it, so filtering by `--posting` or `--settlement` on a running bulk change no longer skips lines. The `posting` and `settlement` filters of `bulkChangeLines` and the `dispatch` and `evidence` filters of `bulkChanges` now refuse a value that is not a state of the bulk change (for example a lower-case `ambiguous`) with `INVALID_REQUEST`, instead of answering with no lines.
- **Device bulk changes are available as Preview.** A bulk change sets or removes one declared SHARED configuration key (`config.set`) on up to 10,000 devices: plan it, create it, review its contract, start it, and follow each device's line until the device itself answers for it. The device-management GraphQL API serves `planBulkChange`, `createBulkChange`, `startBulkChange`, `pauseBulkChange`, `resumeBulkChange`, `abortBulkChange`, `reverseBulkChange`, `resolveAmbiguousBulkChangeLines`, `previewBulkChangeResume`, `previewBulkChangeReversal`, `bulkChange`, `bulkChanges` and `bulkChangeLines`, and `dcctl bulk-change` adds `plan`, `create`, `start`, `status`, `lines`, `pause`, `resume`, `abort`, `reverse` and `resolve`. A line counts as confirmed only on the device's own report; a line no report decides by its deadline ends ambiguous, and a person can record a verdict on it with a recent second factor. Creating, starting, resuming, reversing and resolving need the `fleet:operate` authority, which no built-in role grants (a role with every authority, `*`, holds it; otherwise grant it in a custom role); pausing and aborting need only `device:write`. Agent and service identities and scoped members are refused. All of it is Preview and may change in a minor release. See [Device bulk changes](../guides/bulk-changes.md).
- **Alerts and metrics for bulk changes.** With `metrics.alerts` on, the chart adds a `bulk-changes` PrometheusRule with seven alerts — `BulkChangesNotAdvancing`, `BulkChangesStalled`, `BulkChangesDriverSilent`, `BulkChangesAuthorityUnavailable`, `BulkChangesSettlementLagging`, `BulkChangesFencePending` and `BulkChangesNotActivated` — each with an entry in the alert runbook. They read the `devicechain_devicemanagement_bulk_change_*` series, which carry no tenant, operation or device label. A device-management pod still on v1.9 does not export those series, so while device-management rolls the alerts read only the upgraded pods; nothing to do. `BulkChangesSettlementLagging` also reads the `devicechain_devicemanagement_bulk_change_reconciler_lag_seconds` gauge, which is `NaN` while a pod has no successful measurement, and the alert fires when no pod can measure it.
- **The `/dash` viewer app can check its requests against the schema its instance serves.** Like the console, the viewer asks each GraphQL endpoint for the schema it serves, using an authenticated `OPTIONS` request, and refuses a request the instance cannot serve before sending it, with an error that says what is missing. It is on only where the viewer can be a different release from its instance: under `vite dev`, or in a build made with `VITE_SCHEMA_NEGOTIATION=on`, which is the setting for a viewer you host apart from the instance it reaches. A viewer served by its own instance stays off, so a proxy that answers `OPTIONS` cannot break it; set `VITE_SCHEMA_NEGOTIATION=off` to turn it off in dev too. Sign-in, token refresh and sign-out are not checked. An instance older than v1.9.0 does not offer the discovery, so a viewer with negotiation on refuses requests to it with an error naming v1.9.0. If a CORS proxy or web application firewall answers `OPTIONS` itself, discovery fails and every request is refused, so let `OPTIONS` reach the GraphQL endpoints.
- **Dashboard viewport previews.** Preview layouts at 390, 768, 960, 1366, 1920 or 3840 pixels and review layout findings for that width. Return to Container to resume editing; preview choices never change the saved dashboard or live device bindings.
- **A link to a command opens on that command.** A device link of the form `/devices/<token>?tab=commands#command-<token>`, such as the one the approvals inbox builds, now scrolls to the command's row and highlights it briefly, on any page of the device's history. A command that is not on the loaded page is read by its token and pinned above the history without a Cancel button. A command that no longer exists, belongs to another device or cannot be read shows a note instead.
- **Docker CPU diagnostics.** When Docker's CPU count cannot be read, `dcctl preflight` and install preflight now show an explicit `UNKNOWN` row with guidance to check `docker info`. The result remains advisory for Compact and Standard installs, does not block the install, and no longer reports that all checks passed.
- **The waterfront 3D scene draws every device of its scenario.** `demos/harborpulse-unity` now shows the six devices the `harborpulse` simulator provisions beside its district: two chargers that connect over MQTT with a device token, two that connect with a client certificate, and two lamps that fetch their commands over HTTP. The chargers continue North Quay's charging row and the lamps stand at a landing by the wall. Each kind looks different and shows only what it reports: a charger's lamp is lit while it reports and turns violet when it has gone silent, and a pull lamp's post shows its reported dim level as four pips. The opening caption now counts 43 devices. The scene's layout file changed with this, so use the scene and the simulator from the same release: a scene built before this release turns its guided tour off when it meets a newer simulator. A live or preview player also accepts `-hp-eye x/y/z/lookX/lookY/lookZ/fov`, which holds the camera at one place for a whole run.
- **The waterfront 3D scene's guided tour stops at the six devices that hold their own session.** A live day of `demos/harborpulse-unity` gains one page at North Quay, shown when the charger that dropped its MQTT session at 12:00 reconnects at 13:00. It says that the platform raised an alarm for the dropped session and cleared it by itself, that the dim command waited for the two lamps that fetch their commands and both now report the new level, that two chargers connect with their own certificates and were sent a lower power limit as configuration, and, after the hot connector's page, that one certificate was disabled and only that charger is locked out. The first two are shown only when the platform's own alarm and readings bear them out. The recorded 90-second and five-minute cuts are unchanged.
- **Historical hour rollups.** Event management now fills older hour rollups in the background, newest first, with at most four hourly windows and a 30-second work budget per pass. Reads continue using minute rollups until each hour is complete. The worker shares the existing backfill lock and connection-pool headroom requirement. Operators can track progress with the `devicechain_eventmanagement_rollup_hour_backfill_complete_from_timestamp_seconds`, `devicechain_eventmanagement_rollup_hour_backfill_remaining_windows`, and `devicechain_eventmanagement_rollup_hour_backfill_windows_total` metrics and the standard periodic-task metrics. Retained minute summaries survive hourly backfill after raw chunks expire. Refresh-policy lock conflicts are retried on the next pass.
- **`dcctl install --output json`.** `dcctl install` takes a new `-o, --output` flag. `--output json` reports the install as an event stream for automation: one JSON object per line on stdout and nothing else there, covering each stage as it starts, progresses, finishes or fails, the preflight report, the install's own output as log lines, and a final summary line with the verdict. The stream never reads stdin: `--yes` gives the one confirmation an install asks, and without it a run that needs that confirmation is refused before it changes anything. Secret values are never written to it. The exit status and the failure message on stderr are unchanged. The default, `--output text`, prints exactly what it printed before. The events are described in the [install event stream reference](../reference/install-events.md).
- **`dcctl install --output json` keeps its guarantees at the edges.** The exit status always matches the stream's final verdict, `0` or `1`, even when a tool the install ran was killed or dcctl itself failed unexpectedly, and the stream still ends with its summary line in that case; text output also exits only `0` or `1` again. `--help` with `--output json` prints the help on stderr and leaves stdout empty. Nothing the install runs can read stdin, so a kubeconfig exec credential plugin runs non-interactively. A kubeconfig dcctl cannot parse is now reported by its path, without the parser's reason, which could quote the tokens and passwords in the file, in both output formats, and the kubeconfig's credentials are also redacted from the stream's log lines. With `--output json`, the error printed on stderr and everything else that reaches dcctl's own stdout and stderr during the run, a kubeconfig exec credential plugin's stderr included, are redacted too; with `--output text`, the error dcctl prints is. A kubeconfig that cannot be read or turned into a client is reported by the kind of problem, the file and the context name. A kubeconfig credential is redacted only as a whole word and never changes what the run does, and a value too short to redact (under 4 bytes) is warned about rather than silently left. The [install event stream reference](../reference/install-events.md) now lists every field of every event and describes how progress is coalesced and when an interrupted OpenTofu command can outlive the run.
- **Bounded JSON-mode shutdown.** `dcctl install --output json` no longer hangs while draining process output when the original stderr consumer stops reading. Diagnostics received by the reader are preserved through the drain deadline; pending diagnostics may be dropped after that deadline.
- **dcctl no longer prints credentials embedded in a URL you give it.** A restore source
  (`--restore-rdb-from`, `--restore-tsdb-from`), an image registry (`--registry`), a backup
  endpoint, an object-store endpoint and a broker URL (`--nats-url`) were printed in full, so a
  user name, password, signed query or fragment token written into one reached the terminal, the
  `--output json` event stream and the terminal UI. Wherever dcctl shows such a value it now
  replaces the user information, the query and the fragment with `REDACTED`
  (`s3://user:pass@bucket/archive` is shown as `s3://REDACTED@bucket/archive`), including when
  they are percent-encoded or the value is not a well-formed URL. The output of the tools dcctl
  runs (OpenTofu, ko, docker), which quotes those values back, is filtered the same way. Values
  without credentials are shown exactly as before.
- **Reserved intent authorities.** The new tenant authorities `intent:read` and `intent:write` are reserved for an upcoming feature, declared intents; nothing checks them yet, so granting them gives a role no access in this release. They are not added to any built-in role or to the access every member receives (tenant administrators holding `*` already satisfy both). Grant them to a custom role only **after the upgrade has finished rolling out**: a user-management pod that has not yet been upgraded refuses any role edit whose authority set contains a name it does not know, including an unrelated edit of a role that already holds one.
- **MCP proposal auditing.** Audit schema 3 adds `proposal` and `verdict` to tool-call logs: Preview proposal tools record the stored proposal token and platform decision with allowlisted refusal codes. Reads leave both fields empty; existing audit fields keep their meanings.
- **Rule previews, version comparisons and `dcctl test` evaluations are now capped per event-processing process.** Each tenant was already held to two at a time, but nothing bounded their sum, so on an instance whose resolved-events stream is not sharded by tenant, N busy tenants could run 2N whole-stream replays at once and slow detection for everyone. One event-processing process now runs at most 8 at a time across all tenants (`preview.maxConcurrent`, 1 to 256); the three share one allowance, and each replica has its own allowance, so N replicas run up to N × the setting. A request that finds no free slot is refused at once as busy, as a degraded preview result or a refusal on a comparison or evaluation, never as an empty success, and the author retries. Slots are shared fairly: a tenant's second concurrent slot is granted only while fewer than half the slots are in use, so tenants running two at a time cannot lock out a tenant running none. The key is absent by default. Set it only once every pod of the instance runs a release that knows it, because an older pod refuses a configuration document that carries an unknown key.
- **Rule authoring provenance.** Published device profile versions now record the publisher's stable principal ID and the set of doors their draft rules were saved through: Form, Canvas, Describe, product as code, or API. A later form edit preserves Describe's AI-assisted origin. Existing records read UNKNOWN; the existing publisher name and canvas graph are unchanged. Clients can optionally identify the save door; saves that omit it record API.
- **A metric can have the data type `STRING`.** A device can report a state or a mode as text, for example `"heating"`, under a metric declared `STRING` on its profile. Declare it in the console's metric form, through the API or with `dcctl`. The reading is stored with the device's history, kept as its last-known value (`textValue` on `latestMeasurements` and on the event queries), and shown in the console's readings list. It is never averaged, summed or charted as a number: an aggregate over it is refused, and a detection rule does not see it as a number, even when the text looks like one (`"12"`). A rule that names a `STRING` metric never fires, and the console's rule authoring and the AI rule drafter do not offer one. A value is at most 4,096 bytes of valid UTF-8 with no NUL byte; a longer or invalid value is dropped on its own, counted as `declared_not_storable`, and shown on the device's page like any other dropped reading, and the rest of the message is kept. Text for a key the profile does not declare is still dropped. A `STRING` metric cannot have a formula. The analytics view of measurements does not expose the text column yet.
- **A device's latest reading can hold text.** The latest-reading projection that `latestMeasurements` serves gains a nullable `textValue` field beside `value`: a reading of a STRING metric keeps its text there and leaves `value` null, and a numeric reading is unchanged. The device page shows the text. Every reading stored before the upgrade keeps its `value` and has a null `textValue`. The change adds one nullable column to the device-state store, with no backfill and no default.
- **A dashboard sample can carry a text reading.** `MeasurementSample` in `@devicechain/dashboards` gains an optional `textValue`, and the live measurement stream now selects it, so a reading from a `STRING` metric reaches a widget as text instead of an unexplained empty value. It is absent on a numeric reading and on a server that predates `STRING` metrics. The built-in widgets still plot and aggregate numbers only and skip a sample whose `value` is null, so a text reading is never drawn as a gap or a zero.
- **`get_latest_measurements` and `query_measurements` return a `STRING` metric's text.** Until now an assistant reading a device's latest values or history saw a `STRING` metric as an empty value. The text now comes back as `text` under the result's `untrusted` member, marked as device-reported, so an assistant treats it as data and never as instructions. A numeric reading is unchanged, and an empty text arrives as no text.
- **The rule editor warns when a rule names a `STRING` metric.** A detection rule never reads a text reading, so a rule that names a metric declared `STRING` on its profile can never fire. The console's rule form now says so while the rule is being written, naming the metric; the rule is still accepted and saved. The check is also available to API clients: `validateDetectionRules` takes an optional `metrics` list (each entry a `key` and a `dataType`, at most 1,000) and answers a `namesStringMetric` warning for each such metric a rule names. Leave the list out and no such warning is given. An unknown `dataType`, or a key listed twice with different types, is refused. The console sends the list only for a profile that has a `STRING` metric, so during a rolling upgrade an older event-processing pod simply gives no inline check for such a profile until the upgrade finishes.
- **Update artifacts larger than 64 MiB can be stored on an S3 object store.** Uploads to the S3 backend now stream to the store in parts instead of being held in memory, so the 64 MiB limit that applied only to S3 is gone. An artifact is limited by `updates.maxArtifactBytes` (512 MiB by default) on every backend, the size and digest are still verified while the upload streams, and a refused upload leaves no partial object behind. Raising `updates.maxArtifactBytes` above 64 MiB now takes effect on S3; it was silently held at 64 MiB before. The `ARTIFACT_TOO_LARGE_FOR_BACKEND` refusal is no longer returned.

#### A terminal UI for install and setup (opt-in) {#v1100-install-terminal-ui}

`dcctl install local --ui=tui` runs the whole cluster install in a full-screen terminal UI, and the new `dcctl setup local` (Preview) carries on from it to a first instance. Both are opt-in: without `--ui`, or with `--ui=plain`, which is also the path for screen readers, `dcctl install` prints, prompts and exits exactly as before. See [Install terminal UI](./install-terminal-ui.md).

- **Choosing it.** `--ui=tui` falls back to the plain output whenever stdin, stdout or stderr is not an interactive terminal, under CI, with `TERM=dumb`, and for `--dry-run`. `--ui=auto` chooses the terminal UI only on an interactive terminal, and never with `--yes` or `--dev`; with `--yes` or `--dev`, `--ui=tui` shows progress only and exits when the install ends. The logo splash animates for up to 1.2 seconds on a capable local terminal, and is a static mark, or a one-line header, everywhere else; `--no-splash` skips it. Every screen is readable with `NO_COLOR` and on a 16-colour or ASCII-only terminal, and it runs on the Windows console (Windows Terminal, PowerShell or `cmd`) with the console restored on exit.
- **Welcome and preflight.** The install first shows what it will do (the target, footprint, topology and image source, with "No changes have been made") and then runs the read-only preflight checks on screen. Every check reads PASS, WARN, FAIL, UNKNOWN, NOT NEEDED or NOT CHECKED as a word, and the focused check shows its fix. Recheck runs the checks again after you fix something, and the footprint can be switched between Standard and Compact, after a summary of what that changes, with every check run again for the new footprint. While any check fails, Continue is unavailable and there is no way past it; `--skip-preflight` is still the only way to skip the checks.
- **Cluster choices.** After the preflight, the install shows the target (a managed local kind cluster, which you can rename, or an existing context picked from the names in your kubeconfig), the footprint (Standard or Compact, with what Compact leaves out spelled out under it) and the topology (Single or HA), each marked with the flag that set it. Choose another context is also on the preflight screen. Advanced opens a drawer with editors for the monitoring stack, the bundled CloudNativePG operator and, on Compact, TLS, and says whether database backups run, as the plan decides it; every other flag you gave on the command line is listed there by name and carried into the install unchanged, and credentials show only as "configured (hidden)". Each change is checked the way the command line would check it, so a combination the command line refuses is refused on screen too, and the read-only checks run again for the changed plan: Continue stays unavailable until they pass.
- **An existing cluster is checked too, as advice.** With `--kube-context`, an "Existing cluster (advisory)" group follows the preflight checks: whether the API server's name resolves, whether it answers over a TLS certificate the context trusts, whether the context's credentials are accepted, whether they may create what the install creates (asked with access reviews, which change nothing), and whether this machine can reach the provider, chart and image registries the install uses. They never go through a proxy: when one is configured, for the context or in the environment (`HTTPS_PROXY` and the like), the rows it would carry read NOT CHECKED. These checks only read, follow no redirect, give up after 5 seconds to connect and 15 seconds to answer, and never block Continue: a problem reads WARN, and anything they cannot decide reads UNKNOWN or NOT CHECKED, never PASS. On Windows, a refused connection reads WARN ("refused the connection"). The registry checks follow the plan: a component the plan leaves out (monitoring under Compact or `--no-monitoring`, the CloudNativePG operator under `--no-cnpg`, cert-manager under Compact without TLS) is not checked and cannot warn, and the ingress controllers' charts follow the cluster's IngressClasses: Traefik's always, ingress-nginx's only when the cluster runs ingress-nginx and the install keeps it. The plain output, `dcctl preflight` and the install's own preflight are unchanged.
- **Credential plugins.** A context that gets its credentials from an exec plugin, as GKE, EKS and AKS contexts usually do, is checked for authentication and permissions too: the advisory checks run the plugin themselves, never interactively (no terminal, nothing on its input), for at most 10 seconds, and stop it together with everything it started when that time is up or you leave the screen. The credential it prints is used only for the context's API server, over the same read-only, redirect-free, proxy-free connection as any other credentials, never after it expires, and is never shown. A plugin whose entry says `interactiveMode: Always` is not run (NOT CHECKED), a plugin that is not installed reads WARN, and one that fails or prints no usable credential reads UNKNOWN; what the plugin prints as an error is never shown, since it can hold a token or a key. Legacy `auth-provider` entries are never run.
- **Nothing is installed without your approval.** Continue on the cluster choices opens a review of the plan as the command line would resolve it: the target and whether it is created or reused, the release and image registry, the footprint and topology, TLS, monitoring, backups, any warnings, and where the cluster's state will be kept. It also shows the equivalent non-interactive `dcctl install` command, which reproduces the same plan with `--yes`; a credential such as an alert webhook appears only as a placeholder, and the command is then marked as a template. The review opens with Back focused, and keys typed before it has settled are ignored, so a second Enter can never approve it. The install starts only when you choose Approve and install, and that approval also answers the question to create the local kind cluster, for that cluster only. Going back and changing anything makes a new plan: its checks run again and it needs a review of its own. Until you approve, Exit, Ctrl+C, an interrupt signal, or the terminal UI stopping ends the command having changed nothing.
- **Progress and the result.** The progress screen shows the install's stages with their state. The first Ctrl+C stops the install safely and the screen stays until it has stopped; a second Ctrl+C exits at once. A failed install names the stage, the cause, what may have changed, whether the cluster's lock was released, a command to inspect the cluster and the command to re-run once the cause is fixed; nothing is rolled back, and dcctl still exits 1. A successful one shows the `dcctl bootstrap … --ui=tui` command to run next.
- **Help on every screen.** `?` or F1 opens help on every screen of the install and of `dcctl setup local`, the progress screen included. Help lists exactly the keys the screen's footer shows, says what the screen does, scrolls on a small terminal, and changes nothing: it never answers a question, never pauses the install, and on a screen whose approval starts something the screen waits for the keyboard to be quiet again after help closes. Where `?` can be typed, in a name or a passphrase, F1 opens help.
- **`dcctl setup local` (Preview): a guided install, then bootstrap, with two approvals.** One terminal session prepares a cluster and creates a first instance on it. The cluster stage is the install's own screens and review; once the cluster is installed, a hand-over screen leads to the instance and root-key escrow choices and the instance's own review. Nothing of the instance runs without its own approval, and no key typed before a review has settled approves it. The escrow passphrase is asked for on screen and never shown. If you leave, or the instance fails, after the cluster stage, the cluster stays installed and setup prints the exact `dcctl bootstrap … --ui=tui` command that creates the instance on the same screens. It is guided only: without an interactive terminal it changes nothing and exits 2, and it has no `--yes`. Its exit status says what it left behind: 0 both done, 1 the cluster stage failed, 2 nothing changed, 3 the cluster installed and no instance created, 4 the cluster installed and the instance failed. `dcctl install` and `dcctl bootstrap` are unchanged. See [Install terminal UI](./install-terminal-ui.md#setup-local).

- **`dcctl bootstrap --ui=tui`: the instance in the terminal UI.** `dcctl bootstrap local <instance> --ui=tui` creates the instance in a full-screen terminal UI: the instance and root-key escrow choices, seeded from the command line; a review of the exact bootstrap with its equivalent command, which only **Approve and create the instance** starts; the bootstrap's stages as they run, with the escrow passphrase asked for on screen and never shown; then the escrow receipt, the one-time first-login password and the instance summary. Every flag the screens do not choose, and every flag they choose and leave as it was, is carried into the run exactly as given, empty values included, and an escrow path inside the instance's state directory is refused before the approval. With `--yes` or `--dev` it shows progress only. `--ui` on `dcctl bootstrap` has the same values, default (`plain`) and fallbacks as on `dcctl install`: without it, off an interactive terminal, under CI, with `TERM=dumb` and with `--dry-run`, `dcctl bootstrap` prints, prompts and exits exactly as before, and the escrow passphrase still comes from `--escrow-passphrase-file`, then `DCCTL_ESCROW_PASSPHRASE`, then a person. Inside `dcctl setup local`, the cluster welcome now says the instance follows. See [Install terminal UI](./install-terminal-ui.md#bootstrap-ui).

### v1.9.0 — updates roll out in cohorts, and tools check what an instance serves {#v190-upgrade}

`v1.9.0` is an in-place upgrade from `v1.8.x`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described in [Rolling upgrades](#rolling-upgrades). Use **the v1.9.0 `dcctl` for both commands** with OpenTofu or Terraform 1.10 or later, and give `dcctl install` the cluster's settings, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. Thirteen migrations are appended and run at each service's first start: three in `command-delivery`, four in `device-management`, four in `event-management`, one in `update-management` and one in `event-processing`. Each can be re-run after a failure. They create tables, indexes and a rollup and add columns that may be empty, and the configuration keys this release adds are optional and absent unless you set them, so `v1.8.x` pods keep working beside `v1.9.0` pods while the upgrade rolls. The upgrade is forward-only: there is no downgrade to `v1.8.x`. `dcctl upgrade` now refuses to skip a minor release. Coming from `v1.7.x` or earlier, upgrade through each intervening minor first, including `v1.8.x`, following each release's notes with that release's own `dcctl` and `--version` (see [Supported upgrade paths](./versioning-policy.md#upgrade-paths)). For `v1.8.0`, those are the [`v1.8.0` notes](#v180-upgrade).

```bash
dcctl install local --version v1.9.0
dcctl upgrade local devicechain --version v1.9.0 --dry-run
dcctl upgrade local devicechain --version v1.9.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds covered interfaces and removes none. It deprecates the `InstanceConfiguration` custom resource and the `spec.grafanaSSO` and `spec.compact` fields of an `Instance` declaration, which keep working until a later major release. The GraphQL library update refuses some number literals that were previously accepted; the new refusals and changes in behaviour are listed under [Before you upgrade to v1.9.0](#v190-before-upgrade) and [What is new in v1.9.0](#v190-whats-new). Helm values and instance configuration are outside the covered compatibility surface.

Highlights: an OTA rollout can advance through named canaries and cumulative percentages and halt on a failure rate, and the console runs it. `dcctl` and the console check the schema an instance serves before sending a request, and each release publishes a JSON Schema of its instance configuration. Agents can propose a device command over MCP that a person approves in the console (Preview, off by default), and a command sent by an automation records why it was sent. With fail-closed MQTT on, `event-sources` can now write event envelopes (opt-in, off by default). Facet values can be imported in batches, from a CSV file in the console too, relationships can be managed from `dcctl`, and long-range charts read a one-hour rollup. A read by a re-created device's token no longer returns the deleted device's events, inbound integrations keep accepting uplinks while `device-management` is briefly unavailable, and broker leadership is evened out at install.

What is new:

- [Before you upgrade to v1.9.0](#v190-before-upgrade)
- [What is new in v1.9.0](#v190-whats-new)

#### Before you upgrade to v1.9.0 {#v190-before-upgrade}

- `dcctl bootstrap` and `dcctl upgrade` now read InstanceOperations before acting. The identity running either command needs `list` on `instanceoperations.core.devicechain.io`; cluster-admin already has this permission. An absent resource definition is treated as no requests.
- **`dcctl upgrade` now refuses while another dcctl run holds the cluster lock, instead of warning and carrying on.** It takes the lock before its first write and checks the cluster's operator again while it holds it, so a `dcctl install` cannot move the operator underneath it. The lock is one per cluster, so two upgrades of DIFFERENT instances on the same cluster can no longer run at the same time: run them one after the other. An account without `create` and `delete` on `leases.coordination.k8s.io` in `dc-k8s-system` is now refused rather than warned. A run that was killed leaves the lock behind: take it with `dcctl instances reclaim` (it waits a lease duration and asks you to type the holder back). A run that is still alive keeps renewing and cannot be reclaimed: stop that process first. Two related changes: `dcctl instances release` refuses when the cluster's operator does not match this dcctl (pass `--force` to release anyway; a cluster with no operator at all is still let through, which is what the command is for, and it now needs `get` on `customresourcedefinitions`), and the dcctl writes that replace an instance declaration whole (bootstrap, upgrade and their annotation records) refuse a declaration that carries fields this dcctl does not know, rather than erasing them. Use the dcctl that matches the cluster.
- **Device attribute writes take a per-device lock.** Every write of a device's attributes now runs in a transaction and first takes a lock on that device: setting or deleting an attribute (including CLIENT scope, which used to be written without a transaction), restoring or copying a device configuration, importing facet values for devices, and deleting a device. Two writes to the same device are applied one after the other; writes to different devices do not wait for each other. The lock itself adds one database function (the writer protocol below adds a table). If an attribute row stays busy after five short retries, `setEntityAttribute`, `deleteEntityAttribute`, `restoreDeviceConfiguration` and `copyDeviceConfiguration` answer with the new retryable error code `ATTRIBUTE_WRITE_BUSY`, and nothing is written; retry the request. While the upgrade is rolling out, a write served by an upgraded pod can briefly yield to a write of the same attribute served by a pod that has not been upgraded yet. Three new metrics report this: `device_attribute_lock_wait_seconds`, `device_attribute_write_yields_total` and `device_attribute_write_deadlocks_total`.
- **Device attribute writes follow a writer protocol.** Every write of a device's attributes, and the tenant purge, now marks its transaction as following the device attribute writer protocol, and every set, unset or re-creation of a device attribute advances a per-key generation kept in a new table, `entity_attribute_generations`. Nothing enforces the protocol in this release; a later release relies on every pod of this one following it, so upgrade one minor release at a time. While the upgrade is rolling out, a device deleted by a pod that has not been upgraded yet can leave attribute rows behind; a cleanup pass on one replica removes them within ten minutes, and `device_attribute_orphans_removed_total{table}` counts what it removed.
- **Reserved fleet authority.** The new tenant authority `fleet:operate` is reserved for fleet operations in a later release; no API checks it yet. It is not added to any built-in role (tenant administrators holding `*` already satisfy it). Grant it to a custom role only **after the upgrade has finished rolling out**: a user-management pod that has not yet been upgraded refuses any role edit whose authority set contains a name it does not know, including an unrelated edit of a role that already holds it.
- **The GKE configuration in `deploy/gke` now creates its cluster through a shared module.** The variables, defaults and outputs are unchanged, and a state applied by an earlier checkout moves to the new addresses on its next `tofu apply` (`tofu plan` shows every resource as a move and plans nothing else). Once a state has been applied from this version, do not apply it from an older checkout: the older configuration no longer finds its resources at the old addresses, so it would plan to delete the cluster and create a new one. If you run load tests from several checkouts, update them all first. The README's "If an apply fails part-way" step now names the cluster as `module.gke_cluster.google_container_cluster.this`.
- **Deprecated: the `InstanceConfiguration` custom resource, and the `spec.grafanaSSO` and `spec.compact` fields of an `Instance` declaration.** Nothing changes for an instance in this release, and a later major release removes them. `InstanceConfiguration` (`dcic`) is unused, because each service reads the instance configuration from its mounted Secret, and the API server now warns when one is read or written. `spec.grafanaSSO` has no effect: Grafana's login is configured for the whole cluster. `spec.compact` is replaced by `spec.size: compact`, which dcctl already writes beside it: **keep both fields and do not remove `compact`**. dcctl still reads `compact` and refuses a declaration whose `size` is `compact` while `compact` is not set, so removing it by hand blocks every later bootstrap and upgrade. No action is needed.
- **Upgrade a second or third local instance only with this release's `dcctl` or a later one.** An instance that this release gave MQTT node port 31884 or 31885 keeps it on every later upgrade. An older `dcctl` knows only 31883, and its upgrade removes that instance's MQTT port, leaving the broker reachable inside the cluster only.
- **`dcctl upgrade` refuses to skip a minor release.** Skipping a minor has never been a supported path; `dcctl upgrade` now enforces it. It reads the version the instance's workloads actually run (from the running images, not the declared version, so an upgrade that stopped half-way is judged from what still runs) and refuses a target more than one minor ahead, telling you which minor to upgrade to first. Across a major it allows only the major's first minor. If the running version cannot be read, the upgrade is refused too. `--allow-skipped-minor` overrides the refusal; that path is unsupported and untested, and the flag never disables a protection the database enforces.

#### What is new in v1.9.0 {#v190-whats-new}

- **A device whose auto-registration keeps failing no longer slows its source.** When an integration, Sparkplug or LwM2M source auto-registers a device and device-management refuses the create (for example, because the source names a device type that does not exist), that device now waits out a short backoff before it is tried again. The backoff starts at 10 seconds and doubles while the failures continue, up to 5 minutes. Messages it sends inside the window are dropped and counted with the other unregistered-device drops, without another call to device-management. The backoff is per device, so other devices on the same source are not held back, and it ends as soon as the device registers. An LwM2M device in the window is told to register again later.
- **Broker leadership is spread at install, and you can see where it sits.** `dcctl bootstrap` now ends by evening out which message broker server leads which stream, the same best-effort step `dcctl upgrade` runs after it restarts the broker. It never fails the bootstrap. The step, `dcctl upgrade` and `dcctl broker rebalance` now also keep the resolved-events stream off the server that leads the broker's MQTT message store, and move the resolved-events consumers off a server that leads more of the per-event streams than one of their replicas does. The device-ingest streams and the MQTT streams are still never moved. The new `dcctl ha leaders` command reports, without moving anything, which server leads each per-event stream and each resolved-events consumer, and whether the rebalance could separate them further (`PASS`, `FAIL` or `UNSETTLED`, with `--json` and `--expect-fail`). An older `dcctl` does not know about the MQTT message store and can move resolved-events onto its server, so run the rebalance with this release's `dcctl`.
- **Import facet values in batches.** The new `importFacetValues` GraphQL mutation accepts up to 1,000 values on existing devices, assets, areas, and customers addressed by token. It validates each value against the facet's declared type, writes at SHARED scope, and reports every row, including failures. Preview with `dryRun: true`; rerunning unchanged values reports `EXISTS` without writing again. CSV device import remains available through the existing `importDevices` mutation.
- **A command sent by an automation records why it was sent.** When a rule's send-command action sends a command, the command now carries a `cause`: the rule and profile that fired, the type of detection, its exact time and triggering value and, when the same rule also targets an alarm on that detection, the alarm's key (it does not by itself prove the alarm was raised). `Command.cause` is new in the command-delivery GraphQL API, beside `CommandCause.deviceId`, which lets a reader match the alarm to the right device even if a device token was reused. Seeing a cause requires `device:read`, plus `alarm:read` when it names an alarm and `location:read` when the rule tests a geofence. Members limited to some customers do not see it. Only platform services can set a cause; a request that sets one with a person's token is refused. The MCP `list_commands` tool returns the cause too, and `list_alarms` now returns each alarm's `originatorId`. Commands sent by people, by batches or before this release have no cause, and some sent while the upgrade rolls out may have none. The new `react_command_cause_omitted_total` metric counts commands that were accepted only when resent without one.
- **The console no longer overwrites someone else's change to an area, asset, customer or group.** Editing the name, description or type of an area, asset or customer, or the name or description of an area, asset, customer or device group, now sends the version the form was opened on. If someone else saved a change in the meantime, the save is refused with a message that says so, and what you typed stays in the form. Previously the later save silently replaced the earlier one.
- **Bulk device create accepts a token template with stray spaces.** The console's bulk create form already trimmed the token template before sending it, but checked the untrimmed text first, so a template pasted with a leading or trailing space was refused with "Rendered token is not valid". It now checks the template exactly as it is sent.
- **Import facet values from a CSV file in the console.** On the Facets screen, **Import values** reads a CSV file of up to 10,000 records and 5 MiB, maps its columns (entity type, entity token, facet key, value), previews every row with a dry run, then sets the values in batches of at most 1,000. A row that repeats an earlier entity and facet key anywhere in the file is held back and reported. The results can be filtered and downloaded, rows that were refused can be retried on their own, and the failed-rows file keeps only the four import columns, so a credential column in your source file is never written back out.
- **The console can now run a staged OTA rollout.** A rollout created with cohorts shows its plan on the rollout page (each cohort's size, whether an operator promotes it, and whether it is complete, current or upcoming), and the devices table gains a Cohort column. When a cohort finishes cleanly the rollout pauses as "Cohort complete, ready to promote" and **Promote to next cohort** releases the next one; the button names the cohort on screen, so a stale tab or a second operator cannot promote twice. When a cohort finishes with failed or unknown devices, **Promote despite failures** asks for confirmation before it accepts them, and a rollout halted by its failure-rate limit offers a confirmed **Override failure-rate halt and resume** for the halt on screen. All three need `artifact:write`, like the other rollout controls. The counts on the page still cover the whole rollout, not one cohort. The new strings are in English and Spanish; Simplified Chinese shows English for the updates pages until that catalog is translated.
- **The console and `@devicechain/client` can refuse requests an instance cannot serve.** A console built for a newer release, pointed at an older instance, used to send requests the instance did not understand and fail with an opaque error. Negotiation asks each GraphQL endpoint for the schema it serves, using an authenticated `OPTIONS` request, and checks every request against it before sending. A request that uses a field or input the instance lacks is refused, nothing is sent, and the error says what is missing. Nothing is trimmed or rewritten. An instance older than v1.9.0 does not offer this discovery, so its release cannot be confirmed: the request is refused with an error naming v1.9.0 and is not sent on a guess. The console turns negotiation on only where it can be a different release from its instance: under `vite dev`, or in a build made with `VITE_SCHEMA_NEGOTIATION=on`. A console served by its own instance stays off, so a proxy that answers `OPTIONS` cannot break it. Sign-in, token refresh and live subscriptions are not checked. For your own application, `@devicechain/client` adds `enableSchemaNegotiation` (with an optional `release`), `disableSchemaNegotiation`, `resetSchemaNegotiation`, `SchemaSkewError` and `isSchemaSkewError`; it is off unless you enable it and needs the `graphql` peer package you already install. If a CORS proxy or web application firewall answers `OPTIONS` itself, discovery fails and negotiation refuses every request, so let `OPTIONS` reach the GraphQL endpoints. The `/dash` viewer app does not enable it yet.
- **The console is now fully available in Simplified Chinese.** Every console screen, including the over-the-air update pages, the update signers, the browse facets, device profiles, tenants, members and the admin screens, now appears in Simplified Chinese instead of falling back to English.
- **In-cluster executor (Preview).** `dcctl executor enroll/status/unenroll` install, inspect and remove an owner-pinned executor: its namespace, ServiceAccount, Deployment, RBAC, enrollment and an unbound requester role. It executes nothing in this release: requests are refused as `NotExecutableInThisBuild`, and no instances change. Enrollment requires an escrow artifact whose fingerprint matches the deployed root key for every instance (`OwnerOffline`). `dcctl install` installs two new resource definitions, `ExecutorEnrollment` and `InstanceOperation`, without deploying the executor. As with every release, an older dcctl refuses a cluster whose definitions a 1.9 `dcctl install` has moved.
- **More `dcctl` commands can print JSON for scripts.** `dcctl dead-letters list`, `dcctl tenant-deletion status` and `dcctl instances list` accept `-o json` (the same flag `dcctl events failed` has), and `dcctl ha verify` and `dcctl ha verify-db` accept `--json` (like `dcctl ha leaders`). With either flag, stdout carries only the JSON, and an empty `instances list` prints `[]` instead of a sentence. Exit statuses are unchanged: `tenant-deletion status` still exits non-zero unless the target is reached, and `--expect-fail` still inverts the `ha` checks, with its negative-control message on stderr in JSON mode.
- **Manage entity relationships from dcctl.** Use `dcctl relationship add`, `list` and `remove` to create, inspect and remove typed relationships through the tenant API. The commands support `--instance` or `--server`, two-factor sign-in, and table or JSON output (`--json` or `--output json`). List supports pagination and source, target, relationship type and tracked filters.
- **dcctl negotiates API capabilities.** Tenant and admin commands check the target schema before sending operations or input fields. Unsupported features report an upgrade remedy. Each session caches successful discovery per exact endpoint, and discovery refuses redirects. Newer instances advertise their actual schema to authenticated callers without enabling anonymous introspection. For older instances, dcctl uses the instance's recorded release from its cluster Instance resource, located through the local instance record or `--instance`. A recorded stable v1.7.x/v1.8.x release permits the conservative v1.7 contract on matching endpoints. An OPTIONS rejection never identifies a release: unknown versions still require discovery, with guidance to select a readable instance record or use v1.9 discovery. Core client callers register independently verified releases with `WithLegacyEndpointVersion`; preview features and features outside the legacy contract require v1.9 discovery. Commands and flags remain available.
- **`dcctl bootstrap --sharding`.** A new flag chooses an instance's sharding level when you install it. The level is recorded on the instance, and an existing level cannot be changed, added or removed later. `off`, the default, renders exactly what dcctl rendered before the flag existed and records nothing. `scale` is refused as not yet available. Before you rely on `--sharding`, run `dcctl install` so the cluster's Instance definition records the field. If you leave the flag out and set a raw `shardGroups` block in `--values`, it works exactly as before, and the bootstrap summary and `dcctl upgrade` report that configuration as `custom`.
- **Payload decoder health reads stay fast over long spans.** The decoder health tally (`payloadDecoderHealth`) is now summed in the database instead of loading every replica-minute row into device-management. A new migration adds two indexes on the decoder health table: one serves the per-profile read and the other the hourly trim of old rows. The answer is unchanged, including which last error is reported. The migration runs during the upgrade and briefly holds writes to that table while the indexes build.
- Concurrent device deletes return `false` when another delete wins, and event-source shutdowns count abandoned placement holds with `reason="shutdown"`.
- **The size of the deleted-entity table is reported, with an alert.** The event store keeps one row for every distinct entity token that was ever deleted, so a later entity created under the same token cannot read the deleted one's event history. Nothing prunes that table. It now appears on the `append_only_table_bytes` and `append_only_table_rows_estimate` series (`table="entity_deletion_watermarks"`, exported by event-management), and the new `EntityDeletionWatermarksLarge` warning fires when it stays over 1 GiB (about five million distinct deleted tokens) for an hour. Its entry in the alert runbook gives the query that names the tenant behind it.
- **Device configuration report history.** The service now keeps a 200-day history of accepted device configuration verdicts internally; this history is not yet served through the API. The new `device_configuration_reports_pruned_total{reason="orphan|retention"}` metric counts rows removed by maintenance.
- **Concurrent device deletion.** Fixed database deadlocks when deleting a gateway while creating a child and updating attributes, and when deleting an entity while restoring or copying its configuration. Gateways with children continue to refuse deletion.
- Deleted-device holder repair now backs off confirmed absences from one hour to seven days, reducing repeated device-management queries. Roster facts still detect re-created devices promptly. If a roster fact is missed, a successful sweep detects the re-creation on its next due pass, within seven days plus one sweep interval. No deletion is permanently skipped.
- **Agent kills stay fail-closed when delivery retries run out.** If device-management cannot apply a kill on its final delivery, it writes an `unprocessable` dead letter and blocks agent alarm executions for every tenant in that process until restart. After storage recovers, repeat the kill and then restart the affected device-management processes; successful kills and healthy broker polls do not clear this latch.
- **The size of the device token holder table is reported.** The event store keeps one row for every device token a live device was ever known to hold. It is how a read by a token that was deleted and then created again returns only the new device's events. Nothing prunes that table. It now appears on the `append_only_table_bytes` and `append_only_table_rows_estimate` series (`table="device_token_holders"`, exported by event-management). It has no size alert of its own: it grows with the fleet, so a large table is normally a large fleet. Devices deleted and created again and again under new tokens also grow the deleted-entity table, which `EntityDeletionWatermarksLarge` watches, and the event store's volume alerts cover the space.
- **A re-created device no longer shows the deleted device's events.** When a device is deleted and a new device is created with the same token, reading events by that token (events, measurements, locations, alerts, presence changes, bucketed measurements and the calculated-metric preview, including through MCP) returns only what the platform received for the new device, from the moment of the deletion on, buffered readings dated earlier included. A deleted token that has not been created again still reads its device's full history, as before. Bucketed measurements for a re-created token are computed from the stored readings rather than the pre-aggregated rollups. This applies to deletions processed by v1.8.0 or later.
- **Protect concurrent edits.** The device-management GraphQL mutations `updateArea`, `updateAsset`, `updateCustomer` and `updateEntityGroup` now accept an optional `expectedUpdatedAt` argument. Send the entity's last-read `updatedAt` to refuse an outdated save with `STALE_WRITE`; reload before retrying. Omitting the argument preserves last-write-wins behavior.
- **Fleet operation readiness (Preview).** The `fleetOperationReadiness` query and `dcctl fleet-op readiness` report whether this instance can run fleet operations. In this release they report that enforcement is not installed and no operation kind is executable, which is expected. Requires `fleet:operate`; scoped members are refused. The preview clock starts at v1.9.
- **GraphQL requests are parsed more strictly.** The GraphQL library is updated, and number literals that were previously accepted are now refused as syntax errors: a leading zero (`01`), a trailing or leading decimal point (`1.`, `.5`), an integer out of range, and a minus sign before something that is not a number. A document is also limited to 100,000 field-overlap comparisons during validation. Well-formed requests are unaffected.
- **HA verification rejects incomplete consumer listings.** `dcctl ha verify` now refuses to judge a snapshot when a stream's listed consumer count differs from the broker's reported count. The error identifies the stream and both counts; rerun the command to collect a complete snapshot.
- **The waterfront 3D scene has a coastline behind the town, and every model is more detailed.** In `demos/harborpulse-unity`, haze now fades the far forest, so a sparser set of fuller trees (maples, birches, oaks, spruce and pine) stands on hills with clearings, granite outcrops and a summit against the sky. The town has fewer, more detailed buildings with porches, trim, chimneys and varied roofs. Lamps, chargers, gauges, pump stations, the truck, boats, buoys, beacons and piers have more detailed models, and the quays show concrete seams, stone coping, pavers and line markings. A lamp no longer overlaps a charger or a car, and a test pins that no prop overlaps another. The night alarm count is easier to read.
- **The waterfront 3D scene gains depth behind the town and an hour you can read at a glance.** In `demos/harborpulse-unity`, wooded foothills and a middle range now stand between the last trees and the far ridge, and the haze darkens below the horizon, so the land steps back in layers instead of ending in a pale band. A low sun lights the faces turned to it and the cloud from below, and the moon silvers the night's cloud, so dawn, day, dusk and night look different even from the overview, which faces north. The 9:16 overview has a new framing with the sky and the sunrise in it, the open coast has a rocky foreshore and skerries, granite outcrops read as rock, a lit street light has a soft round glow, and the service truck is a complete model close up. A browser build now takes its preview options from the page address (`?hp-offline&hp-start-hour=17.3`) and draws the water correctly.
- **The waterfront 3D scene shows why the platform sent a command.** In `demos/harborpulse-unity`, a live day now says "The platform tells it to derate" when the overheating charger's derate command carries the cause the platform recorded with it and that cause names the alarm on screen; the scene joins the two by alarm key, device record and detection time, and when the link is missing, or the reader is not allowed to see it, it keeps to "Alarm raised". The storm pump's rail shows `start-pump sent` the same way. `hack/harborpulse-rig.sh` checks that the token each simulator serves can read a command's cause, and a player started with `-hp-control-no-cause` shows the fallback on purpose. The scene also gains a granite tor tall enough to be a landmark from the overview, rain squalls across the distance in the 9:16 overview, a calmer storm sky, a shorter ferry terminal that no longer hides a charger, wet-well views with nothing parked in front and their level tags clear of the caption, and a morning that is no longer blue.
- **The waterfront 3D scene gets a director.** `demos/harborpulse-unity` now draws Port Alder as a compact horseshoe harbour that one camera can hold, with every device at a drawn position that keeps its quay and its order along the waterfront. A director follows the simulated day: it moves the camera to each story beat, writes a short caption, and builds a cause-and-effect chain on screen, and each step appears only when the platform's own record for it has been read. A step it cannot confirm is never shown as done; the scene says less instead. Free roam takes the camera at any time and hands it back after 20 seconds idle. A run with a planted defect shows a red TEST CONTROL banner, and a scene built for a different layout turns the director off and says so. Each live run is recorded to files that can be checked afterwards. The scene still only reads.
- **The waterfront 3D scene is lit like a cloudy harbour day, and its nights can be read.** In `demos/harborpulse-unity`, daylight is soft and overcast, with gentle shadows and haze on the hills. The storm darkens the sky but leaves the town legible, and at night the ambient stays at a moonlit level. Windows, street lamps, the quays' lights and alarm beacons now glow in the built player as well as in the editor; before, they could look flat there. The town has its own street lamps, and its eastern windows and lamps go dark through the east feeder outage. Captions stay readable on every page. A new `-hp-frame-stats` flag logs frame times.
- **The waterfront 3D scene shows more sky, a fuller forest and weather you can watch arrive.** In `demos/harborpulse-unity`, the hills behind the town are lower and the story cameras stand lower, so the sky is in every shot. The sky follows the scenario clock and the storm: a rose dawn, fair-weather cloud at noon, cloud building for three hours before the storm, a dark overcast with rain, and a sky that clears afterwards to a moonlit night. Dawn, dusk and the storm are lit so the town stays readable. The forest is denser, about three fifths green conifers with autumn colour in the rest, with meadows and granite ledges set into the slope, and it fades into haze before a distant ridge. Buildings, cars and boats near the camera gain doors, window frames, porches, wheels and fittings, while distant ones stay simple.
- **The waterfront 3D scene's storm now has a sunset, and its dawn is no longer dark.** In `demos/harborpulse-unity`, the storm that covers every dusk of the scenario stops short of the western horizon: a band of lit sky shows under the cloud for the last hour of the day, the low sun reaches the town through it, and the rain in front of it is lit from behind. Dawn is brighter and the quays are no longer blue in the morning. The hills behind the town carry a tree line with autumn colour in drifts, the granite ledges carry loose blocks and a few tors, the 9:16 overview holds the harbour in the middle of the frame, and the street-light crew arrives with a bucket lift. Distant rain hangs in shafts instead of one grey field. A still can be rendered as a fair-weather look check (`-hp-stills-weather fair`), which is stamped as such and changes nothing the scenario reports.
- **The waterfront 3D scene reads properly in a 9:16 frame, and its ground no longer shows its grid.** In `demos/harborpulse-unity`, a wet well's cut-away view has a version of its own for a tall frame, so the level tags stand between the caption and the event rail instead of under them. The 9:16 overview gains a granite tor of its own in the wood west of the Basin, and both tors now show narrow bedding joints in place of dark and light stripes. The ground's colours are blended across the terrain instead of changing square by square. Rain squalls are drawn only for a high viewpoint, where they read as rain; from the 16:9 overview and the quays the far ranges fade evenly again. The hot-connector page's wide view no longer looks over the berthed ferry's roof.
- **The waterfront 3D scene gets a real town.** In `demos/harborpulse-unity`, the uniform grid of identical houses behind the harbour is replaced by a smaller, rustic town: a main street that climbs inland with a few side streets, a mix of clapboard houses, brick shop blocks, waterfront sheds, an inn and a white church, with trees between them and forested hills in autumn colour behind. The waterfront gains a park lawn with a gazebo, timber piers on pilings and moored boats. The harbour water now shows a soft reflection that fades with distance instead of a hard mirror, with a turquoise tint in the shallows along the quays. The quays and every device stay exactly where they were.
- **Faster long-range charts for new data.** Measurement reads bucketed by a whole number of hours (1h, 2h, 6h, 24h and so on) are now served from a second, one-hour rollup built over the existing one-minute rollup, so a long chart reads a few hundred pre-aggregated rows per metric instead of tens of thousands. The upgrade creates the new rollup without filling it, so the speed-up applies to data written after the upgrade; readings from before it are read from the one-minute level exactly as before, and the results are the same either way. Hours a range cuts through, including the hour still filling, also come from the one-minute level. The rollup is refreshed in the background every minute and kept for the same retention window as the one-minute rollup. The existing `DisableRollupReads` switch turns both rollup levels off.
- **Instance configuration schema for each release.** Download `instance-config.schema.json` from the exact target GitHub Release, or run that release's `dcctl config-schema` to obtain the same schema offline. Generated from the Go config types, it validates the instance document's structure and rejects unknown keys. Startup still checks semantic and cross-field constraints.
- **Inbound integrations keep accepting uplinks while device-management is briefly unavailable.** event-sources remembers which device an integration's identifier resolved to for five minutes. Before this release, once that answer was older than five minutes, an uplink that arrived while device-management could not be reached was answered `503`. Now the last answer is used for up to 15 minutes after device-management gave it, the same limit the integration's token and payload-decoder lookups already use, and the uplink is accepted and processed when device-management is back. Device-management still checks the integration, the device and its identifier for every event, so a device disabled, deleted or moved to another identifier in the meantime is refused then. An answer that device-management actually gives, such as an identifier that is not registered or an integration that is disabled, is never replaced by an older one. Downlink reports (acknowledgements from a network server) are not covered: they are still answered `503` while device-management cannot be asked, as before. A request served this way is counted under the new `resolve_stale` result of `integration_requests_total`, in addition to its usual result, and event-sources logs a warning.
- **A second and a third local instance get their own MQTT port.** On a local kind cluster, `dcctl bootstrap` now gives each new instance the first free MQTT node port the cluster publishes. Previously only the first instance could be reached from the host and the others were in-cluster only. A cluster created by this release publishes three: hosts 1883, 1884 and 1885. A cluster created earlier publishes 1883 alone and behaves as before. The bootstrap summary prints the address (`MQTT: ssl://localhost:<port>`). The port is recorded with the instance, so `dcctl sim create` uses it. An upgrade keeps the port an instance already has and never gives it a new one. Creating a new local cluster now needs host ports 1884 and 1885 free as well as 1883; `dcctl preflight` checks them.
- **A dead MQTT broker no longer wedges a busy client.** The external MQTT event source, the Sparkplug host and the edge agent's cloud uplink now bound each socket write to the broker at 5 seconds and state their 30 second keep-alive explicitly. Before, a publisher that had filled its socket against a peer that stopped answering could block on that write indefinitely and never notice the dead connection; it now drops the connection and reconnects.
- **Staged OTA rollouts.** Optionally declare named canaries followed by cumulative fleet percentages. SKIPPED and CANCELLED targets do not block the next cohort. Failed or UNKNOWN outcomes pause progression as `COHORT_FAILED`; `promoteUpdateRollout` requires `overrideFailure: true` to accept them and refuses pending or in-flight cohort targets. UNKNOWN retains its concurrency slot. If UNKNOWN fills the cap before all canaries dispatch, the rollout pauses visibly; resolve or abandon it, then resume to finish the pending canaries. Manual promotion is available. An optional failure-rate policy uses a trailing window and minimum sample; its threshold is 1–99, with a strict greater-than comparison. Numbered halt evidence survives overrides; a version-only change to an acknowledged attempt does not halt again.
- **Enable after upgrading.** The fixed instance setting `infrastructure.updateManagement.rolloutSafety` defaults to false. Creating or starting a rollout with cohorts or `failureRate` is refused with `ROLLOUT_SAFETY_DISABLED` while it is off. Keep it off throughout the version upgrade. After **all update-management replicas have upgraded**, set it to true in a **separate configuration roll**. Old replicas bypass cohort and rate gates, allow ordinary resume of these pauses, and cannot serve the new pause reasons. Existing ordinary rollouts keep their defaults and concurrency limits.
- **A held delivery gives up after 15 minutes.** While a tenant's shard placement is unavailable, event-sources holds that tenant's last-chance deliveries. A tenant whose placement never returns no longer keeps holding them: after 15 minutes the delivery is left unacknowledged, as it would be without the hold. New counter `total_placement_hold_fallback{source,reason}` counts deliveries that were not held because the hold was full (`full`) or that stopped being held (`expired`). A recovered held message is also no longer counted as inbound twice.
- **Replicated streams stay replicated across a broker restart.** On a clustered broker, a service whose connection was briefly between servers during a rolling restart treated the broker as a single server. It logged that every stream and bucket was being created with one replica, and a stream or bucket created at that moment could be created with one replica and stay that way. A service now keeps the topology it last saw while it reconnects, and before it has seen any it uses the configured replica count. A stream or bucket that is already below the configured count is reported by the `JetStreamNotReplicatedAsConfigured` alert, and `dcctl broker reconcile` raises it.
- **Resolved events wait for shard placement.** With shard groups on resolved-events, an inbound event whose final delivery attempt had some of its resolved events refused because shard placement was unavailable is now held and finished once placement returns, instead of being recorded as a dead letter with its events never resolved. Only the refused resolved events are published again; those already stored are not repeated. This applies to inbound events, integration uplinks and resolved envelopes alike. The new `resolve_placement_held_sources` gauge shows how many final deliveries are being held, and `resolve_placement_hold_fallback_total` counts, by reason, those the hold did not take or stopped holding. The hold is bounded in size and holds a message for at most 15 minutes, then leaves it unacknowledged as before; it lasts for the current run, and shutdown abandons held messages unacknowledged, so exhausted deliveries can still be recorded. Rolling upgrades need no wire or configuration change; old pods retain the previous behavior.
- **Rule-health statistics no longer accumulate for superseded profile versions.** Each publish of a device profile starts fresh firing statistics for that version's rules. The statistics of the version it replaced were kept indefinitely, although the rule-health view shows them only if a rollback makes that version active again. Event processing now removes them once a week has passed since that version's rules last fired. The statistics of a profile's active version are always kept, as are those of a profile that has no active version. If a rollback later makes a version active again after its statistics were removed, rule health says so: the new `historyRemovedAt` field on `RuleHealth` gives the time of the removal, the console shows a notice, and fire counts and last-fired times then cover only the fires since that time. One replica runs the cleanup each hour, in bounded batches per tenant. The new metric `devicechain_eventprocessing_rule_stat_sweep_deleted_total` counts the rows it removes.
- **Sharded event-processing startup.** Starting a sharded instance no longer attempts to delete an unsharded legacy consumer on a nonzero group stream, eliminating the resulting broker permission warning. Group zero still cleans up its legacy consumer during an in-place upgrade. Broker permissions are unchanged.
- **Tenant deletion completes with sharded resolved-events.** On an instance with resolved-events in shard groups, deleting a tenant no longer stays blocked on "no DETECT engine answered" with the tenant's token held. event-processing removed the tenant from every group, but the broker refused its answers, because it sent one answer per group over one connection and the broker accepts one per request. It now sends a single answer that covers every group. During a rolling upgrade, a deletion can stay blocked while old and new user-management or event-processing pods overlap, and it resumes on the coordinator's next pass once both are upgraded, then completes after the usual settle and token-hold waits. Instances without sharded resolved-events are unaffected.
- **`dcctl sim create` no longer points a sim at another instance's broker.** On a local cluster where every published MQTT port belongs to other instances, an instance is reachable inside the cluster only. `dcctl sim create` for such an instance used to fall back to the cluster's first MQTT port, which is another instance's broker. It now refuses and tells you to pass `--mqtt-broker`. The instance record now says explicitly when an instance has no MQTT port. For an instance recorded before this release, dcctl reads the instance's own Services instead, so an instance that does hold the first port still resolves to it.
- **The Site Pulse demo's dozers have their own device profile.** A dozer carries no load and has no tyres, yet it shared the equipment profile with the loaders and haul trucks, so the fleet table declared payload and tyre pressure for it and showed "no data in range". The dozer type now uses a profile of its own that declares fuel, engine temperature and engine hours only, with the same commands and the low-fuel and overheat rules but no tyre rule, so those cells read "does not report this". A demo instance created by an earlier release keeps the shared profile until it is recreated.
- **Rejecting a single sign-on link request is now audited.** Rejecting a pending request with `rejectSsoLinkRequest` records an `identity_link_rejected` row in the authentication audit log, the way approving one records `identity_linked`. The row names the administrator who rejected it and the request and connection; it never records the email or subject the provider asserted. Rejecting a request that is already gone, or being refused, records nothing.
- **One Simplified Chinese term per concept.** The Simplified Chinese console and documentation now use the same words for the same things: provisioning profile, artifact, facet, rollout, identity provider, secret and signer no longer vary between screens or between the console and the docs.
- **Fail-closed MQTT can write event envelopes (opt-in, off by default).** With `infrastructure.nats.mqttFailClosed` on, `event-sources` has until now written each MQTT message to the inbound-events stream on its own, even when [event envelopes](#event-envelopes) are on. The new `event-sources` setting `capture.mqttStoreEnvelopes` lets it write them as envelopes instead, which cuts the broker's work per event. It takes effect only when `infrastructure.nats.mqttFailClosed` and `infrastructure.messaging.envelopes.inbound` are both on, and changes nothing otherwise. A message is acknowledged in the MQTT message store only once its envelope is on the inbound-events stream, and a redelivered message is recognised and not written twice. Turn it on in a separate `dcctl upgrade`, after every `event-sources` pod runs this release, under `functionalAreas.event-sources.config.capture.mqttStoreEnvelopes` in a values file (see [Tuning an instance](./tuning-an-instance.md)). When the setting changes, `event-sources` waits for the messages it had in flight under the old setting to settle before it writes the new way.
- **Harborpulse simulator:** Adds MQTT and certificate-authenticated chargers, a connectivity alarm, desired/reported configuration, and HTTP command-pull lamps, with platform-readback invariants and isolated negative controls. The original fleet and its rollout and outage beats remain available.
- **Rollout progress by cohort.** The update-management GraphQL API now reports outcome counts for each rollout cohort and supports filtering targets by cohort. Counts use the same outcome rules as overall rollout progress.
- **Harborpulse tenant isolation and MCP questions.** The validation rig now checks two tenants with matching device identifiers and compares fleet answers from the real MCP service with GraphQL truth. It registers a public OAuth client per run and uses a tenant member's PKCE user token. Two planted defects prove each new check can fail independently.
- **Agent command proposals (Preview).** Commands expose additive `causeDetails` and `proposal` fields. Legacy `cause` keeps its detection meaning and is null for a command created by an agent proposal.

- MCP adds the static `propose_command` tool under `act:commands`, only while both MCP actuation and `infrastructure.mcp.commandProposals` are on. `get_proposal` and `list_my_proposals` accept `area: "command-delivery"`; omission keeps alarm behavior. MCP sessions refresh their tool list after the key is enabled. The console Approvals inbox adds a separately paged Commands tab with full JSON arguments, incarnation, impact, definition fingerprint, cutoff, approval countdown and plain untrusted rationale. Approval sends only the proposal token and displayed digest, queues ordinary delivery, and may still wait for maintenance, expire or fail; it never recalls a command already sent.
- Command-delivery reads device facts under the caller's own token: proposing agents and approving people both need `device:read`, or the call is refused with `FORBIDDEN`. Proposal limits are 200 per hour per client and independently per person, with 20 open per client; policy may tighten them. An agent follows its executed proposal through `get_proposal(area: "command-delivery").resultRef`; `list_commands` does not show its originating proposal.

- Command-delivery serves `proposeCommand`, `approveAgentProposal`, `rejectAgentProposal`, `agentProposal` and `agentProposals` (Preview). The `infrastructure.mcp.commandProposals` switch defaults off; proposals and approvals are refused while off. Enable it only in a separate upgrade after the release rollout completes: pods from an earlier release refuse the key. The broker's auth callout grants command-delivery's new reader permission on the agent kill stream, so complete the rollout, including the callout, before enabling it.
- Each proposal addresses one device. People approve it in the console against a snapshot of the device's published command definition, re-checked at approval. This fresh check is not a cross-service lock: a later remote definition change can still race the commit. The command delivery cutoff is fixed when proposed: one hour by default, at most 24 hours.
- The platform defaults are 50 executions per hour per agent client and independently per person, 100 open proposals and 1000 proposals per hour per tenant, and 200 executions per hour per tenant; dry runs have their own budget of 60 per minute per agent client and per person. Decided proposals and their request keys are kept for 30 days, so an agent's `requestId` is remembered for that period. A tenant kill ends pending proposals and cancels agent commands still waiting (queued, held or parked); it cannot recall one already sent.

### v1.8.0 — maintenance windows hold alarm pages, and deliveries ride out outages {#v180-upgrade}

`v1.8.0` is an in-place upgrade from `v1.7.x`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described in [Rolling upgrades](#rolling-upgrades). Use **the v1.8.0 `dcctl` for both commands** with OpenTofu or Terraform 1.10 or later, and give `dcctl install` the cluster's settings, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. Six migrations are appended and run at each service's first start: two in `device-management`, two in `notification-management`, one in `event-management` and one in `event-processing`. Each can be re-run after a failure. They create tables and add columns that may be empty, and no configuration key is added, so `v1.7.x` pods keep working beside `v1.8.0` pods while the upgrade rolls. Alarm suppression applies in full only once `device-management` and `notification-management` have both finished rolling out, as described under [Before you upgrade to v1.8.0](#v180-before-upgrade). The upgrade is forward-only: there is no downgrade to `v1.7.x`. Coming from `v1.7.0`, `update-management` also appends the `v1.7.1` migration, and the [`v1.7.1` notes](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.1) apply too. Coming from `v1.6.x` or earlier, upgrade through each intervening minor first, including `v1.7.x`, following each release's notes with that release's own `dcctl` and `--version`: skipping a minor is not a tested path (see [Supported upgrade paths](./versioning-policy.md#upgrade-paths)). For `v1.7.0`, those are the [`v1.7.0` notes](#v170-upgrade).

```bash
dcctl install local --version v1.8.0
dcctl upgrade local devicechain --version v1.8.0 --dry-run
dcctl upgrade local devicechain --version v1.8.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds covered interfaces and removes or deprecates none. Withholding the notification history of geofence-derived alarms from members without `location:read` is a security fix permitted by the policy; the other new refusals and changes in behaviour are listed under [Before you upgrade to v1.8.0](#v180-before-upgrade). Helm values and instance configuration are outside the covered compatibility surface.

Highlights: alarm maintenance windows, declared over devices, device types, groups, areas, assets, customers or a whole tenant, mark the alarms raised under them as suppressed and hold their pages until the window ends; the console starts and manages them, and MCP shows them. An alarm state change's notification is recorded together with the change and sent from that record once the broker accepts it, through a broker outage or a `device-management` restart, and device deletes and authority changes answer sooner while the broker is unreachable. With NATS shard groups, captured messages wait for placement to return, live subscriptions follow placement moves, and `resolved-events` can be sharded on a new installation. Events stored after an entity's deletion no longer reattach to a re-created entity with the same token. The `harborpulse` simulator scenario now exercises outbound deliveries, LoRaWAN, fleet campaigns, signed updates and a maintenance window, and its verifier judges each.

What is new:

- [Before you upgrade to v1.8.0](#v180-before-upgrade)
- [What is new in v1.8.0](#v180-whats-new)

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

## Older releases {#older-releases}

When a new minor release line is added, the oldest line documented in full moves here as a short entry. Each entry links to the full notes in the documentation for that version, as they stood at its release, and to its GitHub release. Every release is listed on [GitHub Releases](https://github.com/devicechain-io/devicechain/releases).

### v1.7.0 — deletions reach every service, and sessions ride out outages {#v170-upgrade}

<a id="v171-upgrade"></a><a id="v171-before-upgrade"></a><a id="v171-whats-new"></a><a id="v171-rollout-identity"></a><a id="v170-before-upgrade"></a><a id="v170-whats-new"></a><a id="v170-deletion-announcements"></a><a id="v170-deleted-anchor-shares"></a><a id="v170-deleted-device-alarms"></a><a id="v170-deleted-device-commands"></a><a id="v170-deleted-device-state"></a><a id="v170-deleted-device-updates"></a><a id="v170-deleted-entity-anchors"></a><a id="v170-shard-groups"></a>

Full notes: [v1.7 documentation](/1.7/deployment/releases-and-upgrades#v170-upgrade). GitHub release: [v1.7.1](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.1), [v1.7.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.0).

### v1.6.0 — tighter authority checks and devices connected through broker reloads {#v160-upgrade}

<a id="v160-before-upgrade"></a><a id="v160-values-commands"></a><a id="v160-broker-reload"></a><a id="v160-detection-stream-location"></a><a id="v160-solar-campaign-position"></a><a id="v160-action-authorities"></a><a id="v160-rule-ceiling"></a><a id="v160-review-deleters"></a><a id="v160-no-escalation"></a><a id="v160-device-relationship-refused"></a><a id="v160-new-relationship-event"></a><a id="v160-device-uplink-guessing"></a><a id="v160-http-credentials"></a><a id="v160-credential-resupply"></a><a id="v160-signing-key-follow"></a><a id="v160-nginx-snippets-off"></a><a id="v160-ops-port"></a><a id="v160-destroy-instance-name"></a><a id="v160-mcp-memory"></a><a id="v160-mcp-discovery"></a><a id="v160-api-checks"></a><a id="v160-command-holds"></a><a id="v160-campaign-planning"></a><a id="v160-tenant-fairness"></a><a id="v160-escalation-clock"></a><a id="v160-sparkplug-limits"></a><a id="v160-entity-attributes"></a><a id="v160-nats-shard-groups"></a><a id="v160-backup"></a><a id="v160-console-dashboards"></a><a id="v160-console-tab-session"></a><a id="v160-authoring-tools"></a><a id="v160-npm-deprecations"></a><a id="v160-decoder-redelivery-budget"></a><a id="v160-invalid-failures-name-the-device"></a><a id="v160-agent-metrics"></a><a id="v160-other-changes"></a><a id="v160-going-back"></a>

Full notes: [v1.6 documentation](/1.6/deployment/releases-and-upgrades#v160-upgrade). GitHub release: [v1.6.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.6.0).

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
