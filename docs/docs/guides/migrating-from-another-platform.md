---
title: Moving to DeviceChain from another IoT platform
---

# Moving to DeviceChain from another IoT platform

This guide is for a team that runs a fleet on another IoT platform and wants to know what moving
to DeviceChain involves. It maps the concepts you already use to their DeviceChain equivalents,
lists the migration paths that exist today, says plainly what has no direct equivalent, and
suggests an order of work.

There is no one-click converter. DeviceChain does not read another platform's export files, so a
migration is a rebuild of your model in DeviceChain plus a move of your devices. The parts that
carry the most weight, your devices and their credentials, can be loaded in bulk. The parts that
are logic, such as rules and dashboards, are rebuilt by hand. This guide does not name other
products, because the mapping depends on the concepts and not on the product.

## How the concepts map {#concept-map}

Platforms use different words for similar things, and the same word for different things. Read the
right-hand column as "the closest DeviceChain concept", and read the notes where the match is
partial.

| In your current platform | In DeviceChain | Notes |
| --- | --- | --- |
| **Device** | [Device](../concepts/domain-model.md#core-entities), addressed by a **token** you choose | The token is the identity and does not change. Your old device name or serial number can go in `externalId` (unique per tenant) and your old identifier in an [alias](../user-guide/devices.md#aliases). |
| **Device profile or device type** | A [device type](../user-guide/device-types-and-profiles.md) that references a versioned [device profile](../user-guide/device-types-and-profiles.md) | DeviceChain splits the idea in two. The **type** classifies and displays devices. The **profile** is the versioned contract: it owns the metric definitions, the commands, the detection rules and the configuration keys. Many types can share one profile. |
| **Telemetry key** | A **metric** declared on the profile, with a data type and unit | Readings arrive as `measurements` keyed by metric name. A metric is `DOUBLE`, `INT` or `BOOLEAN` and is stored as a time series (a boolean is stored as 0 or 1). See [payload shapes](./connecting-a-device.md#payload-shapes) and [measurement](./connecting-a-device.md#measurement). |
| **Attributes** (client-side, server-side, shared) | [Attributes](../concepts/domain-model.md#attributes-vs-events) with the scopes `CLIENT`, `SERVER` and `SHARED` | The scopes mean the same: reported by the device, platform-only, and set by the platform for the device. Shared attributes a profile declares as configuration are delivered to the device; see [configuration delivery](./connecting-a-device.md#reserved-device-topics). Attributes also serve as [facets](../user-guide/groups-facets-browse.md) for browsing and grouping. |
| **Asset** | [Asset](../user-guide/assets-customers-areas.md), classified by an asset type | An asset type can publish a versioned [property contract](../user-guide/assets-customers-areas.md#asset-types). Assets nest, with one parent each. |
| **Relations** between entities | [Typed, directed relationships](../concepts/domain-model.md#relationships) | A relationship type can be marked **tracked**. A tracked relationship from a device is stamped on each event as an anchor, so readings can be queried by customer, area or asset and keep that history if you reassign the device later. Group membership uses the same graph. See [managing assignments](./managing-assignments.md). |
| **Customer** | [Customer](../user-guide/assets-customers-areas.md), plus **customer users** whose membership is limited to that customer's data | A customer is a record in your tenant. Giving a person access limited to it is separate; see [customer users](../user-guide/members-roles-and-customer-users.md#customer-users). Customers do not nest. |
| **Tenant**, tenant profile | [Tenant](../concepts/multi-tenancy.md) and [tenant tier](../concepts/tenant-tiers.md) | One installation serves many tenants, each isolated from the others. A tier is the operator-defined packaging that sets a tenant's ceilings. |
| **Users and roles** | Members, [roles](../user-guide/members-roles-and-customer-users.md#roles), and [single sign-on](../concepts/single-sign-on.md) | Single sign-on uses OpenID Connect. For scripts, use a [service account](../user-guide/service-accounts.md). |
| **Device group** | [Entity group](../user-guide/groups-facets-browse.md), static or dynamic | A dynamic group is a saved selector over attributes, resolved when read. Groups hold one family of members: devices, assets, areas or customers. |
| **Rule chain, flow or workflow** | [Detection rules](../concepts/event-processing.md) on the profile, authored as a [form or on the canvas](../user-guide/detection-rules.md) | A rule watches events and, when its condition holds, acts. The conditions and what they cannot do are in [what has no direct equivalent](#no-equivalent). |
| **Alarm** | [Alarm](../user-guide/alarms.md) with a lifecycle, severity and escalation | Raised and cleared by detection rules. A device can also report an alert event. |
| **Notification or alert delivery** | [Notification policies](../user-guide/notification-policies.md) | Per-tenant policy, with email and webhook delivery and escalation. |
| **Calling out to other systems** | [Outbound connectors](../concepts/outbound-connectors.md) and the webhook action | A rule action can call a webhook or publish to MQTT, Kafka, AWS SNS or AWS SQS. |
| **Derived or computed telemetry** | [Calculated metrics](../user-guide/calculated-metrics.md) | Stored as a metric of its own, and can be backfilled over history. |
| **RPC to a device** | [Commands](../concepts/commands.md) | A command is declared on the profile with typed parameters, checked when it is issued, kept until the device is reachable, and tracked until the device reports a result or its time-to-live runs out. See [sending commands](./sending-commands.md) and [commanding a fleet](./commanding-a-fleet.md). |
| **Firmware or software update** | [Over-the-air updates](../concepts/over-the-air-updates.md): artifacts, signers, assignments and group rollouts | The device side is a small protocol you implement; see [building an updatable device](./building-an-updatable-device.md). |
| **Dashboard** | [Dashboard](../concepts/dashboards.md), a versioned tenant resource (draft, publish, roll back) | Rebuilt, not imported; see [dashboards](#dashboards). |
| **Device credentials** (access token, basic, certificate) | [Credentials](./device-credentials.md): `ACCESS_TOKEN`, `MQTT_BASIC` and [X.509](./device-x509.md) | Details in [credentials and connecting devices](#connecting-devices). |
| **Gateway** | A [gateway device](../deployment/edge-services.md#gateway-identity) that speaks for the devices assigned to it | Also usable with the edge agent. |
| **Integration with a LoRaWAN network server** | [Inbound integration](./inbound-integrations.md) | A webhook that maps the network server's uplinks to devices. |

## Migration paths that exist today {#paths}

These are the things you can do now. Each is a documented feature, not a plan.

### Build your model through the API {#model}

Before devices can report, DeviceChain needs the model they report into: device types and
profiles, asset and customer types, assets, customers and the relationships among them. There is
no import file for these. Create them in the console, or script them against the
[GraphQL API](../reference/graphql-api.md), which has a mutation for each (`createDeviceProfile`,
`createDeviceType`, `createAsset`, `createCustomer`, `createEntityRelationships` and so on). The
relationship mutation takes up to 1,000 relationships in one call.

A script signs in with a [service account](../user-guide/service-accounts.md#obtaining-a-token):
a non-human account of your tenant that presents a client ID and secret and receives a short-lived
token. Give it a role that grants only what the script needs.

The profile is where most of the effort goes, because it is the contract. Decide each metric's
key, type and unit once, publish the profile, and then bring devices in. A published version is
frozen; a later change is a new version. See
[device types and profiles](../user-guide/device-types-and-profiles.md).

### Load devices from a CSV file {#csv}

[Importing devices from CSV](./importing-devices.md) is the bulk door for an inventory. It is in the
console (**Devices → Import**) and over GraphQL as `importDevices`.

- Columns are `token`, `deviceTypeToken`, `externalId`, `name`, `description`, `metadata` and, if you
  want the device earmarked for self-registration, `provisioningProfileToken`.
- With **factory credentials** the file also carries `credentialType`, `credentialId` and, for
  `MQTT_BASIC`, `credentialValue`. This is how you keep a device's existing access token: put it in
  `credentialId` with type `ACCESS_TOKEN`, and a device that presents that token keeps working
  without being reflashed with a new one. The secret is stored only as a keyed digest and is never
  shown again.
- The import is create-only and safe to run again. A **Check the file** step is a dry run that
  decides every row exactly as the real import will and writes nothing.
- A file may hold 10,000 rows and 5 MiB; split a larger fleet into several files.
- Export your old inventory to CSV with the headers above, or use the console's own **Export CSV**
  from a DeviceChain tenant as a template.

Credentials in the file must satisfy DeviceChain's rules. An `MQTT_BASIC` password must be at least
16 bytes, so an older, shorter device password cannot be carried across and needs rotating. Rows
that break a rule come back as `INVALID` with the reason, and the failed rows can be downloaded,
corrected and imported again.

### Register devices as they connect {#self-registration}

For devices you can reflash or reconfigure, [self-registration](./device-self-registration.md) lets
a device create itself with a provisioning profile's key and secret and a serial number, then
receive its token and MQTT client id. `dcctl device register` plays that part from a shell, which
is a useful way to test a profile before a device uses it; see the
[command reference](../reference/dcctl.md#dcctl-device-register). Certificate-based fleets can use
[X.509 credentials](./device-x509.md) with a trust anchor your tenant adds.

### Re-point MQTT devices {#connecting-devices}

A device that already speaks MQTT needs its connection settings changed, and in general its payload
too. Plan for these differences from what it does today:

- **Broker address and TLS.** The listener is TLS, and a device connects with the instance CA; see
  [connection settings](./connecting-a-device.md#connection-settings).
- **Client id.** It must be `{instanceId}:{tenant}:{deviceToken}`, optionally with a `:suffix`. The
  broker refuses any other client id, including a random one a library invents.
- **Username.** It is `{tenant}:{credentialId}`, not the credential id alone.
- **Topic.** Events are published to `{instanceId}/{tenant}/devices/{token}/events`.
- **Body.** A JSON envelope with a `device`, an `eventType` and `payload.entries`, in which each
  reading is a JSON string. Over MQTT the body carries no credential. See
  [the event body](./connecting-a-device.md#the-event-body) and
  [three identifiers](./connecting-a-device.md#three-identifiers), the page's list of the three
  values most often confused.

If you cannot change the firmware, put a small bridge in front: a process that subscribes to the old
topics and publishes to DeviceChain, or a [gateway device](../deployment/edge-services.md#gateway-identity)
that speaks for a set of devices assigned to it. Devices on HTTP post the same envelope to the
ingest endpoint described in [Connecting a device](./connecting-a-device.md).

Devices behind a LoRaWAN network server do not change at all. Add an
[inbound integration](./inbound-integrations.md) and point the network server's webhook at it.
Sparkplug B and LwM2M devices connect through their own adapters; see
[Sparkplug](../concepts/sparkplug.md) and [LwM2M](../concepts/lwm2m.md), and the
[transport matrix](../reference/transport-matrix.md) for what each supports.

### Use dcctl for the instance, not for your data {#dcctl}

`dcctl` installs, bootstraps, upgrades and inspects an instance; see the
[command reference](../reference/dcctl.md) and [bootstrap](../deployment/bootstrap.md). It does not
import entities or readings. Use it to stand up the instance that will receive the migration, and
for the checks it offers, such as `dcctl device register` above.

### Move history {#history}

Nothing converts another platform's stored history for you: there is no history importer, and
reading the old platform's database or export files is work you do yourself. What DeviceChain
offers is the ingest path, which accepts readings with their own time. An entry's `occurredTime` is the instant it is stored, charted and
evaluated at, so you can replay history through the same ingest path as live data. Two limits apply:
a reading with an `occurredTime` more than **366 days** older than the moment the platform receives
it is refused, and ingest is rate-limited per tenant, so a large replay should be paced. Set an
`altId` on every replayed event so a retry does not store it twice. See
[buffered uploads and windowed rules](./connecting-a-device.md#buffered-uploads-and-windowed-rules)
and [quality of service](./connecting-a-device.md#quality-of-service).

If you need older history, or the volume is large, decide first whether you need it in the new
platform at all. Keeping the old store read-only for reporting, and starting the new one from the
cutover, is often the cheaper answer. To make DeviceChain's data reachable from your existing
reporting tools afterwards, see [SQL and BI access](./sql-and-bi-access.md).

### Rebuild dashboards {#dashboards}

A dashboard is a JSON definition that DeviceChain can export and import, but it imports DeviceChain
dashboards only: widgets are bound to DeviceChain's own metrics and entities. Rebuild each board in
the [canvas editor](../user-guide/dashboards.md), using slots so a board binds to a device or a
customer when it is opened instead of naming one. An imported board is saved as a draft under a new
token and is never published for you; see
[importing a dashboard](../user-guide/dashboards.md#importing-a-dashboard). Once you have a few
boards, export them as your templates.

## A worked example {#worked-example}

The repository carries a runnable evaluation under `examples/migration-pilot`
([on GitHub](https://github.com/devicechain-io/devicechain/tree/main/examples/migration-pilot)).
It starts from a small, invented export of "the previous platform": 25 devices of two types, their
telemetry keys, two threshold rules and one script-based rule, a dashboard description and an
access token per device. It then does, with `curl` and `jq` against the public GraphQL API and
nothing else:

1. **A plan.** A mapping file turns each concept into its DeviceChain counterpart, and the plan
   prints the result, including what will not be carried, before anything is written.
2. **The model.** It creates the profiles, metrics, detection rules and device types, and publishes
   the profiles.
3. **A dry run, then the import.** The device import described [above](#csv) checks all 25 rows
   without writing, then creates 23 devices with their existing access tokens. The other two come
   back with a reason: a password shorter than 16 bytes, and a serial number that appears twice.
4. **A reconciliation report.** Every item in the export is accounted for as created, already there
   or skipped with a reason, and the counts are then read back from DeviceChain and compared.
5. **A side-by-side pilot.** Three simulated devices publish to a stand-in for the old platform and
   to DeviceChain at the same time, and the values are compared. Flipping one line in the device's
   configuration moves it back, and the example shows both that and the hard stop in DeviceChain.

It is a feasibility path, not a converter. It does not read the old platform's files for you, it
does not rebuild rules or dashboards, it does not convert history, and a device still needs its
connection settings and its payload changed. The example says so in detail, including what each
limit costs.

## What has no direct equivalent {#no-equivalent}

Some things do not carry across, and a few are not planned. Check this list against what your
current deployment relies on before you commit to a date.

- **A converter for another platform's files.** DeviceChain has no importer for another platform's
  export format, for its dashboards, or for its rule definitions. You rebuild them.
- **Rules as a free-form node graph.** Detection rules are conditions over events, written as a form,
  on a canvas, or in the same expression language the detection engine uses, and they act by raising
  an alarm, sending a command, calling a webhook or publishing to a connector. There is no node
  that runs arbitrary script code in the middle of a flow, and no general-purpose flow engine. A
  chain that does enrichment, branching and external calls in one graph usually becomes several
  rules plus a calculated metric, and anything more complex belongs in a service you call by
  webhook. Read [event processing](../concepts/event-processing.md) before you plan the port.
- **Device payload decoders you write yourself.** The platform stores values that a network server
  decodes, and can map a generic JSON body; it does not run a decoder script you supply. If your
  devices send an opaque binary payload, decode it before it reaches DeviceChain, in the network
  server's codec or in a bridge. See [inbound integrations](./inbound-integrations.md).
- **Text readings as history.** Numeric and boolean readings are stored as time series. A text
  reading is not, so a string-valued signal is held as state, such as an attribute, and not charted
  as history.
- **Nested customers.** Customers do not have parents. Assets nest, and relationships can express
  other structure. If you relied on a customer hierarchy for visibility, model it with groups and
  scoped memberships and test what a user sees.
- **Several of the over-the-air update behaviours some platforms offer.** Rollouts have no waves,
  canary promotion or schedules, and only devices connected over MQTT receive managed updates. See
  [over-the-air updates](../concepts/over-the-air-updates.md).
- **Industrial protocol drivers, SNMP and a native mobile app.** The same limits as in
  [Why DeviceChain](../why-devicechain.md#not-yet): use a gateway that speaks the protocol and
  forwards over MQTT or HTTP.
- **Platforms other than Kubernetes.** DeviceChain runs on Kubernetes. If you are moving from a
  single-server install, budget for that change as well as the data move.

Where the missing piece blocks you, say so in the project's
[Discussions](https://github.com/devicechain-io/devicechain/discussions/categories/ideas); the
[roadmap](https://github.com/devicechain-io/devicechain/blob/main/ROADMAP.md) shows what is already
planned.

## A suggested plan {#plan}

The order below reduces risk by moving a few devices first, running both platforms side by side,
and cutting over only when you can see the new one agrees with the old.

### 1. Inventory and design

1. List what you run: device classes, the telemetry keys and units each reports, attributes,
   commands, rules, dashboards, customers, users and integrations.
2. Mark each item as carried across, rebuilt, or dropped, using the tables above.
3. Design the DeviceChain model on paper: one profile per device class, the metric keys and units,
   the relationship types you will mark tracked, and your customer and area structure. Choose
   tokens you will keep for the life of the device.
4. Decide how each device class will connect: unchanged behind a network server, reconfigured, or
   behind a bridge.

### 2. Stand up a pilot

1. Bring up an instance with the [quickstart](../quickstart/first-device.md) for a trial, or follow
   [bootstrap](../deployment/bootstrap.md) for a real one, and read
   [tuning an instance](../deployment/tuning-an-instance.md) and the
   [measured throughput](../deployment/bootstrap.md#measured-throughput) against your fleet size.
2. Create the model through the API or console, publish the profiles, and import a **pilot fleet**
   of a few dozen devices from CSV, with their existing credentials if you are keeping them.
3. Re-point those devices, or their bridge, and confirm readings arrive with the right units on the
   device's **Connectivity** and **Events** tabs.
4. Rebuild the rules those devices need and the dashboard their owners use, and confirm an alarm
   raises, notifies and clears the way it does today.

### 3. Run in parallel

1. Send the pilot devices' readings to both platforms. A device or a bridge can publish to both, or
   a network server can have two webhooks.
2. Compare the two for a period that includes the conditions you care about: the same alarms at the
   same times, the same totals for a day, a command that reaches a device and reports back.
3. Fix the differences in the model, not in the data. A wrong unit or key is a new profile
   version.
4. Widen the pilot by device class, importing each class from its own CSV file and checking before
   you move on.
5. Train the people who will operate it. Create their [members and roles](../user-guide/members-roles-and-customer-users.md),
   set up [single sign-on](./configuring-sso.md) if you use it, and invite customer users to a
   customer's own view before it goes live.

### 4. Cut over

1. Choose a quiet window, and a rollback: the old platform stays running and its devices are not
   deleted.
2. Re-point the remaining devices, class by class, watching each class's connection and event
   counts before starting the next. Self-registered fleets move as each device is next configured.
3. Replay any history you decided to bring, with `altId` and `occurredTime` set.
4. Move alarm notifications and integrations to DeviceChain, and turn them off on the old platform
   so people are not paged twice.
5. Keep the old platform read-only for as long as you need its history, then retire it.

### 5. After the cutover

Watch the instance's metrics and alerts from [observability](../deployment/observability.md), set
up the [backup and restore](../deployment/disaster-recovery.md) you intend to rely on, and check
the [versioning policy](../deployment/versioning-policy.md) so you know what an upgrade will and
will not change.

## Related

- [Why DeviceChain](../why-devicechain.md): what DeviceChain is for, and what it does not do yet.
- [Domain model](../concepts/domain-model.md): the entities and relationships this page maps to.
- [Importing devices from CSV](./importing-devices.md) and [Connecting a device](./connecting-a-device.md).
- [GraphQL API](../reference/graphql-api.md): the surface a migration script drives.
