---
sidebar_position: 2
title: Connecting a Device
---

# Connecting a Device

Devices send events to DeviceChain over **MQTT** or **HTTP**. MQTT is served directly by NATS' built-in MQTT server on port 1883, with no separate broker. Both transports feed the same decode → resolve → persist pipeline, so the JSON event body is identical between them.

:::note Status
MQTT and HTTP ingestion are available. Constrained devices can instead connect over CoAP/UDP with DTLS via [LwM2M ingestion](../concepts/lwm2m.md), and brownfield fleets over [Sparkplug B](../concepts/sparkplug.md). A WebSocket transport and the full self-service provisioning/claiming flow are still planned.
:::

Connections are secured at the broker. The MQTT/NATS listeners are TLS, and a NATS auth-callout authenticates each connection and binds it to that one device's subjects, so a device can only publish its own events and read its own commands. Device authentication is also enforced per event by credential. The default device-auth mode is `required`, so a credential is expected on both the connection and the event. See [Device credentials](./device-credentials.md). LwM2M and Sparkplug B authenticate at the transport handshake rather than per event.

## Three identifiers {#three-identifiers}

Connecting a device over MQTT means getting three different identifiers right. They do different jobs and are not interchangeable, and every one of them is called a token or an id somewhere in the console.

| Identifier | What it is | Where it goes |
| --- | --- | --- |
| **Device token** | The device's identity in the registry, e.g. `sensor-001`. You choose it. | The `device` field in the event body and the `{token}` segment of the topic. The two must agree: an event claiming to be from a different device than its topic is rejected. |
| **Credential id** | What the device presents to prove it is itself. The console labels it **Access token** for an `ACCESS_TOKEN` credential and **Username** for `MQTT_BASIC`. | It authenticates twice: on the MQTT connection, and again per event in the pipeline. The MQTT username is `{tenant}:{credentialId}`. |
| **MQTT client id** | `{instanceId}:{tenant}:{deviceToken}`. It is a session key, not a label. | The MQTT connection. It appears nowhere in the console. |

:::warning The MQTT username is not the credential id on its own
It is **`{tenant}:{credentialId}`**. Copying the console's "Username" value straight into your client is the single most common connection failure.
:::

