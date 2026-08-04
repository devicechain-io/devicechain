---
title: Tenant Deletion
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-077, ADR-033, ADR-023, ADR-025, ADR-026, ADR-030, ADR-044, ADR-047, ADR-058, ADR-059, ADR-065]
---

# Tenant Deletion

Deleting a tenant in DeviceChain is a **lifecycle, not a row delete**. The operator's action cuts
access immediately; reclaiming the tenant's data happens afterwards, driven by a background
coordinator, and finishes hours later. Only when every storage system has reported — and kept
reporting — that it holds nothing does the platform remove the tenant's row, write a completion
stamp on a durable deletion record, and release the token for reuse.

This document is the traversal across components. No single file holds it: the entry point, the
row's state machine, the coordinator, the six stores, the catalog-driven classifier, the two
waiting windows and the device-plane gates each document themselves thoroughly, and none of them
documents the path. Every claim below names the file it came from.

## The shape, end to end

```
deleteTenant (admin GraphQL)
   └─ tenant row: active ──▶ purging      (disabled, stamped with an epoch)
        │
        │   purge coordinator, once a minute, one replica at a time
        ▼
      one pass ──▶ all six stores erase, in order ──▶ a ledger line each
        │            (every store runs every pass; one failing does not stop the rest)
        │                                             │
        │                                             ├─ any line not clean ⇒ no completion, retry
        │                                             │
        │                       every store clean for the settle window (5 min)
        │                                    and the purge older than the token hold (12 h)
        ▼
      completion: record stamped, tenant row hard-deleted, token released
```

The two arrows that look like formalities are the load-bearing ones. A store reporting clean once
does not complete a purge, and a purge that is clean does not release the token until the second
window has also elapsed. Both are argued below.

## 1. The entry point

There is exactly one: the `deleteTenant(token: String!): Boolean!` mutation on the admin plane
(`backend/services/user-management/graphql/admin_schema.gql`), resolved through
`backend/services/user-management/graphql/admin_catalog.go` into
`backend/services/user-management/admin/catalog.go`.

What it does, in order: a missing tenant returns `false` with no error; a tenant already through the
door returns `false` with no error; a tenant that still has memberships is **rejected**, because
removing those is what actually revokes human access; otherwise the row moves to `purging` and the
epoch is stamped.

What it returns is *"the delete door was walked through"*, not *"the data is gone"*. It is
idempotent in both directions, so a retry after a partly-failed teardown converges — which is what
`dcctl sim destroy` relies on (`backend/cli/sim/admin.go` drives this same mutation).

## 2. The tenant row and the epoch

The lifecycle has exactly two states (`backend/services/user-management/iam/model.go`):

| State | Meaning |
| --- | --- |
| `active` | the normal state |
| `purging` | deleted; access cut, data being reclaimed, token still reserved |

**There is deliberately no `purged` state.** It would have to live on a row that cannot survive its
own purge — the completion step removes the row, and its absence *is* the purged state. The durable
record of the erasure is a separate table that outlives the tenant.

The predicate `Deleted()` is `state != "active"`, so an unrecognised or empty value reads as
deleted rather than active. That direction is chosen so a value nobody anticipated cannot quietly
re-admit a tenant.

**The epoch** (`purge_epoch`) is set once, when the purge begins, from the deleting service's clock.
It exists because **the token is released at the end**, so the same token can be purged more than
once over an instance's life. Everything durable keys on `(token, epoch)`:

- the deletion record's unique index is `(token, epoch)`, so a successor's purge cannot overwrite
  the evidence that its predecessor was erased;
- completion refuses unless the record's epoch still equals the row's, which is what stops a
  predecessor's record from closing a successor's purge;
- the coordinator refuses to purge a `purging` tenant that has no epoch at all — without one the
  deletion record would have no identity.

The tenant row itself is the token reservation: a unique index on the token keeps it taken while the
row exists. An operator trying to recreate a tenant at that token gets a message saying so
explicitly — and that message is worded to avoid the words *"already exists"*, *"duplicate"* and
*"unique"*, because `dcctl`'s teardown tolerates errors containing those and would swallow it
(`backend/services/user-management/admin/catalog.go`).

