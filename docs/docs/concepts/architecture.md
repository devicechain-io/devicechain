---
title: Architecture
---

# Architecture

DeviceChain is a set of stateless Go microservices built on a shared core library. A Kubernetes operator coordinates them, and NATS JetStream connects them. A single instance serves all tenants (a shared-microservice model). Tenant isolation is enforced at the messaging and storage layers, not by running separate pods per tenant.

## Components {#components}

| Component | Responsibility |
|---|---|
| `event-sources` | Inbound device transports. Decodes raw messages (JSON today; Protobuf and custom decoders planned), applies a per-tenant ingest rate limit, and publishes them onto the pipeline. |
| `device-management` | Devices, device types and versioned device profiles, the typed relationship graph, the alarm object and its lifecycle, and event resolution (attaching device and organizational context to each event). |
| `event-processing` | Detection and actions. A streaming core, which gives the same result when events are replayed, evaluates detection rules over resolved events: threshold, duration, repeating, rate-of-change, absence, connectivity, windowed aggregate and area correlation. It then dispatches automated actions: raise alarm, send command, and outbound connectors. Detection lives here. The alarm object it raises stays in `device-management`, and connector delivery is handed off to `outbound-connectors`. |
| `event-management` | Persists resolved events to TimescaleDB, applies the data-lifecycle policies (compression, retention, rollups), and serves time-series queries over GraphQL. |
| `device-state` | The live last-known-state projection per device: presence and the current reading per measurement. |
| `command-delivery` | Persistent, two-way command dispatch to devices, tracked through a per-command lifecycle. It sends to one device at a time, or to a whole fleet as a single recorded batch. |
| `dashboard-management` | Versioned dashboard definitions (draft, publish, rollback), stored opaquely and rendered by the React runtime packages in this repository's frontend workspace. Exporting a definition is a console-side feature, not a service operation. |
| `notification-management` | Routes triggered alarms to humans, using per-tenant policy over email (SMTP) and webhook. A policy routes alarms by severity and can re-notify, at a set interval, an alarm that stays unacknowledged and uncleared. |
| `user-management` | Global identities, per-tenant memberships, the role catalog, and JWT issuance and validation. |
| `sparkplug-ingest` _(opt-in)_ | A stateful Sparkplug B Host Application. It connects *out* to per-tenant customer MQTT brokers, runs the Sparkplug session machine, and feeds this pipeline, including authoritative device presence. One replica serves at a time, elected through a fenced lease. See [Sparkplug B](./sparkplug.md). |
| `lwm2m-ingest` _(opt-in)_ | Terminates OMA LwM2M over CoAP/UDP with DTLS. Devices connect *in* and are identified by their authenticated DTLS PSK identity. Registration drives presence, observed resources decode to measurements, and reads, writes, executes and firmware update go through the command and update paths. See [LwM2M](./lwm2m.md). |
| `ai-inference` _(opt-in)_ | Drafts a detection rule from a natural-language description and runs it through the *same* compiler the other authoring surfaces use. The model proposes a rule; the compiler decides whether it is valid. It never sits in the path that evaluates rules. See [AI Authoring](./ai-authoring.md). |
| `outbound-connectors` _(opt-in)_ | Delivers outbound actions to external systems: an HTTP/webhook call, and a `publish` to message brokers and cloud queues (MQTT, Kafka, AWS SNS/SQS). It uses tenant-scoped, versioned connectors whose credentials are held in the secret store. It runs in its own process, so a slow or misbehaving external system can't affect the detection pipeline. See [Outbound Connectors](./outbound-connectors.md). |
| `mcp` _(opt-in)_ | A read-only Model Context Protocol server that lets AI assistants operate a tenant on a user's behalf. It is a thin OAuth 2.1 resource server over the GraphQL API that carries the caller's own tenant-scoped token. It has no service token and offers curated read tools only. See [AI Access (MCP)](./mcp.md). |
| `operator` | A controller-runtime operator that owns the `Instance` custom resource and its lifecycle. Today its reconcile loop only observes the resource; readiness-status aggregation across the rendered Deployments is a planned follow-up on that loop. Config is not reloaded in place: a service reads its configuration once at startup, and a change is adopted by rolling the pods. The Helm chart renders the workloads themselves. Tenants are control-plane database records, not reconciled resources. |

