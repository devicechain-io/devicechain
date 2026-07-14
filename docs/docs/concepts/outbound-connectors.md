---
sidebar_position: 6
title: Outbound Connectors
---

# Outbound Connectors

Detection is only half of automation — the other half is **acting on the outside world**. When a [detection rule](./event-processing.md) fires, its REACT actions can reach beyond the platform: call a webhook, or publish a message to a broker or cloud queue. These **outbound connectors** are how DeviceChain fans processed events out to the systems you already run — an incident tool, a data pipeline, another application's message bus.

Outbound delivery is handled by a dedicated **outbound-connectors** service, kept separate from the detection engine on purpose: a slow or misbehaving external endpoint can back up its own delivery without ever slowing down rule evaluation.

:::note Status
**Available today:** the `httpCall` webhook action, and a `publish` action delivering to **MQTT**, **Apache Kafka**, **AWS SNS**, and **AWS SQS** through a tenant-scoped, versioned connector with credentials held in the encrypted secret store. Additional `publish` targets (Google Cloud Pub/Sub, RabbitMQ, Azure, NATS, Redis, Slack, Splunk) and a visual **publish** node in the automation canvas are planned behind the same model — this repository is the source of truth for what currently builds.
:::

## The two outbound actions

Both are [REACT actions](./event-processing.md#automated-actions) — you add them to a rule alongside *raise alarm* and *send command*, and each can be **guarded** by a condition on the firing.

### `httpCall` — call a webhook

A direct HTTP request to an endpoint you specify. The request body is shaped with a **CEL expression** over the firing, so you send exactly the fields the receiver expects. Everything the action needs — URL, method, headers, body template — lives on the action itself, so a one-off webhook needs no separate setup. Optional authentication (a bearer token, an API key header) is stored in the **secret store** and attached at send time.

Webhook delivery is **hardened**: it refuses to follow redirects, strips reserved and platform headers, and — when a secret is attached — does not echo the response body back into logs, so a misconfigured target can't be used to probe internal addresses or leak the credential.

### `publish` — send to a connector

For message brokers and cloud queues, the target is a reusable **connector** (below) rather than inline config. You pick a registered connector and shape the message payload in CEL; the connector carries the destination and its sealed credential. One connector — configured and credentialed once — is reused across as many rules as you like, and the credential never appears in a rule.

A single generic `publish` action covers every broker/queue type: the **connector's type** selects the transport. Supported types today are `mqtt`, `kafka`, `aws_sns`, and `aws_sqs`.

## Connectors are versioned resources

A connector is a **tenant-scoped resource** with the same lifecycle as a [device profile](./domain-model.md) or a [dashboard](./dashboards.md): you edit a **draft**, **publish** an immutable version, and **roll back** to an earlier one if a change misbehaves. A connector holds:

- a **type** (`mqtt`, `kafka`, `aws_sns`, `aws_sqs`),
- the **destination config** for that type (broker addresses, topic/queue/ARN, and options like QoS or TLS), and
- an optional **credential**, referenced by handle — the value is written into the secret store and **never returned in cleartext**, exactly like a notification channel's secret.

Because connectors are tenant-level, one tenant never sees or sends through another's connectors.

## How delivery works

When a guarded `publish` (or `httpCall`) action fires, REACT does not make the outbound call itself. It hands a **dispatch request** — the resolved action plus an idempotency key — to the outbound-connectors service over the internal message bus, and returns to detecting. The dispatch request is **durable**: if the connector service restarts, the request survives and is delivered on recovery.

Two properties keep this safe:

- **Fire-and-forget, exactly-shaped.** An outbound action does not block the rule waiting for a reply. Payloads are shaped only with CEL — there is no arbitrary scripting in the delivery path — so what a rule can send is bounded and reviewable.
- **Idempotent by construction.** Each dispatch carries a content-addressed **idempotency key** derived from the firing, so if a detection is replayed or a delivery is retried, the receiver can recognize and drop the duplicate — a redelivery never means a double-send.

## Governance

Every outbound action is subject to **per-tenant governance**, because an external call is more expensive — and easier to turn into a self-inflicted flood — than an in-process one. Outbound volume is rate-limited per tenant at both ends of the hop: REACT sheds over-budget emissions before they are dispatched, and the connector service admits sink traffic within a bounded budget. A tenant with no configured limit falls back to a platform default that is **never unlimited**. Shed volume is surfaced as an operational metric so an operator can see a rule that has started to over-emit.

## Isolation and dependencies

The outbound-connectors service runs in its **own process**, separate from event-processing. That boundary is deliberate:

- A cloud SDK or broker client that hangs, crashes, or leaks memory affects only connector delivery — never detection.
- The broker/cloud client libraries that back `publish` are linked **only** into this service, so the replay-correct detection engine stays lean and its dependency surface small.
- The service resolves connector credentials itself; the detection engine never holds them.

## Related

- **[Event Processing & Alarms](./event-processing.md)** — where rules and their REACT actions are authored.
- **[Architecture](./architecture.md)** — where outbound-connectors sits among the services.
- Credentials are held in the encrypted **secret store** described under [Secret handling](./architecture.md#secret-handling).
