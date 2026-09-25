---
sidebar_position: 6
title: Configuring Notification Channels
---

# Configuring Notification Channels

Notifications carry a raised **alarm** the last mile, to a person. When a detection rule's actions raise an alarm, a per-tenant **policy** routes it by severity to the **channels** you configure: email over SMTP, or a webhook. Policies add throttling and **escalation** of unacknowledged alarms.

This machine-to-human path is deliberately separate from the machine-to-machine **[outbound connectors](../concepts/outbound-connectors.md)**. Connectors carry payloads to *systems*; notifications carry alerts to *people*, with recipients and per-severity routing. For how alarms are raised in the first place, see [Event Processing & Alarms](../concepts/event-processing.md).

:::note Status
Available. You manage channels and policies over the notification-management GraphQL API. Reading requires the `notification:read` authority; creating or changing anything requires `notification:write`.
:::

## Channels

A **channel** is a delivery endpoint your tenant configures: an instance of a channel **type**, plus its connection config. Query `notificationChannelTypes` for the types the platform defines. Today `smtp` and `webhook` ship with working adapters; each type's `available` flag says whether its adapter has landed.

A channel splits its settings in two:

- **`config`**: the non-secret connection settings, as a JSON document (SMTP host/port/from; webhook URL/method/headers).
- **`secret`**: the credential, such as the SMTP password or a webhook auth token. It is stored in the platform's envelope-encrypted **secret store** and is **write-only**. You submit it on create, and it is never returned on read; the channel exposes only a `hasSecret` boolean.

On an **update**, the `secret` field behaves as follows:

- **Omit** it to leave the existing secret unchanged. You never need to re-send it.
- Send a non-null value to replace it.
- Send `null` or an empty string to clear it.

If your client binds one variable per field, an unsupplied variable arrives as an explicit `null` and **clears the secret**. Send the whole request as a single variable and leave the `secret` key out.

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

### Create a webhook channel

A webhook channel POSTs the rendered notification to a URL. Create it the same way, with `channelType: "webhook"` and a config carrying the `url` and, optionally, `method` and extra `headers`. The only accepted `method` is `POST`, which is also the default; any other method fails at delivery.

By default the secret is presented as `Authorization: Bearer <secret>`. To use a custom header instead, set `authHeader`/`authScheme` in the config.

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

## Verify the path end to end

1. **Create a channel** (as above). Confirm `hasSecret: true` and `enabled: true` on the result.
2. **Create a policy** whose rules map the severities you care about to that channel.
3. **Raise a real alarm.** Trip a detection rule on a test device (see [Event Processing & Alarms](../concepts/event-processing.md)) and confirm the email or webhook call arrives.
4. **Inspect delivery state.** The service keeps a read-only per-alarm record of what it has done. Query `notificationStatesByAlarmToken(alarmTokens: [...])`, or search with `notificationStates`. Check `firstNotifiedAt` and `notifyCount`, and, once the alarm has sat unacknowledged past the escalation window, `escalationLevel`.

Acknowledging or clearing the alarm stops further escalation. The state row records `acknowledgedAt`/`clearedAt` alongside the notification history.
