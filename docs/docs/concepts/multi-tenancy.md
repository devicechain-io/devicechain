---
title: Multi-Tenancy
---

# Multi-Tenancy

Each DeviceChain instance runs a **single shared set of microservices** that serves all of its tenants. It does not start a separate stack of pods for each tenant. Isolation between tenants is enforced at the messaging and storage layers instead.

## The instance and its tenants

One Kubernetes custom resource models the platform itself:

- **`Instance`** (cluster-scoped): one per installation. It represents the platform.

Tenants are not Kubernetes resources. A tenant is a control-plane **database record**: a registry entry plus per-tenant configuration. You create tenants on demand through the instance admin API and the `/admin` console. Tenants share the instance's services and do not get their own pods.

A fresh instance has no tenants. It seeds only a superuser, who creates the first tenant from the admin console.

## Isolation {#isolation}

- **Storage (enforced).** Every tenant-owned row carries its tenant, in a `tenant_id` column on almost every table (a few detection-engine tables name it `tenant`). A central database scope applies a `WHERE tenant_id = …` predicate, on whichever tenant column the table has, to every read and stamps the tenant on every write. If a tenant-scoped query has no tenant in context, the scope rejects it, so a missing filter cannot leak another tenant's data. For API requests, the tenant comes from the tenant claim in the caller's verified JWT (internal service calls are the one exception, described below). For messages, the tenant is derived from the messaging subject.
- **Messaging (enforced).** Subjects are scoped per tenant (`{instance}.{tenant}.{suffix}`), so each tenant's traffic has its own namespace on the bus. On the device plane the broker enforces this:
  - the MQTT/NATS listeners use TLS;
  - a NATS auth-callout, in which the broker asks the platform to authorize each connection, binds each device connection to its own tenant's subjects;
  - the messaging write and subscribe points reject a malformed tenant segment.

  As a result, a device cannot publish into or subscribe to another tenant's subjects.
- **Auth.** JWTs carry tenant claims that resolve the request's tenant. Services validate them locally, without a network call per request.

## Deleting a tenant

A tenant is a database record, not a set of pods, so deleting one is not a matter of tearing down infrastructure. Its data is rows, streams, cached lookups and uploaded objects, spread across every storage system the instance uses and all keyed on the tenant's token.

Deletion is therefore a lifecycle:

1. Access is cut immediately.
2. The data is reclaimed in the background.
3. The token stays reserved until reclamation finishes and no connection that predates the delete could still write under it.

See [Tenant Deletion](../deployment/tenant-deletion.md).

## Why shared microservices

One set of services for all tenants keeps the cluster footprint small and the operational model simple. The enforced row-level scope, plus subject scoping on the bus, provides the isolation that matters. The shared services work out the tenant of each request or message and scope all data access to it automatically.

The API-path tenant is taken from the caller's verified RS256 JWT tenant claim. The one exception is an internal service-to-service call: its service token has no tenant claim, so the calling service names the tenant in a request header, which is honored only after the service token's signature is verified. The shared pod consumes every tenant's messages over a wildcard subject and derives each message's tenant from its subject. An earlier, temporary mechanism that trusted a tenant header set by a gateway has been removed.

:::note Status
Runtime tenant scoping on the data path is enforced today. A tenant-scoped query with no tenant is rejected.
:::