## 3. The coordinator

`backend/services/user-management/purge/coordinator.go`. A loop in user-management, ticking once a
minute by default, running one pass immediately at startup rather than waiting for the first tick —
an operator who just deleted a tenant is watching, and a purge that appears to do nothing for a
minute is indistinguishable from one that is broken.

**The tenant row is the work list.** There is no queue and no fact to lose: a purge is *"there is a
row whose purge state is purging"*. A dropped message, a coordinator that died mid-sweep, a store
that was down for a week and a replica that was rescheduled all converge on the next pass. The work
list ends when the row is removed, which is the same act that releases the token — so there is no
state in which the platform believes a purge is finished while the row says otherwise.

**One replica at a time, via a non-blocking advisory lock.** Every replica runs the loop. A blocking
acquire would not prevent concurrent work, it would *queue* it — each replica running the whole pass
in turn, sweeping tenants a peer had just swept. A per-tenant CAS claim was considered and rejected:
the claim would have to be taken before the sweep and released after, so a replica killed mid-sweep
would leave a claim nothing clears, and the purge would stall until someone noticed.

Three refusals guard the top of each tenant's pass, and each closes a way the purge could complete
having done nothing:

- **an empty store set is refused.** Otherwise no store reports anything unclean, every check
  passes, the settle window is measured from a zero timestamp that is always long elapsed, and the
  purge releases the token having asked nobody.
- **the row is re-read, and the one the pass carried in is not trusted.** Between listing the work
  and reaching here, a peer can have completed this purge and an operator can have created a *new*
  tenant at the reclaimed token. The advisory lock is a session lease, not a fence, so this really
  can happen. The re-read shrinks the window; the relational store closes it with a precondition
  that runs inside the deleting transaction.
- **a ledger line for a store nobody asks about any more still counts.** De-registering or renaming
  a store would otherwise drop its line out of the completion decision while leaving it in the
  record — a deletion record whose completion stamp sits above its own contradiction.

## 4. The two windows

Completion needs both, and neither substitutes for the other.

**The settle window** (default 300s) asks *has everything already admitted finished arriving?* Every
store must have been reporting clean for this long, measured from the **most recent** store to go
clean. Its size is derived, not padded: the ingest gate's tenant-lifecycle cache TTL (60s) bounds
how long after the cut a straggler can still be let in, and this leaves five times that for it to
land.

It also has a **floor that configuration cannot go below, and the floor is two terms**: the broker
answers new subscribers from an in-memory retained cache for two minutes after a stream purge, with
no configuration knob — *plus* the time one broker purge may take. The second term is not padding.
The window is measured from a store's clean-since, and that timestamp is stamped *before* the store
is called, so the purge that empties the retained stream — and therefore the last moment its
payloads can enter the broker's cache — can land a whole purge timeout after the instant being
measured from. A floor of the cache window alone would admit a configured 121s and still allow
completion while a retained payload was servable. The load rejects anything at or below 140s, and
strictly below rather than at it, because the last stamp can be the instant of the purge.

**A pass that deleted rows restarts the settle window**, however clean it ended. `Clean` means
"nothing deferred", not "found nothing" — a store that swept 200k rows and then read back none is
clean by that definition, and measuring the window from a moment the store demonstrably still held
the data is the opposite of what settling is for.

**The token hold** (default 12h) asks a different question: *can anything still be admitted at all?*
It is measured from the epoch, not from when the stores went quiet, and it exists because of a
property that is easy to miss:

> **The fence dies with the tenant row.** Every device-plane gate resolves the tenant's lifecycle
> through user-management, and an unknown tenant reads as *not deleted*. So the instant completion
> removes the row, every one of those gates starts admitting the released token again. Nothing
> downstream is epoch-aware; the data plane knows only tokens.

What can still present itself at that moment is a session established *before* the cut. Its
credential rows were swept so it cannot re-authenticate, but the broker arms its force-close on the
JWT's own expiry, so an already-connected device keeps its live connection for up to one credential
TTL. Release the token before that elapses and a straggler's writes land under it, to be inherited
by the successor — the exact defect this whole feature exists to close, re-entering through its own
completion step. Hence the default is not a round number: it is read from the broker credential TTL
constant so the two cannot drift apart.

