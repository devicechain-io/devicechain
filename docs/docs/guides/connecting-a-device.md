---
sidebar_position: 2
title: Connecting a Device
---

# Connecting a Device

Devices connect to DeviceChain over **MQTT**, which is served directly by NATS' built-in MQTT server on port 1883 — there is no separate broker. A standard MQTT client (Arduino, ESP32, Eclipse Paho, etc.) works unchanged.

:::note Status
MQTT ingestion is available. Additional transports (HTTP, CoAP, WebSocket) and the full provisioning/credentials flow are planned — see the [Domain Model](../concepts/domain-model.md#identity-and-credentials).
:::

## Topic / subject mapping

An MQTT topic maps directly to a NATS subject, so a publish on:

```
dc/{tenant}/devices/{token}/events
```

is consumed by the `event-sources` service as the NATS subject:

```
dc.{tenant}.devices.{token}.events
```

`{token}` identifies the device at connection time.

## Publishing telemetry

Publish a JSON payload to your device's events topic:

```bash
mosquitto_pub \
  -h localhost -p 1883 \
  -t "dc/acme/devices/sensor-001/events" \
  -m '{"type":"measurement","name":"temperature","value":21.5}'
```

The platform decodes the message, resolves the device and its tracked relationship context, and persists the event to TimescaleDB, where it is queryable via the event-management GraphQL API.

## What happens next

1. **event-sources** decodes the raw message.
2. **device-management** resolves the device by its token and attaches the device's tracked relationships (customer, area, asset, …) as index dimensions.
3. **event-management** persists the resolved event to a TimescaleDB hypertable.

See [Architecture → The event pipeline](../concepts/architecture.md#the-event-pipeline).
