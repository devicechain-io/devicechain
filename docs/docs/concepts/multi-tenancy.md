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

- **Messaging** — NATS subjects are scoped per tenant (`{instance}.{tenant}.{suffix}`), so a tenant's traffic is namespaced on the bus.
- **Storage** — event data is partitioned by tenant in TimescaleDB.
- **Auth** — JWTs carry tenant claims; services validate them locally without a per-request network call.

## Why shared microservices

Running one set of services for all tenants keeps the cluster footprint small and the operational model simple, while subject scoping and storage partitioning provide the isolation that matters. The shared services discover tenants at runtime by watching `DeviceChainTenant` resources and scope their work accordingly. *(Runtime multi-tenant scoping in the shared services is in progress.)*