## 5. The six stores

`backend/services/user-management/purge/stores.go`. The unit of erasure is the **storage system**,
not the functional area. An area is a code boundary, not a place data is; several areas share one
database while one area is alone in another.

The set is the coordinator's entire coverage claim — a store missing from it is not a gap that
surfaces as a warning, it is a store nobody asks about, and its absence looks exactly like a store
that reported clean.

| # | Store | Erases | Can defer? |
| --- | --- | --- | --- |
| 1 | `rdb` | the main Postgres cluster — every area's schema except event-management | only on a classifier deferral (none today) |
| 2 | `tsdb` | the telemetry cluster, where event-management is alone, over a guest connection | same as `rdb` — it delegates to the same sweep |
| 3 | `broker` | JetStream subjects + the MQTT gateway's client-id-keyed state | no |
| 4 | `kv` | the tenant-scoped JetStream key-value buckets | no |
| 5 | `detect` | the running DETECT engine's in-memory state | **yes** — three ways |
| 6 | `blob` | uploaded objects in the object store | **yes** — one way |

**The order is deliberate.** The relational store is first because it is the only one that can fail
the whole purge closed — a table it cannot classify stops the pass before a row is touched anywhere
— so it is better to learn that before the others have deleted anything. The DETECT engine comes
*after* the broker for a different reason: the broker purge deletes the tenant's still-pending
resolved events, and those are the only thing that can rebuild the state the eviction is about to
remove. Evicting first would still converge, but it would spend a pass doing it.

Three behaviours are worth carrying across all six:

- **An error means "try again", not "this store is broken."** The coordinator records it and comes
  back. A store that errors loses its clean-since timestamp, because an unestablished claim cannot
  support a completion.
- **A deferral means "this data is here and I did not erase it."** It blocks completion and its
  sentence goes verbatim into the deletion record, so it is written for a person deciding whether to
  make an erasure claim: not `detect_snapshots`, but *"the DETECT engine's checkpoint still contains
  this tenant's open windows and buffered event values."*
- **Absent-by-design is clean, and logged at warning.** A telemetry database that does not exist, or
  a DETECT engine that is neither subscribed nor checkpointed, produce a record byte-identical to a
  real sweep — so both paths log that the purge is about to record an erasure that did not happen.

### The object store's idempotency trap

`backend/services/user-management/purge/object.go` is worth singling out because its central design
point is not obvious. The object store has no `List`, so the work list is driven from Postgres: the
tenant row's reference columns. But that row is exempt from the sweep and survives to completion,
and object deletion is idempotent-*silent* — so without care every pass would report one row erased,
forever. Since rows erased restarts the settle window, that is a purge that can never complete. The
resolution is that a successful delete clears the column that named the object, and the clear
happens **before** the counter increments.

Its stated, unfixed gap: an object whose reference was already lost is invisible to a row-driven
work list, and nothing will ever find it again. The real fix is a prefix sweep, blocked on the
object store interface having no `List`.

## 6. How the relational sweep knows what to delete

`backend/core/tenantpurge/`. The sweep does not work from a list each area maintains. It reads the
**Postgres catalog** and classifies every table it finds, which is what turns "what holds this
tenant's data" from *"what eleven people remembered"* into a question the database answers.

It reads `pg_class`/`pg_attribute` rather than `information_schema`, because a materialized view
does not appear in `information_schema` at all — it would never reach the unclassified state and the
fail-closed gate would never fire.

Two column names carry a tenant token directly — `tenant_id` and `tenant` — and a match also
requires a character type, because an integer `tenant_id` is a row-id foreign key and comparing it
to a token would fail at runtime, mid-purge.

Every table lands in one of six classes:

