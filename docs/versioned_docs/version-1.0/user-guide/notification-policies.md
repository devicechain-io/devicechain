---
title: Notification policies
sidebar_position: 18
---

# Notification policies

A **notification policy** decides who is told about a raised alarm, through which channel, and
when. This chapter shows how to view policies in the console and how to give one **quiet hours**, so
it stops paging people overnight and sends what it held when the quiet stretch ends. It is for
administrators who look after on-call routing. How quiet hours behave, and how to set them through
the API, is in [Quiet hours](/guides/notification-channels#quiet-hours).

Notification policies are under **Monitor → Notification Policies** in the sidebar. The entry appears if your
role can read notifications. Changing a policy also needs permission to write notifications;
without it, a policy opens read-only.

## The policy list

The list shows each policy's token, name, whether it is enabled, how many routing rules it has, and
its quiet hours. The **Quiet hours** column shows the time zone the policy's quiet hours are read
in, or a dash when it has none. Click a row to open the policy.

## Creating and editing a policy

Click **New notification policy**. The form asks for:

- **Token**: the identifier of the policy. It cannot change after you create it.
- **Name** and **Description**.
- **Enabled**: a disabled policy sends nothing, and drops any notifications it was holding for
  quiet hours.
- **Throttle (seconds)**: the minimum time between notifications about the same alarm. Leave it
  blank for no throttle.
- **Escalate after (seconds)** and **Max escalations**: how long an unacknowledged alarm waits
  before it is notified again, and how many times. See
  [Throttling and escalation](/guides/notification-channels#throttling-and-escalation).
- **Quiet hours**, described below.

The console does not edit **routing rules**. A policy you create here starts with none, and a policy
you open shows its rules read-only: each severity, the channel it sends through, and its recipients.
Saving never changes them. Add or change rules through the API, as described in
[Configuring notification channels](/guides/notification-channels#policies).

When you edit a policy, **Save changes** sends only the fields you changed. The console also tells
the server which version of the policy you were looking at. If someone else saved the policy in the
meantime, your save is refused with a message that the policy was modified by another writer;
reload the page and make your change again.

## Quiet hours

Tick **Hold notifications during quiet hours** to give the policy quiet hours. While the policy is
inside them it holds its notifications and sends them when the stretch ends, unless the alarm was
acknowledged or cleared in the meantime. Untick it and save to remove the quiet hours.

When quiet hours are on, the form shows:

- **Time zone**: the zone the slot times are read in. It starts as your browser's zone. The slot
  times are wall-clock times in this zone, so quiet hours follow daylight saving time with it.
- **Quiet slots**: one row per stretch, each a day of the week, a start time and a duration in
  minutes. Use **Add slot** for more, up to 56. A slot may run past midnight, and a Sunday slot
  wraps into Monday.
- **Bypass severity**: alarms at or above this severity are sent immediately even during quiet
  hours. It starts as **Critical** the first time you turn quiet hours on. Choose **None** to hold
  every severity.

Slots may not overlap, and together they may not cover the whole week. The form points out both as
you type and keeps **Save changes** disabled until they are fixed; to silence a policy entirely,
disable it instead. The server makes the final check when you save.

If a policy has quiet hours the console cannot read, it says so instead of showing an empty
schedule. The quiet hours stay stored and in force, and saving other changes leaves them alone.

Quiet hours are not [maintenance windows](/guides/maintenance-windows). A maintenance window belongs
to a device type and holds disruptive commands; quiet hours belong to a notification policy and hold
its pages.

## Related

- [Quiet hours](/guides/notification-channels#quiet-hours), for what is held and released, time
  zones, the validation rules and the metric
- [Alarms](/user-guide/alarms), where you acknowledge and clear the alarms that policies notify about
- [Configuring notification channels](/guides/notification-channels)
