---
sidebar_position: 5
title: Event Processing & Alarms
---

# Event Processing & Alarms

DeviceChain turns raw device telemetry into meaningful signals. As events flow through the pipeline, **alarm rules** evaluate them in real time and raise **alarms** — stateful conditions with a lifecycle, a severity, and a path to notify a human.

:::note Status
**Available today:** profile-defined alarm rules (threshold, held-for-duration, and repeating-occurrence conditions; static or attribute-driven thresholds), the four-state alarm lifecycle with severity tiers and in-place escalation, propagation across the relationship graph, live alarm subscriptions (the console **Alarms** view and the dashboard alarm widgets), and notification delivery over email and webhook with per-severity escalation.

**Planned:** a dedicated event-processing pipeline that broadens detection (rate-of-change, silence/absence, windowed aggregates, and area/group correlation) and adds automated actions beyond raising an alarm, together with visual rule authoring. These build on the same rule model described below.
:::

## Where rules live

Alarm rules are defined on a **[device profile](./domain-model.md)** — the versioned capability contract shared by one or more device types. Because the profile is versioned (draft → publish → rollback), a change to a fleet's alarm logic is authored as a draft, published atomically, and rolled back if needed, exactly like the profile's metric and command definitions. Every device that resolves to the profile picks up its rules automatically.

A rule targets a **metric** the profile defines, states a **condition**, and declares the **severity** of the alarm it raises.

## Condition types

| Condition | Fires when | Parameters |
|---|---|---|
| **Threshold** | a reading crosses a comparison (e.g. `temperature > 80`) | the comparison + a threshold value |
| **Duration** | the condition holds continuously for at least a set time (e.g. `pressure low for 5 minutes`) | a hold time |
| **Repeating** | the condition occurs a number of times within a window (e.g. `3 faults in 10 minutes`) | an occurrence count + a window |

### Static and dynamic thresholds

A threshold can be a **fixed value** on the rule, or **dynamic** — the name of a device **attribute** the rule reads at evaluation time. A dynamic threshold lets one rule adapt per device: the profile defines the rule once, and each device carries its own limit as a `SERVER`- or `SHARED`-scoped attribute (server-set values take precedence). Change the attribute and the effective threshold changes with no rule edit.

## The alarm lifecycle

A raised alarm is a **stateful object**, not a one-off message. Its state is a combination of two axes — a **four-state model**:

- **State** — `ACTIVE` while the condition holds, `CLEARED` once it resolves.
- **Acknowledged** — whether an operator has taken ownership of the alarm (with a record of who and when).

So an alarm moves through `ACTIVE/unacknowledged` → `ACTIVE/acknowledged` → `CLEARED`, and a flapping condition re-activates the *same* alarm rather than spawning duplicates.

### Severity and escalation

Each alarm carries a **severity** — `CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, or `INDETERMINATE`. A single condition can declare rules at several severity tiers (for example `temp > 80 → MAJOR`, `temp > 100 → CRITICAL`); the engine **escalates a single active alarm in place** to the highest tier currently met, and de-escalates as conditions ease — rather than opening a separate alarm per tier.

## Propagation across the relationship graph

Because DeviceChain models device context as a **[typed relationship graph](./domain-model.md)**, an alarm on a device is visible in the context of what that device is related to — its customer, area, or asset. Alarm counts roll up along the graph, so an operator looking at an area sees the alarms of the devices within it without each device having to report upward explicitly.

## Reaching a human

A raised alarm can notify people through the **notification** system. A per-tenant policy routes alarms to **email (SMTP)** and **webhook** channels, with per-severity **escalation** (notify a wider audience if an alarm is not acknowledged in time) and throttling/deduplication so a noisy condition does not flood a channel. This machine-to-human path is kept distinct from the machine-to-machine **[outbound connectors](./architecture.md)** that fan events out to other systems.

## Seeing alarms

Alarms surface live in two places without any extra wiring:

- The console **Alarms** view — a live, tenant-wide list, filterable and acknowledgeable in place.
- **Dashboard widgets** — a live **alarm table** and an **alarm count** widget (see [Dashboards](./dashboards.md)), including **acknowledge/clear actions** that the server authorizes against the operator's own rights.

Both are fed by live subscriptions, so state changes appear as they happen.
