---
sidebar_position: 7
title: Running the Detection Engine
---

# Running the Detection Engine

[Detection rules](../concepts/event-processing.md) are evaluated by a service that behaves
differently from the rest of the platform: it holds live state in memory, it runs as a **single
instance**, and it evaluates on **event time** rather than on the clock. This page is the operator's
contract — what that buys, what it costs, and how to tell a healthy engine from a stuck one.

If you are looking for what a rule can express or how to author one, start at
[Event Processing & Alarms](../concepts/event-processing.md) instead.

## One instance, on purpose

The detection engine runs as **exactly one replica**, and the chart ships it that way with a
recreate-style rollout.

This is not a scaling limitation waiting to be lifted casually. The engine holds every open window,
every running timer and every raised-edge latch in memory, and commits them as a single checkpoint.
Two engines reading the same stream would each see part of it, and each would checkpoint a state
built from a partial view.

Three things protect that invariant, and it is worth knowing that they are not equally strong:

1. **The rollout strategy** stops a deploy from overlapping the old and new instances. It covers
   deploys only.
2. **The chart refuses to render** a configuration that asks for more than one replica alongside
   that strategy.
3. **The engine itself refuses to commit** a checkpoint that is behind one already stored. If two
   engines do briefly run — an eviction, a node drain, or a manually deleted pod all schedule a
   replacement immediately — the one that fell behind stops rather than overwriting.

:::warning A drain can briefly run two engines
Only the rollout path is fully covered. An eviction or node drain has the replacement scheduled
before the original has stopped, so for a few seconds two engines can consume the same stream. The
checkpoint fence contains it — the loser halts — but it is the reason to prefer a deliberate rollout
over draining the node the engine happens to be on.
:::

Because there is one replica, there is no pod disruption budget. Draining its node stops detection
until the pod is rescheduled.

## What a restart costs

A restart is routine, not an incident. On start the engine reloads its last checkpoint and replays
the stream from that position, so it re-derives the state it had.

| | What happens |
|---|---|
| **Events already processed and committed** | Nothing is lost. Messages are acknowledged only *after* the checkpoint that includes them commits, so anything not committed is redelivered. |
| **Alarms and commands already sent** | Re-derived and re-sent, then collapsed: an alarm is an idempotent update, and a command carries a key that prevents a second enqueue. |
| **Outbound webhooks and connector publishes already sent** | Re-derived and **sent again**. Nothing on the platform side collapses them — see the delivery section below. |
| **Rule fire counts** | **Over-counted.** A replay increments them again. The *last fired* time is correct; treat the count as a floor, not an exact total. |
| **Open windows, holds and timers** | Restored from the checkpoint. A hold that was part-way through is still part-way through. |
| **Rules using a dynamic (attribute-based) threshold** | See the caveat below. |

:::caution Dynamic thresholds and replay
A rule whose threshold reads a **device attribute** is not fully replay-safe. On replay, the
attribute is read at its *current* value rather than the value it held at the original event time.
If the attribute changed in between, a firing can be lost or an extra one produced. Rules with a
**fixed** threshold are unaffected. If you rely on exact replay behaviour — for auditing an
erasure, or reproducing an incident — prefer fixed thresholds.
:::

## Delivery: everything is at-least-once

The telemetry path is at-least-once end to end, so plan for a repeat rather than for exactly one.

- A **detection** may be produced more than once and is collapsed by its identity.
- An **alarm** update is idempotent — a repeat lands on the same alarm.
- A **command** carries a key derived from the firing, so a repeat never enqueues a second command.
- An **outbound webhook or connector publish** is the one action a retry can genuinely duplicate.
  Every request carries an `X-DC-Idempotency-Key` header derived from the firing, but collapsing on
  it is the **receiving endpoint's** job. For queue and broker targets the key travels as metadata,
  which most brokers cannot act on. **Design outbound receivers to be idempotent.**

When a downstream system is unavailable, the message is left unacknowledged and retried on a timer
rather than hammered. After five delivery attempts, roughly four minutes apart in total:

- an **outbound connector** request is **dead-lettered**, so it can be inspected or replayed;
- a **detection** is **dropped with a loud error** — there is no dead-letter queue on that path. A
  dropped *raise* will not re-appear until the condition clears and breaches again; a dropped
  *resolve* leaves an alarm active that should have cleared.

The `ReactPoisonDropping` alert exists for exactly that case and should be treated as urgent.

