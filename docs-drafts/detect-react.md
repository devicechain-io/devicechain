---
title: Detection and Response (DETECT + REACT)
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-051, ADR-052, ADR-053, ADR-054, ADR-056, ADR-057, ADR-060, ADR-061, ADR-062, ADR-023, ADR-042, ADR-043, ADR-044, ADR-045, ADR-059, ADR-077]
---

# Detection and Response (DETECT + REACT)

A device event becomes an alarm — or a command, or a webhook — by crossing seven component
boundaries, and no file holds the crossing. Each end documents itself thoroughly and none of them
documents the path.

Two properties are load-bearing everywhere below, and almost everything unusual in the design is
paying for one of them:

- **Replay-correctness.** The engine evaluates on *event time* and checkpoints its state, so a
  restart re-derives the same firings from the same stream. This is why there is a watermark rather
  than a wall clock, why the checkpoint is one row, and why the engine takes ownership handoffs
  instead of locks.
- **The engine is the one store nothing can query.** Every other subsystem's state can be read back
  out of Postgres. DETECT's lives in process memory, serialized as an opaque blob. That single fact
  drives the single-writer loop, the deliver-before-checkpoint order, and the request/reply protocol
  used to evict a tenant.

Every claim below names the file it came from.

## The shape, end to end

```
device → transport → event-sources → inbound-events stream
                                          │
                          device-management: event resolution
                          (device, profile version, anchors, group memberships)
                                          │
                                  resolved-events stream
                                          │
                    ┌─────────────────────┼─────────────────────┐
              event-management       device-state        event-processing
              (persist)              (projection)        (DETECT)
                                                              │
                                              one goroutine, in stream order
                                                              │
                                        fan out: 1 message → N per-rule events
                                                              │
                                        keyed streaming engine + timer wheel
                                                              │
                                          rising / falling edge detections
                                                              │
                                    ── publish BEFORE checkpoint, ack AFTER ──
                                                              │
                                                  derived-events stream
                                                              │
                                            REACT (a separate consumer)
                                                              │
                              ┌───────────────┬───────────────┴──────────┐
                        raise-alarm      createCommand            connector-dispatch
                              │           (GraphQL)                      │
                     device-management                            outbound-connectors
                     alarm object                                 httpCall / publish
                              │
                       alarm-events → notification-management → a human
```

The three arrows worth staring at: DETECT publishes to a stream that DETECT's own sibling consumer
reads (REACT is not a function call); the publish happens *before* the checkpoint and the ack
happens *after*; and every sink is reached across a boundary, so none of them can block the engine
except by refusing an ack.

## 1. The input — what a resolved event is

Resolution happens in device-management, not here.
`backend/services/device-management/processor/event_resolver.go:703` drains inbound events, derives
the tenant from the subject and fails closed if it cannot (`:720-725`), and produces a
`ResolvedEvent` (`backend/services/device-management/model/events.go:89-122`) carrying: the source
device token (by token, never a row id), the anchors from the relationship graph, the
`ProfileVersionToken` in the form `{profileToken}@{version}`, the occurred and processed times, the
event type, the payload — and `ScopeMemberships`, the dynamic-group stamp described in §8.

Two resolution behaviours matter downstream. A device with no tracked relationship resolves
**anchorless rather than being dropped** (`event_resolver.go:486-489`). And a device that does not
exist is synthesized into an explicit error (`:675-691`) that is treated as *retryable* — redelivered
until the delivery cap, then dead-lettered (`:755-764`). The comment at `:680-686` records why the
synthesis exists at all: without it the resolver nil-dereferenced and crash-looped.

`ProfileVersionToken` is **empty when the device type has no profile or its profile is unpublished**
(`events.go:94-102`), and an empty value matches no rules at all
(`backend/services/event-processing/internal/runtime/registry.go:226-232`). A silent "my rule never
fires" is far more often this than a rule bug.

Publication is 1→N with a coordinated ack, stated as the contract at
`backend/services/device-management/processor/inbound.go:174-176`: `:209` publishes each resolved
event, and the source message is acked (`:239`) only after the last event it produced is durable. A
publish failure latches instead, so the source is never acked and the whole message redelivers
(`settleResolved`, `:226-241`).

The stream is `resolved-events` (`backend/core/streams/streams.go:176`), subject
`{instance}.{tenant}.resolved-events`, file storage, 7-day age limit
(`backend/core/messaging/nats.go:566-574`). **It declares no dedup window** — contrast
`inbound-events`, which declares 1800s (`streams.go:322`).

## 2. The spine — one goroutine, and everything else marshals into it

`backend/services/event-processing/processor/ResolvedEventsProcessor.go` is the whole service in one
file, and its organising principle is stated at `:104-106`: the engine is owned by exactly one
goroutine at a time, by **handoff**, not by a lock.

Startup runs a strict sequence (`:417-557`) whose order is load-bearing: restore the snapshot →
drain the roster / entity-deleted / published-rule projections to head → start the attribute view →
load dead-man membership → **replay to head** → fail startup if the checkpoint is already stale →
fail startup if idle-advance is on but the reader cannot report backlog → reconcile the three
in-memory views → *then* launch the loop.

Replay reads **in order** from `LastSeq()+1` to the head captured at open (`:911-957`) rather than
consuming live, because a durable's post-crash redelivery is not ordered and the engine's
sequence guard would permanently discard an event it had never applied (`:90-99`).

Five other goroutines exist and **none of them touch the engine**. Each sends onto a channel that the
loop selects over: the read pump (`:1091`), the rule consumer (`:1581`), the roster and
entity-deleted consumers, and the attribute consumer.

The tenant-purge responder looks like a sixth and is not one. It is a plain core-NATS `Subscribe`
whose callback runs **inline** on the subscription's own serial dispatch
(`backend/services/event-processing/processor/tenant_purge.go:289-306`), and the file argues at
length that spawning a goroutine per request — the shape the device-callout responder uses — was the
rejected design: nothing here is independent, every request contends for the same loop, and each one
that reaches it runs a full sweep plus a synchronous commit with detection stopped meanwhile. It is
still the only sender that carries a **reply** channel (`ResolvedEventsProcessor.go:1013`), because
it is the only one whose caller needs an answer.

The side-channel consumers share one durability primitive, `persistBeforeAck`
(`backend/services/event-processing/processor/roster_consumer.go:60`): retry the write until it
commits or the service stops, leaving the fact unacked throughout — deliberately defeating the
delivery cap, because these facts are not replaceable. They then signal only the **identity** of what
changed, never a snapshot of it (`:30`), and the loop re-reads the authoritative projection
(`ResolvedEventsProcessor.go:1452`). That is what makes two goroutines over reorderable streams
converge.

## 3. Time

This is the replay-correctness heart, and it is smaller than it looks.

**The engine holds no clock.** `internal/detect/core/engine.go` never calls one; the clock lives on
the processor's config and reaches the engine only as the argument to `Advance(now)`
(`ResolvedEventsProcessor.go:1334`). During replay nothing calls `Advance`, so replay is clock-free
by construction.

