---
title: Over-the-Air Updates
---

# Over-the-Air Updates

The opt-in **`update-management`** service delivers firmware, software and configuration images to
devices. It keeps a per-tenant catalogue of **artifacts**, lets you **assign** an artifact to a
device, sends the device what it needs to fetch the image, and tracks each **update attempt** until
the device proves it is running the new version.

:::note Status
**Available today:** the artifact catalogue with a verified streaming upload, assignment to one
device at a time, a resumable download through a short-lived grant, and tracking of each attempt
with deadlines.

**Not yet:** nothing authenticates **who produced** an artifact (the platform checks only that the
bytes are the bytes that were declared), so assigning one must be allowed explicitly; there is no
fleet rollout (staged or percentage-based assignment across many devices); and devices that speak
only HTTP cannot receive updates, because assignments travel on the device's MQTT channel.
:::

## Turning it on {#enabling}

`update-management` is not part of the `default` profile. Install with `--profile full`, or name it
in `enabledFunctionalAreas`. It depends on `device-management`, and it stores artifact content in
the [object store](./object-storage.md): with no store configured, declaring an artifact is refused
with `OBJECT_STORE_NOT_CONFIGURED`. On the S3 backend an upload is buffered in memory, so artifacts
are limited to 64 MiB there whatever the configured ceiling.

Two settings are deliberate decisions rather than defaults to leave alone. Both live under
`functionalAreas.update-management.config.updates` in the chart values:

| Setting | Default | Meaning |
| --- | --- | --- |
| `allowUnverifiedArtifacts` | `false` | Every artifact is unverified today, so with this `false` **no artifact can be assigned**. Set it to `true` on a development or test instance. The value is shown in the `updateManagementInfo` query, so an instance running with it can be seen to be. |
| `downloadBaseUrl` | unset | The public URL devices reach the service at, for example `https://<ingress host>/api/update-management`, with no query or fragment. Without it no device can be given a download grant: a device's request is refused and counted. |

The remaining settings bound the service:

