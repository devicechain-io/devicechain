---
sidebar_position: 1
title: Kubernetes Operator
---

# Kubernetes Operator

DeviceChain is deployed and managed by a Kubernetes **operator** (built with controller-runtime). The operator reconciles custom resources into the running platform, so the platform's state is declarative and GitOps-friendly.

:::note Status
The operator manages the instance and tenant lifecycle today. Production-readiness items (configurable replicas/resources/probes, leader election, bootstrap ordering) are in progress.
:::

## Custom resources

- **`DeviceChainInstance`** (cluster-scoped) — one per installation. The operator materializes the shared microservice Deployments and Services for the instance from a microservice catalog.
- **`DeviceChainTenant`** (namespaced) — one per tenant. The operator maintains the tenant's configuration; tenants share the instance's microservices (see [Multi-Tenancy](../concepts/multi-tenancy.md)).

```bash
kubectl get devicechaininstance      # platform health
kubectl get devicechaintenant        # tenant roster
```

## Separation of concerns

DeviceChain deliberately splits infrastructure provisioning from platform lifecycle:

| Layer | Tool | Responsibility |
|---|---|---|
| Infrastructure | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Platform lifecycle | **Operator** | CRDs → Deployments, Services, configuration |
| Business configuration | kubectl / UI | tenants and their settings |

OpenTofu runs once at cluster creation. The operator runs continuously, watching and reconciling. Cluster bootstrapping never lives in application or operator code — it is the infrastructure layer's job. OpenTofu modules and DeviceChain Helm charts are planned deliverables; see the repository for current status.

## How reconciliation works

The operator uses server-side apply to write only the fields it owns, sets owner references so Kubernetes garbage-collects child resources when an instance or tenant is deleted, and watches its child resources so changes are reconciled promptly without unnecessary churn.
