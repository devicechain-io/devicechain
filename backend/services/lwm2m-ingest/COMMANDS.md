# LwM2M downlink commands (ADR-075 L4a + L4b)

The LwM2M ingest adapter dispatches platform-originated commands **down** to a connected LwM2M
device: a command created in `command-delivery` is consumed by the adapter (only on the serving
leader), mapped to a CoAP **Read / Write / Execute** on the device's live DTLS session, and its
outcome is reported back on `command-responses`.

This is the *first* true downlink adapter in the platform: an MQTT/NATS device subscribes to its own
command subject directly, but a CoAP device cannot, so the adapter stands in for it.

## The three command keys

Commands are **generic and path-addressed**: the profile declares three `CommandDefinition`s, and the
LwM2M object/instance/resource path (and, for a write, the value) rides in the command **payload**.
The adapter needs no per-device-type mapping.

| Command key      | CoAP op | Payload                                   | Success → `command-responses` |
|------------------|---------|-------------------------------------------|-------------------------------|
| `lwm2m.read`     | GET     | `{"path":"/3/0/9"}`                        | `success:true`, `payload` = the device's response body (text; base64 if opaque) |
| `lwm2m.write`    | PUT     | `{"path":"/5/0/1","value":<scalar>}`      | `success:true` |
| `lwm2m.execute`  | POST    | `{"path":"/5/0/2","args":"<optional>"}`   | `success:true` |

- **`path`** is an absolute LwM2M path with 1–4 numeric segments (`/objectId[/instanceId[/resourceId
  [/resourceInstanceId]]]`), each a 16-bit id. A malformed path is refused locally (the command
  reports `FAILED` without touching the wire).
- **`value`** (write) is a single JSON **scalar**: a string is written as-is, a number by its exact
  literal, a boolean as `1`/`0` (LwM2M text/plain). Object/array/null and multi-resource
  (SenML/TLV) writes are **not** supported in this slice.
- **`args`** (execute) is the optional LwM2M execute-argument string; omit it for a bare Execute.

A device's CoAP response class decides the outcome: `2.xx` → `SUCCESSFUL`; `4.xx`/`5.xx` → `FAILED`
with the code; no response within the timeout → `FAILED` (timeout). A `lwm2m.read` returns the
response body in the command's `responsePayload`.

## Queue-mode hold-and-drain

A command for a device that is **connected right now** is dispatched immediately (the live path). A
command for a device that is **offline** — a queue-mode sleeper, or a device between sessions — is
**backlogged in `command-delivery`**, and **drained to the device on its next wake**.

### 🔴 Presence is not reachability — the two ways a command becomes backlogged

`command-delivery` decides whether to publish from **presence**: is the device registered? A
queue-mode device is registered *and asleep*, so the two facts come apart, and the backlog is
reached by two different routes that end in two different states:

- **`HELD`** — the device's *absence* produced it. `command-delivery` knew the device was not
  reachable and deliberately withheld dispatch rather than publishing into the void.
