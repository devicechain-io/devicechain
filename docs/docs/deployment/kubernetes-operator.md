---
sidebar_position: 3
title: Deployment & Operator
---

# Deployment & Operator

DeviceChain deploys in two declarative layers: a **Helm chart** renders the platform's workloads, and a Kubernetes **operator** (built with controller-runtime) handles the `Instance` lifecycle. Both are declarative and GitOps-friendly. (Tenants are not part of the operator's work — they are control-plane database records, see below.)

:::note Status
The Helm chart renders the per-service workloads and config today. The operator's instance **status aggregation** and **config hot-reload** are in progress. Per-environment Kustomize overlays are planned.
:::

## Deploying with Helm

The chart at `deploy/helm/devicechain` renders one Deployment + Service per **enabled functional area**, along with the per-service config ConfigMaps and the instance config Secret (it carries persistence credentials, so it is a Secret rather than a ConfigMap). Each pod exposes `/healthz` (liveness) and `/readyz` (readiness) so a service that isn't ready is held out of rotation.

You choose which services to run with **either** a named profile **or** an explicit set:

| Profile | Functional areas |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — the standard system, and what an unset profile resolves to |
| `full` | everything this build ships: `default`, plus `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest` and `lwm2m-ingest` — the areas held back from `default` because each carries a decision to make deliberately (a paid provider key, an egress surface, an agent-facing API, a Sparkplug B or LwM2M device transport that binds its own inbound port) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

Any profile that runs an area owning a secret store — which the `default` profile does,
through `notification-management` — requires the instance's **secret-store root key**, and
the chart fails the render without it rather than letting the area crash-loop. Generate one
value (`openssl rand -base64 32`), keep it, and pass the **same** value on every install and
upgrade: a new key makes secrets already stored under the old one unreadable. `dcctl
bootstrap` mints and escrows this key for you; supply it yourself only when driving the
chart directly.

```bash
DC_ROOT_KEY="$(openssl rand -base64 32)"   # generate ONCE, then keep it

helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"

# Run a smaller set of services. `telemetry` runs no area with a secret store,
# so it needs no root key.
helm install dc deploy/helm/devicechain --set profile=telemetry
```

