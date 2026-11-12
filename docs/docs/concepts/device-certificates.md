---
title: Certificate Identity
---

# Certificate Identity {#device-certificates}

A device can prove who it is with a **client certificate** instead of a token or a password. It presents the certificate in the TLS handshake of its MQTT connection, and the platform checks that the certificate was issued by a certificate authority (CA) that the tenant trusts. There is no shared secret to flash into firmware, and nothing the device holds can be replayed by someone who copies a message off the wire: the private key never leaves the device.

This page explains how the platform decides to trust a certificate, and where that trust stops. To set it up, follow [Connecting with a certificate](../guides/device-x509.md); the console side is in [Provisioning and trust anchors](../user-guide/provisioning-and-trust-anchors.md). To run the broker and watch it, see [Certificate authentication](../deployment/device-certificates.md).

:::note Status
Available over **MQTT on the platform broker**. HTTP ingest and LwM2M do not accept client certificates, the C# SDK presents one only for RSA and ECDSA keys, and renewal of a certificate on its own is not available yet. [What is not supported](#not-supported) lists everything left out.
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

A **pinned credential** is identified by the SHA-256 of the certificate, which the server computes. The credential records the anchor the certificate must chain to, and the earliest expiry along the whole chain. A device can hold up to three certificate credentials at once, so a replacement can be added before the old one expires. Disabled and expired credentials count toward the three until you delete them.

A **certificate profile** has no provision secret to guard or rotate, because the certificate is the proof. It uses the same controls as [self-registration](../guides/device-self-registration.md): the strategy, a cap on devices, an expiry, and revoking what it registered. A tenant can hold eight certificate profiles.

### The derived token and the client id {#derived-token}

MQTT sessions are keyed by client id, and the platform requires the client id to be `{instanceId}:{tenant}:{deviceToken}` so that one device cannot take over another's session. A device that registers on first connect has no token yet, so the platform needs a token it can compute from the certificate alone, before it writes anything. The token of a device registered from a certificate is:

```text
pv-{slug}-{hash}
```

