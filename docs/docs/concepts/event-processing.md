---
title: Event Processing & Alarms
---

# Event Processing & Alarms

DeviceChain turns raw device telemetry into signals you can act on. A dedicated **event-processing** service watches events as they flow through the pipeline. Its **detection** stage evaluates streaming rules in real time. Its **actions** stage then runs the automated responses each firing declares: raising an **alarm** (a stateful condition with a lifecycle, a severity, and a path to notify a human) or issuing a **command** back to the device.

The service evaluates on event time and persists its state, so a restart re-derives identical firings — none missed, none duplicated.

:::note Status
**Available today:** detection and actions are the platform's live detection engine, with all eight [condition types](#condition-types) (static or attribute-driven thresholds) and all four [actions](#automated-actions) with per-action guards. Rules are authored three ways over one schema — form builder, visual automation canvas, and, with the AI service enabled, a natural-language "Describe" door — all [validated by the same compiler](#authoring--previewing-rules) before publish, and the canvas can preview a draft against replayed history. The four-state [alarm lifecycle](#the-alarm-lifecycle) with in-place severity escalation, live alarm and detection subscriptions, and email/webhook [notification](#reaching-a-human) with escalation are all in place.
:::

## Where rules live

You define detection rules on a **[device profile](./domain-model.md)**, the versioned capability contract shared by one or more device types. The profile is versioned (draft → publish → rollback), so you change a fleet's detection logic the same way you change its metric and command definitions: author a draft, publish it atomically, and roll it back if needed. Every device that resolves to the profile picks up its rules automatically.

A rule states a **condition** over the profile's telemetry, declares its **severity**, and lists the **actions** to run when it fires.

## Scoping a rule to a group