**One global watermark, not one per key.** `internal/detect/core/watermark.go:30-37` — `observe(t)`
moves the frontier to `max(now, t - lateness)` and never backward. The comment at `:16-21` records
that a single frontier across every key and every tenant is deliberate: an idle key's absence timer
has to fire off *other* keys' progress, since by definition nothing is arriving for it.

Event time is clamped on the way in: `EffectiveEventTime`
(`backend/services/event-processing/internal/runtime/input.go:96-104`) caps a device-reported time at
`processed + maxSkew`. Both times are immutable in the payload, so the clamp is deterministic under
replay. **Only future skew is bounded** (`:87-95`) — a device reporting a time far in the past is
handled as lateness, not as skew.

`advance` (`engine.go:720-728`) does three things in an order that matters: move the watermark, fire
due timers, close due panes. Timers fire against the **pre-event** state.

**What happens to a late event depends on the rule kind**, and the variation is intentional rather
than accidental:

| Kind | Late event | Anchor |
|---|---|---|
| Aggregate | dropped — its pane has closed | `internal/detect/core/window.go:250-252` |
| Session | dropped — its session has closed | `internal/detect/core/session.go:27-29` |
| DeltaRate | skipped; the stored previous value is not rewound | `internal/detect/core/state.go:39-44` |
| SlidingAgg | accepted — binary-search insert at the sorted position | `state.go:89-102` |
| Correlation | accepted, but never regresses a member's timestamp | `internal/detect/core/correlation.go:44-47` |
| Repeating | accepted; eviction is by event time, not watermark | `window.go:203` |
| Absence, Session timers | cannot *shrink* a deadline (`scheduleForward`) | `internal/detect/core/timers.go:103-108` |
| a **value-kind** falling edge | refused if it predates the raise | `engine.go:954-961` |

The last row is the one to state narrowly, because the tempting generalisation — "a falling edge that
predates its raise is always refused" — is wrong in both places it matters, and deliberately so.
`resolve` drops a stale falling edge because an older reading is not evidence the condition ended;
the latest reading still supports it (`:948-953`). Two paths that are *not* value readings therefore
clamp to `max(at, raisedAt)` instead, so they can never be refused:

- **Descope** (`engine.go:436-442`, argued at `:430-435`, pinned by
  `TestDescopeLateStillResolvesAtRisingEdge`). Membership is stamped at resolution and is monotone
  with stream sequence, so a bounded-late out-of-scope event is still the current word on membership.
  Suppressing its resolve would strand the alarm raised forever if the device never reported again.
- **Connectivity CONNECT** (`engine.go:844-848`). Presence ordering is session-dominant: a failover
  reconnect mints its session on another host's clock, so a resolve can legitimately carry an
  *earlier* wall clock than the raise it clears.

**A documented silence gap.** `engine.go:143-171`: the event-driven kinds only re-evaluate when an
event arrives, so a series that goes **completely silent while raised stays raised**. Two kinds carry
an additional inherent residual under *non-matching* traffic, and they carry it to different degrees
— the summary at `engine.go:166-168` groups them, and the per-kind notes are where the difference
is. A **CountWindow** rule cannot observe a falling edge from non-matching traffic at all: its window
is counted in matching events with no time axis, so "closed" is definitionally *Count* more matches
(`window.go:270-276`). A **Session** rule is weaker than that summary suggests: an open session
always closes via its gap timer even under non-matching traffic, because the watermark still
advances, so an in-flight breach is always evaluated (`session.go:15-22`). What Session cannot do is
*re*-evaluate — only a new session that closes unsatisfied resolves a raised one, and a device
reporting only non-matching values opens none. The mitigation stated in all three places is to pair
the rule with an Absence rule. This is the single most useful thing to know before believing an alarm
is stuck.

## 4. The engine's state, and why eviction is a predicate

One `Engine` per partition (`engine.go:206-235`) holding the rule set, a timer wheel, the watermark,
the last applied sequence, ten `SeriesKey`-keyed state maps, the aggregate panes and their close
heap, and an un-drained emission buffer. `SeriesKey` is `{Rule, Series}` (`timers.go:15-18`); the
tenant rides as the rule-id prefix rather than as a field.

Eleven rule kinds dispatch through one switch (`engine.go:745-800`). It is preceded by a second,
smaller switch over the same `Kind` (`:739-744`) that drops a non-finite measurement before it can
reach serializable state — a single NaN in a running sum makes every later snapshot fail to marshal,
which halts checkpointing while the engine keeps running. Firing is **two-edge and latched**: `emit`
sets `raised[key]` and is a no-op while already raised; `resolve` clears it and emits the falling
edge. Detection identity is `(RuleID, Series, Kind, At, Edge)` (`:173-199`) — the value is
informational and deliberately *not* in the identity.

Eviction is `RemoveMatching(func(ruleID string) bool)` (`engine.go:347-390`) rather than "look up the
rules, remove each", and the reason (`:328-335`) is the kind of thing only a traversal surfaces:
**state can outlive the rule that created it.** A restore reloads dead-man armings for rules the
restored set no longer contains, and a rule removed with a pane close still pending leaves that close
item behind by design. A predicate over the id catches what an enumeration of live rules cannot.

## 5. Durability — what a restart costs

The checkpoint is **one row** per partition (`backend/services/event-processing/model/model.go:30-48`)
holding the sequence, the watermark and the serialized state, so state and position commit
atomically. The watermark is denormalized out of the blob purely so an operator can graph lag.

Serialization is made deterministic on purpose — every keyed slice is sorted before marshalling
(the shared primitive is `sortByRuleSeries`, `internal/detect/core/state.go:247-256`; `Snapshot`
applies it or an inline equivalent to every map it walks, `engine.go:1042-1103`) — and one
value is restored **verbatim rather than recomputed**: a sliding aggregate's running sum
(`state.go:288-293`), because float addition is not associative and re-deriving it could flip a
threshold sitting exactly on the boundary.

**Pending detections are deliberately NOT serialized** (`engine.go:1036-1041`). The contract is
deliver-before-checkpoint: `checkpoint` (`ResolvedEventsProcessor.go:1720`) publishes every buffered
detection **first**, then commits the snapshot, then acks (`:1773-1780`). Any failure leaves messages
unacked to redeliver, with nothing committed.

`SnapshotStore`'s save (`backend/services/event-processing/model/snapshot_store.go:46-84`) locks the row
and refuses two things: a lower sequence, and **an equal sequence with a lower watermark** — the
second exists specifically to catch a split-brain peer that idle-advanced.

So, precisely, what a restart costs:

- **Committed events: nothing.** Acks follow the commit.
- **Already-published detections: re-emitted, not lost** — collapsed downstream on the identity above.
- **Per-rule fire counts: over-counted.** `RuleStat`'s last-fired stamp advances monotonically, but its
  `FireCount` increments on every replay (`backend/services/event-processing/model/rule_stat_store.go:45-47`,
  `:86-88`). Documented and accepted.
