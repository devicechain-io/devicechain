---
sidebar_position: 1
title: Architecture
---

# Architecture

DeviceChain is a set of stateless Go microservices over a shared core library, coordinated by a Kubernetes operator and connected by NATS JetStream. A single instance serves all tenants (a shared-microservice model), with tenant isolation enforced at the messaging and storage layers rather than by running separate pods per tenant.

## Components

| Component | Responsibility |
|---|---|
| **event-sources** | Inbound device transports. Decodes raw messages (JSON today; Protobuf and custom decoders planned), applies a per-tenant ingest rate limit, and publishes them onto the pipeline. |
| **device-management** | Devices, device types + versioned device profiles, the typed relationship graph, the alarm object and its lifecycle, and event resolution (attaching device + organizational context to each event). |
| **event-processing** | The DETECT + REACT pipeline: a replay-correct streaming core evaluates detection rules over resolved events (threshold, duration, repeating, rate-of-change, absence, windowed aggregate, area correlation) and dispatches automated actions (raise alarm, send command, and outbound connectors). Detection lives here; the alarm object it raises stays in device-management, and connector delivery is handed off to outbound-connectors. |
| **event-management** | Persists resolved events to TimescaleDB, applies the data-lifecycle policies (compression / retention / rollups), and serves time-series queries over GraphQL. |
| **device-state** | The live last-known-state projection per device — presence, latest location, and current reading per measurement. |
| **command-delivery** | Persistent, two-way command dispatch to devices, tracked through a per-command lifecycle. |
| **dashboard-management** | Versioned dashboard definitions (draft, publish / rollback, export), rendered by the embeddable widget packages. |
| **notification-management** | Routes triggered alarms to humans — per-tenant policy over email (SMTP) and webhook, with per-severity escalation. |
| **user-management** | Global identities, per-tenant memberships, the role catalog, and JWT issuance/validation. |
| **outbound-connectors** | Delivers REACT's outbound actions to external systems — an HTTP/webhook call and a `publish` to message brokers and cloud queues (MQTT, Kafka, AWS SNS/SQS) — over tenant-scoped, versioned connectors with credentials held in the secret store. Runs in its own process so a slow or misbehaving external system can't touch the detection pipeline. See [Outbound Connectors](./outbound-connectors.md). |
| **operator** | A controller-runtime operator that manages the `DeviceChainInstance` lifecycle (status aggregation, config hot-reload). Workloads themselves are rendered by the Helm chart; tenants are control-plane database records, not reconciled resources. |

Additional services — batch operations and scheduling — are planned. See the repository for current status.

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

Infrastructure (NATS, TimescaleDB, ingress, TLS) is provisioned by **OpenTofu** at cluster-creation time. A **Helm chart** renders the platform workloads — one Deployment + Service per enabled functional area, selected by a deployment **profile** (`full` / `telemetry` / `ingest-only`) or an explicit set, with a dependency gate that rejects an invalid selection at install time. The **operator** assumes infrastructure exists and handles the `DeviceChainInstance` lifecycle rather than stamping workloads (tenants are control-plane database records, not reconciled resources). This separation keeps cluster bootstrapping out of application code. See [Deployment](../deployment/kubernetes-operator.md).

## Configuration, health, and startup

Each service loads its configuration into a typed schema and **fails closed**: an unknown or misspelled key, a wrong type, or an invalid value is rejected at startup rather than silently ignored, so a bad config surfaces immediately instead of as wrong behavior later.

Every service exposes two HTTP endpoints for Kubernetes:

- **`/healthz`** (liveness) — returns `200` whenever the process is running.
- **`/readyz`** (readiness) — returns `503` until the service's authentication is live, then `200`.

Services start in a **not-ready** state and fetch the JWT signing keys from `user-management` in the background. While not ready, a service is pulled from Service endpoints and its message consumers stay paused — so a brief `user-management` outage degrades a service rather than crashing it, and no request or message is ever processed without verified authentication.

## Secret handling

Integration and provider credentials — an SMTP password, a webhook bearer token, an outbound-connector's broker or cloud credential — are never stored in plaintext config or a reversible column. They live in an **encrypted secret store**: each value is sealed at rest with a per-secret AES-256-GCM data key wrapped by a key-encryption key (KEK), where the default KEK is a root key on the instance's existing Kubernetes Secret — encryption-at-rest with no additional infrastructure, and cloud KMS / HashiCorp Vault are drop-in alternatives for regulated deployments. A consumer stores only an opaque **handle**; the value is **write-only over the API** and resolved server-internally at use time, never returned as cleartext. Secret mutations are audited (who, when, which handle — never the value).

## API surface

All external APIs are **GraphQL** (one schema per service), which is introspectable and self-documenting. Internal service-to-service communication is asynchronous over NATS. There is no gRPC and no REST surface to maintain.
