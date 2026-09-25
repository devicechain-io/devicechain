---
title: Object Storage
---

# Object Storage

Some things a platform holds are neither rows nor time-series points: a tenant's **logo** today, and eventually firmware packages. These are opaque binary assets, and they do not belong in the relational database. DeviceChain stores them in a **pluggable object store** — one interface in the shared core library, with storage backends you select by configuration.

The object store is the sibling of the [encrypted secret store](./architecture.md#secret-handling) and follows the same design: one interface, many backends, and typed configuration that rejects an unknown or invalid backend at startup rather than silently ignoring it. The division of labor between the two is strict. The object store holds **non-secret** binary assets only; credentials and other secrets live only in the envelope-encrypted secret store, never here.

:::note Status
**Available today:** the object-store interface with two backends, **filesystem** (the default) and **S3-compatible** (AWS S3 or MinIO) as a drop-in. The first consumer is **tenant white-labeling** (per-tenant branding logos).

**Planned:** a Google Cloud Storage backend; larger branding images such as login backgrounds; firmware/OTA packages and tenant data exports, all behind the same interface. This repository is the source of truth for what currently builds.
:::

## One interface, many backends {#one-seam-many-backends}

Every feature that stores a binary asset goes through the same interface. No feature talks to a storage SDK directly.

- **Filesystem** (the default): objects live on a mounted volume (a PVC in Kubernetes), with no cloud dependency. It runs in a local kind cluster or a self-hosted deployment once `blob.directory` is set and the chart's `blobStorage.persistence` is enabled (a PVC mounted at that path). The chart leaves it unconfigured by default. Reads are served through an **authorizing API proxy**; there is no direct public path to the files.
- **S3 / S3-compatible**: AWS S3 or a self-hosted **MinIO**, one API covering both. Selecting it is a configuration change, not a code change. Cloud backends can also mint **presigned, expiring URLs** for reads. Credentials come from the standard cloud credential chain (environment, workload identity), never from a plaintext config value.

Every stored handle is bound to the backend that wrote it. An instance that already holds objects on one backend must migrate (or re-upload) them when it switches.

Because every consumer sits behind the one interface, a new backend benefits all of them at once. Switching backends is a deployment decision rather than a feature-by-feature migration: one data migration, not one per consumer.

## Objects are referenced by handle

A stored object is identified by an **opaque reference**. The consumer persists the handle in its own record (say, a tenant's `branding logo` field) and dereferences it when it needs the bytes. The handle carries no data; the bytes live only in the store.

Object keys are **instance- and tenant-prefixed**, so each tenant's assets are namespaced away from every other tenant's. Every key segment is strictly validated, so on a path-based backend a key can never traverse outside its namespace.

There is **no public bucket by default**. Every read is either authorized through the API proxy or served from a short-lived signed URL that the owning service mints deliberately.

## What goes in the object store {#what-goes-here--and-what-doesnt}

| Data | Where it lives |
|---|---|
| Branding logos today; firmware packages and other binary assets as they land | **Object store** (this page) |
| SMTP passwords, webhook tokens, connector credentials | [Encrypted secret store](./architecture.md#secret-handling) — envelope-encrypted, write-only, resolved by handle |
| Device telemetry and events | TimescaleDB hypertables, via [event-management](./architecture.md#components) |
| Entities (devices, profiles, dashboards, …) | The relational database |

The relational system of record stays single and non-pluggable by design. The *binary* store is the one storage concern that is legitimately pluggable, because where a logo or a firmware image physically lives is a deployment preference, not a data-model decision.

## Deployment

The filesystem default needs only a **persistent volume**. Set `blob.directory` and enable `blobStorage.persistence`, and the Helm chart creates the PVC and mounts it into the services that store assets. There is no extra infrastructure to install or operate.

Selecting the S3 backend is a configuration change on the instance. The endpoint and bucket are non-secret config. The access credential resolves from the deployment's credential chain — for example, environment variables from the instance's Kubernetes Secret, or workload identity on a cloud cluster.

Like every DeviceChain config surface, the object-store configuration is typed and strict: a misspelled backend name is a startup error, not a silent fallback. A filesystem backend with no directory is treated as "object store not configured". The service still starts, the logo upload and read endpoints return **503**, and inline and URL logos keep working.

## First consumer: white-labeling

Tenant white-labeling is the first feature built on the object store. A tenant's logo uploads into the store and is referenced by handle from the tenant's branding configuration. Very small assets can still be supplied inline (a bounded data-URI) for deployments with no storage wired up, but real image assets go through the store. The branding record's `background` is a hex color, not an image.

Firmware/OTA distribution, the main case for large binaries, is planned on the same interface.

## Related

- **[Architecture](./architecture.md)**: where the shared core library sits, and the [secret store](./architecture.md#secret-handling) this abstraction parallels.
- **[Multi-tenancy](./multi-tenancy.md)**: the tenant isolation model the tenant-prefixed keys enforce.
