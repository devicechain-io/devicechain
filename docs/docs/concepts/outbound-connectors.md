---
title: Outbound Connectors
---

# Outbound Connectors

Detection is only half of automation — the other half is **acting on the outside world**. When a [detection rule](./event-processing.md) fires, its REACT actions can reach beyond the platform: call a webhook, or publish a message to a broker or cloud queue. These **outbound connectors** are how DeviceChain fans processed events out to the systems you already run — an incident tool, a data pipeline, another application's message bus.

Outbound delivery is handled by a dedicated **outbound-connectors** service, kept separate from the detection engine on purpose: a slow or misbehaving external endpoint can back up its own delivery without ever slowing down rule evaluation.

:::info The service is opt-in
`outbound-connectors` ships in the `full` [deployment profile](../deployment/kubernetes-operator.md), not in `default` — so an instance brought up without naming a profile does not run it. The console still shows the **Connectors** section, and its pages explain that this instance does not run the area rather than pretending the feature is missing.

Everything on this page applies once the area is deployed. To add it, bring the instance up on the `full` profile.
:::

:::note Status
**Available today:** the `httpCall` webhook action, and a `publish` action delivering to **MQTT**, **Apache Kafka**, **AWS SNS**, and **AWS SQS** through a tenant-scoped, versioned connector with credentials held in the encrypted secret store. Both outbound actions are configurable as **action nodes on the automation canvas**. A `gcp_pubsub` connector can be created through the API but cannot be dispatched yet — a `publish` to one is dead-lettered as unsupported, and the console does not offer the type. Additional `publish` targets (RabbitMQ, Azure, NATS, Redis, Slack, Splunk) are planned behind the same model — this repository is the source of truth for what currently builds.
:::

## The two outbound actions

Both are [REACT actions](./event-processing.md#automated-actions), authored on the **automation canvas** alongside *raise alarm* and *send command*, and each can be **guarded** by a condition on the firing. The form builder's action picker offers only *raise alarm* and *send command*; a rule that already carries an outbound action opens in the form with that action shown read-only and preserved, so switching surfaces never drops it.

### `httpCall` — call a webhook

A direct HTTP request to an endpoint you specify. The request body is shaped with a **CEL expression** over the firing, so you send exactly the fields the receiver expects. Everything the action needs — URL, method, headers, body template — lives on the action itself, so a one-off webhook needs no separate setup. Optional authentication is a **secret handle**: the token is stored in the **secret store** and presented at send time as an `Authorization: Bearer <token>` header — the header name and scheme are not configurable, so a receiver that expects a custom API-key header cannot be authenticated this way.

Webhook delivery is **hardened** in specific ways: it refuses to follow redirects (so an external endpoint cannot `3xx` the request somewhere else), allows only `http`/`https` targets and rejects URL-embedded credentials, strips reserved and platform headers (so a tenant-supplied header cannot forge the auth header or the internal service identity), validates every header name and value against the wire grammar — which forbids the CR/LF that header injection depends on — and, when a secret is attached, does not echo the response body back into logs, so a hostile endpoint cannot reflect the credential into them.

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

## Where a connector may send {#destinations}

Every connection a connector makes is checked at the moment it is made, on the address the
destination actually resolved to. A destination that resolves to a **loopback, private,
carrier-grade NAT, link-local or cloud-metadata address is refused**, whatever name it was
given. The check applies the same way to webhooks, MQTT, Kafka, SNS and SQS:

- **The refusal is final.** A refused dispatch is dead-lettered with the outcome `blocked` and is
  not retried, because waiting does not make an address public. A destination that is merely down
  is different: that is an ordinary failure and is retried.
- **MQTT broker URLs** must use `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://` or
  `wss://`, with an explicit port and one broker per entry. Any other scheme — `unix://` included —
  is refused when the connector is saved, and again if a stored connector with one is dispatched.
- **A dispatch is `blocked` as soon as any address it tries is refused** — and an address is only
  judged when it is tried. With a list of brokers, the outcome can therefore depend on which one
  the client reaches first: an MQTT client that connects to an allowed first broker delivers, and
  meets a refused one only when the first is down; a Kafka client picks its first seed at random.
  List only destinations that are allowed.
- **Kafka** addresses are `host:port`. Every broker the cluster **advertises** in its metadata is
  checked too, not only the addresses you configured.
- **SNS and SQS** endpoint overrides are checked like any other destination. Without an override the
  connector talks to the regional AWS endpoint, which is checked as well.
- **The service's own environment is not consulted.** Proxy variables (`HTTPS_PROXY`,
  `ALL_PROXY`, …) are not used, and neither are `AWS_*` variables, AWS configuration files or the
  pod's cloud identity. A connector reaches exactly the destination it names, with the credential it
  carries.

To let connectors reach a private destination — an in-cluster broker, **Amazon MSK**, **Amazon MQ**,
or SNS/SQS through an **interface VPC endpoint with private DNS** (which makes even the default
regional names resolve to private addresses) — an operator lists each address as its own `/32`
under `instance.config.infrastructure.egress.allowedDestinations`. An interface endpoint has one
address per availability zone, and each needs its own entry. An allowance applies to **every tenant
and every connector and webhook**, not only the one it was added for.

A `blocked` outcome tells a tenant only that the destination resolved to a refused address — the
same thing a webhook refusal says. The refused address itself is recorded in the dead letter, which
operators can read and tenants cannot.

## Governance {#governance}

Every outbound action is subject to **per-tenant governance**, because an external call is more expensive — and easier to turn into a self-inflicted flood — than an in-process one. Outbound volume is rate-limited per tenant at both ends of the hop: REACT sheds over-budget emissions before they are dispatched, and the connector service admits sink traffic within a bounded budget. A tenant with no configured limit falls back to a platform default that is **never unlimited**. REACT and the connectors service both meter a tenant's outbound actions on the time the triggering telemetry reached the platform, so a detection backlog drained after a restart or failover is neither mistaken for a flood nor slowed to the tenant's ceiling. An action still over budget is recorded as a dead letter with reason `shed`: recorded, not retried. Past a per-tenant budget of about one letter a second, shed actions are counted and summarised in one letter per tenant per minute. Both ends enforce the ceiling per replica of their service; see [Ceilings are per replica](./governance.md#per-replica).

## Isolation and dependencies

The outbound-connectors service runs in its **own process**, separate from event-processing. That boundary is deliberate:

- A cloud SDK or broker client that hangs, crashes, or leaks memory affects only connector delivery — never detection.
- The broker/cloud client libraries that back `publish` are linked **only** into this service, so the replay-correct detection engine stays lean and its dependency surface small.
- The service resolves connector credentials itself; the detection engine never holds them.

## Related

- **[Event Processing & Alarms](./event-processing.md)** — where rules and their REACT actions are authored.
- **[Architecture](./architecture.md)** — where outbound-connectors sits among the services.
- Credentials are held in the encrypted **secret store** described under [Secret handling](./architecture.md#secret-handling).
