---
sidebar_position: 7
title: Commanding a Fleet
---

# Commanding a fleet

A **command batch** issues one command to many devices as a single, recorded operation. The
devices are either named explicitly or resolved from an entity group, and what comes back
is a persisted record of what the platform tried to do — how many devices the target
resolved to, how many were actually enqueued, and which ones were refused and why.

Everything a single command does still happens per device: each one is validated against
that device's capability contract, held if the device is away, tracked through the same
lifecycle, and expires on the same TTL. Read [Sending a command](./sending-commands.md)
first — this guide only covers what changes when the target is a fleet.

A loop of `createCommand` calls can command the same devices. What it cannot do is leave a
record of what was attempted, pin the group's membership so a selector edit mid-loop does
not change the target, or be called off as one operation.

Batches live on the `command-delivery` endpoint,
`https://<your-host>/api/command-delivery/graphql`, with a tenant access token. Firing and
cancelling need **`command:write`**; reading batch records needs **`command:read`**.

:::warning A group target needs `device:read` as well
Resolving a group to its members is a read of the device registry that the platform
performs under its own identity, and the answer comes back to you — the refusal list names
device tokens, and `resolved` discloses the group's size. So targeting a group, reading a
group-targeted batch record, and cancelling one each require **`device:read`** on top of
the command authority. Naming devices explicitly needs only the command authority, because
a caller doing that already knows them.
:::

## Name the target: devices, or a group

`deviceTokens` and `groupToken` are alternatives. Supply **exactly one** — both or neither
is refused with `BATCH_TARGET_AMBIGUOUS` rather than resolved by a precedence rule, because
a caller that sent both does not know which fleet it just actuated.

**Naming devices.** At most **10,000** tokens in one request; more is `BATCH_TOO_LARGE` and
you must split the operation. The order is meaningful — a partially-admitted batch admits
in the order you gave, so put the devices you care about most first. A token you name twice
is counted once.

**Naming a group.** The group must collect **devices**, and a dynamic group must have been
**published** — a batch resolves the published selector, never the draft, because a fleet
actuation must not follow whatever someone last typed into the editor. Pass `groupVersion`
to pin a specific frozen version, or omit it for the active published one. Naming a version
for a static group is refused rather than ignored, as is naming one with no group at all. A
group that resolves to more than 10,000 devices is `BATCH_TOO_LARGE` — the walk refuses
rather than commanding the first 10,000 and reporting success.

