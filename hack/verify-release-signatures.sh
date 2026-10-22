#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Verify that every artifact a DeviceChain release publishes to a container registry was
# signed by THIS repository's release workflow -- and prove the check can fail.
#
#   hack/verify-release-signatures.sh <tag> [options]
#   hack/verify-release-signatures.sh --self-test
#
# For each of the release images and the Helm chart it checks:
#   1. the cosign keyless SIGNATURE, bound to the workflow identity (certificate identity
#      + OIDC issuer) -- not merely "signed by somebody";
#   2. for images, the signed SPDX SBOM ATTESTATION (cosign verify-attestation);
#   3. the SLSA build PROVENANCE attestation (gh attestation verify, which checks it against
#      GitHub's attestation store and the same workflow identity).
# Then it runs the NEGATIVE CONTROLS (below) so a green run means the verifier discriminates.
#
# Self-contained on purpose: a user can download this one file next to a release. The image
# list is embedded and is checked against release.yml by backend/k8s/functionalarea's
# release contract test, so it cannot drift from what the release publishes.
#
# Options
#   --registry R            default ghcr.io/devicechain-io
#   --repo OWNER/REPO       default devicechain-io/devicechain (the workflow's home)
#   --workflow FILE         default release.yml (the workflow that signs)
#   --identity-regexp RE    replaces the derived certificate identity regexp entirely
#                           (the signing dry run uses it; a release verification must not)
#   --source-ref REF        git ref the provenance must name (default refs/tags/<tag>)
#   --issuer URL            default https://token.actions.githubusercontent.com
#   --image NAME            verify only this image (repeatable); default every image
#   --foreign-image REF     image NOT signed by the workflow for this tag, for negative
#                           control B (default: the v0.23.0 operator image BY DIGEST --
#                           ours, on the same registry as everything else checked here, so
#                           the control cannot fail over a third-party registry's outage;
#                           released before signing existed, and never signed for any
#                           newer tag in any case)
#   --skip-provenance       do not run `gh attestation verify` (explicit, never implicit)
#   --with-chart            with --image, verify the chart too (otherwise --image skips it)
#   --skip-chart            do not verify the chart
#
# Needs: cosign >= 3, jq, and (unless --skip-provenance) an authenticated gh.
#
# NEGATIVE CONTROLS. A verifier that only ever sees good input has never been shown to
# reject anything -- "cosign verify" returning 0 is worth exactly as much as the identity
# it was told to demand. So after the real checks:
#   A. the SAME signed image is verified against an identity that cannot match; this must
#      FAIL, with cosign's identity-mismatch error (proves the identity binding bites);
#   B. a FOREIGN image is verified against the real identity; this must FAIL with a
#      no-signature / identity error, not a pull error (a registry 404 "fails" too, for
#      the wrong reason, and would make this control pass over nothing).
set -euo pipefail

SELF="$(basename "$0")"
REGISTRY="ghcr.io/devicechain-io"
REPO="devicechain-io/devicechain"
WORKFLOW="release.yml"
ISSUER="https://token.actions.githubusercontent.com"
IDENTITY_RE=""
SOURCE_REF=""
FOREIGN=""
SKIP_PROV=0
SKIP_CHART=0
WITH_CHART=0
ONLY=()

# Keep in step with the release: every backend/services/<dir> with a main.go, the operator
# (backend/k8s), the edge agent (backend/edge/dc-edge-agent) and the console. The release
# contract test (backend/k8s/functionalarea) fails the build if this array and release.yml
# disagree. The chart is separate: it is an OCI artifact, not an image.
RELEASE_IMAGES=(
  ai-inference command-delivery dashboard-management device-management device-state
  event-management event-processing event-sources lwm2m-ingest mcp
  notification-management outbound-connectors sparkplug-ingest update-management
  user-management
  operator dc-edge-agent frontend
)
RELEASE_CHART="charts/devicechain"
DEFAULT_FOREIGN="ghcr.io/devicechain-io/operator@sha256:c3106468ae919b9f129fcf86b659808db10f9fdc49af3300d0cae71db35374dc"

die() { echo "error: $*" >&2; exit 2; }

# Escape regexp metacharacters so a tag / path matches literally. Slash is not special in
# ERE, so it is left alone (and sed's own delimiter is '#', not '/').
re_escape() { printf '%s' "$1" | sed -E 's#[].^$*+?(){}|\\[]#\\&#g'; }

