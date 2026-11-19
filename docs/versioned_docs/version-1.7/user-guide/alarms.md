---
title: Alarms
sidebar_position: 6
---

import GuideShot from '@site/src/components/GuideShot';

# Alarms

An alarm is what a detection rule raises when something needs a person's attention. The **Alarms**
page lists every alarm in the tenant and updates as alarms are raised, change and clear. This
chapter is for operators who watch and handle alarms: reading the list, filtering it,
acknowledging and clearing alarms, and exporting the list.

## The alarm list

Choose **Monitor → Alarms** in the sidebar. Each row is one alarm.

<GuideShot id="alarms-list" alt="The Alarms page with the filters All states, All severities and Any acknowledgement and an Export CSV button. A green Live indicator sits beside the description. The table lists Major bp-over-temp alarms on the temperature metric for thermostats bp-therm-001 to bp-therm-012: three are Active with Acknowledge and Clear buttons, the rest are Cleared." />

The columns:

- **Severity**: Critical, Major, Minor, Warning or Indeterminate, set by the rule that raised it.
- **Status**: **Active** while the condition holds, **Cleared** once it has resolved or someone
  cleared it, and **Acknowledged** for an active alarm that someone has taken on.
- **Originator**: the device that raised the alarm.
- **Alarm**: the alarm key, with the metric underneath.
- **Value**: the latest reading the alarm saw.
- **Raised**: when the alarm was raised.
- **Actions**: **Acknowledge** and **Clear**, for an active alarm.

The **Live** indicator beside the description shows that the page is receiving changes as they
happen. If the connection drops, it shows that it is reconnecting, and the page catches up when it
is back. The page also refreshes itself every 30 seconds, so a missed change does not linger.

One alarm stays one row. If the condition stops and starts again, the same alarm becomes active
again rather than a new row appearing. Rules that share an alarm key can also raise or lower the
severity of a single alarm. The [alarm lifecycle](/concepts/event-processing#the-alarm-lifecycle)
explains both.

## Filtering

Three filters sit above the table: state (**Active** or **Cleared**), severity, and
acknowledgement (**Unacknowledged** or **Acknowledged**). Each opens a short list you can search.

<GuideShot id="alarms-filter" alt="The Alarms page with the state filter open, offering Active and Cleared, above the alarm list." />

Filters combine. For example, choose **Active** and **Unacknowledged** to see what nobody has
picked up yet. Clear a filter with the × inside it.

## Acknowledging and clearing

**Acknowledge** says "I've got this". The alarm stays active, its status changes to
**Acknowledged**, and the platform records who acknowledged it and when. Acknowledging also stops
repeat notifications for that alarm.

**Clear** closes the alarm by hand. Most alarms clear themselves when the rule's condition stops
holding, so you rarely need it, but it is there for an alarm you know is resolved.

<GuideShot id="alarms-acknowledged" alt="The Alarms page filtered to Acknowledged, showing one Major alarm, bp-over-temp on bp-therm-011, with status Acknowledged and a Clear button. Showing 1–1 of 1 result." />

Things to expect:

- An acknowledged alarm keeps its **Clear** button but loses **Acknowledge**.
- If an acknowledged alarm clears and its condition later returns, the platform re-opens the same
  alarm with its acknowledgement cleared, so it needs acknowledging again.
- The buttons appear only if your role lets you change alarms. Without that right the page is read
  only.

## Exporting the list

**Export CSV** saves every alarm that matches the current filters, not just the page you are
looking at, up to 10,000 rows. If more alarms match, the console says the export stopped at the
limit, and you narrow the filters to get the rest. The file holds each alarm's token, severity,
status, whether it is acknowledged, originator, alarm key, metric, value, and the raised and
cleared times.

## Related

- [Detection rules](/user-guide/detection-rules), which raise these alarms
- [Event processing and alarms](/concepts/event-processing#the-alarm-lifecycle): the alarm lifecycle and escalation
- [Configuring notification channels](/guides/notification-channels), to have alarms emailed or posted to a webhook
- [Notification policies](/user-guide/notification-policies), to hold a policy's pages during quiet hours
