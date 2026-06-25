---
sidebar_position: 1
title: Deployment & Operator
---

# Deployment & Operator

DeviceChain deploys in two declarative layers: a **Helm chart** renders the platform's workloads, and a Kubernetes **operator** (built with controller-runtime) handles instance and tenant lifecycle. Both are declarative and GitOps-friendly.

:::note Status
The Helm chart renders the per-service workloads and config today. The operator manages tenant lifecycle; instance/tenant **status aggregation** and **config hot-reload** are in progress. Per-environment Kustomize overlays are planned.
:::

## Deploying with Helm

The chart at `deploy/helm/devicechain` renders one Deployment + Service per **enabled functional area**, along with the instance and per-service config ConfigMaps. Each pod exposes `/healthz` (liveness) and `/readyz` (readiness) so a service that isn't ready is held out of rotation.

You choose which services to run with **either** a named profile **or** an explicit set:

| Profile | Functional areas |
|---|---|
| `full` | user-management, device-management, event-sources, event-management, device-state, command-delivery |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state |
| `ingest-only` | user-management, device-management, event-sources |

```bash
helm install dc deploy/helm/devicechain --set instance.id=devicechain
# pick a smaller footprint:
helm install dc deploy/helm/devicechain --set profile=telemetry
```

`user-management` and `device-management` are the required core; `event-management`, `device-state`, and `command-delivery` are independently optional. The chart **fails the render** if a selection omits a required core service or an enabled service's hard dependency — so a broken topology is caught at install time, not after pods crash-loop. Values are validated against the chart's `values.schema.json` at apply time.

## Custom resources

- **`DeviceChainInstance`** (cluster-scoped) — one per installation, declaring the instance identity and configuration.
- **`DeviceChainTenant`** (namespaced) — one per tenant. The operator maintains the tenant's configuration; tenants share the instance's services (see [Multi-Tenancy](../concepts/multi-tenancy.md)).

```bash
kubectl get devicechaininstance      # platform
kubectl get devicechaintenant        # tenant roster
```

## Separation of concerns

DeviceChain deliberately splits each layer:

| Layer | Tool | Responsibility |
|---|---|---|
| Infrastructure | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Workloads | **Helm chart** | Deployments, Services, and config ConfigMaps per functional area |
| Lifecycle | **Operator** | tenant bootstrap; instance/tenant status and config hot-reload |
| Business configuration | kubectl / UI | tenants and their settings |

OpenTofu runs once at cluster creation; the chart renders the workloads; the operator runs continuously, reconciling lifecycle. Cluster bootstrapping never lives in application or operator code — it is the infrastructure layer's job. OpenTofu modules are planned deliverables; see the repository for current status.
