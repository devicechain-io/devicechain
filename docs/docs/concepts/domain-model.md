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

The `Tracked` flag is central: when a device reports an event, the platform looks up that device's tracked relationships and **denormalizes them onto the event** as index dimensions. This is how a query like "every temperature reading for Building 7" resolves without joins, while still allowing arbitrary, evolving context (a device can relate to many customers, areas, assets, or even other devices simultaneously).

## Attributes vs. events

DeviceChain distinguishes **current state** from **history**:

- **Events** are the append-only, time-series record of everything a device reports (measurements, locations, alerts, command invocations/responses, state changes). They live in TimescaleDB hypertables.
- **Attributes** *(planned)* are the current key-value state of an entity, in three scopes:
  - `CLIENT` — reported by the device.
  - `SERVER` — platform-only metadata the device never sees.
  - `SHARED` — set by the platform and readable by the device (the channel for remote configuration and OTA targets).

## Identity and credentials

A device has a **stable identity** that everything else references, kept separate from its **credentials** (the material it uses to authenticate). Credentials are pluggable — access token, X.509 certificate, MQTT-basic — so a device can rotate or hold multiple credentials without changing its identity. *(In design; credentials beyond the connection token are planned.)*

## Events

Each event records the reporting device, the event type, the device-reported and platform-received timestamps, an optional external correlation id (`alternateId`) for idempotent ingestion, and the resolved relationship anchors described above. Event categories include measurements, locations, alerts, command invocations and responses, and state changes.