To install a published release, pin the image tag to a version — released images are public
on `ghcr.io/devicechain-io`, so nothing has to be built locally. Substitute a real released
tag for `<version>`; the
[releases page](https://github.com/devicechain-io/devicechain/releases) lists them.

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

See [Releases & Upgrades](./releases-and-upgrades.md) for the versioning model and the
upgrade procedure. For an instance you bootstrapped it is two commands: `dcctl install` moves
the operator, which belongs to the cluster, and `dcctl upgrade` moves the configuration
document and the release, which belong to the instance. The operator is not part of the
chart, so something outside the chart has to move it. For an instance driven from the chart
alone it is `helm upgrade`, with your values
[carried forward by hand](./releases-and-upgrades.md#chart-only-upgrade).

`user-management` and `device-management` are the required core; `event-management`, `device-state`, and `command-delivery` are independently optional. The chart **fails the render** if a selection omits a required core service or an enabled service's hard dependency — so a broken topology is caught at install time, not after pods crash-loop. Values are validated against the chart's `values.schema.json` at apply time.

## Custom resources

- **`Instance`** (cluster-scoped; `instances.core.devicechain.io`, short name `dci`) — one per installation, declaring the instance identity and configuration.
- **`InstanceConfiguration`** (cluster-scoped; `instanceconfigurations.core.devicechain.io`) — the rendered configuration an `Instance` resolves to, which the operator reconciles against.

Both definitions are installed by [`dcctl install`](./bootstrap.md#install), together with
the controller itself, and they are **cluster-scoped in every sense**: one copy per cluster,
shared by every instance on it, versioned with the cluster rather than with any one instance.
That is why the command that prepares a cluster is the command that moves them. `dcctl
bootstrap` and `dcctl upgrade` only read them — a cluster whose definitions are missing, or
are not the ones the release needs, is refused with the install command to run.

Tenants are **not** custom resources — they are control-plane database records created through the instance admin API and the `/admin` console, sharing the instance's services (see [Multi-Tenancy](../concepts/multi-tenancy.md)).

```bash
kubectl get instances      # platform
```

## The instance declaration {#instance-declaration}

`dcctl bootstrap` **writes** one of these objects for the instance it is about to build,
and then reads it back and works from what came back rather than from the flags that
produced it. That makes the cluster — not the machine the command was typed on — the
record of what the instance is: which provider and cluster it belongs to, the profile
and image version it runs, whether its databases were recovered from an archive, which
`dcctl` build last wrote it, and what the last run was trying to do.

An operator on a second machine can therefore read the instance with no local state at
all:

```bash
kubectl --context <kube-context> get instances
kubectl --context <kube-context> get instance <id> -o yaml
```

Part of the spec is **immutable** once written — the cluster binding above all, because
rewriting it would point `dcctl destroy` at a different cluster. Both layers refuse such
an edit: the CRD carries the rules as validation expressions, so the API server rejects a
hand edit made with `kubectl`, and `dcctl` compares the same fields against the
declaration already in the cluster before it writes.

### Reading the PHASE column {#phase}

`kubectl get instances` prints a **PHASE** column, read from the declaration's
`core.devicechain.io/phase` annotation. `dcctl instances list` — which works only on the
machine that bootstrapped the instance, from its local records — reads the same annotation
and renders it as words in its STATUS column.

:::caution The phase is intent, not health
It records what the **last `dcctl` run was trying to do**. Nothing that writes it has looked
at a pod, which is why `dcctl instances list` never says `running`. To know whether the
workloads are up, look at them: `kubectl get pods -n dci-<id>`.
:::

| PHASE | `dcctl instances list` STATUS | What it means |
|---|---|---|
| `Bootstrapping` | `bootstrap started, not finished` | `dcctl bootstrap` wrote the declaration and has not yet written a final phase. **This is also what a healthy bootstrap running right now in another terminal looks like.** |
| `Upgrading` | `upgrade started, not finished` | The same, for `dcctl upgrade`. |
| `Ready` | `declared ready` | The last bootstrap or upgrade finished. It says nothing about the pods today. |
| `Failed` | `last run failed` | The last bootstrap or upgrade returned an error. |
| `Destroying` | ``PART-WAY DESTROYED — re-run `dcctl destroy` `` | `dcctl destroy` started and did not finish. It writes this **before** deleting anything, so a destroy killed at any later point is visible here. |
| *(blank)* | `declared, phase not recorded` | The declaration was written by a `dcctl` from before the annotation existed. |

Before acting on `Bootstrapping` or `Upgrading`, check whether the run is still going: a
live run rewrites the phase to `Ready` or `Failed` when it ends, including when it is
interrupted with Ctrl+C. Only a run that never got to write its ending — a machine that lost
power, a terminal that was killed — leaves one of these behind, and neither value blocks
anything: `dcctl bootstrap` and `dcctl upgrade` run over them without complaint. The one
phase the commands **act on** is `Destroying` (see the note below). A row that reads
`declared, unknown phase "…"` was written by a newer `dcctl` than the one listing it.

`dcctl instances list` answers a few things before it reaches the phase, and each gets its
own words rather than a healthy one: `PART-WAY DESTROYED` from this machine's own destroy
marker, even when the cluster cannot be reached; `cluster gone — stale local state`;
`no record — destroy will guess the cluster` for an instance bootstrapped before the cluster
was recorded; `cluster present, no declaration`; `declaration marked for deletion` (see
[below](#finalizer)); and `could not check: …` whenever a probe failed or timed out.

### `kubectl delete instance` does not complete {#finalizer}

A declaration carries a **finalizer**, so deleting it by hand leaves the object in place,
marked for deletion, until `dcctl` clears it:

```bash
kubectl delete instance prod    # does not return; the object stays, now terminating
```

That is deliberate, and it protects two different things. A declaration deleted by hand
would orphan a live instance — namespaces, databases, volumes and workloads all still
running, with nothing in the cluster recording what they belong to. And because the
immutability rules work by comparing the new version of the object against the old one,
a recreated object has no old version to compare against: delete-then-re-apply would
repoint the cluster binding in two steps that each look legitimate on their own.

**`dcctl destroy` clears the finalizer itself**, as its last step in the cluster, once the
namespace is gone and before the local state is removed — so in the ordinary case there is
nothing to do by hand. `destroy` never
deletes the cluster itself, so this is the step that removes the declaration.

:::note A destroy that fails leaves the declaration behind on purpose
Unless it fails only while removing the local state, after the declaration is already gone,
it still records which cluster the instance lives in, which is what a re-run needs, and
it reads `Destroying` rather than `Ready`, so the next reader can tell they are looking
at a teardown in progress. `dcctl bootstrap` and `dcctl upgrade` over such a declaration
both **refuse** rather than building half a new instance on top of half an old one; the
refusal tells you to finish the teardown with `dcctl destroy <id>`, which is resumable — or,
if you are certain nothing of the instance remains, to drop the declaration with
`dcctl instances release <id>`, which destroys nothing.
:::

### Removing a declaration without destroying anything {#release}

```bash
dcctl instances release <id> --kube-context <kube-context>
```

This clears the finalizer and deletes the declaration. **It destroys nothing.** The
namespaces, databases, volumes and workloads are all still running afterwards, and
`dcctl` simply no longer has a record of what they belong to — which is exactly why the
command makes you say out loud that it is what you want. It exists because a finalizer
whose remover is gone would otherwise leave the cluster holding an object nobody can
delete.

It prints what it is about to do and asks you to type the instance name back; `--yes`
skips that prompt for scripted use. `--kube-context` is needed from any machine that did
not bootstrap the instance, since that is what tells `dcctl` which cluster holds the
declaration.

If what you want is to remove the **instance**, run `dcctl destroy` instead.

## Separation of concerns

DeviceChain deliberately splits each layer:

| Layer | Tool | Responsibility |
|---|---|---|
| Infrastructure | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Workloads | **Helm chart** | Deployments, Services, per-area config ConfigMaps, and the instance config Secret |
| Lifecycle | **Operator** | `Instance` status aggregation and config hot-reload |
| Business configuration | kubectl / UI | tenants and their settings |

OpenTofu runs when a cluster is installed (`dcctl install`, for the prerequisites every instance shares) and when an instance is bootstrapped (for that instance's own broker and event store); `dcctl install` also applies the operator and its definitions, from manifests embedded in the CLI rather than through either of the other two layers; the chart renders the workloads; the operator runs continuously, reconciling lifecycle. Cluster bootstrapping never lives in application or operator code — it is the infrastructure layer's job. The OpenTofu modules live in [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu); they provision the database tier with retention guards so it survives application teardown (see [Releases & Upgrades](./releases-and-upgrades.md#data-durability)).
