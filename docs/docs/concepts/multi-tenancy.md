---
sidebar_position: 3
title: Multi-Tenancy
---

# Multi-Tenancy

DeviceChain runs a **single shared set of microservices per instance** that serves all tenants, rather than spinning up a separate stack of pods for each tenant. Isolation is enforced at the messaging and storage layers.

## The instance and its tenants

One Kubernetes custom resource models the platform itself:

- **`DeviceChainInstance`** (cluster-scoped) — one per installation. Represents the platform.

Tenants are **not** Kubernetes resources. A tenant is a control-plane **database record** — a registry entry plus per-tenant configuration — created on demand through the instance admin API and the `/admin` console. Tenants share the instance's services and do **not** get their own pods. A fresh instance is **tenant-less**: it seeds only a superuser, who creates the first tenant from the admin console.

## Isolation

- **Storage (enforced)** — every tenant-owned row carries a `tenant_id`, and a central database scope applies a `WHERE tenant_id = …` predicate to every read and stamps it on every write. The scope is **fail-closed**: a tenant-scoped query with no tenant in context is rejected, so a missing filter cannot leak another tenant's data. The per-request tenant comes from the caller's verified JWT tenant claim, and the per-message tenant is derived from the messaging subject.
- **Messaging** — subjects are scoped per tenant (`{instance}.{tenant}.{suffix}`), so a tenant's traffic is namespaced on the bus.
- **Auth** — JWTs carry tenant claims that resolve the request tenant; services validate them locally without a per-request network call.

## Why shared microservices

Running one set of services for all tenants keeps the cluster footprint small and the operational model simple, while the enforced row-level scope (plus subject scoping on the bus) provides the isolation that matters. The shared services derive each request's or message's tenant and scope all data access to it automatically.

:::note Status
Runtime tenant scoping on the data path is enforced today (fail-closed): the API-path tenant is sourced from the caller's verified RS256 JWT tenant claim, and the shared pod consumes every tenant's messages over a wildcard subject, deriving each message's tenant from its subject. The earlier temporary trusted-gateway-header seam has been removed.
:::
