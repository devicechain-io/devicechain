---
sidebar_position: 2
title: Deployment & Operator
---

# Deployment & Operator

DeviceChain deploys in two declarative layers: a **Helm chart** renders the platform's workloads, and a Kubernetes **operator** (built with controller-runtime) handles the `DeviceChainInstance` lifecycle. Both are declarative and GitOps-friendly. (Tenants are not part of the operator's work — they are control-plane database records, see below.)

:::note Status
The Helm chart renders the per-service workloads and config today. The operator's instance **status aggregation** and **config hot-reload** are in progress. Per-environment Kustomize overlays are planned.
:::

## Deploying with Helm

The chart at `deploy/helm/devicechain` renders one Deployment + Service per **enabled functional area**, along with the instance and per-service config ConfigMaps. Each pod exposes `/healthz` (liveness) and `/readyz` (readiness) so a service that isn't ready is held out of rotation.

You choose which services to run with **either** a named profile **or** an explicit set:

| Profile | Functional areas |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — the standard system, and what an unset profile resolves to |
| `full` | everything in `default`, plus `ai-inference`, `outbound-connectors`, and `mcp`: the areas that reach outside the instance, each of which carries a decision to make deliberately (a paid provider key, an egress surface, an agent-facing API) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

```bash
helm install dc deploy/helm/devicechain --set instance.id=devicechain
# pick a smaller footprint:
helm install dc deploy/helm/devicechain --set profile=telemetry
```

To install a published release, pin the image tag to a version — released images are public
on `ghcr.io/devicechain-io`, so nothing has to be built locally:

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

See [Releases & Upgrades](./releases-and-upgrades.md) for the versioning model and how
`helm upgrade` rolls forward with zero downtime.

`user-management` and `device-management` are the required core; `event-management`, `device-state`, and `command-delivery` are independently optional. The chart **fails the render** if a selection omits a required core service or an enabled service's hard dependency — so a broken topology is caught at install time, not after pods crash-loop. Values are validated against the chart's `values.schema.json` at apply time.

## Custom resources

- **`DeviceChainInstance`** (cluster-scoped) — one per installation, declaring the instance identity and configuration.

Tenants are **not** custom resources — they are control-plane database records created through the instance admin API and the `/admin` console, sharing the instance's services (see [Multi-Tenancy](../concepts/multi-tenancy.md)).

```bash
kubectl get devicechaininstance      # platform
```

## Separation of concerns

DeviceChain deliberately splits each layer:

| Layer | Tool | Responsibility |
|---|---|---|
| Infrastructure | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Workloads | **Helm chart** | Deployments, Services, and config ConfigMaps per functional area |
| Lifecycle | **Operator** | `DeviceChainInstance` status aggregation and config hot-reload |
| Business configuration | kubectl / UI | tenants and their settings |

OpenTofu runs once at cluster creation; the chart renders the workloads; the operator runs continuously, reconciling lifecycle. Cluster bootstrapping never lives in application or operator code — it is the infrastructure layer's job. The OpenTofu modules live in [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu); they provision the database tier with retention guards so it survives application teardown (see [Releases & Upgrades](./releases-and-upgrades.md#data-durability)).
