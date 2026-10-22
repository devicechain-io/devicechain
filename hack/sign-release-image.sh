#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Sign one released OCI artifact BY DIGEST, keylessly, and (for an image) attach its SBOM
# as a signed attestation.
#
#   hack/sign-release-image.sh <registry/name@sha256:...> [--no-sbom]
#
# Called by the `sign` job in release.yml AND by release-signing-dryrun.yml, which runs the
# same bytes against a throwaway registry on every pull request that touches the signing
# path. That sharing is the point: the signing step only ever runs at tag time otherwise,
# and a step that only runs at tag time is a step whose first failure costs a release
# candidate (see the release plumbing checklist).
#
# WHY BY DIGEST. A tag is a pointer the registry can move; a signature over a tag-resolved
# reference signs whatever the tag happened to point at when cosign resolved it. The digest
# is the bytes. This script REFUSES a reference without one rather than resolving it.
#
# KEYLESS. The signing identity is the GitHub Actions OIDC token of the calling workflow
# (the job needs `id-token: write`); there is no key to hold, rotate or leak. What a
# verifier binds to is the workflow path + ref baked into the Fulcio certificate -- see
# hack/verify-release-signatures.sh.
#
# Needs on PATH: cosign (>= 3) and, unless --no-sbom, syft (override with $SYFT).
set -euo pipefail

usage() { echo "usage: $0 <registry/name@sha256:digest> [--no-sbom]" >&2; exit 2; }

ref="" sbom=1
for a in "$@"; do
  case "$a" in
    --no-sbom) sbom=0 ;;
    -*) usage ;;
    *) [ -z "$ref" ] || usage; ref="$a" ;;
  esac
done
[ -n "$ref" ] || usage

if ! [[ "$ref" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]]; then
  echo "refusing to sign '$ref': a release is signed by DIGEST (name@sha256:<64 hex>), never by tag." >&2
  exit 2
fi

echo "::group::sign $ref"
cosign sign --yes "$ref"
echo "::endgroup::"

if [ "$sbom" -eq 1 ]; then
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  # A registry on localhost (the dry run) is plain HTTP.
  case "$ref" in
    localhost:*|127.0.0.1:*) export SYFT_REGISTRY_INSECURE_USE_HTTP=true ;;
  esac
  echo "::group::sbom $ref"
  "${SYFT:-syft}" scan "registry:${ref}" -o "spdx-json=${work}/sbom.spdx.json"
  # An empty SBOM attests nothing and reads like a complete one. A Go image with no
  # packages is a scanner that found nothing, not an image with nothing in it.
  n="$(jq '.packages | length' "${work}/sbom.spdx.json")"
  if [ "${n:-0}" -lt 1 ]; then
    echo "::error::syft produced an SBOM with no packages for $ref" >&2
    exit 1
  fi
  echo "SBOM lists $n packages"
  cosign attest --yes --type spdxjson --predicate "${work}/sbom.spdx.json" "$ref"
  echo "::endgroup::"
fi