By default a rule applies to **every device** that resolves to its profile. You can instead **scope a rule to a [dynamic group](./domain-model.md#facets-and-dynamic-groups)**, so it fires only for the devices that are currently members. For example, you can run a stricter heat rule only on *devices in arid areas*. Scoping is optional and set per rule. Absence and area-correlation rules cannot be scoped, and publishing a scoped one is refused.

Group membership is recorded on each event **as it is resolved**. The engine therefore sees exactly which rules applied at that moment, including when it replays history to preview or re-derive firings. When a device joins or leaves the group, it is enrolled or dropped on its next event, with no rule edit and no rescan.

## Condition types {#condition-types}

Detection covers threshold, held-for-duration, repeating-occurrence, rate-of-change, silence/absence, connectivity, windowed-aggregate, and area/group correlation conditions.

| Condition | Fires when | Parameters |
|---|---|---|
| **Threshold** | a reading crosses a comparison (e.g. `temperature > 80`) | the comparison + a threshold value |
| **Duration** | the condition holds continuously for at least a set time (e.g. `pressure low for 5 minutes`) | a hold time |
| **Repeating** | the condition occurs a number of times within a window (e.g. `3 faults in 10 minutes`) | an occurrence count + a window |
| **Rate of change** | a metric changes too fast between consecutive readings (e.g. `temperature rising > 5°/s`) | the comparison + an optional flag to normalise the change to a per-second rate |
| **Absence / silence** | a device goes quiet — no event at all within a window (a dead-man check); every event counts as a heartbeat, so the rule takes no condition | a silence window |
| **Connectivity** | a device reports an authoritative disconnect (raise) and reconnects (resolve) — for presence-asserting transports like [Sparkplug-B](./sparkplug.md) and [LwM2M](./lwm2m.md). *Authored in the console's form builder, on the automation canvas, or through the API.* | none — the [presence](./device-presence.md) edge is the whole signal |
| **Windowed aggregate** | an aggregate over a window crosses a comparison (e.g. `average > 50 over 10 minutes`) | the function (count/sum/avg/min/max), a window (tumbling, sliding, session, or a count window of N events), the comparison + value |
| **Area correlation** | enough distinct devices in an area meet the condition together (e.g. `≥ 3 devices in a zone report a fault within 5 minutes`) | the area/anchor type, a distinct-device count + window |

Each condition's comparison can be a structured `metric · operator · value` leaf or an advanced **CEL expression** over the event. Both are statically type-checked and cost-limited when the profile is published, so a malformed or runaway rule is rejected before it can run.

:::note Windows have a ceiling
Every time span a rule declares is capped at **24 hours** by default. A rule asking for longer is refused when the profile is published, and the error names the field and the limit. See [Time limits on rules](#time-limits-on-rules).
:::

### Time limits on rules {#time-limits-on-rules}

The 24-hour default cap applies to every time span a rule declares: a window, a hold time, a silence timeout, and a session gap.

The cap exists because a windowed rule keeps **one record per reading** for the whole window, per device, in an engine shared by every tenant. A multi-day window over a fleet reporting every few seconds is a large amount of memory held indefinitely. Everyone on the instance pays that cost, not only the tenant that authored the rule.

Silence timeouts and session gaps are capped too, even though they hold no readings. Those rules re-arm a timer every time a device reports, and the superseded timers are not released until their deadline passes. A long timeout under frequent reporting therefore accumulates in the same way.

If you need a longer span, an operator can raise the limit for the instance ([`maxRuleDurationSeconds`](../deployment/detection-engine.md#configuration)) after sizing the memory. Before asking for one, consider whether the question is really about *retention* rather than *detection*. A "compare against last month" question is usually better answered by querying stored history than by holding a month of readings in memory.

### Opening a stored rule in the form or on the canvas

The form builder and the automation canvas both author Connectivity rules. The presence edge is the whole signal, so neither offers a condition or parameters for the type: on the canvas it is a **Connectivity** node that takes the source's stream and feeds actions, like any other condition.

Neither surface silently rewrites a rule it cannot show in full. If the form opens a stored rule it cannot hold completely (a field it does not model, or a type it does not know), it warns that part of the definition is not shown and that saving would replace the original with only what you can see. That warning is distinct from the "could not be read" notice shown for a definition that is not valid JSON. A Connectivity rule opens with neither.

The canvas is stricter. When it opens a stored rule, it asks the compiler whether saving the rule as laid out would keep everything the stored definition says. If the rule has a type the canvas has no node for, a field or action type the canvas does not model, or if that check cannot be completed, the canvas says so and turns saving off. Edit such a rule through the API. The form can open it too, but it will warn that saving there drops what it cannot show.

Two cases are specific to rules built on the canvas:

- If the rule's definition was changed through the API after it was last saved on the canvas, the saved layout no longer matches the rule. The canvas lays the rule out again from its current definition and tells you so, so that saving does not undo the change. If it cannot lay the current rule out in full, saving is turned off.
- If the saved canvas no longer compiles as it stands, it opens as it was, with a note. Fix it on the canvas; saving then replaces the stored rule with what is on the canvas.

### Static and dynamic thresholds

A threshold can be a **fixed value** on the rule, or **dynamic**: the name of a device **attribute** the rule reads at evaluation time. A dynamic threshold lets one rule adapt per device. The profile defines the rule once, and each device carries its own limit as a `SERVER`- or `SHARED`-scoped attribute (server-set values take precedence). Change the attribute and the effective threshold changes, with no rule edit.

#### Dynamic thresholds in a CEL expression {#dynamic-thresholds-in-cel}

In a CEL expression, the event's measurements are the map `m` and the device's attributes are the map `attr`, both from a key to a number. A key is in `attr` only while the device has a **numeric** value for it at `SERVER` or `SHARED` scope. It is absent when the attribute was never set, when it was set to something other than a number, when it was set with `CLIENT` scope, and for a short time after it is set, until the change reaches the detection engine.

Test presence before you read a value. A dynamic threshold built on the form compiles to `"tempLimit" in attr && "temp" in m && m["temp"] > attr["tempLimit"]`, which does not fire for a device without the attribute. The form has no fallback value. To fall back to a fixed limit, write the fallback as its own comparison:

```
"temp" in m && ("tempLimit" in attr ? m["temp"] > attr["tempLimit"] : m["temp"] > 80.0)
```

Because this expression cannot be true for an event without `temp`, the rule looks only at events that carry `temp`. An event without it is skipped: it does not resolve a threshold alarm, and it does not cancel a duration hold.

A threshold or duration condition that would be true on every event from **every** device without the attributes it reads, whatever the event carries, is refused when the profile is published. For example, `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]` would raise an alarm for every such device, whatever it reported, for as long as the attribute was missing. A condition that still depends on the reading, such as `!("tempLimit" in attr) && m["temp"] > 80.0`, is accepted. Remember that it also applies to devices whose attribute has the wrong type or scope, not only to devices that never set one.

On a repeating, rate-of-change, windowed-aggregate or area-correlation rule the condition is a filter on which events count, so a filter such as `!("maint" in attr)` ("devices not in maintenance") is accepted there.

## Automated actions {#automated-actions}

When a rule fires, its actions run. The built-in actions are:

- **Raise alarm** — open (or escalate) a stateful alarm for the device, described below. It is the type a new action starts as, in both the form builder and the canvas, and it needs no target beyond a severity. A rule with no actions raises no alarm; it only emits a detection you can subscribe to.
- **Send command** — enqueue a command back to the device through the persistent command pipeline. Dispatch is idempotent, so a replay or retry never double-sends.
- **Call a webhook** (`httpCall`) — POST a CEL-shaped payload to an external HTTP endpoint, with hardened delivery (redirects refused, reserved headers stripped) and optional secret-store auth.
- **Publish to a connector** (`publish`) — hand a CEL-shaped payload to an **[outbound connector](./outbound-connectors.md)** that fans it out to a message broker or cloud queue (MQTT, Kafka, AWS SNS/SQS).

The two outbound actions, `httpCall` and `publish`, are described in **[Outbound Connectors](./outbound-connectors.md)**. A separate service delivers them, so a slow external system never slows detection.

A rule can carry several actions, up to a small fixed limit. An area-correlation rule carries none: its firing belongs to an area, not a device, and every action targets a device. Each action can be **guarded** by a condition on the firing. For example, one rule can raise an alarm on every firing but send a command only when the reading is in a particular band.

A firing is **edge-triggered**: a rising edge when the condition starts holding, a falling edge when it stops. An alarm raised on the rising edge is therefore cleared automatically on the falling edge. You author the raise, and the clear is implied.

## Authoring and previewing rules {#authoring--previewing-rules}

You author rules in the console in three ways. All three use the same schema, and the **same server-side compiler** validates all of them before publish:

- A **form builder** — a typed form per condition type, the quickest path for a single rule. As you edit, it shows the compiler's type and cost feedback inline, before you publish. Its action picker offers only raise alarm and send command. Guards and the outbound actions are authored on the canvas; the form shows them read-only and preserves them when you save.
- A **visual automation canvas** — a node graph (source → condition → optional branches → actions) for richer flows. The canvas **compiles to the same rule** a form would produce; it is an authoring surface, not a second engine. It adds **branch** nodes (route a firing to different actions by a guard) and **compute** nodes (name a reusable derived value and reference it in a condition or guard). It offers a node for every condition type.
- A natural-language **"Describe" door** — where the AI service is enabled, you describe the rule in words and get a drafted candidate to review and publish. It is offered when you create a new rule, and it produces a rule in the same schema the other two produce. See [AI-Assisted Authoring](./ai-authoring.md).

The canvas's standout feature is **preview against history**. You run a *draft* rule over the profile's replayed event history and see the raise/resolve edges it *would* have produced over a chosen window, without publishing anything. Selecting a firing overlays a **per-node trace** onto the canvas that shows the path the event took: which condition matched, which branch it took, and which action fired. Edit and re-preview until the rule does what you expect, then publish.

## The alarm lifecycle {#the-alarm-lifecycle}

A raised alarm is a **stateful object**, not a one-off message. Its state combines two axes into a **four-state model**:

- **State** — `ACTIVE` while the condition holds, `CLEARED` once it resolves.
- **Acknowledged** — whether an operator has taken ownership of the alarm, with a record of who and when.

An alarm moves through `ACTIVE/unacknowledged` → `ACTIVE/acknowledged` → `CLEARED`. A flapping condition re-activates the *same* alarm rather than spawning duplicates.

An alarm names the device that raised it. You query alarms tenant-wide with filters (state, severity, acknowledgement, originating device) rather than reading them off a parent entity.

### Severity and escalation

Each alarm carries a **severity**: `CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, or `INDETERMINATE`. A single condition can declare rules at several severity tiers (for example `temp > 80 → MAJOR`, `temp > 100 → CRITICAL`). When those rules raise under the same alarm key, the engine **escalates a single active alarm in place** to the highest tier currently met, and de-escalates as conditions ease, rather than opening a separate alarm per tier. A raise alarm action with no alarm key is keyed on the rule itself (its profile and rule tokens), so rules left at that default open separate alarms.

## Reaching a human {#reaching-a-human}

A raised alarm can notify people through the **notification** system. A per-tenant policy routes alarms to email (SMTP) and webhook channels with per-severity routing: each of a policy's rules maps one severity (or any) to a channel and a recipient list.

**Escalation is per policy, not per severity.** The policy sets a single interval and cap. An alarm that stays neither acknowledged nor cleared is re-notified through the same channels on that schedule until the cap. An alarm carries one escalation clock and tier however many policies match it, so the shortest interval among them paces all of them. You cannot give a severity a cadence of its own.

Policies also support throttling: a minimum gap between notifications for the same alarm, so a repeatedly-signalling alarm does not flood a channel.

Where two policies route to the same channel and an **identical** recipient list, the duplicate delivery is collapsed and the notification is sent once. The same recipients in a different order, or in different letter case, are treated as distinct, and both are sent.

Channel credentials (the SMTP password, a webhook bearer token) are held in the platform's encrypted secret store. They are sealed at rest with envelope encryption, write-only over the API, and never returned as cleartext.

This machine-to-human path is kept distinct from the machine-to-machine **[outbound connectors](./outbound-connectors.md)** that fan events out to other systems.

## Seeing alarms and rule health {#seeing-alarms--rule-health}

Alarms surface live in two places without any extra wiring:

- The console **Alarms** view — a live, tenant-wide list, filterable and acknowledgeable in place.
- **Dashboard widgets** — a live **alarm table** and an **alarm count** widget (see [Dashboards](./dashboards.md)), including **acknowledge/clear actions** that the server authorizes against the operator's own rights.

Both are fed by live subscriptions, so state changes appear as they happen.

A profile's own editor also shows **rule health** — per-rule status, last-fired time, and fire count — alongside a **live feed** of detections as they occur. You can confirm a newly published rule is behaving before it ever raises an alarm.

## Running the service {#running-it}

The service that evaluates rules holds live state in memory and detects from a single active instance; any extra replicas are standbys. That gives it a few operational properties to know before you depend on it in production: what a restart costs, how quickly a silence rule can fire, why an alarm might not clear, and how to find a rule that is failing to evaluate. **[Running the Detection Engine](../deployment/detection-engine.md)** covers them.
