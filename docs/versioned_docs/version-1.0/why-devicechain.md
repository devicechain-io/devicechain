---
title: Why DeviceChain
---

# Why DeviceChain

DeviceChain is an Apache-2.0 IoT platform that you run on Kubernetes. Devices connect over MQTT,
HTTP, [Sparkplug B](./concepts/sparkplug.md) or [LwM2M](./concepts/lwm2m.md), or through a LoRaWAN
network server such as ChirpStack. Rules raise alarms and send commands back. Dashboards,
[over-the-air updates](./concepts/over-the-air-updates.md) and
[multi-tenant customer access](./user-guide/members-roles-and-customer-users.md#customer-users) are
built in. There is no device cap and no enterprise edition.

This page helps you decide whether it fits. It compares DeviceChain with kinds of solution, not with
named products, says where each kind is the better choice, and ends with what DeviceChain does not do
yet.

## Who it is for {#who-it-is-for}

**You might be a platform engineer evaluating an open-source IoT platform.** You want to run it
yourself, in a cluster you control, with your data staying there. You would like the license to stay
the same whether you have 100 devices or 100,000, and you want evidence about how it behaves, not
only a feature list. Start with [Architecture at a glance](./concepts/architecture-at-a-glance.md)
and the [measured throughput](./deployment/bootstrap.md#measured-throughput).

**You might already run a LoRaWAN network server or a Sparkplug B plant.** Your network server decodes
radio frames, or your edge nodes publish to an MQTT broker, and you need the layer above them: a
device registry, rules, alarms, commands, dashboards and customer access. DeviceChain
[takes data from a network server](./guides/inbound-integrations.md) without the device ever
connecting to it, and joins an existing Sparkplug environment as a
[Host Application](./concepts/sparkplug.md).

**You might build a connected product and need to give each of your customers a view of their own
equipment.** You are an OEM or an integrator and you want one platform for many customers, with
logins that see only what belongs to them, a [white-labelled](./concepts/white-labeling.md) console,
and updates you can roll out to the devices you ship.

## What you get, compared by kind of solution {#comparison}

The columns are kinds of solution, and every cell is a generalisation: a given product in a category
may do better or worse. Read it as a way to ask the right questions of the options you are actually
considering.

| | Build it yourself from parts (broker, time-series database, rules engine, dashboards) | Hosted IoT clouds | Other self-hosted platforms whose open-source edition is limited by a device cap or an enterprise tier | DeviceChain |
| --- | --- | --- | --- | --- |
| **Licensing** | Each part has its own license, and you carry the integration. | A service, usually metered by devices or messages. | Open-source core, with some features or scale reserved for a paid edition. | Apache 2.0, with no device cap and no paid edition. High availability, multi-tenancy and command delivery are in the open platform. |
| **Where it runs** | Wherever you put it. | The vendor's cloud. | Your infrastructure, or the vendor's. | A Kubernetes cluster you control, so your data stays where you put the cluster. See [Bootstrap](./deployment/bootstrap.md). |
| **Operating it** | You operate and upgrade every part. | The vendor does. | You do. | You do. A CLI, `dcctl`, installs and upgrades the platform; see [Releases and Upgrades](./deployment/releases-and-upgrades.md). |
| **Multi-tenancy and customer access** | You design it, usually late and with difficulty. | Usually account-level separation; per-customer views are yours to build. | Often present, sometimes in the paid edition. | One instance serves many [tenants](./concepts/multi-tenancy.md), and a tenant can give its own customers [scoped logins](./user-guide/members-roles-and-customer-users.md#customer-users). |
| **Closed-loop commands** | You build the queue, retries and result tracking. | Usually a command or job feature, with differing levels of result tracking. | Varies. | A [command](./concepts/commands.md#command-lifecycle) is persisted and tracked to an [outcome](./concepts/commands.md#outcome). A device that is away has its commands held, and a rule can send one. |
| **Rules and alarms** | You pick and wire a rules engine. | Usually a rules service, often stateless per message. | Varies. | [Streaming detection rules](./concepts/event-processing.md) that give the same result when events are replayed, and [alarms](./concepts/event-processing.md#the-alarm-lifecycle) with a lifecycle and a path to a person. |
| **Over-the-air updates** | You build the artifact store, signing and tracking. | Often available as a managed service. | Varies, and sometimes an enterprise feature. | [Signed artifacts, tracked attempts and group rollouts](./concepts/over-the-air-updates.md), for devices on MQTT. See the limits [below](#not-yet). |
| **Evidence about operations** | Whatever you measure yourself. | Service-level terms, but you cannot inspect the platform. | Varies. | A [measured throughput](./deployment/bootstrap.md#measured-throughput) table that states its workload and caveats, [signed releases](./deployment/verifying-releases.md) you can verify, and [disaster recovery](./deployment/disaster-recovery.md) with a restore procedure. The repository's [`hack/`](https://github.com/devicechain-io/devicechain/tree/main/hack) directory holds the high-availability and recovery validation rigs, which are run by hand against a real cluster. |

## When something else is the better choice {#when-something-else-fits}

Each kind of solution is the right answer in some situations.

- **Build it yourself** when you need one or two pieces, not a platform. If a broker and a
  time-series database already meet your need, or your requirements are unusual enough that a
  platform's model would fight them, parts give you control that a platform will not. The cost is
  that device registry, tenancy, command tracking and updates become your product.
- **A hosted IoT cloud** when you do not want to run Kubernetes, when your devices and your other
  workloads already live in one cloud and you want them to integrate there, or when you need managed
  scale in many regions today. DeviceChain gives you control and a license without per-device
  metering; it also gives you the operating work. [Tuning an Instance](./deployment/tuning-an-instance.md)
  shows what that work involves.
- **Another self-hosted platform** when you need something DeviceChain does not have yet, such as an
  industrial protocol driver (see below), when a longer history matters to you, or when you want a
  vendor's commercial support. DeviceChain is a young project, and the community and the set of
  integrations around it are smaller.

## What we do not do yet {#not-yet}

This list is here so that you find these things out before you adopt DeviceChain, not after.

- **Industrial fieldbus drivers.** There are no BACnet, Modbus or OPC-UA drivers, and nothing the
  project ships speaks them. The supported shape is a local gateway that speaks the fieldbus and
  forwards over MQTT or HTTP; you supply the protocol translation. See
  [Not available](./reference/transport-matrix.md#not-available).
- **SNMP.** DeviceChain does not collect from SNMP agents or receive SNMP traps. Use a collector that
  forwards over MQTT or HTTP.
- **Other transports.** WebSocket device ingest is not available, and CoAP reaches the platform only
  through LwM2M. Only the platform's MQTT broker authenticates a device by client certificate; HTTP
  uses a token or basic credential and LwM2M uses pre-shared keys. See the
  [transport capability matrix](./reference/transport-matrix.md#not-available).
- **Commands to devices behind a network server.** There is no downlink through an
  [inbound integration](./reference/transport-matrix.md#inbound-integrations). A command for such a
  device is accepted and then fails with `COMMAND_TRANSPORT_UNSUPPORTED`.
- **A mobile app.** DeviceChain has a web console and dashboards that adapt to a phone screen, and
  client libraries for [JavaScript](./reference/npm-packages.md) and, in the repository's
  [`sdks/`](https://github.com/devicechain-io/devicechain/tree/main/sdks) directory, .NET and Unity.
  It has no native mobile application.
- **Over-the-air update limits.** Rollouts have no waves, canary promotion, schedules or maintenance
  windows. Only a device connected over MQTT can receive a managed update: a device that speaks only
  HTTP cannot, LwM2M devices are updated with ordinary commands, and Sparkplug defines no firmware
  update. See [Which devices can be updated](./concepts/over-the-air-updates.md#transports).
- **Platforms other than Kubernetes.** The platform runs on Kubernetes. A local instance runs in a
  [kind](./quickstart/first-device.md) cluster, and a real one in your own cluster.
- **An external key manager.** Secrets are sealed under an instance root key. The store is built so a
  key manager can take over, but none ships today. See
  [Secret handling](./concepts/architecture.md#secret-handling).
- **Compatibility promises before v1.0.0.** Semantic versioning, with a promise on the device wire,
  the GraphQL API and the npm packages, starts at v1.0.0. Releases before it may change APIs and
  schemas. See the [versioning policy](./deployment/versioning-policy.md).

## Where to go next {#next}

- [Quickstart: your first device](./quickstart/first-device.md) runs an instance on your machine.
- [Architecture at a glance](./concepts/architecture-at-a-glance.md) shows how a message moves
  through the platform, and [Architecture](./concepts/architecture.md) lists every component.
- [Getting help](./getting-help.md) says where to ask a question or report a gap on this page.
