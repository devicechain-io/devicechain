---
sidebar_position: 1
title: Architecture
---

# Architecture

DeviceChain is a set of stateless Go microservices over a shared core library, coordinated by a Kubernetes operator and connected by NATS JetStream. A single instance serves all tenants (a shared-microservice model), with tenant isolation enforced at the messaging and storage layers rather than by running separate pods per tenant.

## Components

| Component | Responsibility |
|---|---|
| **event-sources** | Inbound device transports. Decodes raw messages (JSON today; Protobuf and custom decoders planned) and publishes them onto the pipeline. |
| **device-management** | Devices, device profiles, the typed relationship graph, and event resolution (attaching device + organizational context to each event). |
| **event-management** | Persists resolved events to TimescaleDB and serves time-series queries over GraphQL. |
| **user-management** | Users, roles, and JWT issuance/validation. |
| **operator** | A controller-runtime operator that manages `DeviceChainInstance` and `DeviceChainTenant` lifecycle (tenant bootstrap, status, config hot-reload). Workloads themselves are rendered by the Helm chart. |

Additional services — command delivery, device state, device registration, outbound connectors, batch operations, and scheduling — are planned. See the repository for current status.

## The data and messaging backbone

- **NATS JetStream** is the single backbone for asynchronous messaging, the MQTT ingress (devices connect to NATS' built-in MQTT server on port 1883), and key-value caching / locking. There is no separate Kafka, Redis, or MQTT broker.
- **TimescaleDB** (PostgreSQL + the TimescaleDB extension) is the single data store for both relational entity data and time-series events. Events live in hypertables with compression and continuous aggregates.

Subjects are scoped per tenant (`{instance}.{tenant}.{suffix}`) and event data is partitioned by tenant in the database, which is how a shared set of services safely serves many tenants.

## The event pipeline

```
device → MQTT/NATS → event-sources → (decoded event)
       → device-management → (resolved event: device + relationship context attached)
       → event-management → TimescaleDB
```

During resolution, device-management looks up the device's **tracked** relationships and attaches them to the event as index dimensions, so downstream queries like "all events for customer X" need no joins. See the [Domain Model](./domain-model.md).

## Deployment model

Infrastructure (NATS, TimescaleDB, ingress, TLS) is provisioned by **OpenTofu** at cluster-creation time. A **Helm chart** renders the platform workloads — one Deployment + Service per enabled functional area, selected by a deployment **profile** (`full` / `telemetry` / `ingest-only`) or an explicit set, with a dependency gate that rejects an invalid selection at install time. The **operator** assumes infrastructure exists and handles instance/tenant lifecycle rather than stamping workloads. This separation keeps cluster bootstrapping out of application code. See [Deployment](../deployment/kubernetes-operator.md).

## Configuration, health, and startup

Each service loads its configuration into a typed schema and **fails closed**: an unknown or misspelled key, a wrong type, or an invalid value is rejected at startup rather than silently ignored, so a bad config surfaces immediately instead of as wrong behavior later.

Every service exposes two HTTP endpoints for Kubernetes:

- **`/healthz`** (liveness) — returns `200` whenever the process is running.
- **`/readyz`** (readiness) — returns `503` until the service's authentication is live, then `200`.

Services start in a **not-ready** state and fetch the JWT signing keys from `user-management` in the background. While not ready, a service is pulled from Service endpoints and its message consumers stay paused — so a brief `user-management` outage degrades a service rather than crashing it, and no request or message is ever processed without verified authentication.

## API surface

All external APIs are **GraphQL** (one schema per service), which is introspectable and self-documenting. Internal service-to-service communication is asynchronous over NATS. There is no gRPC and no REST surface to maintain.