- **An idle-advanced watermark is not replayable** (it carries no sequence). Guarded by
  commit-before-event: if that checkpoint cannot commit, the loop **stops receiving live messages
  entirely** by nilling its own channel (`:976-987`). Receive-and-skip was rejected because a skipped
  unacked message redelivers, is guard-dropped, and is acked — silently lost (`:978-983`).
- **🔴 Dynamic thresholds are not replay-deterministic.** `internal/runtime/derived.go:80-88` and
  `ResolvedEventsProcessor.go:1698-1704`: the dedup identity omits the resolved threshold, and replay
  resolves an attribute against its **current** value rather than its value at the original event
  time. A detection that fired but was still buffered at a crash can be silently lost; one that did
  not fire can be re-derived and published at the old event time with nothing to collapse against.
  Static-threshold rules are unaffected. This is the arc's one known correctness gap.

## 6. The rule — authored in device-management, projected into event-processing

The **authored** rule is a child of the device profile
(`backend/services/device-management/model/model_detection_rules.go:36-86`) and device-management
never parses its taxonomy. What is opaque is the **`Definition` column**, and only that: the write
checks it is valid JSON and a JSON object — `{}` passes, `null` is rejected explicitly because it
unmarshals into a map without error (`backend/services/device-management/model/api_detection_rules.go:28-54`).
The rule's *other* columns are not opaque and are gated at the same write
(`CreateDetectionRule`, `:84-130`): the token against the platform grammar, the optional canvas
sidecar as a JSON object, and the group scope through `validateDetectionRuleScope` — a genuine
cross-entity read that re-compiles the frozen selector. Only the taxonomy inside `Definition` defers
to publish.

At publish, `backend/services/device-management/model/api_profile_versions.go:166-264` snapshots the
version and validates **the exact bytes about to be frozen** (`:95-126`) — no gap between what was
checked and what was stored. Validation is a synchronous call into event-processing, and it **fails
closed on a transport error** (`:109-116`). It **fails open when unwired**, which is worth knowing:
if the service secret or the event-processing coordinate is missing the gate returns nil immediately
(`:96-98`) and a profile publishes with no rule validation at all. The gate also compiles **only the
enabled rules** (`:132-145`), deliberately — a parked, still-WIP rule must not block shipping the
rest — with the consequence that a disabled rule rides the frozen snapshot having been compiled by
nothing, and is first gated when a later draft enables it.

The **projection** is a separate durable table (`backend/services/event-processing/model/model.go:70-96`)
keyed on the composed `{tenant}/{profileVersionToken}/{ruleToken}` id, with tenant, profile version
and rule token denormalized so a load parses no ids. It is the restart source of truth precisely
because the fact stream has a 7-day retention (`:56-63`, the figure at `:58`; the authority is
`backend/core/messaging/nats.go:24-28` and `:572`, where every stream takes the same `MaxAge`).

The compiler is `backend/services/event-processing/internal/rules/compile.go:167-234`. Its posture:
limits are floored before anything else so **compile can never run uncapped** (`:168`); each rule
type forbids every field it does not read (`internal/rules/validate.go:30-57`), returning the first
forbidden non-zero field; the leaf lowers to CEL and compiles under a cost ceiling. That ceiling is
**100** (`:34`) and it is the only value ever in play, because every call site passes the defaults
and `DefaultLimits` is the zero `Limits` that the floor fills in (`:47`, `:56-57`).

Generated CEL is **never string-spliced from author input**
(`backend/services/event-processing/internal/rules/cel_gen.go`): operators come from a closed map
(`:19-26`), and metric and attribute keys are grammar-validated then quoted. A dynamic comparison
emits its attribute-presence guard **first** (`:52-82`), so the common "device never set the
attribute" case short-circuits before the metric is examined.

The rule author's whole vocabulary is five **declared variables** — `device`, `anchors`, `occurred`,
`m`, `attr` — plus the `cel.bind` macro
(`backend/services/event-processing/internal/detect/predicate/env.go:26-60`, declared at `:84-97`).
Five is the count of declarations, not of what an author may write: cel-go's standard library rides
along unconditionally, so `has()`, `size()`, the comprehension macros and the timestamp accessors are
all available and none of them appears in that list.

**An action guard's vocabulary is a different and much smaller one**: `value`, `hasValue`, `series`
(`backend/services/event-processing/internal/rules/guard.go:64-81`). Smaller in *variables* only —
the guard env declares `ext.Bindings()` too, so the macro surface is identical, and a guard carries a
second cost bound the leaf does not: a runtime `CostLimit` backstop of 1000 stamped on the program
(`guard.go:51-54`) behind the publish-time ceiling that is the authoritative gate. Conflating the two
vocabularies is a mistake the natural-language prompt made (§12).

What the compiler does **not** check, stated because each has bitten someone:

- a metric key that does not exist — and the typo never fires by **two different routes**, which
  matters because only one of them is inert. A structured leaf is generated presence-guarded, so
  `"typo" in m` is cleanly false and the rule simply does not match. A hand-written raw-CEL
  `m["typo"] > 5` is an unguarded index on a missing key, which **errors**; the runtime treats a leaf
  eval error as a *skip*, and for a Duration rule a skip preserves the hold rather than cancelling it
  (`cel_gen.go:60-66`). The rule that "never fires" may therefore be one whose state is frozen;
- a raw-CEL leaf's totality over devices that have set nothing. The structured generator only ever
  emits a **positive** presence guard, so a structured dynamic rule cannot mis-fire on absent state.
  An author who writes the negation — `!("k" in attr) && …` — gets an always-true guard for exactly
  the devices with no bound, so the rule fires across the un-configured part of the fleet and stops
  firing per-device as each attribute arrives. `predicate/env.go` states this as live behaviour, not
  a transitional window that closes;
- a `sendCommand` action's command name against the profile's command vocabulary, and its payload against the
  command's parameter schema — both are checked, but downstream at enqueue (§10b), not here;
- a `publish` action's connector reference. A dangling one is not a drop-and-log: outbound-connectors
  classifies both "no such connector" and "connector has no published version" as terminal
  (`backend/services/outbound-connectors/processor/executor.go:185-198`), so the dispatch is
  **dead-lettered and acked on its first delivery** (`consumer.go:333-339`) — zero retries, and
  nothing surfaces it to the rule's author. The rule keeps firing and every firing goes straight to
  the dead-letter stream.

## 7. Three authoring doors, one compiler

Forms, the visual canvas, and a natural-language "Describe" box all lower to the same `rules.Rule`
and pass through the same server-side compiler. The console never authors a definition itself: the
canvas round-trips through `compileCanvas` on a debounce and refuses to save unless a *fresh* compile
of the *current* graph succeeded
(`frontend/apps/console/src/routes/device-profiles/canvas/CanvasEditor.tsx:187-215`, `:292-293`).

