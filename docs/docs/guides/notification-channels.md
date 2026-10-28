---
sidebar_position: 6
title: Configuring Notification Channels
---

# Configuring Notification Channels

Notifications carry a raised **alarm** the last mile, to a person. When a detection rule's actions raise an alarm, a per-tenant **policy** routes it by severity to the **channels** you configure: email over SMTP, or a webhook. Policies add throttling and **escalation** of unacknowledged alarms.

This machine-to-human path is deliberately separate from the machine-to-machine **[outbound connectors](../concepts/outbound-connectors.md)**. Connectors carry payloads to *systems*; notifications carry alerts to *people*, with recipients and per-severity routing. For how alarms are raised in the first place, see [Event Processing & Alarms](../concepts/event-processing.md).

To manage policies and quiet hours from the web console instead, see [Notification policies](../user-guide/notification-policies.md) in the user guide.

:::note Status
Available. You manage channels and policies over the notification-management GraphQL API. Reading requires the `notification:read` authority; creating or changing anything requires `notification:write`.
:::

## Channels

A **channel** is a delivery endpoint your tenant configures: an instance of a channel **type**, plus its connection config. Query `notificationChannelTypes` for the types the platform defines. Today `smtp` and `webhook` ship with working adapters; each type's `available` flag says whether its adapter has landed.

A channel splits its settings in two:

- **`config`**: the non-secret connection settings, as a JSON document (SMTP host/port/from; webhook URL/method/headers and how it authenticates).
- **`secret`**: the credential, such as the SMTP password or a webhook auth token. It is stored in the platform's envelope-encrypted **secret store** and is **write-only**. You submit it on create, and it is never returned on read; the channel exposes only a `hasSecret` boolean.

On an **update**, the `secret` field behaves as follows:

- **Omit** it to leave the existing secret unchanged. You never need to re-send it.
- Send a non-null value to replace it.
- Send `null` or an empty string to clear it.
- On a webhook channel whose config declares `bearer` or `header` auth, clearing the secret is refused. Switch `auth` to `none` in the same request if you mean the endpoint to be anonymous.

If your client binds one variable per field, an unsupplied variable arrives as an explicit `null` and **clears the secret**. On a webhook channel that declares `bearer` or `header` auth, that same `null` makes the whole update fail instead, even one that only meant to rename the channel. Send the whole request as a single variable and leave the `secret` key out.

### Create an SMTP channel

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-email",
    name: "Operations email",
    channelType: "smtp",
    config: "{\"host\":\"smtp.example.com\",\"port\":587,\"from\":\"alerts@example.com\",\"username\":\"alerts\",\"security\":\"starttls\"}",
    secret: "<smtp password>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

An SMTP channel that sets `username` must have a secret. A delivery to one that has none is refused
on its first attempt, before the platform connects to the mail server, and is not retried; it is
counted with `reason="credential"` (see below). This is checked at delivery, not when you save the
channel.

### Create a webhook channel

A webhook channel POSTs the rendered notification to a URL. Create it the same way, with `channelType: "webhook"` and a config carrying the `url`, an `auth` mode, and optionally `method` and extra `headers`. The only accepted `method` is `POST`, which is also the default; any other method is refused when you save the channel.

`auth` is required and says how the channel authenticates:

| `auth` | What is sent | `secret` |
| --- | --- | --- |
| `none` | No credential header. Use this when the URL itself carries the credential, as a Slack incoming webhook's does. | Must not be set |
| `bearer` | `Authorization: Bearer <secret>` | Required |
| `header` | The secret in the header named by `authHeader`, prefixed by `authScheme` and a space if you set one. For example, `"authHeader":"X-API-Key"` sends the raw token, and `"authHeader":"Authorization","authScheme":"Token"` sends `Authorization: Token <secret>`. | Required |

`authHeader` and `authScheme` are read only with `header`. With `none` or `bearer`, leave them out: a channel that sets them is refused rather than having them silently ignored.

A channel whose `auth` and `secret` disagree is refused when you save it, not when an alarm fires. That covers a missing `auth`, `bearer` or `header` with no secret, and `none` with a secret. To make a `bearer` channel anonymous, send `auth` `none` and `secret: null` in the same update. An update that only renames, describes or disables a channel is not checked, so you can always switch a misconfigured channel off; enabling one is checked.

