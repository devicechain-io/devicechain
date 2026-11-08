---
sidebar_position: 8
title: Scheduling a Fleet
---

# Scheduling a Fleet

A **campaign** sends commands to the devices of an entity group on a schedule, and keeps a record
of every device it tried. Street lighting is the case it was built for: dim every light at its own
sunset, restore it at its own sunrise, skip public holidays, and know the next morning which
fixtures did not obey and why.

A campaign has three parts:

- a **trigger** that says when it fires: a cron expression, a calendar of dates, or the sun;
- one or more **steps**, each a command with an optional payload and an optional delay after the
  trigger;
- a **target**, an entity group of devices.

Each time the trigger fires, the platform plans a **cycle**. A cycle holds one **element** per
device per step, and the element is the unit of tracking and of retry. Under the hood every group of
due elements becomes an ordinary [command batch](./commanding-a-fleet.md), so each device is
validated against its capability contract, held for its [maintenance window](./maintenance-windows.md),
tracked through the command lifecycle and expired on its time-to-live exactly as any other command.
Read those two guides first; this one covers only what the schedule adds.

Campaigns live on the `command-delivery` endpoint, `https://<your-host>/api/command-delivery/graphql`.
Creating, switching and deleting a campaign, and retrying its failures, need `command:write`. Reading
needs `command:read`. Every operation also needs `device:read`, because a campaign names a group and
its tracking lists the group's devices. A member limited to part of the tenant cannot use campaigns.

## Create a campaign {#create-a-campaign}

```graphql
mutation {
  createCampaign(request: {
    token: "dusk-dim"
    name: "Dim at dusk"
    trigger: "{\"kind\":\"SOLAR\",\"zone\":\"Europe/Berlin\",\"event\":\"SUNSET\",\"offsetMinutes\":-15}"
    steps: [
      { type: "COMMAND", name: "setLevel", payload: "{\"level\":40}" }
      { type: "COMMAND", name: "setLevel", payload: "{\"level\":20}", delaySeconds: 21600 }
    ]
    groupToken: "city-streetlights"
  }) {
    campaign { token enabled plannedThrough lastError }
    rejection { code reason }
  }
}
```

A definition that cannot be accepted comes back as a `rejection`, not an error. The codes are
`CAMPAIGN_TRIGGER_INVALID`, `CAMPAIGN_STEPS_INVALID`, `CAMPAIGN_STEP_UNSUPPORTED`,
`CAMPAIGN_TARGET_INVALID`, `CAMPAIGN_FIELD_INVALID`, `TOKEN_IN_USE`, and the payload and command-name
codes a single command is refused with.

| Field | Meaning |
| --- | --- |
| `token` | Unique within the tenant. |
| `trigger` | A JSON document, described below. |
| `steps` | One to ten steps. A step is `COMMAND` with a `name`, an optional JSON `payload`, and `delaySeconds` from 0 to 86400 after the trigger. |
| `groupToken` | The entity group to target. A dynamic group can select the devices of an area. |
| `maxLatenessSeconds` | How late a planned command may still be sent, 1 to 86400. Default 900. |
| `expiresInSeconds` | The time-to-live of each command, 1 to 604800, counted from when it is created. Default 3600. It is how long a command may wait for an unreachable device or a maintenance window, so it can outlast `maxLatenessSeconds`, which bounds when the scheduler sends, not when a device receives. Set it short if a late delivery is worse than none. |
| `enabled` | Whether it starts on. Default true. |

An `ARTIFACT` step type is part of the vocabulary but is not executed by this release; it is refused
with `CAMPAIGN_STEP_UNSUPPORTED` rather than ignored. To roll out an update on a schedule, use a
[rollout](../concepts/over-the-air-updates.md).

## Triggers {#triggers}

Every trigger needs an IANA time zone in `zone`. An empty zone, or `Local`, is refused: a schedule
that follows the host would move when the pod does.

### Cron

```json
{"kind":"CRON","zone":"America/New_York","cron":"30 18 * * *"}
```

Five fields: minute, hour, day of month, month, day of week. Each accepts `*`, a number, `a-b`,
`*/n`, `a-b/n` and comma lists. Day of week runs 0 to 7, with 0 and 7 both Sunday. When both day
fields are restricted, a day matches if either does. Names, `?`, `L` and `@daily` are refused.

### Calendar

```json
{"kind":"CALENDAR","zone":"Europe/London","dates":["2026-12-24","2026-12-31"],"times":["07:00","17:30"]}
```

Fires at each time on each listed date, in the zone.

### Solar

```json
{"kind":"SOLAR","zone":"Europe/Berlin","event":"SUNSET","offsetMinutes":-15}
```

Fires at `SUNSET` or `SUNRISE` plus `offsetMinutes` (negative is before), **computed separately for
each device from its own installed location**. The same trigger therefore fires at a different
instant for every fixture, and a city's lights go down in a wave that follows the sun.

The installed location is the device's installed position, recorded with the `setInstalledPosition`
mutation or the installed-position columns of a fleet import:

```graphql
mutation {
  setInstalledPosition(family: DEVICE, token: "lamp-0001",
    position: { latitude: 52.52, longitude: 13.40, source: SURVEY }) { latitude longitude }
}
```

Set it when the fixture is commissioned. It is deliberately not the last reported position: a light
does not move, and one whose receiver is faulty must not dim at the wrong dusk. The calculation is
accurate to about two minutes between roughly 60 degrees north and south.

A solar event involves no time zone, because the sun does not know it. The zone here only names the
civil calendar the day and the holidays belong to.

Two kinds of device are recorded as **skipped** rather than guessed at:

- a device with no installed position is skipped as `NO_SITE`;
- a device where the sun does not rise or set that day (inside the polar circles) is skipped as
  `POLAR_DAY` or `POLAR_NIGHT`.

If the campaign has a solar trigger and device locations cannot be read at all, planning fails and the
reason is shown in the campaign's `lastError`; nothing is sent.

### Holidays

Any trigger may carry `holidays`, a list of local dates in the zone on which it does not fire:

```json
{"kind":"SOLAR","zone":"Europe/Berlin","event":"SUNSET","holidays":["2026-12-24","2026-12-25"]}
```

A holiday suppresses a firing. It never moves it to another day.

### Daylight saving {#daylight-saving}

A cron or calendar time is wall-clock time in the zone. On the two days a year when that is
ambiguous:

- a time inside the spring-forward gap fires at the instant the clock jumps, once;
- a time that occurs twice in the fall-back hour fires at its first occurrence only.

## Maintenance windows {#maintenance-windows}

A campaign does not have a window of its own. Each command it creates goes through the same gate as
every other command: a disruptive command to a device whose device type has a
[maintenance window](./maintenance-windows.md) is held until the window opens, and is refused
outright if the window opens only after the command would expire. A held command counts as
`waiting` in the cycle's counts. Set `expiresInSeconds` long enough to cover the wait you want to
allow.

## What happens when it is late or down {#late-and-down}

The scheduler ticks every 15 seconds in every replica, with no leader, and a device is commanded
once however many replicas are running or restarting: the claim on each element is a
compare-and-set in the database, and the batch it creates carries a token derived from its elements,
so a replica that finishes a dead one's work replays the batch rather than duplicating it.

A command is never sent after its **lateness bound**. An element that falls due and is not claimed
within `maxLatenessSeconds` (an outage, or the campaign was switched off) is marked `SKIPPED` with
the reason `MISSED`. A dim command sent at midnight is worse than none, so a campaign never catches
up.

Switching a campaign off stops planning and sending. Deleting it cancels everything planned and not
yet sent, as `SKIPPED` with the reason `CANCELLED`; commands already created are untouched.

The tenant's [undelivered-command ceiling](./commanding-a-fleet.md) still applies. If a very large
fleet falls due in the same instant, devices beyond the ceiling are recorded as `REFUSED` with
`HELD_CEILING_EXCEEDED`, and can be retried.

## Follow a cycle {#follow-a-cycle}

```graphql
query {
  campaignCycles(campaignToken: "dusk-dim", limit: 3) {
    key day resolved
    counts { pending dispatched refused skipped succeeded inFlight waiting failed unknown }
  }
}
```

`counts` answers the morning-after question. The first five numbers count elements by the
campaign's state: `pending`, `claimed`, `dispatched`, `refused`, `skipped`. The rest count the
commands behind the dispatched elements: `succeeded`, `inFlight` (of which `waiting` for a window),
`failed` (finished without success and safe to send again) and `unknown` (may already have acted).

To see devices, page through the elements, optionally by status:

```graphql
query {
  campaignElements(campaignToken: "dusk-dim", cycleKey: "d2026-06-21", status: "SKIPPED", limit: 200) {
    id deviceToken step dueAt status reason commandStatus commandOutcome
  }
}
```

Pass the last `id` as `afterId` for the next page.

## Retry from failed {#retry-from-failed}

```graphql
mutation {
  retryCampaignFailed(campaignToken: "dusk-dim", cycleKey: "d2026-06-21", includeUnknown: false) {
    batches retried held
  }
}
```

This hands the cycle's unsuccessful elements to the platform's [batch retry](./commanding-a-fleet.md),
so its safeguards apply: a device whose command is still in flight, or already succeeded, is never
sent a second copy, and a device whose outcome is unknown is sent one only if you pass
`includeUnknown: true`, having confirmed that acting twice is harmless. Elements that were refused by
the batch are retried too. `held` counts the elements left alone because sending again is unsafe.

An element is retried once. If the retry itself fails, retry that batch from the
[batch API](./commanding-a-fleet.md).

## Limits {#limits}

- A cycle plans at most 100,000 devices.
- A cycle's devices are the group's members when the cycle is planned, up to 36 hours before it fires. A device added to the group later joins the next cycle.
- Finished cycles and their elements are kept for 30 days.
- A target is an entity group. Targeting an area directly is not supported; use a dynamic group that
  selects the area's devices.
- A step is a command. Update steps are not executed by this release.
