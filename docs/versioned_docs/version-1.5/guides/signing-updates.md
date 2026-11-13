---
sidebar_position: 12
title: Signing Updates
---

# Signing Updates

A digest check proves an image is the image that was declared. It does not prove who made it. This guide shows how to attach that proof: you register the public keys your organization signs with, sign each artifact's digest, and let the platform hand the matching signature to the device, which makes the final decision.

:::note Status
Signing ships with the signer registry in the update service. Until your instance runs a build that includes it, artifacts stay `UNVERIFIED` and assignments carry no signature. For the concepts behind this, see [Over-the-Air Updates](../concepts/over-the-air-updates.md#verified); for the messages a device receives, see [Connecting a Device](./connecting-a-device.md#over-the-air-updates).
:::

## How trust works

- A **signer** is a public key the tenant trusts. The platform stores only the public half and refuses a private key.
- A **signature** is made over the artifact's SHA-256 digest and attached to the artifact. The platform verifies it against the digest it stored at upload.
- An artifact's **trust status** is derived every time it is read: `SIGNED` while at least one active signer has a verified signature on it, otherwise `UNVERIFIED`. Revoking a signer therefore changes every artifact it vouched for, with no write to the artifacts.
- Assigning an `UNVERIFIED` artifact is refused unless the instance allows unverified artifacts (`updates.allowUnverifiedArtifacts`).
- The **device** verifies the signature against keys it already holds. The platform relays the signature; it never decides for the device.

Two authorities are involved, kept separate on purpose: `artifact:trust` decides whose signature counts (registering and revoking signers), and `artifact:write` publishes artifacts and attaches signatures.

## 1. Generate a key

Use ECDSA over P-256 or Ed25519. No other key type or curve is accepted. Keep the private key off the platform and out of the build servers that do not need it.

```bash
# ECDSA P-256
openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt -out signer.pem

# or Ed25519
openssl genpkey -algorithm ed25519 -out signer.pem

# the public half, as an SPKI PEM block (this is what you register)
openssl pkey -in signer.pem -pubout -out signer.pub.pem
```

## 2. Compute the key ID

A key's identity, the `keyId` a device looks up, is the lowercase hex SHA-256 of its public key as DER `SubjectPublicKeyInfo`:

```bash
openssl pkey -in signer.pem -pubout -outform DER | openssl dgst -sha256 -r | cut -d' ' -f1
```

The platform computes the same value when you register the key and rejects a second registration of the same key. The signer's token is `signer-` followed by the first 16 hex characters of the key ID.

## 3. Register the signer

Requires `artifact:trust`. Pass the public PEM only.

```graphql
mutation {
  registerArtifactSigner(
    name: "Release signing key 2026"
    publicKeyPem: "-----BEGIN PUBLIC KEY-----\nMFkw…\n-----END PUBLIC KEY-----\n"
  ) {
    token
    keyId
    algorithm
    state
  }
}
```

A private key, any other key type or curve, or a key that is already registered is refused. `artifactSigners` lists the tenant's signers with their state, and each reports `soleSignedArtifactCount`, the number of artifacts that would fall back to `UNVERIFIED` if it were revoked.

## 4. Sign an artifact's digest

Both algorithms sign the **raw 32-byte SHA-256 digest** of the artifact. Sign the bytes you uploaded; the result must verify against the `digest` the artifact was declared with.

**ECDSA P-256.** `openssl dgst -sha256 -sign` hashes the file and signs the digest, and produces the ASN.1 DER signature the platform and devices expect:

```bash
openssl dgst -sha256 -sign signer.pem -binary firmware.bin | base64 | tr -d '\n'
```

`cosign sign-blob --key` is documented to produce a signature of the same form for ECDSA keys, so its base64 output should be accepted too. That behavior comes from cosign's documentation and was not tested for this guide; confirm it on a throwaway artifact before relying on it.

**Ed25519.** Ed25519 signs a message directly, so the message must be the 32 digest bytes, not the file:

```bash
openssl dgst -sha256 -binary firmware.bin > digest
openssl pkeyutl -sign -rawin -inkey signer.pem -in digest | base64 | tr -d '\n'
```

:::warning Do not sign the file with Ed25519
`openssl pkeyutl -sign -rawin -in firmware.bin` (and tools that sign blobs directly) signs the file itself. That signature is cryptographically valid and still wrong: the platform and the device both verify against the digest, so it will be refused when you attach it, or rejected on the device at install time. Always sign the `digest` file as shown.
:::

These recipes were checked with OpenSSL 3.2.1 against an independent verifier that computes the SHA-256 of the file and calls the Go standard library's ECDSA and Ed25519 verification: both signatures above verify, and an Ed25519 signature made over the file instead of the digest does not.

## 5. Attach the signature

Requires `artifact:write`. The platform verifies the signature against the artifact's stored digest before keeping it.

```graphql
mutation {
  addArtifactSignature(
    artifactToken: "fw-1-4-2"
    signerToken: "signer-b1b9eef5a4de45e2"
    signature: "MEQCIHGf…"
  ) {
    token
    trustStatus
  }
}
```

A signature that does not verify is refused, and so is one from a revoked signer. Signing again with the same signer changes nothing. An artifact may carry signatures from several signers. When it is assigned, the platform pins the **oldest** signature by an active signer, and the device is sent that signature with its `keyId` and algorithm (`ECDSA_P256_SHA256` or `ED25519`).

An artifact lists the signatures on record in its `signatures` field, oldest first. Each entry names the `signer`, `createdAt`, `createdBy` and `countsTowardTrust`, which is true while the signer is active and false once it is revoked. Signatures by revoked signers stay listed. The signature bytes are never returned. The console's artifact page shows the same list under **Signatures and trust**.

## 6. Put the key on your devices

The platform does not manage device-side trust. Every device that should accept your updates must hold the public key (or at least its `keyId` and key) before it receives a signed assignment. It fails the update with `UNKNOWN_SIGNER` for a `keyId` it does not hold and `SIGNATURE_INVALID` for a signature that does not verify. Provision keys in the factory image or through a configuration update you have already verified.

A device must hold the key of **every signer that could be pinned** to an assignment it may receive, not only your newest key.

## Rotating keys

1. Generate and register the new signer.
2. Ship the new public key to every device, and wait until your fleet has it. Do this **before** anything is signed with it.
3. Sign new artifacts with the new key. Where an artifact carries both signatures, the oldest active one is pinned, so the old key keeps being used until it is revoked.
4. Revoke the old signer when no device still needs it.

Never revoke the old key before the new one is on the devices. Devices that have not yet received the new key would then be sent signatures they cannot check.

## Revoking a signer

Requires `artifact:trust`.

```graphql
mutation {
  revokeArtifactSigner(token: "signer-b1b9eef5a4de45e2") { token state revokedAt }
}
```

Revocation is terminal and idempotent. The signer's signatures stay on record and stop counting:

- Artifacts it alone vouched for read `UNVERIFIED` immediately, and new assignments of them are refused unless the instance allows unverified artifacts.
- An assignment already pinned to that signature keeps it, so an update in flight is delivered as it was assigned.
- If the platform cannot read an assignment's pinned signature, it sends nothing rather than sending the update unsigned.
- With the instance flag off, an unpinned assignment (one made while unverified artifacts were allowed) is not sent.

Check `soleSignedArtifactCount` first to see what a revocation will affect.

## What a signature does not cover

A signature binds the **digest only**. It does not bind the artifact's version, component or compatible profiles. Anyone with `artifact:write` can declare a previously signed image again under a new version, and the old signature will verify. If you must refuse downgrades or wrong-component installs, the device has to enforce that itself, and publishing rights should be held as carefully as signing rights. [Building an Updatable Device](./building-an-updatable-device.md#refusing-downgrades) shows how a reference device does it.

## Related

- **[Signers](../concepts/over-the-air-updates.md#signers)**: how a signature makes an artifact trusted, and what revoking a signer changes.
- **[Over-the-Air Updates](../concepts/over-the-air-updates.md#assigning)**: assigning an update and how an attempt progresses.
- **[Connecting a Device](./connecting-a-device.md#over-the-air-updates)**: the assignment message and the device-side check.
- **[Building an Updatable Device](./building-an-updatable-device.md)**: a reference device that verifies, installs and rolls back.
- **[Updates in the console](../user-guide/updates.md)**: uploading artifacts, registering signers and assigning updates from the web console.