| Class | Assigned when | Sweep does |
| --- | --- | --- |
| `Direct` | it has a character-typed tenant column | `DELETE … WHERE tenant = ?` |
| `Transitive` | a foreign key leads into tenant-bearing data | `DELETE … WHERE (cols) IN (SELECT … )` |
| `Exempt` | a registry entry says why it holds no tenant data | skipped |
| `Deferred` | a registry entry says it holds data nothing can erase yet | skipped, **blocks every purge** |
| `External` | a registry entry names a purge store that erases it out of band | skipped, does not block |
| `Unclassified` | none of the above — **the zero value** | **fails the purge before a row is touched** |

`Unclassified` being the zero value is the fail-closed property: a table nobody has answered for
stops the sweep with a message naming it and the three ways out. Only `Direct` and `Transitive` are
actionable.

`External` is the newest class and exists for one table. `Exempt` would be a lie — it holds the
tenant's data. `Deferred` was the truth right up until something erased it, and leaving it there
once a store does would block every purge forever over data that is in fact gone.

**Exemptions are applied last**, and only to tables still unclassified. A stale exemption naming a
table that has since gained a tenant column is therefore inert rather than dangerous: an exemption
can only excuse a table the catalog could not explain, never override the catalog.

The sweep itself is **one transaction, raw SQL, in foreign-key delete order**. Raw because gorm's
tenant-scope callbacks would force the purge to impersonate the tenant, and because several models
embed soft deletion — a gorm delete would set a timestamp, report rows affected, and leave every
byte in place. The delete order is a real dependency sort, not alphabetical; a foreign-key cycle is
reported rather than broken, because breaking one silently produces a sweep that fails partway
through on a constraint violation and reads as a database problem rather than the schema shape it
is.

Afterwards a **residual scan** re-runs the same predicates as counts. Rows found are an *error*, not
a deferral — something is still writing for a tenant whose access was cut.

### The CI gate, and its blind spots

The coverage gate runs inside `hack/migration-diff.sh verify`, on both supported Postgres majors. It
migrates every area into one database and classifies every table; an unclassified one fails the
build. It refuses a filtered run outright, and it carries a negative control — every migrated area
must have contributed at least one table, because *"no unclassified tables"* is also what an empty
plan says.

Its limits are worth stating plainly, because each one is a place a green build means less than it
looks:

- **It sees shape, never rows.** A *false* exemption passes green forever. The only automated check
  on an exemption is that it has a non-empty reason.
- **It cannot check that an `External` store exists.** Core cannot import a service. That claim is
  closed instead by a unit test in the store package asserting the named store is registered.
- **It covers Postgres only.** Nothing derives the set of *storage systems* that hold tenant data.
  The store registration test is the honest stand-in, not a replacement.

## 7. The store that is a running process

`backend/services/event-processing/processor/tenant_purge.go` and
`backend/core/messaging/tenant_purge_detect.go`.

The DETECT engine's windows, timers and dead-man armings live in one process's memory and are
checkpointed as a single opaque blob per partition holding **every tenant at once**, with tenancy
carried inside it as a prefix of each rule id. No SQL predicate reaches that, and deleting the row
would not be an erasure but a corruption — it is the checkpoint every other tenant's replay-correct
recovery depends on.

So this store asks. A request/reply gather on a subject rooted at `$DC.`, deliberately outside every
stream's capture space so a control message cannot be captured, stored and replayed as a platform
message. The request carries the tenant and explicitly **not** the epoch: the engine's state is
addressed by token alone and evicted in full, so there is no residue for an epoch to distinguish.

Four properties make the answer trustworthy:

1. **It waits for everyone.** Not a plain request — that returns the first reply and unsubscribes,
   silently reducing a multi-partition fleet to whichever engine answered fastest. The expected set
   is built from the partitions that have actually committed a checkpoint.
2. **The eviction runs on the single-writer loop.** The engine has no locks by design, so mutating
   it from a broker callback goroutine would be a data race.
3. **The reply is sent only after the checkpoint commits.** Alone among the loop's cases it both
   answers and *forces* a commit, because an eviction is not re-derivable from a replay: a crash
   before the next scheduled checkpoint would resurrect exactly what was just erased — and the
   caller would already have been told it was gone.
