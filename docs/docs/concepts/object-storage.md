---
sidebar_position: 11
title: Object Storage
---

# Object Storage

Some things a platform holds are neither rows nor time-series points — a tenant's **logo**, a dashboard **background image**, and eventually firmware packages. These are opaque binary assets, and they do not belong in the relational database. DeviceChain stores them in a **pluggable object store**: one interface in the shared core library, with swappable storage backends selected by configuration.

It is the sibling of the [encrypted secret store](./architecture.md#secret-handling), built on the same philosophy: one seam, many backends, typed **fail-closed** configuration — an unknown or invalid backend is rejected at startup, never silently ignored. And the division of labor between the two is strict: the object store holds **non-secret** binary assets; credentials and other secrets live only in the envelope-encrypted secret store, never here.

:::note Status
**Available today:** the object-store abstraction with two backends — **filesystem** (the default, a mounted volume/PVC wired by the Helm chart) and **S3-compatible** (AWS S3 or MinIO) as a drop-in. The first consumer is **tenant white-labeling**: per-tenant branding logos and background images. Additional backends (Google Cloud Storage) and consumers (firmware/OTA packages, tenant data exports) are planned behind the same interface — this repository is the source of truth for what currently builds.
:::

## One seam, many backends

Every feature that stores a binary asset goes through the same abstraction — no feature ever talks to a storage SDK directly. That keeps the platform's storage story simple:

- **Filesystem** (the default) — objects live on a mounted volume (a PVC in Kubernetes). Zero cloud dependency: it works in a local kind cluster and in self-hosted deployments out of the box. Reads are served through an **authorizing API proxy** — there is no direct public path to the files.
- **S3 / S3-compatible** — AWS S3 or a self-hosted **MinIO**, one API covering both. A config flip, not a code change. Cloud backends can additionally mint **presigned, expiring URLs** for reads. Credentials come from the standard cloud credential chain (environment, workload identity) — never from a plaintext config value.

Because every consumer sits behind the one interface, a new backend benefits all of them at once, and switching backends is a deployment decision rather than a feature-by-feature migration.

## Objects are referenced by handle

A stored object is identified by an **opaque reference** — the consumer persists the handle in its own record (say, a tenant's `branding logo` field) and dereferences it when the bytes are needed. The handle carries no data; the bytes live only in the store.

Object keys are **instance- and tenant-prefixed**, so one tenant's assets are namespaced away from another's, and every key segment is strictly validated — a key can never traverse outside its namespace on a path-based backend. There is **no public bucket by default**: every read is either authorized through the API proxy or served via a short-lived signed URL that the owning service mints deliberately.

## What goes here — and what doesn't

| Data | Where it lives |
|---|---|
| Branding logos, background images, and other binary assets | **Object store** (this page) |
| SMTP passwords, webhook tokens, connector credentials | [Encrypted secret store](./architecture.md#secret-handling) — envelope-encrypted, write-only, resolved by handle |
| Device telemetry and events | TimescaleDB hypertables, via [event-management](./architecture.md#components) |
| Entities (devices, profiles, dashboards, …) | The relational database |

The relational system of record stays single and non-pluggable by design; the *binary* store is the one storage concern that is legitimately pluggable, because where a logo or a firmware image physically lives is a deployment preference, not a data-model decision.

## Deployment

The filesystem default needs only a **persistent volume**, which the Helm chart wires for the services that store assets — no extra infrastructure to install or operate. Selecting the S3 backend is a configuration change on the instance: the endpoint and bucket are non-secret config, while the access credential resolves from the deployment's credential chain (for example, environment variables from the instance's Kubernetes Secret, or workload identity on a cloud cluster).

Like every DeviceChain config surface, the object-store configuration is **typed and fails closed**: a misspelled backend name or a filesystem backend with no directory is a startup error, not a silent fallback.

## First consumer: white-labeling

Tenant white-labeling is the first feature built on the object store: a tenant's branding assets (logo, background) upload into the store and are referenced by handle from the tenant's branding configuration. Very small assets can still be supplied inline (a bounded data-URI) for zero-storage-wiring deployments, but real image assets go through the store. Firmware/OTA distribution — the load-bearing case for large binaries — is planned on the same seam.

## Related

- **[Architecture](./architecture.md)** — where the shared core library sits, and the [secret store](./architecture.md#secret-handling) this abstraction parallels.
- **[Multi-tenancy](./multi-tenancy.md)** — the tenant isolation model the tenant-prefixed keys enforce.
