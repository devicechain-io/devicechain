---
sidebar_position: 14
title: Sparkplug-B Ingestion
---

# Sparkplug-B Ingestion

Many industrial and building-automation fleets already publish telemetry as [**Eclipse Sparkplug B**](https://sparkplug.eclipse.org/) over an MQTT broker they operate. DeviceChain can ingest directly from those networks without asking the devices to change anything: it joins your Sparkplug environment as a **Host Application** and translates the edge traffic into the same events every other transport produces.

Unlike plain MQTT — where DeviceChain *is* the broker — Sparkplug ingestion works the other way around: DeviceChain **connects out to your broker** as a client, subscribes to the Sparkplug groups you configure, and follows the Sparkplug session protocol.

## What it does

- **Announces itself as a Host Application.** DeviceChain publishes the Sparkplug `STATE` message so edge nodes know a consumer is online, and follows the birth/death handshake so it always knows which nodes and devices are live.

- **Follows the Sparkplug session.** Sparkplug is a stateful protocol — edge nodes send a **BIRTH** certificate that defines their metrics (and compact aliases), then a stream of **DATA** messages that reference them, and a **DEATH** when they go offline. DeviceChain runs the full session machine: it tracks each node's aliases and message sequence, detects a gap or a missed birth, and asks the node to re-announce (a *rebirth*) when it needs to resynchronize — so a dropped message never silently corrupts what it decodes.

- **Maps edge identities to devices.** Each Sparkplug `{group}/{node}` (or `{group}/{node}/{device}` for a device under a node) becomes the [`externalId`](./domain-model.md) of a DeviceChain device. If you enable auto-registration for a source, a device is created automatically the first time it's seen; otherwise unknown identities are dropped and counted, so you stay in control of what enters your registry.

- **Produces authoritative presence.** A node or device BIRTH marks the corresponding device **online**, and a DEATH marks it **offline** — immediately and explicitly. This makes Sparkplug the first transport to drive [**asserted device presence**](./device-presence.md): a Sparkplug device's online state is authoritative, not inferred from a timeout.

- **Feeds the same pipeline.** Decoded measurements and presence changes flow into the normal decode → resolve → persist pipeline, so everything downstream — history, live state, dashboards, and the detection engine — treats Sparkplug telemetry exactly like any other.

## Tenancy and configuration

Each Sparkplug **source** is configured for one tenant: the broker URL, credentials (supplied as a projected secret, never in plaintext config), and the groups to subscribe to. Every message that arrives on a source is attributed to *that source's tenant* — the tenant is fixed by which broker the message came in on, never read from the Sparkplug topic — so one tenant's edge network can never be mistaken for another's.

## High availability

Only **one** replica of the Sparkplug service connects to a given broker at a time, elected through a lease. A second replica stands by and takes over if the leader fails; on takeover it re-establishes the session (asking nodes to re-announce) and reconciles device presence, so a disconnect that happened during the handover isn't missed and no device is left wrongly showing online.

:::note Status
Sparkplug-B ingestion is available as an opt-in service. It ingests measurements and drives authoritative [device presence](./device-presence.md); it connects to a broker over TLS or plaintext per the configured URL. A second standards-native edge protocol, [LwM2M](./lwm2m.md), is also available. Custom-CA / mutual-TLS to a private broker is planned.
:::
