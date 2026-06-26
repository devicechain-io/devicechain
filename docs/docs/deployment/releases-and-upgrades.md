---
sidebar_position: 3
title: Releases & Upgrades
---

# Releases & Upgrades

DeviceChain ships as a set of prebuilt, versioned container images plus a Helm chart.
You do **not** need to build anything to run it — pull a released version, install the
chart, and upgrade in place with zero downtime.

## Versioning model

Every release is a single semantic-version git tag (`vX.Y.Z`). That one version covers
**everything together** — each service image, the operator, the Helm chart, and the
`dcctl` CLI are all published at the same version. There is no per-service version skew to
reason about: a deployment is one coherent number.

- **Stable releases** are `vX.Y.Z` (e.g. `v1.2.0`). The `:latest` tag tracks the most
  recent stable release.
- **Pre-releases** are `vX.Y.Z-rc.N` (e.g. `v1.2.0-rc.1`). These never move `:latest`.

## Images

Images are published to the public GitHub Container Registry under
`ghcr.io/devicechain-io` — for example `ghcr.io/devicechain-io/device-management`. They are
multi-arch (`linux/amd64` and `linux/arm64`) and built on a distroless nonroot base, so
they run as an unprivileged user with no shell and a minimal attack surface.

Because the registry is public, no credentials are required to pull released images.

## Installing a specific version

Pin the image tag to the release you want:

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

The Helm chart itself is also published as an OCI artifact, so you can install it without a
checkout of the repository:

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version 1.2.0 \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

## Zero-downtime upgrades

Upgrading to a new version is a normal `helm upgrade`. The chart and services are built to
roll customers forward without dropping traffic:

```bash
helm upgrade dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.3.0
```

What makes the rollout safe:

- **Surge-before-terminate.** Each Deployment uses a `RollingUpdate` strategy with
  `maxUnavailable: 0` and `maxSurge: 1`, so a new pod must pass its `/readyz` readiness
  probe **before** an old pod is removed. Capacity never dips during the rollout.
- **Graceful shutdown / connection draining.** When a pod is asked to terminate it first
  reports "not ready" (so the Service stops routing new requests to it), waits a short
  drain window for that change to propagate, and only then finishes in-flight work and
  shuts down. Configure the window with `shutdownDrainSeconds` (default `5`), kept safely
  under `terminationGracePeriodSeconds` (default `30`).
- **Coordinated schema migrations.** Services run database migrations under a database-level
  lock, so when several replicas start at once exactly one applies migrations and the rest
  wait — no races, no duplicate DDL.

:::tip Run at least two replicas in production
For true zero-downtime, run `replicas: 2` (or more) for each area so the rollout always has
a live pod serving traffic. A single replica still has a brief gap while its one pod is
replaced. Set it globally with `--set replicas=2`, or per area under
`functionalAreas.<area>.replicas`. A `PodDisruptionBudget` is rendered automatically for any
area with more than one replica, so node drains can't evict every replica at once.
:::

## Data durability

The database tier is intentionally **lifecycle-independent** from the application: the
Postgres and TimescaleDB volumes are provisioned with a `Retain` policy and a destroy guard,
so they survive an application uninstall, a redeploy, or an accidental teardown. Upgrading or
reinstalling the application never puts your data at risk.

This is durability of the running volumes — it is not a substitute for scheduled backups and
point-in-time recovery, which are provisioned with the production infrastructure. See
[Deployment & Operator](./kubernetes-operator.md) for how the infrastructure and application
layers are separated.
