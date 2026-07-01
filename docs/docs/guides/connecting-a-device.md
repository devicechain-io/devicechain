---
sidebar_position: 2
title: Connecting a Device
---

# Connecting a Device

Devices connect to DeviceChain over **MQTT** (served directly by NATS' built-in MQTT server on port 1883 — no separate broker) or **HTTP**. Both transports feed the same decode → resolve → persist pipeline, so the JSON event body is identical between them.

:::note Status
MQTT and HTTP ingestion are available. **Device authentication is available**: a device can present a credential (access token, MQTT-basic username/secret, or X.509 thumbprint) that the platform resolves to the owning device and verifies — honoring expiry and revocation-by-disable — and an instance can be configured to reject events that don't authenticate. See [Device credentials](./device-credentials.md). Additional transports (CoAP, WebSocket) and the full self-service provisioning/claiming flow are still planned.
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
- `credentialType` / `credentialId` — the credential the device presents (ADR-014). `MQTT_BASIC` additionally carries `credentialSecret`. Omit these only when the instance's device-auth mode is not `required`.
- `payload` — shape depends on `eventType`; measurement values are strings.

## MQTT

An MQTT topic maps directly to a NATS subject, so a publish on `dc/{tenant}/devices/{token}/events` is consumed by `event-sources` as the subject `dc.{tenant}.devices.{token}.events`. Publish the event body to your device's events topic:

```bash
mosquitto_pub \
  -h localhost -p 1883 \
  -t "dc/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## HTTP

`event-sources` also accepts events over HTTP on port **8081**. The tenant is taken from the path (mirroring the MQTT topic convention); the device and its credential ride in the body. `POST` returns **202 Accepted** once the event is queued:

```bash
curl -X POST http://localhost:8081/dc/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** authenticates the device by its credential and resolves the event: **each** of the device's tracked relationships (its assignments to a customer/area/asset) is recorded as an anchor, so the reading is queryable by every dimension. An **unassigned** device still reports — its event simply carries no anchors rather than being dropped (see [Managing device assignments](./managing-assignments.md)).
3. **event-management** persists the resolved event to a TimescaleDB hypertable, and **device-state** updates the device's latest reading + connectivity.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
