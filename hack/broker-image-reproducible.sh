#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# broker-image-reproducible.sh — the broker image must be a function of the broker's own inputs,
# not of the commit it was built at.
#
# WHY. The broker is pulled by its image, so a new image is a broker roll: every server restarts,
# and MQTT sessions reconnect. A release that changed nothing under backend/broker/dc-nats-server
# must therefore publish the SAME image. Two things in a default Go build break that, and the
# broker is built without both everywhere it is built (backend/cli/bootstrap/brokerimage.go
# brokerBuildEnv, and the release and CI workflows):
#
#   - the VCS stamp (vcs.revision, vcs.time, vcs.modified in the build info), which moves with
#     every commit — and which nats-server also reads as its git_commit, so a DeviceChain commit
#     would appear in the broker's varz and INFO. Off with -buildvcs=false.
#   - workspace mode, in which the broker links whatever versions the whole go.work selects, so a
#     dependency bump in an unrelated module changes it. Off with GOWORK=off.
#
# WHAT THIS CHECKS. The module is copied into a fresh git repository of its own (so the stamp a
# build would take is real and this script never commits to yours), built, then an EMPTY commit
# is made — every VCS stamp moves, no file does — and built again: the two images must be the
# same. Then, 🔴 the negative control, the second commit is built once more WITHOUT
# -buildvcs=false, and that image must DIFFER; a check that cannot see a stamp would pass the
# first half for the wrong reason.
#
# Builds through hack/ko-build.sh, so the base is the one .ko.yaml pins. Requires ko, docker, go
# and git.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
module="$root/backend/broker/dc-nats-server"
for tool in ko docker go git; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: $tool is required" >&2; exit 2; }
done

work="$(mktemp -d)"
repo="dc-broker-repro-$$"
built=()
cleanup() {
  local r
  for r in "${built[@]+"${built[@]}"}"; do
    docker rmi "$r" >/dev/null 2>&1 || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

cp -R "$module" "$work/m"
git -C "$work/m" init -q
git -C "$work/m" add -A
git -C "$work/m" -c user.name=repro -c user.email=repro@invalid commit -q -m "first"

# build <env...> — prints the image ID of a local ko build of the copy, with that environment.
build() {
  local out ref
  out="$(cd "$work/m" && env "$@" KO_DOCKER_REPO="ko.local/$repo" "$root/hack/ko-build.sh" --bare --local --platform=linux/amd64 ./)"
  ref="${out##*$'\n'}"
  built+=("$ref")
  docker image inspect -f '{{.Id}}' "$ref"
}

reproducible=(GOWORK=off GOFLAGS=-buildvcs=false)

first="$(build "${reproducible[@]}")"
git -C "$work/m" -c user.name=repro -c user.email=repro@invalid commit -q --allow-empty -m "second, changing nothing"
second="$(build "${reproducible[@]}")"
stamped="$(build GOWORK=off)"

echo "first commit:                    $first"
echo "second commit:                   $second"
echo "second commit, VCS stamp kept:   $stamped"

if [ "$first" != "$second" ]; then
  echo "FAIL: two commits that change nothing under the broker built different images; every release would roll the broker" >&2
  exit 1
fi
if [ "$stamped" = "$second" ]; then
  echo "FAIL: NEGATIVE CONTROL: the build that keeps the VCS stamp produced the same image, so this check cannot see a stamp and proves nothing" >&2
  exit 1
fi
echo "OK: the broker image is the same at both commits, and the stamped build differs"
