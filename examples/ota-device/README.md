# ota-device: a reference updatable device

A small Go program that does what a real connected product has to do to take an over-the-air
update from DeviceChain: follow the device-side update protocol, refuse what it should refuse,
replace its own executable, and prove to the platform which version is really running.

It exists for two reasons. It shows, in code you can read in an afternoon, the order of checks a
device must make. And it is the "real device" the platform's update path is tested against: it is
built **only from the published protocol** (the "Over-the-air updates" section of the
[device guide](../../docs/docs/guides/connecting-a-device.md#over-the-air-updates)), so a green run
says something about that protocol and not about the platform's internals.

> **This is an example, not a product.** It is built and tested in CI but never released: there are
> no published binaries and no compatibility promise. A file rename standing in for a bootloader is
> a teaching device, not a thing to ship. See [What a real device must add](#what-a-real-device-must-add).

## The two programs

| Binary | Stands in for | Job |
| --- | --- | --- |
| `cmd/ota-boot` | the bootloader | Picks the slot to run. Gives a freshly installed slot exactly **one** try; if it does not confirm itself, runs the last confirmed slot again and leaves a note saying so. Kills a new slot that hangs without confirming. Never updated over the air. |
| `cmd/ota-device` | the firmware | Connects to the instance over MQTT as one device, follows the update protocol, installs a verified image into the **inactive** slot, and exits with status 75 ("reboot") so the launcher starts it. |

```
<root>/
  boot.json            {"confirmed":"a","pending":"b","tries":1}   written atomically
  boot.json.good       the last state with nothing pending: what a corrupt boot.json falls back to
  rollback.json        the launcher's note that a pending slot did not make it
  attempt.json         the one update in flight: ids, expected digest/version, last stage, seq
  slots/a/ota-device   the image in slot a
  slots/b/ota-device   the image in slot b
```

`ota-device describe` prints `{"product":..., "version":...}` and exits. The version and product are
set with the linker (`-ldflags -X`), never read from configuration and never copied from an
assignment: **the version the device reports as running is the one compiled into the binary that is
running**, and its `digest` is the SHA-256 of that very file.

## The order of checks (this is the point)

When an assignment arrives:

1. **Policy, before anything is downloaded.** The component must be this device's; an unsigned
   assignment is refused unless the device was told otherwise; the version must be valid semantic
   versioning **and strictly newer** than the one running (an equal version is a replay). A version
   that is not semver fails closed. There is no way to turn this off except `--allow-downgrade`.
2. **Disk space** for the remainder of the download.
3. **Download** with `Range: bytes=<held>-` into `slots/<inactive>/ota-device.part`. A response whose
   `Content-Range` starts anywhere but the requested offset is refused; one byte past the declared
   size is a failure and the partial file is deleted. An expired or refused grant asks for a new one,
   a bounded number of times.
4. **Digest.** SHA-256 of the bytes held equals `artifact.digest`. *Nothing has been executed.*
5. **Signature.** The detached signature over the raw 32-byte digest verifies against a key in the
   device's **local** trust set. The wire's `keyId` only selects a key from that set; a key is never
   taken from a message. *Nothing has been executed.*
6. **Identity.** Only now is the image run, once, as `describe`, with a scratch working directory, an
   empty environment and a deadline. It must say it is this device's product and the version the
   assignment named. This is the device-side binding the platform's signature does not give: the
   signature covers the image bytes, **not** the version, the component or the product, so a
   correctly signed older or foreign image is a perfectly valid replay unless the device refuses it.
7. **Install** into the inactive slot, stage it as pending, report `REBOOTING`, exit 75.

On the next boot the new process confirms itself once it is connected (moving `pending` to
`confirmed`) and reports `RUNNING` with the compiled version and the digest of its own file. If the
launcher rolled back instead, the old image reports `FAILED` with code `ROLLED_BACK` and a detail
like `booted 1.0.1, expected 1.0.2`; it never reports `RUNNING` for an update that did not take.

Two more rules protect the device from the platform side of the wire:

- **Grant-origin pin.** The download URL is chosen by the platform and delivered over a broker. The
  device fetches only from the origin it was configured with (scheme, host and port; no userinfo; an
  `/updates/download/` path), and a redirect must pass the same pin. A compromised or misconfigured
  component that can publish on `device-desired` therefore cannot make the device fetch from, or
  send its capability to, another host.
- **TLS always.** There is no switch that turns certificate verification off. A plain-`http` origin
  is refused unless `--allow-insecure-download` is given (a lab on a compact install without TLS).
  The download capability is a bearer secret in the URL; the device never logs the URL or its query
  and strips URLs out of transport errors.

## Running it

Build the two binaries and put the first image in slot `a`:

```bash
ROOT=$PWD/state
go build -ldflags "-X devicechain.io/examples/ota-device/internal/build.Version=1.0.0" -o bin/ota-device ./cmd/ota-device
go build -o bin/ota-boot ./cmd/ota-boot
mkdir -p "$ROOT/slots/a" && cp bin/ota-device "$ROOT/slots/a/"
```

The trust set is a file of PEM public keys (`openssl pkey -in signer.pem -pubout -out trust.pem`;
several keys can be concatenated). Then:

```bash
export OTA_CREDENTIAL_SECRET='<device credential secret>'
bin/ota-boot --root "$ROOT" -- \
  --root "$ROOT" \
  --broker ssl://mqtt.example.com:8883 --ca instance-ca.crt \
  --instance devicechain --tenant acme --device pump-7 --credential-id <credentialId> \
  --origin https://dc.example.com --trust trust.pem
```

`--credential-id` is the credential id, not the MQTT username: the program sends
`{tenant}:{credentialId}` as the username and `{instance}:{tenant}:{device}` as the client id, as the
device guide describes. `--tls-server-name` verifies the broker certificate against a name other than
the broker host (a local cluster whose certificate does not cover `localhost`). To sign an image the
way the guide shows, use `openssl dgst -sha256 -sign key.pem -binary image | base64` (ECDSA P-256) or
sign the raw digest bytes (Ed25519).

`--allow-unsigned`, `--allow-downgrade` and `--allow-insecure-download` exist for labs and are off by
default.

## What is tested

```bash
cd examples/ota-device
go vet ./... && go test ./... -count=1
```

`-count=1` matters: the conformance tests read the platform's golden fixtures
(`backend/core/ota/testdata`) from outside this module, and Go's test cache does not track files
outside the module.

| Guard | Test | Shown red by |
| --- | --- | --- |
| Device imports nothing from the platform | `independence_test.go` | a build-tagged fixture that imports a platform package must make the guard fail |
| Tampered bytes refused, never run | `TestTamperedBytesAreRefusedAndNeverExecuted` | removing the digest check |
| Bad / unknown signature refused, never run | `TestSignatureFailuresAreRefusedAndNeverExecuted` | skipping the signature check; accepting an unknown signer |
| Nothing runs before digest and signature | `TestNothingRunsBeforeIntegrityAndAuthenticity` | moving the identity preflight ahead of them |
| Wrong component refused before download | `TestWrongComponentIsRefusedBeforeAnyDownload` | removing the component check |
| Downgrade and replay refused even when correctly signed | `TestDowngradeAndReplayAreRefused...`, `TestPolicy` | removing the downgrade check; allowing an equal version |
| Image that lies about itself refused | `TestAnImageThatLiesAboutItself...` | removing the product / version identity check |
| Failed boot rolls back and is never `RUNNING` | `TestFailedBootRollsBack...`, `TestSupervisor...`, `TestProcessRunnerRealImages` | making the launcher retry a pending slot forever |
| `RUNNING` is evidence of the file that runs | `TestRightVersionWrongFileIsNotRunning` | dropping the digest comparison |
| Grant-origin pin | `TestGrantOriginPin`, `TestGrantForAnotherHostIsNeverFetched` | removing the host, scheme or userinfo rule |
| Resume, wrong range, size cap | `internal/download` tests | removing each check |
| Capability never in an error or report | `TestErrorsNeverCarryTheCapability`, `TestCapabilityNeverAppearsInAReport` | un-redacting transport errors |
| Wire grammar equals the platform's | `internal/wire/conformance_test.go` | duplicate or case-folded keys accepted |

The tests drive real processes where it matters: they build the launcher's child images with the Go
toolchain and run them, so the A/B swap and the rollback are exercised on Linux and Windows alike.

## Limits (deliberate)

- **The launcher is not updatable over the air**, like an MCU's ROM bootloader.
- **A file rename is not power-safe flash.** A real device writes the inactive bank, verifies it, and
  flips a bank-select bit that the hardware makes atomic. Here a rename and an fsync stand in.
- **The preflight is not a sandbox.** It limits the working directory, environment and time, not the
  network or the filesystem. A real device runs the first execution of a new image in a minimal
  privilege domain, or verifies a manifest instead of running the image at all.
- **Check-then-use.** The image is hashed, verified, run once and then renamed; a local attacker who
  can write the inactive slot in between defeats it. A real device keeps the staging area unwritable.
- **The device does not learn that a signer was revoked.** The platform refuses assignments signed
  only by revoked signers; a device acts on the trust set it holds.
- **The signature binds only the digest.** Version, component and product are not signed. This
  device enforces them itself (above); a signed manifest that binds them is a future platform change.
- No delta updates, no encrypted images, no configuration or LwM2M / Sparkplug transport. This device
  connects as an ordinary MQTT device.

## What a real device must add

Power-fail-safe flash with an atomic bank switch; a hardware root of trust that anchors the trust
set and the launcher; rollback protection (an anti-rollback counter the hardware holds, so a
downgrade cannot be forced by wiping state); secure storage for the credential; and a manifest the
bootloader verifies before it jumps. The order of checks above is what carries over unchanged.