- `slug` is the common name with every character other than letters, digits, `-` and `_` replaced by `-`, and leading and trailing `-` and `_` removed.
- `hash` is the first 12 hexadecimal characters of the SHA-256 of the common name.
- The whole token is at most 128 characters; a long `slug` is cut to fit, and any `-` or `_` the cut leaves at its end is removed.
- If no character of the common name survives into a `slug`, the token is `pv-{hash}`.

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
| Stop one certificate, a leaf or an intermediate, registered or not | Revoke it by fingerprint with `revokeDeviceCertificate` | From the certificate's next connect, on every replica; the open sessions it affects are ended |
| Stop the certificates a CA has revoked | Upload the CA's revocation list (CRL) to the trust anchor | From the next connect of each certificate the list names; the open sessions of the certificates it newly names are ended |
| Stop a device that registered itself | `revokeProvisionedDevices` or `resetDeviceProvisioning`, as for any [self-registered device](../guides/device-self-registration.md#compromise) | Immediately |
| Let a certificate lapse | Issue short-lived certificates | At the certificate's expiry, exactly |

**A revocation refuses the certificate when it connects, and ends the open sessions it affects.** Revoking a leaf certificate, by hand or by a CRL that names it, ends the sessions of the devices that hold it. Revoking a certificate authority is wider: when you give its certificate, or a CRL names an intermediate that a registered credential's chain was seen to use, the sessions of every device with a certificate credential under that trust anchor are ended, and the devices whose chain is still trusted reconnect at once. A manual revocation by bare fingerprint of an intermediate that no registered credential was seen to use refuses connects through it, under that anchor only, and ends no session, because nothing is known to rely on it. On an instance upgraded from an earlier release, credentials registered before the upgrade have no recorded intermediate, so such a revocation cannot tell which sessions it affects: the result says `sessionsEnded: false`, and repeating the revocation with the intermediate's certificate (`certificatePem`) ends the sessions of every device under the anchor. Sessions are ended when the revocation commits and again 10 to 40 seconds later (the second pass is due 10 seconds out and runs on the next 30-second retry cycle), which catches a device that was being admitted at that instant. If the service that ends sessions cannot confirm, the revocation still stands; the result says `sessionsEnded: false` and repeating the call asks again. See the [upgrade notes](../deployment/releases-and-upgrades.md#v120-x509-sessions).

**CRLs are uploaded, never fetched.** The platform does not follow a CRL distribution point. (Asking an OCSP responder is a separate, optional check, described below.) You upload the CA's current list to the trust anchor with `putDeviceRevocationList` (a list of up to 2 MiB over GraphQL) or with a streaming `PUT /trust-anchors/{token}/crl` on the `device-management` API, in DER or PEM. By default, both paths share a 2 MiB ceiling; the streaming upload accepts larger lists only once you raise `deviceManagement.callout.revocation.maxCrlBytes` (up to 32 MiB). The platform checks the list's signature against the anchor (or the issuer chain you send with it) and keeps one current list for each issuer. A list whose CRL number is not above every number accepted before for that issuer is refused, so an old list can never replace a newer one, even after the list was deleted. `deleteDeviceRevocationList` removes a list and the refusals it carried.

**A manual revocation is the platform's own decision.** `revokeDeviceCertificate` names a certificate by its PEM or SHA-256 fingerprint and an optional reason. It applies wherever the certificate appears on a verified path, whatever later becomes of its credential or its device, so deleting the credential does not let the certificate register again. `unrevokeDeviceCertificate` removes it. A trust anchor that holds manual revocations cannot be deleted until they are removed.

**How strict an anchor is.** An anchor has a revocation mode. `ENTRIES` (the default) refuses a certificate that a stored list or a manual revocation names and admits the rest, however old the lists are, so an anchor with no list behaves as it did before revocation existed. `REQUIRE_CURRENT` also refuses a certificate whose issuer has no current list; a list counts as current until its `nextUpdate` plus the anchor's stale grace (`revocationStaleGrace`, up to 168 hours) has passed. The platform refuses to switch an anchor to `REQUIRE_CURRENT` while it holds no current list, or while a registered certificate was issued by a CA that has none, because every such device would then be refused. Watch each list's `status` (`FRESH`, `EXPIRING`, `STALE`) and upload the next list before it goes stale.

**OCSP is an optional second check, per trust anchor, off by default.** An anchor has an OCSP mode: `OFF` (the default: no responder is asked and no request is sent), `ENFORCE_KNOWN` or `REQUIRE_CURRENT`. Nothing is asked until the instance switch `deviceManagement.callout.ocsp.enabled` (default off) is on, and `REQUIRE_CURRENT` also needs `allowRequireCurrent` (default off) and an `ocspResponderUrl` on the anchor. The API refuses to switch an anchor to a checking mode while the switch is off. If the switch is turned off later, an `ENFORCE_KNOWN` anchor runs as `OFF`, and a `REQUIRE_CURRENT` anchor refuses new connects, because that mode never admits what it cannot confirm.

The check runs after the chain, expiry, list and manual checks, so a certificate that is already refused costs no request, and it asks about every certificate below the anchor on every verified path. A `revoked` answer refuses the connect in both checking modes. An `unknown` answer, a malformed or unverifiable one, and a responder that is unreachable, slow or busy all count as no answer, and the mode decides what that means: `ENFORCE_KNOWN` admits the connect and counts it (`x509_ocsp_unavailable_allowed_total`), while `REQUIRE_CURRENT` refuses a certificate whose status it could not confirm current. Only good answers are cached, and only for a bounded time.

The responder URL comes from the certificate's Authority Information Access entry, or from the anchor's own `ocspResponderUrl`, which a tenant sets with `updateDeviceTrustAnchor` and which replaces the certificate's entry. Every lookup goes through the platform's outbound egress guard. A URL taken from a certificate gets no operator allow list; only an anchor's own `ocspResponderUrl` can reach a destination an operator has allowed.

Current limits: a `revoked` answer refuses that connect, but it does not yet end live sessions and is not written to the revocation store, so it is not shared with other certificates or replicas beyond the refusal itself. There is no background refresh; an answer is fetched when a connect needs it. Switch anchors to a checking mode only after a rolling upgrade has completed, because replicas that have not upgraded do not check OCSP and could admit a certificate that another replica refuses. An OCSP refusal is counted in `callout_x509_total` as `ocsp_revoked` (a revoked answer) or `ocsp_unavailable` (`REQUIRE_CURRENT` could not confirm).

A refusal by list or by manual revocation is counted as `cert_revoked`, and one by `REQUIRE_CURRENT` as `revocation_unknown`, in `callout_x509_total`. Short-lived certificates remain the cheapest revocation; use lists and manual revocation for the certificate you cannot wait out.

## What is not supported {#not-supported}

- **Name constraints.** A name-constrained CA is refused, as above.
- **Fetching a CRL on its own, and OCSP that ends sessions.** Lists are uploaded to the trust anchor; the platform never fetches one. An OCSP check at connect exists, per anchor and off by default, but a revoked answer does not yet end live sessions or update the revocation store, and there is no background refresh. See [Revocation](#revocation).
- **Renewal on its own, and adopting a device after its provisioning is reset.** A device that holds a certificate and then presents a different one is refused (`ALREADY_PROVISIONED`) when it connects for registration. Until renewal ships, add the new certificate as a pinned credential (a device can hold three) before the old one expires.
- **Ed25519 keys in the C# SDK.** The SDK presents a client certificate, but .NET has no Ed25519, so it cannot present one that uses that key type.
- **HTTP ingest and LwM2M.** Neither authenticates with a client certificate. HTTP devices use a token or basic credential, and LwM2M devices use pre-shared keys. A certificate presented on MQTT with any username other than `{tenant}:@x509` is ignored, and the connect is judged on its token or password as before.
- **Other brokers.** Only the platform's own broker authenticates by certificate. A device connecting through an operator-owned external broker is not covered.
- **Certificates issued by the platform.** You bring your own CA and your own certificates.
