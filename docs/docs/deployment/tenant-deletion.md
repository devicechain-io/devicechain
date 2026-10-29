---
sidebar_position: 7
title: Tenant Deletion
---

# Tenant Deletion

Deleting a tenant is a lifecycle, not a single action. The delete cuts access immediately. The
tenant's data is reclaimed afterwards, in the background, and the tenant's token does not become
available for reuse until that finishes.

This page covers what happens between those two moments: what to expect, what can hold a deletion
open, and what the platform deliberately keeps.

## What happens, and when {#timeline}

| | |
|---|---|
| **Immediately** | Sign-in is revoked. The tenant is disabled and marked as deleting, and the moment of the cut is recorded. New device connections, new ingest and new command dispatch are refused within about a minute. |
| **Within a minute or two** | A background coordinator begins reclaiming the tenant's data from every storage system that holds it, and keeps going until each one reports that it holds nothing. |
| **Once every storage system has stayed clean for the settle window** (five minutes by default) | The deletion is recorded as [erased](#erased-signal): the platform stands behind the statement that the tenant's data is gone. The token is still held. |
| **After at least 12 hours** | The tenant's record is removed and its token becomes available again. |

The delete itself is idempotent. Deleting a tenant that does not exist, or one that is already
being deleted, succeeds and changes nothing, so you can re-run a teardown script that half failed.

:::note Memberships must be removed first
A tenant that still has user memberships is refused, because removing those is what actually
revokes people's access to it. Remove the memberships, then delete the tenant.
:::

## Why the token is held for 12 hours {#token-hold}

The tenant's data is typically gone within the first minute or two. The **token** is what stays
reserved, and two separate waits must pass before the platform reports the deletion finished.

**Every storage system must report clean, and keep reporting clean.** One clean answer is not
enough: a sweep can finish just as a write that was already in flight lands behind it. The platform
therefore requires a sustained quiet period (five minutes by default), and restarts that clock
whenever a pass still finds something to erase.

**No connection from before the delete may still be able to write.** A device that was already
connected keeps its broker credential until that credential expires, up to 12 hours. Its credentials
have been erased, so it cannot reconnect, but the existing connection survives. Releasing the token
before then would let a straggler's writes land under a name that a *new* tenant could then be
created at, and inherit. Preventing that is the point of the whole process, so the wait matches the
credential lifetime and is measured from when the last storage system finished erasing the tenant's
data, after which no new credential can be issued for it.

Until both waits have passed, the token stays taken. Creating a tenant at it returns an error saying
so.

## What is erased {#erased}

A tenant's data does not live in one place, so the reclamation asks each storage system in turn:

- the **main database**, across every functional area's schema in it;
- the **telemetry database**, where the event history lives;
- the **message broker**: the tenant's streams, plus the MQTT gateway's own session state, queued
  messages and subscriptions;
- the **key-value store**: cached lookups and resolutions;
- the **live event-processing engine**: open detection windows, running timers and armed absence
  checks, which exist in memory and are asked to evict the tenant rather than queried;
- the **object store**: uploaded assets such as a tenant's branding logo.

A deletion cannot be reported as finished until all of them report clean. A storage system that is
unreachable is retried on the next pass, not skipped.

### What you remove yourself {#operator-steps}

Some settings that name a tenant are part of a service's configuration rather than the tenant's
data, so a deletion does not touch them.

**LwM2M device identities.** Each LwM2M DTLS-PSK identity is bound to a tenant and an external id in
the lwm2m-ingest configuration. After deleting a tenant, remove its identities from that
configuration: for an instance installed with `dcctl`, remove them from the file passed as
`--lwm2m-identities` and run `dcctl upgrade --lwm2m-identities` with it (or `--withdraw-lwm2m-identities` if none remain). Otherwise the bindings stay in place and
apply to any tenant later created at the same token.

## What is deliberately kept {#retained}

Some records survive on purpose, and none of them contains the tenant's own data:

- **People.** A user account is instance-wide, not owned by a tenant. What is removed is the
  membership that linked them to it.
- **Instance-level definitions**: roles, tiers, OAuth clients, signing keys and system settings.
  These exist once per installation and outlive any tenant.
- **The deletion record.** Each deletion writes a durable record of what was erased and when, plus a
  line per storage system. It deliberately does not keep the tenant's name or contact details;
  otherwise the erasure's own evidence would be the last place the customer's details lived.
- **The audit journal, with the identities in it destroyed.** See below.

**Backups are the exception to that sentence.** A backup is a copy of a whole database, so a
deleted tenant's data stays in the backup archive until it ages out of that database's
[recovery window](./bootstrap.md#backup-retention): its core data for 30 days by default
(`backup_retention_rdb`), and its event data for 7 (`backup_retention_tsdb`). Until then, a restore
to a point before the deletion brings that data back. Backups sent to an object store you supplied
are also subject to that store's own lifecycle rules. With
[volume-snapshot base backups](./bootstrap.md#snapshot-base-backups), the snapshots at your cloud
provider are pruned to the same window while the cluster runs; if the cluster is deleted before
it has deleted them, they stay at the provider, with that data, until you delete them there.

### The audit journal {#audit}

Every change to an entity is recorded in an audit journal: when it happened, which table, which
operation, how many rows, and who did it. The journal is kept through a deletion, because it is the
record that the deletion happened. Sweeping it away would erase the evidence of the erasure.

What does not survive is anybody's identity. Two fields can name a person:

- the acting user, which for a human sign-in is their email address;
- the label of the affected row, which can be an email or a name you chose yourself for a customer,
  device or asset.

**Both are emptied for a deleted tenant, permanently.** They are written blank rather than scrambled
or hashed. An email address is short and guessable, so anyone holding a list of addresses could
match a hash back to one, which would be erasure in name only.

After a deletion, a journal entry for that tenant still shows the shape of what happened ("three
devices deleted at 14:02") with nobody named. That is the retention working, not data missing.

The kept entries are filed under the deletion rather than under the tenant's token, so a new tenant
created later with the same token starts with an empty journal. The deletion record's
`retainedAuditTenant` field is the value they are filed under: pass it as the `tenant` filter of the
admin API's `auditEvents` query to read that deletion's entries in the administrative journal.

In the console, open **Admin → Audit** and use the tenant filter. It lists each completed deletion as
`<token> (deleted <date>)`, and choosing one shows the entries kept for that deletion.

A tenant created with a deleted tenant's token also does not inherit that tenant's sessions. A refresh
token issued in an earlier second than the new tenant was created is refused, for every caller, so someone who was signed
in to the earlier tenant has to sign in again.

:::warning Sign-in records belong to no tenant, and are not reached
A former member's sign-in history survives a tenant deletion. If your obligations extend to sign-in
records, handle them through database and log retention rather than through tenant deletion.
:::

Signing in happens in two steps: you authenticate as a person, then you choose a tenant. The first
step is recorded, successes and failures alike, with the email address and no tenant attached,
because no tenant has been chosen yet. Records with no tenant cannot be reached by any tenant's
deletion. What a deletion does cover is everything a member did *inside* the tenant.

Two smaller limits, for completeness:

- A former member who tries to sign in to the tenant *after* its deletion completes creates a new
  record naming them, under a token that may by then belong to someone else.
- A journal entry about a person's own profile still records which account was changed. Accounts are
  not owned by a tenant and are not deleted with it, so an operator with access to both can still
  connect the two. What is destroyed is the name in the journal, not the existence of the account it
  referred to.

## Knowing that the data is erased {#erased-signal}

A deletion has two outcomes worth separating, and the platform records both on the deletion
record.

- **Erased** (`erasedAt`): every storage system has reported clean, and has stayed clean for the
  whole settle window. This is the platform's statement that the tenant's data is gone, and it is
  the moment to quote when someone asks when an erasure request was fulfilled.
- **Complete** (`completedAt`): the token has also been held for the full token hold and has been
  released. This follows `erasedAt` by roughly the token hold, 12 hours by default.

`erasedAt` is a standing claim, not a log entry, and it is set only when everything agrees. If any
one storage system still holds data, is unreachable, or has erased something on the latest pass, the
claim is not made: a deletion that is partly done is not erased. The coordinator keeps sweeping
through the token hold, so if data reappears after the claim was made (a late write that landed
behind the settle window), the claim is withdrawn, `erasedAt` returns to null, and it is made again
once the stores have settled again. A record that is `completedAt` always has an `erasedAt` that is
not later than it.

Read it from the admin API, where `erased` is true once the claim stands or the deletion has
completed:

```graphql
query {
  tenantDeletions(criteria: {pageNumber: 1, pageSize: 50}) {
    results { token epoch erased erasedAt completedAt awaiting blockedBy }
  }
}
```

From a terminal, `dcctl tenant-deletion status <token>` prints the same record and exits non-zero
unless the data is erased, so a teardown script can rely on it:

```bash
dcctl tenant-deletion status acme --email ops@example.com --password "$PASSWORD" --wait --timeout 1h
```

`--wait` polls until the claim stands and fails when `--timeout` passes first. `--until complete`
waits for the token release instead. A token with no deletion record is an error, never a success.
The `devicechain_usermanagement_tenant_purge_erased_total` counter (see
[below](#blocked-store)) counts claims as they are made.

## What can hold a deletion open {#stalled}

A deletion that is not finishing is almost always held by one of these, and each names itself in the
platform's logs:

- **Event processing is not running.** The live engine's state can only be erased by the process
  that holds it. If that service is scaled to zero, the deletion waits, correctly, because the data
  really is still there. Starting the service resolves it on the next pass.
- **No object storage is configured, but the tenant uploaded something.** The reference to the object
  is known, but the object itself cannot be reached. Configure the storage backend that holds it, or
  remove the object out of band.
- **A storage system is unreachable.** This is treated as "try again", not as a failure. The deletion
  resumes by itself once the system is back.

None of these loses the deletion. The work list is the tenant's own record, so a coordinator that was
stopped, a replica that was rescheduled, and a system that was down for a week all converge on the
next pass.

### How you are told {#stalled-alert}

A stalled deletion makes nothing else look broken, which is why it needs its own alert. The
coordinator visits the tenant on every pass, finds nothing it can report as an error, and the pass
succeeds. The [scheduled-task metrics](./observability.md#maintenance-passes) that would tell you
the coordinator had stopped therefore stay healthy throughout; they answer a different question.

`TenantPurgeStalled` answers this one. It fires when the oldest open deletion has been open for more
than twice the configured token hold, well past the point where every mandatory wait has elapsed.
The admin API's `tenantDeletions` query then names the tenant and the storage system still
outstanding. It is paged like the admin API's other lists; to read the open deletions:

```graphql
query {
  tenantDeletions(criteria: {pageNumber: 1, pageSize: 50, completed: false}) {
    results { token epoch awaiting blockedBy stores { store complete retaining } }
    pagination { totalRecords }
  }
}
```

Its counterweight, `TenantPurgeVisibilityLost`, fires when those figures stop being collected at all.
Without it, a user-management service that was not being scraped would look exactly like one with no
deletions open.

### Which storage system is holding a deletion back {#blocked-store}

`TenantPurgeStalled` says a deletion is stuck; these series say where. User-management exports them
with no tenant label, because a series named for a tenant would outlive the erasure it describes:

- `devicechain_usermanagement_tenant_purge_store_blocked{store,reason}`: the number of open deletions
  each storage system is currently holding back. `store` is `rdb` (relational data), `tsdb`
  (telemetry), `broker` (streams), `kv` (key-value buckets), `detect` (detection state), `blob`
  (object storage) or `other`. `reason` is `deferred` (the store answered that data is still present
  or still arriving) or `error` (the store could not be asked). Every pair is exported and reads 0
  when nothing is blocked.
- `devicechain_usermanagement_tenant_purge_store_rows_erased_total{store}`: rows, objects, messages
  or keys each store has erased. A blocked store whose counter is still rising is working through a
  large tenant; one whose counter is flat is not progressing.
- `devicechain_usermanagement_tenant_purge_store_erase_duration_seconds{store}`: how long each erase
  call took, which shows which store the time goes to.
- `devicechain_usermanagement_tenant_purge_erased_total`: erasure claims made (see
  [Knowing that the data is erased](#erased-signal)).
- `devicechain_usermanagement_tenant_purge_completed_total`, `tenant_purge_failures_total`,
  `tenant_purge_duration_seconds` and `tenant_purge_clean_duration_seconds`: deletions completed;
  times the coordinator could not carry a deletion through a pass (it retries on the next one); the
  time from deletion to token release; and the time from deletion to the last store reporting clean.
  The last excludes the settle and token-hold waits, so it measures the erasure itself.

Three alerts use them. `TenantPurgeStoreFailing` fires when a store has returned an error for an
hour. `TenantPurgeStoreHolding` fires when a store has reported data still present for as long as the
token hold. `TenantPurgeCoordinatorFailing` fires when the coordinator itself keeps failing to take a
deletion through a pass. The first two carry the store in their `store` label; the log line "Tenant
purge store did not report clean" and the `tenantDeletions` query carry the reason.

## Configuration {#configuration}

These settings live under `tenantPurge` in the user-management service. For all three, `0` means "use
the default".

| Setting | Default | Meaning |
|---|---|---|
| `intervalSeconds` | `60` | How often the coordinator runs. A **negative value disables it** entirely. |
| `settleSeconds` | `300` | How long every storage system must keep reporting clean. Must be greater than 140. |
| `tokenHoldSeconds` | `43200` | How long a deleted token stays reserved, measured from when the last storage system finished erasing the tenant's data. |

Disabling the coordinator is a supported operational lever, for example during a maintenance window
in which nothing should be deleting rows. It is safe: pending deletions stay pending rather than being
lost, and no token is released while it is off.

**The two windows cannot be disabled.** A negative value for either is rejected when the
configuration loads, rather than taking effect. Turning off the settle window would mean declaring data erased without ever
having observed it gone. Turning off the token hold would release a name while a pre-existing session
could still write under it. Lowering `tokenHoldSeconds` is a real choice, since a deleted tenant's
name is unavailable for that long, but it is a choice about correctness, not tidiness.

## During a deletion {#during}

**Device traffic stops within about a minute.** New connections, ingest on every transport, and
command dispatch are all refused once the deletion is picked up. A refused device is answered exactly
as one that is over its rate limit, and backs off and retries.

**Already-connected devices are not disconnected.** They keep their existing broker credential until
it expires, up to 12 hours, and cannot reconnect once it does. This is the reason for the
[token hold](#token-hold).

**If the user-management service is unreachable, these refusals stop applying** until it is back.
That is deliberate: otherwise user-management would be a hard dependency of device connectivity for
every tenant on the instance. The refusals stop traffic early so the reclamation is not chasing data
that is still arriving. The erasure itself does not depend on them.

### Database write refusal {#database-writes}

Writing to the databases is refused separately, and that refusal does not depend on any other
service. From the moment the reclamation first reaches the main database and the telemetry database,
each refuses every write for the deleted tenant, whether it arrives through the API, through a stream
a service consumes, or through a background job of its own. The check runs inside the same database
transaction as the write it refuses.

This is what makes the guarantee an erasure rather than a cleanup: reclaimed rows cannot come back to
stay, even if everything that was supposed to stop the traffic earlier failed. A late write is swept
again, and the deletion does not complete until the writes have stopped. The refusal is lifted only
when the deletion completes, which is also when the token is released, so a new tenant created at
that token writes normally from its first request.

A refused write is not retried. A service that meets the refusal while handling a message from a
stream acknowledges the message on the delivery it arrived on and records nothing about it: no dead
letter and no `failed-events` record, because either would be a new copy of the data being erased.
It counts the message as `erased` (see [Message results](./observability.md#message-results)).
Where a service writes several tenants' messages in one transaction, the refusal settles every
message of the deleted tenant in it at once, and the other tenants' messages are written without
them. A database that cannot say whether the tenant has been deleted is not a refusal: those
messages are retried like any other failed write.

Work for a deleted tenant can also fail before it reaches a write, for example because the deletion
has already removed the device or command it refers to. No dead letter is written for that either,
provided the service can learn from user-management that the tenant has been deleted. If it cannot,
the letter is written, and one written before the deletion finishes is removed with the tenant's
other records.

The refusal is checked at a transaction's first write for a tenant, so a transaction that had
already written for the tenant when the deletion began can still commit, including writes it makes
afterwards. The deletion catches such writes, provided the transaction commits before the
settle window ends. The databases guarantee that for a transaction whose service stops talking to
them, for example because its pod froze or lost the network: a transaction left idle for 60 seconds
is ended and rolled back, well inside the shortest settle window allowed. The database logs
`terminating connection due to idle-in-transaction timeout` and nothing the transaction wrote is
kept. The request it belonged to fails with an error, and work a service takes from a stream is
delivered to it again.

The refusal covers the two databases, which is where a tenant's records live. The broker, the
key-value store and the object store are reclaimed by the same passes but have no equivalent refusal.
A message or object arriving late there is collected on a later pass rather than turned away, which
is also why the deletion is not reported as finished until every one of them has stayed clean for the
settle window.

### Outbound connectors and notifications {#connectors-and-notifications}

Outbound connectors are covered by the same refusal, and it matters most there. Everywhere else, a
message admitted a moment too late is data that stays on the platform until the reclamation reaches
it. An outbound connector sends a tenant's data to a system you own but the platform does not (a
webhook endpoint, an MQTT broker, a Kafka topic, an SNS or SQS queue), and once it has been sent, no
later pass can take it back. So a connector dispatch for a deleted tenant is refused and discarded
rather than held for inspection.

Notifications stop too. A deleted tenant's alarms no longer send email or fire notification webhooks,
and open alarms stop escalating. This matters because it needs no device traffic to happen:
escalation re-pages open, unacknowledged alarms on a timer. Without this refusal, a deleted tenant's
on-call recipients would keep being paged about alarms belonging to a tenant that no longer exists.

:::warning Refusals are not instant
An action already in flight when the delete lands will complete, and refusals begin within a minute
of the delete rather than instantly. If a deletion must guarantee that *nothing* further reaches an
external system or recipient, disable the tenant's connectors and notification policies before
deleting it.
:::

## What you can see today {#visibility}

A tenant that is being deleted has a **Deletion** tab on its detail page in the admin console. It
answers the question an operator actually has, *is it done, and if not, why not*, with:

- a plain-language status line;
- anything that is blocking;
- a row per storage system showing whether that system is clean, still holding data, or retrying
  after a failure.

Those three row states are kept apart on purpose. Data still held will not clear until someone
changes something, while a failure clears itself on the next sweep.

Some systems add a short note to a clean line, saying what they *declined* to look at: a telemetry
database that does not exist on this instance, for example, or the internal caches a deletion
deliberately exempts. A note is a footnote to "clean", not a problem to act on.

Completed deletions live at **Admin → Deletions**, not on a tenant page. Finishing a deletion removes
the tenant, so a finished deletion has no tenant page left to appear on. That instance-wide list is
the durable record: token, when it was requested, when it completed, how much was erased, and what
each storage system reported. Open it when someone asks you to show that a customer's data was
erased.

Two actions are deliberately missing from it:

- No **retry**. Every sweep already retries, so a button would imply the absence of one.
- No **force-complete**. It is the one action that could record an erasure that did not happen.
