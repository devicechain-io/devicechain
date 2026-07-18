---
sidebar_position: 2
title: Domain Model
---

# Domain Model

DeviceChain models the physical world with a small set of composable concepts. The defining choice is that device *context* is expressed as a **typed relationship graph** rather than a fixed assignment record — which keeps the model open to new entity types over time.

## Core entities

- **Device** — the thing that connects and reports; an instance of a device type.
- **Device Type** — the **taxonomy/identity** layer: a device class's name, appearance (icon and colors), and classification for grouping and filtering. A device type references at most one device profile.
- **Device Profile** — the **capability contract**: a distinct, **versioned** aggregate (draft → publish → rollback) that owns a device class's **metric**, **command**, and **detection-rule** definitions. Many device types can share one profile — so the capability config is defined once and reused — and a device resolves its capabilities through `device → type → profile`. A device type without a profile is valid; it just classifies and displays its devices without granting a typed capability contract.
- **Asset** — the real-world thing a device monitors (categorized as Device / Person / Hardware).
- **Area** — a spatial/organizational location, optionally with polygon boundaries and zones; areas nest into hierarchies.
- **Customer** — an organizational owner; customers also nest into hierarchies.
- **Groups** — one uniform **entity group** collects any of the above. Membership is either **static** (an explicit member list) or **dynamic** — a saved selector over the members' attributes, resolved on read (see [Facets and dynamic groups](#facets-and-dynamic-groups)).

Every one of these entities is addressed uniformly by an **entity type + id**, which is what lets relationships, groups, and event indexing operate generically across all of them.

## Relationships

Instead of binding a device to a single fixed `(customer, area, asset)` assignment, DeviceChain connects entities with **typed, directed relationships**:

- A relationship has a **source**, a **target**, and a **relationship type**.
- A relationship type carries a **`Tracked`** flag.

The `Tracked` flag is central. When a device reports an event, the platform records **each** of the device's tracked relationships as an **anchor** on that event (an `(anchor_type, anchor_id)` entry in the event's anchor set). A device may hold **several** tracked relationships — a customer *and* an area *and* an asset — and the reading is then queryable by **every** one of them: "every temperature reading for Building 7" and "…for customer Acme" both find it. Anchors are captured at write time, so history stays put when a device is later reassigned.

**Assignment organizes; it does not gate.** A device that is credentialed but not yet assigned still reports telemetry — its events resolve with a **null anchor** rather than being dropped. Assigning the device later gives its subsequent events a customer/area/asset anchor. (See [Managing device assignments](../guides/managing-assignments.md) and ADR-013.)

## Attributes vs. events

DeviceChain distinguishes **current state** from **history**:

- **Events** are the append-only, time-series record of everything a device reports (measurements, locations, alerts, command invocations/responses, state changes). They live in TimescaleDB hypertables.
- **Attributes** are the current key-value state of an entity, in three scopes:
  - `CLIENT` — reported by the device.
  - `SERVER` — platform-only metadata the device never sees.
  - `SHARED` — set by the platform and readable by the device (the channel for remote configuration and OTA targets).

## Facets and dynamic groups

Attributes double as **classification facets** — the axes you browse and filter entities by. A per-tenant **facet registry** declares which attribute keys (for a given entity family) are facets, giving the console browse UI its axes and value typeahead; it declares *which keys* are facets, not the values (the values stay as attributes on the entities themselves).

A **dynamic group** turns a facet filter into saved, self-updating membership. Its selector is a boolean expression over the members' attributes — for example `attr["climate"] == "arid" && attr["country"] == "US"` — written in [CEL](https://github.com/google/cel-go), the same expression language the detection engine uses. The platform validates and cost-limits the selector when the group is saved, then resolves membership **on read** by lowering the expression to an indexed database query (never by scanning every entity), so a dynamic group always reflects the current attribute state without any materialized cache to keep in sync. A static group, by contrast, holds an explicit member list. The console's **Browse** screen composes a selector from facet axes, previews the matching count live, and saves it as a dynamic group; the **Facets** screen manages the registry.

