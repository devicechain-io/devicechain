<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# x509-device: a device that signs in with a certificate

A device that has **no password and no token**. It proves who it is with a TLS client certificate
that a certificate authority (CA) you control signed, then does what any device does: publishes
telemetry, receives a command and reports the outcome.

```
 your machine                                      DeviceChain (kind cluster)
┌────────────┐  1 mint CA + device cert            ┌─────────────────────────────┐
│ pki.sh     │                                     │ trust anchor   <- ca.crt    │
│ provision  │─ 2 anchor the CA, pin the cert ────▶│ device thermostat-1         │
└────────────┘                                     │   credential   <- device.crt│
                                                   │                             │
┌──────────────────────┐  mqtts, user x509-demo:@x509  ┌──────────────────────┐  │
│ device (Mosquitto)   │── client certificate ────────▶│ broker + auth callout│  │
│  publishes telemetry │◀─ reboot ─────────────────────│                      │  │
│  answers the command │                               └──────────────────────┘  │
└──────────────────────┘                               └─────────────────────────┘
```

The device is [`device/device.sh`](device/device.sh): about forty lines of shell around
`mosquitto_pub` and `mosquitto_sub`, in the stock Mosquitto image. Everything specific to
certificates is in its connection settings.

## Requirements

`docker` (with `compose`), `kind`, `kubectl`, `jq`, `curl`, `openssl` 3.x and `dcctl`. A DeviceChain
instance on a local kind cluster with the default profile: no flag turns certificate sign-in on, it
comes with the default.

Run every script from this directory. They talk to `http://localhost` (the ingress of a default
`dcctl install local`). Set `DC_URL` if yours is elsewhere, `KUBECONFIG` or `KUBE_CONTEXT` to pick
the cluster, `INSTANCE` for the instance name (default `x509`) and `MQTT_HOST` for the kind node's
container name (default `devicechain-control-plane`, which is `<cluster>-control-plane`). Export
the same `MQTT_HOST` for `docker compose` and the scripts.

## Run it

```bash
# 1. DeviceChain.
dcctl install local --compact
dcctl bootstrap local x509 --host localhost --no-tls --no-escrow

# 2. A CA and a device certificate, then the platform side: tenant, device, trust anchor, pin.
./pki.sh
./provision.sh

# 3. The device.
docker compose up -d
```

### What step 2 did

- `pki.sh` made a CA (`pki/ca.crt`) and a device certificate (`pki/device.crt`, common name
  `SN-000123`) with `openssl`: ECDSA P-256, client authentication only, not a CA, valid 90 days.
  The 90 days are deliberate: the platform has no revocation list, so a short life is how an
  unwanted certificate goes away by itself. The private keys never leave `pki/`.
- `provision.sh` created the tenant `x509-demo`, a profile `thermostat` that declares the command
  `reboot`, a device type, the **trust anchor** (`createDeviceTrustAnchor`: the tenant now believes
  certificates this CA signs), the device `thermostat-1` whose external id is the certificate's
  common name, and the **pinned credential** (`createDeviceCredential` with type `X509_CERTIFICATE`
  and the certificate). It also saved the broker's own CA as `pki/instance-ca.crt`, so the device
  can verify the broker.

### How the device connects

| Setting | Value |
| --- | --- |
| Client certificate and key | `pki/device.crt`, `pki/device.key` |
| Username | `x509-demo:@x509` (`{tenant}:@x509`; the tenant comes from here, never from the certificate) |
| Password | none. A password sent with this username is refused |
| Client id | `{instanceId}:{tenant}:{deviceToken}`, here `x509:x509-demo:thermostat-1` |
| Server verification | `--cafile pki/instance-ca.crt` |

The device keeps one connection open for commands, which is also what makes it show as online, and
opens another for each publish and answer. The suffixes after the device token (`:pub`, `:resp`)
keep those connections from fighting over a client id.

## See it work

```bash
./show-state.sh
./send-command.sh reboot
```

```
thermostat-1  online=true  connected=2026-10-30T12:06:59.089148Z
    temperature = 23
    credential X509_CERTIFICATE 382ff57d6e9ce19c... enabled=true expires=2027-01-28T12:04:55Z

command x509-1793362044-26861 to thermostat-1: reboot
08:07:25 SUCCESSFUL
queued:   2026-10-30T12:07:24.537594Z
sent:     2026-10-30T12:07:24.550035Z
answered: 2026-10-30T12:07:24.58493Z
outcome:  SUCCEEDED
evidence: "reboot done"
```

