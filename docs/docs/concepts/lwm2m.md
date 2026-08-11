---
title: LwM2M Ingestion
---

# LwM2M Ingestion

Constrained and cellular fleets often speak [**OMA LwM2M**](https://lwm2m.openmobilealliance.org/) — a compact device-management standard over CoAP. DeviceChain terminates LwM2M directly: devices connect to it over **CoAP/UDP secured with DTLS**, and their registration, telemetry, and firmware all map onto the same one device model every other transport uses.

Unlike Sparkplug ingestion — where DeviceChain connects out to *your* broker — LwM2M devices connect **in** to DeviceChain's secured CoAP endpoint, the same shape as the golden MQTT path.

## What it does

- **Authenticates every device at the handshake.** A device presents a **DTLS pre-shared-key (PSK) identity**; DeviceChain resolves that identity to a tenant and a device before any application traffic flows. An unknown or malformed identity fails the handshake and never reaches registration — tenancy comes from the *authenticated* identity, never from anything the device asserts in a payload. With auto-registration enabled for a credential, the device row is created the first time a provisioned identity connects. Roaming clients (a device whose network address changes) are followed via DTLS Connection ID, so a cellular device keeps its session across an IP change.

- **Drives authoritative presence from the registration lifecycle.** An LwM2M **Register** marks the device **online**, periodic **Updates** keep the session alive, and a **Deregister** — or a lapsed registration lifetime — marks it **offline**. Like [Sparkplug-B](./sparkplug.md), this makes LwM2M a [**presence-asserting**](./device-presence.md) transport: a device's online state is authoritative, not inferred from a timeout.

- **Turns observed sensor objects into measurements.** DeviceChain **Observes** the device's *sensor* object instances — those whose object id falls in the IPSO Smart Object range configured as telemetry, capped at 32 instances per registration — and decodes each **Notify** into typed measurements on the normal envelope, so LwM2M telemetry lands in history, live state, dashboards, and the detection engine exactly like any other reading. Management objects (Security, Server, Device, and the rest of the OMA set) are never observed.

  Only **SenML-JSON** notifications are decoded today, and that has a consequence you should size up front: a conformant **LwM2M 1.0-only** client cannot produce them — SenML arrived in LwM2M 1.1, so a 1.0 client correctly answers the Observe with `4.06 Not Acceptable`. Such a device still registers, drives presence, and accepts commands, but reports **no telemetry at all**. This is the single largest functional gap in LwM2M support today; decoding the older TLV format is the follow-up that closes it. Both refusals are counted, so an operator can see it happening rather than infer it from missing data.

- **Sends commands and firmware down.** Platform commands become LwM2M **Read / Write / Execute** operations on the device's resources. A firmware update is driven the same way, as an operator sequence rather than a single platform operation: you issue the Writes and the Execute against the standard Firmware Update object yourself, as ordinary commands. DeviceChain keeps one device's commands in the order you enqueued them — a firmware write and its execute can never reorder — but it does not model the update as one managed job. A command for a device that is currently asleep is **held durably and delivered on its next wake** (queue mode), rather than dropped — with a bounded horizon so a command never waits forever.

- **Feeds the same pipeline.** Decoded measurements and presence changes flow through the normal decode → resolve → persist path, so everything downstream treats LwM2M devices like any other.

## Tenancy and identity

Every device is bound to its tenant by its **authenticated DTLS PSK identity**, mapped to a `(tenant, externalId)` at connection time. Because the identity is checked during the handshake, a device can never present traffic for another tenant, and the identity on the wire is an opaque handle rather than a readable `tenant:device` string.

## High availability

A single replica serves the CoAP endpoint at a time, held by a fenced ownership lease. Devices connect **in** to one bound UDP socket, so this is not a tuning choice: a second replica sharing the Service would silently receive — and drop — a share of the datagrams. Only the lease holder binds the socket; the deployment refuses to render more than one replica.

What the lease buys is that **replacement is safe and automatic**. A replacement pod binds nothing until it has acquired the lease, so two processes are never bound to the endpoint at once. Because a queue-mode device can be silent for long stretches by design, the new leader then reconstructs presence from the durable projection and each device's registration lifetime rather than probing — so a changeover doesn't false-flag sleeping devices as offline.

Recovery is automatic but not instantaneous: it takes as long as the replacement pod needs to schedule and bind, plus up to the lease's 30-second fencing window. Datagrams sent during that window are lost, and CoAP confirmable messages are retransmitted, so the changeover itself passes largely unnoticed.

**Observations do not survive it, though.** The DTLS sessions die with the old process, and the new leader starts with none — so an Observe is not re-issued until the device re-registers of its own accord. Presence is reconstructed; telemetry is not. The blackout is bounded only by each device's own registration lifetime, which for a device using LwM2M's default is 86400 seconds — a full day of silence from a device that is perfectly healthy. The lever is the server's **maximum registration lifetime**: clamping it down caps how long that blackout can last, at the cost of more frequent registration updates. Set it against how long you are willing to be telemetry-dark after a restart, not against the device's preference.

:::note Status
LwM2M ingestion is available as an opt-in service over CoAP/UDP with DTLS-PSK. It drives authoritative [device presence](./device-presence.md), ingests observed sensor objects as measurements, and sends Read/Write/Execute commands downlink (with durable hold-and-drain for sleeping devices). Notify decoding is **SenML-JSON only**, so an LwM2M 1.0-only client gets presence and commands but no measurements until TLV decoding lands. GA scope is PSK credentials (X.509 / raw-public-key and a Bootstrap server are planned).
:::

## Running it

The CoAP endpoint is served by a single owning replica, which gives LwM2M a few operational
properties worth knowing before you depend on it in production — what a changeover costs, why
observations do not come back on their own, and how to bound the telemetry gap. Those are covered
in **[Running the Edge Services](../deployment/edge-services.md)**.
