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

The three most recent minor releases (`v1.5`, `v1.4` and `v1.3`) are documented in full, newest first. The detailed `v0.27.0` through `v0.24.0` notes are also retained below.

### v1.5.0 — reviewed automation changes and sign-in-bound agent approvals {#v150-upgrade}

`v1.5.0` is an in-place upgrade from `v1.4.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described in [Rolling upgrades](#rolling-upgrades). Use **the new release's `dcctl` for both commands**, and give `dcctl install` the cluster's settings, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. Four appended migrations run on upgrade: three in `device-management` and one in `user-management`. They add the agent kill fence and effect outbox, automation reviews and review enforcement, and the actuation generation column. See [Schema changes and going back](#v150-going-back). Coming from `v1.3.0` or earlier, read the [`v1.4.0` notes](#v140-upgrade) first.

```bash
dcctl install local --version v1.5.0
dcctl upgrade local devicechain --version v1.5.0 --dry-run
dcctl upgrade local devicechain --version v1.5.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds covered interfaces and removes none. Automation change review, agent proposal tools, approval access and in-session step-up are in the [preview tier](./versioning-policy.md#preview-interfaces), where they may still change in a minor release. Helm values are not covered by the compatibility policy; the new opt-ins are off by default.

What is new:

- **Automation changes can require an approved review (Preview).** Compare a draft against recorded history before publishing. See [Automation change review (preview)](#v150-automation-review).
- **Agent approvals are bound to a sign-in (Preview),** with MCP proposal tools, in-session step-up and a local kill fence. See [Agent actions (preview)](#v150-agent-actions).
- **`dcctl` is forward-only** and remembers MQTT uplink and relational WAL choices. See [dcctl is forward-only](#v150-dcctl).
- **Configuration fails closed and API inputs are bounded.** See [Configuration fails closed](#v150-configuration), [GraphQL input limits](#v150-input-limits) and [Stricter checks on existing operations](#v150-tightenings).
- **LoRaWAN queue acceptance no longer claims a device answer.** See [LoRaWAN and commands](#v150-lorawan).
- **Tracing follows an event across services,** off by default. See [Observability](#v150-observability).
- **Partial updates preserve concurrent edits.** See [Concurrent edits preserved](#v150-concurrent-edits) and [Other changes](#v150-other-changes).

#### Before you upgrade {#v150-before-you-upgrade}

What needs action, in the order to do it:

- **Upgrade note:** Plan to roll forward or restore from backup. `dcctl upgrade` and a bootstrap re-run refuse an earlier release than the instance's declared one; `dcctl install` refuses a `dcctl` older than the cluster's last installer. There is no override. Use the target release's tool, install with the cluster's settings, then upgrade each instance.
- **Upgrade note:** Correct values that earlier releases accepted before restarting. The chart schema and `dcctl` overlay validation refuse out-of-range signing-key days, negative ingest ceilings, negative command-delivery settings, overflowing seconds settings and malformed LwM2M tokens at render. Services refuse them at startup too. Check `functionalAreas.user-management.config.auth.signingKeyMaxAgeDays` and `signingKeyRetentionDays`: the allowed range is 0–106751. `-1` no longer disables rotation; use `0` for max age (retention `0` takes the 8-day default). In `event-sources`, `lwm2m-ingest` and `sparkplug-ingest`, remove negative `ingestRateLimit.readingsPerSecond` or `ingestRateLimit.burst`. In `command-delivery`, remove negative `defaultCommandTtlSeconds`, `heldCommandCeiling`, `sweepIntervalSeconds`, `maxDispatchFailures` or `deliveryMachineryReserve`. Zero or absent keeps their defaults.
- **Upgrade note:** Check seconds settings against the maximum 9223372036: `updates.sweepIntervalSeconds`, `updates.deadlines.*`, `deliverySeconds`, `stateRetentionSeconds`, `attemptRetentionSeconds`, `retentionSweepSeconds` and `escalationSweepSeconds`. Correct LwM2M `deviceTypeToken` values to letters, digits, hyphens and underscores, starting with a letter or digit, even when `autoRegister` is off; pass the corrected identities file with `--lwm2m-identities`, which validates it too. These checks also apply at render. `dcctl` and chart defaults never render most of these settings; they affect operator-supplied values, not a default install.
- **Upgrade note:** Check hand-written configuration for startup-only refusals: negative `maxOpenConnections` or `maxIdleConnections`, endpoint ports above 65535, negative `terminationGracePeriodSeconds`, trailing content after the configuration document (even `]` or `}`), and an `aheadHoldBudgetMillis` large enough to overflow. Correct them before restarting. `dcctl` and chart installs do not normally produce these invalid documents.
- **Upgrade note:** Split or disable rules before publishing a profile with more than 1,000 enabled detection rules. Such a publish now fails with `LIMIT_EXCEEDED`; `validateDetectionRules` also refuses more than 1,000 rules before compiling any. Review API callers against the [input limits](#v150-input-limits) and [write checks](#v150-tightenings).
- **Upgrade note:** Roll forward fully before enabling agent actions (Preview). Approvals recorded before this release no longer count: approve again. People signed in before the upgrade must refresh their token or sign in again before their approval counts. A mixed rollout fails closed: a new device-management calling old user-management refuses with `POLICY_UNAVAILABLE`, and new user-management counts no unbound approvals from an old device-management. If a tenant disabled through an upgraded pod is re-enabled during the roll, re-enable it through an upgraded pod; an old pod does not advance the generation and agent actions stay refused until this is done.
- **Upgrade note:** Protect profiles for automation change review only after the rollout. Protected publishes fail with `REVIEW_UNAVAILABLE` until user-management is on `v1.5.0`; running a review also needs event-processing on `v1.5.0`. `dcctl` does not order these areas for you. No profile becomes protected on upgrade.
- **Upgrade note:** Turn on new opt-ins only after the rollout, each in its own upgrade: `infrastructure.tracing`, the MCP area's `agentActuation.enabled`, and `--mqtt-device-uplinks` if uplinks are not already on. A restarting `v1.4.0` pod refuses an instance or area configuration key it does not know. Disabled tracing is omitted from the document, so the default is safe. MQTT uplinks themselves were available in `v1.4.0`; the new flag records the choice.
- **Upgrade note:** If a hand-written instance id falls outside the chart grammar (lowercase letters, digits and interior hyphens, at most 50 characters), plan to recreate it under a conforming id: derived broker or database names can collide. Startup still succeeds with a warning and a per-area gauge, for example `devicechain_devicemanagement_instance_id_noncanonical`, set to 1. `dcctl` and chart installs already conform.

New Helm values and `dcctl` settings, each off or unchanged by default:

| Value | Default | What it does |
| --- | --- | --- |
| `instance.config.infrastructure.tracing.enabled` | `false` | Enables OpenTelemetry tracing; the disabled block is omitted from the instance document. |
| `instance.config.infrastructure.tracing.protocol` / `endpoint` / `insecure` | `grpc` / `""` / `false` | OTLP export over `grpc` or `http`, collector endpoint and transport security. |
| `instance.config.infrastructure.tracing.samplingRatio` | unset; `0.1` when enabled | Parent-based sampling; explicit `0` is allowed. |
| `agentActuation.enabled` under `functionalAreas.mcp.config` | absent (`false`) | Registers the MCP proposal tools; the user-management instance switch and tenant policy still apply. |
| `--mqtt-device-uplinks` / `spec.mqttDeviceUplinks` | unset; off without an existing choice | Records the MQTT uplink choice; `=false` disables it, omission keeps the recorded choice. |
| `dcctl install --relational-store-max-wal-mib` | omitted keeps the record; no record keeps prior behaviour | Records relational `max_wal_size`; `0` restores PostgreSQL's default, otherwise at least 64 MiB. |

#### dcctl is forward-only {#v150-dcctl}

There is no downgrade. Upgrade and bootstrap re-runs refuse an earlier release than the declared instance, and install refuses an older tool than the last cluster installer, with no override. If an upgrade fails, retry with the new release's tool, roll forward to a fixed release or restore from backup. Published-registry aliases now receive the same version-prefix and pinned broker-digest handling as the canonical spelling.

`--mqtt-device-uplinks` records `spec.mqttDeviceUplinks` and survives flagless re-runs and upgrades. A newly supplied values overlay that contradicts a recorded choice is refused before writes when the flag is omitted; a carried-forward old overlay is allowed and the recorded choice wins. An old CRD that prunes the field is refused before apply.

`dcctl install --relational-store-max-wal-mib` persists the relational WAL target before apply, including across a failed apply and retry. Once recorded it overrides `TF_VAR_postgres_max_wal_size_mib`; omission keeps it. This fixes the earlier need to repeat the setting on every install. It is never inferred from `--size` and can be changed while instances run.

An HA performance-size bootstrap, including a dry run, now refuses before creating anything when fewer than three schedulable nodes can each fit a broker's CPU and memory request. Pick a smaller size, add larger nodes, or explicitly set `nats_cpu_request` / `nats_memory_request` (skips that resource's check). Upgrade is unaffected. `--lwm2m-identities` now validates the device-type token grammar before provisioning.

#### Configuration fails closed {#v150-configuration}

Invalid values listed in [Before you upgrade](#v150-before-you-upgrade) are refused with the key named instead of silently becoming defaults, disabling rotation, wrapping a duration or crashing at startup. Zero and absent retain the documented defaults. Chart schema checks cover the documented key spellings; hand-written documents still need startup validation. Configuration must end after its one document, with no ignored suffix.

#### GraphQL input limits {#v150-input-limits}

- Dashboard name search text over 256 bytes is refused with `LIMIT_EXCEEDED`.
- The audit log's `actor` filter over 256 bytes is refused with `LIMIT_EXCEEDED` on both tenant and admin planes.
- `calculatedMetricBackfills(statuses:)` refuses more than five entries with `LIMIT_EXCEEDED`; repeats in an accepted list are ignored.
- Device and device-type substring searches refuse `nameContains` over 256 UTF-8 bytes, measured before trimming, with `LIMIT_EXCEEDED`.
- Command searches refuse `statuses` or `outcomes` lists over 1,000 entries, including duplicates, with `LIMIT_EXCEEDED`.
- One GraphQL WebSocket connection permits at most 1,000 concurrent subscription operations. A further `subscribe` receives `LIMIT_EXCEEDED`; the connection stays open.

#### Stricter checks on existing operations {#v150-tightenings}

- **Dashboards.** `createDashboard` refuses at 10,000 live dashboards per tenant with `DASHBOARD_LIMIT`. Existing dashboards remain usable, even above the cap; delete unused ones to create more. Publishing stops at 1,000 versions per dashboard with `DASHBOARD_VERSION_LIMIT`; the existing history, published pointer and draft remain usable.
- **Dashboard draft selectors.** Saving a `kind: "device"` datasource requires a string `deviceToken` and a `measurements` array of strings, including both keys. Malformed hand-written inputs are refused at draft save; stored dashboards still read, publish, roll back and render.
- **Notification policies.** Each supplied rule set accepts at most 100 rules (`LIMIT_EXCEEDED`). Existing larger policies keep working; an update omitting `rules` leaves them intact.
- **Campaigns.** Creating a campaign refuses trigger JSON with trailing data instead of silently discarding it.

#### Automation change review (preview) {#v150-automation-review}

A profile's **Change review** tab compares a draft rule change with the active baseline over incident and normal recorded windows. The comparison engine runs both versions on the same recorded events without side effects, comparing alarm transitions and reaction requests, including content. Incomplete or unsupported evidence is `INCONCLUSIVE`, never a pass. Bounds are 50 devices, two windows of at most 24 hours each, up to six hours of lead-in, ten expectations and 2,000 listed changes per window. The Night Shift simulation supplies a recorded scenario: `dcctl sim create --episode night-shift` and `dcctl sim windows`.

Reviews keep the input digests, stream bounds, expectations and result, can be shared, and need `event:read`. Approval needs a recent second factor; draft authors are excluded and self-approval follows the profile's policy. Publishes and rollbacks are now recorded in the audit journal.

Protection is opt-in on a published, eligible profile. It can require one or two approvals. A protected publish must supply `automationReviewToken` and consumes an approved review once, checking the frozen draft, baseline, evidence, expiry, current policy and approvers' current standing. Missing reviews give `REVIEW_REQUIRED`; unavailable standing checks give `REVIEW_UNAVAILABLE`. Published versions link back to their review and evidence. Unprotected profiles retain their existing publish path.

A protected rollback, re-pointing a device type to or from a protected profile, or moving a device out of its bound types needs a recorded break-glass reason and a recent second factor. A rollback changes the baseline and invalidates outstanding approvals. Protected profiles cannot be deleted. PostgreSQL guard triggers enforce these conditions even for previous-release pods: an old write is refused rather than bypassing protection. Tightening applies immediately; loosening needs a second person or, when self-approval is allowed, a 24-hour wait. Protect profiles only after the rollout.

See [Automation change review](../concepts/automation-change-review.md) and [Reviewing a rule change](../guides/reviewing-a-rule-change.md).

#### Agent actions (preview) {#v150-agent-actions}

MCP adds `propose_alarm_action`, `get_proposal` and `list_my_proposals`, registered only with the MCP `agentActuation.enabled` switch on and the caller's required act scope. Requests forward the caller's own bearer token. People approve in the console; there is no MCP approval or execution tool. `agentApprovalAccess` lets qualifying approvers reach the inbox without `agent:admin`; the kill switch still requires `agent:admin`. In-session `stepUp(refreshToken, code)` refreshes `mfa_time` with an authenticator code, with sign-in-again fallback when unavailable or for SSO.

A disabled or purging tenant refuses proposal and execution decisions with `OPERATOR_SUSPENDED`. Disabling or purging also publishes a kill; re-enabling advances the actuation generation. Device-management's local kill fence serializes execution with consumed kills and refuses an unavailable or stale feed with `FENCE_UNAVAILABLE`. Alarm effects enter a transactional outbox, so a committed effect is retried for publication. A kill publish can fail after the policy or tenant change committed: repeat the action to republish it.

Authorization database facts come from one read snapshot; sign-in records are read once before it. Execution asks for the final decision after all database work, immediately before commit, with a four-second deadline and no SQL after the decision. A refusal or late answer rolls everything back; an idle-session termination or lost connection after asking maps to `POLICY_UNAVAILABLE`. A revocation committed after the decision snapshot can still be overtaken within five seconds plus commit time. The bound holds only after all device-management and user-management pods are upgraded.

**Approvals are tied to the approver's sign-in.** Signing out, resetting a password or second factor, or disabling the person ends that approval; approve again from a new sign-in. Earlier approvals without a sign-in no longer count. Console access tokens now carry `sid`, filling the audit journal's session column for console changes too. Enable agent actions only after the full rollout. See [Proposals and approvals](../concepts/mcp.md#agent-proposals).

#### LoRaWAN and commands {#v150-lorawan}

An unconfirmed downlink merely queued by the network server still settles `SUCCESSFUL`, but records neither `deliveredTime` nor `deliveryEvidence: DEVICE_RESPONSE`: no device answered. Confirmed acknowledgements still record `DEVICE_RESPONSE`. The command guide explains which component settles each outcome. The LoRaWAN guide now sends a confirmed command back to the device, and `dc-simulator` can play a ChirpStack downlink queue and acknowledgement feed with `--lorawan-downlink-listen` and `--lorawan-ack`.

#### Observability {#v150-observability}

Opt-in OpenTelemetry tracing follows an event through ingest, NATS publishing and consumption, device-management resolution, event storage and event-processing. Export uses OTLP over gRPC or HTTP. GraphQL spans record no documents, variables or tokens; ingest spans record no credentials or device tokens. At the ingest edge, a caller's `traceparent` is linked to a new local root, never adopted: remote `tracestate` is dropped and the remote sampled flag is ignored. Internal spans continue the platform's context. Disabled tracing installs no provider, goroutines or exporter and leaves message bytes unchanged. Failed-event publishes and split-out retries start a new trace.

A noncanonical instance id logs a warning and sets its per-area gauge (for example `devicechain_devicemanagement_instance_id_noncanonical`) to 1 without refusing startup; see [Before you upgrade](#v150-before-you-upgrade).

#### Concurrent edits preserved {#v150-concurrent-edits}

Partial updates write only named fields for customers, notification channels, device profile drafts, asset types, assets, maintenance windows, metric definitions, device types, detection-rule drafts, command definitions, area types, customer types, relationship types, credentials, devices and geofences. Omitted fields no longer revert a concurrent edit; an empty update writes nothing. A stale area rename no longer restores a concurrently removed boundary-fence link. A credential edit that would overwrite a concurrent password rotation is refused with a retry error.

#### Other changes {#v150-other-changes}

- **Shutdown and handover.** Event-sources flushes pending undecoded-uplink tallies and joins an in-flight debounced disconnect. LwM2M waits for the deleted-tenant sweep before leadership handover. Messaging finishes pending follow-up cache evictions before disconnecting. Governance refreshes are cancelled and joined. Notification shutdown cancels stuck dispatch within its drain budget and avoids resending a delivery already made. Command-delivery cancels a dispatch nudge before it claims, leaving the command queued. Integration authentication waiters stop on caller cancellation; the shared lookup has its own ten-second deadline.
- **Operator leadership.** The operator accepts `--leader-elect`; the shipped manifest enables it.
- **Analytics access.** Reconciliation converges direct and nested group members regardless of reader-name prefix. A colliding superuser is skipped, logged and counted; a collision with the service login or schema owner refuses boot. Foreign-owned views are diagnosed before any repair or grant changes.
- **Console and widgets.** Telemetry headers, alarm tables and command forms/history follow the viewer's language. Alarm-table scrolling is keyboard-accessible. Personal geofence basemap URLs and attribution are applied together; an incomplete pair keeps the prior map. The console warns before a facet declaration changes an existing axis's value type.
- **Existing clients and edits.** Stale connector updates return `STALE_WRITE`; the console keeps unsaved edits and offers reload and retry. Legacy `dcsa_` OAuth clients remain ordinary, maintainable clients, never service accounts; register a replacement before deleting one.
- **Operational fixes.** A gateway cannot restart while its previous timed-out stop is still live. Snapshot reset compares both sequence and watermark, preserving a successor's newer checkpoint. A rejected `CONNECTED` claim no longer advances last activity. Mixed notification delivery is partial, not complete, and a hard failure retains precedence. Payload-encoder precision refusals name the parameter. Fence-archive version skew is recognized only from the peer's own exact unknown-field refusal. A list whose count query fails returns that error instead of a page with an invented total.
- **Charts and documentation.** Release-candidate charts are marked as prereleases on Artifact Hub. Counter tiles read zero while their area is up and backlogs include in-flight messages. The CRL guide clarifies that streaming uploads above 2 MiB require raising `deviceManagement.callout.revocation.maxCrlBytes` (up to 32 MiB). The decoder reference corrects `base64.decode` to at most 5467 bytes of input text, including line breaks; runtime behaviour is unchanged.

#### Schema changes and going back {#v150-going-back}

Exactly four migrations are additive and appended. New tables start empty; existing data is retained.

- **Agent kill fence and effect outbox** (`device-management`, `20261110000100`): adds `agent_actuation_fences` and `agent_effect_outboxes`.
- **Automation reviews** (`device-management`, `20261111150000`): adds `automation_reviews`.
- **Automation review enforcement** (`device-management`, `20261126120000`): adds three tables for protection policies, pending policy changes and break-glass records, one nullable column `device_profile_versions.automation_review_token`, one function and six `BEFORE` row triggers. No profile is protected by migration; the guards also apply to previous-release pods once protection is enabled.
- **Actuation generation** (`user-management`, `20261110130000`): adds `iam_agent_policies.actuation_generation`.

**The upgrade is forward-only; there is no downgrade to `v1.4.0`; `dcctl` now refuses it.** Retry a failed upgrade with the `v1.5.0` tool, roll forward to a fixed release or restore from backup. See the [versioning and upgrade policy](./versioning-policy.md).

#### Known issues {#v150-known-issues}

- **A confirmed LoRaWAN downlink with no network-server report settles `TIMEOUT` at its deadline,** with unknown delivery. Unconfirmed commands recorded before the upgrade retain their old evidence.
- **An escalation that won its claim before a clear was stamped can still deliver after it;** targets recorded after the clear was claimed get no clear.
- **Only alarm ACK/CLEAR proposals execute.** Proposals for other actions do not execute even if approved. The kill switch does not reverse an executed action.
- **Removing and re-granting an approver role within one open sign-in revives that sign-in's approval.** Session binding does not detect that intervening role change.
- **A matured 24-hour loosening of review protection applies at the next protected operation,** rather than at a background timer.
- **Review protection eligibility uses conservative text search,** rather than parsing rules; identifiers or keys that resemble excluded live dependencies can prevent opting in.
- **The agent effect relay is at least once.** Broker deduplication covers 120 seconds; alarm events can arrive out of order, and a row failing 100 times remains for operator attention.

### v1.4.0 — confirmed LoRaWAN downlinks, a device's own payload over MQTT, and notifications that name the device {#v140-upgrade}

`v1.4.0` is an in-place upgrade from `v1.3.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described [for v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Use **the new release's `dcctl` for both commands**, and give `dcctl install` the settings the cluster was installed with, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. Ten appended migrations run on upgrade (two in `device-management`, four in `notification-management`, two in `outbound-connectors` and two in `user-management`); they add tables and columns. The notify-on-clear timestamp migration also sets `notify_on_clear_since = updated_at` where `notify_on_clear` is true and `notify_on_clear_since IS NULL`. See [Schema changes and going back](#v140-going-back). Coming from `v1.2.0` or earlier, read the [`v1.3.0` notes](#v130-upgrade) first and follow them with `--version v1.4.0`.

```bash
dcctl install local --version v1.4.0
dcctl upgrade local devicechain --version v1.4.0 --dry-run
dcctl upgrade local devicechain --version v1.4.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds to the covered interfaces (new GraphQL fields, operations and enum values, a new MQTT topic, new `dcctl` flags and a new `--size`) and removes none. The new agent proposal and approval operations are in the [preview tier](./versioning-policy.md#preview-interfaces), where they may still change in a minor release; the release notes then say what changed. The Helm values this release adds are not covered by the compatibility policy, and every one is off, or keeps the behaviour of `v1.3.0`, by default.

What is new:

- **`dcctl` refuses upgrades in the wrong order.** See [Upgrade ordering is enforced](#v140-ordering).
- **Token audiences can no longer be set to `observe`,** as the `v1.3.0` notes announced. See [Token audiences can no longer be set to observe](#v140-token-audience).
- **A notification policy can notify when an alarm clears,** and alarm notifications name the device and can link to it. See [Notifications that name the device](#v140-notifications).
- **A device can publish its own payload over its MQTT connection** for its profile's decoder, and the console's connect snippet can show it. Opt-in. See [A device's own payload over MQTT](#v140-mqtt-uplinks).
- **LoRaWAN commands can be confirmed,** on ChirpStack v4 and on The Things Stack v3, and a command definition can carry a declarative payload encoder. See [LoRaWAN confirmed downlinks](#v140-lorawan).
- **Ingest metering can credit the lag a platform stall causes.** Opt-in. See [Lag-credited ingest metering](#v140-ingest-metering).
- **A `perf-16k` size,** corrections to `perf-12k`, and a relational `max_wal_size` setting. See [Sizing](#v140-sizing).
- **Agent actions, a preview.** An AI agent can propose acknowledging or clearing alarms, and people approve from their own sessions. The final approval carries out an alarm ACK or CLEAR. See [Agent actions (preview)](#v140-agent-actions).
- **A supported way to reach a service on your own machine from a local cluster:** `--dev-allow-egress`. See [Local development egress](#v140-dev-egress).
- **The Grafana boards page has screenshots of all eight boards,** and the boards count messages in flight. See [Other changes in this release](#v140-other-changes).

#### Before you upgrade {#v140-before-you-upgrade}

What needs action, in the order to do it:

- **Remove `infrastructure.tokenAudience.enforcement: observe` before you upgrade,** if your instance sets it anywhere. A service refuses to start with it and `dcctl upgrade` refuses before changing anything. See [Token audiences can no longer be set to observe](#v140-token-audience).
- **Run `dcctl install` with the new release's `dcctl` before `dcctl upgrade`, always.** `dcctl upgrade` now reads the cluster's install record and refuses, before it writes anything, when the `dcctl` that last finished `dcctl install` is older than the one upgrading, when an install started and never finished, or when the record is missing. That includes a patch release: a flagless `dcctl upgrade` with a newer patch release of `dcctl` needs `dcctl install` with that `dcctl` first. A flagless `dcctl upgrade`, which re-applies the instance's declared version, is also refused when that version is newer than the `dcctl` you run. See [Upgrade ordering is enforced](#v140-ordering).
- **An explicit `--version` must be your `dcctl`'s own release,** for `install` and for `upgrade`. To move to another release, use that release's `dcctl`. A custom-registry tag, `--build` and `enable-area` are not affected.
- **Turn on the new opt-in features only after the rollout completes, and each in an upgrade of its own.** Each is off by default, and a pod of `v1.3.0` that is still running does not know it:
  - Set `infrastructure.nats.mqttDeviceUplinks` in an upgrade of its own, after `v1.4.0` is in place. See [A device's own payload over MQTT](#v140-mqtt-uplinks) for what an older pod does with it: one that restarts while it is on stops at startup.
  - Set `agentActuation.enabled` in a separate upgrade, after the rollout of `v1.4.0` has finished. See [Agent actions (preview)](#v140-agent-actions).
  - Set `eventSources.ingestMetering: lagCredit` only after every `event-sources` pod is `v1.4.0`. See [Lag-credited ingest metering](#v140-ingest-metering).
  - Turn on the LoRaWAN downlink (`functionalAreas.outbound-connectors.config.lpwanDownlink.enabled`) only after the rollout, if it is not on already: a `v1.3.0` `event-sources` pod that restarts while it is on stops on the configuration key `v1.4.0` derives from it.
  - Pass `--dev-allow-egress` only after the rollout: an entry adds `infrastructure.egress.devAllowedDestinations` to the instance configuration, which a `v1.3.0` pod that restarts refuses. See [Local development egress](#v140-dev-egress).
- **If the LoRaWAN downlink is already on** (`functionalAreas.outbound-connectors.config.lpwanDownlink.enabled`), a `v1.3.0` `event-sources` pod that restarts during the rollout stops with `unknown field "lpwanDownlink"`. `v1.4.0` adds that key to the configuration `event-sources` receives (the chart derives it; it is not set by hand), and a `v1.3.0` pod refuses a key it does not know. A pod that does not restart is unaffected, and the rollout replaces a stopped one with a `v1.4.0` pod, which starts. With the downlink off, as by default, nothing changes for `event-sources`.
- **Check `linkBaseUrl` on generic webhook, Slack, Teams and PagerDuty channels.** It is now validated when you save the channel (a query or a fragment is refused), and a stored invalid one omits the links at delivery after the upgrade, and the notification is still sent. See [Notifications that name the device](#v140-notifications).
- **Rename OAuth client ids starting with `dcsa_` (case-insensitive), including seeded configuration.** The prefix identifies service accounts and can collide with the agent policy client list. See [Stricter checks on existing operations](#v140-tightenings).
- **If an instance is recorded with `--size perf-12k`, expect two pod-spec changes on its next `dcctl upgrade`:** `device-management` requests 400m CPU (was 500m), and `event-sources` has a 256Mi memory limit (was 512Mi). Pods restart as for any rollout. See [Sizing](#v140-sizing).
- **For a first connect admitted with OCSP status unavailable, expect `devicechain_devicemanagement_x509_ocsp_unavailable_allowed_total` to rise by one, not two.** The first connect of a device ran the OCSP step twice and counted both; the series counts connects, as its help says, and now does. Admit and refuse decisions, the name and the labels are unchanged. Re-baseline a dashboard or alert you tuned against the doubled rate.
- **Review scripts against the stricter checks below,** including raw LoRaWAN commands and HTTP decoder lookup timeouts.

New Helm values and `dcctl` settings, each off or unchanged by default:

| Value | Default | What it does |
| --- | --- | --- |
| `eventSources.ingestMetering` | `legacy` | `lagCredit` selects the lag-credited gate. Rendered into the `event-sources` pods as an environment variable. |
| `instance.config.infrastructure.nats.mqttDeviceUplinks` | `false` | Grants each device the right to publish its own uplink topic, and adds the uplink subject to the capture stream. Settable with `dcctl --values`. |
| `agentActuation.enabled` under `functionalAreas.user-management.config` | `false` | The instance switch for agent actions, the first of three. It is a key of the instance configuration document, not a `values.yaml` default, and absent means off. |
| `postgres_max_wal_size_mib` (cluster-root OpenTofu variable) | `0` | The relational store's `max_wal_size`; `0` keeps PostgreSQL's own default of 1 GB. |

#### Upgrade ordering is enforced {#v140-ordering}

An explicit `dcctl install --version` or `dcctl upgrade --version` now refuses a different published release, older or newer, than the running `dcctl`. A flagless upgrade keeps the declared version but refuses a declaration newer than the running tool. Use the target release's `dcctl`, run `dcctl install` with the cluster's settings, then upgrade each instance. The binary and the release it installs must match so that the tool knows that release's migrations and ordering.

`dcctl upgrade` also requires a completed install record from a `dcctl` at least as new as itself. A missing or unreadable record, an unfinished install, or an older completed install is refused before writes. Scripts that previously skipped install must run it first, including for a patch release.

The same release makes `-i` / `--instance` work on commands that talk to the platform API (`events failed`, `attr`, `dead-letters`, `presence`, `product`, `tenant-deletion`, `test` and `device register`). Without `--server`, `--instance` uses the host and TLS setting from the declaration; an explicit `--server` or `--tls` wins.

#### Stricter checks on existing operations {#v140-tightenings}

- **OAuth client ids.** Saves and seeded configuration now refuse a `dcsa_` prefix, case-insensitively. Rename such clients: the prefix identifies service accounts, including agent accounts, and could otherwise be mistaken for one or collide with the agent policy's client list.
- **Notification links.** Saving a generic webhook, Slack, Teams or PagerDuty channel now refuses a `linkBaseUrl` with a query or fragment. Remove them from the base URL: links are built by appending a path. A stored invalid base URL omits the links at delivery; the notification is still sent.
- **LoRaWAN dispatch.** Every dispatch now reads the published command vocabulary and encoder, including raw commands without an encoder. A lookup outage leaves delivery waiting for a retry; a definitively missing command in a constrained vocabulary refuses it as not sent. Keep the command definition published and restore lookup availability. A waiting command uses the encoder current at dispatch, never a cached encoder from when it was queued.
- **HTTP device-decoder lookup.** A caller's cancellation now stops waiting for the lookup. The shared backend lookup continues to populate the cache but has its own 10-second limit, so it cannot run indefinitely. Scripts may see an earlier cancellation or a retryable lookup failure; retry the latter rather than treating it as no decoder.

#### Token audiences can no longer be set to observe {#v140-token-audience}

The `v1.3.0` notes announced this. `infrastructure.tokenAudience.enforcement: observe`, accepted in `v1.2.0` and `v1.3.0` as an opt-out, is refused:

- A service stops at startup, naming the key, if the instance configuration sets it.
- The chart's values schema rejects it, in Helm values and in a `dcctl` values overlay.
- `dcctl upgrade` refuses before changing anything if the overlay recorded on the instance, the values the release is installed with, or the document in `instance.existingSecret` still sets it.

Remove the key (unset means `enforce`) or set it to `enforce`, then upgrade. On a `dcctl`-managed instance whose recorded overlay sets it, run `dcctl upgrade` with `--reset-values`, or with `--values <file without the key>`; a run with neither is refused, naming those two commands. If you supply the document yourself (`instance.existingSecret`), remove the key from your document before you run `helm upgrade`, because the services refuse it at startup and a plain `helm upgrade` cannot look inside your Secret; also update `instance.existingSecretChecksum`. Before upgrading, check that `{__name__=~"devicechain_[a-z0-9]+_token_audience_checks_total", verdict="would_reject"}` and `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}` show no new increments over a representative traffic window on the `v1.3` instance, or the clients behind them will be refused. The `would_reject` series and the `TokenAudienceWouldReject` and `OAuthResourceWouldReject` alerts remain for compatibility and can no longer fire. `unscopedClientCredentials` is unchanged.

#### Notifications that name the device {#v140-notifications}

- **`notifyOnClear`.** A notification policy can notify when an alarm it paged clears; the option is off by default. The clear goes only to channels and recipients that delivered a page in that cycle. Quiet hours discard a clear notification rather than hold it. Delivery is at most once after claiming: a crash between claiming and sending can lose it. Failed pickup retries back off two minutes and stop 30 minutes after the clear.
- **Notifications name the device.** Webhook payloads carry `deviceToken`, `deviceName` and `deviceTypeToken` when known. The Slack, Teams, PagerDuty and email summaries show the device name; the generic webhook's `text` names the device by its token, and the device-authored name is only in the structured `deviceName` field, so a name cannot inject markup into a chat tool that renders `text`. To post to Slack, use the native Slack channel rather than a generic webhook. Timed re-notifications and late clears use the stored source; older rows without it omit these fields. `originatorType` and `originatorId` are included when known.
- **Links.** Generic webhook, Slack, Teams and PagerDuty channels can set `linkBaseUrl`. Generic webhook payloads gain `alarmUrl` and, when the device is known, `deviceUrl`; native messages can link to the device. See [Stricter checks on existing operations](#v140-tightenings) before saving an existing channel.
- **Text rendering.** Slack's control characters (`&`, `<`, `>`) are escaped and Teams mentions and links are neutralised in device- or tenant-authored text. Names in summaries become one line and are capped at 120 characters; structured payloads keep the stored name. Non-ASCII email subjects are RFC 2047 encoded; ASCII subjects pass through unchanged.

#### A device's own payload over MQTT {#v140-mqtt-uplinks}

A device can publish its own bytes to `{instance}/{tenant}/devices/{device}/uplink` and the decoder on its device type's profile turns them into events, the same capability the HTTP `uplink` route gave in `v1.3.0`. The device authenticates at connect and its signed grant names only its own uplink topic, so a payload can never be decoded as another device. A payload on `events` is still never read by a decoder.

It is opt-in: with `infrastructure.nats.mqttDeviceUplinks` off, which is the default, the grant is exactly what earlier releases minted and the broker refuses a publish to the topic. Terminal carrier errors are recorded as failed decodes with their cause and counted in the new `devicechain_eventsources_device_uplink_messages_total{result}`; rate-limit sheds are acknowledged separately, and message-stage sheds bypass this counter. There is also a new service-plane query, `brokerAssertedDeviceDecoder`.

**Turn it on in an upgrade of its own, after `v1.4.0` is in place.** Turning it on also makes the capture stream declare the uplink subject. `mqttDeviceUplinks` is a key in the shared instance configuration, so while it is on, a `v1.3.0` pod of any area that starts or restarts stops at startup with `unknown field "mqttDeviceUplinks"`, before any stream check. A `v1.3.0` pod that does not restart keeps running, with these effects: an `event-sources` pod that is already running acknowledges and drops a subject outside its two, so a raw payload that reaches it is lost; and a `device-management` pod refuses the uplink it cannot place, which is redelivered. With it off the capture stream is exactly what `v1.3.0` declares, so a pod that restarts mid-rollout is unaffected. Existing publish grants persist until reconnect or session token expiry, but disabling the setting removes uplinks from capture during reconciliation, so continued publishing can lose payloads.

With `dcctl`, set it in a `--values` overlay: `instance.config.infrastructure.nats.mqttDeviceUplinks: true`; later upgrades carry it. See the [payload decoders guide](../guides/payload-decoders.md) and the [Device Protocol Reference](../reference/device-protocol.md#raw-payload). The console's **Send data** tab for a device now offers a **Raw payload** choice beside the event JSON, with the `curl` for the HTTP route and the `mosquitto_pub` for the topic. It cannot see whether the operator enabled either, so each command names the setting it needs.

#### LoRaWAN confirmed downlinks {#v140-lorawan}

- **ChirpStack v4.** A command with `"confirmed": true` stays `SENT` until the network server settles it, instead of being answered when the downlink is queued. ChirpStack's `ack` and `txack` events arrive at the inbound integration URL already configured for uplinks. An acknowledgement settles the command `SUCCESSFUL`; an unacknowledged confirmation timeout, or nothing by the deadline, settles it `TIMEOUT` with unknown delivery, never "not delivered", because the device may have heard it. The deadline is the earlier of command expiry and 24 hours after queueing. An event that cannot be matched to a pending downlink by queue item id, integration and DevEUI is dropped and counted in `devicechain_outboundconnectors_lpwan_ack_reports_total`, never matched by guess.
- **The Things Stack v3.** The downlink also reaches devices behind a The Things Stack v3 network server. Create a `lorawan_downlink` connector with `networkServer: "tts_v3"` and the application's id; the device needs a `tts.device-id` alias. Confirmed downlinks are settled from the `downlink_ack`, `downlink_nack`, `downlink_sent` and `downlink_failed` webhook events, which must be enabled on the application's webhook with paths that start with `downlink` (for example `/downlink-ack`). A failure gives a definite not-delivered outcome only when it proves the frame was never transmitted; uncertain failures settle as `TIMEOUT` with unknown delivery.
- **A payload encoder on a command definition.** A command definition can carry an optional, declarative `encoder` that turns the command's typed parameters into the application payload, the mirror of the uplink decoders: an `fPort`, an optional static `confirmed`, and ordered fields (constant bytes, fixed-width integers of 8, 16 or 32 bits in either byte order with `scale` and `offset`, booleans and packed bit fields). It is validated when the definition is created or updated and at profile publish, so a bad encoder fails loudly with the command named. Fractional inputs are allowed when scale/offset produces an in-range integer within the encoder's floating-point tolerance. Values outside those bounds, missing values and wrong types refuse the command as not sent. A definition without an encoder keeps the raw hex or base64 contract. A command waiting for delivery uses the published encoder current when it is dispatched, including after a republish; every dispatch reads the vocabulary again. A lookup outage defers delivery for retry, rather than using a stale encoder. See the [inbound integrations guide](../guides/inbound-integrations.md).

The feature needs `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled`, as in `v1.3.0`. Do not send confirmed downlinks until the rollout is complete, so that every `outbound-connectors` pod reads the new acknowledgement stream. A pod of `v1.3.0` that is still running refuses `"confirmed": true` and answers the command as not sent, so it cannot leave a command stuck; a pod that stops between queueing the downlink and recording it leaves the command `SENT` until the expiry sweep settles it `TIMEOUT`. For ChirpStack, confirmed downlinks need the HTTP integration to send `ack` (and, optionally, `txack`) events to the integration URL; for The Things Stack, enable the downlink events above. A network-server report that `outbound-connectors` fails to process on every delivery attempt is not recorded as a dead letter: its downlink settles at the deadline as above.

#### Lag-credited ingest metering {#v140-ingest-metering}

After a short platform stall (a message broker pause, a paused reader, a slow decode stage), a tenant that stayed within its ceiling has its queued traffic read in a burst, and the part the live allowance cannot hold was shed after the device had been told the message arrived. A new metering mode removes that.

- **Opt-in, and not the default.** `eventSources.ingestMetering` stays `legacy`, and it remains so until the mode has been measured on a kind rig and a cloud round. Set it to `lagCredit` to use the new gate, and back to `legacy` to return. It is a Helm value rendered into the `event-sources` pods as an environment variable, so a pod of an earlier release never sees it. An unrecognised value stops the pod at startup. The upgrade is forward-only.
- **What it does.** Fresh traffic is still counted when the pod reads it, but a tenant's allowance grows by what its rate earns over the measured delay of the readers, up to the time its burst covers (never more than 5 seconds). A tenant above its ceiling is still shed by its excess, and a backlog older than the cover drains as before.
- **What it does not change.** Sheds over the ceiling, by an overload band or by the hard drop are counted by the existing rate-limit counters as before. HTTP and inbound integrations keep their limits. Ceilings are still per replica.
- **How it measures.** Each replica estimates the offset between the broker's append stamps and its own clock, and how far each reader is behind, from the delivery information the broker already sends, and asks the broker about four times a second whether a quiet stream has anything queued. A replica that starts during a stall has not measured the broker's clock yet, so for the first moments of that stall it can credit less than it should.
- **New metrics,** empty in `legacy` mode and none with a tenant label: `devicechain_eventsources_ingest_shed_cause_total{stream,route,cause}`, `devicechain_eventsources_ingest_lag_credit_seconds{stream}`, `devicechain_eventsources_reader_lag_seconds{stream}`, `devicechain_eventsources_append_skew_seconds{stream}` and `devicechain_eventsources_append_skew_clamped_total{stream}`. **New alerts,** quiet in `legacy` mode: `IngestShedBeyondLagCredit` and `IngestAppendSkewLarge`.

#### Sizing {#v140-sizing}

- **`perf-16k` is a new `--size` value** for sustained 16,000 events a second: NATS at 6 CPU and 6Gi requested, an event store `max_wal_size` of 40960 MiB on a 260Gi volume, `event-processing` at 2 CPU and 1536Mi, and a 36Gi JetStream volume for the design rate. The measured shape held 16,000 events a second exactly; 20,000 events a second is stored exactly but over the latency target, so it is documented as a throughput ceiling and there is no `perf-20k`. The `perf-16k` values are derived from the measured ramp and have not yet been run as written; the sizing page says so.
- **`perf-12k` is corrected:** `device-management` requests 400m CPU (was 500m) and `event-sources` has a 256Mi memory limit (was 512Mi, measured at 81 MiB resident at most). An instance recorded with `perf-12k` picks both up on its next `dcctl upgrade`.
- **A relational `max_wal_size`.** The new cluster-root OpenTofu variable `postgres_max_wal_size_mib` (default `0`, which is PostgreSQL's own 1 GB, so no existing install changes) sets it through the cluster definition. The plan rejects an allowance above one quarter of `postgres_storage` when storage is expressed as an integer Mi, Gi or Ti quantity; other forms bypass this check. The relational store belongs to the cluster, so `dcctl bootstrap --size` does not write it: state it at install with `TF_VAR_postgres_max_wal_size_mib`, with `TF_VAR_postgres_storage` large enough. `dcctl` does not persist this setting: a later `dcctl install` without it resets the value to PostgreSQL's default. Repeat it on every install. The sizing page lists the value each size was measured with.

See [Sizing](./sizing.md).

#### Agent actions (preview) {#v140-agent-actions}

An AI agent can propose acknowledging or clearing alarms. In this preview, off by default, the final approval **carries out the ACK or CLEAR as the approving person**, in one database transaction after the platform asks the agent policy again under that person's own credentials. A proposal whose alarm was raised again or deleted, whose dependencies changed, or whose policy refuses (including the tenant switch, instance flag or client admission) ends `REFUSED` and changes no alarm. Audit rows name the approving person, on-behalf-of and the proposal. Console approvals also record the agent's client; if an approving token has its own `client_id`, that claim takes precedence. There is no automatic mode. See [Proposals and approvals](../concepts/mcp.md#agent-proposals).

- **Enable only after the rollout.** The instance switch `agentActuation.enabled`, the operator's per-tenant suspension and the tenant's agent policy all gate decisions. `v1.3.0` has no agent API, policy tables or suspension surface: agent requests fail on those pods and nothing executes. `v1.4.0` pods honour the switches. Enable the instance switch in a separate upgrade after all pods are `v1.4.0`. People who signed in before the upgrade must sign in again before their agent can propose: a sign-in started before `v1.4.0` carries no session epoch, and a proposal made under it is refused as `PROPOSER_SESSION_ENDED`. After they sign in again, the agent proposes again.
- **Console Approvals inbox (Preview).** People can list proposals, read their detail, approve or reject, and use the tenant kill switch. When step-up is required, the console offers sign-in again and returns to the proposal. Policy, proposals and local decision state reset when the tenant, user or session changes. The final approval's confirmation says that approving carries the action out as you, and the inbox then shows the outcome: `EXECUTED` with the result, `REFUSED` with the reason (including `DEPENDENCY_CHANGED` when the alarms changed), or `CONCURRENT_APPROVAL` when someone else is deciding the same proposal, after which it refreshes. Against a server that still returns the deprecated `EXECUTION_NOT_AVAILABLE`, it says the approval was not recorded and no alarm changed.
- **Approvals are checked again.** The approving person needs the action's write permission, an approver role and a fresh second factor. Earlier approvals count only while their approvers still qualify and their second factors remain fresh. Changes to who holds a role change whose approval counts.
- **The kill switch takes effect at the next decision.** `setAgentActuation(enabled: false)` is accepted even when the policy cannot be read or validated. A decision that has not yet been reached refuses execution when the switch is off; an action already past that decision may complete. The switch does not undo an executed action. Turning actions back on needs a fresh second factor. This is an application-level gate.
- **Other actions do not execute.** Only alarm ACK/CLEAR proposals have an execution path in this preview; commands and rollouts do not. MCP tools execute no actions.
- **Proposer standing.** The platform checks the original sign-in and current permissions again. A caller who is neither the proposer nor an `agent:admin` receives the coarse `PROPOSER_NOT_IN_STANDING` reason; stored `PROPOSER_` refusal reasons are also coarsened.
- **Proposal targets must be valid alarm tokens.** `proposeAlarmAction` refuses a target that is not a well-formed token, so a stored proposal cannot carry free-form text that a later read would present as a platform identifier.
- **Deprecation.** `EXECUTION_NOT_AVAILABLE` is no longer returned and is deprecated; it remains in the vocabulary.

#### Local development egress {#v140-dev-egress}

`dcctl bootstrap` and `dcctl upgrade` take `--dev-allow-egress <address-or-CIDR>` (repeatable, or comma-separated; `none` clears it) so a webhook or connector on a local cluster can reach a service on your own machine or LAN. It is refused unless the kube context starts with `kind-` or `k3d-`, or is `minikube`, `docker-desktop` or `rancher-desktop` (also accepted with a hyphen and profile suffix). It is a separate key, `infrastructure.egress.devAllowedDestinations`, and the operator's `allowedDestinations` is still not tunable. The cloud metadata addresses, loopback, link-local and multicast ranges, and the other never-allowed ranges stay blocked whatever you list, and an entry that overlaps one fails `dcctl` up front. It is recorded on the instance so later upgrades keep it, `dcctl instances` shows it as `DEV EGRESS ALLOWED`, and every service that uses it logs a warning at startup. See [Outbound connectors](../concepts/outbound-connectors.md). Set it only after every pod is `v1.4.0`: a `v1.3.0` pod that restarts refuses a configuration that lists an entry.

#### Other changes in this release {#v140-other-changes}

Nothing here needs action.

- **The [Grafana boards](./grafana-boards.md) page has screenshots** of all eight boards from a `v1.3.0` instance under a steady synthetic load, with a scripted outage.
- **Waiting-backlog panels count messages in flight.** The broker fallback counts waiting and taken-but-unacknowledged messages together, so messages a restarted reader took stay counted; the ingest, persistence, device-state, messaging, delivery and event-processing backlog panels were corrected. The platform OOM and delivery refused or quiet-hours tiles read `0`, not "No data", while their owner is up.
- **The notification clear pickup** reports a pass that delivered and then ran out of budget as partial, not failed, and counts a tenant whose reads both failed once.
- **A failed delete of a deleted tenant's pending downlink** is recorded as `store_failed`, and the acknowledgement sweep drops a deleted tenant's rows before claiming them.
- **Panels and alerts for this release's features.** The Ingest, Command Delivery and Delivery boards gain panels for device MQTT uplinks, LoRaWAN acknowledgements and downlinks, integration downlink reports, lag-credited ingest, OCSP and the clear pickup, and seven new alerts have [runbook entries](./alert-runbook.md): `LpwanAckReportsAnomalous`, `LpwanAckSettlementFailing`, `LpwanDownlinkTrackingFailing`, `IntegrationDownlinkReportsDiscarded`, `DeviceUplinkMessagesFailing`, `X509OcspUnavailableAllowed` and `NotificationClearPickupFailing`.
- **A refused database read** returns an error instead of a nil-pointer panic.
- **An agent token can no longer change a person's profile name:** `updateProfile` refuses agent tokens.
- **Built with Go 1.27.4.** The `v1.4.0` binaries and images are built with Go 1.27.4, which fixes a vulnerability in the standard library's `net/http`.

#### Schema changes and going back {#v140-going-back}

The following migrations are additive and appended. New columns are nullable or defaulted and new tables start empty. This supports overlapping pods during the upgrade; it is not a downgrade path.

- **Agent policy** (`user-management`, migration `20261109120000`). Two new tables, `iam_agent_policies` (a head row per tenant) and `iam_agent_policy_versions` (append-only published versions), and the agent flag on `iam_service_accounts`. No rows are seeded: a tenant with no published policy has agent actuation off.
- **Tenant kill switch** (`user-management`, migration `20261109130000`). Three columns on `iam_agent_policies`: `tenant_actuation_off`, not null and defaulting to false, and nullable `actuation_off_at` and `actuation_off_by`. `v1.3.0` has no agent policy surface.
- **Agent proposals** (`device-management`, migration `20261125090000`). One new table, `agent_proposals`.
- **Command encoder** (`device-management`, migration `20261109000100`). One nullable column, `encoder`, on the command definitions.
- **Notify on clear** (`notification-management`, migrations `20261109000100`, `20261109000200` and `20261110000100`). A `notify_on_clear` column with a default of false on the policies, per-cycle columns and a partial index on the notification state table, and a nullable `notify_on_clear_since` on the policies, set to `updated_at` only where `notify_on_clear` is true and `notify_on_clear_since IS NULL`.
- **Notification state source** (`notification-management`, migration `20261110000200`). Six nullable columns on the notification state table: `originator_type`, `originator_id`, `device_token`, `device_name`, `device_type_token` and `source_at`. No backfill.
- **Pending acknowledgements** (`outbound-connectors`, migrations `20261109120000` and `20261110120000`). One new table, `lpwan_pending_acks`, and a nullable `device_ref` column on it.

**The upgrade is forward-only; there is no downgrade to `v1.3.0`.** A failed upgrade is fixed by running `dcctl upgrade` again with the `v1.4.0` `dcctl`. That tool refuses `--version v1.3.0`. Follow the [versioning and upgrade policy](./versioning-policy.md).

#### Known issues {#v140-known-issues}

- **A confirmed LoRaWAN downlink that the network server never reports on settles by its deadline,** as `TIMEOUT` with unknown delivery.
- **An escalation that had already won its claim before a clear was stamped can still deliver after it,** and an escalation's targets recorded after the clear was claimed get no clear.
- **The relational `max_wal_size` is not written by `dcctl bootstrap --size`.** Set it at install and repeat it on every install; a run without it restores PostgreSQL's default.
- **Proposals for anything other than alarm ACK/CLEAR do not execute,** even if approved; MCP tools execute no actions.
- **The kill switch does not reverse an executed action.** It takes effect at the next decision; an action already past that decision may complete.

### v1.3.0 — commands reach HTTP and LoRaWAN devices, and deliveries leave evidence {#v130-upgrade}

`v1.3.0` is an in-place upgrade from `v1.2.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for each instance, as described [for v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Give `dcctl install` the settings the cluster was installed with, such as `--cluster`, `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as `--version`. Ten appended migrations run on upgrade (five in `device-management`, two in `command-delivery`, two in `event-management` and one in `notification-management`), and every area's audit journal gains four nullable columns; they add tables, nullable columns and indexes, and change no existing row. See [Schema changes and going back](#v130-going-back). Run `dcctl install` before the instance upgrade, as above: it also moves the Alertmanager onto the routing configuration that `dcctl` writes (see [Alerts reach a person](#v130-alert-routing)). **Token audiences are now enforced by default:** read [Token audiences are enforced by default](#v130-token-audience) before you upgrade, above all if you write the instance configuration yourself. Coming from `v1.1.0`, read the [`v1.2.0` notes](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-upgrade) first and follow them with `--version v1.3.0`.

```bash
dcctl install local --version v1.3.0
dcctl upgrade local devicechain --version v1.3.0 --dry-run
dcctl upgrade local devicechain --version v1.3.0
```

The [versioning and upgrade policy](./versioning-policy.md) applies. This release adds to the covered interfaces (new GraphQL queries, mutations, fields and enum values, new HTTP device routes, new `dcctl` flags and commands, a new C# SDK type) and removes none. Three things about the policy itself are new in this release:

- **A preview tier.** A new served surface can spend its first minor releases in [preview](./versioning-policy.md#preview-interfaces), where it may still change in a minor release; the release notes then say what changed and what to do. In this release the preview surfaces are the rule tests (the `evaluateRules` operation of the event-processing API, the scenario and test file formats, and `dcctl test`) and `dcctl product export`, `plan` and `apply` with their bundle format (`product.devicechain.io/v1alpha1`). Build on them, and expect a change to be announced here.
- **The MCP tool catalog is not covered.** Tool names, inputs and result shapes can change in a minor release, and this one changes them: see [MCP tool results carry their provenance](#v130-mcp-provenance).
- **Helm values.** The values this release adds or changes are not covered by the compatibility policy, and this section is where a minor release says so. Every new value is off, or keeps the behaviour of `v1.2.0`, by default.

What is new:

- **Token audiences are enforced by default,** as the `v1.2.0` notes announced. An API refuses a token bound to a resource it does not serve with `401`, and the authorization server refuses a request for a resource the instance does not serve with `invalid_target`. Tokens bound to nothing, which includes every sign-in and every service token, are not affected, and neither are MCP tokens. See [Token audiences are enforced by default](#v130-token-audience).
- **Commands reach devices that hold no connection.** A device that only speaks HTTP fetches its commands with a long poll, acknowledges receipt and answers. A device behind a ChirpStack v4 network server gets its command as a LoRaWAN downlink. Every command now records how its delivery was observed, on every transport. See [Commands over HTTP, and delivery evidence](#v130-http-command-pull) and [LoRaWAN downlink](#v130-lorawan-downlink).
- **Alerts reach a person.** `dcctl install` and `dcctl upgrade` route the platform's own alerts to a webhook, an email address or a configuration of your own, a dead-man receiver tells you when alerting itself goes quiet, every alert links to a runbook entry, and an instance that routes its alerts nowhere says so. See [Alerts reach a person](#v130-alert-routing).
- **Outbound webhooks can be signed,** per the open Standard Webhooks specification, for the `httpCall` rule action and for webhook notification channels. See [Signed outbound webhooks](#v130-signed-webhooks).
- **The HTTP event endpoint tells a device the truth sooner.** An unknown tenant or a credential that will not authenticate is answered `401` at once, a refused document's `400` names the offending field, and an accepted event carries a receipt. See [The HTTP event endpoint answers 401](#v130-http-events-401).
- **A device can post its own payload for its profile's decoder,** on a new opt-in route. See [A device's own payload, decoded](#v130-device-uplinks).
- **Certificate connects can be checked with OCSP,** per trust anchor, off by default. See [OCSP for certificate connects](#v130-ocsp).
- **A tenant's product can be kept as files** with `dcctl product export`, `plan` and `apply` (preview). See [Product as code](#v130-product-as-code).
- **Detection rules can be unit-tested** with `dcctl test` and the `evaluateRules` query (preview). See [Rule tests](#v130-rule-tests).
- **Each tenant's daily event usage is recorded** and readable by the operator. See [Event usage per tenant](#v130-usage-metering).
- **Campaigns send commands on a schedule:** cron, calendar dates, holidays, and sunrise or sunset at each device's own installed position. See [Campaigns](#v130-campaigns) and [Installed position](#v130-installed-position).
- **Notification deliveries are recorded,** one row per attempt, with each channel's health. See [The notification delivery ledger](#v130-delivery-ledger).
- **Alarm triage in the console:** alarms link to their device and show units, several can be acknowledged at once, a device has an Alarms tab, and the home page counts alarms and devices online. Operators can also label what an alarm turned out to be. See [The console searches devices and triages alarms](#v130-console) and [Alarm dispositions](#v130-alarm-dispositions).
- **Vocabulary terms on the native model:** device, asset, area and customer types, relationship types and metrics can carry terms from Brick, SAREF, Smart Data Models and NGSI-LD. See [Semantic bindings](#v130-semantic-bindings).
- **MCP tool results mark device- and user-written text as data,** under an `untrusted` member with its source. See [MCP tool results carry their provenance](#v130-mcp-provenance).

#### Before you upgrade {#v130-before-you-upgrade}

What needs action, in the order to do it:

- **Check for tokens bound to the wrong resource, and decide whether to opt out of enforcement for this minor.** Token audiences are now enforced by default. On the `v1.2.0` instance, `{__name__=~"devicechain_[a-z0-9]+_token_audience_checks_total", verdict="would_reject"}` and `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}` should read zero; if they do, the new default changes nothing for your clients. If you supply the instance configuration yourself (`instance.existingSecret`) and run the MCP area, add the `tokenAudience` block to the document first, or AI agents lose MCP access. See [Token audiences are enforced by default](#v130-token-audience).
- **If you routed alerts by editing the Alertmanager's own Secret by hand, move that configuration to `dcctl install --alert-config <file>` first.** From this release the Alertmanager reads the Secret `dc-alertmanager-config` that `dcctl` writes, so a hand-edited configuration stops being read when you run `dcctl install`. A cluster installed by an earlier release also needs one `dcctl install` before `dcctl upgrade` accepts the `--alert-*` flags. See [Alerts reach a person](#v130-alert-routing).
- **Know that `POST /{instanceId}/{tenant}/events` now answers `401` for an unknown tenant or a credential that will not authenticate.** These requests used to be answered `202` and were then refused later, so their events were never stored. A device that sees `401` needs its tenant name or credential fixed; a device whose events are stored sees no change. During the rolling upgrade, old pods still answer `202` for these requests. See [The HTTP event endpoint answers 401](#v130-http-events-401).
- **Switch a trust anchor's OCSP mode on only after the rolling upgrade has completed.** Until every `device-management` replica runs this release, older replicas do not check OCSP, so a certificate one replica refuses could still be admitted by another. See [OCSP for certificate connects](#v130-ocsp).
- **Turn on the other new opt-in features only after the rollout completes, too.** Each is off by default and has the same reason: a pod of `v1.2.0` that is still running does not know it.
  - Set a device type's `commandChannel` to `HTTP_PULL` only after every replica of every service is upgraded; an older `command-delivery` publishes such a device's commands where nobody reads them, and they time out.
  - Turn on `httpIngest.deviceUplinks` only after every `device-management` replica is upgraded; an older one refuses the uplink after it was answered `202`.
  - Turn on webhook signing (`signingSecretRef` on an `httpCall` action, `"signing": true` on a webhook channel) only after every `outbound-connectors` and `notification-management` replica is upgraded; an older one delivers unsigned.
  - Create campaigns with sunrise or sunset triggers after the rollout: a `user-management` replica of `v1.2.0` refuses to mint the token that reads installed positions, so solar planning reports an error and retries until it is gone.
- **Move any `httpCall` Bearer credential handle that starts with `webhook-signing/`.** That namespace is now reserved for signing secrets, and a rule naming such a handle is refused when it is published. See [Signed outbound webhooks](#v130-signed-webhooks).
- **Update MCP clients that read free text from a tool result.** A device's name, description and external id, a metric's name and unit, an alarm's key and a command's name and error now sit under `untrusted`, as `{ "value": ..., "source": ... }`. The MCP tool catalog is not a covered interface, so this ships in a minor release. See [MCP tool results carry their provenance](#v130-mcp-provenance).
- **Expect `devicechain_eventprocessing_detect_late_samples_total` to read higher.** Late readings that a closed tumbling window or a closed session drops are now counted, as every other rule kind's already were; until now those drops were silent. Detection results do not change. Re-baseline a dashboard or alert you tuned against the old rate. See [Rule tests](#v130-rule-tests).
- **Publish profiles and asset types that carry semantic bindings after the rollout.** During the rolling window an older pod that serves the publish writes a snapshot without the bindings. The draft keeps them, and the next publish from an upgraded pod freezes them. See [Semantic bindings](#v130-semantic-bindings).
- **Expect the `event-management` migration to take longer on a large event store.** It builds an index on the events table one chunk at a time. See [Event usage per tenant](#v130-usage-metering).
- **Add the new alerts and metrics to your routing and dashboards.** `devicechain_usermanagement_service_token_checks_total` is new ([details](#v130-service-token-ceiling)), as are the OCSP series on certificate connects and `devicechain_notificationmanagement_ledger_writes_total`.
- **Nothing needs doing for anything else in this release.** Every other change is additive or off by default. `infrastructure.tokenAudience.enforcement: observe` is accepted in this release only, as an opt-out, and `v1.4` refuses it. The `v1.2.0` deprecation of a `client_credentials` request that names a `resource` without a `scope` still stands.

New authorities:

- **`usage:read`** is a system-tier authority. It reads each tenant's event usage on the event-management admin API. The built-in `superuser` role holds `*` and so has it; grant it to another system role to let that role read usage. No tenant role can hold it.
- **`location:installed:read`** is a tenant-tier authority. It reads a device's or an asset's installed position and nothing else. `location:read` implies it, so a role that already holds `location:read` (and `tenant-admin`, which holds `*`) needs no change. It is not in the viewer baseline: a role that should see installed positions but not live device locations is granted `location:installed:read` alone.

New Helm values, each off or unchanged by default:

| Value | Default | What it does |
| --- | --- | --- |
| `functionalAreas.device-management.config.callout.ocsp.enabled` | `false` | The instance switch for OCSP. With it off no responder is asked; see [OCSP](#v130-ocsp) for the other `ocsp.*` keys. |
| `functionalAreas.device-management.config.callout.ocsp.allowRequireCurrent` | `false` | Lets an anchor use `REQUIRE_CURRENT`. |
| `functionalAreas.event-sources.config.httpIngest.deviceUplinks` | `false` | Serves `POST /{instanceId}/{tenant}/devices/{token}/uplink`. |
| `functionalAreas.event-sources.config.httpIngest.maxCommandPollsPerTenant` | `256` | Held HTTP command polls per tenant per replica; one more is answered `429`. Not listed in the chart's `values.yaml`; set it if you need another cap. |
| `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled` | `false` | Sends commands to LoRaWAN devices through their network server. Not listed in the chart's `values.yaml`; set it to turn the feature on. |

#### Token audiences are enforced by default {#v130-token-audience}

The `v1.2.0` notes announced this: what [Tokens are checked against their resource](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-token-audience) counted as `would_reject` is now refused. An unset `infrastructure.tokenAudience.enforcement` means `enforce`, for both checks:

- **An API refuses a token bound to a resource it does not serve,** with `401`, the same answer as for an invalid token. An area's API accepts a token bound to nothing, to that area, or to a registered MCP server.
- **The authorization server refuses a request for a resource this instance does not serve:** `invalid_target` at the authorization endpoint and in the `client_credentials` grant, and `invalid_grant` for a refresh or an authorization code bound to a resource that is no longer served. A refresh token refused that way is spent, so the client must authorize again.

Tokens bound to nothing are not affected. That includes every sign-in from the console, the dashboard app, the C# SDK and `dcctl`, and every service token. MCP tokens are bound to the MCP server, which the chart registers, so they are not affected either. Under `enforce` the refusals are counted on the same series with `verdict="rejected"`, and the two `info` alerts, `TokenAudienceWouldReject` and `OAuthResourceWouldReject`, can fire only on an instance that set `observe`.

What a refusal looks like: a service account that asks for a token bound to one area, and uses it on another, is refused there.

```bash
TOKEN=$(curl -s -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials -d scope=read-only \
  --data-urlencode "resource=https://dc.example.com/api/device-management" \
  https://dc.example.com/api/user-management/oauth/token | jq -r .access_token)

# Accepted: the token is bound to device-management.
curl -s -o /dev/null -w '%{http_code}\n' https://dc.example.com/api/device-management/graphql \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{ __typename }"}'

# 401 under enforce: event-management does not accept a token bound to device-management.
curl -s -o /dev/null -w '%{http_code}\n' https://dc.example.com/api/event-management/graphql \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{ __typename }"}'
```

An integration that calls more than one area should leave out the `resource`: an unbound token is accepted by every area. See [Binding a token to a resource](../user-guide/service-accounts.md#binding-a-token-to-a-resource).

**Before you upgrade,** check the `v1.2.0` instance for traffic that enforcement would refuse:

- The API counter is one series per area, and its name carries the area without hyphens: `devicechain_devicemanagement_token_audience_checks_total`, `devicechain_eventmanagement_token_audience_checks_total`, and so on. Select them all with `{__name__=~"devicechain_[a-z0-9]+_token_audience_checks_total", verdict="would_reject"}`. The authorization server's counter is `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}`. Both should read zero, and the two alerts should be quiet.
- If either moves, the rate-limited warnings name the client: `Token audience is not accepted by this plane` in the area's log, and `OAuth resource is not one this instance serves` in the `user-management` log. Change the `resource` that client sends: leave it out, or use one the instance serves.

**To opt out for this minor,** set `enforcement: observe` before you upgrade, so that no pod enforces during the roll. In Helm values or a `dcctl` [values overlay](./tuning-an-instance.md#tunable) the key is `instance.config.infrastructure.tokenAudience.enforcement`. `v1.4` refuses `observe`: a service stops at startup, and the chart's values schema rejects it. Without the opt-out, old and new pods overlap during the roll, so for a few minutes a token bound to the wrong resource can be accepted by a `v1.2.0` replica and refused by a `v1.3.0` one.

:::warning Plain Helm with `instance.existingSecret` loses MCP access unless the document lists it
The chart does not write the `tokenAudience` block into a configuration you supply yourself. Under `enforce`, the `mcp` area checks at startup that its `resourceUrl` is listed in `infrastructure.tokenAudience.mcpResources`, and **refuses to start** if it is not, so AI agents lose MCP access. `dcctl upgrade` refuses such a document before anything rolls, but a plain `helm upgrade` cannot look inside your Secret. Before you upgrade, add the block to the document, with your origin and the MCP server's `resourceUrl` exactly as the `mcp` area is configured, with a lowercase scheme and host:

```json
"infrastructure": {
  "tokenAudience": {
    "areaBaseUrl": "https://iot.example.com/api",
    "mcpResources": ["https://iot.example.com/api/mcp"]
  }
}
```

Or set `"enforcement": "observe"` in the same block. Either way, **set `instance.existingSecretChecksum` to the sha256 of the new `instance` document**, or no pod is rolled and every pod keeps reading the old one.
:::

- **If you set `functionalAreas.mcp.config.resourceUrl` by hand** with an uppercase letter in its scheme or host, or to an area's own identifier such as `<origin>/api/device-management`, `helm upgrade` now fails and says why. Lowercase it or move it, or set `observe`; under `observe` the chart leaves such a value out of the list, as `v1.2.0` did.
- **Going back to `v1.2.0`** needs nothing for this: `v1.2.0` reads the same block and defaults to `observe`.

#### Alerts reach a person {#v130-alert-routing}

Every alert rule the chart ships now carries a `runbook_url` that opens its entry in the new [Alert Runbook](./alert-runbook.md). Nothing else about a rule changes: names, expressions, `for` durations and labels are as they were.

`dcctl install` and `dcctl upgrade` take `--alert-webhook`, `--alert-email` (with `--alert-smtp-host`, `--alert-smtp-from`, `--alert-smtp-user` and `--alert-smtp-password-file`), `--alert-config`, `--alert-watchdog-webhook` and `--clear-alert-routing`. The Alertmanager now reads its configuration from the Secret `dc-alertmanager-config` in the `monitoring` namespace, which `dcctl install` writes before it applies the monitoring stack. A re-run of `dcctl install` with no `--alert-*` flag keeps the routing that is there, and a first install with none writes a configuration with no receiver, which is the behaviour of earlier releases. Naming any `--alert-*` flag restates all of the routing. With `--alert-watchdog-webhook`, the always-firing `Watchdog` alert goes to that receiver every minute, so its silence tells you alerting has stopped.

An instance reports whether anything is listening in its new `AlertsRouted` condition, and `dcctl instances list` shows `ALERTS NOT ROUTED` for one that routes nowhere. The condition never makes the instance unready.

Re-run `dcctl install` once on a cluster installed by an earlier release before using the flags with `dcctl upgrade`; `dcctl upgrade` refuses otherwise, because the older Alertmanager reads no Secret of ours. `--alert-config` is checked by `dcctl` for the mistakes that would leave alerts unrouted, but it is not Alertmanager's own parser: run `amtool check-config` on the file first. See [Routing alerts to a person](./observability.md#alert-routing).

#### Commands over HTTP, and delivery evidence {#v130-http-command-pull}

A device that only speaks HTTP, such as a battery sensor that wakes, reports and sleeps, can now receive commands. Set its device type's `commandChannel` to `HTTP_PULL`, and its commands wait for the device to ask:

- `GET /{instanceId}/{tenant}/devices/{device}/commands?wait=0..30` holds the request for up to `wait` seconds (25 by default) until a command is due;
- `POST .../commands/{command}/ack` acknowledges receipt, before the device acts;
- `POST .../commands/{command}/response` answers.

The device presents its access token on every request. A device type with no channel set, or with `PUBLISH`, is delivered to exactly as before. A command for a pull device can be `deliveryMode: ONEWAY` (completed by its acknowledgement) and can set `maxAttempts`; for any other device those options are refused with `DELIVERY_OPTION_NOT_APPLICABLE`. Each `event-sources` replica holds at most `httpIngest.maxCommandPollsPerTenant` polls for one tenant (256 by default) and answers `429` past it. The C# SDK has a new `HttpCommandPuller`.

Every command now records its delivery evidence: `deliveredTime` and `deliveryEvidence` (`DEVICE_ACK` for a pull device's acknowledgement, `DEVICE_RESPONSE` for an answer on any transport). No new command status is added. See [Devices that fetch their commands over HTTP](../concepts/commands.md#http-command-pull).

- **Turn it on after the rollout.** Upgrade `device-management` first, and set `HTTP_PULL` only once every replica of every service runs this release. A `command-delivery` replica that is already upgraded while `device-management` is not yet holds the commands of every device it asks about, and releases them once `device-management` answers, so ordinary devices are delayed for the overlap, never sent to the wrong place.
- **The new route is not exposed through the chart's ingress,** the same as `/events`.

#### LoRaWAN downlink {#v130-lorawan-downlink}

A command to a device that reports through an inbound integration can now be sent to the device as a LoRaWAN downlink, queued on its ChirpStack v4 network server. This first release takes raw `hex` or `base64` payloads and sends unconfirmed downlinks; `confirmed: true` is refused, never downgraded. The link is a published connector of the new type `lorawan_downlink`, and its ChirpStack API key is the connector's sealed secret. Success means the network server queued the downlink, not that the device received it.

It is off unless `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled` is `true`; the chart then makes `command-delivery` dispatch these commands, and refuses a hand-set copy of that flag. The `connectorTypes` query lists one more value. The first time it is turned on, a command dispatched in the seconds before the adapter starts times out. See [LoRaWAN downlink](../guides/inbound-integrations.md#lorawan-downlink).

#### Signed outbound webhooks {#v130-signed-webhooks}

The `httpCall` rule action and the webhook notification channel can sign what they send, per the open Standard Webhooks specification. A signed delivery carries `webhook-id`, `webhook-timestamp` and `webhook-signature`; the body is unchanged, and an unsigned delivery is exactly what it was. A signing secret is generated by the platform, shown once and never readable again, and can be rotated with an overlap during which deliveries carry both signatures. A delivery that should be signed and cannot be is refused, never sent unsigned. See [Signing outbound webhooks](../guides/signed-webhooks.md).

- **Enable signing after the rollout.** An older `outbound-connectors` or `notification-management` replica ignores `signingSecretRef` and `signing`, and delivers unsigned.
- **`webhook-signing/` is reserved.** A rule whose Bearer credential handle sits under it is refused at publish; move the handle first.
- **A new secret takes up to a minute to reach every `httpCall` delivery,** because the connectors service caches secrets.

#### The HTTP event endpoint answers 401 {#v130-http-events-401}

`POST /{instanceId}/{tenant}/events` used to answer `202 Accepted` for requests that could never be stored: a tenant name with a typo in it, or a credential that would not authenticate. They were refused later, and the device had been told its data was safe. They are now refused at the door:

- **`401`** for an unknown tenant and for a credential that will not authenticate (unknown, expired, a wrong secret, another device's, or missing where the instance requires one). Every cause gets the same status, headers and body, `{"error":"unauthorized"}`, so the endpoint does not reveal which tenants or devices exist. Operators tell the causes apart on `total_http_ingest_refused{source,reason}` and in a sampled log line.
- **`400` stays `400`** for a malformed payload. A body that is valid JSON but refused now carries a JSON body whose `errors` name each offending value with a JSON Pointer; `message` is the sentence the plain-text body always carried. A body that is not JSON keeps its plain-text `400`.
- **`X-DC-Event-Id`** on a `202`, and on a refused document's `400`, is a receipt for the request.

The check fails open: if the services it asks are slow or unreachable, the request is accepted as before and judged later. A credential that fails five times in a row is not asked about again for a while (1 second, doubling to 60), so the `401` cannot be used to guess secrets. The cached answer is not an authority: `device-management` still authenticates every event, so a credential that was just disabled is accepted at the door for at most 30 seconds and then refused as before. The refusals made at the door are counted and logged, but they are not failed-event records.

#### A device's own payload, decoded {#v130-device-uplinks}

A device that sends a binary or vendor JSON payload of its own, rather than the DeviceChain event document, can post it to `POST /{instanceId}/{tenant}/devices/{token}/uplink`, and the payload decoder on its device type's profile reads it. The device presents its own credential in the `X-DeviceChain-Credential-*` headers, and it is proven before anything else: a caller without it gets one `401`, whether or not the device exists or has a decoder. The decode, its budget, its health and its samples are the ones the inbound integration route uses. A device type with no decoder is answered `422`.

The route is off unless `functionalAreas.event-sources.config.httpIngest.deviceUplinks` is `true`. Turn it on only after every `device-management` replica runs this release: an older replica refuses such an uplink and records it as a failed event after the device was answered `202`.

#### OCSP for certificate connects {#v130-ocsp}

A trust anchor can now ask the certificate authority's OCSP responder whether a certificate is still good, as a second revocation source beside the [revocation lists](../concepts/device-certificates.md#revocation) you upload. It is off everywhere by default: the instance switch `functionalAreas.device-management.config.callout.ocsp.enabled` is `false`, and every anchor's new `ocspMode` is `OFF`.

- **`ENFORCE_KNOWN`** refuses a certificate the responder says is revoked and admits one whose status it cannot learn.
- **`REQUIRE_CURRENT`** refuses any certificate whose status it cannot confirm. It also needs `callout.ocsp.allowRequireCurrent: true` and a responder URL on the anchor (`ocspResponderUrl`).
- **What it costs.** OCSP runs only after the chain, expiry, revocation-list and manual checks have passed, so a certificate already refused costs no fetch. Good answers are cached; a revoked answer refuses the connect and is never cached. The other keys under `callout.ocsp` (`fetchDeadline`, `maxInFlight`, `maxAge`, `failureTtl`, `cacheEntries`, `maxResponderHostsPerTenant`) bound the fetches, and their defaults are in the chart's `values.yaml`.
- **What it does not do yet.** A revoked answer refuses each connect but is not stored as a revocation, and it does not end sessions that are already open; upload a list or revoke the certificate for that.
- **New results and metrics.** `callout_x509_total` gains the results `ocsp_revoked` and `ocsp_unavailable`, and five new `x509_ocsp_*` series report fetches, the cache, the breaker and the connects admitted without an answer.

**Switch anchors to `ENFORCE_KNOWN` or `REQUIRE_CURRENT` only after the rolling upgrade has completed.** Until then, older replicas do not check OCSP, so a certificate one replica refuses could still be admitted by another. With the instance switch off, an anchor set to `REQUIRE_CURRENT` refuses new certificate connects, and the API refuses to switch an anchor on.

#### MCP tool results carry their provenance {#v130-mcp-provenance}

The MCP tool catalog (tool names, inputs and result shapes) is not covered by the compatibility promise. The [versioning policy](./versioning-policy.md) now lists it under what can change in a minor release, and this release changes it.

Every read tool keeps tokens and platform-computed values at the top level and moves free text under `untrusted`, as `{ "value": ..., "source": ... }`, where `source` is `device-reported`, `operator` or `agent`. The fields that move are a device's `name`, `description` and `externalId`, a metric's `name` and `unit`, an alarm's `alarmKey`, and a command's `name` and `error`. A client that reads them must read them from there. The classes a result contains are listed under `_meta["io.devicechain/provenance"]`, and every tool description says that text under `untrusted` is data, never instructions. See [What an assistant sees](../concepts/mcp.md#what-an-assistant-sees).

Also new, and additive: OAuth access tokens carry a `sid` claim (the grant session), the audit journal records the `client_id` and session of the token that made each change, and the MCP audit log line moves to `schema: 2`, which adds `scopes`.

#### The service-token authority ceiling is observed {#v130-service-token-ceiling}

The endpoint that mints service tokens now checks each request against the authorities its caller is expected to need, and counts the verdict on `devicechain_usermanagement_service_token_checks_total{caller,verdict}`. It refuses nothing and mints exactly as before. A non-zero rate of `over_ceiling`, `wildcard` or `unknown_caller` names a caller that a later release that enforces would refuse; none is expected today.

#### Product as code (preview) {#v130-product-as-code}

`dcctl product export <dir>` writes a tenant's product as files: device types, profiles with their metrics, calculated metrics, payload decoders with their test vectors, commands, detection rules and canvas graphs, dashboards, connectors and integrations. `dcctl product plan <dir>` prints what applying the files would change and writes nothing (`--fail-on-changes` makes drift an exit status), and `dcctl product apply <dir>` converges the tenant to the files and publishes what changed. Secrets never appear in a bundle: a connector names its credential by handle (`env:NAME`), and every bundle is scanned for values that look like secrets. Only what the bundle names is touched.

This is a **preview** surface: the command tree and the bundle format (`product.devicechain.io/v1alpha1`) can change in a minor release, and `dcctl` refuses a bundle version it does not know. It runs entirely in `dcctl`, through the served tenant APIs, so nothing changes on the server. See [Product as Code](../guides/product-as-code.md).

#### Rule tests (preview) {#v130-rule-tests}

`dcctl test <dir|file>` runs deterministic unit tests for detection rules. A `scenario.yaml` describes devices and their readings (constant, a bounded random walk, a step schedule, or a CSV or NDJSON file) with faults (gaps, duplicates, reordering, offline windows) and a seed, and `*.test.yaml` files state what must happen: an alarm raised or cleared within a time, never raised, a command or webhook requested, and so on. `dcctl test` prints a report, can write JUnit XML (`--junit`), and exits `0` when every expectation held, `1` when one failed, and `2` when something could not be judged. A run that cannot answer is never green.

It drives a new query, `evaluateRules` (event-processing tenant API, `device:read`), which runs a profile's published rules, or a draft, over the events in the request, in event time with live's lateness, and returns the edges, the actions they would request and why any result is partial. It executes nothing and touches no stream. The file formats (`devicechain.io/ruletest/v1alpha1`), the query and the command are in the **preview** tier.

- **`devicechain_eventprocessing_detect_late_samples_total` reads higher.** Late readings dropped by a closed tumbling window or a closed session are now counted, as every other rule kind's were. Detections do not change; a dashboard or alert you tuned against the old rate needs re-baselining.

#### Event usage per tenant {#v130-usage-metering}

`event-management` keeps a daily record of what each tenant used: the events it ingested, and the devices that sent them (distinct, peak in an hour, and the hourly average). An operator reads it with the `tenantEventUsage(tenant, from, to)` query on the new instance-scoped `/admin/graphql` endpoint of `event-management`, which needs an identity token holding `usage:read`. The record reports usage and enforces nothing.

- **Events are counted on the day they were received,** not the day they are dated, so a late or backfilled reading never revises a finished day, and a redelivered duplicate is counted once.
- **An hour is final 10 minutes after it closes,** and a final hour is never rewritten. A day reads `complete` once all 24 of its hours are final. As a result, **an event stored more than 10 minutes after the end of the hour in which it was received is not counted.**
- **The record starts on the UTC day the upgraded service first runs.** Earlier days read as not complete. Events received after the upgrade but dated inside chunks that were already compressed at that time are not counted.
- **The upgrade builds an index on the events table** one chunk at a time, so it does not hold writes to the whole table, but on a large store the `event-management` migration takes longer than usual.

#### Campaigns {#v130-campaigns}

A campaign sends a series of commands, with a delay between steps, to the devices of an entity group on a schedule: a cron expression in a named time zone, calendar dates and times, or sunrise or sunset plus an offset, computed for each device from its [installed position](#v130-installed-position). Holidays suppress a firing; they never move it. Daylight-saving changes follow one rule: a time inside a spring-forward gap fires at the jump, and a repeated hour fires once. Each firing is tracked per device (pending, dispatched, refused or skipped, with a reason), and `retryCampaignFailed` sends the cycle's command again to the devices whose command did not succeed. Commands go through the usual batch path, so maintenance windows still apply. Every replica runs the scheduler, and the database decides which one sends each command, so nothing is sent twice.

- **A campaign cannot be edited.** Delete and recreate it; a recreated campaign sends nothing that fell due before it was created.
- **Targets are groups.** Use a dynamic group to select an area's devices.
- **Solar triggers need the rollout to finish.** Until no `user-management` replica of `v1.2.0` is left, planning a sunrise or sunset firing reports an error and retries.

#### Installed position {#v130-installed-position}

A device or an asset that does not move, such as a streetlight, a parking space or a cabinet, can now have an installed position: `setInstalledPosition` and `clearInstalledPosition` (`device:write`), the optional `installedLatitude`, `installedLongitude`, `installedElevation` and `installedAccuracy` columns of a fleet import, and the `installedPosition` field on `Device` and `Asset`. Reading it needs the new `location:installed:read`, which `location:read` implies. It never falls back to a position the device reported. `positionWithheld` on every device and asset tells a caller that it may not see the position, without revealing whether one exists. An area can name its boundary fence (`boundaryFenceToken`); a fence an area names cannot be deleted.

#### The notification delivery ledger {#v130-delivery-ledger}

Each attempt to send an alarm's notification through a channel now writes one row: what triggered it, the policy, the channel, the recipients, the outcome (`SENT`, `FAILED`, `REFUSED` or `NOT_ATTEMPTED`), the attempt number, the latency and a classified error code. Read it with `notificationAttempts` (`notification:read`); `NotificationChannel.deliveryHealth` gives each channel's last success, last failure and consecutive failures. Delivery URLs, which can themselves be credentials, are never stored. Rows are kept for 7 days by default.

The ledger never delays a page: rows are written in the background from a bounded queue, and a full queue drops the row and counts it on `devicechain_notificationmanagement_ledger_writes_total{outcome}`. So a row proves an attempt happened; a missing row proves nothing.

#### Alarm dispositions {#v130-alarm-dispositions}

`setAlarmDisposition` labels the current cycle of an alarm as `TRUE_POSITIVE`, `FALSE_POSITIVE` or `EXPECTED`, with an optional note (`alarm:write`). A label belongs to one cycle: when the alarm is raised again, the new cycle starts unlabelled and earlier cycles keep theirs. `Alarm.disposition` reads the current cycle's label, and `alarmDispositions` lists every labelled cycle. Labelling changes nothing else about the alarm. A customer-limited member reads the labels of cycles it could see and cannot write one.

#### Semantic bindings {#v130-semantic-bindings}

A tenant can attach terms from a vocabulary to the model it already has: `semanticTypes` on device, asset, area and customer types, `semanticPredicate` and `semanticInverse` on relationship types, and `semanticProperty` and `semanticPointClass` on metrics. The vocabularies shipped are Brick 1.4.4, SAREF 4.1.1 with SAREF4CITY and SAREF4BLDG, the Smart Data Models for street lighting, parking and environment, and the NGSI-LD core terms. A tenant enables the ones it uses, and may add prefixes of its own, with `setSemanticContext` (`device:write`). Every term is checked for existence and kind, stored as a full IRI, and refused with `INVALID_ARGUMENT` naming the term; a context change that would leave a stored binding unexplained is refused with `SEMANTIC_CONTEXT_IN_USE`. `thingModel(includeSemanticTypes: true)` adds each metric's semantic property and point class to its property's `@type`; by default its output is unchanged.

- **During the rolling window,** an older pod that publishes a profile or an asset type writes a snapshot without the bindings. The draft keeps them, and the next publish from an upgraded pod freezes them.

#### The console searches devices and triages alarms {#v130-console}

The device search and the device-type search take one new optional argument each, `nameContains`: a case-insensitive substring match on the name or the token, where `%` and `_` match themselves and blank text matches everything. A client that does not send it sees the same results as before. In the console, the devices list has a name search and a device-type filter, both kept in the page address, and the type pickers on the device, bulk-create, import and provisioning-profile forms search the server as you type instead of reading the first 1,000 types.

The alarm list links each alarm to its device, prints values with the unit the device's profile declares, acknowledges several selected alarms in one request, and keeps its filters in the page address. A device has an Alarms tab, and the home page counts active alarms by severity and devices online and offline. These are console changes only.

#### A device clock that runs ahead no longer moves other tenants' detection {#v130-detect-watermark}

Detection keeps one logical clock for the whole instance, and it used to follow the newest event time it had seen. The platform accepts event times up to five minutes ahead of the server clock, so one device with a fast clock could carry that shared clock ahead of real time. Every tenant's absence timers then fired early, and windowed readings from devices with correct clocks were dropped as late. From this release a message moves the shared clock no further than the time the broker stored it, plus the configured lateness. The event keeps its own time, so its readings are still windowed and ordered at the instants the device reported, and the replay preview applies the same bound.

- **Detection results change only in the case that was wrong.** A device whose clock is within the lateness of the broker's (5 seconds by default) behaves as before. For a device running further ahead, other devices' absence alarms now fire at their true deadline, and a fast device's own timers wait for real time to reach them.
- **Snapshots carry a frontier version.** A detection snapshot written by this release records it, and restoring an earlier snapshot still works. In the first moments after the upgrade the restored clock can still be as far ahead as it was; it corrects itself as real time catches up, and the service logs a warning when it restores such a snapshot. During a rolling upgrade, a pod of the previous release that takes over writes snapshots without the version and ignores it when reading, so nothing needs doing.
- **This does not cover the opposite case.** A device that uploads old readings (store and forward) can still have them dropped as late by windowed rules, because the shared clock has moved on. Per-device clocks are not part of this release.

#### Edge agents {#v130-edge}

Nothing here needs action; both are off by default and change nothing until an agent is configured for them.

- **An agent can keep an event until the platform has accepted it.** With `uplink.retireOn: accept`, the agent deletes a buffered event only when the platform reports that it was durably accepted, and offers it again if the platform shed it over the tenant's ingest ceiling. The default, `puback`, behaves as before. Upgrade the platform before you switch an agent to `accept`: an agent in that mode cannot connect to an older platform, and the connect error names `uplink.retireOn`. When its spool nears its budget, an agent can also shed buffered events by priority class (`local.priority.*`). See [Retiring events on the platform's acceptance](./edge-services.md#retire-on-accept).
- **The agent side of command downlink to devices behind it** ships with `commands.enabled` off. The platform does not yet route gateway commands, so it has no effect in this release.

#### Documentation, sample hosts and agent notes {#v130-docs-hosts}

The .NET and Unity SDK documentation and the `DeviceChainClient.Origin` doc comment used `demo.devicechain.io` as their example host, which does not resolve; they now use `devicechain.example.com`. The SDK has no default host, so no behaviour changes. A new [Device APIs at a glance](../reference/device-apis.md) page gives one command for each way a device reaches the platform, and the `@devicechain/client` and `@devicechain/widget-sdk` packages and the .NET SDK each carry an `AGENTS.md` for coding agents. A new [Security process](./security-process.md) page describes how vulnerabilities are handled (the acknowledge-and-triage target is now 24 hours) and what each release publishes.

#### Other changes in this release {#v130-other-changes}

Nothing here needs action.

- **LwM2M: device information from devices that answer in TLV.** `lwm2m.readDeviceInfo` and the device-information harvest at registration read only plain-text answers, so a client that answers a single-resource read in LwM2M TLV, as Eclipse Leshan does, had nothing recorded. TLV answers are now decoded.
- **The [transport matrix](../reference/transport-matrix.md) is generated from a conformance run** in which third-party clients (Eclipse Paho, curl, Eclipse Leshan and Eclipse Tahu) drove a real instance, and every cell is judged by what the platform stored.
- **`dcctl install` no longer requires the `helm` command.** The chart is installed through Helm's library inside `dcctl`; `tofu` (or `terraform`) is still required.
- **`dcctl bootstrap --values` no longer fails on a fresh instance** with "changed while the values overlay was being recorded". A status write by the operator moved the instance's version under it; `dcctl` now retries when only the status moved, and still refuses when anything else changed. The same applies to recording the front end and the event-envelope switch.

#### Schema changes and going back {#v130-going-back}

The following migrations are appended. Every new column is nullable and every new table starts empty, so a service from `v1.2.0` that runs against the new schema writes rows it can still store.

- **OCSP on trust anchors** (`device-management`, migration `20261113100000`). Two nullable columns on `device_trust_anchors`: `ocsp_mode` and `ocsp_responder_url`. An empty mode reads as `OFF`.
- **HTTP command channel** (`device-management`, migration `20261114120000`). One nullable column, `command_channel` on `device_types`. Empty reads as `PUBLISH`.
- **Delivery evidence** (`command-delivery`, migration `20261114120100`). Five nullable columns on `commands`: `delivered_time`, `delivery_evidence`, `delivery_mode`, `max_attempts` and `delivery_attempts`.
- **Audit journal identity** (every area). The audit journal table `audit_events`, which each area keeps beside its own tables, gains four nullable columns: `client_id`, `session_id`, `on_behalf_of` and `proposal`. They are added at start-up, as the table itself always has been, rather than by a numbered migration.
- **Event usage** (`event-management`, migrations `20261107000000` and `20261108000000`). Five new tables, `tenant_usage_daily`, `tenant_usage_hourly`, `tenant_usage_devices`, `tenant_usage_ledger` and `tenant_usage_state`, and a BRIN index on `events.processed_time`, built one chunk at a time.
- **Notification delivery ledger** (`notification-management`, migration `20261108000100`). One new table, `notification_attempts`, with three indexes.
- **Installed position** (`device-management`, migration `20261112120000`). Six nullable columns on `devices` and on `assets` (`installed_lat`, `installed_lon`, `installed_elev`, `installed_accuracy`, `installed_source` and `installed_updated_at`), one on `areas` (`boundary_fence_token`) and a partial index on it.
- **Alarm dispositions** (`device-management`, migration `20261114090000`). One new table, `alarm_dispositions`, whose rows are removed with their alarm.
- **Campaigns** (`command-delivery`, migration `20261116120000`). Three new tables, `campaigns`, `campaign_cycles` and `campaign_elements`, with their indexes.
- **Semantic bindings** (`device-management`, migration `20261120120000`). Nullable columns for the bindings on the type, relationship-type and metric tables, and one new table, `semantic_contexts`.

**Nothing in a database needs undoing to go back to `v1.2.0`:** the new tables, columns and indexes stay in place and the `v1.2.0` services ignore them. Before going back, set every trust anchor's OCSP mode back to `OFF` and every device type's `commandChannel` back to `PUBLISH`, and turn off `httpIngest.deviceUplinks`, `lpwanDownlink.enabled` and webhook signing, since `v1.2.0` does not know them. Roll forward when you can: a failed upgrade is fixed by running `dcctl upgrade` again.

#### Known issues {#v130-known-issues}

- **Fail-closed MQTT acknowledgement is off by default,** as in `v1.2.0`. The [MQTT transport](../reference/transport-matrix.md#mqtt--the-platform-broker) page states the measured limits with it off and on.
- **A change to the broker's configuration still disconnects the devices the auth callout admitted,** until the broker library carries the upstream fix. A certificate renewal does not.
- **`eventSources.ingestMetering: lagCredit` is still not available,** and is refused at startup; see [Ingest metering](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-ingest-metering). It arrives, opt-in, in [`v1.4.0`](#v140-ingest-metering).
- **OCSP does not yet store what it learns or end open sessions,** as described under [OCSP for certificate connects](#v130-ocsp).
- **An event stored more than 10 minutes after the end of the hour in which it was received is not counted in the tenant's usage record,** and neither is an event received after the upgrade but dated inside a chunk that was already compressed. See [Event usage per tenant](#v130-usage-metering).

### v0.27.0 — devices sign in by certificate, and Sparkplug commands work {#v0270-upgrade}

`v0.27.0` is an in-place upgrade from `v0.26.0`: `dcctl install` for the cluster, then one `dcctl upgrade` for
each instance, as described [for v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Give `dcctl install` the settings the cluster was
installed with, such as `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as the new `--version`. Coming from
`v0.25.2` or earlier, read the [`v0.26.0` notes](#v0260-upgrade) first and follow them with `--version v0.27.0`.

```bash
dcctl install local --version v0.27.0
dcctl upgrade local devicechain --version v0.27.0 --dry-run
dcctl upgrade local devicechain --version v0.27.0
```

The release lets devices [authenticate with an X.509 certificate](../guides/device-x509.md), makes Sparkplug B commands usable, renews the broker's certificate without dropping device sessions, and gives customer users area and asset dashboards. What needs action, in the order to do it:

- **If you install the chart without `dcctl` and the broker's auth callout is on, give it the broker's system login.** The callout now checks that a certificate connect was requested by a live broker server, and it asks the broker's system account for that list. Without `infrastructure.nats.auth.sysUser` and `sysPassword`, device-management refuses to start. Set them, or set `callout.issuerPinDisabled: true` to run without the check (the service logs a warning). A `dcctl` install already has the login. See [Pinning requests to the broker](./device-certificates.md#issuer-pin).
- **A `dcctl bootstrap` run again over an instance that holds Sparkplug sources or LwM2M identities must say what to do with them.** (On a running instance `dcctl bootstrap` already refuses to rebuild; this covers the retry of a bootstrap that did not finish.) It now refuses unless you give the same file again (`--sparkplug-source`, `--lwm2m-identities`) or withdraw them on purpose (`--withdraw-sparkplug-sources`, `--withdraw-lwm2m-identities`). Before this release a bootstrap re-run without the flag removed the credentials silently. `dcctl upgrade` keeps them, as before.
- **Rotate your LwM2M pre-shared keys after upgrading.** See [the LwM2M keys leave the Helm release](#v0270-lwm2m).
- **Settings you added by hand to the `lwm2m-ingest` block of the release are no longer carried** by an upgrade. Put them in a [values file](./tuning-an-instance.md) and give it with `--values`.
- **If you set `extraSecrets` in the chart values, know that its checksum changed.** `checksum/extra-secrets` now covers each Secret's name and key names, never the values. Changing only a value no longer restarts the pods; restart them yourself when a value changes.
- **A typed GraphQL client must accept `null` for `ProvisioningProfileSecretResult.issuedSecret`.** A provisioning profile that names a trust anchor has no secret. Every profile that has one still returns it once, as before.
- **Roll forward from this release.** See [Going back](#v0270-going-back).

Two migrations run, one per first start of the service that owns it, and each can be run again if it is interrupted: in `device-management`, a trust-anchor table and columns for the anchor on credentials and provisioning profiles; in `user-management`, two nullable columns on the tenant purge record that hold when the data was established as erased and when that was last checked. Neither rewrites a row.

#### Sparkplug B commands work, and `dcctl` takes a Sparkplug source {#v0270-sparkplug}

In `v0.26.0` a command key with a dot in it was refused, and the Sparkplug command types (`sparkplug.write`, and the rebirth and reboot commands) are named that way, so no Sparkplug command could be created. A key may now be namespaced with dots, each segment following the same rule as before. Sparkplug commands stay off until you turn them on for a source, as in `v0.26.0`.

- **A refused command says why.** A command refused before it reaches the node (`FAILED` or `NOT_DELIVERED`) now has a machine-readable reason as its stored response, for example `commands_not_enabled`, `node_offline`, `node_rebirthing`, `metric_not_writable` or `reboot_not_allowed`. The console explains each. Anything you wrote that matched the wording of the old error text should match the reason code instead.
- **A node born before its device type existed is asked to rebirth.** The platform remembers the node and asks it to rebirth 10 seconds after the failed registration, then on a doubling interval up to a minute, until it registers. Two new metrics, `birth_recovery_pending` and `birth_recovery_rebirths_total`, show it.
- **`dcctl bootstrap` and `dcctl upgrade` take `--sparkplug-source <file>`.** The file lists the sources, the broker password stays in a Secret that `dcctl` writes itself, and later upgrades carry both. A bootstrap with the flag turns the area on; an upgrade with it refuses when the area is not running and names `dcctl enable-area`. See [Giving `dcctl` a Sparkplug source](./edge-services.md#sparkplug-sources). A `--values` overlay still cannot set a source.

#### The LwM2M keys leave the Helm release {#v0270-lwm2m}

Until now `dcctl` put every LwM2M device pre-shared key in the Helm release's values, where `helm get values` printed it and Helm's history kept it for every revision. `dcctl` now writes the keys to the Secret `dci-<instance>-lwm2m-identities` itself, outside the release, and the release carries only the Secret's name and a stamp that is not derived from any key.

- **The first upgrade moves the keys.** It reads them out of the release once, writes the new Secret before anything else changes, drops the old `extraSecrets` entry and runs the Helm upgrade. Devices keep the keys they have. The pods that are still running hold them in their environment until they restart.
- **Rotate the keys afterwards.** The revisions Helm recorded before this upgrade (it keeps up to ten) still hold the old values, and so does anything that backed up the release. Run `dcctl upgrade local <instance> --lwm2m-identities <file>` with new keys; a file given again rewrites the Secret and restarts the pods. Until the old revisions age out, restrict who can read Secrets in the namespace where Helm stores its releases (`default`).
- **`dcctl upgrade` gains `--lwm2m-identities` and `--withdraw-lwm2m-identities`,** and the Sparkplug Secret works the same way. A Secret `dcctl` did not write is never touched.
- **A shrink or a delete waits for the release.** `dcctl` adds keys before the release and removes the ones nothing references only after it succeeds. A failed upgrade leaves the old keys in place for the running pods, and the next run finishes the job. If the final step fails after a successful release, the run warns with the exact command and does not fail.

#### Certificate authentication {#v0270-certificates}

Devices can authenticate to the platform broker with an X.509 client certificate, either pinned to a device you created or registered on first connect through a certificate provisioning profile. Nothing is trusted until you add a trust anchor, so an upgrade changes no device's behaviour. Token and password devices connect as before. Read [Upgrading an instance that already runs](./device-certificates.md#cutover) before you add the first anchor, and [Connecting with a certificate](../guides/device-x509.md) for the steps.

- **The broker's request for a certificate no longer names an accepted issuer.** A Go client or the simulator withheld a certificate whose issuer the broker's list did not name; the broker now sends no list, so a device presents the certificate it has.
- **Every certificate connect is checked against the live broker servers.** See [Pinning requests to the broker](./device-certificates.md#issuer-pin). The new alert `DeviceX509IssuerPinUnavailable` pages on a sustained outage, not on a broker roll.
- **Three alerts and a latency histogram arrive with the chart.** `DeviceX509ChainFailuresSustained`, `DeviceX509JitRefusedSustained` and `DeviceTrustAnchorsExpiringSoon` are quiet on an instance with no trust anchors. `callout_duration_seconds{path,result}` and the warning `DeviceCalloutLatencyHigh` cover every path through the callout.

#### The broker certificate renews without dropping devices {#v0270-broker}

- **A renewed TLS certificate no longer disconnects MQTT sessions.** The broker applies a certificate that changed under the same authority to new connections without reloading its configuration, so a device that is connected stays connected. In a test, 30 held sessions behind the auth callout survived a rotation and the next connection was served the new certificate.
- **`dcctl upgrade` renews a broker leaf certificate without rolling the broker** when expiry is the only reason: the authority is unchanged and the broker already runs the replica count the certificate is issued for. It writes the Secret, then waits up to five minutes for every broker pod to report the new expiry. If that cannot be confirmed, it warns and rolls the broker, so an old certificate is never left serving behind a green renewal. A change of authority, of names or of replica count still rolls it.
- **A change to the broker's configuration still reloads it,** and a reload still drops the sessions of devices the auth callout admitted, until the broker library carries an upstream fix that is not in a release yet. `dcctl upgrade` rolls the broker for any upgrade that changes its image or configuration.
- **The broker library is nats-server 2.15.2,** a security release; among its fixes the auth callout now respects the connection types it allows. `dc-nats-server` is built on it. The control image for going back to the upstream broker is `nats:2.15.2-alpine`; the setting to use is `server_image_tag = "2.15.2-alpine"`.
- **After an upgrade rolls the broker, `dcctl` evens out leadership.** The server that restarted last used to lead no streams, and publish-acknowledgement p99 roughly doubled until leadership moved. The last step of `dcctl upgrade` now moves stream and consumer leaders to even the servers out and prints the leaders per server before and after. It is best effort: a failure is a warning, never an upgrade failure. [`dcctl broker rebalance <instance>`](../reference/dcctl.md#dcctl-broker-rebalance) does the same on demand, and the new alert `BrokerLeadershipImbalanced` fires when a server leads fewer than half the average.

#### Ingress rollouts keep requests {#v0270-ingress}

Behind a cloud load balancer, Traefik and ingress-nginx pods now wait before they leave (`drain_seconds`, 40 by default) and a new pod must be ready for `min_ready_seconds` (15) before the next is replaced, so a rollout no longer drops requests while the load balancer's health check catches up. The values are module variables of the OpenTofu controller modules; 0 turns either off. On GKE, a request loop of one a second through a cloud load balancer during Traefik rollouts saw no failed requests, where the previous measurement saw failures. A local cluster, which has no cloud load balancer, is unchanged. On a cloud cluster, the first `dcctl install` that applies this rolls each controller once, with the longer drain. See [what changes at the edge](./ingress-cutover.md#ingress-cutover-differences).

#### Customer users and the console {#v0270-customers}

- **A customer user sees the areas and assets their visible devices are in.** The rule is in the store, beside the tenant predicate: an area or asset appears while a device the caller can see has a tracked relationship to it, and `anchorDevices` lists the devices on it. Area and asset names are visible to anyone who can see a device in them; their other fields are withheld. Nothing on the relationship graph itself opens.
- **Alarms and event history can be filtered by an anchor.** `alarms`, `alarmCountsBySeverity`, `alarmCountsByDeviceType` and the `alarmStream` subscription take an optional `anchor` (a customer, an area or an asset), and the answer is limited to the caller's own scope. An alarm stays under the area its device was in when it was raised. A customer anchor outside the caller's scope is refused. No migration is needed.
- **The console hides what the server refuses.** A customer user is not offered new device, import, command cancel or dashboard write actions, and a stat card that shows the latest reading is filled from the newest stored value instead of waiting for the next one.

#### Tenant deletion says when the data is erased {#v0270-tenant-erasure}

A tenant deletion record gains `erased`, `erasedAt` and `lastVerifiedAt`. The claim is made only when every store has been clean for the settle window, it is withdrawn if any store regresses, and an empty ledger reads as "still being checked", never as erased. `dcctl tenant-deletion status <token> [--wait] [--until erased|complete]` exits non-zero until the data is erased. The metric `tenant_purge_erased_total` counts the claims. See [Knowing that the data is erased](./tenant-deletion.md#erased-signal), and [How long erasing a large tenant takes](./tenant-deletion.md#measured) for the measured run and what it does not cover.

#### Fail-closed MQTT acknowledgement stays opt-in {#v0270-fail-closed}

The fail-closed MQTT acknowledgement (`--mqtt-fail-closed`) stays off unless you turn it on, as in `v0.26.0`, and it stays an option through 1.0. The [MQTT transport](../reference/transport-matrix.md#mqtt--the-platform-broker) now states its measured limits and the loss bounds with it off and on. Nothing changes for an instance that does not set it.

#### Going back {#v0270-going-back}

Roll forward from this release, not back. The two new migrations add a table and columns that `v0.26.0` does not know, and an LwM2M instance has its keys in a Secret that `v0.26.0` does not read. A failed upgrade is fixed by running `dcctl upgrade` again.

#### Known issues {#v0270-known-issues}

- **A change to the broker's configuration still disconnects the devices the auth callout admitted,** until the broker library carries the upstream fix. A certificate renewal does not.

### v0.26.0 — devices enrol themselves, and the front end moves to Traefik {#v0260-upgrade}

`v0.26.0` is an in-place upgrade from `v0.25.2`: `dcctl install` for the cluster, then one `dcctl upgrade` for
each instance, as described [for v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Give `dcctl install` the settings the cluster was
installed with, such as `--kube-context`, `--ha`, `--compact` and `--database-node-selector`, as well as the new `--version`. Coming from
`v0.25.1` or `v0.25.0`, read the notes of the releases in between too.

```bash
dcctl install local --version v0.26.0
dcctl upgrade local devicechain --version v0.26.0 --dry-run
dcctl upgrade local devicechain --version v0.26.0
```

On a cluster that holds two default-profile instances, add `--max-connections 700` to this `dcctl install`.

The release ships [device self-registration](../guides/device-self-registration.md), [inbound integrations](../guides/inbound-integrations.md) for LoRaWAN network servers and webhooks, an opt-in command downlink for Sparkplug B nodes, [two-factor authentication](#after-v0252-mfa), and the move from ingress-nginx to Traefik. It removes the MQTT body-credential strip. What needs action, in the order to do it:

- **A cluster that holds two default-profile instances needs a larger database connection budget before the upgrade.** The budget a new cluster gets is now `700`; a cluster installed with the earlier default keeps `600`, and the upgrade of the second instance can be refused. Re-run the `dcctl install` the cluster was installed with, adding `--max-connections 700`: for example `dcctl install local --max-connections 700`, plus the flags it was installed with, such as `--kube-context`, `--ha`, `--compact` and `--database-node-selector`. See [the update-management note below](#v0260-updates) and [the connection budget](./bootstrap.md#connection-budget).
- **Delete `functionalAreas.event-sources.config.capture.bodyCredential` if you set it,** and remove `--mqtt-body-credential` from scripts. See [the body-credential strip is removed](#after-v0252-body-credential).
- **Check anything that scripts provisioning profiles,** and rotate each existing profile's secret. See [Provisioning profiles](#self-registration-profiles).
- **API clients that sign in must handle `MFA_CHALLENGE`.** See [Two-factor authentication](#after-v0252-mfa).
- **If you move an existing install to Traefik, check the Gateway's certificate before you drop the Ingress objects.** An instance upgraded across the issuer switch can be left with a `devicechain-tls-gateway` certificate that the instance's own certificate authority did not issue. Check its issuer and reissue it if it is not the instance CA; the check is a step of [the cutover guide](./ingress-cutover.md#ingress-cutover-steps), before the Ingress objects are dropped.
- **The monitoring stack is upgraded to a new major line** (Prometheus 3). See [The monitoring stack](#v0260-monitoring).
- **Roll forward from this release, not back.** The device-management migration that replaces provisioning-profile and device-claim secrets with digests cannot be undone, and the capture reader moves to a new durable consumer (see [Messaging](#v0260-messaging)).

Six migrations run, one per first start of the service that owns it, and each can be run again if it is interrupted: in `device-management`, device self-registration, enrolment-secret digests, integrations and device aliases; in `user-management`, the second factor and the self-registration governance dimension. At start, `device-management` also converts every stored provisioning-profile secret and device-claim secret to a keyed digest; a claim that is open at the upgrade keeps working with the secret its owner was given.

Until now a provisioning profile could be created and edited, but nothing read it: no device could register through one. So nothing that worked yesterday stops working because a fleet used it. What changes is how profiles are made and what they need.

The release also adds [two-factor authentication](#after-v0252-mfa), which changes how API clients sign in to an account that turns it on.

#### Provisioning profiles {#self-registration-profiles}

- **The server generates the provision secret.** `createProvisioningProfile` no longer takes `provisionSecret`. It returns `{ profile, issuedSecret }` where it used to return the profile, and `issuedSecret` is shown **once**. A script that selects `token` from the result must now select `profile { token }`. `updateProvisioningProfile` no longer takes `provisionSecret` either. `rotateProvisioningProfileSecret(token)` is the only way to replace a secret. Both are breaking: regenerate any client built against the old schema and check your scripts. The new operations are listed under [Provisioning profiles](../reference/graphql-api.md#provisioning-profiles).
- **Existing profiles are refused until you rotate them.** A secret an operator chose is not accepted by the new endpoint, because only a secret the server generated is. Every profile that exists at the upgrade answers `401 PROVISIONING_FAILED` to a registration, and the console marks it **Rotate required** (`secretIssued` is `false` in GraphQL). Rotate it, copy the new secret, and give it to the fleet. Nothing about the profile's other settings changes. **This upgrade cannot be undone.** Provisioning-profile and device-claim secrets are replaced by digests in the database, and that migration refuses to roll back (the plaintext is gone, so dropping the digests would destroy every secret). Restore from a backup if you must go back to an earlier release.
- **The provision key is unique per tenant, not across the instance.** Two tenants can now use the same key. The database constraint that made it instance-wide is replaced by one per tenant.
- **A profile can have a cap.** `maxDevices` is new and optional. A profile that has it unset stays uncapped.

#### Devices {#self-registration-devices}

- **A profile adopts an existing device only if the device was earmarked for it.** Under both strategies, and including `ALLOW_NEW`, a device that exists but was not earmarked for the profile is refused with `409 DEVICE_NOT_EARMARKED`. Earmark with `provisioningProfileToken` on `createDevice` and `importDevices`, or the console's **Expected provisioning profile**. Existing devices have no earmark, and an earmark can only be set when a device is created: `updateDevice` has no such field, and an `importDevices` row for an existing device whose earmark differs, including a device with none, is a `CONFLICT`. A profile can therefore never adopt a device created before this upgrade. Such a device keeps working with the credential it has; to bring it under a profile, create a new device that is earmarked for it.
- **A profile cannot be deleted while it has issued credentials**, and a device type cannot be deleted while a profile uses it. Both answer `REFERENCE_VIOLATION`. Reset or delete the devices first.

#### Operating it {#self-registration-operating}

- **The endpoint needs the API ingress.** `POST /api/device-management/provisioning/{tenant}/register` is reached through the same `/api/<area>` route as the GraphQL API. An instance with `ingress.enabled: false` has no public path to it.
- **`dcctl bootstrap` and `dcctl upgrade` now render `infrastructure.http.trustedProxies`** from the cluster's pod range, so the per-source limit sees the device and not the ingress controller. The instance configuration changes, so the services restart with the rest of the rolling upgrade. Check it if you run your own ingress controller or sit behind a load balancer: [Client addresses and trusted proxies](./bootstrap.md#trusted-proxies). Without it every device shares one request allowance per ingress pod, which a stranger can use up.
- **New configuration**, all optional, under device-management's `provisioning` key: `readDeadline`, `maxInFlight`, `maxConcurrentRegistrations`, `sourceBurst` and `sourceEvery`. The defaults are in [the limits](../reference/device-protocol.md#self-registration-limits).
- **New metrics and three alerts** (`ProvisioningAuthFailuresSustained`, `ProvisioningRateLimited`, `ProvisioningMisconfigured`) arrive with the chart. They are quiet on an instance that does not use the endpoint. See [Device self-registration](./observability.md#self-registration-alerts).
- **A new governance dimension** caps the registrations a tenant can make: a burst of 500 and 20 a second by default, set per tier or per tenant. See [Governance & Quotas](../concepts/governance.md).

#### The front end moves from ingress-nginx to Traefik {#after-v0252-front-end}

New installs serve their host through Traefik and Gateway API routes, and `ingress-nginx` is no longer installed on a cluster that does not already have it. **An existing cluster keeps `ingress-nginx`, still serving, and gains Traefik beside it.** Nothing about the host changes until you move it:

- `dcctl install` installs Traefik and the Gateway API definitions beside `ingress-nginx`, on a load balancer and address of its own.
- `dcctl upgrade` gives each instance a Gateway and routes, and keeps its Ingress objects beside them while `ingress-nginx` is on the cluster. The two use separate certificates: the Ingress objects keep `devicechain-tls`, and the Gateway uses `devicechain-tls-gateway`. During the window the upgrade waits 30 seconds for the Gateway's certificate, then warns and continues; `--drop-legacy-ingress` refuses until it is `Ready`, and `--remove-legacy-ingress` checks every instance.
- You move DNS to Traefik's address when you choose. Then `dcctl upgrade --drop-legacy-ingress` stops rendering the Ingress objects, and `dcctl install --remove-legacy-ingress` removes `ingress-nginx`. The removal refuses while any Ingress object still names its class.
- `dcctl upgrade --ingress-controller nginx` puts an instance back on `ingress-nginx` while it is installed. The choice is recorded and kept by later upgrades; `--ingress-controller auto` clears it.
- **An ACME issuer that solves HTTP-01 through the `nginx` ingress class keeps working during the window, then fails its renewals once `ingress-nginx` is removed.** Leave it until DNS points at Traefik, then give it a `gatewayHTTPRoute` solver before `--drop-legacy-ingress` and `--remove-legacy-ingress`. One set earlier fails. A DNS-01 solver works throughout.
- On AWS the Classic Load Balancer hides client addresses. `dcctl install --aws-nlb traefik` gives Traefik's Service a Network Load Balancer (`nginx`, `all` and `none` are the other values); changing a Service replaces its load balancer and changes its hostname.
- An HA install runs two Traefik replicas, spread over nodes, with a PodDisruptionBudget that keeps one up.
- On a local cluster there is no DNS to move: the browser reaches whichever controller holds the node's ports 80 and 443. `ingress-nginx` holds them until `--remove-legacy-ingress` gives them to Traefik, so run `--drop-legacy-ingress` and the removal back to back: the host does not answer in between. A fresh local cluster has Traefik on the ports from the start.

Every step, the commands, the refusals you may see and the rollback are in [Moving an existing install to Traefik](./ingress-cutover.md). The new flags are in the [`dcctl` reference](../reference/dcctl.md).

#### Ingress-nginx keeps the client address {#after-v0252-nginx-local}

While `ingress-nginx` is on the cluster, `dcctl install` now sets `externalTrafficPolicy: Local` on its LoadBalancer Service. Without it every device on the internet shared one allowance per node. See [Client addresses and trusted proxies](./bootstrap.md#trusted-proxies). The change has a cost at the moment it is made:

- The load balancer keeps sending traffic to nodes that run no controller pod until its health check marks those nodes unhealthy. **Expect traffic to those nodes to drop until then (tens of seconds).**
- A non-HA install runs one controller replica, so each controller rollout is a blip. An HA install runs two, spread over nodes.

#### Self-signed instances keep their old certificate {#after-v0252-self-signed}

An instance that serves a self-signed certificate now issues it from a certificate authority the chart creates in the instance namespace, and clients can trust that authority through `ca.crt` in the certificate's Secret ([TLS, flags and a laptop install](./bootstrap.md#install-tls-and-flags)). An existing instance **keeps its previous leaf certificate until it next renews**. Delete the `devicechain-tls` Secret to have it reissued from the authority at once; clients then fetch `ca.crt` from that Secret again.

An instance that has moved to the Gateway serves `devicechain-tls-gateway` for good, a new leaf from the same authority. After the move, clients fetch `ca.crt` from `devicechain-tls-gateway`.

#### The MQTT body-credential strip is removed {#after-v0252-body-credential}

A body credential on the platform MQTT broker is now always refused. There is nothing left to configure:

- **`capture.bodyCredential` is removed, and `event-sources` refuses to start while it is set,** to any value. The error names the key and what to do. If you manage the chart values yourself, **delete `functionalAreas.event-sources.config.capture.bodyCredential` before upgrading.**
- **`dcctl bootstrap` and `dcctl upgrade` no longer take `--mqtt-body-credential`.** A script that passes it fails: remove the flag. This is a breaking change to the command line.
- An instance dcctl manages that was on `strip` is on refuse after the upgrade, and the upgrade prints a notice. Devices that still sent a body credential go quiet. Watch `devicechain_eventsources_total_msg_credential_in_mqtt_body` for them, and update them as [Devices must stop sending a body credential over MQTT](#v0240-mqtt-body-credential) describes.
- The edge agent's own stripping of credentials from device payloads (`body_credentials_stripped_total`) is unchanged.

#### MQTT messages can be lost across the upgrade {#after-v0252-mqtt-roll}

The upgrade rolls the platform broker, one server at a time. In a rehearsal of this upgrade's broker roll on a pre-release build, on a seven-node GKE cluster (three service, three database and one load-generator node) installed with `--ha`, whose broker runs three servers, with MQTT devices sending QoS 1 at 300 messages a second throughout, **23 of 448,346 messages that the broker had acknowledged were never stored**. The losses came in bursts around each of the three broker server restarts. QoS 1 at-least-once delivery is **not guaranteed across a broker roll**: a device that was sent a PUBACK will not resend. The earlier measurements are under [Rolling upgrades](#rolling-upgrades), the broker's behaviour is in [MQTT, the platform broker](../reference/transport-matrix.md#mqtt--the-platform-broker), and the fail-closed setting that changes it is `--mqtt-fail-closed`.

#### Two-factor authentication {#after-v0252-mfa}

An account can now turn on [two-factor authentication](../guides/two-factor-authentication.md): a code from an authenticator app after the password, with ten single-use recovery codes. It is off for every account until its owner turns it on, so nothing changes for a person who does nothing. The console, the dashboard app and the MCP authorization page ask for the code, and tenants and tiers can require it. The user-management database gains tables for the factor and its recovery codes through an automatic migration.

- **`login` can now fail with `MFA_CHALLENGE`, so API clients must handle it.** For an account with a second factor, a correct password no longer returns a token: `login` fails with the error code `MFA_CHALLENGE` and a challenge in `extensions`, and the client finishes with the new `verifyMfa(challenge, code)`. A client that does not know about it sees a failed sign-in with a legible message, never a token. Accounts without a second factor sign in exactly as before. The C# SDK throws `MfaRequiredException` and gains `VerifyMfaAsync`; `dcctl` gains `--otp` on `dead-letters`, `presence` and `sim`. See [Signing in with two-factor authentication](../reference/graphql-api.md#two-factor-sign-in).
- **The `login` audit row changed meaning.** It is now written when a sign-in **completes**, with a label of `pwd`, `pwd+otp` or `pwd+recovery`. For an account with a second factor the password step is a new `mfa_challenged` row, so a query that counted `login` rows as password successes now counts completed sign-ins. New rows record enrolment, resets, failed and locked attempts and recovery-code use.
- **Enrolling, and turning the factor off, ends the account's sessions.** Enrolling ends every other session and the console swaps in a fresh one. Turning it off ends every session including the caller's.
- **A tenant can require it,** and a tier can set a floor with the `mfaRequired` tier key. Members without a factor are sent to set one up, and sessions already inside end at their next refresh. Superusers, service accounts and single sign-on sessions are not held to a tenant's setting.
- **A new break-glass command, `dcctl instances break-glass mfa-reset`,** resets an operator who has lost their factor and cannot sign in. It needs cluster credentials, always replaces the password and prints the new one once.
- **Two new alerts, `MfaIdentityLocked` and `MfaVerificationFailuresSustained`,** and four metrics arrive with the chart. They are quiet while nobody has a second factor. The remedy for a lock is to reset the password, not the factor. See [Second-factor lockouts](./observability.md#mfa-alerts).
- **Nodes need working time synchronisation.** A code is valid for its 30-second step and one step either side.

#### Over-the-air updates join the default profile {#v0260-updates}

[Over-the-air updates](../concepts/over-the-air-updates.md) are part of the `default` profile from this
release, so an instance that runs the standard system gains the `update-management` service when it
is upgraded. Nothing else about the upgrade changes. Check these before you start:

- **The relational database's connection budget may need to be raised first.** `update-management` is one
  more service that uses the database, so one default-profile instance now reserves 320 connections,
  up from 280. The budget a new cluster gets is now `700`, which admits two default-profile instances.
  **A cluster installed with the earlier default of `600` keeps it**, and if it holds two default-profile
  instances the upgrade of the second can be refused. Raise the budget before upgrading by re-running the `dcctl install` the cluster was installed with, adding `--max-connections 700`: for example `dcctl install local --max-connections 700`, plus the flags it was installed with, such as `--kube-context`, `--ha`, `--compact` and `--database-node-selector`. See
  [the connection budget](./bootstrap.md#connection-budget), which also says what raising it costs: the database's instances restart one at a time.
- **If your object store is a `ReadWriteOnce` volume on a cluster you pointed `dcctl` at, the service runs
  without a store.** Such a volume can be mounted by one service and the others already hold it, so
  `dcctl` upgrades the instance with `update-management` set to `updates.objectStore: none` rather than
  refusing. The upgrade says so. Declaring an artifact and uploading its content are refused with
  `OBJECT_STORE_NOT_CONFIGURED`, the console shows a notice on the Updates screens, and
  `updateManagementInfo.objectStoreConfigured` is `false`. An instance cannot gain a store by upgrading;
  to use updates, recreate it with S3 or a `ReadWriteMany` volume. See
  [An instance with no object store](../concepts/over-the-air-updates.md#no-object-store). A shareable
  volume, S3, and a cluster `dcctl` created itself are unaffected.
- **No artifact can be assigned until a signer is registered.** By default only an artifact signed by an
  active signer can be assigned, and the assignment is otherwise refused with
  `UNVERIFIED_ARTIFACT_REFUSED`. Registering a signer takes the `artifact:trust` authority. Follow
  [Signing Updates](../guides/signing-updates.md).
- **The download base is now derived.** `updates.downloadBaseUrl` used to be unset until you set it, so no
  device could be given a download link. When you leave it out, the chart now derives
  `<origin>/api/update-management` from the instance's ingress host, with `https` if ingress TLS is on. A
  value you set still wins, and an empty string turns downloads off. An instance with no ingress has no
  origin to derive from, and the console says downloads are not configured.
- **`dcctl enable-area local <instance> update-management` on a `default` instance reports it as already enabled.**
  Instances of the `telemetry` and `ingest-only` profiles still leave it out.
- **The same devices are reached as before.** Only a device connected over MQTT can take a managed update.
  HTTP-only, LwM2M and Sparkplug devices are not reached; see
  [Which devices can be updated](../concepts/over-the-air-updates.md#transports).

#### More on over-the-air updates {#v0260-updates-more}

Updates are finished for this release beyond joining the default profile. A [reference updatable device](../guides/building-an-updatable-device.md) (A/B slots, a launcher and rollback) ships in `examples/ota-device`. Four new alerts, `UpdateDownloadsNotConfigured`, `UpdateReportsMostlyRefused`, `UpdateRolloutStepsFailing` and `UpdateAttemptsGoingUnknown`, arrive with the chart and render only on an instance that has `update-management`; an instance that does not use updates stays quiet. See [the alert runbook](./observability.md).

#### Sparkplug B commands (opt-in) {#v0260-sparkplug-commands}

DeviceChain can now send commands to Sparkplug nodes and devices: write a metric, ask a node to rebirth, or reboot it, published on the Host Application session the platform already holds with your broker. It is off by default and is enabled per source with `commands.enabled` in the chart values (`dcctl` does not take Sparkplug sources, so this is a Helm values setting). Sparkplug has no command acknowledgement, so a command is confirmed when the node later reports the requested value, and one the node does not report ends as a timeout, not a failure. Writes are limited to the metrics a source lists, reboots need an explicit setting, and a circuit breaker pauses a source whose broker refuses the publish. The source's broker credential needs permission to publish on the node and device command topics. Three new alerts cover unconfirmed commands, an open breaker and internal command failures: `SparkplugCommandsUnconfirmed`, `SparkplugCommandBreakerOpen` and `SparkplugCommandResponsesFailing`.

Nothing changes for an install whose Sparkplug sources leave `commands.enabled` off. When a source turns it on, the chart sets the capability flag of `command-delivery` itself; the render fails if `command-delivery` is not deployed, or if you set `functionalAreas.command-delivery.config.sparkplugCommands` by hand.

#### Calculated metrics {#v0260-calculated-metrics}

[Calculated metrics](../user-guide/calculated-metrics.md) stay off: the tier ceiling that allows a tenant to author them is still `0` by default, and this release does not change it. Two things change around it. **Rolling a device profile back to an earlier version now meets the same ceiling that publishing does:** a tenant whose ceiling was lowered after a publish can no longer reactivate a version that carries more calculated metrics than the tier allows; the rollback is refused as `LIMIT_EXCEEDED`, and it is also refused when the ceiling cannot be read. A rollback to a version without calculated metrics never depends on the ceiling. The console now says why a calculated metric may not combine its inputs. A measurement harness for the cost of calculated metrics ships in `hack/`; it is for maintainers and is not part of an install.

#### Inbound integrations and device aliases {#v0260-integrations}

An [integration](../guides/inbound-integrations.md) is a tenant's authenticated sender of device data over HTTP, such as a LoRaWAN network server, with ChirpStack v4, The Things Stack v3, generic JSON and generic binary types. The chart publishes exactly one path for it on the instance's front end, `POST /api/integrations/{tenant}/{integration}[/{messageType}]`, through both the Gateway and the Ingress objects. The general device-ingest route is not published. A tenant can hold five integrations by default (the `integrationsPerTenant` tier key, 0 to 100). A device type that an integration is scoped to cannot be deleted. Devices are matched by **aliases**, and an uplink for a device that no longer holds the alias it arrived under is refused as `IntegrationAliasMismatch`, so removing or moving an alias takes effect within five minutes at most, never an hour. The console gains an Integrations page, connect snippets, unregistered devices and device aliases.

#### The monitoring stack {#v0260-monitoring}

`dcctl install` upgrades the monitoring stack (kube-prometheus-stack 65.1.1 to 92.13.1): **Prometheus 2.54 to 3.15**, Grafana 11.2 to 13.2 and the Prometheus Operator 0.77 to 0.94. Prometheus, Alertmanager, Grafana, kube-state-metrics and the operator each restart once. The operator's CRDs are updated by a pre-upgrade job that pulls `docker.io/busybox:1.37.0` and `registry.k8s.io/kubectl`, so a cluster with no route to those registries must mirror them. Grafana keeps its admin login, folders and boards. On a local cluster Prometheus keeps its data on the node's disk only, so its history restarts empty. Anyone with their own PromQL against this Prometheus should know it is Prometheus 3: range selectors are left-open, `.` in a regular expression matches a newline, and classic-histogram `le` labels are normalised. DeviceChain's own rules and boards load with no rule errors.

The chart's Grafana boards are filled in: Ingest, Messaging, Persistence and state, Delivery and Platform boards, an Instance overview that links to each, and a scaled-to-0 state on the overview.

#### Messaging and the command line {#v0260-messaging}

- **The device capture reader moves to a new durable consumer,** `<instance>_event-sources_device-events-capture_unfiltered`, created without a broker filter. A reader behind on a filtered consumer fell to about 2,000 events a second and could not catch up. During the roll an older `event-sources` pod keeps its own consumer. Going back to `v0.25.x` needs the new consumer deleted first (`nats consumer rm <instance>_device-events-capture <instance>_event-sources_device-events-capture_unfiltered`), or its capture reader stays in retry; the alert selectors and boards already match both names.
- **REACT dispatches derived events concurrently, keyed by device,** where one worker did it in turn; a hung device no longer costs the others in its lane their delivery attempts. Nothing is configured.
- **`dcctl bootstrap` and `dcctl upgrade` gain `--mqtt-fail-closed`** (and `--mqtt-fail-closed=false`) to turn the fail-closed MQTT acknowledgement on and off; omitting it keeps the recorded choice. Every `dcctl upgrade` now also refuses, before changing anything, a configuration whose stream reservation does not fit the broker's file-store ceiling.
- **OpenTofu acts on the cluster `dcctl` resolved.** Its provider now receives the same kubeconfig and context as the client, and `dcctl` refuses to run when they reach different clusters or when a provider environment override (`KUBE_HOST`, `KUBE_TOKEN` and similar) is set.

#### A broker that requests device certificates {#v0260-broker}

The broker is now DeviceChain's own `nats-server` build, `dc-nats-server`: upstream `v2.15.1` plus one setting, applied at start and on every reload, so the MQTT listener requests (but does not require) a TLS client certificate. Token and password devices are unaffected, and no device authenticates by certificate yet. The image is signed like the others, runs as root as the upstream image does, and exports `dc_broker_mqtt_client_auth_request`, `dc_broker_mqtt_tls` and `dc_broker_info` on port 7778. The new alert `BrokerMqttClientAuthLost` fires when a listener stops requesting certificates, or when a broker pod is not this build. **To go back to the upstream image,** set `server_image_repository = "nats"` and `server_image_tag = "2.15.1-alpine"`; that disables certificate devices. Add `dc-nats-server` to the images you mirror.

`dcctl` pins the broker by the digest the release pushed, so a release that changes nothing under the broker does not restart it. The first upgrade from an older `dcctl` moves the pod template from the tag to the digest, which is one roll. `dcctl version` prints a `broker:` line, the digest or `by tag`.

#### Known issues {#v0260-known-issues}

- **After a rolling restart of the broker, including this upgrade, the last server restarted leads no streams,** and publish-acknowledgement p99 roughly doubles until leadership rebalances. **Fixed in `v0.27.0`:** `dcctl upgrade` now evens out leadership after it rolls the broker; see [the broker certificate renews without dropping devices](#v0270-broker).
- **A TLS certificate rotation makes the broker reload and disconnects most MQTT sessions.** Devices reconnect on their own. **Fixed in `v0.27.0`:** the broker applies a renewed certificate to new connections without a reload, so established sessions stay up. A change to the broker's configuration still reloads it, and a reload still disconnects the sessions of devices admitted by the auth callout until the broker's `nats-server` library carries the upstream fix.
- **An ingress controller rollout drops about 5% of requests for 28 to 40 seconds.** Run it in a quiet window. **Fixed in `v0.27.0`:** both controllers now drain before a pod leaves and wait before removing an old one, so a rollout no longer drops requests behind a cloud load balancer; see [what changes at the edge](./ingress-cutover.md#ingress-cutover-differences).

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
each instance, as described [for v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Give `dcctl install` the settings the cluster
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
omits the flag; the flag was removed in `v0.26.0` (see
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
  [`v0.18.0`](https://github.com/devicechain-io/devicechain/blob/v0.18.0/docs/docs/deployment/releases-and-upgrades.md#v0180-provenance).

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
`dcctl upgrade` for each instance, as described [for v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Give `dcctl install`
the settings the cluster was installed with, such as `--ha` and `--database-node-selector`, as well as
the new `--version`. **It cannot be reached in place from `v0.22.0` or earlier: upgrade to `v0.23.0`
first.** Its four migrations add two tables and two indexes and rewrite no existing row (see
[During the upgrade](#v0240-during)). **Roll forward from it, not back:** `v0.23.0` cannot start on
the configuration this release writes (see [Going back](#v0240-going-back)). Coming from `v0.22.0` or earlier, read the
[`v0.23.0` notes](https://github.com/devicechain-io/devicechain/blob/v0.23.0/docs/docs/deployment/releases-and-upgrades.md#v0230-upgrade) as well; they apply to any upgrade that crosses that release.

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
     `devicechain_eventsources_capture_body_credential_strip` is `1`. On a chart install (not `dcctl`), set
     `functionalAreas.event-sources.config.capture.bodyCredential: strip` instead.
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
  to the platform's MQTT broker. A later `dcctl upgrade` without the flag keeps the value. `strip` was
  transitional and is removed in `v0.26.0`: from that release a body credential is always refused, the
  `--mqtt-body-credential` flag no longer exists, and `event-sources` refuses to start while
  `capture.bodyCredential` is still set. Delete the key before upgrading to `v0.26.0`.

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
| `event-sources` `capture.bodyCredential` (new, fixed, under `functionalAreas.event-sources.config`) | none: a body credential on the platform's MQTT broker was accepted | `refuse` | Removed in `v0.26.0`: a body credential is always refused. See [Devices must stop sending a body credential over MQTT](#v0240-mqtt-body-credential). |
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

## Older releases {#older-releases}

When a new minor release ships, the oldest detailed release moves into this table; each Details link opens that release's full notes as they stood at its tag.

| Version | Summary | Action needed | Details |
| --- | --- | --- | --- |
| <a id="v120-upgrade"></a>v1.2.0 | payload decoders a tenant writes, and revocation that ends sessions | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-upgrade) |
| <a id="v110-upgrade"></a>v1.1.0 | ingest costs less per event, and devices outside the cluster connect over MQTT | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v1.1.0/docs/docs/deployment/releases-and-upgrades.md#v110-upgrade) |
| <a id="v100-upgrade"></a>v1.0.0 | the first stable release: a database outage holds events, and Traefik is the default front end | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v1.0.0/docs/docs/deployment/releases-and-upgrades.md#v100-upgrade) |
| <a id="v0230-upgrade"></a>v0.23.0 | customer users with scoped access, and an MQTT ingest that loses less | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.23.0/docs/docs/deployment/releases-and-upgrades.md#v0230-upgrade) |
| <a id="v0220-upgrade"></a>v0.22.0 | dashboards on a square grid, and an event path that waits less | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.22.0/docs/docs/deployment/releases-and-upgrades.md#v0220-upgrade) |
| <a id="v0211-upgrade"></a>v0.21.1 | a patch, nothing to do | No | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.21.1/docs/docs/deployment/releases-and-upgrades.md#v0211-upgrade) |
| <a id="v0210-upgrade"></a>v0.21.0 | a device can be shut out whole, and updates are signed | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.21.0/docs/docs/deployment/releases-and-upgrades.md#v0210-upgrade) |
| <a id="v0200-upgrade"></a>v0.20.0 | sign in through your own identity provider; widgets ship as packages | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.20.0/docs/docs/deployment/releases-and-upgrades.md#v0200-upgrade) |
| <a id="v0190-upgrade"></a>v0.19.0 | sized from measurement at 6,000 events a second; a full stream refuses | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/docs/deployment/releases-and-upgrades.md#v0190-upgrade) |
| <a id="v0180-upgrade"></a>v0.18.0 | what failed silently now says so, and ingest keeps up with its ceiling | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.18.0/docs/docs/deployment/releases-and-upgrades.md#v0180-upgrade) |
| <a id="v0170-upgrade"></a>v0.17.0 | instances stop owning the cluster they run on | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade) |
| <a id="v0160-upgrade"></a>v0.16.0 | devices must name the dispatch they are answering | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.16.0/docs/docs/deployment/releases-and-upgrades.md#v0160-upgrade) |
| <a id="v0150-upgrade"></a>v0.15.0 | updates stop erasing what you did not send | No | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.15.0/docs/docs/deployment/releases-and-upgrades.md#v0150-upgrade) |
| <a id="v0140-upgrade"></a>v0.14.0 | the packages you build against | No | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.14.0/docs/docs/deployment/releases-and-upgrades.md#v0140-upgrade) |
| <a id="v0130-upgrade"></a>v0.13.0 | geofence limits become part of your plan | No | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.13.0/docs/docs/deployment/releases-and-upgrades.md#v0130-upgrade) |
| <a id="v0121-upgrade"></a>v0.12.1 | a patch, nothing to do | No | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.12.1/docs/docs/deployment/releases-and-upgrades.md#v0121-upgrade) |
| <a id="v0120-upgrade"></a>v0.12.0 | an upgrade that changes contracts | Yes | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.12.0/docs/docs/deployment/releases-and-upgrades.md#v0120-upgrade) |
| <a id="v0110-upgrade"></a>v0.11.0 | a normal upgrade again | No | [Full notes](https://github.com/devicechain-io/devicechain/blob/v0.11.0/docs/docs/deployment/releases-and-upgrades.md#v0110-upgrade) |

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
