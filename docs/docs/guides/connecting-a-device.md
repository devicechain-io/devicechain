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

Every inbound event, over any transport, is a JSON object. The example below is the HTTP form; the MQTT form is the same without the credential fields:

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
- `credentialType` / `credentialId` — the credential the device presents **over HTTP**. `MQTT_BASIC` additionally carries `credentialSecret`. Omit these only when the instance's device-auth mode is set to `disabled` or `optional`. The default is `required`, so an HTTP event is expected to carry a credential. **Over MQTT, leave all three out**: the broker has already authenticated the device when it connected, and an event on the platform's MQTT broker that carries any of them is refused (see the [Device Protocol Reference](../reference/device-protocol.md#rejections-at-the-door)).
- `payload` — its shape depends on `eventType`, and every shape is `{ "entries": [ … ] }`. See below.

Every field, with its type and whether it is required, including the optional `altId` and `relationship`, is in the [Device Protocol Reference](../reference/device-protocol.md#device-event), together with JSON Schemas you can validate firmware output against.

### Payload shapes {#payload-shapes}

Every payload wraps its content in an `entries` array, and the shape fixes the JSON type of each value:

- Measurement values and every `Location` field are **JSON strings** (`"21.5"`, not `21.5`).
- An alert's `level` is a **bare JSON integer** from 0 to 2147483647.

Both rules are enforced. A payload with no entries, an entry with nothing in it, a value of the wrong JSON type (a bare number where a string is expected, or a quoted alert `level`), or an alert `level` outside that range is rejected rather than silently accepted: HTTP answers `400`, and an MQTT publish is dead-lettered.

One entry is one reading, taken at one instant. An entry may carry its own `occurredTime`. That is the instant the reading is stored, charted, evaluated and returned at, so a device that buffers readings while offline can upload a buffered run (up to the limit below) and keep the history it actually recorded.

- An entry with no `occurredTime` takes the envelope's.
- An envelope with no `occurredTime` is dated at the moment the platform received the message. A message that waited in the platform during an outage keeps the time it arrived, not the time it was processed.
- `occurredTime` is RFC 3339 (`2026-08-09T12:00:00.125Z`) wherever it appears. A value that is not is rejected with the offending entry named, never quietly replaced.
- An `occurredTime` more than **366 days** before the platform received the message is refused, on the envelope or on any entry. A buffered upload must reach the platform within that time; a reading older than that is not stored anywhere.

One valid RFC 3339 value is refused anyway: **`0001-01-01T00:00:00Z`**, which the platform reserves to mean "no time was reported". A device that has no clock should omit `occurredTime`, not send a placeholder: the epoch, `1970-01-01T00:00:00Z`, is refused too, as more than 366 days old. Like every other timestamp refusal, this is terminal and takes the **whole message** with it, including every sibling reading in the same batch. Rule it out in firmware rather than discovering it in a dead-letter queue.

### How much one message may carry {#how-much-one-message-may-carry}

An event carries **at most 256 readings**, on every transport. The limit is fixed and is not a setting. On this page's transports (the JSON device event on MQTT and HTTP), one message is one event, so a message over the limit is refused. The protocol gateways split instead: an [LwM2M](../concepts/lwm2m.md) Notify or a [Sparkplug B](../concepts/sparkplug.md) message with more readings becomes several consecutive events of at most 256 (see [what an operator must know](../deployment/edge-services.md#sparkplug-what-an-operator-must-know)).

A reading is one stored datum. For measurements, that is one *metric key*, so an entry with twelve metrics is twelve readings. For locations and alerts, it is one entry. The limit counts keys rather than entries because a single entry can hold thousands of metrics, and it is the readings, not the entries, that become stored rows, state updates and rule evaluations.

That fan-out is what the limit exists for. The per-tenant ingest ceiling counts readings over time, so it bounds how many a tenant sends per second, and one message can cost no more than the tier's burst. But the burst is a tier setting: a tier whose burst is raised to forty thousand could send one message of forty thousand. Without the limit, what one message costs the whole instance would be set by the largest burst any tier allows. A device with a deeper backlog uploads it as several messages.

Over the limit, a message is **refused whole**, never trimmed to fit. A batch quietly cut short would be answered `202`, and the missing readings would be undetectable from either end. Nothing is stored. For diagnosis the message is routed to the failed-decode stream with any credential it carried removed, and with its body cut to the first 16 KiB.

How the device finds out depends on the transport:

- **HTTP** answers `400`, naming the count and the limit.
- **MQTT** does not tell the device anything. The broker acknowledges a publish when it durably captures it, which is before the message is decoded. A `PUBACK` is therefore not a promise that the message was accepted, and a refusal that happens afterwards is visible only to the operator.

Operators see every refusal on the `total_msg_too_many_readings` counter. The limit is not configurable. An instance upgraded from a release that allowed more refuses, on the new limit, messages that were already captured and not yet decoded.

:::caution A deeply buffered batch is stored in full, but detection may not see all of it
Storage holds every reading at its own instant, as long as the message arrives within [366 days](#payload-shapes) of its oldest reading; a message with an older one is refused whole. Detection is different: a device that was offline and then uploads its whole run at once can have its older readings discarded by rules that use a time window or a hold time, with no log or alarm. See [Buffered uploads and windowed rules](#buffered-uploads-and-windowed-rules).
:::

#### Buffered uploads and windowed rules {#buffered-uploads-and-windowed-rules}

The detection engine tracks a single frontier across the whole instance and advances it from each message's own time. A device that was offline for a while and then uploads its whole run at once can have its older readings arrive behind that frontier. Rules with a time window discard a reading whose window has already passed the frontier: tumbling-window aggregates, session/gap rules, and the sliding kinds (repeating, sliding aggregates and correlation). Duration rules discard a reading that meets their condition and is further behind the frontier than the rule's hold time, and place the rest by their own time. No log and no alarm records the discard.

The sliding kinds and duration rules count what they discard on the `detect_late_samples_total` metric. Tumbling-window aggregates and session/gap rules discard silently and do not appear in it.

Threshold, count-window and rate rules still evaluate those readings.

The tolerance is [`watermarkLatenessSeconds`](../deployment/detection-engine.md) (default 5 seconds). Raising it helps only up to a point: the frontier is shared, so busy devices keep carrying it forward regardless of how long your quiet one was away.

If you use windowed rules on a fleet that buffers, either **upload in batches that span less than the lateness tolerance**, or keep windowed rules off the metrics those devices report. Storage, charts and the [event queries](../reference/graphql-api.md) are unaffected either way; the readings are all there.

### Clocks that run ahead {#clocks-that-run-ahead}

A reported timestamp may not run far ahead of the platform's own clock. One that does is stored at the ceiling instead. The tolerance is generous enough for ordinary clock drift, so this only affects a device whose clock is genuinely wrong. Set the clock rather than relying on the ceiling: a reading stored at the ceiling is a reading stored at the wrong time. A clock running far behind is treated differently: a reading more than 366 days old is refused, not moved (see [Payload shapes](#payload-shapes)).

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

:::tip Let the console write it
The **Send data** tab of a device's page in the console builds this command, and the HTTP one below, for that device: its token, its credential and example readings for the metrics its published profile declares. See [Send data](/user-guide/devices#send-data).
:::

Publish the event body to your device's events topic:

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

The credential authenticates the connection. The broker then knows which device every publish comes from, so the event carries no credential; one that does is refused. The TLS host, CA source and port exposure depend on how the instance is deployed; see [Deployment](../deployment/kubernetes-operator.md).

The broker speaks TLS, so `--cafile` needs the instance's CA certificate. On a local kind install it
is in the instance namespace's `dc-nats-tls` Secret, under the key `ca.crt`. Save it as
`instance-ca.crt` with one command (substitute your instance id after the `dci-` prefix):

```bash
kubectl -n dci-devicechain get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > instance-ca.crt
```

On a local kind cluster the broker is on `localhost:1883`, but its certificate is not issued for
`localhost` or `host.docker.internal`, so verification fails on the host name. For local testing
only, add `--insecure` to `mosquitto_pub` to skip the host name check:

```bash
mosquitto_pub --cafile instance-ca.crt --insecure -h localhost -p 1883 ...
```

:::warning Local testing only
`--insecure` turns off host name verification, so the connection no longer proves it reached your
broker. Never use it in production; connect to a host name the certificate covers instead.
:::

### Quality of service {#quality-of-service}

Publish telemetry at **QoS 0** unless you have a specific reason not to. The examples above do, because `mosquitto_pub` defaults to it.

QoS ≥ 1 costs real storage on the server. The broker keeps a second copy of every QoS ≥ 1 message in its own internal store, in addition to the copy in the stream that serves it, and that store shares the same disk as everything else the instance runs on. What a PUBACK promises, and what a backlog does, depends on one instance setting, `infrastructure.nats.mqttFailClosed`:

- **Off (the default).** The platform caps the store so it cannot consume the whole volume. A PUBACK means the broker took the message, not that the platform will keep it: a sustained backlog drops its **oldest** undelivered messages rather than taking the instance down, and a device is not told.
- **On.** A PUBACK means the platform holds the message. This covers events, command responses and device reports. When the platform falls behind and the store fills, it **refuses new publishes** instead of dropping old ones. The broker sends no PUBACK and closes the connection, so the device keeps the message and sends it again when it reconnects: it is pushed back, and no data is lost. While the store is more than 80% full, new device connections are refused until it is below 60%. The refusal is MQTT CONNACK return code 5 (not authorized), so firmware must keep retrying after code 5 rather than give up. A device whose connection was closed this way and that reconnects within 30 seconds is not reported offline. One exception: a message over its tenant's ingest ceiling is still acknowledged and dropped, as with the setting off.

QoS 0 is not affected by the setting. A QoS 0 publish is never stored by the broker, so nothing pushes back on it, and it stays best effort either way.

QoS 1 is fully supported. Use it deliberately if your devices are on links where losing an in-flight publish matters more than the storage, and size the deployment's JetStream volume accordingly. With the setting on, the store takes the hot stream ceiling (`streamMaxBytes`), which is reserved on the volume like any other stream's.

The setting is accepted only on a broker that serves this one instance, because the broker keeps one store for every instance it serves; an instance on a shared broker refuses to start with it on. The guarantee is stated for a replicated broker (three replicas). On a single replica, a node crash can lose messages the broker had acknowledged and not yet written to disk. A publish made while the setting is being turned on or off, or while the store's leader moves, can arrive twice, never not at all, which is one more reason to set `altId`. The edge agent's own broker is not covered.

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
- It returns **429 Too Many Requests** if the tenant is over its HTTP ingest ceiling, counted in readings, so a message carrying many readings costs more than one carrying a single reading. The MQTT path drops over-limit messages instead of answering.
- It returns **400 Bad Request** when the body cannot be read or decoded, or the tenant in the path
  is not a valid token. Sending the same request again gets the same answer.
- It returns **503 Service Unavailable** when the event was not accepted. With a `Retry-After`
  header, the platform is refusing new events
  ([backpressure](../deployment/observability.md#ingest-backpressure)) and the event was not
  stored: wait that many seconds and send it again. Without one, the publish failed and the event
  may have been stored anyway, so a resend stores it twice unless it carries an `altId` and an
  `occurredTime` (see [Quality of service](#quality-of-service)). Retry it either way. The
  tenant's own ceiling is checked first, so a tenant over it gets `429`, not this `503`. The
  [transport matrix](../reference/transport-matrix.md#http) has the full list.

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

## Receiving commands {#receiving-commands}

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
- **`payload`** is optional and may be any JSON value (a string is stored as its text, an object or array as that JSON); **`error`** is an optional string. Both are surfaced in the console's command history and returned by the API.

Every way a response can fail to settle its command is listed in the [Device Protocol Reference](../reference/device-protocol.md#rejections-command-responses).

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

## Configuration delivery topics {#reserved-device-topics}

A device receives its configuration, and tells the platform what it did with it, over two per-device topics:

```
{instanceId}/{tenant}/device-desired/{deviceToken}    (platform to device; a device may subscribe to its own)
{instanceId}/{tenant}/device-reports/{deviceToken}    (device to platform; a device may publish to its own)
```

As with commands, a device is authorized only for the topic carrying its own device token, and only in the direction shown. The platform takes the tenant and the device from the topic, never from the message, so a message must not carry a device identity (one that does is refused), and a device can report only for itself.

The configuration is built from the device's `SHARED` attributes, but only from the keys its profile declares as device-visible configuration: declare them in the console's profile editor (or with `setDeviceProfileConfigurationDeclaration`), and they reach devices when the profile is published. A shared key the declaration does not name is never sent, and a declared key whose stored value does not fit its declared type is left out. A shared-attribute write that would make a device's configuration larger than 64 KiB is refused. The console's device page shows the latest revision, what the device last reported and whether the revision is still pending; the `deviceConfiguration` query returns the same.

Whenever the device's configuration changes, the platform publishes a revision on `device-desired`:

```json
{"v":1,"kind":"config.revision","revision":3,"digest":"sha256:9e39b20b9def308341baa751db8c2570bfc5b562e1d5420f9e6506510e0328da","document":"{\"sampleIntervalSeconds\":30}"}
```

`document` is a JSON **string** holding the configuration. Decode the JSON string value, hash its UTF-8 bytes with SHA-256, and compare `sha256:` followed by the lowercase hex digest with `digest`; if they differ, apply nothing. Do not parse the document and serialize it again before hashing, because the digest covers the bytes the platform sent.

Revisions can reach a device out of order, so a lower revision can arrive after a higher one. Ignore a `config.revision` whose revision is lower than the one you hold, and when the revision is the one you hold, report `APPLIED` for it again rather than applying it twice.

The device answers on `device-reports`. Every message is JSON, at most 4 KiB, and unknown fields are refused.

- **`sync`**, sent every time the device connects, says which revision it holds (`0` if none): `{"v":1,"kind":"sync","config":{"haveRevision":3}}`. Subscribe to `device-desired` **before** you send the sync: nothing is retained, so a reply published before you subscribe is lost. The platform replies with the current revision, whether or not the device already holds it, and sends no reply at all when the device has no configuration. Do not rely on a message published while the device was offline arriving later: syncing is how a device catches up. Sync on every connect and also periodically (every few hours, say), because a revision lost between the platform committing it and sending it is repaired only by a sync. Syncs sent within a few seconds of the previous one are acknowledged and ignored.
- **`config.status`** reports what the device did with a revision: `{"v":1,"kind":"config.status","revision":3,"digest":"sha256:…","status":"APPLIED"}`. The status is `RECEIVED`, `APPLIED` or `REJECTED`. A `REJECTED` report must carry `errors`, a list of up to 16 strings of up to 256 bytes each, saying why.

The platform records a `config.status` only when the device has that revision and the digest matches it; the device stops showing the revision as pending once it has reported `APPLIED` for it. Reports are processed at least once, so repeating one is safe, and an older report never overwrites a newer one. A message that does not decode, names a revision the device was not sent, or carries a digest that is not that revision's is refused and recorded on the dead-letter stream rather than dropped. Devices that speak only HTTP are not served by this channel.

### Restoring or copying a configuration {#restore-copy-configuration}

A device's configuration can be set back to an earlier revision, or made the same as another device's: for example, to undo a bad change, or to give a replacement unit the configuration of the unit it replaced. Both are done from the **Configuration** tab of the device page by an operator with `device:write`, or with the `restoreDeviceConfiguration` and `copyDeviceConfiguration` mutations.

- **Restore** takes one of the device's own revisions from the revision history. **Copy from another device** takes the latest revision of another device in the same tenant.
- The console first shows a preview: every key, its current value, the value it will have, and whether it is added, changed or removed. Nothing changes until you choose **Apply**.
- Applying sets the device's `SHARED` attributes for its declared keys to the values in that revision. A declared key the revision does not carry is removed. A **new** revision is created and sent to the device as usual. Earlier revisions are never changed. If the device already holds that configuration, nothing is created.
- The whole change is refused, and nothing is written, if the device's profile does not declare a key the revision carries (`CONFIGURATION_KEY_UNDECLARED`), if a value does not fit the type now declared for its key (`CONFIGURATION_VALUE_TYPE_MISMATCH`), or if the result is over the size limit (`CONFIGURATION_TOO_LARGE`). Restoring an old revision after the profile's declaration has changed can therefore be refused. A device of another tenant, an unknown revision, or a source device with no revision is `NOT_FOUND`.
- If another change to the device's configuration lands while the restore or copy is being applied, it is refused with `CONFIGURATION_CONFLICT` and nothing is written; it is safe to try again.
- A copy applies the source revision the console previewed, even if the source device's configuration changes before you choose **Apply**. Through GraphQL, pass that revision as `fromRevision`; without it, `copyDeviceConfiguration` copies the source's latest revision.

```graphql
mutation {
  restoreDeviceConfiguration(deviceToken: "pump-7", revision: 3) {
    desired { revision document }
  }
}
```

## Over-the-air updates {#over-the-air-updates}

On an instance running the update service (see [Over-the-Air Updates](../concepts/over-the-air-updates.md)), the same two topics carry firmware and software updates. The rules above apply here too: every message carries `"v":1` and a `kind`, unknown or repeated fields are refused, and a message must not carry a device identity. Update messages in either direction are at most 4 KiB.

When an artifact is assigned to the device, the platform publishes on `device-desired`:

```json
{"v":1,"kind":"update.assignment","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","trust":"UNVERIFIED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,"requiresReboot":true}}
```

`component` names the part of the device to update; a device works on at most one assignment per component. `requiresReboot` is true for a firmware artifact and false for software and configuration. `trust` is `UNVERIFIED` or `SIGNED`. `UNVERIFIED` means the platform has checked that the bytes are the bytes that were declared, not who produced them; it carries no signature fields, and one that does is refused. `SIGNED` adds three fields, all required, as shown below, and the device **must** verify them before it reports `VERIFIED`: the platform relays the signature, the device decides whether to trust it.

```json
{"v":1,"kind":"update.assignment","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","trust":"SIGNED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,"requiresReboot":true},"signature":"MEQCIHGf…","keyId":"f3ef3f7c…","algorithm":"ECDSA_P256_SHA256"}
```

- `signature` is the detached signature, base64 with the standard alphabet.
- `keyId` is 64 lowercase hex characters: the SHA-256 of the signer's public key as DER `SubjectPublicKeyInfo`. The device looks it up in the public keys it trusts.
- `algorithm` is `ECDSA_P256_SHA256` (an ASN.1 DER signature, the form `cosign sign-blob` produces) or `ED25519`. Any other value is refused.
- Both algorithms sign the **raw 32-byte SHA-256 digest** of the artifact, not its hex text and not the `sha256:` string. Ed25519 signs those 32 bytes directly as its message. Compute the digest from the bytes you downloaded, never from `artifact.digest`, and check it equals `artifact.digest` first.

To produce a signature by hand (`file` is the artifact, `k.pem` the private key):

```bash
# keyId: SHA-256 of the public key as DER SubjectPublicKeyInfo
openssl pkey -in k.pem -pubout -outform DER | openssl dgst -sha256

# ECDSA_P256_SHA256 (a P-256 key): signs SHA-256(file)
openssl dgst -sha256 -sign k.pem -binary file | base64 | tr -d '\n'
cosign sign-blob --key k.key file          # the same signature, base64

# ED25519: sign the 32 digest bytes themselves
openssl dgst -sha256 -binary file > digest
openssl pkeyutl -sign -rawin -inkey k.pem -in digest | base64 | tr -d '\n'
```

For Ed25519, `cosign` and `openssl pkeyutl -sign -rawin -in file` sign the **file**, not its digest, so a device would reject their signatures: use the `digest` form above. ECDSA is the algorithm `cosign sign-blob` is compatible with.

If the signature does not verify, fail the update with `FAILED` and `result.code` `SIGNATURE_INVALID`. If `keyId` names a key the device does not trust, use `UNKNOWN_SIGNER`; use the same code for an `UNVERIFIED` assignment when the device requires signed images. Both are reported at the point the download ends (after `DOWNLOADED`, like a digest mismatch), and neither changes the stages above. A signature covers the image, not the version, so a device that must refuse downgrades has to enforce that itself.

To fetch the image, ask for a download grant on `device-reports`:

```json
{"v":1,"kind":"download.request","assignmentId":"asg-91xq"}
```

The platform answers on `device-desired` with a URL that works, with no other credential, for this one artifact until `expiresAt` (at most fifteen minutes):

```json
{"v":1,"kind":"download.grant","assignmentId":"asg-91xq","url":"https://…/api/update-management/updates/download/…?cap=…","expiresAt":"2026-10-12T10:15:00Z"}
```

Download it with an HTTP `GET`. The endpoint answers `Range` requests, so an interrupted download resumes from the bytes you hold; when the grant expires, send the same `download.request` again for a new one. If the assignment is no longer current, the answer is an `update.cancel` instead of a grant.

Report each stage on `device-reports` with `update.progress`:

```json
{"v":1,"kind":"update.progress","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","artifactDigest":"sha256:…","seq":3,"stage":"DOWNLOADING","progress":{"bytes":4194304,"total":8388608},"bootId":"b-20261012-0815"}
```

- `attemptId`, `assignmentId`, `component` and `artifactDigest` repeat the assignment; a report that names another attempt or digest is not applied.
- `seq` starts at 1 and rises with every report for the attempt. Sending the same report again is harmless; a different report under a `seq` already used is a conflict and is not applied.
- `stage` is one of `RECEIVED`, `DOWNLOADING`, `DOWNLOADED`, `VERIFIED`, `INSTALLING`, `REBOOTING`, `RUNNING`, `FAILED` and `ABANDONED`. You may skip a stage whose report was lost, but never go back to an earlier one.
- `progress` (bytes held, and the total if known) is allowed only with `DOWNLOADING`.
- Report `VERIFIED` only once the SHA-256 of the bytes you hold equals `artifact.digest` and, for a `SIGNED` assignment, the signature verifies.
- `FAILED` must carry `result`: `{"code":"FLASH_WRITE","detail":"…"}`, a code of up to 64 capital letters, digits and underscores, and a detail of up to 512 bytes. The codes `SIGNATURE_INVALID` and `UNKNOWN_SIGNER` mean the device refused the image's signature, as described above.
- `RUNNING` must carry `running`: `{"version":"1.4.2"}`, optionally with the `digest` of what runs. Send it once the new version is running. A `RUNNING` report that names any other version is not applied: if the device is not on the target, report `FAILED`.
- `bootId` is required on every report: an identifier of the current boot, up to 128 printable characters, that changes every time the device boots. When `requiresReboot` is true, the platform accepts `RUNNING` as proof of the update only from a boot other than the one the device installed in.

`update.cancel` (`{"v":1,"kind":"update.cancel","attemptId":"att-7f3k","assignmentId":"asg-91xq"}`) asks the device to stop working on that assignment. If it has not begun installing, it reports `stage` `ABANDONED`; once installation has started an update cannot be recalled.

After it boots, a device reports what each component runs with `update.inventory`, which names no attempt:

```json
{"v":1,"kind":"update.inventory","component":"firmware","running":{"version":"1.4.2"},"bootId":"b-20261012-0821"}
```

That is how an update the platform marked `UNKNOWN`, because the device went quiet while installing or rebooting, is resolved.

The `sync` a device sends on every connect (see [Configuration delivery topics](#reserved-device-topics)) also makes the platform resend each update the device should still be working on, and a cancel for any whose cancellation the device has not confirmed. Assignments are not retained, so subscribe to `device-desired` before you sync.

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** authenticates the device by its credential and resolves the event. Each of the device's tracked relationships (its assignments to a customer/area/asset) is recorded as an anchor, so the reading is queryable by every dimension. An **unassigned** device still reports: its event carries no anchors rather than being dropped (see [Managing device assignments](./managing-assignments.md)).
3. **event-management** persists the resolved event to a TimescaleDB hypertable, and **device-state** updates the device's latest reading and connectivity.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
