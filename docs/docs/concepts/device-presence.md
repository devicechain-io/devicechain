---
title: Device Presence
---

# Device Presence

DeviceChain keeps a live **presence** signal for every device — whether it is currently online, and when it last connected, disconnected, or reported activity. Presence is part of a device's [last-known state](./architecture.md) (the same projection that holds its most recent measurements), and it surfaces on the device's **Connectivity** tab in the console.

The important thing to understand is *how* DeviceChain decides a device is online, because it depends on the transport.

## Two ways presence is known

Every device carries a **presence source** that says how its online/offline state is determined:

- **Inferred** (the default) — DeviceChain has no explicit connect/disconnect signal from the transport, so it infers presence from **activity**. A device is considered online while it is sending data; if it goes quiet for longer than its **inactivity timeout**, a background sweep marks it offline. This is the right model for connectionless transports (plain HTTP, CoAP).

- **Asserted** — the transport tells DeviceChain *explicitly* when a device connects and disconnects, so presence is **authoritative** rather than guessed. The first time such a signal arrives for a device, DeviceChain switches that device to the asserted source and, from then on:
  - its online/offline state is driven **only** by explicit connect/disconnect signals — a stray data packet can never mark a device online that the platform has been told is offline;
  - the inactivity sweep leaves it alone — an asserted device that goes quiet is *not* assumed dead, because silence is not evidence of death on a transport whose whole job is to report death explicitly. Mixing the two would let a long-interval reporter be marked offline while the platform has been told it is connected.

A device stays **inferred** until an asserting transport produces for it, so nothing changes for existing devices unless they start arriving over a transport that asserts presence. Three transports assert presence today:

- **Plain MQTT**, for devices connected to DeviceChain's own broker. No cooperation is needed from the device — it does not have to publish a birth message, set a last will, or announce itself in any way. The broker already knows the moment a connection opens and closes, and DeviceChain reads that directly, so an ordinary MQTT client gets authoritative presence simply by connecting.
- **[Sparkplug-B](./sparkplug.md)**, whose BIRTH and DEATH messages are exactly these explicit connect/disconnect signals.
- **[LwM2M](./lwm2m.md)**, whose registration lifecycle — register, periodic update, and deregister (or a lapsed lifetime) — does the same.

Two details of the MQTT case are worth knowing before you build on it:

- **It follows the device's main connection.** A device may open extra connections by appending its own suffix to its client id (the two-terminal `mosquitto_sub` / `mosquitto_pub` workflow). Those extra connections are deliberately ignored for presence: the device's state follows its primary session, so closing a side connection never makes a connected device read as offline. A device that *only* ever connects with a suffixed client id is not asserted at all and stays inferred.
- **It covers devices on this instance's broker.** A device reaching DeviceChain through a broker you run yourself is not something DeviceChain can observe connections on, so it stays inferred.

The consequence of that skip is deliberate, and worth understanding before you rely on it: **an asserted device has no inactivity backstop.** Its offline signal can only come from the transport, so if that signal never arrives — a Sparkplug death certificate lost along with the connection, or an LwM2M device whose registration lifetime has not yet lapsed (LwM2M's own default is 86400 seconds, a full day) — the device keeps reading online with nothing to correct it. What to watch for, and how to bound the window, is in **[Running the Edge Services](../deployment/edge-services.md)**.

For MQTT devices, DeviceChain closes that gap itself rather than leaving it to you. There is one case where the broker cannot tell you a device has gone: when the broker itself restarts, the connections it was holding simply vanish, and no disconnect is ever announced for them. So DeviceChain periodically compares the broker's live connection list against what it believes, and corrects the difference in both directions — devices it did not know were connected, and devices it thinks are connected that the broker is not holding. Devices that reconnect after a broker restart are corrected by their own reconnect; the rest are corrected by the next comparison.

That comparison deliberately declines to mark anything offline unless it can account for **every** node of the broker cluster. If one node is slow or unreachable, its devices are missing from the list and are indistinguishable from devices that have genuinely gone — and wrongly marking a live device offline is the more damaging error, because commands queued for it would be held. In that situation DeviceChain still marks newly-seen devices online and simply waits for the next pass to make the offline call.

The presence *source* itself is not surfaced anywhere yet. The console's Connectivity tab and the [MCP](./mcp.md) device tools both show the resulting online/offline state and the last connect, disconnect, and activity times — but not whether that state was asserted by the transport or inferred from a timeout. The distinction is live in the platform and drives the behavior described here; it is simply not something you can read off a screen today.

## Why the distinction matters

Inferred presence is convenient but laggy and ambiguous: "offline" only means "hasn't spoken recently," which is slow to notice a real disconnect and blind for devices that report on a long interval. Asserted presence is immediate and unambiguous — a disconnect is a disconnect the instant the transport reports it — which is what you want for anything you'll alarm or act on.

Keeping the two modes as an explicit per-device flag means a device on a connectionless transport keeps its familiar timeout behavior, while a device on a presence-aware transport gets the authoritative signal, and the two never interfere.

:::note Status
Device presence — both inferred and asserted — is available, with [Sparkplug-B](./sparkplug.md) and [LwM2M](./lwm2m.md) as presence-asserting transports. A **detection rule can fire directly on a connect/disconnect edge**: the [Connectivity condition](./event-processing.md#condition-types) raises an alarm the instant an authoritative disconnect arrives and resolves it on reconnect — no timeout to tune. The engine evaluates it today, but neither console authoring surface offers it yet — the form builder and the automation canvas both omit the condition type, so a connectivity rule is defined by sending the rule directly to the API. **Do not open one in the console's form editor**: it does not recognise the type, so it reads the rule back as a threshold rule and saving replaces the original definition, with no warning. (The canvas refuses it properly, naming the unsupported type.) It complements the timeout-based Absence rule (authoritative death vs. inferred silence), and the two are meant to be paired. An authoritative disconnect also updates the device's live state, so the Connectivity tab shows the device offline the moment the transport reports it.
:::

## Running it

Presence is only as good as the signal behind it, and the two asserting transports each run as a
single owning replica — which gives presence a few operational properties worth knowing before you
alarm on it: what a changeover costs, why an asserted device can be stuck online, and how to bound
that. Those are covered in **[Running the Edge Services](../deployment/edge-services.md)**.