Fanning one command out to many devices is part of `command-delivery`, not a separate service. See [One command, many devices](./commands.md#command-batches). Scheduling is still planned; see the repository for current status.

## Data and messaging backbone {#the-data-and-messaging-backbone}

**NATS JetStream** is the single backbone for three things:

- asynchronous messaging
- MQTT ingress: devices connect to NATS' built-in MQTT server on port 1883
- key-value caching and locking

There is no separate Kafka, Redis or MQTT broker.

**PostgreSQL** stores everything, in two separate databases with one shape:

- The *relational* store holds entity data: tenants, users, devices, relationships.
- The *event* store adds the TimescaleDB extension. It keeps time-series events in hypertables with compression and continuous aggregates.

`event-management` owns the event store's schema. It is the only service that writes events to it or serves them from it. The one other service that connects to it is `user-management`'s tenant-purge coordinator, and only to delete a removed tenant's rows. That is why the two stores can be backed up, sized and restored independently.

Both stores run as PostgreSQL clusters managed by an operator, so high availability is an instance count rather than a different storage tier: one instance each by default, three each on a high-availability install. With replicas, the two replicate differently:

- The relational store holds a write until a replica confirms it.
- The event store replicates the same way while a replica is available, but falls back to asynchronous replication when none is, because unpersisted events are still held durably in the messaging layer and can be replayed.

Both stores also archive continuously, on by default: a stream of write-ahead log plus scheduled base backups, each to its own separate bucket. So each is restorable to any point inside a retention window, not only to last night. Restoring is a property of *creating* a cluster, not an operation against a running one; see [Disaster Recovery](../deployment/disaster-recovery.md).

Subjects are scoped per tenant (`{instance}.{tenant}.{suffix}`), and event data is partitioned by tenant in the database. That is how a shared set of services safely serves many tenants.

## The event pipeline {#the-event-pipeline}

```
device → MQTT/NATS → event-sources → (decoded event)
       → device-management → (resolved event: device + relationship context attached)
       → event-management → TimescaleDB
```

During resolution, `device-management` looks up the device's **tracked** relationships and attaches them to the event as index dimensions. Downstream queries such as "all events for customer X" then need no joins. See the [Domain Model](./domain-model.md).

## Deployment model

OpenTofu provisions infrastructure in two layers:

1. What every instance on a cluster shares (the relational database, ingress, TLS, monitoring), once, when the cluster is installed.
2. Each instance's own broker (NATS) and event store (TimescaleDB), when that instance is bootstrapped.

A Helm chart renders the platform workloads: one Deployment and Service per enabled functional area. You select the areas with a deployment **profile** (`default` / `full` / `telemetry` / `ingest-only`) or an explicit set. A dependency gate rejects an invalid selection at install time.

The operator assumes the infrastructure exists and handles the `Instance` lifecycle rather than creating the workloads. Tenants are control-plane database records, not reconciled resources. This separation keeps cluster bootstrapping out of application code. See [Deployment](../deployment/kubernetes-operator.md).

## Configuration, health, and startup

Each service loads its configuration into a typed schema and refuses to start on a bad one. An unknown or misspelled key, a wrong type, or an invalid value is rejected at startup rather than silently ignored. A bad config surfaces immediately instead of as wrong behavior later.

Every service exposes two HTTP endpoints for Kubernetes:

- **`/healthz`** (liveness) returns `200` while the process can still do its job. It returns `503` once the service's message-broker connection has closed permanently without the service asking — for example, after the broker credential changed under a running pod. Kubernetes then restarts the pod, and it reconnects with the credential it is given.
- **`/readyz`** (readiness) returns `503` until the service's authentication is live, then `200`. It returns `503` again while the service shuts down, and whenever `/healthz` does.

Services start **not ready** and fetch the JWT signing keys from `user-management` in the background. While a service is not ready, it is pulled from Service endpoints and its message consumers stay paused. A brief `user-management` outage therefore degrades a service rather than crashing it, and no request or message is ever processed without verified authentication.

## Secret handling {#secret-handling}

Some values are never stored in plaintext config or a reversible column:

- integration and provider credentials, such as an SMTP password, a webhook bearer token, or an outbound connector's broker or cloud credential
- the private half of the key that signs every sign-in token

These live in an **encrypted secret store**. Each value is sealed at rest with its own AES-256-GCM data key, and that data key is wrapped by a key-encryption key (KEK). The default KEK is a root key on the instance's existing Kubernetes Secret, so you get encryption at rest with no additional infrastructure.

The store is built around a pluggable key provider, so an external key manager can take over wrapping without changing how a consumer stores or resolves a handle. The instance root key is the only provider that ships today.

A consumer stores only an opaque **handle**. The value is write-only over the API: it is resolved inside the server at use time and never returned as cleartext. Secret changes are audited (who, when, which handle — never the value).

## API surface

The platform's data and admin APIs are GraphQL, which is introspectable and self-documenting. Each service that exposes an API serves its own schema; `user-management` and `ai-inference` also serve separate administrative schemas. There is no gRPC, and no REST API over the domain to maintain alongside the schemas.

Most traffic between services is asynchronous over NATS. When a service needs an answer or an action from another service at the moment it acts, it calls that service's GraphQL endpoint directly, using a short-lived service token. Checking that a device exists before a command is enqueued is one example; a detection rule sending a command is another.

Apart from the `/healthz`, `/readyz` and `/metrics` endpoints every service serves, and the `/graphiql` explorer served only when developer tools are enabled, the non-GraphQL HTTP endpoints are the ones a standard dictates, plus three narrow ones. The standard ones are:

- the JWKS document that token validators fetch
- the OAuth 2.1 authorization-server endpoints (`/oauth/authorize`, `/oauth/token`, `/oauth/userinfo`, `/oauth/jwks` and the RFC 8414 metadata document), which `user-management` serves once an issuer URL is configured
- the JSON-RPC-over-HTTP transport spoken by the opt-in [MCP server](./mcp.md), and the protected-resource metadata document (RFC 9728) it publishes

The three others are:

- the device-facing HTTP event-ingest endpoint (`POST /{instance}/{tenant}/events`), which `event-sources` serves on its own port; the default configuration includes an HTTP source
- the tenant logo upload and download endpoint (`/branding/logo`) on `user-management`
- the endpoint where services obtain their short-lived service token (`/auth/service-token`) on `user-management`
