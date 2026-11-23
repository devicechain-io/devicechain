---
title: Updates (over the air)
sidebar_position: 16
---

import GuideShot from '@site/src/components/GuideShot';

# Updates (over the air)

The **Updates** area is where you deliver new firmware, software or configuration to your devices
over the air. You upload an update image (an **artifact**), vouch for it with a signature from a key
you trust (a **signer**), and assign it to a device. The device then downloads and installs it, and
the console follows each step until the device proves it is running the new version.

This chapter is for tenant administrators and the people who release device software. It covers the
console screens; for how updates work end to end, see
[over-the-air updates](/concepts/over-the-air-updates), and for creating keys and signing
artifacts, see [signing updates](/guides/signing-updates).

The update service is optional, and not every installation runs it. If yours does not, the sidebar
has no **Updates** group and devices have no Updates tab. See
[turning it on](/concepts/over-the-air-updates#enabling).

In the sidebar, **Devices → Updates** has three screens: **Artifacts**, **Rollouts** and **Signers**. Updating a
whole device group happens on [Rollouts](#rollouts); assigning an update to a single device happens on the device's own page, on its [Updates tab](#assigning-an-update-to-a-device).

## Artifacts

An artifact is one update image: a file plus what it is (its kind, name, version and component) and
which device profiles it is compatible with. Once its content is uploaded, an artifact cannot be
changed.

<GuideShot id="updates-artifacts-list" alt="The Artifacts list with three thermostat firmware images: 2.5.0-rc.1, Unverified and Available; 2.4.0, Signed and Available; and 2.3.1, Signed and Retired. Each row shows the component, the size and the beginning of the SHA-256 digest. Filters and a New artifact button are at the top." />

Each row shows the name, version, component, **Trust** (Signed or Unverified), **State**, size and the
SHA-256 **Digest**. The states are:

- **Uploading**: the artifact is declared but its content is not stored yet.
- **Available**: the content is stored and verified, so the artifact can be assigned.
- **Retired**: the artifact is withdrawn from new use. Its metadata and content remain.

The **Any trust** and **Any state** pickers filter the whole catalogue. The **Filter this page…** box
is different: it only narrows the page already loaded, by name, version, component, token or digest.
To look further, use **Next**.

### Adding an artifact

Click **New artifact**. You start from the file, and the console does the rest in one step: it
computes the file's size and SHA-256 in your browser, declares the artifact, and uploads the content.

<GuideShot id="updates-artifact-new" alt="The New artifact drawer. A Choose file button with the note Largest accepted: 512 MiB, then Kind, Name, Version, Component, Token, Compatible device profiles and Media type fields." />

1. Click **Choose file**. The largest file your instance accepts is shown below the button; a larger
   file is refused before anything is sent.
2. Choose the **Kind**: Firmware, Software or Configuration.
3. Enter the **Name**, **Version** and **Component**. The version is a label the platform does not
   interpret. The component is the part of the device this updates, such as `firmware` or
   `app/agent`. Name, component and version together must be unique.
4. The **Token** is filled in from the name and version. You can change it.
5. Choose the **Compatible device profiles**. The artifact can only be assigned to devices on these
   profiles, so choose at least one.
6. Optionally, set the **Media type** (left empty, it is `application/octet-stream`), a **Source** and
   **Notes**.
7. Click **Declare and upload**.

The upload shows its progress. When it finishes, the console compares what the service stored with
what you declared. **Content stored and verified** means they match. If they do not, the console
says so, and the artifact stays unavailable: check the file and upload it again.

If the upload fails after the artifact has been declared, the artifact exists but has no content.
Click **Retry upload**, or **Open artifact** and upload the file from its page.

### The artifact page

Open an artifact to see its page. The header shows its name and version, its token, and badges for
its trust, state and kind. **Retire** withdraws the artifact from new use, after you confirm.
Retiring cannot be undone.

<GuideShot id="updates-artifact-signed" alt="The page of Thermostat firmware 2.4.0, with Signed, Available and FIRMWARE badges and a Retire button. The Details panel lists the component, version, size, media type, digest, declared and last-changed times, the compatible profile bp-thermostat-profile, and the recorded notes, source and uploadedBy entries. Below it, the Signatures and trust panel shows the Signed badge and one signature, from Acme release signing key." />

The **Details** panel lists what was declared, with the full digest behind the copy button. Any
provenance recorded with the artifact, such as its notes and source, follows. Those entries are shown
under the keys they were stored with, untranslated (for example **UPLOADEDBY**).

An artifact still in the **Uploading** state has an **Upload content** panel. Choose the file declared
for the artifact; the console checks its size and SHA-256 before sending anything, and refuses a file
that is not the declared one.

### Signatures and trust

An artifact's trust is worked out each time it is read, from the signatures on record and the state
of the signers behind them:

- **Signed**: at least one active signer has a verified signature on the artifact.
- **Unverified**: no active signer has. The platform still checks that the bytes are the declared
  bytes, but says nothing about who made them.

The **Signatures and trust** panel shows the trust, then lists the signatures recorded on this
artifact, oldest first. For each one you see the signer's name, its key ID, the algorithm, the
signer's state, when the signature was added and by whom, and whether it still **Counts toward
trust**. A signature from a signer that has since been revoked stays on record but reads **No longer
counts (signer revoked)**. An artifact with no signatures says so.

<GuideShot id="updates-artifact-signatures" alt="The lower part of the Thermostat firmware 2.4.0 page: the Signatures and trust panel with the Signed badge, and a table with one signature, from Acme release signing key: its key ID, the ED25519 algorithm, an Active state, when and by whom it was added, and Counts toward trust. Below it, the Add a signature form with Signer key ID and Signature (base64) fields." />

To add a signature, fill in **Add a signature** at the bottom of the panel:

- **Signer key ID**: the key ID of a registered signer, as shown on the Signers screen or printed by
  your signing tool. The console tells you which signer it matches, or warns you if the signer is
  revoked or not found.
- **Signature (base64)**: the signature over the artifact's digest. An Ed25519 key signs the 32 raw
  digest bytes; an ECDSA P-256 signature matches `cosign sign-blob`.

Click **Add signature**. The service verifies the signature against the artifact's stored digest and
refuses one that does not verify. The [signing guide](/guides/signing-updates) shows the
commands for each step.

### Unverified artifacts

An unverified artifact carries a notice at the top of its page. It tells you what your instance
allows: usually, an unverified artifact **cannot be assigned** to a device until it is signed. An
instance can be set to allow it, which is meant for development only. If the console could not read
the setting, the notice tells you to assume the artifact cannot be assigned.

<GuideShot id="updates-artifact-unverified" alt="The page of Thermostat firmware 2.5.0-rc.1, with Unverified, Available and FIRMWARE badges. A notice reads: This artifact is unverified. This instance does not allow unverified artifacts to be assigned, so this one cannot be assigned to a device until it is signed. It links to the signing guide." />

## Signers

A **signer** is a public key your tenant trusts to vouch for artifacts. Only the public key is stored;
the private key stays with whoever signs. Open **Devices → Updates → Signers**.

<GuideShot id="updates-signers-list" alt="The Signers list with two signers: Acme release signing key, ED25519, Active, with 2 sole-signed artifacts and a Revoke button; and Contract manufacturer key (2025), ECDSA_P256, Revoked, with its revocation time and 0 sole-signed artifacts." />

The list shows each signer's name, key ID, algorithm, state, when it was registered and revoked, and
its **Sole-signed artifacts**: how many artifacts have no other active signer, and so would become
unverified if this one were revoked.

Registering and revoking signers requires the `artifact:trust` permission, which is separate from the
permission to upload artifacts. Without it, you see the list but no actions.

### Registering a signer

Click **Register signer** and enter:

- **Name**: a label to recognize the signer by, such as the team or build pipeline that holds the key.
- **Public key (PEM)**: one SPKI public key block, ECDSA P-256 or Ed25519.

<GuideShot id="updates-signer-register" alt="The Register signer drawer with a Name field, a Public key (PEM) field showing the BEGIN and END PUBLIC KEY lines, a link to how to generate a key and compute its key ID, and a Register button." />

Click **Register**. Paste the public half of the key pair only. If you paste something that looks like
a private key, the console sends nothing and clears the field. The link in the drawer leads to the
[signing guide](/guides/signing-updates), which shows how to generate a key and compute its key
ID.

### Revoking a signer

Click **Revoke** on an active signer. The confirmation tells you how many artifacts have no other
active signer and will become **UNVERIFIED**. That count includes retired artifacts.

<GuideShot id="updates-signer-revoke" alt="The confirmation dialog Revoke Acme release signing key. It says 2 artifacts have no other active signer and become UNVERIFIED, and that update assignments already sent keep the signature they were pinned with. Cancel and Revoke signer buttons." />

Revocation is permanent. The signer's signatures stay on record but stop counting, so artifacts it
alone vouched for become unverified at once. An update already assigned keeps the signature it was
assigned with, so it is still delivered as it was assigned. See
"Revoking a signer" in the [signing guide](/guides/signing-updates).

## Assigning an update to a device

Updates are assigned one device at a time, from the device's page. Open the device and choose the
**Updates** tab. The tab appears only when your instance runs the update service and you are allowed
to read updates.

<GuideShot id="updates-device-tab" alt="The Updates tab of the Lobby thermostat device. Under Current update, Thermostat firmware 2.4.0 is Signed and QUEUED, with its component, attempt ID, progress Not reported, last report time, assigner, Needs reboot Yes, and the pinned signer Acme release signing key, ACTIVE. A Cancel update button follows. Below, the Assign an update section has an Artifact picker, a note that the device already has an update in progress, and a disabled Assign button." />

The links at the top lead to the signing guide and to how signers work. **Refresh** reloads the tab.

### Current update

The **Current update** card shows the update the device is working on: the artifact and its trust,
the state of the update attempt, the component, the attempt ID, the progress, when the device last
reported, who assigned it, and whether it needs a reboot. **Pinned signer** shows the signature the
device was sent with the update. If that signer is revoked later, the card says so: the update keeps
the signature it was assigned with.

The state badge follows what the device reports, from **QUEUED** through downloading, verifying,
installing and rebooting, to **UPDATED**. The update reaches **UPDATED** only on evidence: the device
reports the new version running. The full list of states is in
[how an attempt progresses](/concepts/over-the-air-updates#states).

**QUEUED** means the platform has created the update but the device has not reported anything about
it yet. Progress reads **Not reported** until it does. In the screenshot, the update stays QUEUED
because the simulated thermostat in this demo tenant never reports update progress. A real device
that receives the assignment acknowledges it and moves it forward.

If the update fails, the card shows why: the stage it failed at, whether the device reported the
failure or the platform decided it, and, for the common signature and download failures, what to do
about it.

### Cancelling or abandoning an update

- **Cancel update** is offered until installation begins. A **QUEUED** update is cancelled at once,
  and a new one can be assigned afterwards. Once the device has picked up the update, cancelling
  sends it a request to stop: the cancel completes only when the device confirms, and until then the
  card shows **Cancel pending**. Once installation has begun, the update can no longer be cancelled.
- **Abandon update** is offered when an update is in the unknown state: the device stopped reporting
  while it was installing or rebooting, so nobody knows which version it is running. Abandoning ends
  the update as cancelled, keeps the **Reason** you give (it is required) on the update, records the
  change under your name, sends the device a cancel, and frees the device for a new assignment. It does not find out what the device is
  running, so check its inventory before relying on either version.

### Assigning an update

Use **Assign an update**:

1. Choose an **Artifact**. Only available artifacts are listed, and the device's profile must be on
   the artifact's compatible list. An unverified artifact is marked **Not assignable** when your
   instance does not allow unverified artifacts.
2. Click **Assign**.

While the device already has an update in progress, the section says so, and assigning another is
refused until that one is cancelled or finishes. When the instance does not allow unverified
artifacts, **Assign** stays disabled for an unverified one, with the reason shown.

### Assignment history

**Assignment history** lists the device's past and current assignments, with the artifact, the state,
any failure, when each was assigned, and by whom.

### A disabled device

When a device is disabled, the tab says that it is cut off from updates. No new update
can be assigned until the device is enabled again, and **Assign** is disabled. Disabling the device
does not recall an update already in flight; cancel it on this tab if it should not proceed. A
queued update is cancelled at once. One the device has already picked up stays **Cancel pending**
while the device is disabled, because its reports are refused: the cancel completes when the device
is enabled again and confirms, or the update ends as timed out at its stage deadline.

## Rollouts {#rollouts}

**Rollouts** update a whole device group, a few devices at a time. Use it when one device at a time
is too slow and every device at once is too risky.

**New rollout** asks for a token, an optional name, an **Artifact** (only available ones are offered),
a **Device group**, **Devices updating at once** and an optional **Failure threshold**. The rollout is
created as a draft and nothing is sent to any device yet.

On the rollout's page:

- **Start** freezes the group's current members as the rollout's devices and tells the first ones to
  update. Devices that join the group later are not included; devices that leave stay in the rollout.
- **Pause** stops more devices being told. Devices already updating carry on, and **Resume** carries
  on from where it stopped.
- **Abort** cancels the devices not yet told and ends the rollout. Devices already updating are left to
  finish; you can cancel any of them from its own [Updates tab](#assigning-an-update-to-a-device).

The summary shows how many devices are pending, in flight, succeeded, failed, unknown, cancelled and
skipped, and the **Devices** table lists each one with its outcome, filterable by outcome. A skipped
device was refused by the same rules as a single assignment (for example a disabled device, or one
busy with another update), and its **Reason** column says which. A device whose outcome is unknown
counts as a failure and keeps its place under **Devices updating at once**.

When failed and unknown devices reach the **Failure threshold**, the rollout pauses itself. To carry
on, enter a higher threshold and choose **Resume with this threshold**: resuming never resets the
count. A rollout also pauses itself if its artifact is retired while it runs.

Rollouts need `artifact:read` to view and `artifact:write` to create and control.

## Members limited to some customers {#members-limited-to-some-customers}

A member whose access is limited to some of your customers, and who holds `artifact:read`, can read
the update assignments of the devices that were assigned to one of those customers when the update
was assigned to them. It cannot assign, cancel or abandon an update, and it cannot list artifacts,
signers or rollouts. Assignments made before your instance recorded who a device was assigned to are not shown
to such a member.

## Related

- [Over-the-air updates](/concepts/over-the-air-updates): artifacts, assignments, update states,
  deadlines and signers.
- [Signing updates](/guides/signing-updates): generating a key, registering it, signing an
  artifact and rotating keys.
- [Building an updatable device](/guides/building-an-updatable-device): a reference device that verifies,
  installs and rolls back an update.
