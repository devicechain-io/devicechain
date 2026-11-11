---
title: Over-the-Air Updates
---

# Over-the-Air Updates

The **`update-management`** service delivers firmware, software and configuration images to
devices. It keeps a per-tenant catalogue of **artifacts**, lets you **assign** an artifact to a
device, sends the device what it needs to fetch the image, and tracks each **update attempt** until
the device proves it is running the new version.

:::note Status
**Available today:** the artifact catalogue with a verified streaming upload, assignment to one
device at a time, [rollouts](#rollouts) that assign an artifact to a device group a capped number of
devices at a time with pause, resume, abort and a failure threshold, a resumable download through a
short-lived grant, and tracking of each attempt with deadlines. The console has screens for artifacts,
signers and rollouts, and an Updates tab on each device for assigning and following an update; see
[Updates](/user-guide/updates) in the user guide. A small reference device in the repository follows the
whole device side of the protocol; see [Building an Updatable Device](../guides/building-an-updatable-device.md).
The update path was validated against that reference device, not against production hardware; see
[What has been validated](#validated).

**Not in 1.0:** rollouts have no waves, canary promotion, schedules or maintenance windows; a rollout
dispatches to its group a capped number of devices at a time and that is all. Managed updates are
**MQTT-only**: only a device connected over MQTT can receive one, because assignments travel on its MQTT
channel. Devices that speak only HTTP cannot. [LwM2M](./lwm2m.md) devices are not updated by the platform at all;
they are updated with ordinary commands. [Sparkplug](./sparkplug.md) defines no firmware update, so a Sparkplug
edge node can take a managed update only if it also connects as an ordinary MQTT device. See
[Which devices can be updated](#transports).
:::

## Turning it on {#enabling}

`update-management` is part of the `default` profile (and so of `full`); only the `telemetry` and
`ingest-only` profiles leave it out, and `enabledFunctionalAreas` can name it there. It depends on
`device-management`, and it stores artifact content in the [object store](./object-storage.md):
with no store configured, declaring an artifact is refused with `OBJECT_STORE_NOT_CONFIGURED`. On the
S3 backend an upload is buffered in memory, so artifacts are limited to 64 MiB there whatever the
configured ceiling.

### Before the first update {#first-update}

Four things have to be true before a device can take an update. The first two are about the instance,
the third is a decision you make once, and the fourth is the device.

1. **The instance has an object store.** A filesystem volume that more than one service can mount, or S3.
   See [An instance with no object store](#no-object-store).
2. **Devices can be given a download link.** The chart derives the link's base from the instance's
   ingress host, so this holds on any instance with an ingress; see `downloadBaseUrl` below. The console
   shows a notice on the Updates screens when it does not.
3. **A signer is registered and the artifact is signed by it.** By default
   (`allowUnverifiedArtifacts: false`) an artifact nobody has signed cannot be assigned: the assignment
   is refused with `UNVERIFIED_ARTIFACT_REFUSED`. Registering a signer takes the `artifact:trust`
   authority and a public key you generate; follow [Signing Updates](../guides/signing-updates.md).
   Turning `allowUnverifiedArtifacts` on removes the requirement and is meant for development
   instances only.
4. **The device speaks the protocol over MQTT** and holds the signer's public key. See
   [Which devices can be updated](#transports) and [Building an Updatable Device](../guides/building-an-updatable-device.md).

Two settings are deliberate decisions rather than defaults to leave alone. Both live under
`functionalAreas.update-management.config.updates` in the chart values:

| Setting | Default | Meaning |
| --- | --- | --- |
| `allowUnverifiedArtifacts` | `false` | With this `false`, only an artifact signed by an active [signer](#signers) can be assigned. Set it to `true` on a development or test instance to assign unsigned artifacts too. The value is shown in the `updateManagementInfo` query, so an instance running with it can be seen to be. |
| `downloadBaseUrl` | derived from the ingress host | The public URL devices reach the service at, with no query or fragment. When you do not set it, the chart sets it to `<public origin>/api/update-management`, where the origin is the ingress host with `https` if ingress TLS is on and `http` if it is not. A key you set wins, and setting it to an empty string turns downloads off. With no ingress there is no origin to derive from, so nothing is set: no device can be given a download grant, and a device's request is refused and counted. Set it in the chart values; the tuning overlay of an Instance refuses it, because it follows the instance's host. A plain `http` base on a host other than loopback starts, but the service logs a warning, and a device refuses an `http` download unless it was told otherwise. |

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

### An instance with no object store {#no-object-store}

Artifact content needs an object store, and the store is fixed when the instance is created (see
[the object store](../deployment/bootstrap.md#object-store)). One case needs care. The `default`
profile includes `update-management`. When an instance runs the `default` profile and its store is a
`ReadWriteOnce` filesystem volume on a cluster `dcctl` did not create, that volume can be mounted by one
service and another already holds it. So `dcctl` starts `update-management` with `updates.objectStore` set
to `none` instead of refusing. This applies to a new bootstrap and to an existing instance that gains the
service on upgrade. The other
value is `instance`, the default. `dcctl` sets `none` only in that case: an area you named yourself, the
`full` profile, a shareable volume, S3 and a cluster `dcctl` created are unchanged. The tuning overlay may
not set the key, and a chart-only install may use either value; any other value fails the render.

In that mode the service runs, and anything that does not need content works: signers, for example.
Declaring an artifact and uploading content are refused with `OBJECT_STORE_NOT_CONFIGURED`, rather than
written to a volume the service does not have. `dcctl` says so when it applies the change,
`updateManagementInfo` reports `objectStoreConfigured` as `false`, and the console shows a notice on the
Updates screens.

An instance bootstrapped without a store cannot gain one by upgrading. To use updates there, recreate it
with `--object-store s3`, or with a filesystem store and `--object-store-access-mode ReadWriteMany` on a
storage class that supports it.

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
The device's profile must be on the artifact's compatible list, and the assignment is refused
(`UNVERIFIED_ARTIFACT_REFUSED`) unless the artifact is [signed](#signers) by an active signer or
`allowUnverifiedArtifacts` is on. A [disabled device](../guides/device-credentials.md#disabling-a-device)
is refused with `DEVICE_DISABLED`; while a device is disabled, its update reports and download
requests are discarded and a download link it was already given stops working, until it is enabled
again, when it is re-offered its current assignment. An update already in progress is not cancelled by a disable: the [deadlines](#deadlines)
keep running and may move it to `TIMED_OUT` or `UNKNOWN`; if the device is enabled again before
the phase deadline passes, its next report continues the update, and a download link it was
already given works again. A device works toward **one target per component**:

- assigning while the component's previous update is still in progress is refused until that update
  is cancelled;
- assigning the same artifact again while that update is in progress returns the existing assignment;
- assigning after the previous update finished **supersedes** it, and the history is kept.

**An assignment carries the signature it was made under.** When the artifact is signed, assigning
picks the oldest signature on it whose signer is still active and **pins** it to the assignment. The
message the device receives has `trust: SIGNED` together with that signature (standard base64), the
signer's `keyId`, and the `algorithm` (`ECDSA_P256_SHA256` or `ED25519`), and the device checks them
against the keys it trusts. `signedBy` on the assignment names the signer it is pinned to. An
assignment made with no signature by an active signer (only possible while `allowUnverifiedArtifacts`
is on) pins nothing and stays `UNVERIFIED` on the wire. The pin does not move: if the signer is revoked
afterwards, an update already in flight keeps the signature it was assigned under, and every re-send of
the assignment is identical, while a *new* assignment of that artifact is refused. If the platform ever
cannot read the pinned signature, it sends nothing rather than sending the assignment as `UNVERIFIED`.

Two things follow for operators:

- **A device must hold the key of every signer that could be pinned.** The oldest signature by an
  active signer wins, so which signer a device is told to verify against depends on who has signed the
  artifact. Put a new signer's key on your devices **before** you revoke the old one, and before the old
  signer's signatures stop being the oldest active ones.
- **Turning `allowUnverifiedArtifacts` off stops unsigned assignments from being sent**, including a
  re-send when a device syncs. A device keeps whatever it already had; assign a signed artifact to move
  it forward.

`cancelUpdate` cancels a queued update at once. Before installation starts it records the request,
and the update becomes `CANCELLED` when the device confirms it has dropped the work. Once the device
has begun installing, cancelling is refused: an installation cannot be recalled.

Reading the catalogue, assignments and signers needs `artifact:read`. Changing the catalogue, assigning,
and attaching a signature need `artifact:write`. Registering or revoking a signer needs `artifact:trust`,
which is separate on purpose: being able to publish a binary does not include deciding whose signature
makes it trusted.

## Rolling out to a group {#rollouts}

A **rollout** assigns one artifact to the devices of a [device group](../user-guide/groups-facets-browse.md),
a capped number at a time, and can be paused, resumed and aborted.

1. `createUpdateRollout(request)` declares it as a `DRAFT`: the artifact (which must be `AVAILABLE` and
   assignable, exactly as for `assignArtifact`), the group, `maxInFlight` (1 to 1000) and an optional
   `failureThreshold`. Nothing is resolved or sent yet.
2. `startUpdateRollout(token)` **freezes the group's members** as the rollout's devices and moves it to
   `RUNNING`. A device that joins the group afterwards is not part of the rollout, and one that leaves it
   stays a target; start a new rollout to reach the newcomers. A dynamic group is resolved from its
   published version, which the rollout records. A group of more than 10,000 devices is refused
   (`ROLLOUT_TOO_LARGE`) rather than cut short.
3. The service then assigns the artifact to the devices in order, never letting more than `maxInFlight`
   of them have an unfinished update. Each device goes through the same rules as `assignArtifact`: a
   device that is missing, disabled, on an incompatible profile, or busy with a different update is
   **skipped** with that refusal's code (`DEVICE_NOT_FOUND`, `DEVICE_DISABLED`, `INCOMPATIBLE_PROFILE`,
   `UPDATE_IN_PROGRESS`) and holds no place. A device already working on, or already updated to, the same
   artifact is counted with the assignment it has rather than told again.
4. The rollout is `COMPLETED` when no device is waiting and none has an unfinished update.

A device's outcome is always its own update attempt: `SUCCEEDED` when it reached `UPDATED`, `FAILED` for
`FAILED` or `TIMED_OUT`, and `UNKNOWN` while the outcome is unknown. **An `UNKNOWN` device keeps its place**
under `maxInFlight` and counts as a failure, because it may have installed the update and not come back.

| Control | What it does | What it does not do |
| --- | --- | --- |
| `pauseUpdateRollout` | No further device is told to update. | Devices already updating carry on. |
| `resumeUpdateRollout` | Dispatching continues. | It does not reset the failure count: after a threshold pause it is refused (`ROLLOUT_FAILURE_THRESHOLD_REACHED`) unless you pass a higher `failureThreshold`. |
| `abortUpdateRollout` | Devices not yet told are `CANCELLED`, and the rollout ends for good. | Devices already updating are left to finish, and keep being reported. Cancel any of them with `cancelUpdate`. |

When the number of failed and unknown devices reaches `failureThreshold`, the rollout pauses itself
with the reason `FAILURE_THRESHOLD`. It also pauses with `ARTIFACT_NOT_ASSIGNABLE` if the artifact is
retired, or loses the signature this instance requires, while it runs.

`updateRollout(token)` and `updateRollouts(criteria)` return each rollout with its `progress` (counts of
pending, in-flight, succeeded, failed, unknown, cancelled and skipped devices), and
`updateRolloutTargets(token, criteria)` pages its devices with their outcome, filterable by outcome.
Rollouts need the same authorities as single assignments: `artifact:read` to read them and
`artifact:write` to create and control them. A member limited to some customers cannot see or
control rollouts, since a rollout spans devices outside that member's scope. Every start, pause, resume
and abort is recorded in the [audit journal](../user-guide/audit.md) under the person who did it.

Rollouts keep running across restarts and with several replicas of the service: each replica advances
the running rollouts every few seconds, and a rollout is advanced by one replica at a time, so the cap
holds however many replicas there are. Waves, schedules and maintenance windows for rollouts are not
available yet.

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
| `FAILED` | The device reported a failure, or, for an `UNKNOWN` update, its inventory showed it had rebooted onto a version other than the target. |
| `TIMED_OUT` | A deadline passed before installation was reported started. |
| `UNKNOWN` | A deadline passed during installation or reboot. |
| `CANCELLED` | The update was cancelled before installation began, or an operator abandoned an `UNKNOWN` update. |

A device that refuses an assignment because of its signature reports `FAILED` with the code
`UNKNOWN_SIGNER`, when it does not trust the signer's key, or when it requires a signature and the
assignment is `UNVERIFIED`, or `SIGNATURE_INVALID`, when the signature does not verify. It checks a
signature against the bytes it holds, so it reports a bad or unknown signature after `DOWNLOADED`, once it
has hashed them. A device that requires signed images can refuse an `UNVERIFIED` assignment sooner, right
after `RECEIVED` and before downloading anything, and the reference device does. A device may report `FAILED` at any stage, so both are valid.

Reports only move an attempt forward; a report for an earlier stage is ignored. **`UPDATED` is
reached only on evidence**: the device reports the target version running, and when the artifact
needs a reboot (every `FIRMWARE` artifact does; `SOFTWARE` and `CONFIGURATION` artifacts do not), from a boot other than the one it installed in. An acknowledgement, or an
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

## Signers {#signers}

An artifact's **trust status** says whether someone you trust has vouched for it. It is `UNVERIFIED`
until at least one **signer** you registered has signed it, and `SIGNED` after. It is worked out each
time it is read, from the signatures on record and the state of the signers behind them, so
`artifacts` can be filtered by `trustStatus`.

A signer is a public key. `registerArtifactSigner(name, publicKeyPem)` takes one SPKI `PUBLIC KEY` PEM
block holding an ECDSA P-256 or Ed25519 key; other key types and curves are refused. Register only the
public half: the platform never needs a private key, and one is refused (`SIGNER_KEY_REFUSED`) without
being echoed, logged or stored. The signer's `keyId` is the lowercase hex SHA-256 of the key's DER
encoding, and a key can be registered only once per tenant.

You sign the artifact's **SHA-256 digest**, the 32 raw bytes and not the hex text, with the private
key you keep: ECDSA P-256 produces an ASN.1 DER signature of the digest, and Ed25519 signs the 32 digest
bytes. `addArtifactSignature(artifactToken, signerToken, signature)` takes the signature as standard
base64 and checks it against the digest the platform stored when the artifact was declared. A signature
that does not verify is refused (`SIGNATURE_INVALID`), so a signature made over other bytes cannot be
attached, and a revoked signer is refused (`SIGNER_REVOKED`). Signing again with the same signer changes
nothing. The signature covers the digest only: it does not say which version, component or compatible profiles the
image is for, so the same image declared under another version is vouched for by the same signature.
The device has to enforce version and component itself; see [On the device](#device-side).

`revokeArtifactSigner(token)` is final. The signer's signatures stay on record but stop counting, so
every artifact it alone vouched for reads `UNVERIFIED` straight away and can no longer be assigned
(unless `allowUnverifiedArtifacts` is on); no artifact is rewritten. A signer's
`soleSignedArtifactCount` tells you beforehand how many artifacts that would affect. An assignment
already made is not recalled.

## What "verified" means {#verified}

`VERIFIED`, and the size and digest check at upload, establish **integrity**: the bytes are the bytes
that were declared. They say nothing about who produced them; that is what a [signature](#signers) adds.
An assignment of a signed artifact tells the device `SIGNED` and hands it the signature, so a device
that holds the signer's public key can check that the image is genuine before it installs it. Both
signature algorithms sign the 32 raw bytes of the SHA-256 digest, not the file.

## Which devices can be updated {#transports}

A managed update is a conversation on the device's MQTT channel: the platform publishes the assignment
and the download link on `device-desired`, and the device answers on `device-reports`. Only the download
itself is HTTP. That decides which devices the platform can update.

| Device | Managed update | What to do instead |
| --- | --- | --- |
| Connects over MQTT | Yes | Follow [Connecting a Device](../guides/connecting-a-device.md#over-the-air-updates). |
| Speaks only HTTP | No. It can send events over HTTP, but nothing can tell it an assignment exists, and it has no channel to report on. | Connect it over MQTT as well, or update it by your own means. |
| [LwM2M](./lwm2m.md) | No. The platform does not reach LwM2M devices. | Drive the standard Firmware Update object with ordinary commands, as in [Firmware update with Object 5](./lwm2m.md#firmware-update-object-5). That runbook is a supported path, but it is not a managed job: no artifact, no signature relay, no attempt tracking. |
| [Sparkplug](./sparkplug.md) edge node | No. Sparkplug defines no firmware update, and DeviceChain does not invent one. | A node that should be updated through DeviceChain also connects to DeviceChain as an ordinary MQTT device and uses this channel. See [updating an edge node](./sparkplug.md#updating-an-edge-node). |

## On the device {#device-side}

A device learns of an assignment on its `device-desired` topic, asks for a download grant, fetches
the image over HTTP with resumable `Range` requests, and reports each stage on `device-reports`. The
messages are in [Connecting a Device](../guides/connecting-a-device.md#over-the-air-updates).

The platform checks the bytes against the digest when they arrive and hands the device a signature
over that digest, and the device decides. Because the signature covers only the digest, a correctly
signed older image, or one built for another component, is still a valid signed image. The platform does
not bind the version or the component to the signature, so the device must refuse a version that is not
newer than the one it runs, and an image for a component that is not its own. It must also run none of
the image before the digest and signature have been checked, keep the trust keys locally rather than
taking one from a message, and report `RUNNING` only from evidence of the version that is actually running.
[Building an Updatable Device](../guides/building-an-updatable-device.md) walks through a reference
implementation that does all of this and shows the order of checks.

## What has been validated {#validated}

The update path was validated end to end against the **reference device**, the small program described in
[Building an Updatable Device](../guides/building-an-updatable-device.md). It ran as a separate process
outside the cluster, reached the instance only through its public broker and ingress, verified the signer and
the hash, fetched a signed image through the download grant, replaced its own executable, restarted, and
reported the version it was then running; the platform showed the assignment `UPDATED`. That is the extent of
the claim: the platform has not been validated on production hardware or against any other device
implementation. A device you build is yours to test, and the checks above are the ones to repeat on it.

These failure cases were exercised against the same device, each with the state the platform showed:

| Case | What the platform showed |
| --- | --- |
| A signature that does not verify | Refused when it is attached (`SIGNATURE_INVALID`); the artifact stays `UNVERIFIED` and cannot be assigned. |
| Bytes that do not hash to the declared digest | Refused at upload; the artifact stays `UPLOADING`. A device that receives a corrupted image reports `FAILED` with `VERIFICATION_FAILED`. |
| A signature by a key the device does not trust | `FAILED` with `UNKNOWN_SIGNER`; the device keeps running its old version. |
| A download interrupted part-way | The device resumes with a `Range` request from the bytes it holds; the platform shows `DOWNLOADING` throughout and then `UPDATED`. |
| The device goes offline mid-update, then reconnects | The attempt stays non-terminal (`DOWNLOADING`, or `QUEUED` when the device was offline at assignment) and completes after the reconnect. |
| Cancellation | While the device is unreachable the request is recorded (`cancelRequested`) and the attempt stays non-terminal until the device confirms, then `CANCELLED`; a queued update is `CANCELLED` at once. The device keeps its version and leaves no partial file. |
| A deadline passes | The attempt ends `TIMED_OUT` (failure code `DEADLINE`). A device that returns afterwards does not reopen it. |
| One device of a rollout fails | With `failureThreshold` reached the rollout pauses with the reason `FAILURE_THRESHOLD` and the devices not yet dispatched are untouched. Resumed with a higher threshold, it dispatches the rest and completes, still reporting the failed device. |

## Watching it {#monitoring}

The service's metrics, including report verdicts, download grants, attempts the deadlines moved
and what rollouts dispatched, are listed in the [metrics reference](../deployment/metrics-reference.md#update-management).

## Related

- **[Building an Updatable Device](../guides/building-an-updatable-device.md)**: the device side, walked through on a reference device.
- **[Signing Updates](../guides/signing-updates.md)**: creating keys and signing artifacts.
- **[Object storage](./object-storage.md)**: where artifact content is kept.
- **[Device configuration](../guides/connecting-a-device.md#reserved-device-topics)**: per-device settings delivered on the same device channels.
- **[GraphQL API](../reference/graphql-api.md#endpoints)**: the endpoints, including the upload and download routes.
