---
slug: /
sidebar_position: 1
title: Introduction
---

# DeviceChain

DeviceChain is a modern, cloud-native **IoT Application Enablement Platform** built in Go and React. It connects, manages, and processes data from large, heterogeneous device fleets — device lifecycle, telemetry ingestion, command & control, organizational modeling, and multi-tenancy — and exposes everything through a GraphQL API.

It is a ground-up rebuild of the SiteWhere platform that keeps the proven domain model while replacing the heavy Java/Spring stack with efficient, operationally simple microservices that run on any Kubernetes cluster.

## Why DeviceChain

- **Go-native microservices** — sub-second startup, small memory footprint, single-binary services.
- **Operator + CRDs** — a Kubernetes operator with a declarative `DeviceChainInstance` resource, not shell scripts; tenants are control-plane database records managed through the admin console.
- **GraphQL-first API** — introspectable and self-documenting; no generated client stubs.
- **A lean, fully open-source stack** — NATS JetStream is the entire messaging / MQTT / KV backbone, native JWT handles auth, TimescaleDB is the single data store, and OpenTofu provisions infrastructure. Two dependencies to run locally: **NATS + TimescaleDB**.
- **A uniform relationship model** — device context is a typed relationship graph rather than rigid assignments, so new entity types compose without schema churn.
- **Self-hosted and unmetered** — Apache-2.0 with no open-core split and no per-device pricing. The device inventory, twin state, command delivery, multi-tenancy, high availability, and SSO are part of the open platform, not a paid tier — run it inside your own environment with full data ownership.

## How the platform is organized

DeviceChain is a set of cooperating microservices over a shared core library:

- **event-sources** — pluggable inbound transports (MQTT today; HTTP, CoAP, WebSocket planned) that decode raw device messages onto the pipeline.
- **device-management** — devices, profiles, the relationship graph, and event resolution.
- **event-management** — persists resolved events to TimescaleDB and serves time-series queries.
- **user-management** — users, roles, and JWT issuance/validation.
- **operator (k8s)** — reconciles CRDs into the running platform.

See [Architecture](./concepts/architecture.md) for how these fit together, and the [Domain Model](./concepts/domain-model.md) for the core concepts.

## Project status

DeviceChain is pre-release and under active development. Pages in these docs mark whether a capability is **available**, **planned**, or **in design**. The [GitHub repository](https://github.com/devicechain-io/devicechain) is the source of truth for what currently builds and runs.

## License

Apache License 2.0.