- **`PARKED`** — *this adapter* produced it. The device read as present (it is registered), so
  `command-delivery` published; the dispatcher looked up the device's conn table entry, found no
  live conn, and **handed the command back** (`downlink/parker.go`, invoked from
  `dispatcher.go`'s `dispatch`).

Both are states in which the **platform still holds the command**, so both are drainable
(`drainStatuses` in `downlink/fetcher.go` is the single definition). `SENT` is not: it means "the
device has it".

Before `PARKED` existed, a command published to a registered-but-sleeping device simply stayed
`SENT`. That row was a lie in three directions at once: it blamed the device with `TIMEOUT` at its
TTL for a command that reached nothing, it told an operator cancelling a fleet write that the
command was beyond recall when it was not, and it was re-dispatched by the wake drain **without a
claim**.

### The park path, and why it carries a nonce

The park is a network round trip, so it does **not** run on the JetStream read loop — the reader
hands an offline served device's command to a small **park pool** (`overflowWorkers` goroutines
over a queue of `overflowDepth`), which parks it there (`dispatcher.go`, `route`). The pool, not
the device's shard, carries it, so while `command-delivery` is unreachable parks timing out do not
occupy the shards that live devices are dispatched on. The same pool parks the commands described
under [the per-device gate](#the-per-device-gate-parking-instead-of-waiting) below.

Every publish carries a **`dispatchNonce`** naming the dispatch attempt it belongs to
(`deliveryEnvelope.DispatchNonce`), and `parkCommand` matches on it. 🔴 **The nonce is what makes
the park safe to retry.** A park request can be a JetStream redelivery of a publish whose command
has since been claimed by a wake drain and **run**; a park matching on status alone would drag that
row back into the dispatchable set to be actuated a second time. A stale request names a dispatch
that no longer exists and moves nothing.

Park outcomes are three, and the adapter treats them differently:

| Outcome | Ack? | Meaning |
|---|---|---|
| parked | ack | The command is back in the platform's hands. |
| moved nothing | ack | **Settled, not failed** — answered, cancelled, expired, or re-claimed under us. |
| error | **no ack** | Retry on AckWait redelivery. Not `Nak()` — a Nak redelivers immediately and burns `MaxDeliver` in milliseconds. |

An envelope with **no nonce** (an older publisher, or something other than the delivery sweep) is
**not parked** — counted on `command_park_skipped_total`, and the row stays `SENT`. Parking on a
match that ignores the nonce is precisely the re-arm this design refuses.

### The drain, and the claim that precedes it

When a device next **Registers** or sends a re-handshake **Update** (the LwM2M queue-mode wake
signals), the serving leader queries `command-delivery` for that device's `HELD` + `PARKED`
commands and dispatches them **oldest-first** over the freshly live session — the same CoAP
Read/Write/Execute mapping as the live path, on the same per-device worker (so a drain never races
or reorders the device's live commands).

The drain runs in **turns** of at most `drainTurnMax` (4) rows for one device, and the shard's
worker alternates one live task with one turn. A device keeps getting turns for as long as it is
live and has rows: the next turn is started by the worker itself, not by another wake, because a
device that stays connected never sends one (a keepalive `Update` on its live connection fires no
wake — `ConnTable.Refresh` returns early). A fetch or claim error ends the turn at that row and
retries it after `drainRetryDelay` (5s), on a timer, so an outage neither spins the shard nor
leaves a connected device waiting for traffic that may never come.

🔴 **Every drained row is CLAIMED before it actuates** (`downlink/claimer.go` → `markCommandSent`),
and this is L4b's correctness guarantee. A drain that ran the
CoAP op without claiming would leave a `HELD` row `HELD`, and `HELD` is not a resting place:
`command-delivery`'s reconciler releases a hold back to `QUEUED` the moment the device reads as
present, which *this very registration* makes true. The next delivery sweep then publishes it down
the live path — for a command, a second **physical actuation**, not a duplicate log line. The claim
is a conditional UPDATE that reports whether *this* caller won it, so the exclusion is structural:
whoever wins actuates, everyone else declines. A claim that **errors** does not dispatch (fail
closed — the row is still dispatchable, the turn stops at it, and the device's next turn retries it
after `drainRetryDelay`, in order).

### The live path is claimed too

A command that arrives on the delivery stream for a **connected** device is **confirmed with
`command-delivery` immediately before it actuates** (`downlink/claimer.go` →
`confirmCommandDispatch`, called from `claimLive` in `dispatcher.go`), quoting the envelope's
`dispatchNonce`. 🔴 **A live envelope can arrive late**: redelivered after it waited out its ack
deadline behind a slow device, redelivered to a new leader, or pulled for the first time long after
it was published because no replica was reading (an envelope nobody has pulled has no ack timer
running at all). By then `command-delivery`'s stranded-`SENT` pass may have re-armed the row to
`PARKED` and a wake drain may have carried it out. Before the confirmation the live path actuated
whatever arrived, so that sequence moved the hardware twice.

The confirmation is one conditional UPDATE on `(status = SENT, dispatch_nonce = N)` that **rotates
the nonce** and restamps `sent_time`:

- a late envelope names a dispatch the row is no longer on, loses, and is **discarded** (acked,
  counted on `commands_stale_dispatch_total`), and a wake drain is nudged for the device — the
  usual cause is a re-armed `PARKED` row, and a device that stays connected sends no wake of its own;
- every redelivered copy of an envelope that was already confirmed loses the same way;
- the response then quotes the **new** nonce — `command-delivery` matches an answer against the
  row's current one;
- a confirmation that **errors** fails closed: the command is not actuated, the message is left
  unacked to redeliver at the ack deadline (`command_live_claim_errors_total`), never Nak'd;
- a command whose **batch was called off** before it actuated is stopped here and lands on
  `CANCELLED`.

`status = SENT` is not redundant with the nonce: a park keeps the nonce, so a confirmation on the
nonce alone would match a `PARKED` row and actuate a command the drain is also about to claim.

A confirmation that errors also **gates the device** (see below): left alone, a later command for
the same device could be confirmed and actuated before the unacked one redelivers.

⚠️ **The confirmation's latency cost is UNMEASURED.** Every live command now makes one extra
`command-delivery` GraphQL round trip, plus one `UPDATE`, before its CoAP op — and it runs serially
on the device's shard worker, so it also delays the commands queued behind it on that shard. No
number is claimed here because none has been taken; it belongs to the next measurement pass
(per-command confirm latency, and shard throughput with and without it).

There is no per-pod dedup cache any more. It used to sit beside the claims as an optimization; with
every route to a device claimed on its row it guarded nothing, and it said nothing about another
replica or about this pod after a restart in any case.

**Oldest-first is achieved server-side.** The drain calls `command-delivery`'s dedicated
`drainableCommands(deviceToken:, limit:)` query, which applies the status set, the expiry horizon,
the `ORDER BY` **and** the bound in the database, and returns a bare list. So the `limit` rows it
returns (`drainTurnMax` per turn) *are* the oldest ones (`downlink/fetcher.go`). That ordering is
what the FOTA runbook below depends on.

This replaced a client-side workaround: the general `commands` query had no `ORDER BY`, so
`pageSize=32` returned an *arbitrary* 32 rows and the fetcher had to over-fetch 1000, sort by
numeric `id` and truncate — a per-wake, per-device cost paid on every registration to route around a
missing `ORDER BY`. Two things went with it. Expiry is now evaluated against the **database's**
clock rather than the pod's, so a skewed replica can no longer drop a still-live command; and there
is **no over-fetch**, which is safe because the one way the drain loop skips a row (a lost claim)
describes a row that has *already left* the drainable set — a skipped row is not a slot stolen from
a row still awaiting delivery.

The per-turn bound is the **device-edge flood governor**: a REACT `send-command` storm reaches a
constrained radio at most `drainTurnMax` commands at a time, interleaved with the shard's live
work, rather than in one burst the instant it wakes. It replaced a cap of 32 per wake, after which
the rest of a deeper backlog waited for the device's next wake — which a device that stays
connected never sends, so the remainder could sit until it expired.

### The per-device gate: parking instead of waiting

Commands for one device reach it by two roads: **live** (dispatched as they arrive on the delivery
stream) and **backlog** (parked in `command-delivery`, then drained oldest-first). The moment one of
a device's commands takes the backlog road, a later one taking the live road would overtake it. So
the dispatcher keeps a per-device **gate** (`downlink/gate.go`): once it is up, every further live
command for that device is parked too, and it comes down only when a drain turn has seen the
device's backlog empty with nothing still on its way into it (no park in flight, none awaiting
redelivery, nothing settled since the turn began).

The gate goes up, and the reason is the label on `commands_overflow_parked_total{reason}`, when
the following happens. The gate carries its LATEST cause, and a command parked behind it is counted
under that cause, except that `offline` is used only when the command's own lookup found the device
without a connection (so `commands_served_offline_total` never counts a connected device):

| Reason | Cause |
|---|---|
| `full` | The device's shard queue (`workerQueueDepth`) was full. |
| `offline` | The device had no live connection. |
| `bind` | The device (re)connected, or a live delivery turned out to have been re-armed already (the stale-dispatch nudge). The gates are in memory and per leadership term, so a new leader cannot know what an old one parked, nor what the stranded-`SENT` pass re-armed: every device's first bind gates it until its backlog has been looked at. A connected device still gated `offline` from before it reconnected is counted here too: it is waiting for its bind's drain, not for itself. |
| `unconfirmed` | A live confirmation errored. That command is left unacked to redeliver, and the gate holds for its redelivery, which is then parked into its original place. |

🔴 **This is what removed the head-of-line convoy.** The reader used to *block* on a full shard
queue, so one live-but-slow device (each op burning the full `opTimeout`) held the whole
instance's command throughput to its own rate. Now the reader never waits on a device: a command
that cannot be queued is parked and its device gated. What still back-pressures the reader is the
park pool itself, when all its workers are busy and its queue is full — which happens only while
`command-delivery` is slow, and is counted on `command_overflow_blocked_total`. Blocking there,
rather than dropping or leaving the command unacked, keeps it in its place: a redelivered copy
could otherwise arrive after a drain had served newer rows. While `command-delivery` is down the
reader moves at `overflowWorkers` parks per service-client timeout (10s) — but no live command
could get through then either, since its confirmation needs the same service.

Costs, stated:

- A command parked because its device was gated pays a park round trip plus a fetch and a claim
  before its op, instead of just the confirmation. In steady state (no overflow) the only added
  cost is the bind gate: a live command landing between a device's `Register` and the end of its
  first drain turn — one fetch round trip — is parked rather than dispatched directly.
- **After a failover every re-registering device is gated at once**, so for that first fetch
  round trip per device every live command becomes a park — a burst of `command-delivery` writes
  proportional to the command traffic during re-registration, visible as
  `commands_overflow_parked_total{reason="bind"}`.
- A park that errors holds the device's gate for up to `MaxDeliver × AckWait` (the redelivery
  budget, overstated by up to one `AckWait` because the redelivery clock starts at the last
  delivery). If the broker gives up on the message, the gate stops waiting for it and the rest
  of the backlog is delivered: **one of the two places per-device order can still break**, and it
  needs a `command-delivery` outage longer than the whole budget.
