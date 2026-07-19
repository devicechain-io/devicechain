---
sidebar_position: 2
title: Connecting a Device
---

# Connecting a Device

Devices connect to DeviceChain over **MQTT** (served directly by NATS' built-in MQTT server on port 1883 — no separate broker) or **HTTP**. Both transports feed the same decode → resolve → persist pipeline, so the JSON event body is identical between them.

:::note Status
MQTT and HTTP ingestion are available. **Connections are secured at the broker (ADR-025):** the MQTT/NATS listeners are **TLS**, and a NATS auth-callout authenticates each connection and binds it to per-tenant subjects, so a device can only publish or subscribe within its own tenant. Device authentication is also enforced **per event** by credential, and the default device-auth mode is **`required`** — so a credential is expected on both the connection and the event. See [Device credentials](./device-credentials.md). Additional transports (CoAP, WebSocket) and the full self-service provisioning/claiming flow are still planned.
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
- `credentialType` / `credentialId` — the credential the device presents (ADR-014). `MQTT_BASIC` additionally carries `credentialSecret`. Omit these only when the instance's device-auth mode is set to `disabled` or `optional`; the **default is `required`**, so a credential is expected.
- `payload` — shape depends on `eventType`; measurement values are strings.

## MQTT

An MQTT topic maps directly to a NATS subject, so a publish on `{instanceId}/{tenant}/devices/{token}/events` is consumed by `event-sources` as the subject `{instanceId}.{tenant}.devices.{token}.events`. A device is authorized to publish on **its own** events topic and no other, and the `{token}` in the topic must match the `device` in the body — an event claiming to be from a different device is rejected. The first segment is the **instance id** (the `instance.id` you deployed, e.g. `devicechain`): it namespaces the device plane so instances sharing a broker never cross over, and a device credential is authorized only for its own instance's subject tree.

The listener is **TLS** and the connection is **broker-authenticated** (ADR-025): connect over TLS with the instance CA and present the device's credential as the MQTT username **`{tenant}:{credentialId}`** and password. Publish the event body to your device's events topic:

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
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
  [Commands and the capability contract](../concepts/domain-model.md#commands-and-the-capability-contract).

## Responding to a command

Report the outcome by publishing to the tenant's command-response topic:

```
{instanceId}/{tenant}/command-responses
```

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/command-responses" \
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` must be the `token` from the delivery envelope** — the command's token,
  not the device's. This is the single most common mistake: sending the device token here
  matches no command and the response is discarded.
- **`success`** moves the command to `SUCCESSFUL` or `FAILED`.
- **`payload`** / **`error`** are optional strings, surfaced in the console's command
  history and returned by the API.

Unlike the events topic, this one is **not** per-device: every device in a tenant publishes
to the same subject, and a response identifies its command by token. The tenant is taken
from the topic rather than the body, so a device cannot answer for another tenant.

:::info Responding is what completes the lifecycle
A command that is never answered stays `SENT` until it expires. Without a response the
platform knows only that the command was dispatched — not that the device acted on it. If
your devices do not respond, set an `expiresAt` when issuing commands so they reach a
terminal state rather than sitting in flight indefinitely.
:::

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** authenticates the device by its credential and resolves the event: **each** of the device's tracked relationships (its assignments to a customer/area/asset) is recorded as an anchor, so the reading is queryable by every dimension. An **unassigned** device still reports — its event simply carries no anchors rather than being dropped (see [Managing device assignments](./managing-assignments.md)).
3. **event-management** persists the resolved event to a TimescaleDB hypertable, and **device-state** updates the device's latest reading + connectivity.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
