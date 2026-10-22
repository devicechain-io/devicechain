---
sidebar_position: 11
title: Verifying releases
---

# Verifying releases {#verifying-releases}

Every container image and the Helm chart a DeviceChain release publishes is **signed**, and carries
two signed statements about how it was made. You can check all of it before you run anything, with
no account and no key to obtain.

| What | Where it lives | What it tells you |
| --- | --- | --- |
| **Signature** | Next to the image or chart in the registry | It was published by DeviceChain's release workflow, for that exact tag. |
| **SBOM** (SPDX) | Next to each image, as a signed attestation | The packages inside the image. |
| **Build provenance** (SLSA) | Next to each image and the chart, and in GitHub's attestation store | Which commit, workflow and run built it. |

The signing is **keyless**: there is no long-lived key to leak or rotate. Each signature is bound to
the identity of the workflow run that made it, recorded in a public transparency log, and verified
against that identity. A signature from any other workflow, repository or branch does not verify as
a release.

:::note Which releases
Signing applies to the releases whose notes announce it, which is no later than `v1.0.0`. Earlier
images carry no signature, and verifying one fails with "no signatures found". That is the correct
answer, not a fault in your setup.
:::

## What is signed {#what}

- every service image: `ghcr.io/devicechain-io/<service>` (for example `device-management`)
- the Kubernetes operator image: `ghcr.io/devicechain-io/operator`
- the edge agent image: `ghcr.io/devicechain-io/dc-edge-agent`
- the web console image: `ghcr.io/devicechain-io/frontend`
- the Helm chart: `oci://ghcr.io/devicechain-io/charts/devicechain`

Images are tagged with the release tag (`v1.0.0`). The chart is tagged with the version **without**
the `v` (`1.0.0`). The `dcctl` and edge agent archives attached to the GitHub Release have their
own provenance; see [Releases & Upgrades](./releases-and-upgrades.md).

## Verify everything at once {#script}

The repository ships one script that checks every image and the chart, then proves its own check can
fail. It needs [`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/) 3 or later,
`jq`, and the [GitHub CLI](https://cli.github.com/) signed in (`gh auth login`) for provenance.

```bash
curl -fsSLO https://raw.githubusercontent.com/devicechain-io/devicechain/v1.0.0/hack/verify-release-signatures.sh
chmod +x verify-release-signatures.sh
./verify-release-signatures.sh v1.0.0
```

Fetch the script from the **same tag** you are verifying. It exits non-zero if any signature,
SBOM or provenance check fails, and prints which one.

It ends with two **negative controls**. A verifier that has only ever seen good input has never been
shown to reject anything, so the script verifies a signed DeviceChain image against an identity that
cannot match, and verifies an image that was not signed for this release (by default, an older
DeviceChain operator image, from before releases were signed), and requires both to be rejected for
the right reason. A run that skips either is not a pass.

## Verify one image by hand {#by-hand}

```bash
TAG=v1.0.0
IMAGE=ghcr.io/devicechain-io/device-management:$TAG

cosign verify "$IMAGE" \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Both flags matter. Without the identity, `cosign verify` would accept a signature from anyone who
can sign keylessly, which is everyone. Naming the workflow **and the tag** is what makes the
answer mean "DeviceChain's `$TAG` release".

The SBOM and the provenance:

```bash
# The SBOM, as a signed attestation; print the package list.
cosign verify-attestation --type spdxjson "$IMAGE" \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/$TAG" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  | jq -r '.payload | @base64d | fromjson | .predicate.packages[].name' | sort -u

# Build provenance.
gh attestation verify "oci://$IMAGE" --repo devicechain-io/devicechain \
  --signer-workflow devicechain-io/devicechain/.github/workflows/release.yml
```

The chart is verified the same way, with its own tag:

```bash
cosign verify ghcr.io/devicechain-io/charts/devicechain:1.0.0 \
  --certificate-identity "https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/v1.0.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Pin by digest {#digest}

A tag is a pointer, and a registry can move it. `cosign verify` prints the digest it verified;
deploy **that** (`image@sha256:...`) and what you ran is what you checked. If you mirror images into
your own registry, copy the signatures with them (`cosign copy`), or verify before mirroring and
record the digests.

To refuse unsigned images at admission rather than by hand, enforce the same identity and issuer
with an admission policy such as [Sigstore's policy controller](https://docs.sigstore.dev/policy-controller/overview/).

## What this does and does not tell you {#limits}

- **Origin, not fitness.** A signature says DeviceChain's release workflow produced the artifact. It
  does not say the code is free of defects; that is what the release gates and your own testing are
  for.
- **The SBOM is generated from the image's `amd64` variant.** The `arm64` variant is built from the
  same source and dependencies, but the attestation covers the multi-architecture index as a whole
  and was produced from one platform.
- **Provenance names the builder, not the contents.** It lets you confirm the commit and workflow;
  it is not a substitute for reading the SBOM.
- **Verification trusts the public Sigstore infrastructure** (the certificate authority and the
  transparency log) and GitHub's identity tokens. If you cannot reach them from where you verify, do
  the verification where you can, and carry the digests across.

To report a problem with a signature, or a vulnerability in anything verified here, see the
[security policy](https://github.com/devicechain-io/devicechain/security/policy).