The canvas is a compiler front end, not a second engine
(`backend/services/event-processing/internal/rules/graph/lower.go:91-214`). Three port types —
`stream`, `signal`, `value` — may only join like to like, and that single rule is what makes the
DETECT/REACT partition mechanically provable (`internal/rules/graph/schema.go:38-47`). Two node kinds
lower to **no runtime node at all**: a branch folds onto an action's guard (`lower.go:520-536`), and a
compute folds into the leaf as a `cel.bind` (`:693-712`). The compute fold is a text-injection
boundary and is treated as one — each fragment is parsed **unwrapped** to check self-containment
(`:616-625`), because wrapping in parentheses would silently re-balance a stray one, and the body is
placed on its own line so a trailing comment cannot swallow the scaffold.

**"Byte-identical" is the arc's most-repeated over-claim.** Thirteen places state the form/canvas
identity, and what separates the true ones from the false ones is not which file they are in but
whether the sentence is **hedged**. The true form names the qualifier: the two surfaces *re-marshal
to identical canonical bytes*, their verbatim stored strings may differ in key order, and nothing
compares rule bytes anyway. That is what the GraphQL field description on the compile result says
(`backend/services/event-processing/graphql/schema.graphql:277-280`), and it is also how
`internal/rules/schema.go:9-11`, `graph/lower.go:801-802`, `graphql/resolvers_compile_canvas.go:21`
and the two tests that pin the property put it (`internal/rules/schema_test.go:94-97`,
`graph/lower_test.go:53-56` and again at `:308`).

The unqualified form — "compile to a byte-identical `rules.Rule`", full stop — is in the schema
overview 245 lines above the correct one **in the same file** (`schema.graphql:32`), in
`graph/schema.go:7-8`, `graph/lower.go:83-84`, `graph/config.go:477-478`, `graph/branch_test.go:31-33`
and in the console's own API comment
(`frontend/apps/console/src/lib/api/event-processing.ts:61-62`). A reader who lands on any of those
concludes the stored strings match, and they do not: the form omits `when` entirely where the canvas
always writes `"when":{}`. The console's equality check is written for exactly that skew, and it is
worth knowing that it is broader than its own comment — `stableStringify` drops **any** key whose
value stringifies to `{}`, at any depth, not just `when`
(`frontend/apps/console/src/routes/device-profiles/rule-equal.ts:23`).

Each door can express something the others cannot, and the honest list is shorter than it looks.
**Compute nodes are the only genuinely canvas-exclusive construct.** Branch guards and the two
outbound action kinds are canvas-*authored* but not canvas-only: the natural-language door emits
guards and `httpCall`/`publish` from its own prompt grammar
(`backend/services/event-processing/internal/nldraft/prompt.go:56`, `:59`), and the form carries both
through **verbatim** rather than rewriting them — an unedited outbound action is re-emitted from the
wire object captured at load, and a canvas-authored guard is re-attached on save
(`frontend/apps/console/src/routes/device-profiles/DetectionRuleForm.tsx:1170-1172`, `:1259-1261`).
Only the form sets the group scope, which is a column on the rule rather than a field in the graph.
And **neither console door authors a `connectivity` rule** — a frontend gap, not a compiler one; see
§14.

## 8. Scoping

There are three independent scope notions and only one of them is the dynamic-group scope.

The **implicit** scope is the profile: a resolved event carries its profile version token, and rule
selection is a map lookup on `(tenant, profileVersion)` with no query
(`internal/runtime/registry.go:230-232`).

The **group** scope pins exactly one `{group}@{version}`. There is exactly one scope kind — a
published dynamic entity-group version — and it is worth being precise about what the code refuses,
because "it rejects tags and device lists" would be reading a rejection into an absence. There is no
tag or device-list scope kind for anything to reject. What
`backend/services/device-management/model/api_detection_rule_scoping.go:57-75` refuses is a **static**
group, a half-set token/version pair, a missing group or version, and a version whose frozen selector
no longer lowers. Pinning that frozen selector version is the point: the target set cannot drift
under a later selector edit.

Resolution happens at four distinct times, and the middle two are where the design lives:

1. **Author time** — both-or-neither, and the pin must resolve to a published dynamic group version.
2. **Publish time** — the profile's scope references are rebuilt inside the same transaction as the
   active-version flip, and enrollment is refcounted across all profiles so the read-model is
   materialized only for group versions a rule actually references
   (`api_detection_rule_scoping.go:107-220`). Enrollment tracks **published** state, never draft.
3. **Event-resolution time** — membership is stamped onto the immutable event as the union over the
   device and each tracked anchor (`event_resolver.go:522-549`), short-circuited by an existence
   check so an instance with no scoped rules pays nothing.
4. **Eval time** — a set test, **once per event rather than once per sample**
   (`internal/runtime/fanout.go:100-129`). Out of scope produces a *descope*, which drops the series'
   keyed state and resolves any raised alarm rather than merely skipping.

Stamping membership at resolution is what makes a replay see the membership **as it was**, and it is
why joining or leaving a group takes effect on a device's next event with no rule edit and no rescan.
The caveat that follows from the same mechanism: **a device that leaves scope and then goes silent
keeps its keyed state and its raised alarm** until it next reports.

Two kinds refuse a group scope, at both the author-facing gate
(`backend/services/event-processing/graphql/resolvers_detection_rules.go:59-64`) and again as a
runtime backstop on load (`internal/runtime/publish.go:78-99`): Absence and Correlation. Absence
fires from the *absence* of events, so a membership stamp that only arrives on an event cannot gate
it; Correlation is keyed on an anchor rather than a device.

## 9. Fan-out — one message becomes N per-rule events

`internal/runtime/fanout.go:93-188` is the hot path. Order: resolve the rule set → partition by group
scope once → short-circuit a presence state change to the connectivity path → build **one input per
measurement sample** → bind the device's attributes onto every input → apply the metric-scoped feed
gate → fan a correlation rule per anchor → build the event.

One input per sample, not one per message, is a correctness decision recorded at
`internal/runtime/input.go:22-30`: a batched store-and-forward upload carrying `[120, 80]` would
otherwise last-value-wins into a single map and a `temp > 100` rule would never see the 120.

**A runtime CEL eval error skips the event; it does not evaluate to false.** `fanout.go:31-33` argues
why: a false leaf would *cancel a Duration hold*, whereas a skip preserves it. The consequence for an
operator is in §13.

The whole fan-out is keyed by `SeriesKey{Rule, Series}` where the series is the source device token —
except correlation rules, which re-key by anchor token and count distinct contributing devices
(`correlation.go:20-68`).

## 10. REACT — from a detection to an action

REACT is a **separate durable consumer of the stream DETECT publishes**
(`ReactDispatcher`, `backend/services/event-processing/processor/react_dispatcher.go:34`), not a
function call. It starts at the stream tail, so enabling it on a live cluster does not replay a week
of derived events — the reader opts in at construction
(`backend/services/event-processing/main.go:272`) and the policy is pinned on the durable itself
(`backend/core/messaging/nats.go:907-913`). Note where that lands: `AddConsumer` is idempotent, so
`DeliverNew` applies **only on first creation of the durable**. A consumer that already exists keeps
whatever policy it was created with, which is why this is a one-time property of enabling REACT and
not a per-restart one.