:::info A group target is frozen at fire time
The record stores the group version the target set was resolved against, so an audit can
answer what the group *meant* when the batch fired even after someone edits the selector.
Editing a dynamic group afterwards changes nothing about what already went out. The stored
version is null for a static group, which is never versioned, and for a device-list batch.
See [Facets and dynamic groups](../concepts/domain-model.md#facets-and-dynamic-groups).
:::

## Decide what a partial fan-out means {#decide-what-a-partial-fan-out-means}

On a real fleet some devices will not be able to receive the command — one is not in the
registry, another's profile does not declare the command, a third does not fit under the
tenant's ceiling. `allowPartial` is where you say what should happen then:

- **`false`** — if *any* device cannot receive the command, the whole batch is refused and
  **nothing is created**, including the batch record. There is nothing to record, because
  nothing happened. The refusal names the devices responsible.
- **`true`** — best effort. The devices that can receive the command get it; the rest get
  no command row at all and appear in the record's refusal list.

:::warning `allowPartial` has no default — you must send it
It is a non-null Boolean with no default value, so a request that omits it is invalid. That
is deliberate for a field deciding whether a physical actuation may reach some of a fleet
but not all of it: you state your intent rather than inheriting one from a schema you may
not have read.
:::

The flag has one meaning across every refusal reason. It is not a tolerance for capacity
problems only — opting in also accepts that a device whose profile rejects the command is
silently left out.

## Fire it

```graphql
mutation {
  createCommandBatch(request: {
    token: "nightly-reboot-2026-08-14",
    name: "reboot",              # the commandKey, not the display name
    payload: "{\"delaySeconds\":5}",
    groupToken: "pumps-arid-us",
    allowPartial: true
  }) {
    batch {
      token targetKind groupToken groupVersion
      resolved accepted
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
    rejection {
      code reason resolved
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
  }
}
```

`name` is the **`commandKey`** from the device's vocabulary, exactly as in `createCommand` —
see [`commandKey` is the
identifier](./sending-commands.md#find-out-what-the-device-accepts). Every targeted device
receives the same key and the same payload, which is what makes validating a fleet write
affordable in the first place.

`expiresAt` sets the TTL on every command the batch creates, or the platform default of
seven days applies to all of them. `metadata` is recorded on the batch record; it is not
copied onto the individual commands.

:::danger Check `rejection`, not just for errors
`createCommandBatch` returns **exactly one** of `batch` or `rejection`. A refused batch is a
successful GraphQL response carrying a `rejection` — not a GraphQL error. A GraphQL error
instead of either means the batch could not be *decided* at all, and nothing was created, so
the token is unspent and the request can simply be retried.
:::

### The token is an idempotency key

`token` is yours to choose and names the whole operation afterwards. Re-issuing a token that
already names a batch returns **that batch, unchanged** — it is never topped up with more
devices, because admitting more under the same token would make `accepted` a moving number
and the record un-auditable. A retry after a network failure is therefore safe, which
matters more here than for a single command: the request you are unsure about may have
rebooted ten thousand pumps.

There is no `TOKEN_IN_USE` refusal for a batch. A token already in use is not a conflict; it
is a replay.

## When a batch is refused {#when-a-batch-is-refused}

**Branch on `code`. Never on `reason`** — the reason is prose for a person and its wording
may change.

| `code` | Meaning | Retry? |
|---|---|---|
| `BATCH_PARTIAL_REFUSED` | At least one device cannot receive the command and `allowPartial` is off. **Nothing was created.** | **Read the refusals** — each device's own code says whether it will still be refused next time |
| `HELD_CEILING_EXCEEDED` | The batch needs more room than the tenant has for **undelivered** commands. | **Yes** — it clears as the backlog drains |
| `BATCH_TARGET_AMBIGUOUS` | Both targets were given, or neither, or a `groupVersion` with no group. | No |
| `BATCH_TOO_LARGE` | More devices than one batch may command — named explicitly, or resolved from the group. | No — split the operation or narrow the group |
| `BATCH_GROUP_UNUSABLE` | The group does not exist, collects something other than devices, was never published, or the named version does not exist. The group service's own code travels in the reason. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | The string is not valid JSON. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` is not an RFC3339 timestamp. | No |

**The list is open.** Treat a code you do not recognize as a refusal you cannot classify —
never as a success.

`BATCH_PARTIAL_REFUSED` is the one code that cannot answer the retry question by itself, and
that is why the offending devices travel with it: a device missing from the command
vocabulary needs a profile change, while one refused for headroom will succeed once the
backlog drains. A single code cannot say both, so it says neither and defers to the list.

:::info On a rejection, `resolved` is nullable — and null is not zero
`null` means no target set was ever established: the refusal happened before anything was
resolved. `0` means a target that genuinely resolved to no devices, which is a real and
successful batch rather than a refusal.
:::

The rejection's `refusals` list is populated for exactly one code, `BATCH_PARTIAL_REFUSED`,
and is empty for every other — including `HELD_CEILING_EXCEEDED`. That asymmetry is
deliberate. A partial refusal is caused *by* specific devices, so naming them is what saves
you from bisecting a fleet by hand. A ceiling refusal is caused by the tenant's backlog: no
device in the request is at fault, nothing would change if you swapped its members, and a
list there would invite fixing devices that are fine. What to do about it is in `reason`.

## Read the record

```graphql
query {
  commandBatchesByToken(tokens: ["nightly-reboot-2026-08-14"]) {
    token name targetKind groupToken groupVersion allowPartial
    resolved accepted
    refusals { deviceToken code reason }
    refusalCounts { code count }
  }
}
```

Or search, by command key, by group, or by `targetKind` (`DEVICE_LIST` or `GROUP`):

```graphql
query {
  commandBatches(criteria: {
    pageNumber: 1, pageSize: 25,
    groupToken: "pumps-arid-us"
  }) {
    results { token name resolved accepted createdAt }
    pagination { totalRecords }
  }
}
```

:::warning `resolved` and `accepted` describe the moment the batch fired, not now
They are stored facts, not live counts. Command rows are not immortal — they can be
soft-deleted, or erased with a tenant — so deriving `accepted` from a live query would let
it drift below the creation-time truth with no refusal explaining the gap. For present-tense
delivery state, search the commands instead.
:::

### `refusals` is a sample; `refusalCounts` is complete

`refusals` keeps at most **100 entries per code**, so a batch fired at a large group refuses
more devices than the record names. `refusalCounts` is the complete per-code total and is
never truncated, which is what keeps the record self-auditing:

```
resolved = accepted + the sum of refusalCounts
```

That identity always holds. The sample may be short, and comparing its length against the
counts is how you tell that it was capped.

The per-device `code` is the same open vocabulary a single enqueue rejection uses —
`DEVICE_NOT_FOUND`, `COMMAND_NOT_IN_VOCABULARY`, `PAYLOAD_SCHEMA_VIOLATION` relayed from the
device's profile, and `HELD_CEILING_EXCEEDED` for the devices that did not fit under the
tenant's remaining headroom. See [When an enqueue is
refused](./sending-commands.md#when-an-enqueue-is-refused) for what each one means.

## Follow the commands it created

The batch record deliberately does not move. To ask what the fleet write is *doing* — "of
the 5,000 queued, how many have gone out?" — search the commands with `batchToken`:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    batchToken: "nightly-reboot-2026-08-14",
    statuses: ["QUEUED", "HELD", "PARKED"]
  }) {
    results { token deviceToken status queuedTime }
    pagination { totalRecords }
  }
}
```

The individual command tokens are generated by the platform — you chose the batch's token,
not theirs — so `batchToken` is how you find them rather than by constructing a token
yourself.

## Call the whole thing off

```graphql
mutation {
  cancelCommandBatch(token: "nightly-reboot-2026-08-14") {
    cancelled
    alreadySent
    alreadyFinished
    matched
  }
}
```

`cancelled` is the authoritative number: that many commands moved from `QUEUED`, `HELD` or
`PARKED` to `CANCELLED` and will not be delivered. `alreadySent` were already dispatched to
their devices, and **those devices will still act on them**. `alreadyFinished` had already
reached a terminal state — `SUCCESSFUL`, `FAILED`, `TIMEOUT`, `EXPIRED` or `CANCELLED`.

This is the same brake `cancelCommand` applies to a single command: both cancel `QUEUED`,
`HELD` and `PARKED`, and neither touches `SENT`. Why `SENT` is the line is in
[Cancelling a batch](../concepts/commands.md#cancelling-a-batch).

**It never refuses.** A brake that declined to engage because part of the fleet had already
moved would leave the rest of the fleet commanded, which is the worst available outcome. So
a batch where every command has already been sent is a successful call reporting
`cancelled: 0` — read the counts rather than assuming the call did nothing. A token matching
no batch *is* a GraphQL error.

Cancelling needs **`command:write`**, and a group-targeted batch additionally needs
**`device:read`**, for the same reason firing one does.

:::info `matched` is a live count, and the four numbers need not add up
`matched` is how many of the batch's command rows were live at that moment, not how many it
created. Rows removed since — by a purge, or a deletion — are simply not there to match, so
`matched` below the batch's `accepted` is ordinary and says nothing about the cancel.

`matched` can also *exceed* `cancelled + alreadySent + alreadyFinished`. A command whose
delivery failed can return to the queue between the cancel and the count, and such a command
is left out of all three buckets rather than folded into `alreadyFinished` — reporting a live
command as a finished one is the single thing this vocabulary exists to prevent. Cancel again
and it is caught. It is rare and self-correcting: once a cancel is recorded, a failed delivery
retires the command instead of requeueing it.
:::

The batch record itself is stamped with `cancelledAt` and `cancelledCount`, so the
cancellation is as auditable as the fan-out was. `cancelledCount` is what that call caught,
and the stamp is **first-wins**: a second cancel does not overwrite what the first recorded.

## What a batch does not change

A batch is bounded by exactly the same limits a loop of single commands would hit. It is
admitted against the tenant's [ceiling on undelivered
commands](../concepts/commands.md#held-command-ceiling), minus the [share reserved for the
platform's own delivery](../concepts/commands.md#delivery-machinery-reserve) — so there is
no way around either, and no advantage to one shape over the other.

What that means in practice: with `allowPartial` on, a large fan-out can be admitted only
partially because the tenant is near its ceiling, and the devices that did not fit come back
as per-device `HELD_CEILING_EXCEEDED` refusals. With it off, the whole batch is refused with
that code and nothing is created. Either way it is a temporary condition rather than a defect
in the request — once the backlog drains, a **new** token will command the rest. Replaying
the original token cannot, because a replay returns the batch you already have.
