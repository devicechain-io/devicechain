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
| **operator** | A controller-runtime operator that reconciles `DeviceChainInstance` and `DeviceChainTenant` custom resources into Deployments and Services. |

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

Infrastructure (NATS, TimescaleDB, ingress, TLS) is provisioned by **OpenTofu** at cluster-creation time. The **operator** assumes that infrastructure exists and is responsible only for materializing DeviceChain workloads and maintaining their configuration. This separation keeps cluster bootstrapping out of application code. See [Deployment](../deployment/kubernetes-operator.md).

## API surface

All external APIs are **GraphQL** (one schema per service), which is introspectable and self-documenting. Internal service-to-service communication is asynchronous over NATS. There is no gRPC and no REST surface to maintain.