Admission is four fail-closed identity checks, each of which acks and drops (`:112-147`): a subject
with no parseable tenant, an undecodable payload, a payload tenant that disagrees with the subject
tenant, and — the one that matters — **a rule-id tenant prefix that disagrees with the event tenant**.
The rule is loaded by a *global* point read, so without that check a forged event could resolve
another tenant's rule and enqueue its command content.

The action chain is re-read **from the projection on every attempt**, not taken from the wire
(`backend/services/event-processing/processor/react_rule_resolver.go:39`). So editing a rule's actions
takes effect without republishing events, and the severity a `raiseAlarm` raises at is always the
current one.

Actions dispatch in list order, and **there is no retryable/permanent classification here at all** —
the package says so at `internal/react/dispatcher.go:20-23`, and calls per-error interpretation
fragile. Exactly two things produce a `Retry`: a rule-store read error (`:281-284`) and a **sink**
error — the command client, the alarm sink, the connector writer (`:331-333`, `:383-385`,
`:449-451`). Everything else returns `Done` and **moves on to the next action**: a false guard, a nil
sink, a failed payload render, an egress rate shed, a `sendCommand` on a falling edge, a missing
payload variant, an unknown action type.

That is worth restating in the operator's terms, because "fails closed" reads like "held for later"
and is not. For a guard or a template, failing closed means *skip this action and ack the event*
(`:416-423`): the side effect is permanently dropped, never retried, and the only trace is a log
line. A rule whose template has a defect does not wedge — it silently does nothing, forever.

The `Retry` path aborts at the **failing action** and returns (`:292-297`). Since the chain is
re-resolved per attempt, a redelivery re-runs the already-dispatched prefix — which is why the
idempotency token is **content-addressed rather than index-addressed** (`:613-630`): after an author
reorders actions, an index key would re-send the action now at the old index under the old action's
token, swallowing one dispatch and duplicating another. The guard string is appended to the token
**only when non-empty** (`:648-651`) so adding guard support did not re-key every in-flight command
at the deploy boundary.

The consequence on the *other* side of the failing action is the one to carry away, and it is worse
than duplication: siblings ordered **after** the failure never dispatch on any attempt, and when the
event is finally acked at the delivery cap they are lost outright. See §14.

Guards are consulted on the rising edge only, and **never on a `raiseAlarm`'s falling edge**
(`:350-357`) — gating the clear would strand an alarm active forever. Guards and payload templates
both fail **closed**, in the sense just given.

### 10a. Raise alarm — the alarm object as a level-state integrator

The detection carries an edge; the alarm is a **reference-counted set of contributors** on one
`(device, alarmKey)` row.

The contributor identity is **version-free**:
`runtime.StableRuleKey` = `{profileToken}/{ruleToken}` (`internal/runtime/ruleid.go:63-79`), dropping
the profile version. The composed runtime id rotates on *every* profile publish, and keying the
contributor on it forked a fresh contributor per version and stranded the old one ACTIVE forever
(argued at `internal/react/dispatcher.go:359-370`).

The reduction is pure and order-independent
(`backend/services/device-management/model/alarm_contributor.go:78-134`): an edge older than the
stored decision time is ignored; at an equal timestamp **a resolve beats a raise**; a re-raise at an
equal timestamp takes the higher tier. A resolved contributor is kept as a **tombstone**, not deleted.
Presented severity is the maximum tier over *active* contributors only — and an active contributor
carrying an unknown tier is skipped for severity but **still counts as active**, so a malformed tier
cannot silently clear a live alarm.

So an alarm clears when its **last raising rule** resolves, not when any of them does.

The database half retries a lost CAS in process
(`backend/services/device-management/model/api_alarm_contributor.go:76-88`) rather than burning a
broker delivery attempt, because the contributor set is an accumulator and a lost write is permanent
divergence. A resolve that arrives **before** its raise still creates the row, as a tombstone-bearing
CLEARED row, so the later event-time-older raise is rejected as stale rather than wrongly raising
(`:137-175`).

One deliberate product behaviour to know: an operator's **Clear does not touch the contributor set**
(`:56-61`). The next edge re-derives ACTIVE and the alarm reactivates.

### 10b. Send command

REACT calls `createCommand` over a service token minted with `command:write` alone. Validation lands
at enqueue in command-delivery (`backend/services/command-delivery/model/api.go:109-162`), which
delegates to device-management (`backend/services/device-management/model/api_command_enqueue.go:121-149`):

| Case | Verdict |
|---|---|
| device missing or soft-deleted | rejected |
| profile declares **no** command vocabulary | **allowed, free-form and unvalidated** |
| vocabulary declared, command key absent | rejected |
| key matches, payload violates the parameter schema | rejected |

The vocabulary comes from the **active published** profile version. Idempotency is a partial unique
index on `(tenant, token)` plus an insert that does nothing on conflict and **reads back the original
row** rather than erroring (`command-delivery/model/api.go:186-196`).

The sink **does not classify** its errors (`backend/services/event-processing/processor/react_command_client.go:37-43`),
so a permanent rejection and an outage look identical to REACT. A typo'd command name on a
constrained profile therefore costs five full redeliveries — and the sibling actions fare worse than
"re-fired each time", which was the intuition and is only half of it. `Dispatch` returns at the
failing action (`internal/react/dispatcher.go:292-297`), so a redelivery re-fires only the siblings
ordered **before** it. Anything ordered **after** the bad `sendCommand` never dispatches on any
attempt, and at the cap the event is dropped and acked with those actions never having run. Put the
`raiseAlarm` first and it costs five duplicate raises, which the contributor upsert collapses; put it
second and a typo in the command name means the alarm is never raised at all.

### 10c. Connector dispatch

REACT writes a request onto `connector-dispatch` and returns; `outbound-connectors` consumes it. The
wire package is deliberately leaf-like
(`backend/services/event-processing/connectorwire/connectorwire.go:4-12`) so the connector binary
binds the contract without pulling in the model tree, its gorm stores and two SQL drivers. Two
details of that posture are easy to overstate, and its own package doc overstates both. It says it
imports just `encoding/json` and `time`; it also imports `fmt`. And it is not itself walled off from
the rules package — `connectorwire` lives *inside* the event-processing module, where nothing stops
it importing `internal/rules`. The firewall is real but it is one directory up: the
**outbound-connectors module** is compiler-prevented by Go's `internal` rule, which is what keeps CEL
out of the connector binary. The payload is already rendered to a string by REACT, so the connectors
service never links CEL either way.

The consumer is a bounded worker pool (`backend/services/outbound-connectors/processor/consumer.go:39-66`)
fed through a small hand-off channel, and the pacing is "one fetch batch at a time", not a hard stop:
the channel is buffered at `DispatchBacklog`, which defaults to 8 (`consumer.go:101`,
`backend/services/outbound-connectors/config/configuration.go:30-37`), so the read loop pulls eight
more messages past saturation before it blocks. Small is the point — a large backlog would put two
fetch batches under the `AckWait` clock at once, and a slow-but-succeeding send could be redelivered
underneath its own worker.

