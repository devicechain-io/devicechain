---
slug: /
sidebar_position: 1
title: Introduction
---

# DeviceChain

DeviceChain is a cloud-native **IoT Application Enablement Platform** built in Go and React. It connects, manages and processes data from large, heterogeneous device fleets. It covers device lifecycle, telemetry ingestion, command and control, organizational modeling and multi-tenancy, and exposes all of it through a GraphQL API and embeddable, version-controlled dashboards.

DeviceChain is a ground-up rebuild of the SiteWhere platform. It keeps SiteWhere's proven domain model and replaces the heavy Java/Spring stack with efficient, operationally simple microservices that run on any Kubernetes cluster.

## Why DeviceChain

- **Go-native microservices.** Services start in under a second, use little memory and ship as single binaries.
- **Operator and CRDs.** DeviceChain uses a Kubernetes operator with a declarative `Instance` resource, not shell scripts. Tenants are control-plane database records that you manage through the admin console.
- **GraphQL-first API.** The API is introspectable and self-documenting, so you need no generated client stubs.
- **A lean, fully open-source stack.** NATS JetStream is the entire messaging, MQTT and KV backbone. Native JWT handles auth, PostgreSQL is the single data store (a relational database for entity data, and a second database with the TimescaleDB extension for time-series events), and OpenTofu provisions infrastructure. To run locally you need two dependencies: **NATS + TimescaleDB**.
- **A uniform relationship model.** Device context is a typed relationship graph rather than rigid assignments, so new entity types compose without schema churn.
- **Embeddable, versioned dashboards.** Dashboards use a canvas-first layout (layering, background images, per-breakpoint responsive) with built-in Apache ECharts widgets and live subscriptions. They have draft/publish/rollback versioning and a runtime binding model: one definition plus a host manifest goes live on any device. The viewer ships as npm packages, so any app can embed it.
- **Self-hosted and unmetered.** DeviceChain is Apache-2.0, with no open-core split and no per-device pricing. The device inventory, twin state, command delivery, dashboards, multi-tenancy, high availability and the OAuth 2.1 authorization server are part of the open platform, not a paid tier. You run it inside your own environment with full data ownership.

## How the platform is organized

DeviceChain is a set of cooperating microservices over a shared core library:

| Service | What it does |
| --- | --- |
| **event-sources** | Pluggable inbound transports (MQTT and HTTP today; WebSocket planned) that decode raw device messages onto the pipeline. |
| **sparkplug-ingest** _(opt-in)_ | An [Eclipse Sparkplug B](./concepts/sparkplug.md) Host Application. It joins your existing Sparkplug MQTT environment, feeds the same pipeline, and asserts authoritative device presence from the birth/death handshake. |
| **lwm2m-ingest** _(opt-in)_ | An [OMA LwM2M](./concepts/lwm2m.md) server that terminates CoAP/UDP over DTLS. It authenticates each device by its pre-shared-key identity and asserts presence from the registration lifecycle. |
| **device-management** | Devices, device types and versioned device profiles, the relationship graph, the alarm object and its lifecycle, and event resolution. |
| **event-processing** | The detection and action pipeline over resolved events. It runs streaming rules (threshold, duration, repeating, rate-of-change, absence, windowed aggregate, area correlation) that give the same result when events are replayed, and automated responses (raise alarm, send command, and outbound connectors). Detection lives here; the alarm object it raises stays in device-management. |
| **event-management** | Persists resolved events to TimescaleDB and serves time-series queries, including live subscriptions over a graphql-ws bridge. |
| **device-state** | The live last-known-state projection per device (the current reading per measurement). |
| **command-delivery** | Persistent two-way command dispatch to devices. |
| **dashboard-management** | Versioned dashboard definitions (draft, publish/rollback, export), rendered by the embeddable widget packages. |
| **notification-management** | Routes triggered alarms to people over email (SMTP) and webhook, with per-severity routing and escalation of alarms that stay unacknowledged and uncleared. |
| **outbound-connectors** | Delivers the pipeline's outbound actions (webhook `httpCall`, and `publish` to MQTT/Kafka/AWS SNS/SQS) to external systems over versioned, secret-authenticated connectors. |
| **ai-inference** _(opt-in)_ | Drafts a detection rule from a natural-language description and hands it to the same compiler people use. The AI only proposes; the compiler decides what is valid. The AI never runs in live detection, so a rule gives the same result when events are replayed. See [AI-assisted authoring](./concepts/ai-authoring.md). |
| **user-management** | Global identities, per-tenant memberships, the role catalog, JWT issuance and validation, and tenant tiers (the packaging a platform operator defines and assigns to tenants). |
| **mcp** _(opt-in)_ | A read-only [Model Context Protocol](./concepts/mcp.md) server that lets AI assistants (Claude, Cursor, VS Code) query a tenant on a user's behalf, under the user's own token. |
| **operator (k8s)** | Owns the `Instance` custom resource. Today its reconcile loop only observes the resource; the Helm chart renders the platform workloads. |

See [Architecture](./concepts/architecture.md) for how these fit together, the [Domain Model](./concepts/domain-model.md) for the core concepts, and [Event Processing & Alarms](./concepts/event-processing.md) for how telemetry becomes actionable signals.

## Simulated data {#trying-it-with-simulated-data}

You can explore DeviceChain without physical hardware using its **device-simulation** tool, `dcctl sim`. It provisions a scenario's full topology (customers, areas, assets and devices), then drives live telemetry and alarms into the platform over the **same device wire a real device uses**. You can then explore the console, dashboards and queries against a moving fleet.

A simulation authenticates as a scoped, single-tenant identity, like any other external client. It has no special access to the platform.

## Project status

DeviceChain is pre-release and under active development. Pages in these docs mark whether a capability is **available**, **planned** or **in design**. The [GitHub repository](https://github.com/devicechain-io/devicechain) is the source of truth for what currently builds and runs.

## License

Apache License 2.0.