| Setting | Default | Meaning |
| --- | --- | --- |
| `maxArtifactBytes` | 512 MiB | The largest single artifact. It may not exceed `maxTenantArtifactBytes`. |
| `maxTenantArtifactBytes` | 2 GiB | The most one tenant's artifacts may hold in total. |
| `maxConcurrentUploads` | 4 | Uploads one replica streams at once. |
| `maxConcurrentDownloads` | 16 | Downloads one replica streams at once. |
| `capabilityTtlSeconds` | 900 | How long a download grant lives. Fifteen minutes is also the ceiling; a larger value is refused at startup. |
| `sweepIntervalSeconds` | 30 | How often the platform looks for attempts whose device went quiet. |
| `deadlines.*` | see [below](#deadlines) | How long the platform waits in each phase of an attempt. |

A negative value, an unknown key, or a `downloadBaseUrl` that is relative, carries credentials or a
query is refused at startup.

## Artifacts {#artifacts}

An artifact is declared first, with no content: `createArtifact` takes a token, a **kind**
(`FIRMWARE`, `SOFTWARE` or `CONFIGURATION`), a name, a version label, the **component** of the device
it updates (for example `firmware` or `app/agent`), the exact size in bytes, the SHA-256 digest, and
the device profiles it is **compatible** with (at least one; each must exist). Name, component and
version together are unique within the tenant. The declared size is checked against both ceilings
before anything is stored.

The artifact starts `UPLOADING`. Its content is then sent with an HTTP `PUT` to
`/api/update-management/updates/artifacts/{token}/content`, authenticated with a tenant access token
that holds `artifact:write`; the [GraphQL API reference](../reference/graphql-api.md#endpoints)
covers the ingress settings that route needs. The upload is streamed and judged by what actually
arrived: a body that is longer or shorter than the declared size, or that hashes to a different
digest, is discarded and the artifact stays `UPLOADING`. Only content that matches makes the
artifact **`AVAILABLE`**, after which it is immutable. `retireArtifact` withdraws it from new
assignments; its record and content are kept.

## Assigning an update {#assigning}

`assignArtifact(deviceToken, artifactToken)` tells one device to install one `AVAILABLE` artifact.
The device's profile must be on the artifact's compatible list, and the assignment is refused while
`allowUnverifiedArtifacts` is off. A device works toward **one target per component**:

- assigning while the component's previous update is still in progress is refused until that update
  is cancelled;
- assigning the same artifact again returns the existing assignment;
- assigning after the previous update finished **supersedes** it, and the history is kept.

`cancelUpdate` cancels a queued update at once. Before installation starts it records the request,
and the update becomes `CANCELLED` when the device confirms it has dropped the work. Once the device
has begun installing, cancelling is refused: an installation cannot be recalled.

Reading the catalogue and assignments needs `artifact:read`; every change needs `artifact:write`.

## How an attempt progresses {#states}

Each assignment carries one **attempt**, whose state the platform derives from what the device
reports:

| State | Meaning |
| --- | --- |
| `QUEUED` | Created; the device has not reported yet. |
| `INITIATED` | The device acknowledged the assignment. |
| `DOWNLOADING` | The device is downloading the artifact, with bytes and total if it reports them. |
| `DOWNLOADED` | The device holds all the bytes. |
| `VERIFIED` | The bytes the device holds hash to the artifact's digest. |
| `UPDATING` | The device is installing. |
| `REBOOTING` | The device is rebooting into the new version. |
| `UPDATED` | The device is running the target version. |
| `FAILED` | The device reported a failure, or came back on a version other than the target. |
| `TIMED_OUT` | A deadline passed before installation was reported started. |
| `UNKNOWN` | A deadline passed during installation or reboot. |
| `CANCELLED` | The update was cancelled before installation began, or an operator abandoned an `UNKNOWN` update. |

Reports only move an attempt forward; a report for an earlier stage is ignored. **`UPDATED` is
reached only on evidence**: the device reports the target version running, and when the artifact
needs a reboot, from a boot other than the one it installed in. An acknowledgement, or an
installer's own "success", is not evidence.

### Deadlines {#deadlines}

Deadlines are measured from the last report the platform accepted, so a slow device that keeps
reporting is not timed out. Each is a key under `updates.deadlines`, in seconds:

| Key | Covers | Default | When it passes |
| --- | --- | --- | --- |
| `acknowledgeSeconds` | `QUEUED`, `INITIATED` | 1 day | `TIMED_OUT` |
| `downloadSeconds` | `DOWNLOADING`, `DOWNLOADED` | 6 hours | `TIMED_OUT` |
| `verifySeconds` | `VERIFIED` | 30 minutes | `TIMED_OUT` |
| `installSeconds` | `UPDATING` | 1 hour | `UNKNOWN` |
| `confirmSeconds` | `REBOOTING` | 1 hour | `UNKNOWN` |

Silence while installing or rebooting is **`UNKNOWN`**, never failure and never success: the device
may be on either version. An `UNKNOWN` attempt leaves that state only on evidence. The device's next
inventory report says what it runs, and the attempt becomes `UPDATED`, or `FAILED` if the device has
rebooted and is not on the target. Until then a new assignment for that component is refused. If the
device will never report, `abandonUnknownUpdate(assignmentToken, reason)` ends the update as
`CANCELLED`, keeps your reason, journals the change under your name, tells the device, and frees the
component. It does not find out what the device runs, so check before relying on either version.

## What "verified" means {#verified}

`VERIFIED`, and the size and digest check at upload, establish **integrity**: the bytes are the bytes
that were declared. They say nothing about who produced them. Every artifact's trust status is
`UNVERIFIED`, and so is the trust every assignment tells the device, so a device that needs to know
an image is genuine must check a signature of its own.

## On the device {#device-side}

A device learns of an assignment on its `device-desired` topic, asks for a download grant, fetches
the image over HTTP with resumable `Range` requests, and reports each stage on `device-reports`. The
messages are in [Connecting a Device](../guides/connecting-a-device.md#over-the-air-updates).

## Watching it {#monitoring}

The service's metrics, including report verdicts, download grants and attempts the deadlines moved,
are listed in the [metrics reference](../deployment/metrics-reference.md#update-management).

## Related

- **[Object storage](./object-storage.md)**: where artifact content is kept.
- **[Device configuration](../guides/connecting-a-device.md#reserved-device-topics)**: per-device settings delivered on the same device channels.
- **[GraphQL API](../reference/graphql-api.md#endpoints)**: the endpoints, including the upload and download routes.
