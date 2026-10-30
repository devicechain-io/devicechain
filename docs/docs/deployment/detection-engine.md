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
old and new replica can both dispatch for a short time. An alarm update or connector request sent
by both is stored once by the message bus, and a command carries a key that prevents a second
enqueue; each replica does charge its own outbound ceiling in that window.

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
waits. If the broker is briefly unreachable while the pod stops, the pod keeps trying to release
until shortly before its grace period ends. A broker outage longer than 30 seconds ends the hold
even of a running pod. Once the broker is back, the engine takes the partition again, waits out
the handover period and replays, because nothing records that it stopped cleanly.

| | What happens |
|---|---|
| **Events already processed and committed** | Nothing is lost. Messages are acknowledged only *after* the checkpoint that includes them commits, so anything not committed is redelivered. |
| **Alarms and commands already sent** | Re-derived and re-sent, then collapsed: an alarm is an idempotent update, and a command carries a key that prevents a second enqueue. |
| **Outbound webhooks and connector publishes already sent** | Collapsed too. A detection re-derived within 30 minutes is recognised by the message bus and never dispatched again, and a request the engine sends again within about ten minutes — a detection that was in flight when the pod stopped — is stored once. After a longer outage a request can be **sent again**; see the delivery section below. |
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
- An **outbound webhook or connector publish** that the engine sends again within about ten
  minutes — every retry of one detection — is recognised by the message bus and stored once. Past
  that, a request can still reach its destination twice: a replay after a long outage, or the
  connectors service retrying a call whose response it lost. Every request carries an
  `X-DC-Idempotency-Key` header derived from the firing, so collapsing that remaining duplicate is
  the **receiving endpoint's** job. For queue and broker targets the key travels as metadata, which
  most brokers cannot act on. **Design outbound receivers to be idempotent.**

When a downstream system is unavailable, the message is left unacknowledged and retried on a timer
rather than hammered. After five delivery attempts, roughly four minutes apart in total:

- an **outbound connector** request is **dead-lettered**, so it can be inspected;
- a **detection** some of whose actions still could not be dispatched is **dead-lettered too**,
  with a loud error. The letter's detail names each action that failed, or was refused by the
  outbound rate, on the final attempt, by kind and idempotency key. The detection's other actions
  were carried out, or deliberately skipped (guarded out, not enabled, or rejected as invalid).

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
While one of a detection's actions keeps failing, each retry charges its webhook and connector
actions against the tenant's outbound rate again, even though the message bus stores the re-sent
request once — so a sustained failure, such as a tenant at its held-command limit, can cause sheds
elsewhere in the same tenant.
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

:::note Each action is dispatched on its own
A rule's actions do not depend on each other. Every action is attempted on every delivery, so one
that keeps failing — a command for a device whose tenant is at its held-command limit, a webhook
whose endpoint is down — does not hold back the alarm or the rule's other actions. The same holds
for a service that does not answer at all: each attempt is limited to the time before the message
would be delivered again, and each action gets its share of what is left, so actions listed first
cannot use up the time of the ones after them. Actions that already succeeded are sent again on each
retry and collapsed as described above. There is no way to make one action conditional on another
one succeeding.
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
- **Threshold rules discard a reading that would turn them back.** While a threshold alarm is
  raised, a reading older than the newest one the device has sent is discarded and counted, whether
  it is inside the tolerance or far past it, such as a retried publish a minute late, so an older
  reading cannot clear an alarm that a newer reading keeps raised. While no alarm is raised, a
  matching reading is discarded and counted only if it is further behind than the tolerance and
  also was queued for detection more than 30 seconds after the platform received it, as a
  retried publish is (time the detection engine itself takes to read the queue, during a restart
  for example, does not count), so a retried publish cannot raise an alarm that newer readings had
  cleared. Any other matching reading is evaluated, so a device whose readings are dated more than
  the tolerance behind the rest, such as one whose clock runs slow or one uploading readings it
  stored during an outage, raises a threshold alarm from them as they arrive. Readings that arrive
  in timestamp order are evaluated the moment they arrive, so this adds no delay. Two trade-offs
  follow. A matching reading that arrives out of order still raises, even if a newer reading had
  already cleared the alarm, when it is within the tolerance or reaches the platform afresh, such as
  a reading the device sends a second time; the device's next reading clears it again. And a
  reading the platform held for more than 30 seconds before queuing it for detection, during a
  backlog in ingest or resolution for example, is treated like a retried publish: if it is a matching reading further behind than the
  tolerance, it is discarded and counted. The device's newest reading is remembered only while its
  alarm is raised, and only a reading up to ten minutes older than it is discarded: an older one is
  evaluated as it arrives, so a device whose clock is set back by more than ten minutes, by a
  time-zone correction for example, does not keep its alarm raised until its clock catches up.