Timeouts are sized so that two worker waves fit inside that ack window (`configuration.go:57-66`).
The arithmetic is right and **nothing enforces it**: it is stated as an operator note, and `Validate`
requires only that the concurrency be positive (`:145-147`). See §14.

Two hardening measures in the embedded-Bento path are worth naming because neither is obvious:
**environment interpolation is disabled** (`backend/services/outbound-connectors/publish/bento.go:71-77`)
— Bento substitutes `${VAR}` over raw config before parsing, so without this a tenant-authored config
value could exfiltrate a pod-environment secret to the tenant's own broker — and Bento's logger is
routed to a discarding logger so a component cannot print credentials to stdout. Components are
registered selectively, never the full catalog.

Secret resolution is the only place a credential is **decrypted out of the store**
(`backend/services/outbound-connectors/processor/secret_resolver.go:30-35`), cached for 60s under a
**struct** key so no tenant/handle pair can alias another's entry. Read the stronger claim on that
type — "the ONLY place cleartext is materialized in this service" — as being about the *store
boundary*, because taken literally it is not true of the process, and a reader deciding what a heap
dump or a panic could expose needs the literal answer. After `Resolve` returns, the cleartext sits in
an executor local for the length of the send (`processor/executor.go:109-124` for `httpCall`,
`:204-216` for `publish`), goes onto the outbound request as an `Authorization`-style header
(`backend/core/httpsink/httpsink.go:206-209`), and — for a publish — is written **into the generated
Bento output YAML** before that config is parsed
(`backend/services/outbound-connectors/connectorspec/mqtt.go:106-107`, and the same shape in
`connectorspec/aws.go` and `connectorspec/kafka.go`). What the secret-store red line actually buys is
narrower than "materialized once" and is still worth having: never logged, never surfaced on the API,
never on the wire beyond the authenticated outbound call itself.

## 11. Delivery semantics — the question an operator actually asks

**The telemetry path is at-least-once end to end, and outbound-connectors is the only REACT-side
stage with a dead-letter path.** Both halves of that sentence are narrower than the tempting version
("everything is at-least-once, nothing is at-most-once, one stage dead-letters"), and the two places
it over-reaches are worth naming, because each is a real failure mode:

- Two of the **control-plane** streams are declared **at-most-once**, deliberately:
  `detection-rules-published` and `device-roster` (`backend/core/streams/streams.go:372-380`). They
  carry a human authoring action rather than device traffic, and the producer treats a publish
  failure as a log line. What that costs is §14's second entry.
- The platform has **four** dead-letter paths, not one. Three of them are upstream of DETECT and one
  is downstream of REACT: failed-decode in the ingest gateway
  (`backend/services/event-sources/processor/gateway_jetstream.go:352-377`), failed-events at
  resolution (`backend/services/device-management/processor/inbound.go:158-167`,
  `event_resolver.go:733-757`), failed-events at persistence
  (`backend/services/event-management/processor/EventPersistenceWorker.go:386-412`), and
  connector-dispatch.dead (`backend/services/outbound-connectors/processor/consumer.go:318-338`).
  §1 already relies on the second of those, so "only one stage has a dead-letter path" contradicts
  this document three sections earlier.

| Stage | On failure | Terminal fate |
|---|---|---|
| DETECT applying an event | nothing acked, nothing committed | redelivered |
| REACT, any sink | the event is left **unacked** — never negatively acknowledged, because that would burn the whole delivery cap in about a millisecond (`react_dispatcher.go:162-165`) | after the cap: **dropped, acked, counted — no dead letter** (`:155-160`) |
| device-management raise-alarm consumer | left unacked | dropped past the cap with a loud error. A dropped **raise** will not re-emit until the condition falls and re-breaches; a dropped **resolve** strands the alarm |
| outbound-connectors | left unacked | **dead-lettered** — the only one of these three that is |

The dead-letter path stamps a reason header so a replayable rate-shed is distinguishable from genuine
poison, and when the dead-letter write itself fails at the cap it records an explicit alertable
**loss** rather than the false "will retry"
(`backend/services/outbound-connectors/processor/consumer.go:344-405`).

**Connector dispatch is the one sink a REACT retry can genuinely duplicate.** Alarms are idempotent
upserts and commands dedup on the token, but a redelivery re-publishes a *fresh* dispatch message —
and there is no consumer-side dedup, no dedup window on the stream, and no dedup id on the producer.
Collapsing to one execution is the remote endpoint's job, via the `X-DC-Idempotency-Key` header. For
the MQTT and AWS targets the key is only message metadata, which those outputs cannot act on.

## 12. The natural-language door

The AI produces exactly **one untrusted string** and nothing else. It is stripped of any markdown
fence, decoded with unknown-field and trailing-content rejection, and put through **the same
`rules.Compile` under the same limits** as every other door
(`backend/services/event-processing/internal/nldraft/nldraft.go:228-244`). On failure the model is
handed its own candidate plus the compiler's exact error and asked again, up to **three attempts**
total (`:30`, and the loop bound at `:163`).

The boundary, precisely: the drafting mutation **persists nothing**
(`backend/services/event-processing/graphql/resolvers_draft_rule.go:18-21`) — the human saves through
the ordinary create door under their own token. The caller **cannot name a model**, because a caller
able to name one would be choosing its own entitlement. And it requires `device:write` rather than
`device:read` like the other compile doors, because drafting spends budget and a viewer must not be
able to invoke it.

Error hygiene is stronger than it looks, and it takes **three** fixed constants rather than the two
the shape suggests (`nldraft.go:38`, `:46`, `:52`). The unavailable message never echoes the upstream
error, because the service client wraps failures with peer URLs. Rate-limiting is classified
separately from "the model could not write a compiling rule", because the two call for opposite user
actions. The third covers the case where those two overlap: a rate limit that lands *after* a
candidate was already produced truncates the repair loop, and without its own message
(appended as a diagnostic at `:177`) the author would read a half-finished loop as "the AI could not
write this rule" and rewrite a description that was never the problem.

## 13. Operating it

**Exactly one replica, and this is structural.** The partition key is the hardcoded string
`singleton` (`backend/services/event-processing/main.go:33`). The chart pins one replica with the
`Recreate` strategy and says why (`deploy/helm/devicechain/values.yaml:705-727`). Both the chart and
the code are explicit that `Recreate` covers **rollouts only** — an eviction, node drain, or manual
delete has the ReplicaSet schedule a replacement immediately, so two engines can briefly run
(`deploy/helm/devicechain/templates/deployment.yaml:59-65`). The app-level fences described in §5 are
what actually contain that.

**Metrics carry no per-tenant or per-rule label**, deliberately
(`backend/services/event-processing/processor/metrics.go:15-16`, `:81-84`). The over-budget gauges are
*counts of breaching tenants*; the offender is named in a warn log, never in a label. Per-rule
statistics were pushed into a database table instead. That is the property worth relying on, and it
is about **cardinality**, not about labels as such — the arc is not label-free. `connector_dispatch_total`
carries `{action, outcome}`, where `outcome` is an eight-value enum
(`backend/services/outbound-connectors/processor/metrics.go:67-69`, enumerated at `:13-45`), and
every service running a NATS manager emits seven `jetstream_*` gauges labelled `{stream}`
(`backend/core/messaging/stream_metrics.go:87-97`). Every one of those is a fixed, small,
operator-owned set; none of them can be grown by a tenant.

