---
sidebar_position: 13
title: Building an Updatable Device
---

# Building an Updatable Device

A device that takes over-the-air updates has to do more than download a file. This guide walks through a small reference device that does what a real connected product must do: follow the update protocol, refuse what it should refuse, replace its own executable, and prove to the platform which version is really running.

The reference device is in the repository under `examples/ota-device`. It is a Go program of about an afternoon's reading, built **only from the published protocol** in [Connecting a Device](./connecting-a-device.md#over-the-air-updates), so a successful update says something about that protocol and not about the platform's internals. It imports nothing from the platform, and a test fails if that ever changes.

:::note An example, not a product
It is built and tested with the rest of the repository but never released: there are no published binaries and no compatibility promise. A file rename standing in for a bootloader is a teaching device, not something to ship. See [What a real device must add](#what-a-real-device-must-add).
:::

## What you need {#prerequisites}

- An instance that runs the update service and can give devices a download link. See [Before the first update](../concepts/over-the-air-updates.md#first-update).
- A device that connects over MQTT with its own [credential](./device-credentials.md). Only MQTT devices can receive a managed update; see [Which devices can be updated](../concepts/over-the-air-updates.md#transports).
- A signer, and an artifact signed by it. An artifact nobody has signed cannot be assigned unless the instance allows unverified artifacts. See [Signing Updates](./signing-updates.md).
- The signer's public key in a PEM file on the device. This is the device's **trust set**: one or more `PUBLIC KEY` blocks, concatenated.

## The launcher and the firmware {#launcher-and-firmware}

The example is two programs, because installing a new image means a different program has to start after the reboot.

| Program | Stands in for | Job |
| --- | --- | --- |
| `cmd/ota-boot` | The bootloader | Picks the slot to run. A freshly installed slot gets exactly one try: if it does not confirm itself, the launcher runs the last confirmed slot again and leaves a note saying so. It kills a new slot that hangs without confirming, and it is never updated over the air. |
| `cmd/ota-device` | The firmware | Connects to the instance over MQTT as one device, follows the update protocol, installs a verified image into the inactive slot, and exits with status 75 ("reboot") so the launcher starts it. |

Two slots, `a` and `b`, hold an image each. The state directory records which is **confirmed**, which is **pending**, and how many tries the pending one has left, in a file that is written atomically. Updating always writes the slot that is not running, so the running image is never touched while it runs.

The version the device reports as running is the one compiled into the binary that is running, and the `digest` it reports is the SHA-256 of that very file. It is never read from configuration and never copied from an assignment, so a device cannot be talked into saying it runs something it does not.

## The order of checks {#order-of-checks}

When an assignment arrives, the device does these in order. The order is the point: nothing from the download runs until its integrity and authenticity have been checked.

1. **Policy, before anything is downloaded.** The component must be this device's. An unsigned assignment is refused unless the device was told to accept one. The version must be valid semantic versioning and **strictly newer** than the one running; an equal version is a replay, and a version that is not semver is refused rather than guessed at.
2. **Disk space** for the part of the download still to come.
3. **Download**, with `Range` requests, into a partial file in the inactive slot. A response that starts anywhere but the requested offset is refused. A byte past the declared size is a failure, and the partial file is deleted. An expired grant asks for a new one, a bounded number of times.
4. **Digest.** The SHA-256 of the bytes held equals `artifact.digest`. Nothing has been executed.
5. **Signature.** The detached signature over the raw 32-byte digest verifies against a key in the device's own trust set. The `keyId` on the wire only selects a key from that set; a key is never taken from a message. Nothing has been executed.
6. **Identity.** Only now is the image run, once, in a mode that prints its product and version and exits, with an empty environment, a scratch working directory and a deadline. It must name this device's product and the version the assignment named.
7. **Install.** The image goes into the inactive slot, is staged as pending, and the device reports `REBOOTING` and exits so the launcher starts it.

Every stage is written to disk before it is acted on and before it is reported. A broken link therefore never changes what the device does: it carries on installing, and after the reboot it sends the stages the platform missed, which the protocol allows because a device may skip a stage whose report was lost. A restart at any point resumes from the bytes held. The device installs only while it is the confirmed slot with nothing pending.

## Rollback {#rollback}

After the reboot the new process confirms itself, which moves the slot from pending to confirmed, once the platform has been told what runs. If an update is in flight, it confirms only when what runs is that update's version and file. It then reports `RUNNING` with the compiled version and the digest of its own file.

If the new image does not make it, the launcher starts the old one. The old image reports `FAILED` with the code `ROLLED_BACK` and a detail such as `booted 1.0.1, expected 1.0.2`. It never reports `RUNNING` for an update that did not take, so the platform's attempt ends as a failure and not as a success it could not prove.

## Refusing downgrades and wrong components {#refusing-downgrades}

The platform signs a digest. It does not sign the version, the component or the product, so it cannot stop a correctly signed older image, or an image built for a different product, from being a perfectly valid signed image. Anyone who may publish artifacts can declare an old image again under a new version, and its old signature still verifies. See [What a signature does not cover](./signing-updates.md#what-a-signature-does-not-cover).

The device is therefore the only place that can enforce these, and the reference device does, twice. Before the download it compares the assignment's component and version with its own. After the download it runs the image once and checks that the image names this device's product and the version the assignment named, so an image that lies about itself is refused. There is no way to switch the downgrade check off except an explicit lab flag, `--allow-downgrade`.

## Pin the download origin {#grant-origin-pin}

The download link is chosen by the platform and delivered over the broker. The device fetches only from the origin it was configured with, matching scheme, host and port, with no user information and an `/updates/download/` path, and any redirect has to stay on that origin. A misconfigured or compromised component that can publish to the device's topic therefore cannot make the device fetch from, or send its capability to, another host.

The link carries a capability in its query string, which is a bearer secret. The device never logs the link or its query, and strips links out of transport errors. There is no switch that turns certificate verification off. A plain `http` origin is refused unless `--allow-insecure-download` is given, and so is a broker URL without TLS unless `--allow-insecure-broker` is given; both exist for a lab on a compact install without TLS.

## Run it {#run-it}

From `examples/ota-device`, build the two programs and put the first image in slot `a`:

```bash
ROOT=$PWD/state
go build -ldflags "-X devicechain.io/examples/ota-device/internal/buildinfo.Version=1.0.0" -o bin/ota-device ./cmd/ota-device
go build -o bin/ota-boot ./cmd/ota-boot
mkdir -p "$ROOT/slots/a" && cp bin/ota-device "$ROOT/slots/a/"
```

Put the signer's public key in a trust file, then start the device under the launcher:

```bash
openssl pkey -in signer.pem -pubout -out trust.pem

export OTA_CREDENTIAL_SECRET='<device credential secret>'
bin/ota-boot --root "$ROOT" -- \
  --root "$ROOT" \
  --broker ssl://mqtt.example.com:8883 --ca instance-ca.crt \
  --instance devicechain --tenant acme --device pump-7 --credential-id <credentialId> \
  --origin https://dc.example.com --trust trust.pem
```

`--credential-id` is the credential id, not the MQTT username: the program sends `{tenant}:{credentialId}` as the username and `{instance}:{tenant}:{device}` as the client id, as [Connecting a Device](./connecting-a-device.md) describes. `--tls-server-name` verifies the broker certificate against a name other than the broker host, for a local cluster whose certificate does not cover `localhost`.

Build a second image with a higher version, declare it as an artifact with component `firmware`, sign it, and assign it to the device. You can follow the attempt on the device's Updates tab as it moves from `RECEIVED` through `DOWNLOADING`, `VERIFIED` and `REBOOTING` to `UPDATED`. Assign an image signed by a key the device does not hold and the attempt ends `FAILED` with `UNKNOWN_SIGNER`, after the download. Assign one whose version is not newer and the device refuses it before downloading anything.

## What a real device must add {#what-a-real-device-must-add}

The example stays honest about what it is not:

- **Power-fail-safe flash.** A real device writes the inactive bank, verifies it, and flips a bank-select bit that the hardware makes atomic. A rename and an fsync stand in for that here.
- **A hardware root of trust.** It anchors the trust set and the launcher. The launcher here is a program, not a ROM.
- **Rollback protection.** An anti-rollback counter held in hardware, so a downgrade cannot be forced by wiping the device's state.
- **A first execution in a sandbox.** The identity check limits the working directory, environment and time, not the network or the filesystem. A real device runs it in a minimal privilege domain, or verifies a manifest instead of running the image at all.
- **A staging area nobody else can write.** The image is hashed, verified, run once and then renamed; someone who can write the inactive slot in between defeats the checks.
- **Secure storage for the credential.**
- **A way to learn a signer was revoked.** The platform refuses to assign an artifact signed only by a revoked signer, but a device acts on the trust set it holds.

It also does not do delta updates or encrypted images, and it does not handle configuration, LwM2M or Sparkplug. It connects as an ordinary MQTT device. The order of checks above is what carries over to a real device unchanged.

## Related

- **[Over-the-Air Updates](../concepts/over-the-air-updates.md)**: assignments, attempts and rollouts, from the platform's side.
- **[Connecting a Device](./connecting-a-device.md#over-the-air-updates)**: every message the device sends and receives.
- **[Signing Updates](./signing-updates.md)**: creating keys, signing, and rotating them.
