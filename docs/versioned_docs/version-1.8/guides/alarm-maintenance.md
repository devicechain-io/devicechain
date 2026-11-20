---
sidebar_position: 9
title: Alarm Maintenance
---

# Alarm Maintenance

An alarm suppression window holds pages during planned maintenance. Alarms still raise, remain visible and carry a suppression marker. Detection, severity and contributors continue as usual. This is separate from [command maintenance windows](./maintenance-windows.md).

Manage windows through the device-management GraphQL API at `https://<your-host>/api/device-management/graphql`.

## Choose the window {#choose-the-window}

Give the window a token and a required reason. Targets may be `DEVICE`, `DEVICE_TYPE`, `ENTITY_GROUP`, `AREA`, `ASSET`, `CUSTOMER` or `TENANT`; use a target token except for `TENANT`. Any matching target can apply. An empty `alarmKeys` list covers all alarm keys; otherwise only the listed keys match. `bypassSeverity` lets alarms at or above that severity page normally.

Use `ONE_SHOT` with `startTime` and `endTime`, or `RECURRING` with an IANA `timezone` and weekly `slots` (a JSON string), optionally bounded by `validFrom` and `validUntil`. Preview the next intervals with the window's `openings` field. Intervals include their start and exclude their end. Overlapping matches use the latest end.

```graphql
mutation {
  createAlarmSuppressionWindow(request: {
    token: "pump-service"
    reason: "Pump inspection"
    targets: [{type: "DEVICE", token: "pump-007"}]
    kind: ONE_SHOT
    startTime: "2026-12-01T10:00:00Z"
    endTime: "2026-12-01T12:00:00Z"
    alarmKeys: ["temperature"]
    bypassSeverity: "CRITICAL"
  }) { token status openings { start end } }
}
```

Each window allows at most 20 targets and 20 alarm keys. A one-shot window lasts at most 31 days. A tenant may have at most 500 live windows (scheduled or not yet ended/expired).

## Permissions and scope {#permissions-and-scope}

Reading needs `alarm:read`. A narrow window uses only `DEVICE` targets, is `ONE_SHOT` and lasts at most 24 hours: creating or editing it needs `alarm:write`. Broader windows also need `notification:write`. Updates must satisfy the requirements of both the existing and resulting window.

`endAlarmSuppressionWindow` needs only `alarm:write` and visibility of the window. Ending twice preserves the first end time. `deleteAlarmSuppressionWindow` needs the authorities that creating the window needed. Scoped members see and manage only windows wholly within their scope; they cannot change targets. Device ownership changes refresh window visibility, and windows created by scoped members remain limited by the creator's original scope.

## Follow the release {#follow-the-release}

When the window ends, a held page is released within a few scheduler passes, depending on the backlog, if the alarm is still active and unacknowledged. If the cycle has not paged anyone, acknowledging or clearing it during the window sends nothing, including no clear page. Clear announcements to targets already paged for that cycle follow the existing policy. Policy quiet hours can keep the page held longer. Ending, extending or deleting a window takes effect at the next status recheck. A released page explains its maintenance hold; escalation starts from the send.

Watch [`suppression_total` and `deferral_oldest_due_seconds`](../deployment/metrics-reference.md#notification-management). `suppression_total` counts holds, rechecks, releases and lookup failures; the oldest-due gauge measures overdue release work and returns to zero when no visited tenant has due rows. A failed or unsupported status lookup allows paging.

The **raised during maintenance** marker records the window declared when that alarm cycle was folded, evaluated at the cycle's event time. It remains in history after maintenance ends. Use `raisedDuringSuppression` in alarm search criteria to filter marked or unmarked cycles; it is a history filter, not a live suppression filter.

During a rolling upgrade, alarms under a window may still page. Rely on suppression once device-management and notification-management have both finished rolling out.
