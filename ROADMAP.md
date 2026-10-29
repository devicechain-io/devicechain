# DeviceChain roadmap

This page lists what the project intends to work on. It reflects current intent and can change as we
learn from people running the platform. It is not a schedule: no item here has a date, and an item
can move between sections, shrink, or be dropped.

It lists only work that has not shipped. What already exists is described in the
[documentation](https://docs.devicechain.io), and each release's notes say what changed in it. Gaps
that the docs already state plainly, such as on
[Why DeviceChain](https://docs.devicechain.io/why-devicechain#not-yet), are repeated here only when
work is planned against them.

**Propose or argue for an item** in [Discussions, under Ideas](https://github.com/devicechain-io/devicechain/discussions/categories/ideas).
Say what you are trying to do and what stops you, rather than only naming a feature. A concrete use
case moves an item up faster than a vote. If you want to build one yourself, start with
[CONTRIBUTING.md](CONTRIBUTING.md).

## Now (toward 1.0)

Version 1.0 is about trust more than breadth: a stable interface, an upgrade path that does not
require recreating an instance, and evidence for the claims the documentation makes.

- **The 1.0 compatibility promise.** From 1.0, the device wire contract, the GraphQL API, the
  command-line tool, the Helm values and the npm packages follow semantic versioning, and the
  documentation gets a frozen 1.0 snapshot in all three languages. The rules are in the
  [versioning policy](https://docs.devicechain.io/deployment/versioning-policy).
- **A last upgrade drill from the final 0.x release.** Upgrade a populated instance from the last
  pre-1.0 release to the release candidate and check that nothing is lost, before the tag is cut.
  See [releases and upgrades](https://docs.devicechain.io/deployment/releases-and-upgrades).
- **Acknowledged MQTT telemetry is durable by default.** The setting that makes a QoS 1
  acknowledgement mean "the platform holds this message" exists and is measured, but it is off by
  default. The work is to turn it on by default once the remaining measurements are in. See
  [quality of service](https://docs.devicechain.io/guides/connecting-a-device#quality-of-service).
- **Tenant deletion: finishing the evidence.** Deleting a tenant erases its data, and that is
  tested. The count of work blocked per storage system, the completion signal (`erasedAt`, and
  `dcctl tenant-deletion status`) and a first measurement of erasing a large tenant on one node are
  published. Still to do: the same measurement on a replicated multi-node cluster, which is the
  figure a data-processing agreement should quote. See
  [tenant deletion](https://docs.devicechain.io/deployment/tenant-deletion).

## Next

Work we expect to start after 1.0, roughly in the order we expect to reach it. The order follows
what people ask for, so tell us if it should change.

**Connecting and decoding**

- **Payload decoders you write yourself.** Inbound integrations store what a network server decodes
  and can map a generic JSON body. The next step is a decoder you author per device profile, in the
  same expression language the detection engine uses. See
  [inbound integrations](https://docs.devicechain.io/guides/inbound-integrations).
- **Self-registration from a certificate authority.** A device presenting a certificate issued by a
  CA you trust would be created on first connect against a profile, without a pre-shared secret. See
  [X.509 devices](https://docs.devicechain.io/guides/device-x509) and
  [self-registration](https://docs.devicechain.io/guides/device-self-registration).
- **Certificate sign-in for the edge agent.** The agent's uplink authenticates with an `MQTT_BASIC`
  credential today and has no client-certificate setting. See
  [edge services](https://docs.devicechain.io/deployment/edge-services).
- **Text telemetry kept as history.** Numeric and boolean readings are stored as time series;
  a text-valued reading is not. We intend to store them so they can be queried and charted.
- **Duplicate detection keyed per device.** The `altId` key is matched per tenant today; scoping it
  to the device is planned. See the
  [device protocol](https://docs.devicechain.io/reference/device-protocol).

**Commands and updates**

- **Rollouts with waves, canaries and schedules.** Update rollouts to a device group run a capped
  number of devices at a time, with pause, resume, abort and a failure threshold. Waves, canary
  promotion and scheduled starts are not there yet. See
  [over-the-air updates](https://docs.devicechain.io/concepts/over-the-air-updates).
- **Managed updates beyond MQTT devices.** Only a device connected over MQTT can receive a managed
  update today.
- **Campaign controls for command batches.** A command to many devices is already tracked per
  device. Scheduled start, pacing, staged cohorts, a failure-rate halt, and retrying only the
  unsuccessful targets are planned. See
  [commanding a fleet](https://docs.devicechain.io/guides/commanding-a-fleet).
- **Scheduled jobs.** Cron-style and interval schedules for platform actions.

**Rules and alarms**

- **Sequence detection.** Rules that match "A, then B within a time" over a device's events. See
  [event processing](https://docs.devicechain.io/concepts/event-processing).
- **Alarm operations views.** Queries over alarm history for noisy sources, flapping, time to
  acknowledge and clear, and the aging backlog.
- **Delivery history for notifications.** An answer, from the platform, to "was this alarm paged, to
  whom, and why did it fail". See
  [notification policies](https://docs.devicechain.io/user-guide/notification-policies).
- **Device-reported alerts that raise alarms** without a rule in between.

**Dashboards and the console**

- **Drill-down from a widget to the device behind it,** and layouts authored per screen size. See
  [dashboards](https://docs.devicechain.io/concepts/dashboards).
- **Public dashboards through scoped share links,** for showing a board to someone without an
  account.
- **A tabular event history** with time-range and relationship filters.
- **Widget text in your language.** The built-in widgets still carry English strings, while the rest
  of the console follows the tenant's language.

**Accounts and operations**

- **SAML sign-in.** Single sign-on speaks OpenID Connect today. Some of its settings, such as
  just-in-time account creation and group-to-role mapping, are API-only and are to get console
  screens. See [single sign-on](https://docs.devicechain.io/concepts/single-sign-on).
- **More per-tenant limits:** retention windows per tenant, and API and subscription rate ceilings.
  See [governance](https://docs.devicechain.io/concepts/governance).
- **Distributed tracing** across services, over OTLP. See
  [observability](https://docs.devicechain.io/deployment/observability).
- **Tenant data export,** so a tenant's entities and events can leave as a portable archive.
- **Write tools for AI assistants over MCP,** each behind an elevated scope and a human
  confirmation. The server is read-only today. See [MCP](https://docs.devicechain.io/concepts/mcp).
- **More console languages.** German, Brazilian Portuguese and Japanese are the candidates.
- **A Unity package that installs without staging assemblies by hand.**

## Later

Ideas we think are worth doing and have not scheduled, or that wait on demand or measurement. Treat
these as open questions, not promises.

- **A curated device catalogue.** A library of ready-made device profiles that a tenant adopts as its
  own copy and can later update.
- **Three-dimensional regions and location heatmaps,** extending
  [geofencing](https://docs.devicechain.io/concepts/geofencing).
- **Geospatial search** over stored locations, if a use case needs more than rule-time geofencing.
- **SCADA-style symbol widgets** on the dashboard canvas.
- **Cloud and edge as one:** the same rules and entities running at the edge and in the cloud, with
  synchronization between them. See [edge services](https://docs.devicechain.io/deployment/edge-services).
- **Plain CoAP and WebSocket device transports.** CoAP reaches the platform through LwM2M only, and
  WebSocket ingest is not available. See the
  [transport matrix](https://docs.devicechain.io/reference/transport-matrix).
- **Sharding detection by tenant,** for instances with very large numbers of tenants.
- **A control plane over many clusters,** for a provider operating DeviceChain for several customers.
- **A support "sign in as" action** for operators debugging a permission problem, with an audit
  record.
- **A second, hourly rollup tier** for long-range charts, and a **store-and-forward spool** for
  adapters.
- **An external key manager** for the instance root key. See
  [secret handling](https://docs.devicechain.io/concepts/architecture#secret-handling).

## What is not on this page

Bug fixes, security work and internal hardening are continuous and are not listed. Security reports
go to the address in [SECURITY.md](SECURITY.md). Protocol drivers for industrial fieldbuses (BACnet,
Modbus, OPC-UA), SNMP collection and a native mobile application are described as gaps on
[Why DeviceChain](https://docs.devicechain.io/why-devicechain#not-yet) and are not planned work; if
one blocks you, say so under Ideas.