- **Count-window and rate rules place a reading by its own time**, the way duration rules do. A
  rate is the change between consecutive readings and a count window is every N consecutive
  readings, so a reading that arrives out of order is slotted where its timestamp says, and the
  rule evaluates a reading only once the frontier has passed it — which is also why these two
  kinds raise and clear their alarms one lateness tolerance behind the newest reading. When no
  newer reading arrives — the last reading before a fleet goes quiet — it is decided on wall-clock
  time once the engine has been idle, as an absence rule fires, so with shipped defaults about
  fifteen seconds after it arrived; with `idleAdvanceGuardSeconds` negative it waits for the next
  event. A reading
  older than the newest one the rule has already settled is discarded: the pair or window it
  belonged to has already been decided. A reading that does not meet the condition still ends a
  raised rate alarm however late it arrives, unless it is older than the alarm.

In every case the reading is **stored and charted normally**; this is a detection-only effect.

Inside the tolerance an out-of-order reading is folded in as normal, which is what the tolerance is
for. The sliding kinds read their window as of the **newest reading** the device has sent (for
correlation, the newest sighting in the area), whatever order the readings arrived in. A late
reading therefore counts only alongside readings that were inside one window with it at that
moment, and an alarm it raises or clears is stamped at that newest reading, never earlier than an
alarm change the rule has already reported. After a burst of out-of-order readings, the alarm is
raised or clear as the readings imply in timestamp order. Exactly when inside the burst it changed
is approximate. For a sliding aggregate one case waits: when a late reading would clear an alarm
that a newer reading raised, the clear is held until readings up to one tolerance newer have had
time to arrive. If one of them breaches again, the alarm stays raised instead of clearing and
raising again a moment later; otherwise it clears, stamped at the newest reading. A late reading too old for that window no longer bears on the alarm, and it is not
counted as discarded.

The sliding kinds, duration rules, threshold rules and the count-window and rate rules **count
what they discard**, once for each rule that discards a reading. `detect_late_samples_total` rises every time
a reading arrives after the window it belonged to has passed, when a duration rule discards a
matching reading further behind the frontier than its hold time, when a count-window or rate rule
discards a reading older than one it has already settled, when a threshold rule discards a reading
older than the newest one of a device in alarm, or a retried matching reading too far behind from a
device that is not, and when a reading that does not meet a
duration rule's condition arrives after its alarm was raised but was taken inside the run that
raised it, before the raise.
A late reading older than the whole run is ignored and not counted: the run began after it. A
fleet whose rules have gone quiet therefore has something to look at rather than silence; a store-and-forward upload is the usual cause. Tumbling-window and session rules discard
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

The shared frontier also applies to **device clocks**. A device whose timestamps consistently trail
the rest of the fleet by more than a duration rule's hold time plus the lateness tolerance, whether
from a slow clock or a slow path to the platform, never raises that rule: every reading of it that
meets the condition is discarded as late and counted on `detect_late_samples_total`. Correct the
device's clock, or give the rule a hold time longer than the lag.

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

### How quickly does a detection reach its actions?

For a **replay-deterministic rule**, a threshold or repeating rule with a fixed bound, the detection
reaches the actions of its rule (raise an alarm, send a command, call a connector) within about a
second of the engine deciding it (up to one tick), plus the time the reading took to reach the
engine. The engine hands such a detection to the broker on its next one-second tick without waiting
for a checkpoint, and the checkpoint that commits the engine's state and acknowledges the stream
follows on its own schedule (`checkpointIntervalSeconds`, `checkpointEvents`). Publishing early is
safe for these rules because a restart before the checkpoint replays from the last one and derives
the same detections again, which the derived stream collapses to one, exactly as it does when a
checkpoint's snapshot fails after its publish. If the broker rejects a publish, detections fall back
to the checkpoint until it recovers.

Everything else still waits for the checkpoint, up to `checkpointIntervalSeconds` (10 seconds by
default) after the reading:

