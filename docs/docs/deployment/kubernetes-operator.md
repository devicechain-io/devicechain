---
sidebar_position: 3
title: Deployment & Operator
---

# Deployment & Operator

DeviceChain deploys in two layers, and a `dcctl` command puts each one in place:

- **`dcctl install`** prepares the **cluster**. It installs the prerequisites every instance
  shares, plus the Kubernetes **operator** (built with controller-runtime) and its resource
  definitions. It knows nothing about any particular instance.
- **`dcctl bootstrap`** creates an **instance**. It writes a cluster-scoped **`Instance`**
  resource declaring what it is about to build, and the **Helm chart** renders that instance's
  workloads.

The operator watches the `Instance` resource. Tenants are not part of its work: they are
control-plane database records (see [Custom resources](#custom-resources)).

:::note Status
Available: the Helm chart renders the per-service workloads and config, and `dcctl bootstrap`
and `dcctl upgrade` drive an instance's lifecycle. The operator observes the `Instance`
resource and writes what it observes of the instance's workloads into the resource's
[status](#instance-status). Its one other job is on a cluster installed with
[`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups): every ten minutes it deletes
the databases' volume-snapshot base backups that have left their recovery window, which
CloudNativePG does not do. Planned, not started: per-environment Kustomize overlays.
:::

`kubectl get dci` answers "is it up, and at which version" without reading the workloads:

```text
NAME   PROVIDER   CLUSTER           PROFILE   REQUESTED   OPERATION   READY   RUNNING   AGE
prod   local      kind-devicechain  default   v1.0.3      Ready       True    v1.0.3    12d
```

## Deploying with Helm

The chart at `deploy/helm/devicechain` renders:

- one Deployment and Service per enabled functional area;
- the per-service config ConfigMaps;
- the instance config Secret. It carries persistence credentials, which is why it is a Secret
  rather than a ConfigMap.

Each pod exposes `/healthz` (liveness) and `/readyz` (readiness), so a service that isn't ready
is held out of rotation.

You choose which services to run with either a named profile or an explicit set:

| Profile | Functional areas |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing, update-management — the standard system, and what an unset profile resolves to |
| `full` | everything this build ships: `default`, plus `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest`, `lwm2m-ingest` — the areas held back from `default` because each carries a decision to make deliberately (a paid provider key, an egress surface, an agent-facing API, a Sparkplug B or LwM2M device transport that binds its own inbound port) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

### The secret-store root key {#root-key}

Every profile requires the instance's **secret-store root key**. `user-management`, which every
profile runs, seals the key that signs sign-in tokens under it. The areas that store
integration credentials seal those under it too. Without it, the chart fails the render rather
than letting `user-management` crash-loop.

Generate one value (`openssl rand -base64 32`), keep it, and pass the **same** value on every
install and upgrade. A new key makes secrets already stored under the old one unreadable, and
stops anyone signing in. `dcctl bootstrap` mints and escrows this key for you; supply it
yourself only when driving the chart directly.

```bash
DC_ROOT_KEY="$(openssl rand -base64 32)"   # generate ONCE, then keep it

helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"

# Run a smaller set of services. Every profile needs the root key, the smallest
# ones included.
helm install dc deploy/helm/devicechain --set profile=telemetry \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"
```

### Install a released version {#released-version}

To install a published release, pin the image tag to a version. Released images are public on
`ghcr.io/devicechain-io`, so nothing has to be built locally. Substitute a real released tag
for `<version>`; the [releases page](https://github.com/devicechain-io/devicechain/releases)
lists them.

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

[Releases & Upgrades](./releases-and-upgrades.md) covers the versioning model and the upgrade
procedure. How you upgrade depends on how the instance was created:

- **An instance you bootstrapped** takes two commands. `dcctl install` moves the operator,
  which belongs to the cluster. `dcctl upgrade` moves the instance's broker and event store, its
  configuration document and its release, which belong to the instance. The operator is not part of the chart, so something
  outside the chart has to move it.
- **An instance driven from the chart alone** takes `helm upgrade`, with your values
  [carried forward by hand](./releases-and-upgrades.md#chart-only-upgrade).

### Service selection rules {#selection-rules}

`user-management` and `device-management` are the required core. `event-management`,
`device-state` and `command-delivery` are independently optional. The chart **fails the
render** if a selection omits a required core service or an enabled service's hard dependency,
so a broken topology is caught at install time, not after pods crash-loop. Values are
validated against the chart's `values.schema.json` at apply time.

## Custom resources {#custom-resources}

`dcctl install` defines two cluster-scoped custom resources:

- **`Instance`** (`instances.core.devicechain.io`, short name `dci`) — one per installation,
  declaring the instance identity and configuration.
- **`InstanceConfiguration`** (`instanceconfigurations.core.devicechain.io`, short name `dcic`)
  — a resource that can hold an instance configuration document. The definition is installed,
  but `dcctl bootstrap` does not create one and the operator does not watch it. Each pod reads
  the instance configuration from the instance config Secret mounted into it.

[`dcctl install`](./bootstrap.md#install) installs both definitions together with the
controller itself. They are cluster-scoped in every sense: one copy per cluster, shared by
every instance on it, and versioned with the cluster rather than with any one instance. That is
why the command that prepares a cluster is the command that moves them.

`dcctl bootstrap` and `dcctl upgrade` only read the definitions. They refuse a cluster with no
definitions, or with ones identifiably from a different release, and name the install command
to run. Definitions installed by hand carry no record of which release put them there, so those
are let through with a note naming the same command rather than refused.

### Instance status {#instance-status}

The operator is the only writer of an `Instance`'s `status`, and it records what it
**observed**: it reads the instance's Deployments on every change to the `Instance` or to one of
them, so the status follows a rollout as it happens. Two other things are on the resource and are
different in kind:

| Where | What it says | Who writes it |
|---|---|---|
| `status` | what the operator **observed** of the workloads | the operator only |
| annotations (`core.devicechain.io/phase`, `last-applied-by`, `last-applied-at`) | what the last `dcctl` run **intended** or did; the `OPERATION` column | `dcctl` |
| the `dcctl` Lease in the operator's namespace | who is operating on the cluster **right now** | `dcctl` |

`dcctl` never writes `status`, and the operator never reads the annotations: `OPERATION` can say
`Upgrading` while `READY` says `True`, and both are true.

| Field | Meaning |
|---|---|
| `status.observedGeneration` | the spec generation this status was computed from |
| `status.conditions` | `Ready`, `WorkloadsAvailable`, `RolloutComplete`, `BrokerCertificateValid` (below) |
| `status.version.requested` | `spec.imageVersion` |
| `status.version.running` | the image tag every area with wanted pods runs; `mixed` (reserved: never a release tag) while areas differ; empty when it cannot be derived |
| `status.areas[]` | one entry per functional area: `name`, `desiredReplicas`, `readyReplicas`, `availableReplicas`, `updatedReplicas`, and `image` (the full image reference, not a bare tag). The conditions are computed from `availableReplicas` |

The status is small and bounded: one entry per functional area, and nothing per tenant or per
device.

| Condition | Reason | Status | Meaning |
|---|---|---|---|
| `WorkloadsAvailable` | `AllAreasAvailable` | True | every area that wants pods has all of them available |
| | `NoWorkloads` | False | no area Deployment exists (not rendered yet, or removed), or every area is scaled to zero |
| | `AreaUnavailable` | False | at least one area wants pods and has none available |
| | `AreaDegraded` | False | every wanting area has a pod available, but at least one has fewer than it wants |
| `RolloutComplete` | `RolloutComplete` | True | every Deployment has converged on its current template and the running version is the requested one |
| | `NoWorkloads` | False | no area Deployment exists |
| | `RolloutInProgress` | False | an area has not converged: old pods remain, or the Deployment controller has not observed the latest template |
| | `ProgressDeadlineExceeded` | False | a Deployment reports that its rollout has stopped progressing |
| | `VersionMismatch` | False | converged, but on a different version from `spec.imageVersion` |
| | `VersionUnknown` | Unknown | converged, but the running version cannot be derived (images pinned by digest alone) |
| `BrokerCertificateValid` | `CertificateValid` | True | the broker's TLS certificate and the authority that signed it have more than 30 days left |
| | `CertificateExpiringSoon` | True | one of them expires within 30 days; `dcctl upgrade` renews the certificate, but never the authority, which means rebuilding the instance |
| | `CertificateExpired` | False | one of them has expired: the broker refuses TLS connections until the certificate is renewed (or, for the authority, the instance rebuilt) |
| | `CertificateNotPublished` | Unknown | no expiry has been published for the instance (it was last touched by a `dcctl` from before this existed) |
| | `CertificateUnreadable` | Unknown | the published expiry is not an RFC 3339 time |
| `Ready` | `Ready` | True | `WorkloadsAvailable` and `RolloutComplete` are both True, and the broker certificate has not expired |
| | (the reason of the first condition that is not True) | False / Unknown | |

An absent workload is always reported with a reason; it is never read as healthy. `Ready` is the
settled state: it is False while a rollout is under way even though pods keep serving, so read
`WorkloadsAvailable` to ask whether the instance is serving.

#### Broker certificate expiry {#broker-certificate-expiry}

The broker's TLS certificate is valid for a year and is renewed only when `dcctl upgrade` runs,
inside the certificate's last 30 days. An instance that is never upgraded would therefore lose
broker TLS on the anniversary of its bootstrap with nothing saying so. `BrokerCertificateValid`
is that warning on the `Instance` itself.

The operator has no access to Secrets, so it does not read the certificate. `dcctl` publishes
when the certificate and its authority expire, as two annotations on the `Instance`: the dates of
the certificate in the broker's Secret, written once the broker serves it (`dcctl bootstrap`, and
every `dcctl upgrade`, including one that renews nothing, so an older instance gets them on its
next upgrade). If a renewal restarts the broker and the restart does not finish, the older date
stays, so the condition errs on the pessimistic side:

| Annotation | Value |
|---|---|
| `core.devicechain.io/broker-certificate-not-after` | when the broker's certificate expires, RFC 3339, UTC |
| `core.devicechain.io/broker-ca-not-after` | when the certificate authority that signed it expires |

The operator compares those dates with its own clock, so the condition changes as time passes
with nothing else in the cluster changing: it re-checks at each boundary and at least hourly. The
message names the date, so it does not change from one check to the next.

`Ready` follows only the False case. A certificate that is merely close to expiry leaves `Ready`
alone, because the instance is serving and a status that went unready for a renewable date would
teach readers to ignore it; an expired one means TLS to the broker fails, which is what `Ready`
exists to say. `Unknown` never changes `Ready`. When workloads are also unhealthy, `Ready` keeps
the workload reason, since that is the first thing to fix.

Instances with monitoring enabled also get the `BrokerCertificateExpiringSoon` and
`BrokerCertificateExpiryCritical` alerts, which read the expiry from the certificate the broker
is serving rather than from these annotations (see the [alert
runbook](./observability.md#broker-client-certificates)). The condition is what covers an instance with
monitoring off, and the only thing that covers the certificate authority.

`Instance` is a supported API. New status fields, condition types and reasons may be added;
nothing is removed or changes meaning. A reader must treat a condition type or reason it does not
recognise as Unknown.

Tenants are **not** custom resources. They are control-plane database records, created through
the instance admin API and the `/admin` console, and they share the instance's services (see
[Multi-Tenancy](../concepts/multi-tenancy.md)).

```bash
kubectl get instances      # platform
```

## The instance declaration {#instance-declaration}

`dcctl bootstrap` writes an `Instance` object for the instance it is about to build. It
then reads the object back and works from what came back, not from the flags that produced it.
That makes the cluster, not the machine the command was typed on, the record of what the
instance is:

- which provider and cluster it belongs to;
- the profile and image version it runs;
- whether its databases were recovered from an archive;
- which `dcctl` build last wrote it;
- what the last run was trying to do.

An operator on a second machine can therefore read the instance with no local state at all:

```bash
kubectl --context <kube-context> get instances
kubectl --context <kube-context> get instance <id> -o yaml
```

Part of the spec is **immutable** once written. The cluster binding matters most, because
rewriting it would point `dcctl destroy` at a different cluster. Both layers refuse such an
edit:

- The CRD carries the rules as validation expressions, so the API server rejects a hand edit
  made with `kubectl`.
- `dcctl` compares the same fields against the declaration already in the cluster before it
  writes.

### Reading the OPERATION column {#phase}

`kubectl get instances` prints an **OPERATION** column, read from the declaration's
`core.devicechain.io/phase` annotation. `dcctl instances list` reads the same annotation and
renders it as words in its STATUS column. It works only on the machine that bootstrapped the
instance, from that machine's local records.

:::warning The operation is intent, not health
It records what the **last `dcctl` run was trying to do**. Nothing that writes it has looked at
a pod, which is why `dcctl instances list` never says `running`. Whether the workloads are up is
the **READY** column, which the operator computes from the Deployments (see
[Instance status](#instance-status)).
:::

| OPERATION | `dcctl instances list` STATUS | What it means |
|---|---|---|
| `Bootstrapping` | `bootstrap started, not finished` | `dcctl bootstrap` wrote the declaration and has not yet written a final phase. This is also what a healthy bootstrap running right now in another terminal looks like. |
| `Upgrading` | `upgrade started, not finished` | The same, for `dcctl upgrade`. |
| `Ready` | `declared ready` | The last bootstrap or upgrade finished. It says nothing about the pods today. |
| `Failed` | `last run failed` | The last bootstrap or upgrade returned an error. |
| `Destroying` | ``PART-WAY DESTROYED — re-run `dcctl destroy` `` | `dcctl destroy` started and did not finish. It writes this **before** deleting anything, so a destroy killed at any later point is visible here. |
| *(blank)* | `declared, phase not recorded` | The declaration was written by a `dcctl` from before the annotation existed. |

Before acting on `Bootstrapping` or `Upgrading`, check whether the run is still going. A live
run rewrites the phase to `Ready` or `Failed` when it ends, including when it is interrupted
with Ctrl+C. Only a run that never got to write its ending leaves one of these behind — a
machine that lost power, a terminal that was killed. Neither value blocks anything: `dcctl
bootstrap` and `dcctl upgrade` run over them without complaint. The one phase value the
commands act on is `Destroying` (see [below](#finalizer)).

Beside the phase, one other annotation is acted on.
`core.devicechain.io/bootstrap-unfinished: "true"` marks an instance whose first bootstrap has
not yet ended successfully. While it is there, and the phase
is not `Ready`, `dcctl bootstrap` runs over the instance to finish it even though its
configuration document exists. The bootstrap or upgrade that ends successfully removes it in
the same write that records `Ready`. Do not add it by hand: on a running instance whose phase is
not `Ready`, it lets a bootstrap re-run through over that instance.

A row that reads `declared, unknown phase "…"` was written by a newer `dcctl` than the one
listing it.

`dcctl instances list` checks a few things before it reaches the phase. Each gets its own words
rather than a healthy one:

| STATUS | When |
|---|---|
| `PART-WAY DESTROYED` | From this machine's own destroy marker, even when the cluster cannot be reached |
| `cluster gone — stale local state` | The cluster is gone |
| `no record — destroy will guess the cluster` | An instance bootstrapped before the cluster was recorded |
| `cluster present, no declaration` | The cluster exists but holds no declaration |
| `declaration marked for deletion` | See [below](#finalizer) |
| `could not check: …` | A probe failed or timed out |

### `kubectl delete instance` does not complete {#finalizer}

A declaration carries a **finalizer**. Deleting it by hand leaves the object in place, marked
for deletion, until `dcctl` clears it:

```bash
kubectl delete instance prod    # does not return; the object stays, now terminating
```

That is deliberate, and it protects two different things:

- **The instance.** A declaration deleted by hand would orphan a live instance: namespaces,
  databases, volumes and workloads all still running, with nothing in the cluster recording
  what they belong to.
- **The cluster binding.** The immutability rules work by comparing the new version of the
  object against the old one. A recreated object has no old version to compare against, so
  delete-then-re-apply would repoint the cluster binding in two steps that each look
  legitimate on their own.

**`dcctl destroy` clears the finalizer itself**, as its last step in the cluster: once the
namespace is gone and before the local state is removed. In the ordinary case there is nothing
to do by hand. `destroy` never deletes the cluster itself, so this is the step that removes the
declaration.

A destroy that fails leaves the declaration behind on purpose, unless it fails only while
removing the local state, after the declaration is already gone. The declaration left behind:

- still records which cluster the instance lives in, which is what a re-run needs;
- reads `Destroying` rather than `Ready`, so the next reader can tell they are looking at a
  teardown in progress.

:::note Bootstrap and upgrade refuse a half-destroyed instance
`dcctl bootstrap` and `dcctl upgrade` over such a declaration both refuse rather than
building half a new instance on top of half an old one. The refusal tells you to finish the
teardown with `dcctl destroy <id>`, which is resumable, or, if you are certain nothing of the
instance remains, to drop the declaration with `dcctl instances release <id>`, which destroys
nothing.
:::

### Removing a declaration without destroying anything {#release}

```bash
dcctl instances release <id> --kube-context <kube-context>
```

This clears the finalizer and deletes the declaration. **It destroys nothing.** The namespaces,
databases, volumes and workloads are all still running afterwards, and `dcctl` no longer has a
record of what they belong to. That is why the command makes you confirm explicitly that this
is what you want. It exists because a finalizer whose remover is gone would otherwise leave the
cluster holding an object nobody can delete.

It prints what it is about to do and asks you to type the instance name back; `--yes` skips
that prompt for scripted use. `--kube-context` is needed from any machine that did not
bootstrap the instance, since that is what tells `dcctl` which cluster holds the declaration.

To remove the **instance**, run `dcctl destroy` instead.

## Separation of concerns

DeviceChain deliberately gives each layer to one tool:

| Layer | Tool | Responsibility |
|---|---|---|
| Infrastructure | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Workloads | **Helm chart** | Deployments, Services, and the per-area config ConfigMaps |
| Lifecycle | **Operator** | watches the `Instance` resource and its Deployments and writes the observed [status](#instance-status); prunes volume-snapshot base backups to their recovery window |
| Instance identity + config | **`dcctl`** | writes the `Instance` declaration and the instance config Secret the workloads mount |
| Business configuration | instance admin API / `/admin` console, and each tenant's own API / console | tenants, and each tenant's own settings (such as branding) |

Each layer runs at a different point:

- **OpenTofu** runs when a cluster is installed (`dcctl install`, for the prerequisites every
  instance shares) and when an instance is bootstrapped (for that instance's own broker and
  event store).
- **`dcctl install`** also applies the operator and its definitions, from manifests embedded
  in the CLI rather than through either of the other two layers.
- **The chart** renders the workloads.
- **The operator** watches the `Instance` resource and the instance's Deployments (see
  [Instance status](#instance-status) for what it writes).

Cluster bootstrapping never lives in application or operator code; it is the infrastructure
layer's job. The OpenTofu modules live in
[`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu).
They provision the database tier with retention guards so it survives application teardown
(see [Releases & Upgrades](./releases-and-upgrades.md#data-durability)).
