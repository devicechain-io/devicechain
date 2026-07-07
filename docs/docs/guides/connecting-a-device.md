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

An MQTT topic maps directly to a NATS subject, so a publish on `{instanceId}/{tenant}/devices/{token}/events` is consumed by `event-sources` as the subject `{instanceId}.{tenant}.devices.{token}.events`. The first segment is the **instance id** (the `instance.id` you deployed, e.g. `devicechain`): it namespaces the device plane so instances sharing a broker never cross over, and a device credential is authorized only for its own instance's subject tree.

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

## HTTP

`event-sources` also accepts events over HTTP on port **8081**. The instance id and tenant are taken from the path `/{instanceId}/{tenant}/events` (mirroring the MQTT topic convention); the device and its credential ride in the body. `POST` returns **202 Accepted** once the event is queued — or **429 Too Many Requests** if the tenant is over its ingest rate limit (a per-tenant limiter with a platform-default ceiling shields the shared pipeline; the MQTT path drops over-limit messages instead):

```bash
curl -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** authenticates the device by its credential and resolves the event: **each** of the device's tracked relationships (its assignments to a customer/area/asset) is recorded as an anchor, so the reading is queryable by every dimension. An **unassigned** device still reports — its event simply carries no anchors rather than being dropped (see [Managing device assignments](./managing-assignments.md)).
3. **event-management** persists the resolved event to a TimescaleDB hypertable, and **device-state** updates the device's latest reading + connectivity.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
