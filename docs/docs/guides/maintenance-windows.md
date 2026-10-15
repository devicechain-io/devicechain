---
sidebar_position: 8
title: Maintenance Windows
---

# Maintenance Windows

A maintenance window keeps disruptive commands, such as a reboot, from starting at the wrong time.
This guide walks through setting one up and watching a command wait for it. The reasoning behind it
is in [Maintenance windows](../concepts/commands.md#maintenance-windows).

Windows are managed in the device-management GraphQL API at
`https://<your-host>/api/device-management/graphql`. Commands are issued as described in
[Sending a command](./sending-commands.md), against the command-delivery API.

## Create a window {#create-a-window}

A window is a time zone plus 1 to 56 weekly slots. `slots` is a JSON **string**. Each slot has a
`day` (`MONDAY` to `SUNDAY`), a `start` (24-hour `HH:MM`, wall-clock time in the zone) and a
`durationMinutes` (1 to 10080, elapsed time).

```graphql
mutation {
  createMaintenanceWindow(request: {
    token: "weekend-night",
    name: "Weekend night",
    timezone: "Europe/Berlin",
    slots: "[{\"day\":\"SATURDAY\",\"start\":\"22:00\",\"durationMinutes\":240}]"
  }) {
    token timezone slots
  }
}
```

`Local` and unknown zone names are refused. A slot may run past midnight, so the example above is
open from 22:00 to 02:00 on the night of Saturday into Sunday. To check what a window will do, ask
when it opens:

```graphql
query {
  maintenanceWindowOpening(token: "weekend-night", at: "2026-11-02T12:00:00Z") {
    found openNow dispatchAfter closes
  }
}
```

`dispatchAfter` is the instant you asked about when the window is open, otherwise the next opening.
Omit `at` for now.

## Assign it to a device type {#assign-it-to-a-device-type}

A device type has at most one window. Every device of that type follows it.

```graphql
mutation {
  updateDeviceType(token: "pump-controller", request: {
    maintenanceWindowToken: "weekend-night"
  }) {
    token
    maintenanceWindow { token }
  }
}
```

Send `null` or an empty value to detach it. A device type with no window sends every command
immediately, as before.

## Mark a command DISRUPTIVE {#mark-a-command-disruptive}

Only commands declared with impact `DISRUPTIVE` or `DESTRUCTIVE` wait. Set it on the command
definition, then **publish the profile**; the published vocabulary is what the gate reads.

```graphql
mutation {
  createCommandDefinition(request: {
    token: "pump-reboot",
    deviceProfileToken: "pump-profile",
    commandKey: "reboot",
    impact: "DISRUPTIVE"
  }) { token impact }
}
```

`impact` is `NORMAL` when omitted. The console's **Add LwM2M standard commands** creates
`lwm2m.reboot` as `DISRUPTIVE`. Definitions you declared earlier are not changed. A command whose key
is not declared with one of these impacts, including a raw `lwm2m.execute`, is never held.

## Send a command and watch it wait {#send-and-watch-it-wait}

Send the command as usual. If the window is closed, the command is accepted and waits. If the window
is open, the command is sent at once, but only until the window closes.

```graphql
mutation {
  createCommand(request: {
    token: "reboot-pump-7",
    deviceToken: "pump-007",
    name: "reboot"
  }) {
    command { token status dispatchAfter maintenanceWindowToken }
    rejection { code reason }
  }
}
```

The command stays `QUEUED`, `dispatchAfter` is the next opening and `maintenanceWindowToken` is
`weekend-night`. When the window opens it is sent like any other command, including to a sleeping
device that wakes later. Waiting commands count against the tenant's undelivered-command ceiling and
can be cancelled, singly or as a batch, like any queued command. A batch record's `waiting` says how
many members were held for a window.

To see what would happen without sending anything, ask `commandDispatchSchedule` with the device
tokens, the command key and an optional `expiresAt`.

The window gates only the start. A command already started inside the window finishes whenever it
finishes.

## When the window opens too late: WINDOW_AFTER_EXPIRY {#window-after-expiry}

A command expires after seven days unless you set `expiresAt`. If the next opening is later than
that, the enqueue is refused with the code `WINDOW_AFTER_EXPIRY`, because accepting it could only end
in `EXPIRED`. In a batch it is a per-device refusal, and the whole batch is refused unless
`allowPartial` is set. This is not retryable as it stands. To fix it, do one of:

- set a later `expiresAt`, so the command outlives the wait;
- add slots to the window, or move the next slot sooner, so it opens before the command expires.

## Edit or delete a window {#edit-or-delete-a-window}

`updateMaintenanceWindow` is a partial update: omit a field to leave it alone.

```graphql
mutation {
  updateMaintenanceWindow(token: "weekend-night", request: {
    slots: "[{\"day\":\"SATURDAY\",\"start\":\"23:00\",\"durationMinutes\":180}]"
  }) { token slots }
}
```

Commands already waiting follow the new schedule within about a minute, because command delivery
polls for it rather than being told. Do not expect the move to be instant.

`deleteMaintenanceWindow` is refused while any device type uses the window; detach it first. Commands
already waiting follow their device type: detaching the window from a type releases them, and moving
the type to another window moves them to that window's next opening, within the same minute.
Released commands are sent at once, outside any window: to stop them rather than release them,
cancel them (or their batch) before you detach the window. If a device is
offline through a whole opening, its command is rolled to the next opening rather than being sent
whenever the device returns.

## Limits {#limits}

- A window belongs to a device type. There is no group, per-device or tenant-default window, and no
  blackout windows or holidays.
- Over-the-air update gating is not built.
- If device management cannot be reached when you enqueue, the command is refused rather than sent
  ungated.

The counter `command_delivery_window_restamps_total` counts commands whose hold was moved by an edit,
a delete, a window that closed again before the device returned, or their device type moving to
another window or losing its window.