derive_identity_re() { # <tag>
  printf '^https://github\.com/%s/\.github/workflows/%s@refs/tags/%s$' \
    "$(re_escape "$REPO")" "$(re_escape "$WORKFLOW")" "$(re_escape "$1")"
}

# verify_sig <ref> <identity-regexp>  -> prints the verified digest; cosign's stderr -> $ERR
verify_sig() {
  cosign verify \
    --certificate-identity-regexp "$2" \
    --certificate-oidc-issuer "$ISSUER" \
    "$1" 2>"$ERR" | jq -r '.[0].critical.image."docker-manifest-digest"'
}

verify_sbom() { # <ref> <identity-regexp>
  cosign verify-attestation --type spdxjson \
    --certificate-identity-regexp "$2" \
    --certificate-oidc-issuer "$ISSUER" \
    "$1" >/dev/null 2>"$ERR"
}

verify_prov() { # <ref> <tag>
  gh attestation verify "oci://$1" --repo "$REPO" \
    --signer-workflow "$REPO/.github/workflows/$WORKFLOW" \
    --source-ref "${SOURCE_REF:-refs/tags/$2}" >/dev/null 2>"$ERR"
}

fail_count=0
note_fail() { echo "  FAIL  $1"; sed -n '1,8s/^/        /p' "$ERR"; fail_count=$((fail_count + 1)); }

# The error a REAL rejection carries. A pull error / 404 / auth error is deliberately not
# in this list: it would make a control pass over a broken registry.
REJECTION_RE='no signatures found|no matching signatures|none of the expected identities matched|no matching attestations|expected SAN|certificate identity'

negative_controls() { # <identity-re> <tag> <image>
  local re="$1" tag="$2" img="$3" ref bogus foreign
  # A: right image, wrong identity.
  ref="$REGISTRY/$img:$tag"
  bogus='^https://github\.com/not-the-devicechain-repo/nothing/\.github/workflows/nope\.yml@refs/heads/main$'
  if verify_sig "$ref" "$bogus" >/dev/null; then
    echo "  CONTROL A FAILED: $ref verified against an identity that cannot match -- the verifier does not bind identity."
    fail_count=$((fail_count + 1))
  elif grep -qiE "$REJECTION_RE" "$ERR"; then
    echo "  ok    control A: a signed image is REJECTED under a wrong identity"
  else
    echo "  CONTROL A INCONCLUSIVE: it failed, but not with an identity/signature error:"
    head -5 "$ERR" | sed 's/^/        /'
    fail_count=$((fail_count + 1))
  fi
  # B: foreign image, real identity.
  foreign="${FOREIGN:-$DEFAULT_FOREIGN}"
  if verify_sig "$foreign" "$re" >/dev/null; then
    echo "  CONTROL B FAILED: $foreign verified under the release identity -- it must not."
    fail_count=$((fail_count + 1))
  elif grep -qiE "$REJECTION_RE" "$ERR"; then
    echo "  ok    control B: a foreign / unsigned image ($foreign) is REJECTED"
  else
    echo "  CONTROL B INCONCLUSIVE ($foreign): it failed, but not with an identity/signature error:"
    head -5 "$ERR" | sed 's/^/        /'
    fail_count=$((fail_count + 1))
  fi
}

run_checks() { # <tag>
  local tag="$1" re="$IDENTITY_RE" name ref digest pinned chart_tag
  [ -n "$re" ] || re="$(derive_identity_re "$tag")"
  chart_tag="${tag#v}"
  echo "identity : $re"
  echo "issuer   : $ISSUER"
  echo "registry : $REGISTRY"
  echo

  local images=("${RELEASE_IMAGES[@]}")
  [ "${#ONLY[@]}" -eq 0 ] || images=("${ONLY[@]}")

  for name in "${images[@]}"; do
    ref="$REGISTRY/$name:$tag"
    if digest="$(verify_sig "$ref" "$re")" && [ -n "$digest" ]; then
      echo "  ok    $name  signature  $digest"
    else
      note_fail "$name  signature  ($ref)"
      continue
    fi
    # Everything else is verified against the DIGEST that was just verified, so a tag
    # moved between the checks cannot make them describe different bytes.
    pinned="$REGISTRY/$name@$digest"
    if verify_sbom "$pinned" "$re"; then echo "  ok    $name  sbom attestation"; else note_fail "$name  sbom attestation"; fi
    if [ "$SKIP_PROV" -eq 0 ]; then
      if verify_prov "$pinned" "$tag"; then echo "  ok    $name  provenance"; else note_fail "$name  provenance"; fi
    fi
  done

  if [ "$SKIP_CHART" -eq 0 ] && { [ "${#ONLY[@]}" -eq 0 ] || [ "$WITH_CHART" -eq 1 ]; }; then
    ref="$REGISTRY/$RELEASE_CHART:$chart_tag"
    if digest="$(verify_sig "$ref" "$re")" && [ -n "$digest" ]; then
      echo "  ok    chart  signature  $digest"
      if [ "$SKIP_PROV" -eq 0 ]; then
        if verify_prov "$REGISTRY/$RELEASE_CHART@$digest" "$tag"; then echo "  ok    chart  provenance"; else note_fail "chart  provenance"; fi
      fi
    else
      note_fail "chart  signature  ($ref)"
    fi
  fi

  echo
  negative_controls "$re" "$tag" "${images[0]}"

  echo
  if [ "$fail_count" -gt 0 ]; then
    echo "$SELF: $fail_count check(s) FAILED for $tag"
    return 1
  fi
  echo "$SELF: every signature and attestation verified for $tag, and both negative controls failed as they must."
}