A channel that reaches delivery in that state anyway, for example one saved before `auth` existed, is not sent. The delivery is refused on its first attempt and not retried, and the notification service logs the tenant, the channel's token and the reason. The refusal is counted on `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`, which you can alert on. The same counter carries `reason="egress"` for a webhook or SMTP channel whose destination is on an address outbound traffic may not reach, such as a private, loopback or cloud-metadata address. It also carries `reason="no_adapter"`, counted when a channel of a type this build has no delivery adapter for is skipped, so nobody is notified through it. The `credential` and `egress` refusals are terminal: the delivery is not retried.

```graphql
mutation {
  createNotificationChannel(request: {
    token: "oncall-hook",
    name: "On-call webhook",
    channelType: "webhook",
    config: "{\"url\":\"https://hooks.example.com/alarms\",\"auth\":\"bearer\"}",
    secret: "<token>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

For a Slack incoming webhook, use `"auth":"none"` and leave `secret` out.

## Policies

A **policy** decides which raised alarms are delivered, to whom, and through which channels. It carries a set of **rules**. Each rule maps:

- a `severity`: `CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, `INDETERMINATE`, or `"*"` for any;
- to a channel, named by token;
- with a JSON array of `recipients` that the adapter interprets: email addresses for SMTP; it may be empty for a webhook.

The severity here is **uppercase** because it is the *alarm's* severity. A detection rule's own authoring severity is lowercase (`major`) and is uppercased when the alarm is raised, so a notification rule always matches the uppercase form. A rule whose severity is not one of those values (a lowercase `major`, say) is **refused at write**, rather than stored as a rule that could never match.

