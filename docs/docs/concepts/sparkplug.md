---
title: Sparkplug-B Ingestion
---

# Sparkplug-B Ingestion

Many industrial and building-automation fleets already publish telemetry as [Eclipse Sparkplug B](https://sparkplug.eclipse.org/) over an MQTT broker they operate. DeviceChain can ingest directly from those networks without asking the devices to change anything. It joins your Sparkplug environment as a **Host Application** and translates the edge traffic into the same events every other transport produces.

With plain MQTT, DeviceChain *is* the broker. Sparkplug ingestion works the other way around: DeviceChain **connects out to your broker** as a client, subscribes to the Sparkplug groups you configure, and follows the Sparkplug session protocol.

## What it does

- **Announces itself as a Host Application.** DeviceChain publishes the Sparkplug `STATE` message so edge nodes know a consumer is online. It follows the birth/death handshake, so it always knows which nodes and devices are live.

- **Follows the Sparkplug session.** Sparkplug is a stateful protocol. An edge node sends a **BIRTH** certificate that defines its metrics (and compact aliases), then a stream of **DATA** messages that reference them, and a **DEATH** when it goes offline. DeviceChain runs the full session state machine: it tracks each node's aliases and message sequence, and detects a gap or a missed birth. When it needs to resynchronize, it asks the node to re-announce (a *rebirth*), so a dropped message never silently corrupts what it decodes.

- **Maps edge identities to devices.** Each Sparkplug `{group}/{node}` (or `{group}/{node}/{device}` for a device under a node) becomes the [`externalId`](./domain-model.md) of a DeviceChain device. If you enable auto-registration for a source, a device is created automatically the first time it is seen. Otherwise, unknown identities are dropped and counted, so you stay in control of what enters your registry. Traffic for a tenant that has been deleted is also dropped while its data is being reclaimed, and is counted separately. An operator watching drops can therefore tell "not in the registry" from "the tenant is gone."

- **Produces authoritative presence.** A node or device BIRTH marks the corresponding device online, and a DEATH marks it offline, immediately and explicitly. This makes Sparkplug a [presence-asserting](./device-presence.md) transport, as is [LwM2M](./lwm2m.md): a Sparkplug device's online state is authoritative, not inferred from a timeout. See [Death handling](#death-handling) for which deaths DeviceChain applies.

- **Feeds the same pipeline.** Decoded measurements and presence changes flow into the normal decode → resolve → persist pipeline. Everything downstream — history, live state, dashboards and the detection engine — treats Sparkplug telemetry exactly like any other.

### Death handling {#death-handling}

DeviceChain is deliberately strict about which deaths it applies:

- **Birth sequence must match.** When a node's BIRTH declares a birth sequence number (as the Sparkplug specification requires), a DEATH is accepted only when it carries a matching one. A stale or duplicate will left over from an earlier connection is ignored rather than tearing down a session a newer BIRTH has already re-established.
- **No birth sequence, no correlation.** A node whose BIRTH declared no birth sequence number cannot be correlated at all, so its death is accepted as-is.
- **Unborn devices.** A device DEATH for a device that was never born emits nothing at all, because there is no presence to end.
- **Node death cascades.** A *node* death marks the node offline and every known device beneath it, because in Sparkplug a node's death implies its devices are gone with it.

## Tenancy and configuration

Each Sparkplug **source** is configured for one tenant. It holds the broker URL, the credentials (supplied as a projected secret, never in plaintext config) and the groups to subscribe to.

Every message that arrives on a source is attributed to *that source's tenant*. The tenant is fixed by which broker the message came in on and is never read from the Sparkplug topic, so one tenant's edge network can never be mistaken for another's.

## High availability

Only **one** replica of the Sparkplug service connects to a given broker at a time. A Sparkplug Host owns per-node alias, sequence and session state, so two connected instances would each see part of the traffic and publish conflicting `STATE`. Single ownership is therefore a correctness requirement, not a tuning choice. It is enforced by a fenced ownership lease, and the deployment refuses to render more than one replica.

The lease makes replacement safe and automatic. When the serving pod goes away — a crash, an eviction, a node loss or a rollout — its replacement cannot begin serving until it has acquired the lease, so there is never a window with two Hosts on the broker. On acquiring the lease, the new leader:

1. Re-establishes the session, asking nodes to re-announce.
2. Reconciles device presence, so a disconnect that happened during the changeover is not missed and no device is left wrongly showing online.

Recovery is automatic but not instantaneous. It takes as long as the replacement pod needs to schedule and start, plus up to the lease's 30-second fencing window. There is no warm standby holding a second connection, because that would be the thing single ownership exists to prevent.

:::note Status
Sparkplug-B ingestion is available as an opt-in service. It ingests measurements and drives authoritative [device presence](./device-presence.md). It connects to a broker over TLS or plaintext, per the configured URL. A second standards-native edge protocol, [LwM2M](./lwm2m.md), is also available. Custom-CA / mutual-TLS to a private broker is planned.
:::

## Operations {#running-it}

Because a single replica owns the broker connection, Sparkplug ingestion has a few operational properties to understand before you depend on it in production: what a changeover costs, why a device can be left showing online, and what the drop counters are telling you. [Running the Edge Services](../deployment/edge-services.md) covers them.
