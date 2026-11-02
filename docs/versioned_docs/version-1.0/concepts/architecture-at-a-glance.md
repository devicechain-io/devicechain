---
title: Architecture at a glance
---

# Architecture at a glance

This page is the short version. It shows the path an event takes from a device to a dashboard, an
alarm and a command back to the device. [Architecture](./architecture.md) is the long version: it
describes every component, the data and messaging backbone, the deployment model and the secret
store.

![Devices connect to ingest services, which hand events to device-management for resolution. Resolved events go to event-management for storage, to device-state for live state, and to event-processing for detection and reaction. Reactions raise alarms, notify people, send commands back to devices and call outbound connectors. The console, dashboards, GraphQL API and MCP read the results.](/img/architecture-overview.svg)

## The shape of it {#shape}

- **Devices reach the platform through an ingest service.** The platform's broker is the MQTT server
  built into NATS. `event-sources` takes MQTT and HTTP, `sparkplug-ingest` and `lwm2m-ingest` are
  opt-in services for those protocols, and a LoRaWAN network server posts to an
  [inbound integration](../guides/inbound-integrations.md).
- **Every event is resolved once, near the front.** `device-management` identifies the device and
  attaches its context to the event, so everything downstream reads a self-describing event and
  needs no join.
- **Three consumers read the resolved events independently.** `event-management` stores them,
  `device-state` keeps the last known state of each device, and `event-processing` evaluates rules.
  A slow consumer does not slow the others, because NATS JetStream sits between them.
- **Reactions go through the services that own the thing being changed.** An alarm is an object in
  `device-management`, a command is a record in `command-delivery`, and an outbound call is
  delivered by `outbound-connectors`, in its own process.
- **People and tools read through GraphQL.** The console, the dashboards, your own code and the
  optional [MCP server](./mcp.md) all use the same per-service GraphQL APIs under the caller's own
  token.
- **One instance serves many tenants.** Subjects on the bus and rows in the database carry the
  tenant; see [Multi-Tenancy](./multi-tenancy.md).

## How a message flows {#how-a-message-flows}

Follow one reading, a fuel level, from a device to a dashboard, then see what a rule does with it.

1. **The device publishes.** It connects to the platform broker with its credential and publishes a
   JSON event to its own topic, `{instance}/{tenant}/devices/{token}/events`. The broker lets it
   publish to that topic and no other. See [Connecting a Device](../guides/connecting-a-device.md).
2. **Ingest decodes it.** `event-sources` takes the message, checks that the device in the body is
   the device in the topic, applies the tenant's ingest rate limit, and publishes the decoded event
   onto the pipeline. A message it cannot decode goes to a `failed-decode` stream instead.
3. **The event is resolved.** `device-management` authenticates the device and looks up its
   [device type and profile](./domain-model.md), and its assignments and tracked relationships, to
   customer, area and asset. It writes those onto the event as index dimensions and publishes the
   resolved event.
4. **The event is stored.** `event-management` writes it to TimescaleDB, in batches, partitioned by
   tenant. Lifecycle policies (compression, retention, rollups) run there.
5. **Live state updates.** `device-state` consumes the same resolved event and updates the device's
   current value for that measurement, stamped with the unit and type from its profile.
6. **A dashboard updates.** A dashboard widget has a GraphQL subscription open against the services
   that hold the data. `event-management` pushes the new reading over its WebSocket bridge, and the
   widget redraws. See [Dashboards](./dashboards.md).
7. **A rule evaluates it.** `event-processing` consumes the same resolved event and runs it through
   its detection rules. When the fuel level falls below a threshold, the rule fires on the rising
   edge. See [Event Processing & Alarms](./event-processing.md).
8. **The firing acts.** The rule's actions run. One raises an alarm in `device-management`, an
   object with a lifecycle that clears itself on the falling edge. Another enqueues a command.
   Others can hand a payload to `outbound-connectors`, which delivers it to a webhook, a broker or
   a cloud queue.
9. **A person hears about it.** `notification-management` routes the alarm to email or a webhook,
   according to the tenant's policy, and can re-notify while it stays unacknowledged.
10. **The command makes the round trip.** `command-delivery` persists the command, checks that the
    device is present, and publishes it to the device's own command topic. The device acts and
    publishes its result to its own command-response topic, and `command-delivery` records the
    outcome. The command then ends as a success, a rejection by the device, or one of the states
    that say it never arrived; see [Commands](./commands.md#command-lifecycle). If the device was
    away, the command was held until it returned.

The steps run on separate services connected by NATS JetStream, so each one can be scaled and
restarted on its own. [Observability](../deployment/observability.md) describes the metrics and
alerts that show where an event is held up.

## Where to go next {#where-next}

- [Architecture](./architecture.md) for every component and how the platform is deployed.
- [Domain Model](./domain-model.md) for devices, profiles, relationships and events.
- [Transport Capability Matrix](../reference/transport-matrix.md) for what each way of connecting
  does in each direction.
- [Measured throughput](../deployment/bootstrap.md#measured-throughput) for what has been measured,
  on which workload and cluster.
