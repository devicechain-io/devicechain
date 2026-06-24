---
sidebar_position: 3
title: Multi-Tenancy
---

# Multi-Tenancy

DeviceChain runs a **single shared set of microservices per instance** that serves all tenants, rather than spinning up a separate stack of pods for each tenant. Isolation is enforced at the messaging and storage layers.

## Custom resources

Two Kubernetes custom resources model the platform:

- **`DeviceChainInstance`** (cluster-scoped) — one per installation. Represents the platform itself.
- **`DeviceChainTenant`** (namespaced) — one per tenant. Adding a tenant is a declarative operation: create a `DeviceChainTenant` resource and the operator reconciles the tenant's configuration. Tenants do **not** get their own pods.

Because tenants are declarative resources, the full tenant roster is version-controllable and GitOps-friendly, and `kubectl get devicechaintenant` shows the live roster.

## Isolation

- **Storage (enforced)** — every tenant-owned row carries a `tenant_id`, and a central database scope applies a `WHERE tenant_id = …` predicate to every read and stamps it on every write. The scope is **fail-closed**: a tenant-scoped query with no tenant in context is rejected, so a missing filter cannot leak another tenant's data. The per-request tenant is supplied to the API by the gateway, and the per-message tenant is derived from the messaging subject.
- **Messaging** — subjects are scoped per tenant (`{instance}.{tenant}.{suffix}`), so a tenant's traffic is namespaced on the bus.
- **Auth** — JWTs carry tenant claims that resolve the request tenant; services validate them locally without a per-request network call.

## Why shared microservices

Running one set of services for all tenants keeps the cluster footprint small and the operational model simple, while the enforced row-level scope (plus subject scoping on the bus) provides the isolation that matters. The shared services derive each request's or message's tenant and scope all data access to it automatically.

:::note Status
Runtime tenant scoping on the data path is enforced today (fail-closed). Two pieces are still being wired: sourcing the API-path tenant from the verified JWT claim (currently a trusted gateway header), and consuming all tenants' subjects from one shared pod — which lands with the messaging migration.
:::
