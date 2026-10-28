---
title: Certificate Identity
---

# Certificate Identity {#device-certificates}

A device can prove who it is with a **client certificate** instead of a token or a password. It presents the certificate in the TLS handshake of its MQTT connection, and the platform checks that the certificate was issued by a certificate authority (CA) that the tenant trusts. There is no shared secret to flash into firmware, and nothing the device holds can be replayed by someone who copies a message off the wire: the private key never leaves the device.

This page explains how the platform decides to trust a certificate, and where that trust stops. To set it up, follow [Connecting with a certificate](../guides/device-x509.md); the console side is in [Provisioning and trust anchors](../user-guide/provisioning-and-trust-anchors.md). To run the broker and watch it, see [Certificate authentication](../deployment/device-certificates.md).

:::note Status
Available over **MQTT on the platform broker**. HTTP ingest and LwM2M do not accept client certificates, the C# SDK cannot yet present one, and renewal of a certificate on its own is not available yet. [What is not supported](#not-supported) lists everything left out.
:::

## The trust anchor is the identity boundary {#trust-anchor}

A **trust anchor** is a CA certificate that a tenant has added to say "devices holding a certificate issued by this CA are mine". It is public material and is kept as is. A tenant can hold 32 of them.

- A root CA or an intermediate CA can be an anchor. Anchor at the narrowest CA that issues only your devices. If a manufacturer runs one PKI for many customers, trust the sub-CA that issues your fleet, not the root.
- Every connect is checked against the anchor again. Nothing about a certificate is cached as trusted.
- An anchor can be disabled. That withdraws the CA at once, and the live sessions of every device that relies on it are ended.

Because the anchor decides who is a device of the tenant, **name constraints are not honoured**, and a CA that carries them is refused when you add it as an anchor, or when it appears in a presented chain. The identity DeviceChain uses is the certificate's common name, which name constraints do not cover. A constraint that was meant to narrow what a CA may issue would be silently ignored, so the platform refuses the certificate rather than let you believe it applies.

## What a certificate must be {#certificate-rules}

The platform accepts a device certificate only when all of these hold:

- It names **client authentication** in its extended key usage. A certificate with no extended key usage, or one that allows any use, is refused: it never claimed to be a client certificate.
- It is not itself a CA.
- Neither it nor any certificate above it is signed with SHA-1.
- Every key in the chain is RSA of 2,048 to 8,192 bits, ECDSA on P-256 or P-384, or Ed25519.
- Its chain, from the leaf through the intermediates the device sends, builds to the trust anchor. The device sends at most five certificates, 16 KiB in total.
- It is valid now. A certificate issued up to five minutes in the future is accepted, to forgive a device whose clock is a little behind the CA's; its expiry is never extended.

## Two ways a certificate becomes a device {#two-ways}

| Way | You do | The device's identity |
| --- | --- | --- |
| **Pinned credential** | Create the device, then add an `X509_CERTIFICATE` credential holding its certificate and naming the anchor. | The device you created. Its token is whatever you chose. |
| **Registration on first connect** | Create a **certificate profile**: a provisioning profile that names an anchor. Devices whose certificates chain to the anchor register themselves the first time they connect. | A device created for the certificate. Its external id is the certificate's common name, and its token is derived from it. |

A **pinned credential** is identified by the SHA-256 of the certificate, which the server computes. The credential records the anchor the certificate must chain to, and the earliest expiry along the whole chain. A device can hold up to three live certificate credentials at once, so a replacement can be added before the old one expires.

A **certificate profile** has no provision secret to guard or rotate, because the certificate is the proof. It uses the same controls as [self-registration](../guides/device-self-registration.md): the strategy, a cap on devices, an expiry, and revoking what it registered. A tenant can hold eight certificate profiles.

### The derived token and the client id {#derived-token}

