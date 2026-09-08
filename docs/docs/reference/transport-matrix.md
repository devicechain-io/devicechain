---
sidebar_position: 2
title: Transport Capability Matrix
---

# Transport Capability Matrix

What each transport genuinely does today, per direction, with the gaps named.

:::info The code is the source of truth
This page is maintained by hand against the implementation. It is not generated, so it can
fall behind — and where it and your instance disagree, **your instance is right**. Every claim
below was read out of the service that implements it rather than from a design document, and
anything that could not be established that way is marked as such rather than filled in.

If you find a cell that overstates what the platform does, that is a bug in this page and
worth [reporting](../getting-help.md).
:::

## How to read this page

**Three states, and no fourth.** A capability is one of:

| | | |
| --- | --- | --- |
| ● | **Full** | Implemented, with no limitation specific to this direction. Ordinary caveats that apply to the whole transport are in its notes. |
| ◐ | **Partial** | Implemented, and **something specific is missing**. The note says what. Read it before you design around the row. |
| ○ | **None** | Not implemented. Where that is a deliberate decision rather than unfinished work, the note says so — the two are very different things to plan against. On **Write**, the note also says what becomes of a command you issue anyway, because that is not the same on every row. |
| — | **Not applicable** | The direction has no meaning for this row. It is **not** a synonym for "none", and it never stands in for "unknown" or "planned". |

**The rule behind ● and ◐, and it is applied to every row on this page.** A direction is **◐**
whenever the platform can lose, truncate or refuse something **without telling anyone** — even
where the transport as a whole is the most complete one here. **●** is reserved for a direction
with no such hole. That is the only reason HTTP ingest is `●` for Subscribe and the platform
broker is not: over the tenant's ingest limit HTTP answers the publisher `429`, while the broker
path drops the message after the device has already been PUBACKed and nothing reaches the
publisher either.

**Three directions.** They are named from the platform's point of view:

- **Read** — the platform asks a device for a value and gets the answer in that exchange.
- **Write** — the platform sets a value on a device, or tells it to act.
- **Subscribe** — the device sends readings without being asked each time.

Note that `—` does not appear in the device-transport table at all. All three directions are
meaningful for every device transport, so **None** there is always a real answer to a real
question rather than a non-question.

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

The default path, and the most complete one. The broker is NATS' built-in MQTT server, so
there is no separate broker to run.

- **Subscribe ◐** — the device publishes to its own events topic and the broker captures the
  message durably before any platform code sees it. Authentication is two independent layers:
  the connection is authenticated at the broker and bound to that one device's subjects, and
  the event carries a credential that is checked again in the pipeline. The one hole, and the
  reason this is not `●`: a message that arrives while the tenant is over its **ingest rate
  limit** is acknowledged to the broker and dropped. The device was PUBACKed when the broker
  captured it, so nothing tells the publisher — on this transport there is no `429` to send.
  A fleet that can burst past its limit should be sized against it rather than rely on
  backpressure that does not exist.
