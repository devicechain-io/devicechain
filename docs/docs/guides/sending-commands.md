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

Everything here is on the `command-delivery` GraphQL endpoint,
`https://<your-host>/api/command-delivery/graphql`, with a tenant access token. Issuing
needs the **`command:write`** authority; reading command history needs **`command:read`**.

## Find out what the device accepts

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
| `HELD_CEILING_EXCEEDED` | The tenant is already holding its limit of commands for absent devices. | **Yes** — it clears as those devices return |
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
    statuses: ["HELD", "SENT"]
  }) {
    results { token name status queuedTime }
    pagination { totalRecords }
  }
}
```

`statuses` is the one to reach for when what you care about is a set — "everything still in
flight for this device" is `HELD` plus `SENT`, the commands withheld for it and the ones
already dispatched and unanswered. An empty list is ignored rather than matching nothing.

What each terminal state tells you is in
[Commands](../concepts/commands.md#command-lifecycle); the pair worth internalizing is that
**`EXPIRED` means it never left the platform and `TIMEOUT` means it did** — a run of the
first points at dispatch, a run of the second points at the device.

## Cancel one

```graphql
mutation {
  cancelCommand(token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11") { token status }
}
```

Legal from any non-terminal state — `QUEUED`, `HELD` or `SENT`. Cancelling a held command
is the useful case: it was withheld for an absent device and can be called off before that
device ever sees it, which is much of the point of holding it rather than firing it into
the dark. It records `CANCELLED`.

**Cancelling an already-terminal command is not an error.** The command is returned
unchanged, with whatever status it reached. So a cancel that loses the race with a response
looks like a successful call that returned `SUCCESSFUL` — check the `status` you get back
rather than assuming the cancel took effect. A token matching no command *is* an error.

Cancelling does not recall a dispatched command: `SENT` means it is already with the
device. Cancel races delivery, and losing that race is ordinary.

## Set a TTL you can live with {#set-a-ttl-you-can-live-with}

Every command carries one. Pass `expiresAt` to set it, or the platform default of **seven
days** applies.

Seven days is a long time to wait to learn a command failed. If your devices do not report
outcomes, a command sits in `SENT` for the whole week before `TIMEOUT` records what you
already suspected. Set your own `expiresAt` to whatever "still useful" means for that
actuation — a reboot that has not landed in ten minutes is not going to.

## Two mutations that are not for you

`markCommandSent` and `releaseHeldCommands` appear on this schema but are gated on
**system-tier** authorities (`command:claim` and `command:wake`) that a tenant access token
does not carry. They exist for transports that own a device's connection — an LwM2M device
draining its backlog over the session it just opened, or a broker reporting that a device
came back — and calling them from an application would fight the delivery sweep for control
of a physical actuation.
