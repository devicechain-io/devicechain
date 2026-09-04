---
sidebar_position: 2
title: Connecting a Device
---

# Connecting a Device

Devices connect to DeviceChain over **MQTT** (served directly by NATS' built-in MQTT server on port 1883 — no separate broker) or **HTTP**. Both transports feed the same decode → resolve → persist pipeline, so the JSON event body is identical between them.

:::note Status
MQTT and HTTP ingestion are available. **Connections are secured at the broker:** the MQTT/NATS listeners are **TLS**, and a NATS auth-callout authenticates each connection and binds it to that one device's subjects, so a device can only publish its own events and read its own commands. Device authentication is also enforced **per event** by credential, and the default device-auth mode is **`required`** — so a credential is expected on both the connection and the event. See [Device credentials](./device-credentials.md). Constrained devices can instead connect over CoAP/UDP with DTLS via [LwM2M ingestion](../concepts/lwm2m.md), and brownfield fleets over [Sparkplug B](../concepts/sparkplug.md); both authenticate at the transport handshake rather than per event. A WebSocket transport and the full self-service provisioning/claiming flow are still planned.
:::

:::danger Three identifiers, three jobs — and all three are called some kind of "token"
Connecting a device over MQTT means getting **three different identifiers** right. They do
different jobs, they are not interchangeable, and every one of them is called a token or an id
somewhere in the console. Read this once and the rest of the page will make sense.

**1. The device token** — the device's identity in the registry, e.g. `sensor-001`. You choose it.
It is the `device` field in the event body **and** the `{token}` segment of the topic, and the two
must agree: an event claiming to be from a different device than its topic is rejected.

**2. The credential id** — what the device presents to prove it is itself. The console labels this
**Access token** for an `ACCESS_TOKEN` credential and **Username** for `MQTT_BASIC`. It
authenticates twice: on the MQTT connection, and again per event in the pipeline.

  🔴 **The MQTT username is not the credential id on its own — it is `{tenant}:{credentialId}`.**
  Copying the console's "Username" value straight into your client is the single most common
  connection failure.

**3. The MQTT client id** — `{instanceId}:{tenant}:{deviceToken}`. This one appears **nowhere in the
console**, and the broker refuses any other value, including the random one your client library
invents when you leave it unset. It is a session key, not a label; see [MQTT](#mqtt) below for why
it must be derived this way and how to run more than one connection per device.

**How they go wrong.** A refused connection is **closed, not answered** — your client reports a
reset or an unexpected EOF, never an authorization failure, and a device that reconnects
automatically will loop on it. So all three of these mistakes look identical from the device. If a
device cannot connect, check the client id, then the `{tenant}:` prefix on the username, then the
credential itself — in that order, because that is the order of how often each is the cause.

And a fourth, further on: the `token` in a **command** envelope identifies the *command*, not the
device. Sending the device token back in a command response matches nothing and the response is
discarded — see [Responding to a command](#responding-to-a-command).
:::

## The event body

Every inbound event — over any transport — is a JSON object:

```json
{
  "device": "sensor-001",
  "eventType": "Measurement",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

- `device` — the device's stable token.
- `eventType` — `Measurement`, `Location`, or `Alert` (also `NewRelationship`).
- `credentialType` / `credentialId` — the credential the device presents. `MQTT_BASIC` additionally carries `credentialSecret`. Omit these only when the instance's device-auth mode is set to `disabled` or `optional`; the **default is `required`**, so a credential is expected.
- `payload` — shape depends on `eventType`, and every shape is `{ "entries": [ … ] }`. See below.

### Payload shapes

**Every payload wraps its content in an `entries` array**, and every numeric value is a **JSON
string**. Both rules are enforced: a payload with no entries, an entry with nothing in it, or a bare
number where a string is expected is **rejected** — HTTP answers `400` and an MQTT publish is
dead-lettered rather than silently accepted.

**One entry is one reading, taken at one instant.** An entry may carry its own `occurredTime`, and
that is the instant the reading is stored, charted, evaluated and returned at — so a device that
buffers readings while offline can upload a buffered run — up to the per-message ceiling below —
and keep the history it actually recorded. An entry that carries no `occurredTime` takes the envelope's. `occurredTime` is
RFC 3339 (`2026-08-09T12:00:00.125Z`) wherever it appears; a value that is not is **rejected** with
the offending entry named, never quietly replaced.

One valid RFC 3339 value is refused anyway: **`0001-01-01T00:00:00Z`**, which the platform reserves
to mean "no time was reported". A device that means the epoch should send `1970-01-01T00:00:00Z`.
Like every other timestamp refusal this is terminal and takes the **whole message** with it —
every sibling reading in the same batch — so it is worth ruling out in firmware rather than
discovering in a dead-letter queue.

### How much one message may carry

**A message on this page's transports carries at most 1000 readings.** The ceiling belongs to the
JSON device event described above — MQTT and HTTP. The two transports this page's introduction
points constrained and brownfield fleets at do **not** share it: [LwM2M](../concepts/lwm2m.md)
bounds a single Notify at 256 samples instead, and [Sparkplug B](../concepts/sparkplug.md) applies
no per-message ceiling at all (see [what an operator must
know](../deployment/edge-services.md#sparkplug-what-an-operator-must-know)).

A reading is one stored datum — for measurements that
is one *metric key*, so an entry with twelve metrics is twelve readings; for locations and alerts it
is one entry. Counting keys rather than entries is deliberate: a single entry can hold thousands of
metrics, and it is the readings, not the entries, that become stored rows, state updates and rule
evaluations.

That fan-out is what the ceiling exists for. The per-tenant ingest limiter meters *messages*, and
charges the same for a message of one reading as for a message of forty thousand — so without this,
one message is an unbounded cost the whole instance shares. A device with a deeper backlog uploads
it as several messages.

Over the ceiling a message is **refused whole**, never trimmed to fit: a batch quietly cut short
would be answered `202` and the missing readings would be undetectable from either end. Nothing is
stored, and nothing is lost — the message is routed intact to the failed-decode stream.

How you find out depends on the transport, and the difference matters:

- **HTTP** answers `400`, naming the count and the ceiling.
- **MQTT** does not tell the device anything. The broker acknowledges a publish when it durably
  captures it, which is before the message is decoded, so a `PUBACK` is not a promise that the
  message was accepted — a refusal that happens afterwards is visible only to the operator.

Operators see every refusal on the `total_msg_too_many_readings` counter, and the ceiling is an
operator setting (`maxReadingsPerMessage`) for an instance whose fleet genuinely needs a different
one. Lowering it does not rewrite history, but it does apply to anything still queued: messages
already captured and not yet decoded are refused on the new value.

:::caution A deeply buffered batch is stored in full, but detection may not see all of it
"One entry is one reading, taken at one instant" is about *storage*, and there it holds without
qualification: every reading lands and charts at its own instant. Detection is different. The engine tracks a single frontier
across the whole instance and advances it from each message's own time, so **a device that was
offline for a while and then uploads its whole run at once can have its older readings arrive
behind that frontier** — and the two window-shaped rule kinds, tumbling-window aggregates and
session/gap rules, discard a reading whose window has already closed. No counter, no log, no alarm.

Threshold, duration, repeating, count-window and rate rules still evaluate those readings.

The tolerance is [`watermarkLatenessSeconds`](../deployment/detection-engine.md) (default 5
seconds), and raising it helps only up to a point: the frontier is shared, so busy devices keep
carrying it forward regardless of how long your quiet one was away.

If you use windowed rules on a fleet that buffers, the reliable shape is to **upload in batches
that span less than the lateness tolerance**, or keep windowed rules off the metrics those devices
report. Storage, charts, and the [event queries](../reference/graphql-api.md) are unaffected either
way — the readings are all there.
:::

A reported timestamp may not run far ahead of the platform's own clock. One that does is stored at
the ceiling instead — the tolerance is generous enough for ordinary clock drift, so this only bites
a device whose clock is genuinely wrong. Set the clock, rather than relying on the ceiling: a
reading stored at the ceiling is a reading stored at the wrong time.

**`Measurement`** — one or more named readings:

```json
"payload": { "entries": [ { "measurements": { "temperature": "21.5", "humidity": "48" } } ] }
```

**`Location`** — where the device is:

```json
"payload": {
  "entries": [
    {
      "latitude":  "33.74900000",
      "longitude": "-84.38800000",
      "elevation": "320.5",
      "accuracy":  "4.2",
      "speed":     "0.0",
      "heading":   "271.5"
    }
  ]
}
```

`latitude` and `longitude` are **required**; the rest are optional — send what the receiver actually
knows rather than a placeholder. The units are fixed platform-wide and are **not** configurable per
device:

| Field | Unit | Range |
| --- | --- | --- |
| `latitude` / `longitude` | WGS84 (EPSG:4326) decimal degrees | ±90 / ±180 |
| `elevation` | metres above the WGS84 **ellipsoid** — not above mean sea level | — |
| `accuracy` | horizontal accuracy, metres | 0 or greater |
| `speed` | metres per second | 0 or greater |
| `heading` | degrees clockwise from true north | 0 up to but not including 360 |

:::caution Elevation is above the ellipsoid, not sea level
A receiver that reports height above mean sea level must convert before sending. The two differ by
tens of metres in real terrain — comfortably enough to place a machine on the wrong side of a
geofence — and because both values look equally plausible, getting it wrong produces a confidently
wrong position rather than a visible error.
:::

A value outside its range is rejected as bad data on the first delivery rather than retried. That is
deliberate: the most common mistake is sending degrees scaled by 10⁷ (the convention some GPS and
LwM2M stacks use), and `337490000` is not a latitude at any scale.

**`Alert`** — something the device wants a human or a rule to see:

```json
"payload": { "entries": [ { "type": "overheat", "level": 5, "message": "coolant over limit", "source": "ecu" } ] }
```

`type` is **required** — it is the classifier notification policies, rules and console filters route
on, so an untyped alert is a record nothing can act on. `level`, `message` and `source` are optional.

## MQTT

An MQTT topic maps directly to a NATS subject, so a publish on `{instanceId}/{tenant}/devices/{token}/events` is consumed by `event-sources` as the subject `{instanceId}.{tenant}.devices.{token}.events`. A device is authorized to publish on **its own** events topic and no other, and the `{token}` in the topic must match the `device` in the body — an event claiming to be from a different device is rejected. The first segment is the **instance id** (the `instance.id` you deployed, e.g. `devicechain`): it namespaces the device plane so instances sharing a broker never cross over, and a device credential is authorized only for its own instance's subject tree.

The listener is **TLS** and the connection is **broker-authenticated**: connect over TLS with the instance CA and present the device's credential as the MQTT username **`{tenant}:{credentialId}`** and password.

The connection must also say **which device it is**: set the MQTT **client id** to `{instanceId}:{tenant}:{deviceToken}`. The broker refuses any other value — including the random one your client library invents when you leave it unset.

That requirement is not bookkeeping. An MQTT client id is the key a broker files a device's session under, and the protocol says a connection presenting an id that is already in use *takes that session over*: the device holding it is disconnected and the arrival inherits its subscriptions. Deriving the id from the identity the broker has already authenticated is what stops one device — in your tenant or anyone else's — evicting another, and it is what lets a tenant's session state be found and removed if the tenant is ever deleted.

**If a device needs more than one connection, give each one a suffix:** `{instanceId}:{tenant}:{deviceToken}:pub`, `…:sub`, and so on. Anything after the third `:` is yours to choose. Two connections sharing one client id are two clients fighting over one session — they will disconnect each other in a loop — so a device that publishes on one connection and subscribes for commands on another needs a distinct suffix for each.

:::tip Diagnosing a rejected client id
A refused connection is **closed, not answered** — the broker drops the socket rather than returning an MQTT "not authorized" code, so your client reports a connection reset or an unexpected EOF rather than an authorization failure. A device that reconnects automatically will loop on it. If a device that used to connect suddenly cannot, check its client id before you check its credential.
:::

Publish the event body to your device's events topic:

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","credentialType":"MQTT_BASIC","credentialId":"<credentialId>","credentialSecret":"<credentialSecret>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

The credential authenticates the connection (broker) and the event (pipeline). The TLS host, CA source, and port exposure depend on how the instance is deployed — see [Deployment](../deployment/kubernetes-operator.md).

### Quality of service

**Publish telemetry at QoS 0 unless you have a specific reason not to.** That is what the examples above do — `mosquitto_pub` defaults to it.

QoS ≥ 1 costs real storage on the server: the broker keeps a **second copy** of every QoS ≥ 1 message in its own internal store, in addition to the copy in the stream that serves it. That store shares the same disk as everything else the instance runs on. The platform gives it a ceiling so it cannot consume the whole volume, which means a sustained QoS ≥ 1 backlog drops its **oldest** undelivered messages rather than taking the instance down.

QoS 1 is fully supported. Use it deliberately if your devices are on links where losing an in-flight publish matters more than the storage, and size the deployment's JetStream volume accordingly.

**If you use QoS 1, set `altId` on your events.** QoS 1 is *at-least-once*, so a missed acknowledgement makes the device retransmit — and by default that stores the event twice, double-counting a measurement. Supplying a stable, device-generated `altId` is what opts an event into de-duplication:

```json
{"altId":"sensor-001-4417","device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}
```

A redelivered event carrying an `altId` already seen is detected and skipped. Without one, it is inserted again. This applies to any at-least-once path, not just MQTT QoS 1 — it is the only thing that makes a retry safe.

**QoS 2 is refused by default.** It buys nothing here that `altId` does not give you more cheaply, and it costs more: the broker holds every QoS 2 publish until its PUBREL arrives, so a device that starts the handshake and never finishes it accumulates server-side state that nothing reclaims. Rather than leave that open, the broker rejects QoS 2 publishes outright.

Be aware of *how* it rejects, because it is not gentle: the broker tears down the **connection** rather than declining the single message, so firmware that publishes at QoS 2 in a loop will reconnect in a loop. A QoS 2 Will is refused earlier, at CONNECT. If you see a device reconnecting for no apparent reason, check the QoS it publishes at first.

Publish at QoS 0, or QoS 1 with `altId`. An operator who genuinely needs QoS 2 can turn the rejection off with the `nats_mqtt_reject_qos2_publish` deployment variable; the buffer it fills stays capped either way, so the instance's disk is protected regardless.

## HTTP

`event-sources` also accepts events over HTTP on port **8081**. The instance id and tenant are taken from the path `/{instanceId}/{tenant}/events` (mirroring the MQTT topic convention); the device and its credential ride in the body. `POST` returns **202 Accepted** once the event is queued — or **429 Too Many Requests** if the tenant is over its ingest rate limit (a per-tenant limiter with a platform-default ceiling shields the shared pipeline; the MQTT path drops over-limit messages instead):

```bash
curl -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## Receiving commands

A device receives commands on **its own** topic:

```
{instanceId}/{tenant}/device-commands/{deviceToken}
```

A device is authorized to subscribe to that topic and no other — it cannot see commands
addressed to any other device, and it does not need to filter them out. Subscribe with the
same credential used to publish events:

```bash
mosquitto_sub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001:sub' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/device-commands/sensor-001"
```

Each message is a JSON envelope:

```json
{
  "token": "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11",
  "deviceToken": "sensor-001",
  "name": "reboot",
  "payload": {"delaySeconds": 5}
}
```

- **`token`** identifies **the command**, not the device. It is what you send back in a
  response, and it is the only field that correlates the two.
- **`name`** is the command key. If the device's profile declares a command vocabulary,
  this is one of its published commands and `payload` has already been validated against
  that command's parameter schema — see
  [Commands and the capability contract](../concepts/commands.md#commands-and-the-capability-contract).

## Responding to a command {#responding-to-a-command}

Report the outcome by publishing to the device's **own** command-response topic:

```
{instanceId}/{tenant}/command-responses/{deviceToken}
```

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/command-responses/sensor-001" \
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` must be the `token` from the delivery envelope** — the command's token,
  not the device's. This is the single most common mistake: sending the device token here
  matches no command and the response is discarded.
- **`success`** moves the command to `SUCCESSFUL` or `FAILED`.
- **`payload`** / **`error`** are optional strings, surfaced in the console's command
  history and returned by the API.

Like the events and command topics, this one is **per-device**, and a device is authorized
to publish only to its own. Both the tenant and the responding device are taken from the
topic rather than the body, so a device can answer only for **its own** commands: a response
naming a command that belongs to a different device is rejected, not recorded.

:::caution The topic changed
This topic used to be tenant-wide (`{instanceId}/{tenant}/command-responses`, with no device
segment). A device publishing to the old topic is now refused by the broker, and its
responses never reach the platform — the commands it answers stay outstanding until they
`TIMEOUT`. Update the topic wherever your devices build it.
:::

:::info Responding is what completes the lifecycle
A command that is never answered stays `SENT` until its TTL turns it into `TIMEOUT`.
Without a response the platform knows only that the command was dispatched — not that the
device acted on it. If your devices do not respond, set an `expiresAt` when issuing
commands so they reach a terminal state on your schedule rather than on the platform's
seven-day default.
:::

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** authenticates the device by its credential and resolves the event: **each** of the device's tracked relationships (its assignments to a customer/area/asset) is recorded as an anchor, so the reading is queryable by every dimension. An **unassigned** device still reports — its event simply carries no anchors rather than being dropped (see [Managing device assignments](./managing-assignments.md)).
3. **event-management** persists the resolved event to a TimescaleDB hypertable, and **device-state** updates the device's latest reading + connectivity.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
