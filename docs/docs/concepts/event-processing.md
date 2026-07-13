---
sidebar_position: 5
title: Event Processing & Alarms
---

# Event Processing & Alarms

DeviceChain turns raw device telemetry into meaningful signals. A dedicated **event-processing** service watches events as they flow through the pipeline: a **DETECT** stage evaluates streaming rules in real time, and a **REACT** stage runs the automated responses each firing declares — raising an **alarm** (a stateful condition with a lifecycle, a severity, and a path to notify a human) or issuing a **command** back to the device.

The service is built for **replay-correctness**: it evaluates on event time and persists its state, so a restart re-derives identical firings — none missed, none duplicated.

:::note Status
**Available today:** the DETECT + REACT pipeline is the platform's live detection engine. Detection covers **threshold**, **held-for-duration**, **repeating-occurrence**, **rate-of-change**, **silence/absence**, **windowed-aggregate**, and **area/group correlation** conditions (with static or attribute-driven thresholds); REACT actions are **raise alarm** and **send command**, with per-action guards. Rules are authored two ways over one schema — a **typed form builder** and a **visual automation canvas** — both validated by the same compiler before publish, and the canvas can **preview a draft against replayed history** before it goes live. The four-state alarm lifecycle, severity tiers with in-place escalation, propagation across the relationship graph, live alarm and detection subscriptions, and email/webhook notification with escalation are all in place.
:::

## Where rules live

Detection rules are defined on a **[device profile](./domain-model.md)** — the versioned capability contract shared by one or more device types. Because the profile is versioned (draft → publish → rollback), a change to a fleet's detection logic is authored as a draft, published atomically, and rolled back if needed, exactly like the profile's metric and command definitions. Every device that resolves to the profile picks up its rules automatically.

A rule states a **condition** over the profile's telemetry, declares its **severity**, and lists the **actions** to run when it fires.

## Condition types

| Condition | Fires when | Parameters |
|---|---|---|
| **Threshold** | a reading crosses a comparison (e.g. `temperature > 80`) | the comparison + a threshold value |
| **Duration** | the condition holds continuously for at least a set time (e.g. `pressure low for 5 minutes`) | a hold time |
| **Repeating** | the condition occurs a number of times within a window (e.g. `3 faults in 10 minutes`) | an occurrence count + a window |
| **Rate of change** | a metric moves too fast (e.g. `temperature rising > 5°/min`) | the comparison + a window (optional per-second rate) |
| **Absence / silence** | a device goes quiet — no qualifying event within a window (a dead-man check) | a silence window |
| **Windowed aggregate** | an aggregate over a window crosses a comparison (e.g. `average > 50 over 10 minutes`) | the function (count/sum/avg/min/max), a window (tumbling, sliding, or session), the comparison + value |
| **Area correlation** | enough distinct devices in an area meet the condition together (e.g. `≥ 3 devices in a zone report a fault within 5 minutes`) | the area/anchor type, a distinct-device count + window |

Each condition's comparison can be a structured `metric · operator · value` leaf or an advanced **CEL expression** over the event; both are statically type-checked and cost-limited when the profile is published, so a malformed or runaway rule is rejected before it can run.

### Static and dynamic thresholds

A threshold can be a **fixed value** on the rule, or **dynamic** — the name of a device **attribute** the rule reads at evaluation time. A dynamic threshold lets one rule adapt per device: the profile defines the rule once, and each device carries its own limit as a `SERVER`- or `SHARED`-scoped attribute (server-set values take precedence). Change the attribute and the effective threshold changes with no rule edit.

## Automated actions

When a rule fires, its **REACT** actions run. Two are built in:

- **Raise alarm** — open (or escalate) a stateful alarm for the device, described below. This is the default and needs no target beyond a severity.
- **Send command** — enqueue a command back to the device through the persistent command pipeline (dispatch is idempotent, so a replay or retry never double-sends).

A rule can carry several actions (up to a small fixed limit), and each action can be **guarded** by a condition on the firing — so, for example, one rule can raise an alarm on every firing but send a command only when the reading is in a particular band. Because a firing is **edge-triggered** (a rising edge when the condition starts holding, a falling edge when it stops), an alarm raised on the rising edge is cleared automatically on the falling edge — you author the raise, and the clear is implied.

## Authoring & previewing rules

Rules are authored in the console two ways, both over the same schema and both validated by the **same server-side compiler** before publish:

- A **form builder** — a typed form per condition type, the quickest path for a single rule. As you edit, it shows the compiler's type and cost feedback inline, before you publish.
- A **visual automation canvas** — a node graph (source → condition → optional branches → actions) for richer flows. The canvas **compiles to the same rule** a form would produce; it is an authoring surface, not a second engine. It adds **branch** nodes (route a firing to different actions by a guard) and **compute** nodes (name a reusable derived value and reference it in a condition or guard).

The canvas's standout is **preview against history**: run a *draft* rule over the profile's replayed event history and see the raise/resolve edges it *would* have produced over a chosen window — without publishing anything. Selecting a firing overlays a **per-node trace** onto the canvas, showing the path that event took (which condition matched, which branch it took, which action fired). You can edit and re-preview until the rule does what you expect, then publish.

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

## Seeing alarms & rule health

Alarms surface live in two places without any extra wiring:

- The console **Alarms** view — a live, tenant-wide list, filterable and acknowledgeable in place.
- **Dashboard widgets** — a live **alarm table** and an **alarm count** widget (see [Dashboards](./dashboards.md)), including **acknowledge/clear actions** that the server authorizes against the operator's own rights.

Both are fed by live subscriptions, so state changes appear as they happen.

A profile's own editor also shows **rule health** — per-rule status, last-fired time, and fire count — alongside a **live feed** of detections as they occur, so you can confirm a newly published rule is behaving before it ever raises an alarm.
