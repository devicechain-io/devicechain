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
| `full` | everything in `default`, plus `ai-inference`, `outbound-connectors`, and `mcp`: the areas that reach outside the instance, each of which carries a decision to make deliberately (a paid provider key, an egress surface, an agent-facing API) |
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
upgrade procedure — which is `helm upgrade` for the services **plus** `dcctl upgrade` for the
operator, since the operator is not part of the chart.

`user-management` and `device-management` are the required core; `event-management`, `device-state`, and `command-delivery` are independently optional. The chart **fails the render** if a selection omits a required core service or an enabled service's hard dependency — so a broken topology is caught at install time, not after pods crash-loop. Values are validated against the chart's `values.schema.json` at apply time.

## Custom resources

- **`Instance`** (cluster-scoped; `instances.core.devicechain.io`, short name `dci`) — one per installation, declaring the instance identity and configuration.

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
rewriting it would point `dcctl destroy` at a different cluster. The API server refuses
those edits rather than `dcctl` checking for them.

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

**`dcctl destroy` clears the finalizer itself**, as its last step, once the instance is
actually gone — so in the ordinary case there is nothing to do by hand. (When `destroy`
deletes the whole cluster, the declaration goes with it.)

:::note A destroy that fails leaves the declaration behind on purpose
It still records which cluster the instance lives in, which is what a re-run needs, and
it reads `Destroying` rather than `Ready`, so the next reader can tell they are looking
at a teardown in progress. `dcctl bootstrap` over such a declaration **refuses** rather
than building half a new instance on top of half an old one; it tells you to finish the
teardown first.
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

OpenTofu runs once at cluster creation; the chart renders the workloads; the operator runs continuously, reconciling lifecycle. Cluster bootstrapping never lives in application or operator code — it is the infrastructure layer's job. The OpenTofu modules live in [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu); they provision the database tier with retention guards so it survives application teardown (see [Releases & Upgrades](./releases-and-upgrades.md#data-durability)).
