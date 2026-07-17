---
sidebar_position: 5
title: Configuring Notification Channels
---

# Configuring Notification Channels

When a detection rule's REACT stage raises an **alarm**, the notification subsystem carries it the last mile — to a **person** (ADR-017). A per-tenant **policy** routes alarms by severity to configured **channels** (email over SMTP, or a webhook), with throttling and unacknowledged-alarm **escalation**. This machine-to-human path is deliberately separate from the machine-to-machine **[outbound connectors](../concepts/outbound-connectors.md)**: connectors carry payloads to *systems*; notifications carry alerts to *people*, with recipients and per-severity routing. See [Event Processing & Alarms](../concepts/event-processing.md) for how alarms are raised in the first place.

:::note Status
Available. Channels and policies are managed over the notification-management GraphQL API. Reading requires the `notification:read` authority; creating or changing anything requires `notification:write`.
:::

## Channels

A **channel** is a tenant-configured delivery endpoint: an instance of a channel **type** with its connection config. Query `notificationChannelTypes` for the types the platform defines — today `smtp` and `webhook` ship with working adapters (each type's `available` flag says whether its adapter has landed).

A channel splits its settings in two:

- **`config`** — the non-secret connection settings, as a JSON document (SMTP host/port/from; webhook URL/method/headers).
- **`secret`** — the credential (the SMTP password, a webhook auth token). It is stored in the platform's envelope-encrypted **secret store** (ADR-059) and is **write-only**: you submit it on create, and it is never returned on read. The channel exposes only a `hasSecret` boolean.

On an **update**, a `null` secret leaves the existing secret unchanged (you never need to re-send it), a non-null value replaces it, and an empty string clears it.

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

A webhook channel POSTs the rendered notification to a URL. Create it the same way with `channelType: "webhook"` and a config carrying the `url` (optionally `method` and extra `headers`). Its secret is presented as `Authorization: Bearer <secret>` by default; set `authHeader`/`authScheme` in the config to use a custom header instead.

## Policies

A **policy** decides which raised alarms get delivered, to whom, through which channels. It carries a set of **rules** — each maps a `severity` (`CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, `INDETERMINATE`, or `"*"` for any) to a channel (named by token) and a JSON array of `recipients` the adapter interprets (email addresses for SMTP; may be empty for a webhook). A policy can be tenant-wide or scoped to one device profile via `deviceTypeToken`.

Two more knobs shape delivery:

- **`throttleSeconds`** — the minimum gap between notifications for the *same* alarm, so a flapping condition does not flood a channel (`null` = no throttle).
- **`escalateAfterSeconds`** + **`maxEscalations`** — when set (> 0), an alarm that stays **unacknowledged and uncleared** that long after its last notification is re-notified, up to the cap. Escalation runs on an HA-safe scheduler, and each alarm has **one shared escalation clock and tier**: if several escalating policies match, the shortest window sets the cadence and every cap counts against the shared tier. A `null`/`0` `escalateAfterSeconds` disables escalation for the policy.

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

On update, the request's `rules` **replaces** the policy's existing rule set. Naming an unknown channel token fails the whole write.

## Verify the path end to end

1. **Create a channel** (as above) and confirm `hasSecret: true` and `enabled: true` on the result.
2. **Create a policy** whose rules map the severities you care about to that channel.
3. **Raise a real alarm** — trip a detection rule on a test device (see [Event Processing & Alarms](../concepts/event-processing.md)) and confirm the email or webhook call arrives.
4. **Inspect delivery state** — the service keeps a read-only per-alarm record of what it has done. Query `notificationStatesByAlarmToken(alarmTokens: [...])` (or search with `notificationStates`) and check `firstNotifiedAt`, `notifyCount`, and — once the alarm has sat unacknowledged past the escalation window — `escalationLevel`.

Acknowledging or clearing the alarm stops further escalation; the state row records `acknowledgedAt`/`clearedAt` alongside the notification history.
