---
sidebar_position: 6
title: Tenant Deletion
---

# Tenant Deletion

Deleting a tenant is a **lifecycle, not a single action**. The delete cuts access
immediately; reclaiming the tenant's data happens afterwards, in the background, and the
tenant's token does not become available for reuse until that finishes.

This page is about what happens between those two moments — what an operator should
expect, what can hold a deletion open, and what the platform deliberately keeps.

## What happens, and when {#timeline}

| | |
|---|---|
| **Immediately** | Sign-in is revoked. The tenant is disabled and marked as deleting, and the moment of the cut is recorded. New device connections, new ingest and new command dispatch are refused within about a minute. |
| **Within a minute or two** | A background coordinator begins reclaiming the tenant's data from every storage system that holds it, and keeps going until each one reports that it holds nothing. |
| **After at least 12 hours** | The tenant's record is removed and its token becomes available again. |

The delete itself is **idempotent**: deleting a tenant that does not exist, or one that
is already being deleted, succeeds and changes nothing. A teardown script that half
failed can simply be run again.

:::note Memberships must be removed first
A tenant that still has user memberships is **refused**, because removing those is what
actually revokes people's access to it. Remove the memberships, then delete the tenant.
:::

## Why the token is held for 12 hours {#token-hold}

This is the part most likely to be surprising, so it is worth being precise: the
tenant's **data** is typically gone within the first minute or two. The **token** is
what stays reserved.

Two separate waits have to pass before the platform will say a deletion is finished.

**Every storage system must report clean, and keep reporting clean.** One clean answer
is not the claim it looks like — a sweep can finish just as a write that was already in
flight lands behind it. So the platform requires a sustained quiet period (five minutes
by default) and restarts that clock whenever a pass still finds something to erase.

**No connection from before the delete may still be able to write.** A device that was
already connected keeps its broker credential until that credential expires, up to 12
hours. Its credentials have been erased so it cannot reconnect — but the existing
connection survives. Releasing the token before that elapses would let a straggler's
writes land under a name a *new* tenant could then be created at, and inherit. That is
the exact problem this whole process exists to prevent, so the wait is measured from the
moment of the delete and matches the credential lifetime.

Until both have passed, the token stays taken, and trying to create a tenant at it
returns an error saying so.

## What is erased {#erased}

A tenant's data does not live in one place, so the reclamation asks each storage system
in turn:

- the **main database**, across every functional area's schema in it;
- the **telemetry database**, where the event history lives;
- the **message broker** — the tenant's streams, plus the MQTT gateway's own session
  state, queued messages and subscriptions;
- the **key-value store** — cached lookups and resolutions;
- the **live event-processing engine** — open detection windows, running timers and
  armed absence checks, which exist in memory and are asked to evict the tenant rather
  than queried;
- the **object store** — uploaded assets such as a tenant's branding logo.

A deletion cannot be reported as finished until **all** of them report clean. A storage
system that is unreachable is retried on the next pass rather than skipped.

## What is deliberately kept {#retained}

Some records survive on purpose, and none of them contains the tenant's own data:

- **People.** A user account is instance-wide, not owned by a tenant. What is removed is
  the membership that linked them to it.
- **Instance-level definitions** — roles, tiers, OAuth clients, signing keys and system
  settings — which exist once per installation and outlive any tenant.
- **The deletion record.** Each deletion writes a durable record of what was erased and
  when, plus a line per storage system. It deliberately does **not** keep the tenant's
  name or contact details — a record retaining those would leave the erasure's own
  evidence as the last place the customer's details lived.

## What can hold a deletion open {#stalled}

A deletion that is not finishing is almost always one of these, and each names itself in
the platform's logs:

**Event processing is not running.** The live engine's state can only be erased by the
process that holds it. If that service is scaled to zero, the deletion waits — correctly,
because the data really is still there. Starting the service resolves it on the next
pass.

**No object storage is configured, but the tenant uploaded something.** The reference to
the object is known and the object itself cannot be reached. Configure the storage
backend that holds it, or remove the object out of band.

**A storage system is unreachable.** Treated as "try again", not as a failure. The
deletion resumes by itself once the system is back.

None of these lose the deletion. The work list is the tenant's own record, so a
coordinator that was stopped, a replica that was rescheduled, and a system that was down
for a week all converge on the next pass.

## Configuration {#configuration}

These settings live under `tenantPurge` in the user-management service. `0` means "use
the default" for all three.

