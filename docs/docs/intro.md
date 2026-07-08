---
slug: /
sidebar_position: 1
title: Introduction
---

# DeviceChain

DeviceChain is a modern, cloud-native **IoT Application Enablement Platform** built in Go and React. It connects, manages, and processes data from large, heterogeneous device fleets — device lifecycle, telemetry ingestion, command & control, organizational modeling, and multi-tenancy — and exposes everything through a GraphQL API and embeddable, version-controlled dashboards.

It is a ground-up rebuild of the SiteWhere platform that keeps the proven domain model while replacing the heavy Java/Spring stack with efficient, operationally simple microservices that run on any Kubernetes cluster.

## Why DeviceChain

- **Go-native microservices** — sub-second startup, small memory footprint, single-binary services.
- **Operator + CRDs** — a Kubernetes operator with a declarative `DeviceChainInstance` resource, not shell scripts; tenants are control-plane database records managed through the admin console.
- **GraphQL-first API** — introspectable and self-documenting; no generated client stubs.
- **A lean, fully open-source stack** — NATS JetStream is the entire messaging / MQTT / KV backbone, native JWT handles auth, TimescaleDB is the single data store, and OpenTofu provisions infrastructure. Two dependencies to run locally: **NATS + TimescaleDB**.
- **A uniform relationship model** — device context is a typed relationship graph rather than rigid assignments, so new entity types compose without schema churn.
- **Embeddable, versioned dashboards** — a canvas-first layout (layering, background images, per-breakpoint responsive) with built-in Apache ECharts widgets, live subscriptions, draft/publish/rollback versioning, and a runtime binding model (one definition + a host manifest → live on any device). Shipped as npm packages so any app can embed the viewer.
- **Self-hosted and unmetered** — Apache-2.0 with no open-core split and no per-device pricing. The device inventory, twin state, command delivery, dashboards, multi-tenancy, high availability, and SSO are part of the open platform, not a paid tier — run it inside your own environment with full data ownership.

## How the platform is organized

DeviceChain is a set of cooperating microservices over a shared core library:

- **event-sources** — pluggable inbound transports (MQTT today; HTTP, CoAP, WebSocket planned) that decode raw device messages onto the pipeline.
- **device-management** — devices, device types + versioned device profiles, the relationship graph, the alarm engine, and event resolution.
- **event-management** — persists resolved events to TimescaleDB and serves time-series queries (including live subscriptions over a graphql-ws bridge).
- **device-state** — the live last-known-state projection per device (current reading per measurement).
- **command-delivery** — persistent two-way command dispatch to devices.
- **dashboard-management** — versioned dashboard definitions (draft, publish/rollback, export) rendered by the embeddable widget packages.
- **notification-management** — routes triggered alarms to humans over email (SMTP) and webhook, with per-severity escalation.
- **user-management** — global identities, per-tenant memberships, the role catalog, and JWT issuance/validation.
- **operator (k8s)** — reconciles CRDs into the running platform.

See [Architecture](./concepts/architecture.md) for how these fit together, and the [Domain Model](./concepts/domain-model.md) for the core concepts.

## Trying it with simulated data

DeviceChain includes a **device-simulation** tool (`dcctl sim`) for standing up realistic demo data without physical hardware. It provisions a scenario's full topology — customers, areas, assets, and devices — then drives live telemetry and alarms into the platform over the **same device wire a real device uses**, so you can explore the console, dashboards, and queries against a moving fleet. A simulation authenticates as a scoped, single-tenant identity like any other external client — it has no special access to the platform.

## Project status

DeviceChain is pre-release and under active development. Pages in these docs mark whether a capability is **available**, **planned**, or **in design**. The [GitHub repository](https://github.com/devicechain-io/devicechain) is the source of truth for what currently builds and runs.

## License

Apache License 2.0.