## Commands and the capability contract

A device profile can declare the **commands** its devices accept, each with a typed
parameter schema (name, data type, required, min/max, enum). Those declarations are what
make the profile a contract rather than a label.

When a command is enqueued, it is validated against the **published** profile version — not
the draft. Three outcomes:

- **The profile declares no commands.** Anything is accepted. Declaring a vocabulary is
  opt-in, so a profile that has not adopted one keeps working exactly as before.
- **The profile declares commands, and the key matches one.** The payload is validated
  against that command's parameter schema: unknown parameters, wrong types, out-of-range
  values, and missing required parameters are all rejected.
- **The profile declares commands, and the key matches none.** Rejected — a device cannot
  be sent a command its capability contract does not include.

Command keys are matched **exactly**, including case. A mis-cased key is a mis-keyed
actuation, which is the thing this validation exists to stop.

Validation reads the published snapshot deliberately. A definition you have authored but
not yet published has not been communicated to anything downstream, so enforcing it would
reject commands the device actually accepts. Publish the profile to put a new command into
force.

The published vocabulary is readable, not just enforceable: a device reports which commands
it currently accepts, and the console uses that to offer them directly — a picker of
declared commands and a typed form built from the selected command's parameter schema,
rather than a free-text box. A profile that declares no commands still gets the free-text
form, matching what the platform will accept. Commands you have authored but not published
are named alongside the picker as unavailable, so a missing command reads as "not published
yet" rather than as a missing feature.

## Command lifecycle

An issued command is persisted and tracked, not fire-and-forget. It moves through:

- **`QUEUED`** — accepted and validated, waiting to be dispatched.
- **`SENT`** — published to the device's own command topic.
- **`SUCCESSFUL`** / **`FAILED`** — the device reported the outcome.
- **`TIMEOUT`** / **`EXPIRED`** — a TTL elapsed. `EXPIRED` means it never went out;
  `TIMEOUT` means it did and was never answered. Cancelling a command also records
  `EXPIRED`.

**A command only reaches a terminal outcome if the device answers.** Reporting the result
is the device's half of the contract — see
[Responding to a command](../guides/connecting-a-device.md#responding-to-a-command). A
device that never responds leaves its commands in `SENT` until they expire, and a command
issued with no `expiresAt` stays there indefinitely, so set one if your devices do not
report outcomes.

Each device receives commands on a topic scoped to that device alone, and is authorized
for that topic only — a device cannot observe commands addressed to any other device in
its tenant.

## Identity and credentials

A device has a **stable identity** that everything else references, kept separate from its **credentials** (the material it uses to authenticate). Credentials are pluggable — **access token**, **MQTT-basic** (username + password), and **X.509 certificate** — so a device can rotate or hold multiple credentials without changing its identity. A credential's secret is **write-only**: it is submitted when the credential is registered and never returned on read. See [Device credentials](../guides/device-credentials.md).

A device may also carry an optional **`externalId`** — a customer-owned **business key** such as a VIN, serial number, GS1 code, or asset tag. It is distinct from both the internal identity and the credential: it is **opaque** (no format constraints), **unique within a tenant** when present, and **never used for addressing or authentication**. Its purpose is lookup and integration — matching a DeviceChain device to the identifier your other systems already use for the same physical thing.

## Events

Each event records the reporting device, the event type, the device-reported and platform-received timestamps, an optional external correlation id (`alternateId`) for idempotent ingestion, and the resolved relationship anchor described above (null when the device is unassigned). Event categories include measurements, locations, alerts, command invocations and responses, and state changes.

Measurements are **self-describing**: when a reading matches a metric defined on the device's profile, the platform stamps that metric's **unit** and **data type** directly onto the persisted reading (and onto the live last-known-state projection). A consumer reading a measurement gets its semantics — `22.4 °C`, a `DOUBLE` — without a second lookup against the profile.
