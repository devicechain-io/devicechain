---
title: Object Storage
---

# Object Storage

Some things a platform holds are neither rows nor time-series points — a tenant's **logo** today, and eventually firmware packages. These are opaque binary assets, and they do not belong in the relational database. DeviceChain stores them in a **pluggable object store**: one interface in the shared core library, with swappable storage backends selected by configuration.

It is the sibling of the [encrypted secret store](./architecture.md#secret-handling), built on the same philosophy: one seam, many backends, typed **fail-closed** configuration — an unknown or invalid backend is rejected at startup, never silently ignored. And the division of labor between the two is strict: the object store holds **non-secret** binary assets; credentials and other secrets live only in the envelope-encrypted secret store, never here.

:::note Status
**Available today:** the object-store abstraction with two backends — **filesystem** (the default, backed by a volume/PVC the Helm chart mounts once `blobStorage.persistence` is enabled) and **S3-compatible** (AWS S3 or MinIO) as a drop-in. The first consumer is **tenant white-labeling**: per-tenant branding logos. (The branding record's `background` is a hex color, not an image; larger branding images such as login backgrounds are planned.) Additional backends (Google Cloud Storage) and consumers (firmware/OTA packages, tenant data exports) are planned behind the same interface — this repository is the source of truth for what currently builds.
:::

## One seam, many backends

Every feature that stores a binary asset goes through the same abstraction — no feature ever talks to a storage SDK directly. That keeps the platform's storage story simple:

- **Filesystem** (the default) — objects live on a mounted volume (a PVC in Kubernetes). Zero cloud dependency: it runs in a local kind cluster or a self-hosted deployment once `blob.directory` is set and the chart's `blobStorage.persistence` is enabled (a PVC mounted at that path) — the chart leaves it unconfigured by default. Reads are served through an **authorizing API proxy** — there is no direct public path to the files.
- **S3 / S3-compatible** — AWS S3 or a self-hosted **MinIO**, one API covering both. Selecting it is a configuration change, not a code change — but every stored handle is bound to the backend that wrote it, so an instance that already holds objects on one backend must migrate (or re-upload) them when it switches. Cloud backends can additionally mint **presigned, expiring URLs** for reads. Credentials come from the standard cloud credential chain (environment, workload identity) — never from a plaintext config value.

Because every consumer sits behind the one interface, a new backend benefits all of them at once, and switching backends is a deployment decision rather than a feature-by-feature migration — one data migration, not one per consumer.

## Objects are referenced by handle

A stored object is identified by an **opaque reference** — the consumer persists the handle in its own record (say, a tenant's `branding logo` field) and dereferences it when the bytes are needed. The handle carries no data; the bytes live only in the store.

Object keys are **instance- and tenant-prefixed**, so one tenant's assets are namespaced away from another's, and every key segment is strictly validated — a key can never traverse outside its namespace on a path-based backend. There is **no public bucket by default**: every read is either authorized through the API proxy or served via a short-lived signed URL that the owning service mints deliberately.

## What goes here — and what doesn't

| Data | Where it lives |
|---|---|
| Branding logos today; firmware packages and other binary assets as they land | **Object store** (this page) |
| SMTP passwords, webhook tokens, connector credentials | [Encrypted secret store](./architecture.md#secret-handling) — envelope-encrypted, write-only, resolved by handle |
| Device telemetry and events | TimescaleDB hypertables, via [event-management](./architecture.md#components) |
| Entities (devices, profiles, dashboards, …) | The relational database |

The relational system of record stays single and non-pluggable by design; the *binary* store is the one storage concern that is legitimately pluggable, because where a logo or a firmware image physically lives is a deployment preference, not a data-model decision.

## Deployment

The filesystem default needs only a **persistent volume**: set `blob.directory` and enable `blobStorage.persistence`, and the Helm chart creates the PVC and mounts it into the services that store assets — no extra infrastructure to install or operate. Selecting the S3 backend is a configuration change on the instance: the endpoint and bucket are non-secret config, while the access credential resolves from the deployment's credential chain (for example, environment variables from the instance's Kubernetes Secret, or workload identity on a cloud cluster).

Like every DeviceChain config surface, the object-store configuration is **typed and fails closed**: a misspelled backend name is a startup error, not a silent fallback. A filesystem backend with no directory is treated as "object store not configured": the service still starts, the logo upload and read endpoints return **503**, and inline/URL logos keep working.

## First consumer: white-labeling

Tenant white-labeling is the first feature built on the object store: a tenant's logo uploads into the store and is referenced by handle from the tenant's branding configuration. Very small assets can still be supplied inline (a bounded data-URI) for zero-storage-wiring deployments, but real image assets go through the store. Firmware/OTA distribution — the load-bearing case for large binaries — is planned on the same seam.

## Related

- **[Architecture](./architecture.md)** — where the shared core library sits, and the [secret store](./architecture.md#secret-handling) this abstraction parallels.
- **[Multi-tenancy](./multi-tenancy.md)** — the tenant isolation model the tenant-prefixed keys enforce.
