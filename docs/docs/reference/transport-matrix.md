---
sidebar_position: 2
title: Transport Capability Matrix
---

# Transport Capability Matrix

This page shows what each transport does today, in each direction, and names the gaps.

:::info The code is the source of truth
This page is maintained by hand against the implementation, not generated, so it can fall behind.
Where it and your instance disagree, your instance is right. Every claim below was read from the
service that implements it, not from a design document; anything that could not be established that
way is marked as such rather than filled in. A cell that overstates what the platform does is a bug
in this page — please [report it](../getting-help.md).
:::

## How to read this page

Every capability is in one of three states, and there is no fourth:

| | | |
| --- | --- | --- |
| ● | **Full** | Implemented, with no limitation specific to this direction. Ordinary caveats that apply to the whole transport are in its notes. |
| ◐ | **Partial** | Implemented, and something specific is missing. The note says what; read it before you design around the row. |
| ○ | **None** | Not implemented. Where that is a deliberate decision rather than unfinished work, the note says so — the two are very different things to plan against. On **Write**, the note also says what becomes of a command you issue anyway, because that differs from row to row. |
| — | **Not applicable** | The direction has no meaning for this row. It is not a synonym for "none", and it never stands in for "unknown" or "planned". |

One rule separates ● from ◐, and it is applied to every row on this page. A direction is ◐
whenever the platform can lose, truncate or refuse something **without telling anyone**, even where
the transport as a whole is the most complete one here. ● is reserved for a direction with no such
hole. That is the only reason HTTP ingest is `●` for Subscribe and the platform broker is not: over
the tenant's ingest limit, HTTP answers the publisher `429`, while the broker path drops the message
after the device has already been PUBACKed, and nothing reaches the publisher.

Directions are named from the platform's point of view:

- **Read** — the platform asks a device for a value and gets the answer in that exchange.
- **Write** — the platform sets a value on a device, or tells it to act.
- **Subscribe** — the device sends readings without being asked each time.

`—` never appears in the device-transport table. All three directions are meaningful for every
device transport, so **None** there is always a real answer to a real question.

## Device transports

How devices reach the platform, and how the platform reaches back.

