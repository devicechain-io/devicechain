---
title: Domain Model
---

# Domain Model

DeviceChain models the physical world with a small set of composable concepts. The defining choice is that you express a device's *context* as a **typed relationship graph**, not a fixed assignment record. That keeps the model open to new entity types over time.

## Core entities

- **Device**: the thing that connects and reports. Each device is an instance of a device type.
- **Device Type**: the taxonomy and identity layer. It holds a device class's name, appearance (icon and colors), and classification for grouping and filtering. A device type references at most one device profile.
- **Device Profile**: the capability contract. It is a distinct, versioned resource (draft → publish → rollback) that owns a device class's metric, command, and detection-rule definitions, and the configuration keys its devices receive. Publishing freezes all of them into the new version. Many device types can share one profile, so you define the capability config once and reuse it. A device resolves its capabilities through `device → type → profile`. A device type without a profile is valid: it classifies and displays its devices but grants no typed capability contract.
- **Asset**: the real-world thing a device monitors, classified by a tenant-defined **asset type**. An asset type may publish a versioned property schema, and its assets' properties are validated against it. Assets form a parent/child hierarchy, with one parent per asset and no cycles.
- **Area**: a named spatial or organizational location, classified by an **area type**. Spatial boundaries are modelled separately as [geofences](./geofencing.md), not as fields on the area.
- **Customer**: an organizational owner, classified by a **customer type**.
- **Groups**: one uniform **entity group** collects members of one family (devices, assets, areas, or customers), chosen when you create the group. Membership is either static, an explicit member list, or dynamic, a saved selector over the members' attributes that is resolved on read (see [Facets and dynamic groups](#facets-and-dynamic-groups)).

Devices, assets, areas, customers, and groups are all addressed the same way: by an entity type plus a stable per-tenant token. That uniform address is what lets relationships, groups, and event indexing work generically across them. Device types and device profiles are not addressable entities in this sense; they are configuration a device resolves through.

## Relationships {#relationships}

DeviceChain does not bind a device to a single fixed `(customer, area, asset)` assignment. Instead, it connects entities with typed, directed relationships:

- A relationship has a **source**, a **target**, and a **relationship type**.
- A relationship type carries a **`Tracked`** flag.

The `Tracked` flag is central. When a device reports an event, the platform records each of the device's tracked relationships as an **anchor** on that event. An anchor is an `(anchor_type, anchor_token)` entry in the event's anchor set: the target's entity type and its stable per-tenant token.

A device may hold several tracked relationships, such as a customer *and* an area *and* an asset. The reading is then queryable by every one of them: "every temperature reading for Building 7" and "…for customer Acme" both find it. Anchors are captured at write time, so history stays put when you later reassign the device.

**Assignment organizes; it does not gate.** A device that is credentialed but not yet assigned still reports telemetry. Its events resolve with an empty anchor set rather than being dropped. When you assign the device later, its subsequent events get a customer/area/asset anchor. See [Managing device assignments](../guides/managing-assignments.md).

## Attributes and events {#attributes-vs-events}

DeviceChain separates current state from history:

- **Events** are the append-only, time-series record of everything a device reports: measurements, locations, alerts, command invocations and responses, and state changes. They live in TimescaleDB hypertables.
- **Attributes** are the current key-value state of an entity, in three scopes:
  - `CLIENT`: reported by the device.
  - `SERVER`: platform-only metadata the device never sees.
  - `SHARED`: set by the platform for the device. The shared keys a device's profile declares as configuration are delivered to the device as its [configuration](../guides/connecting-a-device.md#reserved-device-topics); shared keys the declaration does not name are never sent. Firmware and software updates travel separately, as [over-the-air update](./over-the-air-updates.md) assignments.

## Facets and dynamic groups {#facets-and-dynamic-groups}

Attributes also serve as **classification facets**, the axes you browse and filter entities by. A per-tenant **facet registry** declares which attribute keys are facets for a given entity family. That gives the console browse UI its axes and value typeahead. The registry declares only *which keys* are facets; the values stay as attributes on the entities themselves.

A **dynamic group** turns a facet filter into saved, self-updating membership. Its selector is a boolean expression over the members' attributes, for example `attr["climate"] == "arid" && attr["country"] == "US"`. You write it in [CEL](https://github.com/google/cel-go), the same expression language the detection engine uses.

