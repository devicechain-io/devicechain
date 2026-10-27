---
title: LwM2M Ingestion
---

# LwM2M Ingestion

Constrained and cellular fleets often speak [**OMA LwM2M**](https://lwm2m.openmobilealliance.org/), a compact device-management standard over CoAP. DeviceChain terminates LwM2M directly. Devices connect to it over **CoAP/UDP secured with DTLS**, and their registration, telemetry and firmware map onto the same device model every other transport uses.

Sparkplug ingestion connects out to *your* broker. LwM2M works the other way: devices connect **in** to DeviceChain's secured CoAP endpoint, the same shape as the standard MQTT path.

:::note Status
LwM2M ingestion is available as an opt-in service over CoAP/UDP with DTLS-PSK. It drives authoritative [device presence](./device-presence.md), ingests observed sensor objects as measurements, and sends Read/Write/Execute commands downlink, with durable hold-and-drain for sleeping devices. Reboot and a device-information read are available [by name](#named-operations-and-device-information), and each device's manufacturer, model, serial number, firmware version and battery level are read when it registers and kept as attributes. Notify decoding is **SenML-JSON only**, so an LwM2M 1.0-only client gets presence and commands but no measurements until TLV decoding lands. GA scope is PSK credentials; X.509 / raw-public-key and a Bootstrap server are planned.
:::

## What it does

### Authentication at the handshake

A device presents a **DTLS pre-shared-key (PSK) identity**. DeviceChain resolves that identity to a tenant and a device before any application traffic flows. An unknown or malformed identity fails the handshake and never reaches registration. Tenancy comes from the *authenticated* identity, never from anything the device asserts in a payload.

With auto-registration enabled for a credential, the device row is created the first time a provisioned identity registers.

Roaming clients, whose network address changes, are followed via DTLS Connection ID, so a cellular device keeps its session across an IP change.

### Presence from the registration lifecycle

Presence is authoritative and follows the LwM2M registration lifecycle:

- A **Register** marks the device online.
- Periodic **Updates** keep the session alive.
- A **Deregister**, or a lapsed registration lifetime, marks it offline.

Like [Sparkplug-B](./sparkplug.md), this makes LwM2M a [**presence-asserting**](./device-presence.md) transport: a device's online state is authoritative, not inferred from a timeout.

### Sensor objects become measurements

DeviceChain **Observes** the device's *sensor* object instances and decodes each **Notify** into typed measurements on the normal envelope. LwM2M telemetry then lands in history, live state, dashboards and the detection engine exactly like any other reading.

- Sensor objects are those whose object id falls in the fixed IPSO Smart Object sensor range (3200–3441) that the build treats as telemetry.
- Observation is capped at 32 instances per registration.
- Neither the range nor the cap is a setting.
- Management objects (Security, Server, Device and the rest of the OMA set) are never observed.
- A Notify with more than 256 numeric values is stored as several events of at most 256 each, rather than cut short. The tenant's ingest ceiling, counted in readings, still applies: a Notify larger than what the ceiling can admit at once keeps the events it admits, and the rest are shed and counted in `ingest_samples_shed_total`.

Only **SenML-JSON** notifications are decoded today, and you should size that up front: a conformant LwM2M 1.0-only client cannot produce them. SenML arrived in LwM2M 1.1, so a 1.0 client correctly answers the Observe with `4.06 Not Acceptable`. Such a device still registers, drives presence and accepts commands, but reports **no telemetry at all**. This is the single largest functional gap in LwM2M support today; decoding the older TLV format is the follow-up that closes it. Both refusals are counted, so an operator can see it happening rather than infer it from missing data.

### Commands and firmware

Platform commands become LwM2M **Read / Write / Execute** operations on the device's resources.

A firmware update is driven the same way, as an operator sequence rather than a single platform operation. You issue the Writes and the Execute against the standard Firmware Update object yourself, as ordinary commands. DeviceChain keeps one device's commands in the order you enqueued them, so a firmware write and its execute can never reorder. It does not model the update as one managed job, and the platform's [over-the-air updates](./over-the-air-updates.md) do not reach LwM2M devices. The steps are in [Firmware update with Object 5](#firmware-update-object-5).

When a device is slow to answer and its commands queue up, further commands for it are set aside and delivered moments later, still in order. One slow device does not hold up commands to the others.

A command for a device that is currently asleep is **held durably and delivered on its next wake** (queue mode) rather than dropped. The hold has a bounded horizon, so a command never waits forever. While it waits, the command records one of two states, so a real backlog is a mixture of the two:

- [`PARKED`](./commands.md#parked-commands) rather than `SENT`.
- `HELD`, when presence has already marked the device absent and the command is withheld before it is ever published.

Either way the platform still holds the command, so you can still cancel it. If its time-to-live runs out first, the record says it never reached a device rather than blaming the device for not answering.

### Firmware update with Object 5 {#firmware-update-object-5}

This is the supported way to update the firmware of an LwM2M device today. It uses three commands that the device's profile declares like any other command: `lwm2m.write`, `lwm2m.execute` and `lwm2m.read`, each taking a `path` in its payload (and, for a write, a scalar `value`). Issue the steps **in order, and wait for each command to reach `SUCCESSFUL` before issuing the next**. DeviceChain keeps one device's commands in the order you enqueued them, but the device's own firmware state machine needs the ordering as well.

1. **Set the package URI.** Write the location of the image to the Firmware Update object's Package URI resource. The device starts downloading.

   ```json
   {"path": "/5/0/1", "value": "coaps://fw.example/image.bin"}
   ```

   Send that payload with `lwm2m.write`. The image is served from wherever you host it; DeviceChain does not store it or hand out a link. Writing the package itself (`/5/0/0`) is not supported, because a write carries one scalar value of at most 8 KiB.

2. **Trigger the update.** Once the download has completed, execute the Update resource.

   ```json
   {"path": "/5/0/2"}
   ```

   Send that payload with `lwm2m.execute`.

3. **Watch progress and outcome.** Read the State resource (`/5/0/3`; `0` is idle and `3` is updating) and the Update Result resource (`/5/0/5`; `0` is initial, `1` is success and `2` or more is an error) with `lwm2m.read`.

Reading is the only way to follow progress. DeviceChain observes only the sensor object range (3200 to 3441), so Object 5 is never observed and its state and result do not arrive as measurements. For a device in queue mode the whole sequence rides the same hold-and-deliver path as any command: each command is held, then delivered when the device wakes, oldest first.

**Managed updates do not reach LwM2M devices yet.** An artifact, a signature relay, an attempt that ends in `UPDATED` and a rollout all need things this transport does not have today:

- The platform's download links are longer than the Package URI resource accepts: in Object 5 version 1.0 the resource is a string of at most 255 bytes.
- Object 5 has no resource that carries a signature. The device's own firmware format does its verification, and the platform has nowhere to relay a signature to, so an assignment could look authenticated when it was not.
- LwM2M has no boot identifier, so the evidence an attempt needs, a new boot running the target version, has to be inferred from a new registration and the Device object.
- The platform serves downloads over HTTPS only, and a device may support CoAP only for firmware download.

Treat the runbook as the way to update LwM2M devices. The automated interoperability tests cover registration and telemetry, not commands, so try the sequence on your own device model before you depend on it.

### Named operations and device information {#named-operations-and-device-information}

Two standard Device-object operations are available by name, so nobody has to type `/3/0/4`:

| Command key | What it does |
| --- | --- |
| `lwm2m.reboot` | Executes the Device object's Reboot resource. Takes no parameters. |
| `lwm2m.readDeviceInfo` | Reads manufacturer, model number, serial number, firmware version and battery level, and answers with the values it read and a list of the resources it could not. |

They are ordinary commands. Declare them on the device profile like any other command (the profile's Commands tab has an **Add LwM2M standard commands** button that adds both), and they go through the same enqueue check, permissions, audit trail and hold-and-drain delivery as every other command. A resource the device does not have is reported as missing, with the reason, rather than failing the whole read.

`lwm2m.reboot` is created with impact `DISRUPTIVE`, so a device type with a [maintenance window](./commands.md#maintenance-windows) holds it until the window opens. A raw `lwm2m.execute` is not gated.

Factory Reset is deliberately not offered by name: it is destructive and cannot be undone. Factory Reset cannot be sent as a raw `lwm2m.execute` either: an Execute of `/3/0/5` (on any instance of the Device object) is refused with `COMMAND_DESTRUCTIVE_REFUSED`, whether sent as a single command, in a batch, or from an automation rule. A confirmed named operation is planned. Firmware update likewise stays the operator sequence described in [Firmware update with Object 5](#firmware-update-object-5).

The same Device-object information is also collected for you. When a device registers, DeviceChain reads those resources once and records them on the device as **client attributes** (`manufacturer`, `modelNumber`, `serialNumber`, `firmwareVersion`, `batteryLevel`), so they show on the device page. Resources the device did not report are listed in the `lwm2m.deviceInfoMissing` attribute. This never delays registration or telemetry: it runs after the registration is complete, in the same per-device order as commands, asks each resource once, retries a silent device only once, and reads a given device at most once an hour. Values that have not changed are not rewritten.

### The same pipeline

Decoded measurements and presence changes flow through the normal decode → resolve → persist path, so everything downstream treats LwM2M devices like any other.

## Tenancy and identity

Every device is bound to its tenant by its **authenticated DTLS PSK identity**, mapped to a `(tenant, externalId)` at connection time. Because the identity is checked during the handshake, a device can never present traffic for another tenant. The identity on the wire is an opaque handle rather than a readable `tenant:device` string.

## High availability

One replica serves the CoAP endpoint at a time, held by a fenced ownership lease. This is not a tuning choice. Devices connect **in** to one bound UDP socket, so a second replica sharing the Service would silently receive, and drop, a share of the datagrams. Only the lease holder binds the socket, and the deployment refuses to render more than one replica.

The lease makes **replacement safe and automatic**. A replacement pod binds nothing until it has acquired the lease, so two processes are never bound to the endpoint at once. Because a queue-mode device can be silent for long stretches by design, the new leader reconstructs presence from the durable projection rather than probing. A device that does not re-register is marked offline only after the server's maximum registration lifetime has passed. A changeover does not false-flag sleeping devices as offline.

Recovery is automatic but not instantaneous. It takes as long as the replacement pod needs to schedule and bind, plus up to the lease's 30-second fencing window. Datagrams sent during that window are lost, and CoAP confirmable messages are retransmitted, so the changeover itself passes largely unnoticed.

**Observations do not survive a changeover.** The DTLS sessions die with the old process, and the new leader starts with none, so an Observe is not re-issued until the device re-registers of its own accord. Presence is reconstructed; telemetry is not.

The blackout is bounded only by each device's own registration lifetime. For a device using LwM2M's default, that is 86400 seconds: a full day of silence from a device that is perfectly healthy.

To shorten the blackout, shorten the registration lifetime your devices request, at the cost of more frequent registration updates. Choose it by how long you are willing to be telemetry-dark after a restart.

The server's **maximum registration lifetime** (`maxLifetimeSeconds`) is the ceiling every registration lifetime is clamped down to. Lowering it does not shorten the blackout, because the device is never told about the clamp and keeps updating on its own schedule. Keep it above the longest lifetime your devices request. A device that requests a longer lifetime can lapse between its own updates, so it is marked offline in normal running as well as on every handover.

## Running it

The CoAP endpoint is served by a single owning replica. That gives LwM2M a few operational properties worth knowing before you depend on it in production: what a changeover costs, why observations do not come back on their own, and how to bound the telemetry gap. They are covered in **[Running the Edge Services](../deployment/edge-services.md)**.