:::caution Policies are tenant-wide
`deviceTypeToken` is **not honoured yet**, and a policy that sets it is refused at write. Leave `deviceTypeToken` unset. See [Device-type scoping](#device-type-scoping) for why.
:::

### Device-type scoping {#device-type-scoping}

Scoping a policy to a device type needs a cross-service lookup from the alarm's originator to its device type, which has not landed. Until it does, the dispatcher skips a scoped policy rather than applying it tenant-wide and over-notifying. Refusing the write is deliberate: a policy that accepted the field would return success and then deliver nothing at all.

### Throttling and escalation {#throttling-and-escalation}

Two more settings shape delivery:

- **`throttleSeconds`** is the minimum gap between notifications for the *same* alarm, so a flapping condition does not flood a channel. `null` means no throttle.
- **`escalateAfterSeconds`** and **`maxEscalations`**: when set (> 0), an alarm that stays **unacknowledged and uncleared** for that long after its last notification is re-notified, up to the cap. If `maxEscalations` is unset or `0`, a service-wide default cap of 5 applies. A `null`/`0` `escalateAfterSeconds` disables escalation for the policy.

Escalation is safe to run on several replicas: each escalation tier is claimed before it is sent, so exactly one replica delivers it. Each alarm has **one shared escalation clock and tier**. If several escalating policies match, the shortest window sets the cadence, and every cap counts against the shared tier.

```graphql
mutation {
  createNotificationPolicy(request: {
    token: "default-routing",
    name: "Default alarm routing",
    throttleSeconds: 300,
    escalateAfterSeconds: 900,
    maxEscalations: 3,
    enabled: true,
    rules: [
      { severity: "CRITICAL", channelToken: "oncall-hook", recipients: "[]" },
      { severity: "*", channelToken: "ops-email", recipients: "[\"ops@example.com\"]" }
    ]
  }) { token enabled rules { severity channel { token } } }
}
```

On update, the request's `rules` **replaces** the policy's existing rule set. Omit `rules` to leave the stored rules untouched; sending `null` or `[]` leaves the policy with no rules. As with a channel's `secret`, a client that binds one variable per field sends an unsupplied `rules` as `null` and empties the rule set, so send the whole request as a single variable. Naming an unknown channel token fails the whole write.

Two people editing the same policy can overwrite each other: a field both of them change keeps only the last save, and because a `rules` list replaces the whole set, the second save drops every rule the first one wrote. To prevent that, pass `expectedUpdatedAt` with the policy's `updatedAt` as you read it (or as your previous update returned it). If the policy has changed since, the update is refused with "notification policy was modified by another writer; reload and try again" and nothing is written. Reload, reapply your change, and send it again.

### Quiet hours {#quiet-hours}

**Quiet hours** keep a policy from paging people at the wrong time, such as overnight. While a policy is inside its quiet hours it **holds** its notifications instead of sending them, and it sends them when the quiet stretch ends. Each policy has its own quiet hours, so a night-shift policy and a day-team policy can follow different schedules.

Quiet hours are not [maintenance windows](./maintenance-windows.md). A maintenance window belongs to a device type and says when disruptive commands may *start*. Quiet hours belong to a notification policy and say when that policy *holds its pages*. They use the same slot format and the same time-zone and daylight-saving rules, but they are otherwise unrelated, and neither affects the other.

A policy gets quiet hours from three fields. They are `null` on a policy without quiet hours.

| Field | Meaning |
| --- | --- |
| `quietHoursTimezone` | The IANA time zone the slots are read in, such as `Europe/Berlin`. `Local` and unknown names are refused. The policy's own zone is always used, never the server's. |
| `quietHoursSlots` | A JSON **string** holding 1 to 56 weekly slots, in the same document as a maintenance window. Each slot has a `day` (`MONDAY` to `SUNDAY`), a `start` (24-hour `HH:MM`, wall-clock time in the zone) and a `durationMinutes` (1 to 10080, elapsed time). |
| `quietHoursBypassSeverity` | `CRITICAL`, `MAJOR`, `MINOR`, `WARNING` or `INDETERMINATE`, or `null`. Alarms at or above this severity are sent even during quiet hours. `null` holds every severity. It is allowed, and has no effect, on a policy with no quiet hours. |

Set `quietHoursTimezone` and `quietHoursSlots` together, or leave both `null`.

```graphql
mutation {
  createNotificationPolicy(request: {
    token: "night-shift",
    name: "Night shift pages",
    enabled: true,
    quietHoursTimezone: "Europe/Berlin",
    quietHoursSlots: "[{\"day\":\"MONDAY\",\"start\":\"22:00\",\"durationMinutes\":480},{\"day\":\"TUESDAY\",\"start\":\"22:00\",\"durationMinutes\":480}]",
    quietHoursBypassSeverity: "CRITICAL",
    rules: [
      { severity: "*", channelToken: "ops-email", recipients: "[\"ops@example.com\"]" }
    ]
  }) { token quietHoursTimezone quietHoursSlots quietHoursBypassSeverity }
}
```

This policy is quiet from 22:00 to 06:00 on Monday and Tuesday nights, in Berlin time. A `CRITICAL` alarm in that stretch is still sent at once. Any other alarm waits until 06:00.

`updateNotificationPolicy` is a partial update, and the three quiet-hours fields carry three states: omit one to keep it, send `null` to clear it, send a value to set it. The server checks the fields together after applying the update, so to **remove** quiet hours, clear `quietHoursTimezone` and `quietHoursSlots` in the same request (and `quietHoursBypassSeverity` too, if you want it gone).

```graphql
mutation {
  updateNotificationPolicy(
    token: "night-shift",
    request: { quietHoursTimezone: null, quietHoursSlots: null }
  ) { token quietHoursTimezone quietHoursSlots }
}
```

#### What happens during quiet hours {#quiet-hours-behavior}

- **Held, then sent.** When a raised or escalated alarm matches a policy that is inside its quiet hours, and the alarm's severity is below the bypass severity (or no bypass is set), the notification is **held**, not dropped. The policy sends it through its rules when the quiet stretch ends. The escalation scheduler releases held notifications on its pass, which runs once a minute by default, so a release can be up to a pass late. A release interrupted by a restart of the notification service is not lost: it is sent a few minutes later. In rare cases that means the same page is sent twice.
- **Acknowledged or cleared drops it.** A held notification is dropped instead of sent if the alarm was acknowledged or cleared after the event that was held.
- **One held notification per alarm per policy.** If the same policy holds a later raised or escalated event for the same alarm, the later one replaces the earlier. The page in the morning describes the alarm as it last was, and an alarm that flaps overnight costs one page, not one per flap.
- **The bypass severity sends at once.** An alarm at or above the bypass severity is sent immediately, even inside quiet hours. An alarm whose severity is not one of the known tiers is always sent.
- **Throttling comes first.** A notification the policy's throttle suppresses is suppressed, not held.
- **Escalation waits.** While a policy is in quiet hours, its escalation re-notifications wait for alarms below the bypass severity. They resume after the stretch ends: an alarm still unacknowledged and uncleared is re-notified on the first pass after it. Quiet hours do not use up escalation tiers.
- **Each policy is independent.** A policy without quiet hours, or one that is not quiet right now, still pages immediately for the same alarm. A held notification is still sent at the end of the stretch even if another policy has already paged someone, because the recipients can differ.
- **A fresh page answers the held one.** When the policy itself pages for the alarm, because its quiet hours have ended or the alarm reached the bypass severity, what it was holding for that alarm is dropped rather than sent afterwards.
- **Edits re-evaluate what is held.** Changing a policy's quiet hours or bypass severity, or enabling or disabling it, is applied on the next pass. A held notification is sent if the policy no longer holds it, or held until the new end if it still does. A disabled or deleted policy drops what it was holding.
- **A failed release is retried.** A release that reaches no channel, because every delivery failed, is retried after 1, 2, 4 and 8 minutes, 5 attempts in all. It is then dropped, counted and recorded as a dead letter, the same way as an alarm that reached nobody when it was raised.

#### Time zones and daylight saving {#quiet-hours-time-zones}

Slots are wall-clock times in the policy's `quietHoursTimezone`, so quiet hours follow daylight saving time with the zone. A slot whose start falls in a spring-forward gap, a time that does not exist, opens at the end of the gap. A slot whose start falls in a fall-back overlap, a time that happens twice, opens at the first occurrence. A slot's duration is elapsed time, so a long slot across a clock change ends an hour earlier or later on the wall clock.

A slot may run past midnight. A slot that runs past the end of Sunday wraps into Monday.

#### What the server refuses {#quiet-hours-validation}

Every violation is answered with `extensions.code` `INVALID_VALUE`, and nothing is written.

- `quietHoursTimezone` and `quietHoursSlots` are not set together.
- The zone is `Local` or an unknown name.
- A slot has an unknown `day`, a `start` that is not 24-hour `HH:MM`, or a `durationMinutes` outside 1 to 10080, or the list does not hold 1 to 56 slots.
- Two slots **overlap** in the week as written. Slots that only touch are fine, and a Sunday slot that runs past midnight is checked against Monday.
- The slots together **cover the whole week** (10080 minutes). To silence a policy entirely, disable it instead.

#### Watching it {#quiet-hours-metric}

The counter `devicechain_notificationmanagement_quiet_hours_total` counts what quiet hours did, with the label `outcome`:

| `outcome` | Counted when |
| --- | --- |
| `held` | A notification was held rather than sent. |
| `released` | A held notification was sent when its stretch ended. |
| `dropped_resolved` | A held notification was dropped because the alarm was acknowledged or cleared. |
| `dropped_policy` | A held notification was dropped because its policy was disabled or deleted. |
| `dropped_exhausted` | A held notification was dropped after 5 releases reached no channel. It is also recorded as a dead letter. |
| `superseded` | A held notification was dropped because the policy paged a newer event for the same alarm. |

In the console, quiet hours are edited on the **Notification Policies** page; see [Notification policies](../user-guide/notification-policies.md).

## Verify the path end to end

1. **Create a channel** (as above). Confirm `enabled: true` on the result, and that `hasSecret` is `true` for an SMTP channel with a username or a webhook declaring `bearer` or `header`, and `false` for a webhook declaring `none`.
2. **Create a policy** whose rules map the severities you care about to that channel.
3. **Raise a real alarm.** Trip a detection rule on a test device (see [Event Processing & Alarms](../concepts/event-processing.md)) and confirm the email or webhook call arrives.
4. **Inspect delivery state.** The service keeps a read-only per-alarm record of what it has done. Query `notificationStatesByAlarmToken(alarmTokens: [...])`, or search with `notificationStates`. Check `firstNotifiedAt` and `notifyCount`, and, once the alarm has sat unacknowledged past the escalation window, `escalationLevel`.

Acknowledging or clearing the alarm stops further escalation. The state row records `acknowledgedAt`/`clearedAt` alongside the notification history. If the alarm clears and its condition returns, the platform re-opens the same alarm, and its state row starts over for the new cycle: escalation runs again until that cycle is acknowledged or cleared.