- Rules with a **dynamic (attribute-based) threshold**, and the other rule kinds that are not
  replay-deterministic. Publishing one early and then restarting could leave an alarm raised that a
  replay never clears, or raise a second one at a different time, which is the
  [caveat above](#what-a-restart-costs) in another form.
- Absence, duration, session, aggregate, rate and count-window rules, and rules over a correlation
  or connectivity edge. They are decided by event time, as described under "Timing" above, and
  some are judged against the device roster or the active version at the moment they are published.

An alarm's `raisedTime` is the time of the *reading* that raised it, not the moment the alarm was
written, so a command's `queuedTime` minus the alarm's `raisedTime` measures the whole path from
the reading, not a gap between the alarm and the command: the two are written within milliseconds
of each other. Earlier releases held every detection until the checkpoint, which added anywhere from
nothing up to `checkpointIntervalSeconds` between a reading and its alarm and commands.

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

Four more causes worth checking:

- **An operator "clear" does not remove the underlying condition.** If the condition is still true,
  the next event re-activates the same alarm. Clearing is an acknowledgement that you have seen it,
  not a suppression.
- **A device that leaves a rule's scope and then goes silent** keeps its raised alarm. Scope changes
  take effect on the device's next event (on every `device-management` replica; a replica that missed the change notice catches up when its cached copy expires, within five minutes by default), and a silent device has no next event.
- **The rule that raised it no longer runs.** A rule that is skipped at load — the profile's
  **Rule Health** tab shows it as a compile error — is not evaluated, so nothing resolves the alarm
  it raised before. Clear the alarm by hand once you have fixed or retired the rule.
- **A silence (absence) alarm that is still raised after the device or rule changed.** Unlike the
  rule kinds above, an absence alarm is cleared for you when its device leaves the rule's scope: the
  device is disabled, deleted or moved to a device type with a different profile, or a republish removes the
  rule. A republish that **keeps** the rule leaves the alarm raised, with its acknowledgement, rather
  than clearing it and raising it again when the new silence window ends.

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
   event as it is resolved, so a device that has just been added joins on its next event (on every `device-management` replica; a replica that missed the change notice catches up when its cached copy expires, within five minutes by default).
5. **A dynamic threshold has no attribute set on that device.** A threshold built on the form reads
   the device's own attribute, and a device with no numeric `SERVER` or `SHARED` value for it does
   not fire. A value that is not a number, or one set with `CLIENT` scope, counts as not set. A CEL
   expression with a [fallback](../concepts/event-processing.md#dynamic-thresholds-in-cel) fires on
   its fallback instead.
6. **The rule errors at evaluation time.** This is the hard one — see below.
7. **A change had not reached the engine yet.** A publish, rollback, device or attribute change
   the engine was not told about is picked up by its next comparison with device-management (see
   below).
8. **The rule stopped compiling after an upgrade.** An upgrade can refuse a rule that an earlier
   version accepted. Such a rule is skipped when the engine loads it, and the profile's **Rule
   Health** tab shows it as a compile error with the reason. The release notes list each such change.

:::note A lost change notification is repaired, not lost
Publishing or rolling back a profile, creating or re-typing a device, and setting a threshold
attribute each notify the detection engine once. If that notification is lost — the broker was
unavailable at that moment, or device-management restarted right after the change — the change
itself still stands. The engine also compares its copy of every tenant's published rules, active
profile versions, devices and threshold attributes with device-management when it takes over
detection and every five minutes after, and corrects whatever differs. A lost notification
therefore delays a change by a few minutes — no more than about seven, and a deleted device or
attribute no more than about ten — rather than losing it. You do not need to publish again.

`DetectFactsRepaired` tells you a correction was made. `DeviceFactPublishFailing` tells you
notifications are failing to send. `DetectFactReconcileFailing` tells you the comparison itself is
failing, in which case a missed change stays missed until it succeeds.
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

The other direction is fixed, not configured: a reading dated more than **366 days** before the
platform received it is refused, and is not stored or evaluated anywhere. The two directions are
treated differently on purpose. A clock running ahead would otherwise freeze shared live state, so
its time is capped; a time far in the past would otherwise create storage for a period nobody
recorded, so the reading is refused rather than moved. See
[how far back a reading may be dated](../guides/connecting-a-device.md#payload-shapes).

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
| `watermarkLatenessSeconds` | 5 | How long to wait for out-of-order events before treating a moment as settled. **Raise this** if events arrive in batches or an upstream hop can stall; it is the main defence against a false absence alarm. It also tolerates the small reorder between one device's events that resolution introduces, which grows with the number of `device-management` replicas: a windowed rule still counts an event that arrives within this margin, duration, count-window and rate rules place it by its own time, and a threshold rule in alarm discards a reading older than the device's newest, while one not in alarm discards a matching reading that arrives later than this margin only when it was retried or held up on its way. It does not cover an event whose publish failed and was retried, which arrives at least 60 seconds late. |
| `idleAdvanceGuardSeconds` | 5 | How long the engine must be quiet before it will fire a rule on wall-clock time. A negative value turns that path off: absence rules then fire only when a *later event* moves event time past their deadline, so a device that goes silent and stays silent never raises one. Count-window and rate rules likewise decide the last reading before a quiet spell only when a later event arrives. |
| `detectShards` | 1 | How many ways the engine's state is split, by each rule's series (a device, or the anchor for a correlation rule), from 1 to 64 (anything else is refused at startup). Detections, the event-time clock and the saved checkpoint are identical at every value, so it can be changed at any restart, up or down, with nothing to migrate. In this release the shards are processed one after another, so a value above 1 does not make detection faster yet: leave it at 1 unless you are measuring. The value in force is the `detect_shards` metric. |
| `checkpointEvents` | 1000 | Maximum events processed between checkpoints. |
| `checkpointIntervalSeconds` | 10 | Maximum time between checkpoints, so a quiet stream still commits. **At most 30**: a checkpoint is what acknowledges the stream, so an interval near or past the broker's 60-second acknowledgement window makes messages on a quiet stream redeliver. 30 leaves room for the checkpoint itself, and the service refuses anything longer at startup. |
| `checkpointTimeoutSeconds` | 10 | How long one checkpoint call (publishing detections, saving the snapshot) may take before it is abandoned and the checkpoint counts as failed, instead of hanging detection on a database or broker that has stopped answering. At most 120. The snapshot save's limit grows by itself, to four times the last save that completed (up to two minutes), so a large engine on a slow database is not stuck failing; raise this only when the very first save is slower than the value. |
| `maxRuleDurationSeconds` | 86400 | Longest time span a rule may declare — window, hold, silence timeout or session gap. **Enforced**: a longer rule is refused at publish. See below. |
| `maxRulesPerTenant` | 500 | Per-tenant rule ceiling. **Measured and reported, not enforced** — see below. |
| `maxLiveKeysPerTenant` | 1000000 | Per-tenant ceiling on live windows and timers. Also measured, not enforced. |
| `maxRetainedSamplesPerTenant` | 5000000 | Per-tenant ceiling on readings held inside open windows. Also measured, not enforced. |
| `outboundCallsPerSecond` | 100 | Per-tenant rate at which outbound connector actions are dispatched, metered on the time the triggering telemetry reached the platform. |
| `outboundBurst` | 200 | Burst allowance for the above. |
| `shedLetterPerSecond` | 1 | Per-tenant rate at which shed outbound actions are recorded as individual dead letters. |
| `shedLetterBurst` | 60 | Burst allowance for the above. |
| `shedLetterGlobalPerSecond` | 10 | The same, across every tenant. Shed actions past either budget are counted and summarised in one dead letter per tenant per minute. |
| `shedLetterGlobalBurst` | 100 | Burst allowance for the above. |
| `ruleHealth.flushIntervalMillis` | 1000 | How often the per-rule fire counters are written to the database, from 50 to 60000. It is roughly how stale the console's fire counts and last-fired times may be; detection never waits for it. |
| `ruleHealth.maxPendingKeys` | 4096 | Most rules that can have fires waiting to be written at once, from 1 to 1048576. A fire for a rule beyond this is not counted (see below). |
| `ruleHealth.maxPendingBytes` | 4194304 | Memory budget for those waiting fires, from 65536 to 268435456 (256 MiB). |
| `ruleHealth.maxBatchRows` | 256 | Most rules one tenant's write carries, from 1 to 4096; a tenant with more waiting is written in several transactions. |
| `ruleHealth.writeTimeoutMillis` | 2000 | How long one tenant's write may take, from 100 to 60000. A write that fails or times out is dropped, not retried. |
| `derivedPublishing.maxInFlight` | 32 | How many detections a checkpoint may have waiting for the message bus to acknowledge them at once, from 1 to 1024. Detections of different devices are published side by side, never more than one per device at a time. At 1, each is acknowledged before the next is sent. See below. |
| `derivedPublishing.maxBufferedBytes` | 4194304 | Bytes those detections may hold together while they wait to be published, counting ones kept for a retry after a failed attempt, from 65536 to 268435456 (256 MiB). A single detection larger than this is published on its own. |
| `react.workers` | 8 | How many derived events REACT dispatches at once, from 1 to 64. Events of one device are dispatched one after another, in the order they were detected, except that an event left for redelivery is dispatched again after the events behind it; only different devices overlap. When absent beside a connection pool smaller than 16 it is half the pool. A value above 1 must be below the pool size (`rdbConfiguration.maxOpenConnections`, default 20), because each worker may hold one connection while it reads the rule. At 1, events are dispatched one at a time, which caps REACT at one event per round trip to the database and the message bus. |

Each `ruleHealth`, `derivedPublishing` and `react` setting is optional; one that is given must be inside its
range (zero included) or the service refuses to start.

### Rule-health counts are approximate

The console's rule-health view shows each rule's fire count and when it last fired. Those counters
are not written as each detection is published: the engine adds each published detection to an
in-memory total per rule and writes the totals in the background, one transaction per tenant, every
`flushIntervalMillis`. A busy rule therefore costs one row write per interval rather than one per
detection, and a slow or unavailable database never slows detection down. The one place a write is
waited on is erasing a tenant: the purge waits for that tenant's write already in flight to settle,
for at most `writeTimeoutMillis` plus one second.

What that means for the numbers:

- **They lag.** On a healthy database a fire normally appears within the flush interval plus one
  write. Under sustained overload, or while the database is failing, no freshness is promised.
- **They can fall short.** A write that fails or times out is dropped rather than retried, because it
  may have committed and a retry could count its fires twice. Fires are also dropped when the
  waiting budget (`maxPendingKeys`, `maxPendingBytes`) is full, when a tenant is deleted, and when
  the engine stops or loses its leadership with fires still waiting (a graceful stop first makes one
  bounded final write). Every dropped fire is counted, by reason, in
  `devicechain_eventprocessing_detect_rule_stat_updates_discarded_total`.
- **They can run over.** After a restart the engine re-publishes detections since its last checkpoint,
  and those count again.
- **Only published detections count.** A fire is counted only after the broker has acknowledged its
  derived event; a detection that was dropped or is still waiting to be published is not.

The last-fired edge is the most recent one the counters saw, not the rule's current alarm state.
Use the alarm itself, or the detection feed, for that.

Accounting for the two budgets: a waiting entry is one rule with fires not yet written, wherever it
is (still collecting fires, set aside for the next write, or inside a write in progress), and its
size is reckoned as 160 bytes plus the length of its rule id, plus 320 bytes and the tenant's token
for a tenant's first entry. That overstates what it really holds, so the byte budget is a hard bound.

### Publishing several detections at once

A checkpoint publishes every detection it covers to the message bus, and saves the engine's state
and acknowledges the events behind those detections only once every one of them has been
acknowledged. With `derivedPublishing.maxInFlight` at 1, that is one detection at a
time, so a checkpoint covering many detections waits for as many acknowledgements one after another.
Where an acknowledgement is slow, for example on a replicated stream whose servers are far apart,
that wait is what limits how fast detections are delivered.

The default, 32, lets a checkpoint wait for several acknowledgements at once. Under a
firing-heavy load of about 8,000 events a second offering about 2,500 detections a second, a window
of 32 kept up (7,658 events and 2,441 detections a second, with the watermark about 5 seconds
behind), while a window of 1 fell steadily behind (1,656 events and 528 detections a second, with
the watermark more than two minutes behind). Set `maxInFlight` to 1 to publish strictly one at a
time. What stays the same at any setting:

- **Each device's detections arrive in order.** A device's next detection, from any of its rules, is
  sent only after the one before it has been acknowledged, so at most one per device is ever
  waiting. A correlation rule's detections count as its anchor's. Detections of different devices
  may arrive in a different order than at 1; nothing downstream depends on that order, because
  every alarm belongs to one device.
- **Nothing is committed ahead of its detections.** The checkpoint is saved only after every
  detection it covers has been acknowledged, exactly as at 1.
- **A failure is retried without duplicates.** When a publish fails or times out, no more are sent,
  the checkpoint is not saved, and the next attempt sends the same detection with the same identity.
  The message bus recognises a copy it already stored, so a timed-out publish that did arrive is
  stored once, provided the retry arrives within the derived-events stream's duplicate window
  (30 minutes). That is a bounded dedup window, not a permanent exactly-once guarantee: a retry
  that comes later than that, after a long outage, can be stored a second time and is collapsed
  downstream by the detection's identity instead. Detections that were acknowledged are not sent
  again.
- **Rule-health counts are recorded in the same order**, whatever order the acknowledgements come
  back in.

It helps only when the `publish` phase is a large share of the loop's time (see
[where the loop's time goes](#loop-phases)) and the detections are spread over many devices. A
single busy device gains nothing, because its detections are still sent one at a time.
`devicechain_eventprocessing_detect_derived_publish_window_full_seconds_total` is the time
checkpoints spent waiting with another detection ready to send; if it is close to the time spent in
`publish`, a wider window can help. Leave the default unless you are measuring.

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

### How fast alarms are applied

Detection decides that an alarm should be raised or cleared; the `device-management` area then
applies each of those decisions to the alarm itself, and it does so several at a time. One alarm
change costs roughly ten round trips to the database, so applying them one after another tops out
at about a hundred a second on a local database however idle the service is, and a rule that fires
on many devices at once builds a backlog behind that ceiling.

| Setting (on `device-management`) | Default | What it does |
|---|---|---|
| `raiseAlarm.workers` | 8, lowered so that it and `resolution.workers` leave at least 4 connections of the pool free (6 with the default pool of 20), and never below 1 | How many alarm changes are applied at once, from 1 to one below `rdbConfiguration.maxOpenConnections` (20 unless set). Anything outside that range stops the service at startup, naming the setting. Each worker holds a database connection while it writes, so a value above half the pool is logged. An explicit value is checked on its own, not against `resolution.workers`, so choose the two together. The counts in effect are logged at startup. It is read once at startup. |

Within one replica, changes to **one** alarm are applied in the order they arrive, whatever the
setting; only changes to different alarms overlap. The result never depended on that order: a
decision that arrives after a newer one for the same rule is ignored, so a redelivered change, or
one applied by another replica, cannot undo a newer one. Setting it to 1 applies them one at a time.

The connection pool these workers draw from is also used by `resolution.workers` and by the API, and
each worker count is checked against the pool on its own rather than together, so leave room for
the other when you raise one. On a local Docker database (a directional figure, not a capacity
statement) eight workers applied four to seven times as many alarm changes a second as one.

## What to watch

| Signal | Means |
|---|---|
| `DetectCheckpointsStalledWithBacklog` | **The most important alert here.** Checkpoints have stopped while work is waiting: the engine cannot reach its database or the broker, or its loop is stuck. Detection is not happening. |
| `DetectLoopStalled` | The detection loop on the replica that holds the partition has stopped making progress for over two minutes, yet the replica still reports itself live. The loop is stuck inside a call, usually to a database or broker connection that has gone silent. Every other gauge on this page is read on that same loop, so they stay frozen at their last values and look healthy. Detection is not happening; restart the pod named on the alert. A standby never fires it. |
| `DetectConsumerBacklogHigh` | The engine is behind. Absence detection **on silence** is suppressed while it is — a later event still fires an overdue absence, as above. |
| `DetectWatermarkLagHigh` | The engine's sense of event time is falling behind real time. |
| `DetectFanoutEvalErrors` | One or more published rules are failing to evaluate. See the caution above. |
| `ReactPoisonDropping` | Some of a detection's actions failed on every delivery attempt — usually command-delivery or the message bus was unavailable for several minutes. Its other actions were attempted on every delivery; each dead letter names the ones that failed. Nothing replays them. Treat as urgent. |
| `DeadLetterWriteLost` | Something was given up on **and** could not be written to the dead-letter stream. Look at the broker, and at the service's log: a letter the service refused to write lands here too, and so does an inbound event whose failure record device-management could not encode (the event is acknowledged and nothing published says it failed), and so does a dead letter that the dead-letter store or the command writeback ran out of attempts on. |
| `DeadLetterStoreLosing` | Dead letters reached the stream but could not be written to the store, so they will age out of it unrecorded. Look at the operator database. |
| `ReactConnectorEgressShedding` | A tenant is over its outbound rate on the timeline its telemetry reached the platform, and its outbound actions are being shed. Each is dead-lettered with reason `shed`, within a budget; read them with `dcctl dead-letters`. A catch-up after a restart does not cause this. |
| `ReactShedLettersOverBudget` | A tenant is shedding outbound actions faster than they are recorded one by one, so the excess is summarised in one dead letter per tenant per minute. |
| `RateMeteringClockFallback` | Outbound actions have been metered on broker or arrival time for an hour because they carried no trigger time, so a catch-up can be shed as a flood again. Check that event-processing and outbound-connectors run the same release. A trigger time later than its message's broker time is counted separately, as source `capped`, and does not fire this: that is clock skew between the pod and the broker, not a missing time. |
| `DetectFactReconcileFailing` | The engine cannot compare its rules, profile versions, devices and thresholds with device-management. Nothing is removed while it fails, but a missed change stays missed until it recovers. |
| `DetectRuleStatWriteFailing` | Rule-health counters cannot be written. Detection is unaffected, but the console's per-rule fire counts fall short: each failed write is dropped, not retried, and the fires it held are counted in `devicechain_eventprocessing_detect_rule_stat_updates_discarded_total`. The error is in event-processing's log; check its database connection. |
| `DetectLiveGapFillFailing` | Live detection has stopped because part of the event stream cannot be read. The engine will not skip past events it has not seen, so it applies nothing new until the broker answers; the backlog grows meanwhile. Rule and tenant changes are still applied, and nothing is lost. Look at the broker and the engine's connection to it. If the same range still cannot be read after ten minutes, the engine restarts its detection term and re-reads it from the stream. If the stream is still unreadable, the rebuild fails too, and after five failed rebuilds the pod restarts: a JetStream outage longer than ten minutes, with the lease intact, ends in a pod restart. |
| `DetectLiveDeliveriesRecovered` | Events the broker handed out never reached the engine, and it read them from the stream and applied them in order. Detection is correct. Recurring means connections to the broker are dropping, or two engines are consuming at once. |
| `DetectFactsRepaired` | The engine corrected state it had not been told about. Detection is right again; recurring means notifications are being lost. |
| `DeviceFactPublishFailing` | device-management cannot send change notifications. Changes still reach the engine, a few minutes late. |
| `DetectTenantOverStateBudget` | A tenant is over a ceiling that is not enforced — its rule count, its live windows and timers, or the readings its open windows retain. |
| `DetectLeaderIsNotConsuming` | A replica owns the partition but is not inside a live term and has committed no checkpoint for ten minutes. It is stuck building a term, which holds the partition against every standby while nothing is detected. A long replay does not trigger it, because a replay checkpoints as it goes. Read the replica's log around its term build and restart that pod so a standby can take over. |
| `GeoFenceSetPublishFailing` | device-management could not publish a geofence-set manifest: a marshal error, a broker refusal or a transport fault. The fence edit itself succeeded and is durable; what failed is telling the engine, so containment keeps the previous fence set until the five-minute reconcile sweep repairs it. A sustained rate means every fence edit takes minutes to take effect. If it began after a broker configuration change, check that `infrastructure.nats.streamMaxMsgSize` is not set below what a manifest needs; device-management logs a warning naming both sizes at startup. |
| `DetectFenceArchiveSkew` | The engine asked device-management for a fence-set manifest and was told the query does not exist, so the two services run builds from either side of the manifest-delivery change. Expected briefly while an upgrade rolls, because the two Deployments do not wait for each other. Sustained means device-management is behind, most likely rolled back on its own, and fence edits do not reach the engine until it rolls forward. |
| `DetectFenceGeometryUnresolved` | A geofence set names fences whose geometry the engine could not fetch from device-management's archive. Those fences are installed carrying their error, so containment reports them unresolvable instead of answering, which also shows in `DetectFanoutEvalErrors`. It repairs on the next reconcile sweep if the cause was transient; sustained means the archive read is failing, so check device-management's health and its service-token connection. The tenant and fence are named in the engine's error log, never in the counter. |
| `DetectFenceGeometryHashMismatch` | **Critical.** A geometry document did not hash to the address it was requested under. An address is the SHA-256 of the document stored under it, so this is never transient and never benign: device-management served the wrong row, something re-encoded the document in transit, or the archive is corrupt. The affected fences are refused rather than compiled, so containment degrades instead of answering wrongly. Investigate before restarting anything; a restart hides the symptom and changes nothing. |

:::note An engine that loses a split-brain race exits
If two engines ever act as the writer at once, the one whose checkpoint is refused as stale stops
detecting, reports not-ready, and exits with a non-zero status so that it is replaced. It does not
stay up looking healthy. What it leaves behind is a restart of an event-processing pod, whose last
log lines say the checkpoint was refused as stale. If no replica holds the partition for two
minutes, `DetectHasNoLeader` fires.
:::

Per-rule status, last-fired time and fire count are available in the console on the device profile's
**Rule Health** tab, alongside a live feed of detections as they occur.

### Where the detection loop's time goes {#loop-phases}

When the engine is behind (`DetectConsumerBacklogHigh`), the question that decides what to do about it
is what the loop is spending its time on. `devicechain_eventprocessing_detect_loop_seconds_total`
answers it: the seconds the loop has spent in each phase, labelled `phase`. The phases divide the
loop's time between them, so the rate of one is its share of wall time, and the phases other than
`fetch_wait` adding up to 1 means the loop has no spare time at all. (`parked` and `probe` are waiting,
not work, but they are time the loop could not spend on messages.)

| `phase` | The loop is |
|---|---|
| `fetch_wait` | idle, waiting for the next message or tick. |
| `decode` | reading a message: the sequence check, the tenant and the payload. |
| `plan` | working out which rules a reading feeds and evaluating their conditions. |
| `apply` | running the detection engine on the result. |
| `publish` | handing a checkpoint's detections to the broker. |
| `save` | writing the engine's state to the database at a checkpoint. |
| `ack` | acknowledging the messages a checkpoint made safe. |
| `gapfill` | reading a range of events the broker said it delivered and the engine never received. |
| `control` | everything between messages: rule, device and threshold updates, tenant deletions, and the periodic housekeeping. |
| `parked` | waiting with live messages switched off, either behind a wall-clock advance that could not be committed or behind a gap in the event stream it could not read. It is the engine refusing work until a retry succeeds. |
| `probe` | waiting on the broker for its backlog count, which the engine asks for to decide whether it is caught up and to report its lag. |

A backlog with most of the time in `fetch_wait` is not the engine's limit: it is waiting for messages
to arrive, and more engine capacity will not help. Time in `parked` or `probe` is the same kind of
waiting: on a retry or on the broker, not on the engine. Most of it in `plan` and `apply` means the engine itself is the
limit. Most of it in `publish`, `save` and `ack` means checkpoints are the cost, and
`devicechain_eventprocessing_detect_checkpoint_duration_seconds` shows how long each one takes.
(`devicechain_eventprocessing_detect_checkpoint_seconds` keeps only the latest.) Two pairs of
distributions say which part of a checkpoint it is. Time in `publish` is mostly one wait per
detection: the broker acknowledging the derived event
(`devicechain_eventprocessing_detect_derived_publish_ack_seconds`). The rule-health counters are
written by a separate background writer, so their database time is not in any of these phases; it
is `devicechain_eventprocessing_detect_rule_stat_write_seconds`, one observation per tenant write. With
`derivedPublishing.maxInFlight` above 1 the acknowledgement waits overlap, so `publish` is less than
their sum; `devicechain_eventprocessing_detect_derived_publish_pass_seconds` is each checkpoint's
whole publish. Time in `save` is
serializing the engine's state (`devicechain_eventprocessing_detect_checkpoint_serialize_seconds`)
and then writing it to the database (`devicechain_eventprocessing_detect_checkpoint_save_seconds`).

```promql
sum by (phase) (rate(devicechain_eventprocessing_detect_loop_seconds_total[5m]))
```

## Deleting a tenant

The detection engine holds tenant state as an opaque checkpoint that no query can interpret, so a
[tenant deletion](./tenant-deletion.md) asks the engine directly to evict it and waits for the
engine to confirm that the eviction has been **committed** — not merely applied in memory. An
instance running detection must therefore have the engine reachable for a deletion to complete; if
the engine is halted or unreachable, the deletion stays open rather than completing over data that
is still there.