| Setting | Default | Meaning |
|---|---|---|
| `intervalSeconds` | `60` | How often the coordinator runs. A **negative value disables it** entirely. |
| `settleSeconds` | `300` | How long every storage system must keep reporting clean. Must be greater than 140. |
| `tokenHoldSeconds` | `43200` | How long a deleted token stays reserved, measured from the delete. |

**Disabling the coordinator is a supported operational lever** — a maintenance window in
which nothing should be deleting rows. It is safe: pending deletions stay pending rather
than being lost, and no token is released while it is off.

**The two windows cannot be disabled.** A negative value for either is rejected when the
configuration loads, rather than taking effect. Turning off the settle window would mean
declaring data erased without ever having observed it gone; turning off the token hold
would release a name while a pre-existing session could still write under it. Lowering
`tokenHoldSeconds` is a real choice — a deleted tenant's name is unavailable for that
long — but it is a choice about correctness, not tidiness.

## During a deletion {#during}

**Device traffic stops within about a minute.** New connections, ingest on every
transport, and command dispatch are all refused once the deletion is picked up. A
refused device is answered exactly as one that is over its rate limit, and backs off and
retries.

**Already-connected devices are not disconnected.** They keep their existing broker
credential until it expires — up to 12 hours — and cannot reconnect once it does. This
is the reason for the token hold above.

**If the user-management service is unreachable, these refusals stop applying** until it
is back. That is deliberate: the alternative would make user-management a hard dependency
of device connectivity for every tenant on the instance. The refusals exist to stop
traffic early so the reclamation is not chasing data that is still arriving; the erasure
itself does not depend on them.

**Writing to the databases is refused separately, and that refusal does not depend on any
other service.** From the moment the reclamation first reaches the main database and the
telemetry database, each refuses every write for the deleted tenant — whether it arrives
through the API, through a stream a service consumes, or through a background job of its
own — and the check runs inside the same database transaction as the write it is refusing.
This is what makes the guarantee an erasure rather than a cleanup: reclaimed rows cannot
come back while the deletion is in progress, even if everything that was supposed to stop
the traffic earlier failed to. The refusal is lifted only when the deletion completes,
which is also when the token is released, so a new tenant created at that token writes
normally from its first request.

This covers the two databases, which is where a tenant's records live. The broker, the
key-value store and the object store are reclaimed by the same passes but have no
equivalent refusal, so a message or object arriving late is collected on a later pass
rather than being turned away — which is also why the deletion is not reported as
finished until every one of them has stayed clean for the settle window.

**Outbound connectors are covered by the same refusal**, and it matters most there. Every
other place this applies, a message admitted a moment too late is data that stays on the
platform until the reclamation reaches it. An outbound connector sends a tenant's data to
a system you own but the platform does not — a webhook endpoint, an MQTT broker, a Kafka
topic, an SNS or SQS queue — and once it has been sent, no later pass can take it back.
So a connector dispatch for a deleted tenant is refused and discarded rather than held for
inspection.

**Notifications stop too.** A deleted tenant's alarms no longer send email or fire
notification webhooks, and open alarms stop escalating. This one is worth calling out
because it does not need any device traffic to happen: escalation re-pages open,
unacknowledged alarms on a timer, so without this refusal a deleted tenant's on-call
recipients would keep being paged about alarms belonging to a tenant that no longer exists.

The window before these refusals take effect still applies: an action already in flight when
the delete lands will complete, and refusals begin within a minute of the delete rather than
instantly. If a deletion must guarantee that *nothing* further reaches an external system or
recipient, disable the tenant's connectors and notification policies before deleting it.

## What you can see today {#visibility}

A tenant that is being deleted carries a **Deletion** tab on its detail page in the admin
console. It answers the question an operator actually has — *is it done, and if not, why
not* — with a plain-language status line, anything that is blocking, and a row per storage
system showing whether that system is clean, still holding data, or retrying after a
failure. Those three are kept apart on purpose: data still held will not clear until
someone changes something, while a failure clears itself on the next sweep.

Some systems add a short note to a clean line, saying what they *declined* to look at — a
telemetry database that does not exist on this instance, for example, or the internal
caches a deletion deliberately exempts. A note is a footnote to "clean", not a problem to
act on.

**Completed deletions live at Admin → Deletions**, not on a tenant page, and the reason is
worth knowing: finishing a deletion removes the tenant, so a finished deletion has no
tenant page left to appear on. That instance-wide list is the durable record — token, when
it was requested, when it completed, how much was erased, and what each storage system
reported. It is the page to open when someone asks you to show that a customer's data was
erased.

Two things it deliberately does not offer. There is no **retry**: every sweep already
retries, so a button would imply the absence of one. And there is no **force-complete**: it
is the one action that could record an erasure that did not happen.
