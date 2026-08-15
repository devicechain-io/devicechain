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
routes an offline served device's command to that device's shard worker, which parks it there
(`dispatcher.go`, the routing comment in the read loop). The cost is stated rather than hidden:
offline-device commands occupy shard slots, so while `command-delivery` is unreachable a shard can
be blocked by parks timing out. Bounded and self-healing, and preferable to a silent loss.

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

🔴 **Every drained row is CLAIMED before it actuates** (`downlink/claimer.go` → `markCommandSent`),
and this — not the in-process dedup cache — is L4b's correctness guarantee. A drain that ran the
CoAP op without claiming would leave a `HELD` row `HELD`, and `HELD` is not a resting place:
`command-delivery`'s reconciler releases a hold back to `QUEUED` the moment the device reads as
present, which *this very registration* makes true. The next delivery sweep then publishes it down
the live path — for a command, a second **physical actuation**, not a duplicate log line. The claim
is a conditional UPDATE that reports whether *this* caller won it, so the exclusion is structural:
whoever wins actuates, everyone else declines. A claim that **errors** does not dispatch (fail
closed — the row is still dispatchable and the device's next wake retries).

The in-process dedup cache (60s, per-pod, `dedupeTTL`) still exists, but it is now only an
**optimization** against the drain/live overlap inside one pod. It is per-pod and TTL-bounded, so it
says nothing about another replica or about this pod after a restart — the two situations a
leadership change produces. Do not read it as a re-actuation guarantee.

**Oldest-first is achieved server-side.** The drain calls `command-delivery`'s dedicated
`drainableCommands(deviceToken:, limit:)` query, which applies the status set, the expiry horizon,
the `ORDER BY` **and** the bound in the database, and returns a bare list. So the `limit = 32` rows
it returns *are* the oldest 32 (`downlink/fetcher.go`). That ordering is what the FOTA runbook below
depends on.

This replaced a client-side workaround: the general `commands` query had no `ORDER BY`, so
`pageSize=32` returned an *arbitrary* 32 rows and the fetcher had to over-fetch 1000, sort by
numeric `id` and truncate — a per-wake, per-device cost paid on every registration to route around a
missing `ORDER BY`. Two things went with it. Expiry is now evaluated against the **database's**
clock rather than the pod's, so a skewed replica can no longer drop a still-live command; and there
is **no over-fetch**, which is safe because the two ways the drain loop skips a row (the per-pod
dedup cache, a lost claim) both describe a row that has *already left* the drainable set — a skipped
row is not a slot stolen from a row still awaiting delivery.

The 32-per-wake cap is the **device-edge flood governor**: a REACT `send-command` storm cannot slam
a constrained radio with an unbounded burst the instant it wakes. A deeper backlog drains across
subsequent wakes.

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
- ⚠️ **A park whose retries are exhausted is WORSE off than before parking existed, not equal to
  it.** Once the retry budget is spent (`MaxDeliver` × `AckWait`, on the order of minutes), the row
  is stuck in `SENT`, which is invisible to the drain, the sweep and a cancel alike — so it lapses
  to `TIMEOUT`, precisely the mislabel `PARKED` exists to remove. It needs a `command-delivery`
  outage spanning the whole budget. It is bounded, it is rare, and it is the concrete argument for
  the **stranded-`SENT` reconciler that is filed and not built**.
- **Wake triggers can be dropped.** `Drain` is non-blocking so a wake never stalls the CoAP read
  loop; a full shard drops the trigger (counted on `command_drain_dropped_total`) and the device's
  next Update re-triggers it.
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
`command_park_errors_total` and `command_drain_claim_errors_total` mean `command-delivery` could not
be reached (deliverable commands going undelivered), whereas `command_park_settled_total` and
`command_drain_claims_lost_total` are the mechanism *working*. `command_park_skipped_total` should
be flat zero on a configured instance.

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
radios. `downlink.concurrency` (default 16) sets cross-device dispatch parallelism; a single device's
commands always run in stream order regardless of the count.

The wake-drain path is **fail-open on configuration**: if `infrastructure.commandDelivery` is not
configured, draining is disabled entirely rather than refusing to serve — offline commands then ride
their TTL instead of being delivered on a wake.

---

*Follow-up: a Docusaurus `docs/` concept + command-reference page for LwM2M (mirroring
`concepts/sparkplug.md`) is a documentation task tracked separately from this backend slice.*
