---
sidebar_position: 2
title: Domain Model
---

# Domain Model

DeviceChain models the physical world with a small set of composable concepts. The defining choice is that device *context* is expressed as a **typed relationship graph** rather than a fixed assignment record — which keeps the model open to new entity types over time.

## Core entities

- **Device** — an instance of a device profile; the thing that connects and reports.
- **Device Profile** *(evolving from "device type")* — shared configuration for a class of devices: transport, provisioning policy, alarm rules, OTA targets, and processing routing. Defining configuration once on a profile keeps a fleet consistent. *(In design.)*
- **Asset** — the real-world thing a device monitors (categorized as Device / Person / Hardware).
- **Area** — a spatial/organizational location, optionally with polygon boundaries and zones; areas nest into hierarchies.
- **Customer** — an organizational owner; customers also nest into hierarchies.
- **Groups** — named collections of any of the above, with role-tagged membership.

Every one of these entities is addressed uniformly by an **entity type + id**, which is what lets relationships and event indexing operate generically across all of them.

## Relationships

Instead of binding a device to a single fixed `(customer, area, asset)` assignment, DeviceChain connects entities with **typed, directed relationships**:

- A relationship has a **source**, a **target**, and a **relationship type**.
- A relationship type carries a **`Tracked`** flag.

The `Tracked` flag is central. When a device reports an event, the platform looks up the device's tracked relationships and denormalizes the **primary** one — the device's first assignment — onto the event as its **anchor** `(anchor_type, anchor_id)`. This is how a query like "every temperature reading for Building 7" resolves without joins. A device may hold **several** tracked relationships (assign it to a customer *and* an area *and* an asset); they all live in the graph, and the primary is the one denormalized onto events.

**Assignment organizes; it does not gate.** A device that is credentialed but not yet assigned still reports telemetry — its events resolve with a **null anchor** rather than being dropped. Assigning the device later gives its subsequent events a customer/area/asset anchor. (See [Managing device assignments](../guides/managing-assignments.md) and ADR-013.)

## Attributes vs. events

DeviceChain distinguishes **current state** from **history**:

- **Events** are the append-only, time-series record of everything a device reports (measurements, locations, alerts, command invocations/responses, state changes). They live in TimescaleDB hypertables.
- **Attributes** *(planned)* are the current key-value state of an entity, in three scopes:
  - `CLIENT` — reported by the device.
  - `SERVER` — platform-only metadata the device never sees.
  - `SHARED` — set by the platform and readable by the device (the channel for remote configuration and OTA targets).

## Identity and credentials

A device has a **stable identity** that everything else references, kept separate from its **credentials** (the material it uses to authenticate). Credentials are pluggable — **access token**, **MQTT-basic** (username + password), and **X.509 certificate** — so a device can rotate or hold multiple credentials without changing its identity. A credential's secret is **write-only**: it is submitted when the credential is registered and never returned on read. See [Device credentials](../guides/device-credentials.md).

## Events

Each event records the reporting device, the event type, the device-reported and platform-received timestamps, an optional external correlation id (`alternateId`) for idempotent ingestion, and the resolved relationship anchor described above (null when the device is unassigned). Event categories include measurements, locations, alerts, command invocations and responses, and state changes.
