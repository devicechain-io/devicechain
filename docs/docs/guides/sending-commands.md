---
sidebar_position: 3
title: Sending a Command
---

# Sending a Command

This guide covers the operator's half of two-way command dispatch: you issue a command, tell
an accepted one from a refused one, and follow it to an outcome. The device's half — receiving
a command and reporting what happened — is in
[Connecting a device](./connecting-a-device.md#responding-to-a-command). The lifecycle those
two halves move a command through is in [Commands](../concepts/commands.md).

You issue, read and cancel commands on the `command-delivery` endpoint,
`https://<your-host>/api/command-delivery/graphql`, with a tenant access token. Issuing and
cancelling need the `command:write` authority. Reading command history needs
`command:read`.

The first step below is the one exception. Finding out what a device accepts is a
`device-management` query. It uses a different endpoint,
`https://<your-host>/api/device-management/graphql`, and a different authority,
`device:read`.

## Find out what the device accepts {#find-out-what-the-device-accepts}

A device's command vocabulary comes from its profile, so ask `device-management` rather than
guessing:

```graphql
query {
  deviceCommandVocabulary(deviceToken: "sensor-001") {
    constrained
    commands { commandKey name description parameterSchema }
  }
}
```

:::warning `commandKey` is the identifier; `name` is a label
A `PublishedCommand` carries both. The check that accepts or refuses a command matches on
`commandKey`, and that is the value you put in `createCommand`'s field — which is confusingly
called `name`. The vocabulary entry's `name` is a display label and is matched against nothing.
Send the label and you get `COMMAND_NOT_IN_VOCABULARY` for a command the device plainly
supports.
:::

Read `constrained`, not the length of `commands`:

- **`constrained: false`** — the list is empty and *any* command key is accepted. The empty
  list does not mean the device takes nothing; it means its profile declares no vocabulary.
- **`constrained: true`** — the key must match one of the entries exactly, including case. The
  payload is validated against that command's parameter schema.

## Issue a command {#issue-it}

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

- `token` is yours to choose. You use it to refer to the command afterwards.
- `payload` and `metadata` are JSON **strings**.
- `expiresAt` is optional. See [Set a TTL](#set-a-ttl-you-can-live-with).

Re-issuing with a token already in use does not create a second command; you get the original
back unchanged. That makes a retry after a network failure safe. It matters because a command
is a physical actuation, and a dropped response must not reboot a device twice.

This replay applies only to commands **you** own. If the token is held by a command the
platform minted for a batch, you get `TOKEN_IN_USE` instead. Handing you another device's
actuation as though it were your own would be worse than saying no.

## When an enqueue is refused {#when-an-enqueue-is-refused}

:::danger Check `rejection`, not just for errors
`createCommand` returns **exactly one** of `command` or `rejection`. A refused enqueue is a
successful GraphQL response carrying a `rejection`, not a GraphQL error. A client that only
checks the `errors` array reads a refusal as a success and reports a command that was never
created.
:::

The distinction is deliberate. A rejection is a decided verdict: the request is wrong, and the
rejection says exactly how. A GraphQL error means the platform could not answer at all. A
machine caller that cannot tell them apart retries a permanently invalid command until its
redelivery cap gives up, which looks identical to an outage.

Branch on `code`, never on `reason`. The reason is prose for a person, and its wording may
change.

| `code` | Meaning | Retry? |
|---|---|---|
| `HELD_CEILING_EXCEEDED` | The tenant is at its limit of **undelivered** commands — everything still `QUEUED`, `HELD` or `PARKED`, not only what is held for absent devices. | **Yes** — it clears as those commands go out |
| `DEVICE_NOT_FOUND` | No device with that token in this tenant. | No |
| `COMMAND_NOT_IN_VOCABULARY` | The profile constrains commands and this key is not one. Check the casing. | No |
| `PAYLOAD_SCHEMA_VIOLATION` | The payload broke the command's parameter schema — unknown parameter, wrong type, out of range, or a required one missing. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | The string is not valid JSON. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` is not an RFC3339 timestamp. | No |
| `TOKEN_IN_USE` | The token is held by a command you do not own — in practice one the platform minted for a batch. | No — pick another token |
| `COMMAND_REJECTED` | A rejection arrived carrying no classification. | No |

The list is open. Treat a code you do not recognize as a refusal you cannot classify, never
as a success.

Only `HELD_CEILING_EXCEEDED` is temporary. Every other code describes a request that will be
just as wrong next time. Retrying it wastes attempts and hides a real defect from whoever could
fix it.

A tenant whose fleet is entirely present can still hit the ceiling. The ceiling bounds
*undelivered* work, and queued commands count while they wait for the next delivery pass. See
[How much backlog a tenant may hold](../concepts/commands.md#held-command-ceiling).

## Follow it to an outcome {#follow-it-to-an-outcome}

There is **no subscription** for commands, so poll. Fetch a specific command by token:

```graphql
query {
  commandsByToken(tokens: ["6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11"]) {
    token status sentTime respondedTime responsePayload error
  }
}
```

Or search, filtering on one state with `status` or on a set of states with `statuses`:

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

Use `statuses` when you care about a set. "Everything still in flight for this device" is:

- `HELD` — withheld because the device is away.
- `PARKED` — published to a device that turned out not to be awake.
- `SENT` — dispatched and unanswered.

An empty `statuses` list is ignored rather than matching nothing.

What each terminal state tells you is in [Commands](../concepts/commands.md#command-lifecycle).
The pair to remember: `EXPIRED` means the command never reached a device, and `TIMEOUT`
means it did. A run of `EXPIRED` points at dispatch; a run of `TIMEOUT` points at the device.

## Cancel a command {#cancel-one}

```graphql
mutation {
  cancelCommand(token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11") { token status }
}
```

Cancelling is legal from `QUEUED`, `HELD` and `PARKED` — the states in which the platform is
still holding the command — and records `CANCELLED`. The useful cases are a command withheld
for an absent device, or one published to a device that turned out to be asleep. Either can be
called off before the platform delivers it, which is much of the point of holding it rather
than firing it into the dark.

A **`SENT` command is not cancelled**. Cancelling does not recall a dispatched command. Driving
it to `CANCELLED` would stop no actuation; it would only make the platform discard the device's
real answer when it arrives. The device would act, the response would vanish, and the record
would say the operation was called off. So the call succeeds and returns the command unchanged,
still `SENT`. Cancel races delivery, and losing that race is ordinary.

Cancelling an already-terminal command is not an error either. It is returned unchanged,
with whatever status it reached. A cancel that loses the race with a response therefore looks
like a successful call that returned `SUCCESSFUL`.

In both cases, **check the `status` you get back** rather than assuming the cancel took effect.
A token that matches no command *is* an error.

`cancelCommandBatch` applies exactly this brake to a whole fleet write: the same states are
cancelled, and it stops at the same line, `SENT`. See
[Cancelling a batch](../concepts/commands.md#cancelling-a-batch).

## Set a TTL you can live with {#set-a-ttl-you-can-live-with}

Every command carries a TTL. Pass `expiresAt` to set it; otherwise the platform default of
**seven days** applies.

Seven days is a long time to wait to learn a command failed. If your devices do not report
outcomes, a command sits in `SENT` for the whole week before `TIMEOUT` records what you already
suspected. Set `expiresAt` to whatever "still useful" means for that actuation — a reboot that
has not landed in ten minutes is not going to.

## Commanding many devices at once {#commanding-many-devices-at-once}

Everything above issues one command to one device. To send one command to a whole fleet — named
explicitly, or resolved from an entity group — as a single operation you can audit and call off,
see [Commanding a fleet](./commanding-a-fleet.md). A fleet command is not a loop of this
mutation. It pins the group's membership as of the moment it fires, records which devices were
refused and why, and cancels as one operation.

## Five operations that are not for you {#operations-that-are-not-for-you}

`markCommandSent`, `confirmCommandDispatch`, `releaseHeldCommands` and `parkCommand` appear on
this schema, but they are gated on **system-tier** authorities that a tenant access token does
not carry: `command:claim` for the first two, then `command:wake` and `command:park`. They exist
for transports that own a device's connection:

- an LwM2M device draining its backlog over the session it just opened
- an LwM2M adapter confirming a delivery is still current immediately before carrying it out
- a broker reporting that a device came back
- a transport handing a command back because the device it was published toward turned out to
  be unreachable

Calling them from an application would fight the platform's own delivery process for control
of a physical actuation.

`drainableCommands` is the read those transports do first. It is gated on **`command:claim`**,
the same authority as `markCommandSent`, rather than a fourth one of its own: a caller entitled
to claim a device's commands is exactly the caller entitled to find out which ones there are to
claim. Given a device token, it returns the commands still waiting for that device — `HELD` and
`PARKED`, minus anything already past its expiry horizon — **oldest first**, bounded by `limit`.
An absent or non-positive `limit` gives 32, and 1000 is the ceiling.

The ordering is the point of the query. A firmware update's write has to reach the device
before its execute, so a backlog drained in any other order does not merely arrive late — it
runs the rollout backwards.

`command:read` does not open this query, and an application has no use for it anyway. To see
what a device has waiting, use the [`commands` query](#follow-it-to-an-outcome) with
`statuses: ["HELD", "PARKED"]`.
