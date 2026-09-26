---
sidebar_position: 8
title: Running the Detection Engine
---

# Running the Detection Engine

[Detection rules](../concepts/event-processing.md) are evaluated by a service that behaves
differently from the rest of the platform: it holds live state in memory, it detects from a
**single active instance**, and it evaluates on **event time** rather than on the clock. This page
is the operator's contract — what that buys, what it costs, and how to tell a healthy engine from
a stuck one.

If you are looking for what a rule can express or how to author one, start at
[Event Processing & Alarms](../concepts/event-processing.md) instead.

## One active engine, on purpose

Exactly one engine detects at a time. The chart ships a single replica with a recreate-style
rollout, which is the simplest way to arrange that.

This is not a scaling limitation waiting to be lifted casually. The engine holds every open window,
every running timer and every raised-edge latch in memory, and commits them as a single checkpoint.
Two engines reading the same stream would each see part of it, and each would checkpoint a state
built from a partial view.

Three things protect that invariant, and it is worth knowing that they are not equally strong:

1. **The rollout strategy** stops a deploy from overlapping the old and new instances. It covers
   deploys only.
2. **A partition lease** decides which replica may act. A replica fetches, acknowledges,
   checkpoints and publishes only while it holds the lease, and stops the moment it does not.
3. **The engine itself refuses to commit** a checkpoint that is behind one already stored. If two
   engines do briefly run — an eviction, a node drain, or a manually deleted pod all schedule a
   replacement immediately — the one that fell behind stops rather than overwriting.

:::warning A drain can briefly run two pods
Only the rollout path is fully covered by the strategy. An eviction or node drain has the
replacement scheduled before the original has stopped. The lease and the checkpoint fence contain
it — the pod that does not hold the lease stops consuming, and a lagging checkpoint is refused —
but it is the reason to prefer a deliberate rollout over draining the node the engine happens to
be on.
:::

### Running a warm standby

You may run more than one replica, and the extra replicas are **standbys, not writers**. Set both:

```yaml
functionalAreas:
  event-processing:
    replicas: 2
    strategy: RollingUpdate
```

Raising `replicas` without also moving off the recreate strategy fails the render, because that
strategy stops every pod before starting any — the standby would be down exactly when it is needed.

A standby is ready and serves the API, but holds no lease: it consumes nothing, commits nothing, and
takes the partition when the leader releases it or its lease expires. What that buys is the pod
start and, on an eviction, the wait for a replacement to be scheduled. What it does **not** skip is
the restart cost described in the next section — a standby holds no pre-loaded engine state, because
loading it would mean reading a checkpoint the leader is still writing.

That includes the actions detections trigger: a standby dispatches nothing, so a tenant's outbound
ceiling at the engine is charged once, on the replica that detects. When the partition moves, the
old and new replica can both dispatch for up to about five seconds, and a connector call made
twice in that window reaches its destination twice: DeviceChain passes the idempotency key on to
the destination and does not deduplicate connector calls itself.

With a single replica there is no pod disruption budget, and draining its node stops detection until
the pod is rescheduled. A standby is the way to avoid that.

## What a restart costs

A restart is routine, not an incident. On start the engine reloads its last checkpoint and replays
the stream from that position, so it re-derives the state it had.

If the engine's only pod stops without releasing its partition (a crash or an out-of-memory
kill), the actions waiting to be dispatched resume when the replacement takes the partition, up
to about 35 seconds later. Detection resumes later still: the replacement first waits out a further
handover period, because it cannot tell a stopped pod from one that is cut off but still running,
and then replays as described below. A graceful restart releases the partition and skips both
waits.

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

- an **outbound connector** request is **dead-lettered**, so it can be inspected;
- a **detection** whose actions could not be dispatched is **dead-lettered too**, with a loud error.