4. **Only a clean reply satisfies a partition.** A partition can have two responders: a split-brain
   writer that lost its checkpoint race keeps its subscription until the process stops, and it fails
   in microseconds on its cancelled context while the healthy writer is still committing. If an
   error reply satisfied the expectation, the gather would return on the halted pod's answer every
   single time and never read the committed one.

The condition for reporting clean is **not** "this pass evicted nothing". It is that the engine has
nothing *uncommitted*:

> A clean answer requires a clean checkpoint, not a clean engine. An earlier pass can have swept the
> tenant out of memory and then failed to commit. The engine is then clean, the dirty flag is still
> set, and the committed blob still holds every one of the tenant's windows. A second pass moments
> later finds nothing, answers clean, and the coordinator starts its settle window over data that is
> still on disk. The purge completes; a later restart restores the tenant's state from the stale
> blob, and no pass remains that would ever evict it again.

"Is this area even deployed here?" is answered by the **schema catalog**, not by who replies — an
ingest-only instance and an engine that is down both produce no responder, and they mean opposite
things.

The three deferral cases can persist indefinitely, and that is correct rather than a deadlock: an
instance whose event-processing is scaled to zero will never complete a purge, the data really is
still there, the deletion record names the partition holding it, and starting the service resolves
it.

## 8. The deletion record

Two tables in user-management, both exempt from the sweep because they must outlive the tenant:

- **`iam_tenant_purges`** — one row per `(token, epoch)`: when the cut was, when it completed, how
  many rows in total. It deliberately **does not carry the tenant's display name**: a record
  retaining "Acme Manufacturing GmbH" would leave the erasure's own evidence as the last place the
  customer's details lived.
- **`iam_tenant_purge_stores`** — one row per `(purge, store)`, **rewritten each pass rather than
  appended**, so a line is a standing claim that gets re-established rather than a log entry that
  could quietly go stale. It carries what the store deferred, what it failed with (kept apart,
  because the two mean opposite things about the future), and the timestamp since which it has been
  continuously clean.

Row counts **accumulate across passes**. An erasure is idempotent by contract and a purge cannot
complete on its first pass, so a per-pass count would read zero for every real purge.

**Neither table is exposed on any API.** An operator can see *that* a tenant is purging and *when*
the cut was — those two fields are on the admin tenant type and rendered as a badge in the console —
but not how far along it is or what is blocking it. That is a known gap, not a design decision;
`Deferred`, `Failure`, `CleanSince` and the per-store row counts are database-only today, visible
otherwise only in logs.

## 9. The device plane during a purge

One shared mechanism, `governance.NewTenantLifecycleGate`, wired at five services: the broker
auth-callout in device-management, all three inbound sources in event-sources, the Sparkplug and
LwM2M ingest adapters, and command-delivery. It resolves the tenant's lifecycle from
user-management's governance query over a service token scoped to `tenant:read` alone, through the
same 60-second cache the ingest ceilings use.

In event-sources it is composed **onto the rate gate** rather than added to each source, and the
reasoning is worth keeping: every inbound source already calls exactly one admission hook before it
reads a body, and those are the only places a tenant is known before the write. Adding a second
check to each would mean a fourth transport arrives one day with the rate gate wired — it is a
constructor argument, so it cannot be forgotten — and the lifecycle check missing.

**It fails open, deliberately.** An unresolvable or not-yet-fetched tenant reads as active, so a
user-management outage lets devices under a purging tenant keep connecting and ingesting until the
cache warms. That is defensible because of what this gate is *for*: it is not the correctness path
for erasure — the sweep at each store is — it exists to stop the bleeding early, so the sweep is not
chasing rows that are still arriving. Failing closed instead would make user-management a hard
dependency of device connectivity for **every** tenant, trading a bounded window of data the sweep
would have reclaimed anyway for an instance-wide availability regression.

A refused message is shed exactly as a rate-limited one is, down to the HTTP status code, because
answering differently would tell an unauthenticated caller which tenants exist and what has happened
to them. The two are counted apart, because they mean opposite things to an operator reading a
metric.

