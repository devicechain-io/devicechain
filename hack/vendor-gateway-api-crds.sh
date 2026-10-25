#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# (Re)generate deploy/opentofu/modules/gateway-api-crds/chart/templates/ from the
# upstream Gateway API STANDARD-channel install bundle.
#
# WHAT IS DONE TO THE BUNDLE, AND WHY EACH STEP IS LOAD-BEARING
#
#   1. Only the CustomResourceDefinition documents are kept. The bundle also carries a
#      ValidatingAdmissionPolicy + binding, "safe-upgrades.gateway.networking.k8s.io",
#      with failurePolicy Fail and a Deny binding. It denies any write of a
#      gateway.networking.k8s.io CRD whose bundle-version matches v1.[0-5].x, so on a
#      cluster whose platform (GKE) manages an older set it would BLOCK THE PLATFORM'S
#      OWN RECONCILE of its CRDs. We ship the CRDs and never the policy.
#   2. One file per CRD, so the module can derive the CRD names, the pinned version and
#      the channel from the files themselves rather than from a second list.
#   3. `helm.sh/resource-policy: keep` is added to every CRD. Deleting a CRD CASCADES to
#      every Gateway and route in the cluster, and `prevent_destroy` does not protect a
#      resource whose module block was removed (orphans are destroyed without consulting
#      lifecycle). The annotation is the protection that does not depend on the config
#      still existing: helm leaves the object behind on uninstall, whoever asked.
#
# The pinned checksum is of the UPSTREAM bundle, so a changed or repointed release asset
# fails here rather than silently changing what ships. Bumping GATEWAY_API_VERSION means
# changing the checksum in the same commit, then re-running this script.
#
# Usage:  hack/vendor-gateway-api-crds.sh          # regenerate the templates
#         hack/vendor-gateway-api-crds.sh --check  # regenerate to a temp dir and diff
#
# Requires curl, sha256sum, awk.

set -euo pipefail

GATEWAY_API_VERSION="v1.6.3"
BUNDLE_SHA256="356c2b8e286a964dc20995435659ad1095386329974793af10bbfe3eff977fe5"
URL="https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="$repo_root/deploy/opentofu/modules/gateway-api-crds/chart/templates"
check=0
[ "${1:-}" = "--check" ] && check=1

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/bundle.yaml" "$URL"
got="$(sha256sum "$work/bundle.yaml" | cut -d' ' -f1)"
if [ "$got" != "$BUNDLE_SHA256" ]; then
  echo "FAIL: $URL has sha256 $got, expected $BUNDLE_SHA256" >&2
  exit 1
fi

out="$work/out"
mkdir -p "$out"
awk -v out="$out" -v ver="$GATEWAY_API_VERSION" -v sum="$BUNDLE_SHA256" '
  function flush(   f, i) {
    if (n == 0) return
    if (kind != "CustomResourceDefinition") { n = 0; kind = ""; name = ""; return }
    if (name == "") { print "FAIL: CRD document with no name" > "/dev/stderr"; bad = 1; exit 1 }
    f = out "/" name ".yaml"
    print "# Source: kubernetes-sigs/gateway-api " ver " standard-install.yaml (sha256 " sum ")." > f
    print "# Copyright The Kubernetes Authors. Licensed under the Apache License, Version 2.0." >> f
    print "# Modified: split out of the bundle, and `helm.sh/resource-policy: keep` added so deleting" >> f
    print "# this release can never delete the CRD (a CRD delete cascades to every Gateway and route)." >> f
    print "# Regenerate with hack/vendor-gateway-api-crds.sh; do not edit by hand." >> f
    for (i = 1; i <= n; i++) print lines[i] >> f
    close(f)
    n = 0; kind = ""; name = ""; inserted = 0
  }
  /^---[[:space:]]*$/ { flush(); next }
  {
    lines[++n] = $0
    if ($0 ~ /^kind: /) kind = $2
    if (name == "" && $0 ~ /^  name: /) { name = $2; gsub(/"/, "", name) }
  }
  END { if (!bad) flush() }
' "$work/bundle.yaml"

# The annotation goes in with sed, once per file, under the metadata-level annotations key.
for f in "$out"/*.yaml; do
  c="$(grep -c '^  annotations:$' "$f")"
  if [ "$c" -ne 1 ]; then
    echo "FAIL: $f has $c metadata-level 'annotations:' keys, expected exactly 1" >&2
    exit 1
  fi
  sed -i 's/^  annotations:$/  annotations:\n    helm.sh\/resource-policy: keep/' "$f"
done

count="$(find "$out" -name '*.yaml' | wc -l | tr -d ' ')"
if [ "$count" -ne 10 ]; then
  echo "FAIL: expected 10 CRDs in the $GATEWAY_API_VERSION standard bundle, found $count" >&2
  exit 1
fi
if grep -l 'ValidatingAdmissionPolic' "$out"/*.yaml >/dev/null 2>&1; then
  echo "FAIL: an admission policy leaked into the CRD set" >&2
  exit 1
fi

if [ "$check" -eq 1 ]; then
  diff -r "$out" "$dest" && echo "ok: vendored CRDs match the pinned upstream bundle"
  exit $?
fi
rm -f "$dest"/*.yaml
cp "$out"/*.yaml "$dest"/
echo "wrote $count CRDs ($GATEWAY_API_VERSION) to $dest"