The platform validates and cost-limits the selector when you save the group. It then resolves membership on read by translating the expression into an indexed database query, never by scanning every entity. A dynamic group therefore always reflects the current attribute state, with no materialized cache to keep in sync. A static group, by contrast, holds an explicit member list.

In the console, the **Browse** screen composes a selector from facet axes, previews the matching count live, and saves it as a dynamic group. The **Facets** screen manages the registry.

## Commands

A device profile declares the commands its devices accept. An issued command is persisted and tracked through a lifecycle rather than fired and forgotten. [Commands](./commands.md) covers both.

## Exporting a profile as a W3C Thing Model {#wot-thing-model}

Every published version of a device profile can be exported as a [W3C Web of Things](https://www.w3.org/TR/wot-thing-description11/) **Thing Model**, the standard JSON-LD description of a class of devices. A profile describes every device of its types and no one device in particular, so the export is a Thing Model rather than a Thing Description: it carries no device address, protocol binding or security, which a Thing Description for one device would add.

- Each **metric** becomes a read-only, observable **property** named by its metric key. Its data type becomes the property's type (`DOUBLE` → `number`, `INT` → `integer`, `BOOLEAN` → `boolean`, `STRING` → `string`), and its unit, minimum, maximum and allowed values are carried over. A metric's **descriptor** becomes the property's `@type`, so a semantic tag such as `saref:Temperature` reaches tools that understand it.
- Each **command** becomes an **action** named by its command key. Its parameters become the action's `input`: an object schema with one property per parameter, typed the same way, nested parameters as nested objects, and the required ones listed as required.
- The model's `version.model` is the profile version number. The metrics and commands come from the published version, which never changes, but the model's `title` and `description` are the profile's **current** name and description, so exporting the same version after the profile is renamed gives a different title.
- A version that declares a constraint a Thing Model cannot express is **refused**, not exported without the constraint. Numeric bounds on a property that is not numeric, allowed values that are not a list of distinct text values, and allowed values that cannot be written in the property's type are examples. The refusal names the metric or command, so you can correct it in a new version.

In the console, open the profile's **Versions** tab and choose **WoT Thing Model** on a version to download it. Through GraphQL, select `thingModel` on a profile version; it is the JSON-LD as text:

```graphql
query {
  deviceProfileVersions(token: "pump", limit: 1) {
    version
    thingModel
  }
}
```

## Identity and credentials

A device has a stable **identity** that everything else references, kept separate from its **credentials**, the material it uses to authenticate. Credentials are pluggable: access token, MQTT-basic (username + password) and an X.509 client certificate that chains to a [trust anchor](./device-certificates.md) the tenant has added. A device can therefore rotate credentials or hold several without changing its identity.

A device can also be **disabled** as a whole, whatever credentials it holds: its events are discarded, it cannot open a new connection, its configuration and update traffic is refused and commands to it are refused, until it is enabled again. See [Disabling a device](../guides/device-credentials.md#disabling-a-device).

A credential's secret is **write-only**: you submit it when you register the credential, and it is never returned on read. That covers only the MQTT-basic password. For an access token, the credential id is itself the proof of possession, so reading a device's credentials at all requires the `device:write` authority rather than `device:read`. See [Device credentials](../guides/device-credentials.md#reading-a-credential).

A device may also carry an optional **`externalId`**: a customer-owned business key such as a VIN, serial number, GS1 code, or asset tag. It is distinct from both the internal identity and the credential. It is:

- opaque, with no format constraints,
- unique within a tenant when present, and
- never used for addressing or authentication.

Its purpose is lookup and integration: matching a DeviceChain device to the identifier your other systems already use for the same physical thing.

## Events

Each event records:

- the reporting device,
- the event type,
- the device-reported and platform-received timestamps,
- an optional external correlation id (`altId`) for idempotent ingestion, and
- the set of resolved relationship anchors described above: zero or more `(anchor_type, anchor_token)` rows. The set is empty when the device is unassigned.

Event categories include measurements, locations, alerts, command invocations and responses, and state changes.

Measurements are **self-describing**. When a reading matches a metric defined on the device's profile, the platform stamps that metric's unit and data type directly onto the persisted reading, and onto the live last-known-state projection. A consumer reading a measurement gets its semantics (`22.4 °C`, a `DOUBLE`) without a second lookup against the profile.
