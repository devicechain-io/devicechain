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

- **Feeds the same pipeline.** Decoded measurements and presence changes flow into the normal decode → resolve → persist pipeline. Everything downstream — history, live state, dashboards and the detection engine — treats Sparkplug telemetry exactly like any other. A message with more than 256 metric values is stored as several consecutive events of at most 256 each, up to 16,384 metrics in one message; a message carrying more has none of its readings stored. Every value keeps its own timestamp, and a redelivered message produces the same events rather than duplicates.

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

## Commands {#commands}

DeviceChain can send commands **down** to a Sparkplug node or device, over the same Host Application session it already holds with your broker. This is **opt-in and off by default**: until a source enables it, a command to a Sparkplug device fails at once as undeliverable, exactly as before. Enabling it is a deployment setting, described in [Sparkplug commands](../deployment/edge-services.md#sparkplug-commands), and today it is done through Helm values.

Commands are ordinary [commands](./commands.md): declared on the device profile, sent from the device page, a batch or an automation rule, and subject to the same permissions, maintenance windows and audit trail. The console's **Add Sparkplug standard commands** button declares the three below on a profile.

| Command key | What it sends | Allowed when |
| --- | --- | --- |
| `sparkplug.write` with `{"metric": name, "value": v}` | A `DCMD` to a device, or an `NCMD` to a node, setting one metric | The metric is on the source's writable list, and the node declared it in its birth |
| `sparkplug.rebirth` | An `NCMD` asking the node to publish its birth again, with every current value | Always, once commands are enabled |
| `sparkplug.reboot` | An `NCMD` setting `Node Control/Reboot` | The command is declared `DISRUPTIVE` or `DESTRUCTIVE`, the source sets `allowReboot`, the node declared the metric in its birth, the node is online, and it has not been rebooted within the reboot window |

Whether a command goes to a node or a device follows the target: a device that sits under a node gets a `DCMD`, a node gets an `NCMD`. A metric the birth gave an alias is addressed by alias, as Sparkplug requires; otherwise by name. The value is encoded into the datatype the node declared, and a value of the wrong kind is refused before anything is sent, with the metric, the datatype and your value in the reason. Arrays, data sets and other structured datatypes are not supported. A command is published once, at QoS 0 and unretained.

If the device has not been born on the session yet (the node is offline, or the platform has just restarted and the births have not arrived), the command is **parked** and sent when the birth arrives. A reboot is the exception: it is sent only to a node that is online, and is refused otherwise.

### What the outcomes mean {#command-outcomes}

**Sparkplug has no command acknowledgement.** A node that receives a `DCMD` does not answer it. So the platform publishes the command and then waits for the node to **report** the requested value in a later data or birth message, and the outcome tells you which of these happened:

| Status | Meaning |
| --- | --- |
| `SENT` | Published; waiting for the node to report the value. Lasts up to the confirm window |
| `SUCCESSFUL`, reported | The node reported the metric at the requested value after the command was published. The result records the metric, the value, when it was reported and in which message |
| `SUCCESSFUL`, already at the value | The node had already reported that value, and reported nothing different within the window. The platform did not see the node act on this command |
| `TIMEOUT` | Nothing confirmed it within the window (2 minutes by default). **This is not a failure**: the node may have ignored or rejected the command, the broker may have dropped it, or the node obeyed and has not reported yet |
| `FAILED`, not delivered | Refused before anything was sent: commands not enabled for that source, an invalid value, a metric that is not allowed, an expired command, or the breaker below |

Two cautions follow from that. *Confirmed* means the metric **reached** the value, not that this request caused it: Sparkplug does not authenticate who published, so anyone who can publish to the node's topic on your broker can make a metric read any value, and the broker's publish ACLs are what stand behind the evidence. And a metric a node reports only on change may leave a command at `TIMEOUT` even though it worked. A report older than the command, and a report flagged historical, never count as confirmation.

A newer write to the same metric on the same device ends the older one that is still waiting, as `TIMEOUT` with the reason *superseded*. A rebirth is confirmed by the node's next birth, and a reboot by a birth with a new birth sequence number, within its own, longer window.

### Primary and secondary host {#primary-and-secondary-host}

Sparkplug recommends that only a node's **primary host** send it commands. A source declares which it is with `hostRole`, which has no default because it is a fact about your plant:

- **`primary`**: your nodes name this source's host id as their primary host. The *already at the value* outcome is available.
- **`secondary`**: another host is the primary. Commands are still sent, but a node, or the broker's ACLs, may ignore or block them, and the primary may write the value back. The *already at the value* outcome is turned off, because the other host may be the one holding it there, and the console marks a command confirmed by a secondary host with a warning.

### When the broker refuses the publish {#command-breaker}

A broker that refuses a publish may drop the whole session, which marks the source offline and makes every node rebirth, and it would do that again for every queued command. So each source has a **circuit breaker**: if the session is lost within 5 seconds of a command being published, the breaker opens, that command ends as not confirmed with a reason that says the broker likely refused it, and commands to that source fail at once as not delivered for 5 minutes, doubling to 1 hour on each re-open. Telemetry is not affected. After the pause one command is let through to test it, and the breaker closes when it is published without a disconnect. The usual cause is a credential that may not publish on the command topics; see [the broker ACL](../deployment/edge-services.md#sparkplug-commands-acl).

## Updating an edge node {#updating-an-edge-node}

Sparkplug B defines no firmware update, so DeviceChain's [over-the-air updates](./over-the-air-updates.md) do not reach a device through this service, and DeviceChain does not invent a firmware convention on top of Sparkplug. An edge node that should be updated through DeviceChain also connects to DeviceChain as an ordinary MQTT device, with its own [credential](../guides/device-credentials.md), and uses the normal update channel. [Building an Updatable Device](../guides/building-an-updatable-device.md) shows that device side.

## Operations {#running-it}

Because a single replica owns the broker connection, Sparkplug ingestion has a few operational properties to understand before you depend on it in production: what a changeover costs, why a device can be left showing online, and what the drop counters are telling you. [Running the Edge Services](../deployment/edge-services.md) covers them.