MQTT sessions are keyed by client id, and the platform requires the client id to be `{instanceId}:{tenant}:{deviceToken}` so that one device cannot take over another's session. A device that registers on first connect has no token yet, so the platform needs a token it can compute from the certificate alone, before it writes anything. The token of a device registered from a certificate is:

```text
pv-{slug}-{hash}
```

- `slug` is the common name with every character other than letters, digits, `-` and `_` replaced by `-`, and leading and trailing `-` and `_` removed.
- `hash` is the first 12 hexadecimal characters of the SHA-256 of the common name.
- The whole token is at most 128 characters; a long `slug` is cut to fit.

A device's firmware can therefore compute its own client id from its own certificate. The [guide](../guides/device-x509.md#client-id) has a worked example. A connect whose client id does not match is refused before anything is created, so a refused connect never leaves a device behind.

A device that you create ahead of time and earmark for a certificate profile must carry this token. The console and the API fill it in when you leave the token empty, and refuse a different one.

### Which profile registers a device {#narrowest-anchor}

A certificate can chain to the anchors of more than one profile. The profile whose anchor is **closest to the certificate** wins, so a profile anchored at an intermediate takes precedence over one anchored at its root. If two usable profiles tie at the same distance, the connect is refused rather than guessed at, and the alert `DeviceX509JitMisconfigured` fires. Give each profile its own anchor, or anchor one at a narrower CA.

Registration on first connect **creates** devices under the strategy `ALLOW_NEW`. Under `ALLOW_NEW`, any certificate the anchor's CA issues, with a common name no device has used, registers a new device, up to the profile's `maxDevices`. If that is too open, anchor at the narrowest intermediate, set a cap, and disable the anchor on a compromise. Adopting a device you created and earmarked in advance, and accepting a renewed certificate for a device that already holds one, are not available yet.

## How long a session lasts {#session-lifetime}

The session is granted until the earliest `notAfter` among the device certificate, every intermediate and the trust anchor (and the credential's own expiry, if it has one). The broker closes the session at that moment. A device that renews its certificate reconnects with the new one.

## Revocation {#revocation}

| You need to | Do this | Takes effect |
| --- | --- | --- |
| Stop one device | Disable or delete its `X509_CERTIFICATE` credential, or disable the device | Immediately; the open session is ended |
| Stop a whole CA | Disable the trust anchor | Immediately; every relying device's session is ended |
| Stop a device that registered itself | `revokeProvisionedDevices` or `resetDeviceProvisioning`, as for any [self-registered device](../guides/device-self-registration.md#compromise) | Immediately |
| Let a certificate lapse | Issue short-lived certificates | At the certificate's expiry, exactly |
| Stop a stolen certificate that never connected | Pre-create devices and pin their certificates, or disable the anchor | Not before one of those happens |

**There is no CRL and no OCSP.** The platform does not fetch or upload revocation lists and does not ask a responder whether a certificate is still good. A certificate you want to stop is stopped by disabling its credential or its anchor. Plan around that: issue short-lived certificates from your CA and rely on expiry, with credential and anchor disabling as the immediate tools.

## What is not supported {#not-supported}

- **Name constraints.** A name-constrained CA is refused, as above.
- **Certificate revocation lists and OCSP.** See [Revocation](#revocation).
- **Renewal on its own, and adopting a device after its provisioning is reset.** A device that holds a certificate and then presents a different one is refused (`ALREADY_PROVISIONED`) when it connects for registration. Until renewal ships, add the new certificate as a pinned credential (a device can hold three) before the old one expires.
- **The C# SDK.** It cannot present a client certificate yet.
- **HTTP ingest and LwM2M.** Neither authenticates with a client certificate. HTTP devices use a token or basic credential, and LwM2M devices use pre-shared keys. A certificate presented on MQTT with any username other than `{tenant}:@x509` is ignored, and the connect is judged on its token or password as before.
- **Other brokers.** Only the platform's own broker authenticates by certificate. A device connecting through an operator-owned external broker is not covered.
- **Certificates issued by the platform.** You bring your own CA and your own certificates.