| Transport | Read | Write | Subscribe |
| --- | :---: | :---: | :---: |
| [MQTT](../guides/connecting-a-device.md#mqtt) (platform broker) | ○ | ◐ | ◐ |
| [HTTP](../guides/connecting-a-device.md#http) | ○ | ○ | ● |
| MQTT (external broker) | ○ | ○ | ◐ |
| [Sparkplug B](../concepts/sparkplug.md) | ○ | ○ | ◐ |
| [LwM2M](../concepts/lwm2m.md) | ◐ | ◐ | ◐ |

### MQTT — the platform broker

The default path, and the most complete one. The broker is the MQTT server built into NATS, so
there is no separate broker to run.

- **Subscribe ◐** — the device publishes to its own events topic, and the broker captures the
  message durably before any platform code sees it. Authentication has two independent layers:
  the connection is authenticated at the broker and bound to that one device's subjects, and the
  event carries a credential that is checked again in the pipeline.

  There is one hole, and it is why this is not `●`. A message that arrives while the tenant is
  over its **ingest rate limit** is acknowledged to the broker and dropped. The device was
  PUBACKed when the broker captured it, so nothing tells the publisher; this transport has no
  `429` to send. If your fleet can burst past its limit, size it against the limit rather than
  relying on backpressure that does not exist.
- **Write ◐** — commands are delivered, but delivery is **live-only and unacknowledged**. A
  publish reaches a device that is connected and subscribed at that instant. The broker does not
  hold it for a device that is not, and nothing tells the platform whether the device received
  it. There is deliberately no `DELIVERED` state: confirming delivery separately from a response
  would need an acknowledgement this transport does not provide. A command is complete when the
  device answers it — see
  [responding to a command](../guides/connecting-a-device.md#responding-to-a-command).
- **Read ○** — there is no platform-initiated request/response primitive. You can express a read
  as a command whose response carries the value, but that is a vocabulary you define on the
  device profile, not something the transport provides.

### HTTP

A `POST` endpoint for the same JSON event body. Simple, and one-way.

- **Subscribe ●** — `POST /{instanceId}/{tenant}/events` returns:
  - `202` once the event is queued;
  - `400` on a body it cannot decode or a syntactically invalid tenant;
  - `429` when the tenant is over its ingest rate limit;
  - **`503` when the event could not be handed to the stream.**

  Retry on `503`: it is the platform telling you, on the only transport that can, that your data
  did not land. A `429` also means the event was not accepted; it carries a `Retry-After` header,
  so back off and retry. `202` and `400` are terminal for that request.
- **Write ○ / Read ○** — **there is no downlink at all.** A device that reaches the platform only
  over HTTP cannot be commanded. This is less a gap awaiting a fix than the shape of the
  integration: give a device that must receive commands an MQTT connection as well.

  A command issued to an HTTP-only device is **not refused — it expires**. The platform mints no
  transport name for a device that arrived over HTTP (the device's recorded source is the id the
  operator gave the event source), so the gate that recognises an undeliverable transport cannot
  recognise this one. The command is accepted, published where no device is subscribed to
  receive it, marked `SENT`, and ends as `TIMEOUT`. Sparkplug is the only row on this page where
  a `○` in this column produces a prompt `FAILED` instead.
- The ingest listener terminates plain HTTP and has no transport authentication of its own;
  device credentials ride in the event body. Where you need TLS, it comes from whatever fronts the
  service.

### MQTT — an external, operator-owned broker

The platform can also act as a client on a broker you already run, to ingest from it.

- **Subscribe ◐** — it works, but four things are missing, and all of them matter beyond a lab:
  - the connection is plaintext (no TLS);
  - it presents no broker credentials;
  - it is at-most-once in effect;
  - a message refused for being over a limit is dropped, with nothing sent back to the publisher.

  Prefer the platform broker unless you specifically need to read from an existing one.

  The at-most-once point matters if you are choosing a QoS on your own broker. The platform
  subscribes at QoS 1, so the loss is not in the subscription. The session is not persistent and
  the decode hand-off is in memory, so a message the platform has taken from your broker and not
  yet published is gone if the process restarts. Raising QoS on your side does not change that,
  and the platform makes no durability claim about a broker it does not own.

  After your broker restarts or the connection drops, the platform reconnects and subscribes
  again on its own. A broker that refuses the subscription — at startup or after a reconnect,
  for example because an ACL change denies the topic — stops the **whole event-sources service**
  with an error, rather than leaving it connected and ingesting nothing. That includes ingest from
  the platform broker and HTTP, not just this source: the service restarts and keeps failing until
  the broker grants the subscription again.
- **Write ○ / Read ○** — this integration is ingest only. A command issued to a device that
  arrives this way behaves exactly as it does for HTTP above: published, `SENT`, then `TIMEOUT`.

### Sparkplug B

For brownfield fleets already speaking Sparkplug to their own broker.

- **Subscribe ◐** — NBIRTH/NDATA/DBIRTH/DDATA are decoded, including alias tables and sequence
  tracking, and BIRTH/DEATH drive authoritative presence rather than presence inferred from a
  timeout. What keeps it from `●`: **only numeric metrics become measurements**. A boolean,
  string, byte-array, DataSet or Template metric is skipped as the payload is decoded, with
  nothing recorded and nothing said. If your fleet's interesting signals are booleans — a run
  flag, a fault bit — you will see a device that is authoritatively online and reporting nothing.
- **Write ○ — deliberately out of scope, not unfinished.** There is no Sparkplug command egress
  (`DCMD`), and none is waiting on work: a Sparkplug fleet sits on the *customer's* MQTT
  infrastructure, so nothing bridges the platform's command stream to it. A command issued to a
  Sparkplug device ends as `FAILED`, undeliverable, with two qualifications:
  - **The verdict is normally prompt, but not synchronous with the enqueue.** Enqueueing a command
    triggers an immediate dispatch attempt for that device. When it is the device's only queued
    command, the presence gate fails it right away. If the device already has other commands
    queued, that attempt stands down and the verdict lands on the next delivery sweep, which
    runs every 30 seconds by default (configurable between 5 and 300).
  - **It requires the presence gate to be configured.** The presence gate is the check that
    withholds or fails a command based on what the device's transport reports about it. It needs
    the cross-service secret and a `device-state` endpoint. Without either, it is off — it logs
    that at startup — and the command dispatches like any other and ends as `TIMEOUT` instead.
- **Read ○** — same reason.

What the platform *does* publish on Sparkplug: no `DCMD`, ever, and no `NCMD` reachable from the
command API. The single `NCMD` the platform emits is an internal `Node Control/Rebirth`, issued by
the Host Application's own session tracking to repair a sequence gap, at QoS 0 and unretained.

The Host Application does publish its own `STATE` message on `spBv1.0/STATE/{host_id}`, retained
and at QoS 1: when it connects, when it stops cleanly, and as its Last-Will if it dies. That is the
Sparkplug Host birth/death contract, and edge nodes read it.

:::warning Broker ACLs must allow the platform's `STATE` publish
If you write broker ACLs, grant the platform's client publish on `spBv1.0/STATE/{host_id}`. Deny it
and the host session is **broken from the first connection**.
:::

:::caution Sparkplug device identity is established at the broker, not per device
A Sparkplug device's identity comes from the topic it published on, so the per-event
device-authentication mode does **not** stop one publisher on your broker from sending under
another device's identity within the same tenant. Across tenants this cannot happen: tenancy is
fixed by the broker connection a message arrived on, never by anything in the message. Separation
within a tenant is enforced on *your* broker, with per-client credentials and topic permissions.
Size that before pointing a shared broker at a tenant.
:::

### LwM2M

For constrained devices over CoAP/UDP with DTLS.

- **Read ◐** — implemented as a device command, and the response body comes back. The limitation
  is that a body over **8 KiB is truncated** and the response is still reported as a success.
  "Capped" is the wrong mental model: nothing is refused and nothing marks the result as partial,
  so a large resource read comes back looking whole.
- **Write ◐** — a **single scalar resource** at a time. Writing an object instance, writing
  several resources in one operation, and partial update are not supported. Values are capped at
  8 KiB.
- **Subscribe ◐** — Observe works, but **only SenML-JSON notifications are decoded**, and of those
  **only numeric resources become measurements**. A boolean-primary object yields no telemetry at
  all — the same limit Sparkplug has.

  Size this up front: a conformant **LwM2M 1.0-only** client cannot produce SenML, so it
  correctly refuses the Observe. Such a device still registers, drives presence and accepts
  commands, but reports **no telemetry at all**. Decoding the older TLV format is the follow-up
  that closes this.

  Observed objects are restricted to a built-in allowlist that is not configurable, capped at 32
  observations per registration. Observations **do not survive a leader failover**: presence is
  reconstructed, and telemetry is re-established only as each device's registration renews.
- Commands to a sleeping device are held durably and drained when it next checks in, recorded
  as `PARKED`. This is the one place a command the transport has already tried to deliver is kept
  for the device instead of being lost. It is not the platform's only hold: for every transport —
  MQTT included, when the broker itself reports device presence — a command whose device the
  transport asserts is absent is withheld as `HELD` before it is published, and released when
  presence returns. See
  [commands to a device that is away](../concepts/commands.md#commands-to-a-device-that-is-away).

#### LwM2M operations in detail

| Operation | | Notes |
| --- | :---: | --- |
| Read | ◐ | CoAP GET; a response over 8 KiB is silently truncated and still reported successful |
| Write | ◐ | Single scalar resource, replace only |
| Execute | ● | With or without arguments |
| Observe | ◐ | SenML-JSON only; numeric resources only; fixed object allowlist; 32 per registration |
| Discover | ○ | Not implemented |
| Create | ○ | Not implemented |
| Delete | ○ | Not implemented |
| Write-Attributes | ○ | Not implemented — notification bands cannot be set from the platform |
| Bootstrap | ○ | Not implemented; a Bootstrap server is planned |

Device authentication is per device, at the DTLS handshake, using pre-shared keys. X.509 and
raw-public-key credentials are planned.

## Outbound connectors

Where the platform sends data when a rule fires. A connector is a one-way sink with no device
directions, so **Read** and **Subscribe** are `—` rather than `None`.

| Connector | Read | Write | Subscribe | Notes |
| --- | :---: | :---: | :---: | --- |
| `httpCall` webhook | — | ● | — | `POST` only; a non-`POST` method is refused |
| `publish` → MQTT | — | ● | — | QoS 0/1/2; username + secret; `tcp`, `mqtt`, `ssl`, `tls`, `mqtts`, `ws`, `wss` URLs; **no TLS settings** — see below |
| `publish` → Kafka | — | ● | — | TLS; SASL `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `publish` → AWS SNS | — | ● | — | Static per-tenant credentials only |
| `publish` → AWS SQS | — | ● | — | Static per-tenant credentials only |
| `publish` → Google Pub/Sub | — | ○ | — | **Creatable but not dispatchable** — see below |

Unlike Kafka, which has a real `tls` toggle, the MQTT connector config has **no TLS fields of any
kind**, and it rejects unknown keys, so there is nothing to author. TLS happens only implicitly,
when you give the broker an `ssl://`, `tls://`, `mqtts://` or `wss://` URL. The connection is then
verified against the public trust store and the host the URL names, with no way to supply a CA, a
client certificate, or a verification setting.

The two AWS connectors deliberately require a static access key and **will not** fall back to the
ambient IAM identity of the pod they run in. That separation exists so the platform's own cloud
identity is never borrowed to make a tenant's call.

Every connector destination is checked when the connection is made, and one that resolves to a
private or cloud-metadata address is refused unless an operator has explicitly allowed that
address — see
[where a connector may send](../concepts/outbound-connectors.md#destinations).

:::warning A Google Pub/Sub connector can be created and will never send
`gcp_pubsub` is a valid connector type: the API accepts it, and the connector saves and publishes
like any other. It has **no delivery implementation** in this release, so every dispatch to it
fails terminally and is dead-lettered — recognized but not executable, never silently dropped.
When it ships, it will authenticate with a credential stored on the connector, as the AWS
connectors do, never with the pod's identity.
:::

Separately from connectors, [notification channels](../guides/notification-channels.md) reach
people rather than systems, over SMTP and webhook.

## Not available

These are named explicitly because, from outside the platform, "absent from the list above" and
"asked for and answered no" look the same — and only one of them is worth waiting for.

| | |
| --- | --- |
| WebSocket device ingest | Not available. Named as planned in the [introduction](../intro.md). |
| CoAP outside LwM2M | Not available. CoAP reaches the platform through [LwM2M](../concepts/lwm2m.md) and not otherwise. |
| Raw NATS as a device transport | Not available. A device credential authorizes an MQTT connection, and there is no NATS-native device client. |
| Sparkplug command egress (`DCMD`) | Not available, and deliberately out of scope rather than pending — see [above](#sparkplug-b). |
| Industrial fieldbus protocols — OPC-UA, Modbus, BACnet | Not available as platform transports, and **nothing the project ships speaks them.** The supported shape is a local gateway that speaks the fieldbus on the plant network and forwards over MQTT or HTTP; you supply the protocol translation. The project does ship `dc-edge-agent`, which does the *other* half of that job: it terminates the device MQTT path locally, spools durably across a WAN outage, and forwards **over MQTT only**. It speaks no fieldbus itself. |