# --self-test: the pure parts, with no network. The cosign-facing parts are exercised by
# release-signing-dryrun.yml against a real (throwaway) signature.
self_test() {
  local bad=0 re good other
  re="$(derive_identity_re v1.2.3-rc.1)"
  [ "$re" = '^https://github\.com/devicechain-io/devicechain/\.github/workflows/release\.yml@refs/tags/v1\.2\.3-rc\.1$' ] \
    || { echo "FAIL derived identity: $re"; bad=1; }
  # The derived regexp must match its own tag and NOTHING else: not another tag, not a
  # branch, not a lookalike repo (the failure that makes identity binding worthless).
  good='https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/v1.2.3-rc.1'
  grep -qE "$re" <<<"$good" || { echo "FAIL regexp rejects its own identity"; bad=1; }
  for other in \
    'https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/v1.2.3-rc.10' \
    'https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/tags/v1.2.3' \
    'https://github.com/devicechain-io/devicechain/.github/workflows/release.yml@refs/heads/main' \
    'https://github.com/devicechain-io/devicechain/.github/workflows/ci.yml@refs/tags/v1.2.3-rc.1' \
    'https://github.com/evil/devicechain/.github/workflows/release.yml@refs/tags/v1.2.3-rc.1' \
    'https://github.com/devicechain-io/devicechainX.github/workflows/release.yml@refs/tags/v1.2.3-rc.1' \
    'https://github.com/devicechain-io/devicechain/.github/workflows/releaseXyml@refs/tags/v1.2.3-rc.1'; do
    if grep -qE "$re" <<<"$other"; then echo "FAIL regexp accepted: $other"; bad=1; fi
  done
  [ "${#RELEASE_IMAGES[@]}" -ge 18 ] || { echo "FAIL image list suspiciously short"; bad=1; }
  if [ "$bad" -eq 0 ]; then echo "$SELF self-test: ok"; else echo "$SELF self-test: FAILED"; return 1; fi
}

tag=""
while [ $# -gt 0 ]; do
  case "$1" in
    --self-test) self_test; exit $? ;;
    --registry) REGISTRY="${2:?}"; shift 2 ;;
    --repo) REPO="${2:?}"; shift 2 ;;
    --workflow) WORKFLOW="${2:?}"; shift 2 ;;
    --identity-regexp) IDENTITY_RE="${2:?}"; shift 2 ;;
    --source-ref) SOURCE_REF="${2:?}"; shift 2 ;;
    --issuer) ISSUER="${2:?}"; shift 2 ;;
    --image) ONLY+=("${2:?}"); shift 2 ;;
    --foreign-image) FOREIGN="${2:?}"; shift 2 ;;
    --skip-provenance) SKIP_PROV=1; shift ;;
    --with-chart) WITH_CHART=1; shift ;;
    --skip-chart) SKIP_CHART=1; shift ;;
    -*) die "unknown option $1" ;;
    *) [ -z "$tag" ] || die "one tag only"; tag="$1"; shift ;;
  esac
done
[ -n "$tag" ] || die "usage: $SELF <tag> [options]   (e.g. $SELF v1.0.0)"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "'$tag' is not a release tag (vX.Y.Z[-pre])"
for t in cosign jq; do command -v "$t" >/dev/null || die "$t is required"; done
if [ "$SKIP_PROV" -eq 0 ]; then command -v gh >/dev/null || die "gh is required for provenance (or pass --skip-provenance)"; fi

ERR="$(mktemp)"
trap 'rm -f "$ERR"' EXIT
run_checks "$tag"