:::caution Dead-lettered is recorded, not retried
Nothing re-runs a dead letter. The record exists so a failure is visible and diagnosable
rather than silent — the consequences of the failure itself stand either way. A *raise*
that was not dispatched will not re-appear until the condition clears and breaches again,
and a *resolve* that was not dispatched leaves an alarm active that should have cleared.
Treat a dead letter as something to investigate, not something that will drain on its own.
:::

An outbound action refused by the tenant's outbound rate is dead-lettered with reason `shed`, within
a budget. That happens at once rather than after retries, because waiting does not bring a tenant
back under its ceiling. The rate is metered on the time the triggering telemetry reached the
platform, so a backlog the engine works through after a restart is charged as it happened rather
than all at once. Past a per-tenant budget of about one letter a second (60 at once) and ten a
second in total, shed actions are counted and summarised in one letter per tenant per minute.
Detections the engine re-publishes after a restart are recognised by the message bus and stored
once within a 30-minute window, so they are neither dispatched nor charged twice.

A message a service abandons on its last attempt — a pod stopped mid-handling, or a handler
that ran past its window — is recorded too, with reason `no-outcome`. Such a letter never
settles a command: the last attempt may have done its work and lost only its acknowledgement.
For high-volume streams (device events, commands and detection actions) the letter points at
the original rather than copying it, and names where to find it until the stream ages it out; so
does a letter whose message is too large to copy. A connector request's letter points at the
connectors service's own dead-letter stream, which holds the full request. The record is written by the service that
owned the message, the next time that service pulls from the stream — so while a service is
down, its records arrive late rather than not at all.

