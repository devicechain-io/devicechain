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
resource and acts on nothing. Planned, not started: instance status aggregation and
per-environment Kustomize overlays.
:::

Status aggregation will land on the operator's watch loop. Until it does, the `Instance`
reports no status, so read the workloads themselves, or `dcctl`, to learn whether an instance
is healthy.

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
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — the standard system, and what an unset profile resolves to |
| `full` | everything this build ships: `default`, plus `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest` and `lwm2m-ingest` — the areas held back from `default` because each carries a decision to make deliberately (a paid provider key, an egress surface, an agent-facing API, a Sparkplug B or LwM2M device transport that binds its own inbound port) |
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
  which belongs to the cluster. `dcctl upgrade` moves the configuration document and the
  release, which belong to the instance. The operator is not part of the chart, so something
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

### Reading the PHASE column {#phase}

`kubectl get instances` prints a **PHASE** column, read from the declaration's
`core.devicechain.io/phase` annotation. `dcctl instances list` reads the same annotation and
renders it as words in its STATUS column. It works only on the machine that bootstrapped the
instance, from that machine's local records.

:::warning The phase is intent, not health
It records what the **last `dcctl` run was trying to do**. Nothing that writes it has looked at
a pod, which is why `dcctl instances list` never says `running`. To know whether the workloads
are up, look at them: `kubectl get pods -n dci-<id>`.
:::

| PHASE | `dcctl instances list` STATUS | What it means |
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
bootstrap` and `dcctl upgrade` run over them without complaint. The one phase the commands
act on is `Destroying` (see [below](#finalizer)).

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
| Lifecycle | **Operator** | watches the `Instance` resource; acts on nothing today (status aggregation is planned) |
| Instance identity + config | **`dcctl`** | writes the `Instance` declaration and the instance config Secret the workloads mount |
| Business configuration | instance admin API / `/admin` console, and each tenant's own API / console | tenants, and each tenant's own settings (such as branding) |

Each layer runs at a different point:

- **OpenTofu** runs when a cluster is installed (`dcctl install`, for the prerequisites every
  instance shares) and when an instance is bootstrapped (for that instance's own broker and
  event store).
- **`dcctl install`** also applies the operator and its definitions, from manifests embedded
  in the CLI rather than through either of the other two layers.
- **The chart** renders the workloads.
- **The operator** watches the `Instance` resource (see the status note at the top of this page for
  what it does with it today).

Cluster bootstrapping never lives in application or operator code; it is the infrastructure
layer's job. The OpenTofu modules live in
[`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu).
They provision the database tier with retention guards so it survives application teardown
(see [Releases & Upgrades](./releases-and-upgrades.md#data-durability)).
