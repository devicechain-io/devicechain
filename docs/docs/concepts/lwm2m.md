---
sidebar_position: 15
title: LwM2M Ingestion
---

# LwM2M Ingestion

Constrained and cellular fleets often speak [**OMA LwM2M**](https://lwm2m.openmobilealliance.org/) — a compact device-management standard over CoAP. DeviceChain terminates LwM2M directly: devices connect to it over **CoAP/UDP secured with DTLS**, and their registration, telemetry, and firmware all map onto the same one device model every other transport uses.

Unlike Sparkplug ingestion — where DeviceChain connects out to *your* broker — LwM2M devices connect **in** to DeviceChain's secured CoAP endpoint, the same shape as the golden MQTT path.

## What it does

- **Authenticates every device at the handshake.** A device presents a **DTLS pre-shared-key (PSK) identity**; DeviceChain resolves that identity to a tenant and a device before any application traffic flows. An unknown or malformed identity fails the handshake and never reaches registration — tenancy comes from the *authenticated* identity, never from anything the device asserts in a payload. With auto-registration enabled for a credential, the device row is created the first time a provisioned identity connects. Roaming clients (a device whose network address changes) are followed via DTLS Connection ID, so a cellular device keeps its session across an IP change.

- **Drives authoritative presence from the registration lifecycle.** An LwM2M **Register** marks the device **online**, periodic **Updates** keep the session alive, and a **Deregister** — or a lapsed registration lifetime — marks it **offline**. Like [Sparkplug-B](./sparkplug.md), this makes LwM2M a [**presence-asserting**](./device-presence.md) transport: a device's online state is authoritative, not inferred from a timeout.

- **Turns observed resources into measurements.** DeviceChain **Observes** the device's resources and decodes each **Notify** (SenML) into typed measurements on the normal envelope, so LwM2M telemetry lands in history, live state, dashboards, and the detection engine exactly like any other reading.

- **Sends commands and firmware down.** Platform commands become LwM2M **Read / Write / Execute** operations on the device's resources, and a firmware update is decomposed onto those same primitives against the standard Firmware Update object. A command for a device that is currently asleep is **held durably and delivered on its next wake** (queue mode), rather than dropped — with a bounded horizon so a command never waits forever.

- **Feeds the same pipeline.** Decoded measurements and presence changes flow through the normal decode → resolve → persist path, so everything downstream treats LwM2M devices like any other.

## Tenancy and identity

Every device is bound to its tenant by its **authenticated DTLS PSK identity**, mapped to a `(tenant, externalId)` at connection time. Because the identity is checked during the handshake, a device can never present traffic for another tenant, and the identity on the wire is an opaque handle rather than a readable `tenant:device` string.

## High availability

A single replica serves the CoAP endpoint at a time, elected through a lease; a standby takes over on failure. Because a queue-mode device can be silent for long stretches by design, the new leader reconstructs presence from the durable projection and each device's registration lifetime rather than probing — so a takeover doesn't false-flag sleeping devices as offline.

:::note Status
LwM2M ingestion is available as an opt-in service over CoAP/UDP with DTLS-PSK. It drives authoritative [device presence](./device-presence.md), ingests observed resources as measurements, and sends Read/Write/Execute commands and firmware updates downlink (with durable hold-and-drain for sleeping devices). GA scope is PSK credentials (X.509 / raw-public-key and a Bootstrap server are planned).
:::
