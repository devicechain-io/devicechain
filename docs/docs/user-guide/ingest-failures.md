---
title: Ingest failures
sidebar_position: 7
---

# Ingest failures {#ingest-failures}

The **Ingest failures** page lists the events the platform refused or could not store, newest first. Open it when a device says it is sending and nothing appears: the page tells you whether the platform turned the events away, and why. This chapter is for the members who manage devices.

It is a report, not a queue. Nothing is re-sent, and the original events are not kept in your tenant's data. Fix the cause and the device's next events are accepted.

## Who can see it {#who}

The page needs the `device:write` grant, the one held by the members who manage devices and so can act on a refusal. It is not part of the read-only baseline every member holds, and it is not offered to a member whose access is limited to some customers. A member without the grant who opens the page sees a note saying so, and the page asks the platform for nothing.

## Open the page {#open}

Choose **Monitor → Ingest failures** in the sidebar. The page can also open already filtered: a device's detail page links here with that device when events from it were rejected in the last 24 hours, and a device profile's **Decoder** tab links here with the reason **Decoder failed**.

## Reading a row {#rows}

Each row is one refused event.

| Column | Meaning |
| --- | --- |
| **Recorded** | When the platform recorded the refusal. It is the platform's clock, not the device's. |
| **Reason** | Why the event was refused. A row with the reason **Other reason** also shows its reason code. |
| **Device token** | The token the sender named. It is the sender's claim, not proof that the device exists. A row reading **Further records for this reason** stands for records beyond the flood bound, below. |
| **What the platform said** | The platform's own wording, shortened. Under it, **The original event is no longer held; only this summary remains.** or **The original was not kept.** says what became of the event itself. |
| **Also folded in** | **+N more**: how many further records of the same reason from the same device were counted on this row. |

## Reasons {#reasons}

The chips above the table are the reasons. Each shows how many records of that reason the platform counted in the chosen time range, and a hover hint says what it means. Choose a chip to show only that reason; choose it again, or **All reasons**, to clear it. With a reason chosen, the page also names the devices most affected.

| Reason | What happened |
| --- | --- |
| **Unknown device** | No device has the token the sender named. Register the device, or correct the token it sends. |
| **Credential rejected** | The credential the device presented was missing or wrong. Check the device's credential. |
| **Not this gateway's device** | A gateway published for a device that is not assigned to it, or that does not exist. |
| **Device disabled** | The device exists and has been disabled. |
| **Invalid message** | The message could not be read, or a value in it was refused. The error text says which. |
| **Platform error** | A platform service the event needed could not be reached, or storing the event failed. Not something you can fix from the device; tell your operator. |
| **Integration alias changed** | An integration's event named a device that no longer holds the alias it was matched by. |
| **Decoder failed** | A payload decoder could not turn an uplink into readings. The start of the error says why. Open the device type's profile, **Decoder** tab, to see the kept failures and test them. |
| **Other reason** | A reason this version of the console has no wording for. The row carries its reason code. |

For what to check for each reason, see [Why isn't my device's data showing up](../guides/why-isnt-my-data-showing-up.md#reasons).

## Narrowing the list {#filters}

- **Time range**: **Last hour**, **Last 24 hours** (the default) or **Last 7 days**. The platform keeps the records for a retention window an operator sets, seven days by default, so a longer range simply shows what is still held.
- **Reason**: the chips described above.
- **Device**: type a device token in **Filter by device token…** to see only that device's refusals.

The list shows 25 rows to a page. If nothing matches, the page says so, and adds that a device whose data is missing for another reason is not listed here: for example, a disabled device's events are discarded and counted, not listed.

## Why a row can stand for many events {#flood-bound}

A fleet with one wrong credential can be refused thousands of events a second, and a list that kept every one would be useless. So the platform keeps at most 10 records an hour per device and reason, and counts the rest on the newest of them (**+N more**). It itemizes at most 1,000 records an hour per reason across all devices; the rest are counted on a single row marked **Further records for this reason**. The numbers on the reason chips include everything that was folded in.

## From the command line, and dead letters {#cli}

`dcctl events failed` reads the same records as a member of the tenant, with the same `device:write` requirement, and `--summary` counts them per reason as the chips do. See the [command reference](../reference/dcctl.md#dcctl-events-failed).

These records are not dead letters. An ingest failure is an event refused at the door, and the record is yours to read. A dead letter is a message that a platform service gave up on after accepting it, and it is kept for operators; it is read with `dcctl dead-letters` and is not listed here.

## Related {#related}

- [Why isn't my device's data showing up](../guides/why-isnt-my-data-showing-up.md): what to check for each reason, retention, and the alert operators get.
- [Devices](./devices.md): a device's own page, which links here.
- [Console at a glance](./console-at-a-glance.md): where **Ingest failures** sits in the sidebar.