**The gate covers the device plane, and outbound connectors are not on it.** A purging tenant's
automation rules can still fire their outbound actions — webhooks and MQTT/Kafka/SNS/SQS publishes —
until the pass that erases those rules lands. It is the one path where a deleted tenant's data can
still *leave* the platform rather than merely arrive at it, and it is bounded by the sweep rather
than by a gate.

At the broker there is **no per-tenant NATS account or user** — isolation is by per-user permission
within one account — so the only broker-layer stop is the auth callout, which refuses before the
credential store is touched. There is **no revocation mechanism**: an already-connected device keeps
its minted JWT for its full 12-hour TTL, and that number is precisely why the token hold is what it
is.

## 10. What is deliberately retained

The sweep does not erase everything bearing the tenant's name, and the exceptions are registered
with reasons rather than assumed:

- **identities** — a person is instance-global; the tenant link is the membership row, which *is*
  swept.
- **roles, tiers, OAuth clients, signing keys, system settings** — instance-level definitions that
  outlive any tenant.
- **the deletion record and its ledger** — the evidence of the erasure.
- **operator-registered AI providers and their tier grants** — a tenant's *access* is the per-tenant
  grant row, which is swept.
- **migration bookkeeping** — schema history, never tenant data.

## 11. Operating it

Configuration lives under `tenantPurge` in user-management (`backend/services/user-management/config/configuration.go`).
`0` means "use the default" for all three keys. **A negative value disables only the interval** —
the other two reject it at load, and that asymmetry is the point rather than an inconsistency.

| Key | Default | Negative | Meaning |
| --- | --- | --- | --- |
| `intervalSeconds` | 60 | **disables the coordinator** | pass frequency |
| `settleSeconds` | 300 | **rejected at load** | how long every store must stay clean; must also exceed 140s |
| `tokenHoldSeconds` | 43200 (12h) | **rejected at load** | minimum age of the purge before the token is released |

Disabling the interval is a real operational lever — a maintenance window where nothing should be
deleting rows — and it is safe by construction: the tenant row is the work list, so a disabled
coordinator leaves every purge pending rather than losing it, and no token is released while it is
off.

The other two are not that kind of knob. A negative `settleSeconds` would mean completing a purge
without ever having observed the stores clean; a negative `tokenHoldSeconds` would remove the only
thing keeping the token reserved until no pre-deletion session can still write under it. There is
deliberately no configuration that buys either, so both fail the load rather than taking effect.

**How long does a deletion take?** At defaults, the floor is the token hold: 12 hours. The *data* is
typically gone within the first pass or two; the *token* is not reusable until the hold elapses.
Those are two different questions and an operator asking "is it done?" usually means the first.

## 12. Known gaps

Stated here so they are found deliberately rather than discovered:

1. **Outbound connectors are not behind the lifecycle gate.** Between the cut and the pass that
   erases a tenant's automation rules, their outbound actions can still fire. Every other known gap
   here retains data; this one can still send it somewhere.
2. **No progress API.** The ledger is database-only; the console shows two fields.
3. **Orphaned objects** whose reference was lost before the purge are unreachable by a row-driven
   work list.
4. **Exempted key-value buckets** (refresh tokens, OAuth codes, locks, leases) never reach the
   deletion record — the ledger records that store as clean without recording what it declined to
   look at. Same shape as the telemetry store's "no database here".
5. **A false exemption is invisible to CI.** Only the presence of a reason is checked, never its
   truth.
6. **Compressed chunks** are out of reach of the purge drill, because compression is applied by a
   runtime reconciler rather than by migrations. Covered instead by event-management's own
   integration tests.

## Publishing this

This draft is an **engineering reference**, and it is longer than any page currently on the docs
site — the published concept pages run 30 to 114 lines. Integrating it is therefore not a file move:
the user-facing page is a shorter derived article answering "what happens when I delete a tenant,
how long does it take, and what is kept", drawn mainly from sections 1, 4, 10 and 11. This document
is the source that page is written from, and the thing a maintainer reads before touching any part
of the path.

The body carries no ADR references so that it and anything derived from it are publishable as-is;
the frontmatter `adrs` field holds the pointers, and `hack/check-docs-adr-refs.sh` enforces the
split.
