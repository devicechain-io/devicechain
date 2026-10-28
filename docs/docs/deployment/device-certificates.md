---
title: Certificate Authentication
---

# Certificate Authentication {#certificate-authentication}

This page is the operator's side of [device client certificates](../concepts/device-certificates.md): the broker build that makes them possible, the settings that bound registration on first connect, what to watch, and what an upgrade changes on an instance that already runs. To connect a device, see [Connecting with a certificate](../guides/device-x509.md).

## The broker build {#broker}

The NATS server will not request a client certificate on its MQTT listener in the way DeviceChain needs: its configuration file can turn certificates off, or require one from every client. The platform broker is therefore DeviceChain's own `nats-server` build, `dc-nats-server`: upstream `v2.15.1` unmodified, plus one setting, applied when it starts and again on every configuration reload. The MQTT listener **requests, but does not require,** a client certificate.

- **Token and password devices are unaffected.** A client that sends no certificate connects as before. A certificate sent with a token or password username is ignored.
- **The authentication callout does the checking.** The broker hands the certificates the client presented to the callout in device-management, which verifies the chain. The broker itself decides nothing about trust.
- **Image.** `ghcr.io/devicechain-io/dc-nats-server`, signed and verifiable like the other images ([Verifying releases](./verifying-releases.md)). `dcctl` pins it by the digest the release pushed, so a release that did not change the broker does not restart it. Add it to the images you mirror.
- **The broker runs as root,** as the upstream image does, so that it can open a JetStream store that an upstream-built server wrote.
- **One supported broker image.** The upstream image is a rollback path only.

### Watching the broker {#watching}

The server checks, by handshaking with its own MQTT port at start, after every reload and every minute, that the listener is still requesting certificates. It exports the result on port `7778` (a `dc-metrics` endpoint on the chart's PodMonitor):

| Metric | Meaning |
| --- | --- |
| `dc_broker_mqtt_client_auth_request` | `1` while the MQTT listener requests client certificates, `0` when it does not |
| `dc_broker_mqtt_tls` | `1` while the MQTT listener is serving TLS |
| `dc_broker_info{library_version,build}` | which binary is running |

`BrokerMqttClientAuthLost` fires when a TLS listener has stopped requesting certificates, or when a broker pod does not export `dc_broker_info`, which is what the upstream image looks like. It is in the [alert runbook](./observability.md). Devices that authenticate with a certificate are refused while it fires (`no_cert`); token and password devices are not.

### Rolling back to the upstream broker {#rollback}

Set the infrastructure variables `server_image_repository = "nats"` and `server_image_tag = "2.15.1-alpine"`. Every certificate device is then refused, because the upstream broker never asks for a certificate, and `BrokerMqttClientAuthLost` says why. Nothing else is affected.

## Settings {#settings}

Registration on first connect has two fixed settings, read when device-management starts, under the area's `config`:

```yaml
functionalAreas:
  device-management:
    config:
      callout:
        jitMaxInFlight: 16       # default 16, 1 to 128
        jitSourceTrusted: false  # default false
```

| Key | Meaning |
| --- | --- |
| `callout.jitMaxInFlight` | How many registrations on first connect one replica works on at once. The next is refused (`jit_busy`) and the device backs off and retries. These places come out of the callout's own bound, so a wave of first connects never takes the places that reconnecting devices need, and a pinned certificate, a token or a password never waits on them. |
| `callout.jitSourceTrusted` | Adds a limit per client address, sized by `provisioning.sourceBurst` and `provisioning.sourceEvery`. Turn it on **only** when the MQTT Service preserves client addresses (`externalTrafficPolicy: Local`, or a kind NodePort). Behind source NAT every device shares one address and one allowance, and one device's retries would refuse a fleet. With it off, sustained refusals are alerted on rather than throttled by address. |

The per-tenant limit is not a setting here: it is the tenant's provisioning ceiling, the same one [self-registration](../guides/device-self-registration.md) charges, managed with the tenant's other governance limits. Registrations against one profile take turns on that profile, roughly 100 to 200 a second per profile across all replicas, so a first-connect wave of 10,000 devices registers over a minute or two of device retries.

An unknown key under `callout`, or a value outside its range, stops the service at start.

## Limits {#limits}

| Limit | Value |
| --- | --- |
| Trust anchors per tenant | 32 |
| Certificate profiles per tenant | 8 |
| Live certificate credentials per device | 3 |
| Certificates a device presents | 5, at most 16 KiB |
| Trust anchor file | one CA certificate |

## What to watch {#monitoring}

The counter `callout_x509_total{result}` counts every certificate connect by outcome, and every result exists at zero from start. The results include `ok`, `no_cert`, `chain_invalid`, `anchor_disabled`, `revoked`, `client_id_mismatch`, `unknown_pin`, `x509_cert_ignored`, `jit_created`, `jit_replayed`, `jit_rate_limited`, `jit_busy`, `jit_ambiguous` and one `jit_<code>` for each registration refusal. `callout_jit_in_flight` is the registrations in progress now, and `device_trust_anchors_expiring{window="30d"}` is one instance-wide count of enabled anchors that expire within 30 days.

The [alerts](./observability.md#certificate-alerts) are `DeviceX509ChainFailuresSustained`, `DeviceX509JitRefusedSustained`, `DeviceX509JitMisconfigured`, `DeviceTrustAnchorsExpiringSoon` and, for the broker, `BrokerMqttClientAuthLost`.

Anchor and credential changes are journaled in the audit log. Connect outcomes are counted, not journaled.

Revoking a device or disabling an anchor ends the open sessions of the devices concerned. If the platform cannot confirm that a session ended, the change is kept and the platform keeps retrying until the session is gone; repeating the change asks again.

## Upgrading an instance that already runs {#cutover}

For an instance that is already running, certificate authentication is switched on by adding a trust anchor, not by upgrading.

- **Token and password clients are unaffected.** The broker's MQTT listener now asks each client for a certificate; a client that has none answers with an empty one, and connects as before. The same holds for a client that sends a certificate with a token or password: it is ignored. Before you upgrade a fleet, try one device of each TLS stack it runs.
- **The broker image changes if it has not already.** An instance coming from a release that still ran the upstream broker rolls its broker once, and a broker roll opens a short window in which acknowledged MQTT messages can be lost unless fail-closed MQTT is on; see [the MQTT transport](../reference/transport-matrix.md#mqtt--the-platform-broker). An instance already on `dc-nats-server` does not roll when the digest has not changed.
- **Nothing is registered or trusted automatically.** No trust anchors exist after the upgrade, so no certificate authenticates anything until you add one.
- **A certificate credential from before certificates were supported does not authenticate.** The type was refused in earlier releases, and a row that exists has no trust anchor. Create it again as a pinned credential naming an anchor.
- **The schema gains** a trust-anchor table and columns for the anchor on credentials and provisioning profiles. The upgrade applies them; no data is rewritten.

Not available yet, and not part of the upgrade: renewal of a registered device's certificate, adopting a device after its provisioning is reset, a client certificate in the C# SDK, and certificate authentication over HTTP or LwM2M. The [concept page](../concepts/device-certificates.md#not-supported) has the full list, including that name-constrained CAs are refused and that there is no CRL or OCSP.