The broker refuses any client id that is not `{instanceId}:{tenant}:{deviceToken}` or that value plus a `:suffix`. That includes the random id your client library invents when you leave it unset. The suffix is how one device runs two connections; see [MQTT](#mqtt).

All three mistakes look identical from the device. A refused connection gets one generic answer: the broker returns MQTT CONNACK return code 5 (*not authorized*) and then closes the connection. The code is deliberately generic: it is the same whether the client id, the `{tenant}:` prefix or the credential was wrong, so the refusal never says which check failed. Your client reports "not authorized", or, if it never reads the CONNACK, only the reset or unexpected EOF that follows. A device that reconnects automatically will loop on it.

If a device cannot connect, check in this order:

1. The client id. It is the one value the console never shows you, so it is the one you had to construct.
2. The `{tenant}:` prefix on the username.
3. The credential itself.

A fourth identifier comes later: the `token` in a **command** envelope identifies the *command*, not the device. Sending the device token back in a command response matches nothing. The answer settles no command and ends up on the platform's dead-letter stream, where an operator can see it, while the command stays outstanding. See [Responding to a command](#responding-to-a-command).

## The event body

Every inbound event, over any transport, is a JSON object:

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
- `credentialType` / `credentialId` — the credential the device presents. `MQTT_BASIC` additionally carries `credentialSecret`. Omit these only when the instance's device-auth mode is set to `disabled` or `optional`. The default is `required`, so a credential is expected.
- `payload` — its shape depends on `eventType`, and every shape is `{ "entries": [ … ] }`. See below.

### Payload shapes

Every payload wraps its content in an `entries` array, and the shape fixes the JSON type of each value:

- Measurement values and every `Location` field are **JSON strings** (`"21.5"`, not `21.5`).
- An alert's `level` is a **bare JSON integer**.

Both rules are enforced. A payload with no entries, an entry with nothing in it, or a value of the wrong JSON type (a bare number where a string is expected, or a quoted alert `level`) is rejected rather than silently accepted: HTTP answers `400`, and an MQTT publish is dead-lettered.

One entry is one reading, taken at one instant. An entry may carry its own `occurredTime`. That is the instant the reading is stored, charted, evaluated and returned at, so a device that buffers readings while offline can upload a buffered run (up to the per-message ceiling below) and keep the history it actually recorded.

- An entry with no `occurredTime` takes the envelope's.
- An envelope with no `occurredTime` is dated at the moment the platform received the message. A message that waited in the platform during an outage keeps the time it arrived, not the time it was processed.
- `occurredTime` is RFC 3339 (`2026-08-09T12:00:00.125Z`) wherever it appears. A value that is not is rejected with the offending entry named, never quietly replaced.

One valid RFC 3339 value is refused anyway: **`0001-01-01T00:00:00Z`**, which the platform reserves to mean "no time was reported". A device that means the epoch should send `1970-01-01T00:00:00Z`. Like every other timestamp refusal, this is terminal and takes the **whole message** with it, including every sibling reading in the same batch. Rule it out in firmware rather than discovering it in a dead-letter queue.

### How much one message may carry {#how-much-one-message-may-carry}

A message on this page's transports carries **at most 1000 readings**. The ceiling belongs to the JSON device event described above, on MQTT and HTTP. The transports the introduction points constrained and brownfield fleets at do not share it: [LwM2M](../concepts/lwm2m.md) bounds a single Notify at 256 samples instead, and [Sparkplug B](../concepts/sparkplug.md) applies no per-message ceiling at all (see [what an operator must know](../deployment/edge-services.md#sparkplug-what-an-operator-must-know)).

A reading is one stored datum. For measurements, that is one *metric key*, so an entry with twelve metrics is twelve readings. For locations and alerts, it is one entry. The ceiling counts keys rather than entries because a single entry can hold thousands of metrics, and it is the readings, not the entries, that become stored rows, state updates and rule evaluations.

That fan-out is what the ceiling exists for. The per-tenant ingest limiter meters *messages*, and charges the same for a message of one reading as for a message of forty thousand. Without the ceiling, one message would be an unbounded cost the whole instance shares. A device with a deeper backlog uploads it as several messages.

Over the ceiling, a message is **refused whole**, never trimmed to fit. A batch quietly cut short would be answered `202`, and the missing readings would be undetectable from either end. Nothing is stored and nothing is lost: the message is routed intact to the failed-decode stream.

How the device finds out depends on the transport:

- **HTTP** answers `400`, naming the count and the ceiling.
- **MQTT** does not tell the device anything. The broker acknowledges a publish when it durably captures it, which is before the message is decoded. A `PUBACK` is therefore not a promise that the message was accepted, and a refusal that happens afterwards is visible only to the operator.

Operators see every refusal on the `total_msg_too_many_readings` counter. The ceiling is an operator setting (`maxReadingsPerMessage`) for an instance whose fleet genuinely needs a different one. Lowering it does not rewrite history, but it does apply to anything still queued: messages already captured and not yet decoded are refused on the new value.

:::caution A deeply buffered batch is stored in full, but detection may not see all of it
Storage holds every reading at its own instant, without qualification. Detection is different: a device that was offline and then uploads its whole run at once can have its older readings discarded by rules that use a time window, with no log or alarm. See [Buffered uploads and windowed rules](#buffered-uploads-and-windowed-rules).
:::

#### Buffered uploads and windowed rules {#buffered-uploads-and-windowed-rules}

The detection engine tracks a single frontier across the whole instance and advances it from each message's own time. A device that was offline for a while and then uploads its whole run at once can have its older readings arrive behind that frontier. Rules with a time window discard a reading whose window has already passed the frontier: tumbling-window aggregates, session/gap rules, and the sliding kinds (repeating, sliding aggregates and correlation). No log and no alarm records the discard.

The sliding kinds count what they discard on the `detect_late_samples_total` metric. Tumbling-window aggregates and session/gap rules discard silently and do not appear in it.

Threshold, duration, count-window and rate rules still evaluate those readings.

The tolerance is [`watermarkLatenessSeconds`](../deployment/detection-engine.md) (default 5 seconds). Raising it helps only up to a point: the frontier is shared, so busy devices keep carrying it forward regardless of how long your quiet one was away.

If you use windowed rules on a fleet that buffers, either **upload in batches that span less than the lateness tolerance**, or keep windowed rules off the metrics those devices report. Storage, charts and the [event queries](../reference/graphql-api.md) are unaffected either way; the readings are all there.

### Clocks that run ahead {#clocks-that-run-ahead}

A reported timestamp may not run far ahead of the platform's own clock. One that does is stored at the ceiling instead. The tolerance is generous enough for ordinary clock drift, so this only affects a device whose clock is genuinely wrong. Set the clock rather than relying on the ceiling: a reading stored at the ceiling is a reading stored at the wrong time.

### Measurement {#measurement}

**`Measurement`** — one or more named readings:

```json
"payload": { "entries": [ { "measurements": { "temperature": "21.5", "humidity": "48" } } ] }
```

### Location {#location}

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

`latitude` and `longitude` are required; the rest are optional. Send what the receiver actually knows rather than a placeholder. The units are fixed platform-wide and are not configurable per device:

| Field | Unit | Range |
| --- | --- | --- |
| `latitude` / `longitude` | WGS84 (EPSG:4326) decimal degrees | ±90 / ±180 |
| `elevation` | metres above the WGS84 **ellipsoid** — not above mean sea level | — |
| `accuracy` | horizontal accuracy, metres | 0 or greater |
| `speed` | metres per second | 0 or greater |
| `heading` | degrees clockwise from true north | 0 up to but not including 360 |

:::caution Elevation is above the ellipsoid, not sea level
A receiver that reports height above mean sea level must convert before sending. The two differ by tens of metres in real terrain, enough to place a machine on the wrong side of a geofence. Both values look equally plausible, so getting it wrong produces a confidently wrong position rather than a visible error.
:::

A value outside its range is rejected as bad data on the first delivery rather than retried. The most common mistake is sending degrees scaled by 10⁷ (the convention some GPS and LwM2M stacks use), and `337490000` is not a latitude at any scale.

### Alert {#alert}

**`Alert`** — something the device wants a human or a rule to see:

```json
"payload": { "entries": [ { "type": "overheat", "level": 5, "message": "coolant over limit", "source": "ecu" } ] }
```

`type` is required. It is the classifier that notification policies, rules and console filters route on, so an untyped alert is a record nothing can act on. `level`, `message` and `source` are optional.

## MQTT

An MQTT topic maps directly to a NATS subject. A publish on `{instanceId}/{tenant}/devices/{token}/events` is consumed by `event-sources` as the subject `{instanceId}.{tenant}.devices.{token}.events`.

- A device is authorized to publish on **its own** events topic and no other.
- The `{token}` in the topic must match the `device` in the body. An event claiming to be from a different device is rejected.
- The first segment is the **instance id** (the `instance.id` you deployed, e.g. `devicechain`). It namespaces the device plane so instances sharing a broker never cross over, and a device credential is authorized only for its own instance's subject tree.

### Connection settings {#connection-settings}

The listener is TLS and the connection is broker-authenticated. Connect over TLS with the instance CA, and present the device's credential as the MQTT username **`{tenant}:{credentialId}`** and password.

Set the MQTT **client id** to `{instanceId}:{tenant}:{deviceToken}` so the connection says which device it is. The broker refuses anything else, including the random one your client library invents when you leave it unset. The one exception is a `:suffix` after the device token, described below.

The client id is not bookkeeping. An MQTT client id is the key a broker files a device's session under, and the protocol says a connection presenting an id that is already in use *takes that session over*: the device holding it is disconnected and the new connection inherits its subscriptions. Deriving the id from the identity the broker has already authenticated stops one device, in your tenant or anyone else's, evicting another. It is also what lets a tenant's session state be found and removed if the tenant is ever deleted.

If a device needs more than one connection, give each one a suffix: `{instanceId}:{tenant}:{deviceToken}:pub`, `…:sub`, and so on. Anything after the third `:` is yours to choose. Two connections sharing one client id are two clients fighting over one session, and they will disconnect each other in a loop. A device that publishes on one connection and subscribes for commands on another needs a distinct suffix for each.

:::tip Diagnosing a rejected client id
A bad client id, a missing `{tenant}:` prefix and a bad credential all produce the same CONNACK return code 5, so if a device that used to connect suddenly cannot, check its client id before its credential (see [Three identifiers](#three-identifiers)). A device looping on a wrong MQTT password is also slowed down: after 10 failures in a row its connects are refused, even with the right password, for up to 30 seconds at a time (see [Repeated failed connects are slowed down](./device-credentials.md#connect-backoff)). Once you have fixed its password, give it half a minute before you conclude the fix did not work.
:::

### Publishing an event {#publishing-an-event}

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

The credential authenticates the connection (broker) and the event (pipeline). The TLS host, CA source and port exposure depend on how the instance is deployed; see [Deployment](../deployment/kubernetes-operator.md).

### Quality of service

Publish telemetry at **QoS 0** unless you have a specific reason not to. The examples above do, because `mosquitto_pub` defaults to it.

QoS ≥ 1 costs real storage on the server. The broker keeps a second copy of every QoS ≥ 1 message in its own internal store, in addition to the copy in the stream that serves it, and that store shares the same disk as everything else the instance runs on. The platform caps the store so it cannot consume the whole volume, which means a sustained QoS ≥ 1 backlog drops its **oldest** undelivered messages rather than taking the instance down.

QoS 1 is fully supported. Use it deliberately if your devices are on links where losing an in-flight publish matters more than the storage, and size the deployment's JetStream volume accordingly.

If you use QoS 1, **set `altId` and `occurredTime` on your events**. QoS 1 is *at-least-once*, so a missed acknowledgement makes the device retransmit, and by default that stores the event twice, double-counting a measurement. A stable, device-generated `altId` opts an event into de-duplication. The match is on the `altId` and the envelope's `occurredTime` together, so send both on the envelope. An `occurredTime` on an entry does not count toward the match:

```json
{"altId":"sensor-001-4417","occurredTime":"2026-08-09T12:00:00.125Z","device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}
```

A redelivered event carrying an `altId` and `occurredTime` already seen is detected and skipped. Without an `altId`, it is inserted again. An envelope with an `altId` but no `occurredTime` of its own is dated when it arrives, so a copy the device sends again gets a different time, does not match the first, and is also stored again. This applies to any at-least-once path, not only MQTT QoS 1; it is the only thing that makes a retry safe.

**QoS 2 is refused by default.** It gives nothing here that `altId` does not give you more cheaply, and it costs more: the broker holds every QoS 2 publish until its PUBREL arrives, so a device that starts the handshake and never finishes it accumulates server-side state that nothing reclaims. Rather than leave that open, the broker rejects QoS 2 publishes outright.

The rejection is not gentle. The broker tears down the **connection** rather than declining the single message, so firmware that publishes at QoS 2 in a loop will reconnect in a loop. A QoS 2 Will is refused earlier, at CONNECT. If a device keeps reconnecting for no apparent reason, check the QoS it publishes at first.

Publish at QoS 0, or QoS 1 with `altId` and `occurredTime`. An operator who genuinely needs QoS 2 can turn the rejection off with the `nats_mqtt_reject_qos2_publish` deployment variable. The buffer it fills stays capped either way, so the instance's disk is protected regardless.

## HTTP

`event-sources` also accepts events over HTTP on port **8081**. The instance id and tenant come from the path `/{instanceId}/{tenant}/events`, mirroring the MQTT topic convention; the device and its credential ride in the body.

- `POST` returns **202 Accepted** once the event is queued.
- It returns **429 Too Many Requests** if the tenant is over its HTTP ingest rate limit. The MQTT path drops over-limit messages instead of answering.

HTTP ingest has a per-tenant allowance of its own, separate from the one the tenant's MQTT traffic spends, so HTTP requests naming a tenant cannot use up that tenant's MQTT telemetry (see [unconfirmed tenant names](../concepts/governance.md#unconfirmed-tenants)).

```bash
curl -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

:::warning Expose port 8081 only behind network controls
HTTP ingest has no transport authentication: the device credential is in the request body and is checked after the request is admitted. Anyone who can reach port 8081 and knows a tenant's name can therefore spend that tenant's HTTP allowance. The chart's ingress does not route this port, and by default any pod in the cluster can reach it. Put it behind a NetworkPolicy, or behind an ingress or gateway that authenticates callers, before you rely on it.
:::

### Time limits on a request

The ingest listener bounds how long a request may take. A device has **5 seconds** to send its request headers and **60 seconds** to send the whole request, headers and body. Both are configurable per instance, in the `event-sources` area's `httpIngest` settings.

A request that exceeds either bound has its **connection closed**. The server closes it before the event exists, so there is no response, no event, and nothing in the pipeline to trace it to. The symptom looks like intermittent device-side flakiness affecting only the slowest devices. On a constrained link (NB-IoT, 2G, satellite), where several seconds to complete a request is ordinary, raise the bounds rather than leaving those devices to fail silently. The `total_http_connections_closed_before_request` metric counts connections that never delivered a request, which is what such a device leaves behind.

## Receiving commands

A device receives commands on **its own** topic:

```
{instanceId}/{tenant}/device-commands/{deviceToken}
```

A device is authorized to subscribe to that topic and no other. It cannot see commands addressed to any other device, and it does not need to filter them out. Subscribe with the same credential used to publish events:

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
  "payload": {"delaySeconds": 5},
  "dispatchNonce": "0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94"
}
```

- **`token`** identifies the command, not the device. It is what you send back in a response, and it is the only field that correlates the two.
- **`name`** is the command key. If the device's profile declares a command vocabulary, this is one of its published commands, and `payload` has already been validated against that command's parameter schema. See [Commands and the capability contract](../concepts/commands.md#commands-and-the-capability-contract).
- **`dispatchNonce`** names this delivery of the command. It is opaque, so nothing on the device should read or interpret it, and it must be echoed in the response. Keep it with the command until you answer. If the same command arrives again, answer with the nonce from the **latest** delivery rather than the one you first stored.

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
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` must be the `token` from the delivery envelope**, the command's token, not the device's. Sending the device token here is the single most common mistake. It matches no command, so the response settles nothing: it is redelivered until the broker's delivery ceiling (five attempts), then recorded on the dead-letter stream with reason `exhausted`, visible to an operator, while the command stays outstanding.
- **`dispatchNonce` must be the `dispatchNonce` from the delivery envelope you are answering.** It is required. A response that omits it, or that quotes a nonce from an earlier delivery of the same command, does not settle the command. See [Why the nonce is required](#why-the-nonce-is-required).
- **`success`** moves the command to `SUCCESSFUL` or `FAILED`.
- **`payload`** / **`error`** are optional strings, surfaced in the console's command history and returned by the API.

Like the events and command topics, this one is per-device, and a device is authorized to publish only to its own. Both the tenant and the responding device are taken from the topic rather than the body, so a device can answer only for **its own** commands. A response naming a command that belongs to a different device is rejected, not recorded.

:::caution The topic changed
This topic used to be tenant-wide (`{instanceId}/{tenant}/command-responses`, with no device segment). The broker now refuses a device publishing to the old topic, and its responses never reach the platform, so the commands it answers stay outstanding until they `TIMEOUT`. Update the topic wherever your devices build it.
:::

:::info Responding is what completes the lifecycle
A command that is never answered stays `SENT` until its TTL turns it into `TIMEOUT`. Without a response, the platform knows only that the command was dispatched, not that the device acted on it. If your devices do not respond, set an `expiresAt` when issuing commands so they reach a terminal state on your schedule rather than on the platform's seven-day default.
:::

### Why the nonce is required {#why-the-nonce-is-required}

Echoing `dispatchNonce` tells the platform your answer belongs to **this** delivery of the command. That matters because a command can legitimately be published more than once. If a publish reports an error, the platform cannot tell a message that was lost from one whose acknowledgement was lost, so it queues the command to be sent again. Without the nonce, an answer to the first delivery arriving after the second went out would close the command before the device had carried out the second one.

For a device you build yourself, this has two consequences:

- A response with **no** `dispatchNonce` is refused. The command is not settled, and the answer is recorded on the platform's dead-letter stream rather than dropped, so the refusal is visible rather than silent.
- A response quoting a `dispatchNonce` the command has **moved off** is refused the same way. Answer with the nonce from the delivery you are actually responding to.

Store the nonce alongside the command while you carry it out, and overwrite it if the same command is delivered again before you answer.

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** authenticates the device by its credential and resolves the event. Each of the device's tracked relationships (its assignments to a customer/area/asset) is recorded as an anchor, so the reading is queryable by every dimension. An **unassigned** device still reports: its event carries no anchors rather than being dropped (see [Managing device assignments](./managing-assignments.md)).
3. **event-management** persists the resolved event to a TimescaleDB hypertable, and **device-state** updates the device's latest reading and connectivity.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
