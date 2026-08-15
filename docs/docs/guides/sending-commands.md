---
sidebar_position: 3
title: Sending a Command
---

# Sending a command

This guide covers the operator's half of two-way command dispatch: issuing a command,
telling an accepted one from a refused one, and following it to an outcome. The device's
half — receiving a command and reporting what happened — is in
[Connecting a device](./connecting-a-device.md#responding-to-a-command). The lifecycle
those two halves move a command through is in [Commands](../concepts/commands.md).

Issuing, reading and cancelling are on the `command-delivery` endpoint,
`https://<your-host>/api/command-delivery/graphql`, with a tenant access token. Issuing and
cancelling need the **`command:write`** authority; reading command history needs
**`command:read`**.

The one exception is the first step below. Finding out what a device accepts is a
`device-management` query — a different endpoint,
`https://<your-host>/api/device-management/graphql`, and a different authority,
**`device:read`**.

## Find out what the device accepts {#find-out-what-the-device-accepts}

A device's command vocabulary comes from its profile, so ask `device-management` rather
than guessing:

```graphql
query {
  deviceCommandVocabulary(deviceToken: "sensor-001") {
    constrained
    commands { commandKey name description parameterSchema }
  }
}
```

:::warning `commandKey` is the identifier; `name` is a label
A `PublishedCommand` carries both. The enqueue gate matches on **`commandKey`**, and that
is the value you put in `createCommand`'s field — which is confusingly called `name`. The
`name` on the vocabulary entry is a human-readable display label and is matched against
nothing. Send the label and you get `COMMAND_NOT_IN_VOCABULARY` for a command the device
plainly supports.
:::

**Read `constrained`, not the length of `commands`.** When `constrained` is `false` the
list is empty and *any* command key is accepted — an empty list does not mean the device
takes nothing, it means its profile declares no vocabulary. When `constrained` is `true`,
the key must match one of the entries exactly, **including case**, and the payload is
validated against that command's parameter schema.

## Issue it

```graphql
mutation {
  createCommand(request: {
    token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11",
    deviceToken: "sensor-001",
    name: "reboot",          # the commandKey, not the display name
    payload: "{\"delaySeconds\":5}",
    expiresAt: "2026-08-15T00:00:00Z"
  }) {
    command { token status queuedTime }
    rejection { code reason }
  }
}
```

`token` is yours to choose and is how you refer to the command afterwards. `payload` and
`metadata` are JSON **strings**. `expiresAt` is optional — see [Set a
TTL](#set-a-ttl-you-can-live-with).

Re-issuing with a token already in use does not create a second command: the original is
returned unchanged. That makes a retry after a network failure safe, which matters, because
a command is a physical actuation and you do not want a dropped response to reboot a device
twice.

## When an enqueue is refused {#when-an-enqueue-is-refused}

:::danger Check `rejection`, not just for errors
`createCommand` returns **exactly one** of `command` or `rejection`. A refused enqueue is a
successful GraphQL response carrying a `rejection` — **not** a GraphQL error. A client that
only checks the `errors` array reads a refusal as a success and reports a command that was
never created.
:::

A rejection is a decided verdict rather than a failure, and the distinction is deliberate:
a rejection says the request is wrong and describes exactly how, while a GraphQL error says
the platform could not answer at all. A machine caller that cannot tell them apart retries a
permanently-invalid command until its redelivery cap gives up — which looks identical to an
outage.

**Branch on `code`. Never on `reason`** — the reason is prose for a person and its wording
may change.

| `code` | Meaning | Retry? |
|---|---|---|
| `HELD_CEILING_EXCEEDED` | The tenant is at its limit of **undelivered** commands — everything still `QUEUED`, `HELD` or `PARKED`, not only what is held for absent devices. | **Yes** — it clears as those commands go out |
| `DEVICE_NOT_FOUND` | No device with that token in this tenant. | No |
| `COMMAND_NOT_IN_VOCABULARY` | The profile constrains commands and this key is not one. Check the casing. | No |
| `PAYLOAD_SCHEMA_VIOLATION` | The payload broke the command's parameter schema — unknown parameter, wrong type, out of range, or a required one missing. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | The string is not valid JSON. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` is not an RFC3339 timestamp. | No |
| `COMMAND_REJECTED` | A rejection arrived carrying no classification. | No |

**The list is open.** Treat a code you do not recognize as a refusal you cannot classify —
never as a success.

Only `HELD_CEILING_EXCEEDED` is temporary. Every other code describes a request that will be
just as wrong next time, so retrying it wastes attempts and hides a real defect from whoever
could fix it.

A tenant whose fleet is entirely present can still hit the ceiling: it bounds *undelivered*
work, and queued commands count while they wait for the next delivery tick. See [How much
backlog a tenant may hold](../concepts/commands.md#held-command-ceiling).

## Follow it to an outcome

There is **no subscription** for commands — poll. Fetch a specific one by token:

```graphql
query {
  commandsByToken(tokens: ["6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11"]) {
    token status sentTime respondedTime responsePayload error
  }
}
```

Or search, filtering on one state with `status` or a set of them with `statuses`:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    statuses: ["HELD", "PARKED", "SENT"]
  }) {
    results { token name status queuedTime }
    pagination { totalRecords }
  }
}
```

`statuses` is the one to reach for when what you care about is a set — "everything still in
flight for this device" is `HELD`, `PARKED` and `SENT`: the commands withheld because the
device is away, the ones published to a device that turned out not to be awake, and the ones
dispatched and unanswered. An empty list is ignored rather than matching nothing.

What each terminal state tells you is in
[Commands](../concepts/commands.md#command-lifecycle); the pair worth internalizing is that
**`EXPIRED` means it never got to a device and `TIMEOUT` means it did** — a run of the first
points at dispatch, a run of the second points at the device.

## Cancel one

```graphql
mutation {
  cancelCommand(token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11") { token status }
}
```

Legal from `QUEUED`, `HELD` and `PARKED` — the states in which the platform is still holding
the command. Those are the useful cases: the command was withheld for an absent device, or
published to one that turned out to be asleep, and it can be called off before the platform
delivers it, which is much of the point of holding it rather than firing it into the dark.
It records `CANCELLED`.

**A `SENT` command is not cancelled.** Cancelling does not recall a dispatched command, and
driving one to `CANCELLED` would stop no actuation — it would only make the platform discard
the device's real answer when it arrives, so the device acts, the response vanishes, and the
record says the operation was called off. The call therefore succeeds and returns the command
unchanged, still `SENT`. Cancel races delivery, and losing that race is ordinary.

**Cancelling an already-terminal command is not an error either.** It too is returned
unchanged, with whatever status it reached. So a cancel that loses the race with a response
looks like a successful call that returned `SUCCESSFUL`.

Both of those are the same instruction: **check the `status` you get back** rather than
assuming the cancel took effect. A token matching no command *is* an error.

This is exactly the brake `cancelCommandBatch` applies to a whole fleet write — same states
cancelled, same line at `SENT`. See [Cancelling a
batch](../concepts/commands.md#cancelling-a-batch).

## Set a TTL you can live with {#set-a-ttl-you-can-live-with}

Every command carries one. Pass `expiresAt` to set it, or the platform default of **seven
days** applies.

Seven days is a long time to wait to learn a command failed. If your devices do not report
outcomes, a command sits in `SENT` for the whole week before `TIMEOUT` records what you
already suspected. Set your own `expiresAt` to whatever "still useful" means for that
actuation — a reboot that has not landed in ten minutes is not going to.

## Commanding many devices at once

Everything above issues one command to one device. To send one command to a whole fleet —
named explicitly, or resolved from an entity group — as a single operation you can audit and
call off, see [Commanding a fleet](./commanding-a-fleet.md). It is not a loop of this
mutation: it pins the group's membership as of the moment it fires, records which devices
were refused and why, and cancels as one operation.

## Four operations that are not for you {#operations-that-are-not-for-you}

`markCommandSent`, `releaseHeldCommands` and `parkCommand` appear on this schema but are
gated on **system-tier** authorities (`command:claim`, `command:wake` and `command:park`)
that a tenant access token does not carry. They exist for transports that own a device's
connection — an LwM2M device draining its backlog over the session it just opened, a broker
reporting that a device came back, or a transport handing a command back because the device
it was published toward turned out to be unreachable — and calling them from an application
would fight the delivery sweep for control of a physical actuation.

`drainableCommands` is the read those transports do first, and it is gated on
**`command:claim`** — the same authority as `markCommandSent` rather than a fourth one of its
own, since a caller entitled to claim a device's commands is exactly the caller entitled to
find out which ones there are to claim. Given a device token it returns the commands still
waiting for that device — `HELD` and `PARKED`, minus anything already past its expiry horizon
— **oldest first**, bounded by `limit`: absent or not positive gives 32, and 1000 is the
ceiling.

The ordering is the substance of the query rather than a nicety. A firmware update's write
has to reach the device before its execute, so a backlog drained in any other order does not
merely arrive late — it runs the rollout backwards. `command:read` does not open this query,
and an application has no use for it in any case: to see what a device has waiting, use the
[`commands` query](#follow-it-to-an-outcome) with `statuses: ["HELD", "PARKED"]`.