:::caution An action that fails takes its later siblings with it
A rule's actions run in the order they are listed, and a failing action stops the rest. On each
retry the actions *before* it run again, and the actions *after* it have still never run — so if the
event is eventually dropped, those later actions are lost without ever having been attempted.

**Order a rule's actions so the important one comes first.** If a rule both raises an alarm and calls
a webhook, putting the alarm first means a flaky endpoint cannot cost you the alarm.
:::

## Timing: what "when" means

The engine works on **event time** — the timestamp on the reading — not on when the message arrived.
Two settings follow from that.

**Lateness tolerance** is how long the engine waits for out-of-order events before considering a
moment settled. Raise it if your fleet buffers readings and uploads them in batches, or if an
upstream hop can stall; the cost is that every time-based decision is delayed by the same amount.

**A device-reported timestamp is clamped** if it is too far in the *future* relative to when the
platform received it, so one device with a wrong clock cannot drag the whole engine's sense of time
forward. Timestamps in the past are treated as lateness, not clamped.

### How quickly can an absence rule fire?

An "absence" or "silence" rule cannot fire the instant a device goes quiet — nothing arrives to
trigger the evaluation. The floor is:

> the rule's timeout **+** the lateness tolerance **+** the longer of the idle-check interval and
> the checkpoint interval **+** one tick

With shipped defaults that is roughly **the rule's timeout plus about fifteen seconds**. Set the
timeout to the silence you actually care about and expect detection shortly after, not exactly on
it.

There are two ways an absence rule fires, and only one of them waits on the broker. If a *later
event* moves the engine's sense of event time past the rule's deadline, it fires immediately —
including while the engine is working through a backlog, which is the replay-correct behaviour. The
other path is genuine silence, where no event will ever arrive to trigger it; **that** one fires on
wall-clock time and only once the broker confirms there is nothing left to process, so a backlog
cannot make the engine call a device silent when it simply has not read that device's events yet.

## A raised alarm that will not clear

An alarm is cleared by its **last** contributing rule resolving, not by any one of them. If several
rules raise onto the same alarm, all of them must resolve.

Beyond that, the most common cause is a rule kind that **only re-evaluates when an event arrives**.
If a device raises an alarm and then goes completely silent, there is nothing to observe the
condition ending, and the alarm stays active. A **repeating-occurrence** rule has a stronger version
of the same problem: it cannot observe the end of its condition from non-matching traffic at all, so
only a fresh matching event or a scope change will clear it.

**The intended pattern is to pair such a rule with an absence rule**, so a device that stops
reporting raises a distinct, actionable signal rather than leaving a stale one standing.

Two more causes worth checking:

- **An operator "clear" does not remove the underlying condition.** If the condition is still true,
  the next event re-activates the same alarm. Clearing is an acknowledgement that you have seen it,
  not a suppression.
- **A device that leaves a rule's scope and then goes silent** keeps its raised alarm. Scope changes
  take effect on the device's next event, and a silent device has no next event.

## A rule that is not firing

In order of how often it is the answer:

1. **The profile was never published.** Rules run against live telemetry only after the profile
   version containing them is published. A draft rule fires nothing.
2. **The device does not resolve to a profile.** A device whose type has no profile, or whose
   profile has never been published, matches **no rules at all**. Nothing errors — the events are
   simply not evaluated against anything.
3. **The metric name does not match what the device sends.** A condition naming a metric that never
   appears is valid and compiles cleanly; it just never becomes true. Check the device's recent
   events for the exact key.
4. **The rule is scoped to a group the device is not currently in.** Membership is recorded on each
   event as it is resolved, so a device that has just been added joins on its next event.
5. **A dynamic threshold has no attribute set on that device.** The rule reads the device's own
   attribute; a device without it does not fire.
6. **The rule errors at evaluation time.** This is the hard one — see below.
7. **The publish notification was lost.** Rare, but it leaves no trace where you would look for one.

:::warning A lost publish notification silences a profile with no error anywhere
When a profile version is published, the rules it contains are handed to the detection engine as a
one-shot notification. If the broker is unavailable at that exact moment, **the publish itself still
succeeds** — the profile shows as published, the rules are visible in the console, and nothing is
marked failed. The engine simply never receives them, and they never run.

Nothing retries it and no alert fires. **The recovery is to publish the profile again**, which
re-sends the notification. If a whole profile's rules stopped firing at once and item 1 above does
not explain it, check whether the broker was disrupted around the publish time and republish.
:::