The engine's own reading of resolved events is the exception. It acknowledges an event only after
a checkpoint includes it, and reads the stream again from its last checkpoint on every start, so
an event whose attempts ran out while the checkpoint could not be saved is not lost and is not
dead-lettered. It is counted instead, and the `ReplayCoveredDeliveriesExhausted` alert reports it
([Messages that ran out of delivery attempts](./observability.md#max-delivery-records)).

Read them with `dcctl dead-letters list`, which authenticates as an operator identity:

```bash
dcctl dead-letters list --server <host> --email <you> --password <secret> \
  --tenant acme --since 2026-09-04T00:00:00Z
```

They are also on the instance's admin GraphQL endpoint as `deadLetters`, gated on the same
authority as the audit journal. Records are kept for 30 days by default — longer than the
underlying message stream, which is the point of storing them — and the retention is
configurable per deployment.

The `ReactPoisonDropping` alert exists for exactly that case and should be treated as urgent.

:::caution An action that fails takes its later siblings with it
A rule's actions run in the order they are listed, and a failing action stops the rest. On each
retry the actions *before* it run again, and the actions *after* it have still never run — so if the
event is eventually given up on, those later actions never happened at all. The dead letter records
that the detection fired and its actions did not; it does not carry them out.

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

**What happens past the tolerance** depends on the rule kind, and it is worth knowing before you
size the setting:

- **Rules with a window discard the reading**: tumbling-window aggregates, session/gap rules, and
  the sliding kinds — repeating, sliding aggregates and correlation. A window is a claim about a
  span of time, and a reading from outside the span the rule currently covers is not evidence about
  that span. Counting it would let "three readings above 80 within ten seconds" fire on readings an
  hour apart.
- **Duration rules discard a matching reading that is further behind the frontier than their hold
  time.** Such a reading could only change a hold the engine has already decided. A reading inside
  that span is placed by its own time, not by when it arrived. A late reading showing the condition
  had stopped part-way through a run restarts the run from the newest reading that met it, and a
  late reading that meets the condition never reopens a run across a break the engine has already
  seen, so a late reading cannot raise a duration alarm the readings do not support. A reading that
  does not meet the condition ends a raised duration alarm however late it arrives, unless it is
  older than the alarm: a late reading from before the alarm was raised, arriving after it, does
  not withdraw it.
- **Rules without a window still evaluate it** — threshold, count-window and rate. Each compares a
  reading against the one before it or against a fixed bound, so there is no span for a late
  reading to fall outside of.

In every case the reading is **stored and charted normally**; this is a detection-only effect.

Inside the tolerance nothing changes: an out-of-order reading that still falls within the window is
folded in as normal, which is what the tolerance is for. The window can stretch by up to the
tolerance as a result — that is what tolerating out-of-order arrival means — but no further.

The sliding kinds and duration rules **count what they discard**, once for each rule that
discards a reading. `detect_late_samples_total` rises every time a reading arrives after the window
it belonged to has passed, when a duration rule discards a matching reading further behind the
frontier than its hold time, and when a reading from before a duration alarm was raised arrives
after it. A fleet whose rules have gone quiet therefore has something to look at rather than
silence; a store-and-forward upload is the usual cause. Tumbling-window and session rules discard
silently and do not appear in it.

A duration rule raises its alarm when the frontier passes the end of the hold, and the frontier
trails the newest reading by the lateness tolerance. A reading showing the condition stopped that
arrives before then ends the run without raising, even when every reading arrives in order and the
condition had held for the full hold time. So an episode is certain to raise only if it lasts its
hold time plus the lateness tolerance.

One property makes the tolerance less of a lever than it looks: **the frontier is shared across the
whole instance**, not tracked per device. So a fleet's busy devices carry it to roughly "now"
regardless of how long one quiet device was away, and raising the tolerance to cover a
half-hour store-and-forward upload would mean delaying every time-based decision, for every tenant,
by half an hour. Where that trade does not work, the answer is to shorten the upload batches or to
keep window-shaped rules off those metrics — see [connecting a
device](../guides/connecting-a-device.md).

The same applies to readings that waited **inside the platform**. While `event-sources` is down,
the platform broker keeps storing what devices publish over MQTT, and `event-sources` works through
that backlog when it returns. Each reading keeps its own time: one the device reported, or, for a
reading sent with no `occurredTime`, the moment the broker received it. Only transports that
bypass `event-sources` keep arriving in the meantime: LwM2M and Sparkplug do, and they keep the
frontier at "now", while HTTP ingest is served by `event-sources` itself and is down with it. After an outage longer than a rule's window, the backlog therefore arrives late to the
sliding kinds, and to duration rules when it is older than their hold time, exactly as a
store-and-forward upload does: it is stored and charted normally, is not used by those rules, and
`detect_late_samples_total` rises.

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
condition ending, and the alarm stays active. A **repeating-occurrence** rule does not have this
problem while the device keeps reporting: a non-matching reading that carries its metric still ages
the earlier matches out of the trailing window, and the alarm clears when the count drops below N.
**Count-window** and **session** rules have a stronger version of the problem: a count window is
counted in matching events and a session is opened only by one, so neither can observe the end of
its condition from non-matching traffic at all — a raised alarm stays up until the next window
completes or the next session closes with the condition no longer true, which may be never.

**The intended pattern is to pair such a rule with an absence rule**, so a device that stops
reporting raises a distinct, actionable signal rather than leaving a stale one standing.

Three more causes worth checking:

- **An operator "clear" does not remove the underlying condition.** If the condition is still true,
  the next event re-activates the same alarm. Clearing is an acknowledgement that you have seen it,
  not a suppression.
- **A device that leaves a rule's scope and then goes silent** keeps its raised alarm. Scope changes
  take effect on the device's next event, and a silent device has no next event.
- **The rule that raised it no longer runs.** A rule that is skipped at load — the profile's
  **Rule Health** tab shows it as a compile error — is not evaluated, so nothing resolves the alarm
  it raised before. Clear the alarm by hand once you have fixed or retired the rule.

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
5. **A dynamic threshold has no attribute set on that device.** A threshold built on the form reads
   the device's own attribute, and a device with no numeric `SERVER` or `SHARED` value for it does
   not fire. A value that is not a number, or one set with `CLIENT` scope, counts as not set. A CEL
   expression with a [fallback](../concepts/event-processing.md#dynamic-thresholds-in-cel) fires on
   its fallback instead.
6. **The rule errors at evaluation time.** This is the hard one — see below.
7. **The publish notification was lost.** Rare, but it leaves no trace where you would look for one.
8. **The rule stopped compiling after an upgrade.** An upgrade can refuse a rule that an earlier
   version accepted. Such a rule is skipped when the engine loads it, and the profile's **Rule
   Health** tab shows it as a compile error with the reason. The release notes list each such change.

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
- It resolves **no device attributes**: every device previews as though it had none. A dynamic
  threshold built on the form therefore previews as never firing, and a CEL expression with a
  [fallback](../concepts/event-processing.md#dynamic-thresholds-in-cel) previews its fallback on
  every device, including devices that do have the attribute.
- It does not apply a **group scope** — a scoped rule previews across the whole profile.
- It cannot arm absence for a device that has **never reported**.
- It runs with **no lateness tolerance**, so a reading that arrives further behind the rest of the
  replayed history than a sliding window or a duration rule's hold time is not used.

When preview truncates — because the window aged out of retention, or a scan limit was reached — or
sets readings aside as late, it tells you so rather than silently returning a short result. Read that notice before concluding a
rule does not fire.

## Configuration {#configuration}

All of these are optional; the shipped defaults are appropriate for most deployments.

The detection engine has no clock-skew setting of its own. How far a device-reported
timestamp may lead the platform's own clock is decided once, when the event is resolved, and
every consumer — the stored history, the live projections, detection and replay alike —
reads the same already-bounded value. It is configured on the device-management area as
`maxEventFutureSkewSeconds`, in seconds, **default 300**. A reading whose reported time leads the
moment the platform received it by more than that is stored at the ceiling rather than refused.

A **negative value is rejected at startup**, and it is worth knowing what it would have meant:
disabling the bound entirely. One event dated years into the future then pins the device's
last-activity time — every projection here keeps only the strictly newer value — so its inactivity
sweep never fires again and the device can never be seen to go offline. There is no supported way
to turn the bound off; raise the number if a fleet's clocks genuinely drift.

`watermarkLatenessSeconds` below is a different setting for a different direction: skew bounds how
far a timestamp may run *ahead*, lateness bounds how long the engine waits for one that arrives
*behind*.

| Setting | Default | What it does |
|---|---|---|
| `watermarkLatenessSeconds` | 5 | How long to wait for out-of-order events before treating a moment as settled. **Raise this** if events arrive in batches or an upstream hop can stall; it is the main defence against a false absence alarm. It also tolerates the small reorder between one device's events that resolution introduces, which grows with the number of `device-management` replicas: a windowed rule still counts an event that arrives within this margin, a duration rule places it by its own time, and the other rules ignore a reading older than one they have already seen. It does not cover an event whose publish failed and was retried, which arrives at least 60 seconds late. |
| `idleAdvanceGuardSeconds` | 5 | How long the engine must be quiet before it will fire a rule on wall-clock time. A negative value turns that path off: absence rules then fire only when a *later event* moves event time past their deadline, so a device that goes silent and stays silent never raises one. |
| `checkpointEvents` | 1000 | Maximum events processed between checkpoints. |
| `checkpointIntervalSeconds` | 10 | Maximum time between checkpoints, so a quiet stream still commits. **At most 30**: a checkpoint is what acknowledges the stream, so an interval near or past the broker's 60-second acknowledgement window makes messages on a quiet stream redeliver. 30 leaves room for the checkpoint itself, and the service refuses anything longer at startup. |
| `maxRuleDurationSeconds` | 86400 | Longest time span a rule may declare — window, hold, silence timeout or session gap. **Enforced**: a longer rule is refused at publish. See below. |
| `maxRulesPerTenant` | 500 | Per-tenant rule ceiling. **Measured and reported, not enforced** — see below. |
| `maxLiveKeysPerTenant` | 1000000 | Per-tenant ceiling on live windows and timers. Also measured, not enforced. |
| `maxRetainedSamplesPerTenant` | 5000000 | Per-tenant ceiling on readings held inside open windows. Also measured, not enforced. |
| `outboundMessagesPerSecond` | 100 | Per-tenant rate at which outbound connector actions are dispatched, metered on the time the triggering telemetry reached the platform. |
| `outboundBurst` | 200 | Burst allowance for the above. |
| `shedLetterPerSecond` | 1 | Per-tenant rate at which shed outbound actions are recorded as individual dead letters. |
| `shedLetterBurst` | 60 | Burst allowance for the above. |
| `shedLetterGlobalPerSecond` | 10 | The same, across every tenant. Shed actions past either budget are counted and summarised in one dead letter per tenant per minute. |
| `shedLetterGlobalBurst` | 100 | Burst allowance for the above. |

### The rule-duration ceiling is enforced

`maxRuleDurationSeconds` is the one setting in the table above that **refuses a rule** rather than
reporting on it. A rule declaring a longer window, hold, timeout or gap is rejected when the profile is published, with
an error naming the field and the limit, and the same ceiling is applied again when the engine loads
a published rule — so the two can never disagree about what is runnable.

It exists because a windowed rule retains **one record per reading** for the length of its window,
per device. That memory is committed for as long as the rule lives, in a process shared by every
tenant, and it is the one cost that cannot be observed after the fact and then reined in — by the
time the metric moves, the memory is already allocated. Raising this limit raises that exposure for
the whole instance, so size it before you change it: roughly *reporting rate × window × devices ×
32 bytes*, summed over the rules that use long windows.

Silence timeouts and session gaps are bounded for the same reason, even though they hold no
readings. Those rules push a new entry onto the engine's timer heap every time a device reports and
the superseded entry is not discarded until its deadline passes — so a three-day silence timeout on
a device reporting every ten seconds carries roughly 26,000 pending entries *for that one device*.
A long timeout costs memory in direct proportion to its length, just as a long window does.

:::danger A rule over the ceiling does not run — it is refused at startup, not grandfathered
The ceiling is applied when the engine **loads** a rule, not only when one is published. A rule
whose window exceeds the current ceiling fails to compile on load and is **skipped**: it does not
run. The evidence is an error line in the engine's log and a **Compile error** status, carrying the
compile diagnostic, on the profile's **Rule Health** tab, which recompiles every published rule under
the current ceiling. No alert fires, and no metric moves — a skipped rule holds no state to be
measured.

Two situations produce this, and both are silent:

- **Lowering `maxRuleDurationSeconds`.** Rules published under the old, higher limit stop working at
  the next restart. They are not grandfathered.
- **Upgrading to a version that introduces the ceiling.** Any rule published while the limit did not
  exist — a seven-day aggregate, say — is refused the first time the upgraded engine starts.

Before lowering the limit or upgrading, inventory the published rules for time spans above the new
value and shorten or retire them deliberately. Afterwards, check the engine log for
`failed to compile; skipping` and confirm the rules you expect are running on the profile's **Rule
Health** tab.
:::

### The expression cost ceiling is fixed

Every CEL expression in a detection rule is cost-checked when the profile is published: the
condition, an action's guard, a payload template and an alarm-key template. A rule whose expression
has an estimated worst-case cost above **100** is refused, with an error that states the estimate
and the ceiling. The engine applies the same ceiling again when it loads a published rule.
Dynamic-group selectors are checked against the same value when a group is saved.

The ceiling is the same for every tenant, and there is no setting to raise it, for one tenant or
for the instance. It bounds how much work one reading can cost the single engine that every tenant
shares. An expression that iterates over a reading's measurements (`m.all(...)`, `m.exists(...)`)
is the usual way to reach it; name the measurements you need instead.

:::note The per-tenant state budgets are measured, not enforced
The three per-tenant ceilings — rules, live keys, and retained samples — raise a metric and a log
line when a tenant exceeds them. **Nothing stops the tenant.** A single tenant authoring
pathological rules can still exhaust the shared engine's memory. Watch
`DetectTenantOverStateBudget` and act on it — the alert is the enforcement.

Watch **all three** memory dimensions. They fail in different directions and none implies the
others, so any one of them read alone can look healthy while the engine fills up:

| Metric | Counts | Moves when |
|---|---|---|
| `devicechain_eventprocessing_detect_live_keys` | open windows and timers | many devices across many rules |
| `devicechain_eventprocessing_detect_retained_samples` | readings held inside open windows | one long-window rule on a busy device — **one** live key, hundreds of thousands of readings |
| `devicechain_eventprocessing_detect_pending_timers` | entries in the timer heap | a long silence timeout or session gap under frequent reporting — again one live key, and no retained readings at all |

The second and third exist because the first is flat in exactly the cases that exhaust memory
fastest. Only the first two have per-tenant ceilings; the timer heap is reported as an instance-wide
total, because attributing it to a tenant would mean walking the whole heap on every checkpoint.
:::

## What to watch

| Signal | Means |
|---|---|
| `DetectCheckpointsStalledWithBacklog` | **The most important alert here.** Checkpoints have stopped while work is waiting: the engine cannot reach its database or the broker, or its loop is stuck. Detection is not happening. |
| `DetectConsumerBacklogHigh` | The engine is behind. Absence detection **on silence** is suppressed while it is — a later event still fires an overdue absence, as above. |
| `DetectWatermarkLagHigh` | The engine's sense of event time is falling behind real time. |
| `DetectFanoutEvalErrors` | One or more published rules are failing to evaluate. See the caution above. |
| `ReactPoisonDropping` | Actions are not being dispatched after exhausting their retries — alarms and commands are not happening. The detections are dead-lettered so you can see which, but nothing replays them. Treat as urgent. |
| `DeadLetterWriteLost` | Something was given up on **and** could not be written to the dead-letter stream. Look at the broker, and at the service's log: a letter the service refused to write lands here too, and so does a dead letter that the dead-letter store or the command writeback ran out of attempts on. |
| `DeadLetterStoreLosing` | Dead letters reached the stream but could not be written to the store, so they will age out of it unrecorded. Look at the operator database. |
| `ReactConnectorEgressShedding` | A tenant is over its outbound rate on the timeline its telemetry reached the platform, and its outbound actions are being shed. Each is dead-lettered with reason `shed`, within a budget; read them with `dcctl dead-letters`. A catch-up after a restart does not cause this. |
| `ReactShedLettersOverBudget` | A tenant is shedding outbound actions faster than they are recorded one by one, so the excess is summarised in one dead letter per tenant per minute. |
| `RateMeteringClockFallback` | Outbound actions have been metered on broker or arrival time for an hour because they carried no trigger time, so a catch-up can be shed as a flood again. Check that event-processing and outbound-connectors run the same release. A trigger time later than its message's broker time is counted separately, as source `capped`, and does not fire this: that is clock skew between the pod and the broker, not a missing time. |
| `DetectTenantOverStateBudget` | A tenant is over a ceiling that is not enforced — its rule count, its live windows and timers, or the readings its open windows retain. |

:::note An engine that loses a split-brain race exits
If two engines ever act as the writer at once, the one whose checkpoint is refused as stale stops
detecting, reports not-ready, and exits with a non-zero status so that it is replaced. It does not
stay up looking healthy. What it leaves behind is a restart of an event-processing pod, whose last
log lines say the checkpoint was refused as stale. If no replica holds the partition for two
minutes, `DetectHasNoLeader` fires.
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
