#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Make the TimescaleDB operand image the tree points at AVAILABLE to a kind
# cluster, whether or not it has been published yet.
#
#   hack/ensure-operand-image.sh <kind-cluster-name>
#   hack/ensure-operand-image.sh --self-test
#
# THE DEADLOCK THIS REMOVES. An operand bump (versions.conf + the tofu default,
# which hack/check-tofu-validations.sh forces to move together) cannot pass a CI
# drill that installs an instance, because the new tag is only PUBLISHED by
# operand-image.yml on a push to main -- after the PR merged. The drill pulled a
# tag that did not exist yet and died in ImagePullBackOff, on every PostgreSQL
# minor.
#
#   published   -> do nothing; the cluster pulls it, exactly as an operator would.
#                  The common case, and it costs one manifest probe.
#   unpublished -> build it from deploy/images/timescaledb with build.sh (the same
#                  versions.conf-to-build-args path the publish workflow's smoke job
#                  uses), tag it with the EXACT reference the chart/tofu will ask
#                  for, and `kind load` it into every node.
#   unknown     -> FAIL. Anything but a clear "no such manifest" (rate limit, auth,
#                  transport) is not evidence the tag is free, and falling through
#                  to a build would let a drill pass on an image that differs from
#                  the one an operator pulls. A failed build fails too; it never
#                  falls through to the pull.
#
# The CNPG Cluster is rendered without an imagePullPolicy
# (deploy/opentofu/modules/cnpg-cluster/chart), and CloudNativePG's default for it
# is IfNotPresent, so a preloaded image is used without contacting the registry.
# The check at the end proves each node actually holds the ref rather than
# assuming the load took.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
OPERAND_DIR="$REPO_ROOT/deploy/images/timescaledb"

say() { printf '\n==> %s\n' "$*"; }

# classify_probe <exit-code> <combined output>
# Prints published | unpublished | unknown. Pure, so --self-test can drive it.
classify_probe() {
  local rc="$1" out="$2"
  if [[ $rc -eq 0 ]]; then
    echo published
    return
  fi
  # What registries say for a tag that does not exist. Anything else (denied,
  # timeout, 429, EOF) is unknown on purpose.
  case "$out" in
    *"manifest unknown"* | *"no such manifest"* | *"MANIFEST_UNKNOWN"*) echo unpublished ;;
    *) echo unknown ;;
  esac
}

self_test() {
  local bad=0
  expect() { # want rc out
    local got
    got="$(classify_probe "$2" "$3")"
    if [[ $got != "$1" ]]; then
      echo "FAIL: classify_probe $2 '$3' = $got, want $1" >&2
      bad=1
    fi
  }
  expect published 0 '{"schemaVersion":2}'
  expect unpublished 1 'manifest unknown'
  expect unpublished 1 'no such manifest: ghcr.io/x:y'
  expect unknown 1 'denied: requested access to the resource is denied'
  expect unknown 1 'Get "https://ghcr.io/v2/": dial tcp: i/o timeout'
  expect unknown 1 'toomanyrequests: rate limit'
  expect unknown 1 ''
  [[ $bad -eq 0 ]] || exit 1
  echo "ensure-operand-image self-test: ok"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit 0
fi

cluster="${1:-}"
[[ -n $cluster ]] || { echo "usage: ensure-operand-image.sh <kind-cluster-name> | --self-test" >&2; exit 2; }

ref="$("$OPERAND_DIR/operand-ref.sh")"
say "operand image the tree points at: $ref"

state=unknown
for attempt in 1 2 3; do
  rc=0
  out="$(docker manifest inspect "$ref" 2>&1)" || rc=$?
  state="$(classify_probe "$rc" "$out")"
  [[ $state != unknown ]] && break
  echo "probe $attempt/3 was inconclusive: ${out:0:300}" >&2
  sleep $((attempt * 5))
done

case "$state" in
  published)
    say "OPERAND PATH: PUBLISHED -- $ref exists in the registry; the cluster will pull it. Nothing built."
    exit 0
    ;;
  unpublished)
    say "OPERAND PATH: UNPUBLISHED -- $ref is not in the registry yet (an operand bump PR is published only after it merges). Building it from the tree and loading it into kind cluster '$cluster'."
    ;;
  *)
    echo "::error::could not tell whether $ref is published (the registry probe was inconclusive 3 times). Refusing to guess: a guess either pulls an image this tree does not describe or builds one an operator would never run." >&2
    exit 1
    ;;
esac

# build.sh is the single versions.conf -> build-args path. Any failure here is
# the job's failure; there is deliberately no fallback to the pull.
"$OPERAND_DIR/build.sh" "$ref"

say "loading $ref into every node of kind cluster '$cluster'"
kind load docker-image "$ref" --name "$cluster"

# Prove it: every node must hold the exact ref, or the pod would pull and fail.
nodes="$(kind get nodes --name "$cluster")"
[[ -n $nodes ]] || { echo "::error::kind cluster '$cluster' has no nodes" >&2; exit 1; }
for n in $nodes; do
  docker exec "$n" crictl inspecti "$ref" >/dev/null ||
    { echo "::error::node $n does not hold $ref after kind load" >&2; exit 1; }
  echo "    node $n holds $ref"
done
say "OPERAND PATH: UNPUBLISHED -- $ref built locally and loaded into $cluster"