:::caution A rule that errors on every event looks exactly like a quiet rule
When a rule's expression fails at evaluation time, the event is skipped. Rule health still reports
the rule as **active with a zero fire count**, which is indistinguishable from a rule whose
condition is simply never true, and the platform metric that counts these errors is not broken down
per rule.

If the `DetectFanoutEvalErrors` alert is firing and you cannot tell which rule is responsible, use
the **canvas preview** on each suspect rule: preview is the one place an evaluation error is
attributed to the rule that caused it.
:::

## Previewing before you publish

Preview replays real history through the same engine the platform runs, without publishing anything.
It is the best tool available for checking a rule, and it has limits that explain most surprising
results:

- It starts **cold** at the beginning of the window. A hold or a window that began earlier is
  invisible, and an aggregate window straddling the end never closes.
- It resolves **no device attributes**, so a rule with a dynamic threshold previews as never firing.
- It does not apply a **group scope** — a scoped rule previews across the whole profile.
- It cannot arm absence for a device that has **never reported**.

When preview truncates — because the window aged out of retention, or a scan limit was reached — it
tells you so rather than silently returning a short result. Read that notice before concluding a
rule does not fire.

## Configuration

All of these are optional; the shipped defaults are appropriate for most deployments.

| Setting | Default | What it does |
|---|---|---|
| `watermarkLatenessSeconds` | 5 | How long to wait for out-of-order events before treating a moment as settled. **Raise this** if events arrive in batches or an upstream hop can stall; it is the main defence against a false absence alarm. |
| `maxEventFutureSkewSeconds` | 300 | How far ahead of the platform's own clock a device-reported timestamp may be before it is clamped. |
| `idleAdvanceGuardSeconds` | 5 | How long the engine must be quiet before it will fire a rule on wall-clock time. A negative value turns that path off: absence rules then fire only when a *later event* moves event time past their deadline, so a device that goes silent and stays silent never raises one. |
| `checkpointEvents` | 1000 | Maximum events processed between checkpoints. |
| `checkpointIntervalSeconds` | 10 | Maximum time between checkpoints, so a quiet stream still commits. |
| `maxRulesPerTenant` | 500 | Per-tenant rule ceiling. **Measured and reported, not enforced** — see below. |
| `maxLiveKeysPerTenant` | 1000000 | Per-tenant ceiling on live windows and timers. Also measured, not enforced. |
| `outboundMessagesPerSecond` | 100 | Per-tenant rate at which outbound connector actions are dispatched. |
| `outboundBurst` | 200 | Burst allowance for the above. |

:::note State budgets are measured, not enforced
The two per-tenant ceilings raise a metric and a log line when a tenant exceeds them. **Nothing
stops the tenant.** A single tenant authoring pathological rules can still exhaust the shared
engine's memory. Watch `DetectTenantOverStateBudget` and act on it — the alert is the enforcement.
:::

## What to watch

| Signal | Means |
|---|---|
| `DetectCheckpointsStalledWithBacklog` | **The most important alert here.** Checkpoints have stopped while work is waiting. Either the engine has halted after losing a split-brain race, or its database is unavailable. Detection is not happening. |
| `DetectConsumerBacklogHigh` | The engine is behind. Absence detection is suppressed while it is. |
| `DetectWatermarkLagHigh` | The engine's sense of event time is falling behind real time. |
| `DetectFanoutEvalErrors` | One or more published rules are failing to evaluate. See the caution above. |
| `ReactPoisonDropping` | Actions are being dropped after exhausting their retries — alarms or commands are being lost. Treat as urgent. |
| `ReactConnectorEgressShedding` | Outbound dispatch is over the tenant's rate limit and is being shed. |
| `DetectTenantOverStateBudget` | A tenant is over a ceiling that is not enforced. |

:::warning A halted engine still reports healthy
If the engine halts after losing a split-brain race, its health endpoints continue to report ready.
The pod looks fine and detection has stopped. `DetectCheckpointsStalledWithBacklog` is currently the
signal that catches this, and it fires after a delay — do not rely on pod health alone to tell you
detection is running.
:::

Per-rule status, last-fired time and fire count are available in the console on the device profile's
**Rule Health** tab, alongside a live feed of detections as they occur.

## Deleting a tenant

The detection engine holds tenant state as an opaque checkpoint that no query can interpret, so a
[tenant deletion](./tenant-deletion.md) asks the engine directly to evict it and waits for the
engine to confirm that the eviction has been **committed** — not merely applied in memory. An
instance running detection must therefore have the engine reachable for a deletion to complete; if
the engine is halted or unreachable, the deletion stays open rather than completing over data that
is still there.