**Per-tenant state budgets are measured and never enforced.** `computeStateBudget`
(`ResolvedEventsProcessor.go:1827`) rolls up and warn-logs; nothing acts. The GraphQL schema is honest
about it — a `DISABLED_BY_BUDGET` rule status is declared and never returned
(`backend/services/event-processing/graphql/schema.graphql:205-207`). One tenant can still exhaust the
shared engine's memory for everyone.

**Absence latency has a floor**, stated at
`backend/services/event-processing/config/configuration.go:51-52`: the rule's timeout, plus the
watermark lateness, plus the larger of the idle-advance guard and the checkpoint interval, plus a
tick. With defaults that is roughly the timeout plus fifteen seconds.

**Idle advance is gated on the broker, not on silence** — and the gate is on *idle advance*, not on
absence. An absence timer fires whenever the watermark crosses its deadline, and while a backlog is
draining the watermark is moving on **event time**, so absences fire normally throughout. What the
broker gate covers is the one path that has no event behind it: the wall-clock advance taken when
nothing is arriving. That is layered, and the load-bearing layer is positive evidence rather than
inference — `Backlog` must report zero pending *and* zero ack-pending, because read-loop quiet cannot
distinguish "caught up" from "broker outage with a growing backlog", and a delivered-unacked message
at a rolling-update peer shows up as ack-pending (`ResolvedEventsProcessor.go:1267-1286`). An
unavailable probe fails safe, and if idle advance is enabled while the reader cannot report backlog,
**startup fails**.

**The failure mode with the worst diagnostics is a rule that errors on every event.** A CEL eval error
is a skip counted only in an unlabelled aggregate, and **no log line on that path carries the rule
id**. Such a rule reports `ACTIVE` with a zero fire count in rule health — indistinguishable from a
rule that is correctly quiet. The alert tells an operator to check for a bad published rule and gives
them no way to find it. The only place an eval error is attributable to a rule is the authoring-time
preview.

## 14. Known gaps

Ordered by what they cost.

1. **No IP-level egress control on `httpCall`.** `backend/core/httpsink/httpsink.go:21-23` states the
   package does not resolve or filter destination addresses and calls that the caller's job; **no
   caller does it**. A tenant authoring a rule is authoring an outbound HTTP capability, and nothing
   stops it targeting a private, loopback or link-local address. On a non-2xx with no credential
   attached, a 512-byte response snippet is returned in the error (`:234-241`) — and it reaches
   **platform logs only**, not the dead-letter stream: the dead-letter write carries the original
   request plus a fixed-vocabulary reason header, and the error text is never marshalled into it
   (`backend/services/outbound-connectors/processor/consumer.go:364-405`). What *is* hardened is real
   but is all credential and redirect defence: no redirects, scheme allowlist, URL-credential
   rejection, reserved-header stripping, response suppression when a secret is attached, and
   header-grammar validation at publish — the last of which holds on this path only (see 14 below).
2. 🔴 **A lost rule-publish notification permanently silences a published profile's rules.**
   `detection-rules-published` is declared **at-most-once** (`backend/core/streams/streams.go:372-374`)
   and the producer is fire-and-forget: a publish failure is logged and the method returns
   (`backend/services/device-management/processor/detection_rules_publisher.go:41-52`). The publish
   transaction has already committed by then, so the profile shows active and published and its rules
   show enabled. But event-processing's durable projection — which §6 calls the restart source of
   truth — is built from that fact, so it never gets them, and `reconcileRegistry` re-reads only the
   projection. There is no retry, no dead letter, no reconcile against device-management, and no
   alert. The rules simply never run, and the only recovery is republishing the profile. The producer
   comment names "the planned reconcile" as the answer, which is worth reading as what it is: the
   thing that would close this, not something that exists. `device-roster` (`:376-380`) has the same
   shape, with absence-arming for never-reported devices as the casualty.
3. **Dynamic thresholds are not replay-deterministic** — §5.
4. **REACT's abort silently loses every action ordered after the failing one.** `Dispatch` returns at
   the failing action (`internal/react/dispatcher.go:292-297`), so a redelivery re-runs the prefix
   and never reaches the suffix; at the delivery cap the event is dropped and acked with those
   actions never dispatched. This is partial **loss**, not the bounded duplication the prefix re-run
   suggests, and it is silent: the drop is counted as one poison event, not as N undelivered actions.
5. **Duration's late non-matching event tears down a hold with no event-time guard.**
   `engine.go:776-783` deletes the active run and cancels the timer unconditionally, with no
   comparison against the run's own start, so a bounded-late non-matching event can tear down a hold
   armed by a *newer* matching event. Only the trailing `resolve` on that path is stale-guarded — the
   teardown that happens first is not. Untested.
6. **A `connectivity` rule cannot be authored from the console**, in either door — and the gap is in
   the **frontend twin of the catalog**, not in the compiler. The backend canvas knows the node
   (`backend/services/event-processing/internal/rules/graph/schema.go:66`, catalog entry at
   `:128-131`, lowering at `graph/config.go:151-162`); the browser catalog omits it from the node
   union, the catalog and the condition list
   (`frontend/apps/console/src/routes/device-profiles/canvas/model.ts:21-32`, `:49-63`, `:65-73`), and
   the form's own taxonomy omits it too
   (`frontend/apps/console/src/routes/device-profiles/DetectionRuleForm.tsx:50`, `:1225`) — while the
   natural-language prompt actively steers the model toward it. Worse, the form **coerces an
   unrecognised rule type to `threshold` silently** (`DetectionRuleForm.tsx:1239`), so an
   API-authored connectivity rule opens as a blank threshold form and saving replaces it. The canvas
   asks for a synthesized graph and **discards the error half of the answer**
   (`canvas/CanvasEditor.tsx:118-120`), falling through to a graph carrying nothing but a `source`
   node (`:121-125`).
7. **A runtime split-brain halt is invisible to Kubernetes.** The stale latch stops the loop and is
   never wired to readiness, so the pod keeps serving green health endpoints. The *startup* path
   guards this exact hazard by name (`ResolvedEventsProcessor.go:474-480`); the runtime path reaches
   the same state and does not.
8. **`MaxConcurrentSends` is ungated and silently breaks the ack budget.** The wait-budget ceiling is
   sized against a two-worker-wave model that assumes concurrency near its default of 32
   (`backend/services/outbound-connectors/config/configuration.go:57-66`), and that assumption is
   recorded as a note to the operator. `Validate` only requires the value be positive (`:145-147`).
   Set it to 4 and a fetch batch takes sixteen waves instead of two, each up to the wait budget plus
   the 20s send ceiling — far past the 60s `AckWait` the bound was derived from, so messages
   redeliver underneath the workers still sending them.