`docker compose logs` shows the same from the device's side. A command sent while the device is
offline stays `HELD` and is delivered when the device next connects.

### A certificate from the wrong CA is refused

```bash
./rogue.sh
```

It mints a second CA the tenant has **not** anchored, issues a certificate with the real device's
common name, and connects as the real device. The broker answers `Connection Refused: not
authorised`, and the rogue value never becomes `thermostat-1`'s temperature. The platform records
the reason as the `result` label of its `callout_x509_total` counter (here `client_id_mismatch`,
because the real device's token is not the one a certificate profile would derive for that name;
a bad chain under a correct client id is `chain_invalid`). That counter is where an operator looks:
the device only ever sees "not authorised".

### Register on first connect (no device created beforehand)

```bash
./jit.sh
```

```
created certificate profile example-certs
connecting as pv-SN-000901-79a7a211567f
registered: pv-SN-000901-79a7a211567f  external id SN-000901
```

A **certificate profile** on the trust anchor lets a certificate this CA issued create its own
device the first time it connects. The device derives its token from its common name
(`pv-{slug}-{first 12 hex of sha256(CN)}`) and must use it in the client id. The script mints a
certificate for a device nobody registered and connects once. Under `ALLOW_NEW` any certificate the
CA issues registers a device, so the profile's `maxDevices` cap and a narrow anchor are what bound a
mistake. Running it again mints a *different* certificate for the same name, which is refused: a
registered device does not take a new certificate by presenting one. Use another `JIT_CN=...`.

## Results (kind, DeviceChain v0.27.0, Mosquitto 2.0.22)

| Check | Result |
| --- | --- |
| Pinned certificate connects, no password | accepted; `callout_x509_total{result="ok"}` rises on every connect |
| Telemetry published over the certificate session | stored; `show-state.sh` reads the last value |
| Device shows online while its command connection is open | `online=true` |
| `reboot` sent while online | `SUCCESSFUL`, outcome `SUCCEEDED`, in under 100 ms |
| `reboot` sent while the device had no command connection | `HELD`, then delivered on reconnect |
| Certificate from an unanchored CA, real device's client id | refused (`CONNACK 5`), counted `client_id_mismatch`, value not stored |
| Never-registered certificate under a certificate profile | device `pv-SN-000901-...` created (`jit_created`) |

## Things worth knowing

1. **`--insecure` in `device.sh` is host-name verification only.** The broker's certificate is issued
   for the instance's names, not for the kind node's address. The chain is still checked against the
   instance CA. Connect to a name the certificate covers and remove it.
2. **Presence follows the main client id.** Only the connection whose client id is exactly
   `{instanceId}:{tenant}:{deviceToken}` counts as the device being online. A device that only
   subscribes on a suffixed connection stays offline and its commands stay `HELD`.
3. **The command answer must carry the command's own `token` and the delivery's `dispatchNonce`.**
   Putting the device token in `commandToken` is the common mistake.
4. **The session ends at the certificate's earliest expiry.** Renewal of a registered device by
   presenting a new certificate is not available yet; pin the new certificate as a second credential
   before the old one expires.

## For a real fleet

- Use your own PKI, and anchor an issuing intermediate rather than the root.
- Send the leaf and at most one intermediate.
- Keep `maxDevices` on a certificate profile as low as the fleet.
- See [Connecting with a certificate](https://docs.devicechain.io/guides/device-x509) for the full
  reference, including every refusal reason.

## Clean up

```bash
docker compose down
rm -rf pki
dcctl destroy local x509
```

## Third-party software

| Component | Licence | How it is used |
| --- | --- | --- |
| [Eclipse Mosquitto](https://mosquitto.org/) 2.0.22 | EPL-2.0 / EDL-1.0 | Run as the published image, pinned by digest; `mosquitto_pub` and `mosquitto_sub` are the device's MQTT client. |
| [OpenSSL](https://www.openssl.org/) 3.x | Apache-2.0 | Run on the host to mint the throwaway CA and certificates. |
| [jq](https://jqlang.github.io/jq/), [curl](https://curl.se/) | MIT / curl | Run on the host by the scripts. |

`device.sh` and the scripts are licensed with the rest of this repository (Apache-2.0).