- **Write ◐** — commands are delivered, and the limitation is worth planning around: delivery
  is **live-only and unacknowledged**. A publish reaches a device that is connected and
  subscribed at that instant; the broker does not hold it for one that is not, and nothing
  tells the platform whether the device received it. There is deliberately no `DELIVERED`
  state, because confirming delivery separately from a response would need an acknowledgement
  this transport does not provide. A command is complete when the **device answers it** — see
  [responding to a command](../guides/connecting-a-device.md#responding-to-a-command).
- **Read ○** — there is no platform-initiated request/response primitive. You can express a
  read as a command whose response carries the value, but that is a vocabulary you define on
  the device profile, not something the transport provides.

### HTTP

A `POST` endpoint for the same JSON event body. Simple, and one-way.

- **Subscribe ●** — `POST /{instanceId}/{tenant}/events` returns `202` once the event is
  queued, `400` on a body it cannot decode or a syntactically invalid tenant, `429` when the
  tenant is over its ingest rate limit, and **`503` when the event could not be handed to the
  stream**. `503` is the one to retry on: it is the platform saying, on the only transport that
  can say it, that your data did not land. The other statuses are terminal for that request.
- **Write ○ / Read ○** — **there is no downlink at all.** A device that reaches the platform
  only over HTTP cannot be commanded. This is not a gap awaiting a fix so much as the shape of
  the integration: give a device that must receive commands an MQTT connection as well.

  🔴 **A command issued to an HTTP-only device is not refused — it expires.** The platform
  mints no transport name for a device that arrived over HTTP (the projected source is the
  event source's own operator-chosen id), so the gate that recognises an undeliverable
  transport cannot recognise this one. The command is accepted, published to the device plane
  where nothing is subscribed, marked `SENT`, and ends as `TIMEOUT`. Sparkplug is the only row
  on this page where a `○` in this column produces a prompt `FAILED` instead.
- The ingest listener terminates plain HTTP and carries no transport authentication of its
  own — device credentials ride in the event body. TLS, where you need it, comes from
  whatever fronts the service.

### MQTT — an external, operator-owned broker

The platform can also act as a client on a broker you already run, to ingest from it.

- **Subscribe ◐** — it works, and four things are missing, all of which matter for anything
  beyond a lab: the connection is **plaintext** (no TLS), it presents **no broker credentials**,
  it is **at-most-once in effect**, and a message refused for being over a limit is **dropped
  with nothing sent back to the publisher**. Prefer the platform broker unless you specifically
  need to read from an existing one.

  On that third point, the detail matters if you are choosing a QoS on your own broker: the
  platform **subscribes at QoS 1**, so the loss is not in the subscription. It is that the
  session is not persistent and the decode hand-off is in memory, so a message the platform has
  taken from your broker and not yet published is gone if the process restarts. Raising QoS on
  your side does not change that, and the platform makes no durability claim about a broker it
  does not own.
- **Write ○ / Read ○** — this integration is ingest only. A command issued to a device that
  arrives this way behaves exactly as it does for HTTP above: published, `SENT`, then
  `TIMEOUT`.

### Sparkplug B

For brownfield fleets already speaking Sparkplug to their own broker.

- **Subscribe ◐** — NBIRTH/NDATA/DBIRTH/DDATA are decoded, including alias tables and sequence
  tracking, and BIRTH/DEATH drive **authoritative** presence rather than presence inferred from
  a timeout. What keeps it from `●`: **only numeric metrics become measurements**. A boolean,
  string, byte-array, DataSet or Template metric is skipped as the payload is decoded, with
  nothing recorded and nothing said. A fleet whose interesting signals are booleans — a run
  flag, a fault bit — will show a device that is authoritatively online and reporting nothing.
- **Write ○ — deliberately out of scope, not unfinished.** There is no Sparkplug command
  egress (`DCMD`), and it is not a gap waiting on work: a Sparkplug fleet sits on the
  *customer's* MQTT infrastructure, so nothing bridges the platform's command stream to it.
  A command issued to a Sparkplug device ends as `FAILED`, undeliverable — with two
  qualifications worth knowing before you rely on it. It happens on the **delivery sweep**,
  which runs every 30 seconds, not at the moment you enqueue it. And it requires the presence
  gate to be **configured**: that gate needs the cross-service secret and a `device-state`
  endpoint, and without either it is off — it logs that it is off at startup — and the command
  dispatches like any other and ends as `TIMEOUT` instead.
- **Read ○** — same reason.

:::info What the platform *does* publish on Sparkplug, and why an ACL must allow it
No `DCMD` is published, ever, and **no `NCMD` is reachable from the command API** — the single
`NCMD` the platform emits is an internal `Node Control/Rebirth`, issued by the Host's own
session machine to repair a sequence gap, at QoS 0 and unretained.

The Host Application does, however, publish its own **`STATE` message on
`spBv1.0/STATE/{host_id}`, retained and at QoS 1**: when it connects, when it stops cleanly, and
as its Last-Will if it dies. That is the Sparkplug Host birth/death contract, and edge nodes read
it. **If you are writing broker ACLs, grant the platform's client publish on that topic** — deny
it and the host session is broken from the first connection.
:::

:::caution Sparkplug device identity is established at the broker, not per device
A Sparkplug device's identity is derived from the topic it published on. That means the
per-event device-authentication mode does **not** stop one publisher on your broker from
sending under another device's identity **within the same tenant**. Cross-tenant is closed —
tenancy is fixed by which broker connection a message arrived on, never by anything in the
message — but intra-tenant separation is enforced on *your* broker, with per-client
credentials and topic permissions. Size that before pointing a shared broker at a tenant.
:::

### LwM2M

For constrained devices over CoAP/UDP with DTLS.

- **Read ◐** — implemented as a device command, and the response body comes back. The
  limitation is the cap: a body over **8 KiB is truncated**, and the response is still reported
  as a success. "Capped" is the wrong mental model — nothing is refused, and nothing marks the
  result as partial, so a large resource read comes back looking whole.
- **Write ◐** — a **single scalar resource** at a time. Writing an object instance or several
  resources in one operation is not supported, and neither is partial update. Values are capped
  at 8 KiB.
- **Subscribe ◐** — Observe works, and **only SenML-JSON notifications are decoded**, of which
  **only numeric resources become measurements** — a boolean-primary object yields no telemetry
  at all, the same limit Sparkplug has. This has
  a consequence worth sizing up front: a conformant **LwM2M 1.0-only** client cannot produce
  SenML, so it correctly refuses the Observe. Such a device still registers, drives presence
  and accepts commands, but reports **no telemetry at all**. Decoding the older TLV format is
  the follow-up that closes this. Observed objects are restricted to a built-in allowlist that
  is not configurable, capped at 32 observations per registration, and observations **do not
  survive a leader failover** — presence is reconstructed, telemetry is re-established only as
  each device's registration renews.
- Commands to a sleeping device are **held durably and drained** when it next checks in, which
  is the one place the platform holds a command rather than requiring the device to be live.

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

Device authentication is per-device, at the DTLS handshake, using pre-shared keys. X.509 and
raw-public-key credentials are planned.

## Outbound connectors

Where the platform sends data when a rule fires. These carry no device directions — a
connector is a one-way sink — so **Read** and **Subscribe** are `—` rather than `None`.

| Connector | Read | Write | Subscribe | Notes |
| --- | :---: | :---: | :---: | --- |
| `httpCall` webhook | — | ● | — | `POST` only; a non-`POST` method is refused |
| `publish` → MQTT | — | ● | — | QoS 0/1/2; username + secret; **no TLS settings** — see below |
| `publish` → Kafka | — | ● | — | TLS; SASL `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `publish` → AWS SNS | — | ● | — | Static per-tenant credentials only |
| `publish` → AWS SQS | — | ● | — | Static per-tenant credentials only |
| `publish` → Google Pub/Sub | — | ○ | — | **Creatable but not dispatchable** — see below |

The MQTT row's missing TLS is worth stating plainly next to Kafka's, which has a real `tls`
toggle: the MQTT connector config has **no TLS fields of any kind**, and it rejects unknown keys,
so there is nothing to author. TLS happens only implicitly, by giving the broker an `ssl://`
URL — with no way to supply a CA, a client certificate, or a verification setting.

The two AWS connectors deliberately require a static access key and **will not** fall back to
the ambient IAM identity of the pod they run in. Borrowing the platform's own cloud identity
to make a tenant's call is precisely the confusion that separation exists to prevent.

:::warning A Google Pub/Sub connector can be created and will never send
`gcp_pubsub` is a valid connector type: the API accepts it, and the connector saves and
publishes like any other. It has **no output generator**, so every dispatch to it fails
terminally and is dead-lettered — recognized but not executable, never silently dropped.

The reason it is held back rather than shipped: Bento's Pub/Sub output authenticates through
Application Default Credentials — the process-wide identity — with no per-connector credential
field, so a tenant's credential could not be injected without every tenant sharing one
identity. It ships when there is a way to give each connector its own.
:::

Separately from connectors, [notification channels](../guides/notification-channels.md) reach
people rather than systems, over **SMTP** and **webhook**.

## Not available

Named explicitly, because from outside the platform "absent from the list above" and "asked
for and answered no" are indistinguishable — and only one of them is worth waiting for.

| | |
| --- | --- |
| WebSocket device ingest | Not available. Named as planned in the [introduction](../intro.md). |
| CoAP outside LwM2M | Not available. CoAP reaches the platform through [LwM2M](../concepts/lwm2m.md) and not otherwise. |
| Raw NATS as a device transport | Not available. A device credential authorizes an MQTT connection, and there is no NATS-native device client. |
| Sparkplug command egress (`DCMD`) | Not available, and deliberately out of scope rather than pending — see [above](#sparkplug-b). |
| Industrial fieldbus protocols — OPC-UA, Modbus, BACnet | Not available as platform transports, and **nothing the project ships speaks them.** The supported shape is a local gateway that speaks the fieldbus on the plant network and forwards over MQTT or HTTP; the protocol translation is yours to supply. The project does ship `dc-edge-agent`, which does the *other* half of that job — it terminates the device MQTT path locally, spools durably across a WAN outage, and forwards **over MQTT only** — but it speaks no fieldbus itself. |
