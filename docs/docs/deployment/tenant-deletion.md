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

**Outbound connectors are not yet covered.** Between the delete and the pass that erases
a tenant's automation rules, those rules can still fire their outbound actions. If a
tenant's deletion needs to guarantee that nothing further is sent to an external system,
disable its connectors before deleting it.

## What you can see today {#visibility}

The admin console shows whether a tenant is active or being deleted, and when the delete
was requested.

**The per-system detail is not yet exposed.** The platform records, for every deletion,
which storage systems have reported clean, what any of them is still holding and why, and
how much was erased — but that record is currently readable only in the database and in
the service logs. Surfacing it is planned.
