---
sidebar_position: 3.7
title: Connecting with a Certificate
---

# Connecting with a Certificate {#connecting-with-a-certificate}

A device can authenticate to the platform broker with an X.509 client certificate instead of a token or a password. This guide takes you from a certificate authority (CA) to a device publishing its first event, and shows both ways a certificate can become a device: a **pinned credential** you create for a device that exists, and a **certificate profile** that registers devices the first time they connect.

How the platform decides to trust a certificate, and where that trust stops, is in [Certificate identity](../concepts/device-certificates.md). Read its [limits](../concepts/device-certificates.md#not-supported) before you plan a fleet around this: there is no CRL or OCSP, name-constrained CAs are refused, and renewal and the C# SDK are not available yet.

:::note Status
Available over MQTT on the platform broker. HTTP ingest and LwM2M do not accept client certificates.
:::

## What you need {#what-you-need}

- A CA whose certificates you will trust, and a certificate for each device. This guide mints a throwaway CA with `openssl` so you can try the whole path; in production use your own PKI.
- A tenant, and permission to manage devices (`device:write`).
- The instance CA certificate, so the device can verify the broker, as in [Connecting a device](./connecting-a-device.md#publishing-an-event).

## Mint a test CA and a device certificate {#mint}

The device certificate must carry the **client authentication** extended key usage and must not be a CA. Its common name (CN) becomes the device's external id, so use the serial number or other id you want the device known by.

```bash
# A CA: a P-256 key and a self-signed certificate that may sign certificates.
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=Acme Device CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out ca.crt

# A device: its own key, and a certificate the CA signs for it. CN = the device's external id.
openssl ecparam -name prime256v1 -genkey -noout -out device.key
openssl req -new -key device.key -subj "/CN=SN-000123" -out device.csr
cat > device.ext <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in device.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days 90 -extfile device.ext -out device.crt

# Check what you made.
openssl x509 -in device.crt -noout -subject -ext extendedKeyUsage -enddate
openssl verify -CAfile ca.crt -purpose sslclient device.crt
```

The last command must print `device.crt: OK`. Keep `ca.key` and `device.key` private. Only `ca.crt` and `device.crt` ever go to the platform.

The platform accepts RSA keys of 2,048 to 8,192 bits, ECDSA on P-256 or P-384, and Ed25519, and refuses SHA-1 signatures anywhere in the chain. A 90-day lifetime is deliberate: the platform has no revocation list, so a short lifetime is how an unwanted certificate goes away by itself.

## Add the CA as a trust anchor {#trust-anchor}

A trust anchor tells the tenant which CA to believe. In the console, open **Trust Anchors**, choose to add one, and paste or upload `ca.crt`. The console refuses a file that contains a private key. Over GraphQL:

```graphql
mutation {
  createDeviceTrustAnchor(request: {
    token: "acme-device-ca"
    name: "Acme device CA"
    certificatePem: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
  }) { token fingerprintSha256 notAfter enabled }
}
```

The anchor must be exactly one CA certificate, unexpired, with a supported key, and with no name constraints. If your devices' certificates are issued by an intermediate CA, anchor at the intermediate rather than the root, so the tenant trusts only the CA that issues your fleet.

## Choose how devices appear {#choose}

Pick one for each fleet.

### Option A: pin a certificate to a device you created {#pinned}

Use this when you create devices yourself and know each device's certificate.

1. Create the device (see [Importing devices](./importing-devices.md) for many at once). Its token is your choice.
2. Add an `X509_CERTIFICATE` credential. In the console, open the device's **Credentials** tab, choose the certificate type, paste `device.crt` and pick the anchor. Over GraphQL:

   ```graphql
   mutation {
     createDeviceCredential(request: {
       token: "sn-000123-cert"
       deviceToken: "sensor-001"
       credentialType: "X509_CERTIFICATE"
       enabled: true
       certificatePem: "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
       trustAnchorToken: "acme-device-ca"
     }) { token credentialId expiresAt certificate { subjectCn trustAnchor { token } } }
   }
   ```

3. Do not send a `credentialId`; it is refused for this type. The server computes it as the SHA-256 of the certificate, and returns it. It is public, and it is the pin.

The platform verifies the chain when you create the credential, and refuses one that could not authenticate. A device can hold up to three live certificate credentials, so you can add the next certificate before the old one expires. The credential's expiry is the earliest expiry along the whole chain.

### Option B: register devices on their first connect {#registration}

Use this for a fleet that comes off a production line with certificates already installed. No device has to exist beforehand.

1. Create a **certificate profile**. In the console, open **Provisioning Profiles**, choose the **Certificate** mode and pick the anchor. Over GraphQL, create a provisioning profile that names the anchor:

   ```graphql
   mutation {
     createProvisioningProfile(request: {
       token: "acme-sensors-certs"
       provisionKey: "acme-sensors-certs"
       strategy: "ALLOW_NEW"
       deviceTypeToken: "acme-sensor"
       enabled: true
       maxDevices: 5000
       trustAnchorToken: "acme-device-ca"
     }) { profile { token trustAnchor { token } } issuedSecret }
   }
   ```

   `issuedSecret` comes back null: a certificate profile has no secret to flash, protect or rotate.
2. Set `maxDevices`. Under `ALLOW_NEW`, any certificate the anchor's CA issues with a common name that no device has used registers a new device, so the cap and a narrow anchor are what bound the damage of a mistake.
3. Start the fleet. The first time a device connects, the platform creates it with the certificate's common name as its external id, pins the certificate to it and lets it in. Later connects use the pin.

The device's token is derived from its common name, which matters for the client id below. Registration on first connect creates new devices only. A device you created and earmarked for the profile cannot register by certificate yet, and neither can a device whose provisioning was reset; give those devices a pinned credential (option A). A device that holds one certificate and then presents a different one is refused until renewal is available, so add the new certificate as a pinned credential first.

## Connect {#connect}

The device connects to the same MQTT endpoint as any other, over TLS. What differs:

| Setting | Value |
| --- | --- |
| Client certificate | `device.crt` (followed by any intermediate CA certificates), and `device.key` |
| Username | `{tenant}:@x509` |
| Password | none. A password sent with this username is refused |
| Client id | `{instanceId}:{tenant}:{deviceToken}`, as for every device |

The `@` makes the username impossible to confuse with a credential id. The tenant comes from the username, never from the certificate.

```bash
mosquitto_pub \
  --cafile instance-ca.crt --cert device.crt --key device.key \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:pv-SN-000123-06e08144e3e8' \
  -u 'acme:@x509' \
  -t 'devicechain/acme/devices/pv-SN-000123-06e08144e3e8/events' \
  -m '{"device":"pv-SN-000123-06e08144e3e8","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

The event body and topic are the same as for any other device; see [Connecting a device](./connecting-a-device.md). A device whose certificate was issued by an intermediate CA must send that intermediate after its own certificate, because the platform builds the chain from what the device presents up to the anchor. Send the leaf and at most one intermediate: the platform accepts at most five certificates and 16 KiB, and some TLS stacks (mbedTLS at its default 16 KiB output buffer) cannot send a longer chain.

### The client id {#client-id}

A pinned device (option A) uses the token you gave it, as always. A device that registers on first connect (option B) uses the token the platform derives from its common name, so the device can compute it from its own certificate:

```text
pv-{slug}-{hash}
```

- `slug`: the common name with each character other than letters, digits, `-` and `_` replaced by `-`, then leading and trailing `-` and `_` removed.
- `hash`: the first 12 hexadecimal characters of the SHA-256 of the common name.
- The whole token is at most 128 characters; a long `slug` is cut to fit.

With the shell, for the certificate above:

```bash
CN=SN-000123
slug=$(printf '%s' "$CN" | sed -E 's/[^A-Za-z0-9_-]/-/g; s/^[-_]+//; s/[-_]+$//')
hash=$(printf '%s' "$CN" | openssl dgst -sha256 -hex | sed 's/^.*= //' | cut -c1-12)
echo "devicechain:acme:pv-$slug-$hash"     # devicechain:acme:pv-SN-000123-06e08144e3e8
```

A first connect whose client id does not match is refused before anything is created, so a mistake cannot leave a half-registered device. A common name must be 1 to 256 characters, not blank, and contain no NUL character.

If you create a device ahead of time for a certificate profile, leave its token empty and give it the external id: the console and the API fill in the derived token, and refuse a different one.

## When a connect is refused {#refused}

Like every device refusal, the device sees only CONNACK return code 5. The reason is on the operator's side, as the `result` label of the `callout_x509_total` counter. Check in this order:

1. **`no_cert`.** The client sent no certificate. Check `--cert`, and that the key matches the certificate.
2. **`chain_invalid`.** The chain does not build to the anchor, a certificate has expired, the extended key usage lacks client authentication, a signature uses SHA-1, or a key is outside the allowed set. Reproduce with `openssl verify -CAfile ca.crt -purpose sslclient device.crt`, and check that the device sends its intermediates.
3. **`anchor_disabled`, `revoked`.** The anchor or the credential was disabled on purpose.
4. **`client_id_mismatch`.** The client id is not `{instanceId}:{tenant}:{deviceToken}` for this device, or for a first connect, the derived one.
5. **`unknown_pin`.** The certificate is not pinned and chains to no usable certificate profile.
6. **`jit_*`.** A first-connect registration was refused: `jit_rate_limited` and `jit_busy` mean the device should back off and retry, `jit_ambiguous` means two profiles tie, and the rest carry the registration error code.

[Observability](../deployment/observability.md#certificate-alerts) lists the alerts and what to do about each. A certificate sent with a token or password username is ignored (counted as `x509_cert_ignored`) and the connect is judged on the token or password.

## Retries and lifetimes {#lifetimes}

- **Retry with backoff.** A device refused for `jit_rate_limited` or `jit_busy` is not wrong; MQTT has no way to say "try later". Retry with exponential backoff and full jitter, so a factory run does not hammer the broker.
- **The session ends at the earliest expiry.** The platform closes the session when the earliest certificate in the chain expires, with no grace. Renew before then and reconnect with the new certificate. Until renewal is available, a device needs the new certificate pinned as a second credential (option A) before the old one expires.
- **A 5-minute allowance** covers a certificate that was issued moments ago for a device whose clock is slightly behind.

## Try it without firmware {#simulator}

The simulator plays certificate devices: `dc-simulator --x509` mints a throwaway CA and certificates (ECDSA P-256, client authentication, CN = external id), registers them through the API with `--x509-register`, and connects each one. `--x509-jit N` mints certificates for devices it never registers, to exercise a certificate profile. See the simulator's README for the flags.

## What this does not do {#limits}

- **No CRL, no OCSP.** To stop a certificate, disable its credential (or the device, or the anchor), and issue short-lived certificates.
- **A name-constrained CA is refused.** The trust anchor is the identity boundary.
- **No renewal of a registered device by presenting a new certificate, and no adoption of a reset device.** Not available yet.
- **No client certificate in the C# SDK.** Not available yet.
- **No certificates over HTTP or LwM2M.**

The [concept page](../concepts/device-certificates.md#not-supported) says why. For the broker side, including what changes when you upgrade an existing instance, see [Certificate authentication](../deployment/device-certificates.md).
