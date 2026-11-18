---
title: Why isn't my device's data showing up
---

# Why isn't my device's data showing up {#why}

A device sends events and nothing appears. Before you read a pod log, look at what the platform **refused**. When it turns an event away, or cannot store it, it keeps a short record of that, and your tenant can read it.

:::note Status
Available. Open **Ingest failures** in the console's **Monitor** section, or query `failedEvents` and `failedEventSummary` on the event-management GraphQL API. Both need `device:write`, the grant of the members who manage devices and so can act on a refusal, and not the read-only baseline every member holds. They are also not offered to members whose access is limited to some customers: a refusal often names a device that does not exist, so it has no customer to belong to.
:::

## What you are looking at {#what}

Each row is one refused event, newest first:

| Column | Meaning |
| --- | --- |
| **Recorded** | When the platform recorded the refusal. It is the platform's clock, not the device's. |
| **Reason** | Why. See the table below. |
| **Device token** | The token the **sender named**. It is the sender's claim, not proof that the device exists: for an unknown device it names nothing in your registry. |
| **What the platform said** | The platform's own wording, shortened to 512 bytes. |
| **Also folded in** | How many further records of the same reason from the same device were counted on this row. See [flood bound](#flood-bound). |

The rows are a report. They are **not a queue**: nothing is re-sent, and the original events are not stored in your tenant's data. Fix the cause and the device's next events are accepted.

## Reasons and what to do {#reasons}

| Reason | What happened | What to check |
| --- | --- | --- |
| **Unknown device** (`DEVICE_NOT_FOUND`) | No device has the token the sender named. | Register the device, or correct the token it sends. A token is not the device's name or its credential; see [the three identifiers](./connecting-a-device.md#three-identifiers). |
| **Credential rejected** (`UNAUTHENTICATED`) | The credential the device presented was missing or wrong. | The device's [credential](./device-credentials.md): was it rotated, expired, or never set on the device? |
| **Not this gateway's device** (`NOT_GATEWAY_CHILD`) | A gateway published for a device that is not assigned to it, or that does not exist. | The device's [gateway assignment](./connecting-a-device.md). |
| **Invalid message** (`INVALID`) | The message could not be read, or a value in it was refused (not a number, outside the metric's range, older than 366 days, refused by the decoder). The record's error text says which. | The payload format, metric definition, event age, and decoder indicated by the error. |
| **Platform error** (`API_CALL_FAILED`) | A platform service the event needed could not be reached, or storing the event failed. | Not something you can fix from the device. Tell your operator; the detail is in the platform's logs and is not shown to you. |
| **Integration alias changed** (`INTEGRATION_ALIAS_MISMATCH`) | An integration's event named a device that no longer holds the alias it was matched by. | The device's alias in the [integration](./inbound-integrations.md). |
| **Other reason** (`UNKNOWN`) | A reason this version does not name. | The row carries its reason code. It is never shown as a different reason. |

If the table is empty and the data is still missing, the platform recorded no refusal: look at whether the device is [disabled](./device-credentials.md#disabling-a-device) (a disabled device's events are discarded and counted, not listed here), the device's connection, its [live state](../user-guide/devices.md) and whether its events reach the broker at all.

## On a device's page {#device-page}

When events from a device were rejected in the last 24 hours, its detail page says so, with a link to the reasons for that device.

## From the command line {#cli}

`dcctl events failed` reads the same records as a member of the tenant, with the same `device:write` requirement:

```bash
# the last day's failures, counted per reason, with the devices behind most of them
dcctl events failed -s dc.example.com --tls --tenant acme --email ops@acme.example --password "$PASSWORD" --summary

# one device's refusals for one reason, newest first, as JSON
dcctl events failed --tenant acme --email ops@acme.example --password "$PASSWORD" \
  --device sensor-17 --reason device-not-found --since 6h --output json
```

`--since` and `--until` take an RFC 3339 time or how long ago (`6h`). The platform reads at most 31 days back. A list stops at `--limit` records (100 by default) and says when more matched. `--summary` takes only `--since` (24 hours by default). See the [command reference](../reference/dcctl.md#dcctl-events-failed). The command does not return the original event.

## The flood bound {#flood-bound}

A fleet with one wrong credential can be refused thousands of events a second. The list would be useless, and so would the store behind it, if it kept every one. So:

- at most **10 records an hour** are kept per device and reason; every further record is **counted** on the newest of them (the **Also folded in** column);
- at most **1,000 records an hour** are itemized per reason, across all devices. A probe of invented tokens is bounded the same way; the rest are counted on a single row marked as further records for that reason.

Nothing is lost from the counts: the totals in the reason chips include everything that was folded in. The counts are a measure of magnitude, not an audit: counts are written about once a second, so during a database outage they wait in memory and a platform restart before it clears loses them.

## How long records are kept {#retention}

Seven days by default. An operator changes it with `lifecycle.failedEventRetentionDays` in event-management's configuration (`0` keeps the records indefinitely); it is separate from `lifecycle.retentionDays`, which governs telemetry.

The original event itself, where the platform archived one, stays on the platform's failure stream only until that stream's size bound rolls it off. A record whose original has rolled off is still listed, and says that the original is no longer held (`payloadStatus: EXPIRED` in the API). It is not an error. Some records never had an original: when the platform could not read a message at all it kept nothing of it, and the console says "the original was not kept" (`payloadStatus: NOT_RETAINED`). The API never returns the original event itself.

## Deleting a tenant {#deletion}

The records are erased with the rest of the tenant's data. A deleted tenant leaves none.

## For operators {#operators}

`FailedEventsSustained` fires when any one reason is refused at more than one record a second for 15 minutes. The series behind it counts every record, including the ones the flood bound folded away. See [Observability](../deployment/observability.md) and the [metrics reference](../deployment/metrics-reference.md#event-management).