9. **The chart's replica guard has a hole** — the condition fires only on `Recreate` with more than
   one replica (`deploy/helm/devicechain/templates/deployment.yaml:88`, failing at `:89`), so a
   rolling-update strategy with three replicas renders and deploys cleanly.
10. **No dead-letter path for derived events** — a REACT event past the delivery cap is dropped, and a
    dropped resolve strands an alarm.
11. **Nothing keeps the natural-language prompt's grammar in step with the compiler.** Four of its
    field instructions described shapes the compiler rejects outright — a guard written from the leaf
    CEL vocabulary, a `window` on `deltaRate`, a `metric` on `absence`, and actions on a
    `correlation` rule — and each rejection burned one of only three attempts, every attempt a paid
    provider call. All four have been corrected in the prompt
    (`backend/services/event-processing/internal/nldraft/prompt.go:25`, `:37`, `:40`, `:43`,
    `:50-52`), and correlation-with-actions was the worst of them, because the prompt teaches
    `raiseAlarm` as the normal way to make a rule *do* something, so "alert me when three devices in
    an area go hot" walked straight into a rejection. What has not changed is why they were there:
    the prompt and the compiler are two hand-maintained statements of one grammar, and nothing
    compiles the prompt's own worked examples or diffs its field lists against `validate.go`. The
    next drift is found the same way this one was — by a human reading both.
12. **Per-tenant compile ceilings are documented but not wired** — every call site passes the
    defaults (`backend/services/event-processing/internal/rules/compile.go:18-22`).
13. **Nothing pins the published alert names to the chart.** `hack/check-prometheus-rules.sh` runs
    promtool over every rendered `PrometheusRule`, which catches a PromQL typo — the failure that
    silently disables a whole group. The unit tests behind it
    (`hack/testdata/prometheus-rules-tests.yaml`) cover only the database-backup group, so the eight
    `Detect*` / `React*` / `EventProcessing*` alert names in
    `deploy/helm/devicechain/templates/prometheusrule.yaml` are asserted by nothing. Prose that names
    an alert — including the published page — can drift from the chart, or the chart from the prose,
    with every gate green.
14. **notification-management never grammar-checks a webhook header at config time.** The
    header-grammar validation listed in gap 1 is on the event-processing rules path
    (`backend/services/event-processing/internal/rules/compile.go:463-472`). The webhook *channel*
    validates its URL and rejects a non-POST method when the config is parsed
    (`backend/services/notification-management/processor/adapter_webhook.go:110`) and strips reserved
    headers at send (`:63`), but `ValidateHeader` is never called anywhere in that service — so a
    malformed header name authored on a channel is discovered only when a real alarm tries to page
    someone.
15. **A group-scoped rule cannot be previewed as scoped.** The preview registry is built with no group
    pin and the descope results are discarded, so such a draft previews profile-wide
    (`backend/services/event-processing/internal/preview/preview.go:191-193`).
16. **Alarm counts do not roll up the relationship graph.** `type Area` carries no alarm field
    (`backend/services/device-management/model/model_areas.go:57-66`) and the alarm query filters a
    single originator. Four code comments used to describe rollup as an existing property of the
    alarm object — they have since been removed, but the property they described has never existed,
    and it is the kind of claim that gets re-derived from the alarm object's shape.

## 15. What preview cannot prove

The preview harness runs the **production** engine and fan-out over replayed history in an
isolated instance that takes no store and no writer
(`backend/services/event-processing/internal/preview/preview.go:12-21`, `:117-228`). Its limits are
stated in the package doc and are worth repeating because a preview that shows nothing is usually one
of them: it starts **cold** at the window, so a hold that began earlier is invisible and a pane
straddling the end never closes; it resolves **no device attributes**, so every dynamic-threshold rule
previews as non-firing; it runs at zero lateness with no future-skew clamp. That last one is a
property of the **caller**, not of the package — `Run` takes lateness as a parameter (`:117-118`) and
it is zero only because the one production caller passes zero
(`backend/services/event-processing/graphql/resolvers_preview_rule.go:143`). Worth knowing before
someone concludes the harness cannot model lateness. Add the two the package does not list: group
scope is never applied, and absence for a device that has never reported has no arming source.

Degradation is reported rather than hidden — an aged-out window, a read cap, a scan cap and eval
errors each append a reason, and a cancelled preview errors rather than returning a truncated result
dressed as complete.

## 16. Tenant deletion

The engine is the one store no query can reach, so its erasure is a **request/reply onto the
single-writer loop** (`backend/services/event-processing/processor/tenant_purge.go`). Two details
carry the whole design:

- The responder is a plain subscribe, **not a queue group** (`:285-306`), because every partition must
  answer for itself, and it handles **inline** rather than one goroutine per request, because every
  request contends for the same loop and each runs a full sweep plus a synchronous commit.
- 🔴 **The clean answer is conditioned on the dirty flag, not on the count swept** (`:179-198`). Had
  it been conditioned on the count, an earlier pass that swept memory and then failed to commit would
  leave the engine clean and the snapshot full: the next pass would find nothing, answer clean, and
  the purge would complete over data a restart restores. A clean engine is not a clean checkpoint,
  and while `dirty` is set this process cannot say anything about what the snapshot holds.

**Nothing in event-processing consults the tenant-lifecycle gate**, and the reassuring reading of
that — "it only retains, so the sweep erases it" — holds for DETECT and not for REACT. DETECT does
only retain. REACT *dispatches*, on three paths, and two of them land on services that carry the gate
themselves: a `sendCommand` is refused at delivery in command-delivery, and a connector dispatch is
refused before egress in outbound-connectors. The **raise-alarm path is gated nowhere** — not by the
REACT dispatcher's admission checks, and not by
`backend/services/device-management/processor/raise_alarm_consumer.go`, which is the one consumer in
that service without the check its own device-callout responder carries. So a derived event already
published for a purging tenant can still write a fresh alarm row for a tenant whose engine store has
just reported clean. It is not an unbounded leak — the relational sweep reclaims the row on a later
pass — but reclaiming it costs that store its `CleanSince` and restarts the settle window, which is
precisely the cost the outbound dead-letter path was designed to avoid. The full picture, including
why the emitting/retaining distinction has been miscounted twice already, is in
`docs-drafts/tenant-deletion.md`.

## Publishing this

`docs/docs/deployment/detection-engine.md` is the user-facing page derived from this one, with a
Spanish mirror and a link in from the event-processing concept page.

The split is by **audience**. The published page carries the contract: what fires and when, what a
restart costs, what is at-least-once, how many replicas may run, what the tunables are, and how to
diagnose a rule that is not firing. That is stable enough to earn a standing obligation in two
languages.

This document carries the mechanism: the ownership handoff, the deliver-before-checkpoint order, why
eviction is a predicate over rule ids, why the contributor key drops the profile version. That
changes with almost every slice, and translating it would be work thrown away — and the rot gate
works only on file anchors, which no published page may carry.

The body carries no decision-record references so that it and anything derived from it are publishable
as-is; the frontmatter holds the pointers.