- **The other is a live confirmation that committed but whose answer was lost** (the service
  client's 10s timeout, a dropped connection). To this adapter it is an error, so the device is
  gated and the command left to redeliver — but the row is already `SENT` on a nonce the adapter
  never learned. The redelivered envelope's park quotes the old nonce, matches nothing and settles
  as "moved on", which is indistinguishable here from the command having been answered, cancelled
  or expired, so the gate releases it. The drain then delivers the device's later commands while
  this one sits in `SENT` until the stranded-`SENT` pass re-arms it, after its grace; it reaches
  the device on a later bind, after them, or not at all if it expires first. Closing this needs
  `command-delivery` to recognise a retried confirmation; it is not closed here.
- A live command already in the shard queue when its device's gate went up is parked as it leaves
  the queue rather than dispatched, so it stays behind whatever gated the device.
- Every park of a live command runs on the park pool, including the ones a shard worker decides on
  (a command dequeued behind a gate, or one whose device dropped after it was queued): a park is a
  `command-delivery` round trip, and on the worker it would hold every other device on the shard
  for that long. The worker waits only when the pool is full, like the reader, and that wait is
  counted on the same `command_overflow_blocked_total`.

### Expiry, and which terminal state a lapsed command gets

Every command carries a horizon: `command-delivery` stamps a **default TTL of 7 days** (aligned with
the command-stream retention) on any command whose creator supplies no explicit `expiresAt`, so a
command can no longer sit undelivered forever. Tune it with `command-delivery`'s
`defaultCommandTtlSeconds`, or set a per-command `expiresAt` at enqueue.

The terminal state names **who ran out of time** (`expiredTerminalFor`, `command-delivery/model/api.go`):

| Lapsed from | Terminal | Reads as |
|---|---|---|
| `QUEUED`, `HELD`, `PARKED` | **`EXPIRED`** | The platform never got it to the device. |
| `SENT` | **`TIMEOUT`** | It was dispatched toward a device believed live, and never answered. |

So a held command the device never wakes for reaches **`EXPIRED`**, not `TIMEOUT`. (`SENT → TIMEOUT`
still does not promise the device received it: a device that drops between the presence read and the
publish also lands in `TIMEOUT`. That window is narrow and no state distinguishes it, unlike the
queue-mode case, which was systematic.)

### Boundaries, named

- **Seal-fate after the op runs.** Because a physical actuation firing twice is worse than a lost
  status report, the adapter acks a live command's JetStream message once its CoAP op has been
  issued, whether or not the response published. If the device acted but the response could not be
  published (a NATS blip), the command is *not* redelivered — it rides `SENT` to `TIMEOUT` rather
  than re-actuating the device.
- 🔴 **The drain window is AT-MOST-ONCE, and that is a real limitation.** A drained command is
  claimed (`HELD`/`PARKED` → `SENT`) and *then* dispatched. If the leader crashes after the claim
  but before the outcome publishes, the row is `SENT` — which is **no longer drainable** — so it
  does **not** re-dispatch on the next wake. It lapses to `TIMEOUT`. This is the deliberate trade:
  the alternative (leaving the row drainable across the op) is the double-actuation the claim exists
  to prevent. The at-least-once posture the older text described no longer applies to this path.
- ⚠️ **On cutover, rows already sitting in `SENT` are not drained.** They are the ack-dropped
  backlog of the previous build, and they now ride their TTL to `TIMEOUT` instead of being delivered
  on the next wake. Acceptable only because an instance is **recreated** rather than migrated pre-GA
  — stated here rather than discovered as "the drain broke" on an upgraded dev cluster.
- ⚠️ **A park whose retries are exhausted is not delivered by the drain.** Once the retry budget
  is spent (`MaxDeliver` × `AckWait`, on the order of minutes), the row sits in `SENT`, which is
  invisible to the drain, the sweep and a cancel alike, until `command-delivery`'s stranded-`SENT`
  pass re-arms it to `PARKED` after its grace. Meanwhile the device's gate has stopped waiting
  for it, so this is also where per-device order can break. It needs a `command-delivery` outage
  spanning the whole budget.
- **A wake is never dropped.** `Drain` records the device as wanting a turn under its shard's lock
  and nudges the worker; it neither blocks the CoAP read loop nor depends on room in the shard's
  queue. (It used to enqueue onto that queue and drop the wake when it was full, counting it on
  `command_drain_dropped_total` and relying on a next wake that a connected device never sends.)
- ⚠️ **A row the stranded-`SENT` pass re-arms for a device that stays connected waits for its next
  bind** unless a late delivery of that command arrives first (whose stale confirmation nudges a
  drain). Nothing else here starts a turn for a device that is not gated.
- **Tenant lifecycle gates actuation.** Both the live path and the wake drain pass through the
  ADR-077 deleted-tenant check at the shard worker's task union — the drain especially, since it can
  fire long after the tenant was deleted, from a `(tenant, token)` remembered from a registration
  that predates the delete.
- **No `DELIVERED` state.** CoAP is synchronous, so a Read/Write/Execute lands directly on
  `SUCCESSFUL`/`FAILED`. "Waiting for an offline device" is now explicit state (`HELD` / `PARKED`),
  not something an operator has to derive from `SENT` plus a presence lookup.

### What to watch

`commands_served_offline_total` (a served device had no live conn) is the queue-mode signal, not an
error. The failure modes are split so an outage cannot hide inside ordinary contention:
`command_park_errors_total`, `command_drain_claim_errors_total` and
`command_live_claim_errors_total` mean `command-delivery` could not be reached (deliverable commands
going undelivered), whereas `command_park_settled_total`, `command_drain_claims_lost_total` and
`commands_stale_dispatch_total` are the mechanism *working* — the last is a duplicate actuation
avoided. `command_park_skipped_total` should be flat zero on a configured instance.

`commands_overflow_parked_total{reason}` counts live commands parked instead of dispatched, split
by the reasons in the gate table above; `full` rising is a slow device, `bind` spiking is a
failover. `command_overflow_blocked_total` rising means the reader or a shard worker waited on the
park pool, i.e. `command-delivery` is slow. `command_drain_turns_total` is a load signal.
(`command_drain_dedup_total` is gone with the cache it counted.)

## Firmware update over the air (Object 5) — a runbook

L4a does **not** add a firmware mechanism; FOTA is composed from the three primitives plus the
Firmware Update object (`/5`). Drive the steps **in order, waiting for each command to reach
`SUCCESSFUL` before issuing the next** — the adapter serializes a single device's commands, but the
firmware state machine itself requires ordering:

1. **Set the package URI** — write the image location to Firmware Package URI (`/5/0/1`):
   ```
   createCommand(name:"lwm2m.write", payload:{"path":"/5/0/1","value":"coaps://fw.example/image.bin"})
   ```
   The device begins downloading. (For inline delivery, `/5/0/0` Package is a large opaque write and
   is out of this slice's single-resource text/plain scope — prefer the URI method.)

2. **Trigger the update** — execute Firmware Update (`/5/0/2`) once the download has completed:
   ```
   createCommand(name:"lwm2m.execute", payload:{"path":"/5/0/2"})
   ```

3. **Watch progress / outcome** — read Firmware State (`/5/0/3`, `0`=idle … `3`=updating) and Update
   Result (`/5/0/5`, `0`=initial, `1`=success, `≥2`=an error):
   ```
   createCommand(name:"lwm2m.read", payload:{"path":"/5/0/3"})
   createCommand(name:"lwm2m.read", payload:{"path":"/5/0/5"})
   ```
   ⚠️ **Read-on-demand is the only path today.** The observe allowlist is not configurable: `main.go`
   wires `decode.DefaultObjectAllowlist`, a compile-time constant covering the IPSO sensor range
   `3200–3441`, so Object 5 is never observed and `/5/0/3` / `/5/0/5` do not surface as pushed
   measurements. Push progress needs the allowlist to become configuration first.

For a queue-mode device the whole sequence rides the drain: each command is written, backlogged
(`HELD` or `PARKED`), and delivered on a wake — oldest-first, which is why the server-side ordering
above is load-bearing rather than cosmetic.

## Interop

🔴 **The automated Leshan harness does NOT cover commands.** `registry/leshan_interop_test.go`
(build tag `interop`, run by the periodic `lwm2m-interop` workflow) drives a real Eclipse Leshan
client through four scenarios — `lifecycle_and_tenancy`, `observe_notify`, `observe_block2`,
`lifetime_lapse` — all of which are registration, tenancy and telemetry. **The manual procedure
below is the only downlink coverage there is**, so run it rather than assuming the harness has.

Validate against Eclipse **Leshan (pin the client to LwM2M 1.1)**: register a device, then a Read of
a resource, a Write of a resource, an Execute, and the FOTA sequence above. Exercise the queue-mode
path too: enqueue while the client is stopped, restart it, and check the command drains on the
Register. A conformant 1.0-only client is served for presence and commands (Read sends no `Accept`,
so it is not rejected), but its SenML telemetry Observe is 4.06'd until the TLV decode follow-up.

## Tuning

`downlink.timeoutSeconds` (default 10) bounds one CoAP command exchange; raise it for slow cellular
radios. `drainTurnMax` (4) is chosen below `AckWait / timeoutSeconds` (60 / 10) so a live task
queued behind one full turn is still dispatched inside its ack deadline; raising the timeout past
15s breaks that, and a live command waiting behind a turn can then redeliver (its confirmation
then discards the stale copy). `downlink.concurrency` (default 16) sets cross-device dispatch parallelism; a single device's
commands always run in stream order regardless of the count.

`infrastructure.commandDelivery` is **required** whenever the adapter has identities to serve: every
command, live or drained, is claimed with `command-delivery` before it actuates, so without the
coordinate the adapter refuses to start rather than running a downlink that can deliver nothing.

---

*Follow-up: a Docusaurus `docs/` concept + command-reference page for LwM2M (mirroring
`concepts/sparkplug.md`) is a documentation task tracked separately from this backend slice.*
