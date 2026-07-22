---
sidebar_position: 13
title: Device Presence
---

# Device Presence

DeviceChain keeps a live **presence** signal for every device — whether it is currently online, and when it last connected, disconnected, or reported activity. Presence is part of a device's [last-known state](./architecture.md) (the same projection that holds its most recent measurements and location), and it surfaces on the device's **Connectivity** tab in the console.

The important thing to understand is *how* DeviceChain decides a device is online, because it depends on the transport.

## Two ways presence is known

Every device carries a **presence source** that says how its online/offline state is determined:

- **Inferred** (the default) — DeviceChain has no explicit connect/disconnect signal from the transport, so it infers presence from **activity**. A device is considered online while it is sending data; if it goes quiet for longer than its **inactivity timeout**, a background sweep marks it offline. This is the right model for connectionless transports (plain HTTP, CoAP) and for simple MQTT clients that don't announce themselves.

- **Asserted** — the transport tells DeviceChain *explicitly* when a device connects and disconnects, so presence is **authoritative** rather than guessed. The first time such a signal arrives for a device, DeviceChain switches that device to the asserted source and, from then on:
  - its online/offline state is driven **only** by explicit connect/disconnect signals — a stray data packet can never mark a device online that the platform has been told is offline;
  - the inactivity sweep leaves it alone — an asserted device that goes quiet is *not* assumed dead, because if it had died the transport would have said so.

A device stays **inferred** until an asserting transport produces for it, so nothing changes for existing devices unless they start arriving over a transport that asserts presence. Today the asserting transport is [Sparkplug-B](./sparkplug.md), whose BIRTH and DEATH messages are exactly these explicit connect/disconnect signals.

## Why the distinction matters

Inferred presence is convenient but laggy and ambiguous: "offline" only means "hasn't spoken recently," which is slow to notice a real disconnect and blind for devices that report on a long interval. Asserted presence is immediate and unambiguous — a disconnect is a disconnect the instant the transport reports it — which is what you want for anything you'll alarm or act on.

Keeping the two modes as an explicit per-device flag means a device on a connectionless transport keeps its familiar timeout behavior, while a device on a presence-aware transport gets the authoritative signal, and the two never interfere.

:::note Status
Device presence — both inferred and asserted — is available, with [Sparkplug-B](./sparkplug.md) as the first presence-asserting transport. Authoring a detection rule directly on a connect/disconnect edge (raise on disconnect, resolve on reconnect) is planned; today an authoritative disconnect updates the device's live state and can be seen on the Connectivity tab.
:::
