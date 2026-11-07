#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# check-reproducible-build.sh — the shipped binaries must be a function of the source, not of
# the machine, the checkout path or the moment they were built.
#
# WHAT THIS CHECKS. The committed tree (git archive HEAD, so uncommitted edits never count) is
# unpacked into TWO different directories, and each program is built from each with a fresh,
# separate Go build cache. The two SHA-256 digests must match. Different directories prove
# -trimpath is doing its job; separate caches prove nothing was carried over from the first
# build; separate unpack times prove no mtime or clock leaks into the output.
#
# 🔴 THE NEGATIVE CONTROL is built in. --self-test plants a build-time nondeterminism (a string
# that differs per build, referenced from init so the linker keeps it) into the SECOND copy only
# and requires the comparison to FAIL. A check that passes the real run but cannot see a planted
# defect is worth nothing.
#
#   hack/check-reproducible-build.sh                       # dcctl + edge agent
#   hack/check-reproducible-build.sh backend/cli           # chosen module directories
#   hack/check-reproducible-build.sh --self-test           # prove the check can fail
#
# Requires go, git, tar and a sha256 tool. Builds the linux/amd64 artifacts the release ships
# (REPRO_GOOS / REPRO_GOARCH override), through the workspace as the repository does, with -p 2
# (REPRO_P) so a laptop is not saturated.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
default_targets=(backend/cli backend/edge/dc-edge-agent)

sha() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

for tool in go git tar; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: $tool is required" >&2; exit 2; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# unpack <dest> — the committed tree, nothing else.
unpack() {
  mkdir -p "$1"
  git -C "$root" archive HEAD | tar -x -C "$1"
}

# build <tree> <module-dir> <cache-name> <out> — one cold build of one program.
build() {
  local tree="$1" mod="$2" cache="$3" out="$4"
  ( cd "$tree/$mod" \
    && CGO_ENABLED=0 GOOS="${REPRO_GOOS:-linux}" GOARCH="${REPRO_GOARCH:-amd64}" \
       GOFLAGS="-trimpath -buildvcs=false" \
       GOCACHE="$work/cache-$cache" GOMAXPROCS="${GOMAXPROCS:-2}" \
       go build -p "${REPRO_P:-2}" -ldflags="-s -w -buildid=" -o "$out" . )
}

# plant <tree> <module-dir> — a per-build-different string the linker cannot drop.
plant() {
  cat > "$1/$2/zz_planted_nondeterminism.go" <<GO
package main

import "os"

var plantedStamp = "$(date +%s%N)-$RANDOM-$RANDOM"

func init() {
	if len(os.Args) > 1<<20 {
		println(plantedStamp)
	}
}
GO
}

# compare <plant:0|1> <module-dir...> — echoes one line per program; returns 1 on any mismatch.
compare() {
  local planted="$1"; shift
  local rc=0 mod a b
  rm -rf "$work/a" "$work/b" "$work"/cache-*
  unpack "$work/a/one"
  sleep 1 # distinct unpack time: an mtime or clock leak must have a chance to show
  unpack "$work/b/two-longer-path"
  for mod in "$@"; do
    [ "$planted" = 1 ] && plant "$work/b/two-longer-path" "$mod"
    build "$work/a/one" "$mod" a "$work/out-a" || { echo "FAIL: build of $mod (a) failed" >&2; return 2; }
    build "$work/b/two-longer-path" "$mod" b "$work/out-b" || { echo "FAIL: build of $mod (b) failed" >&2; return 2; }
    a="$(sha "$work/out-a")"; b="$(sha "$work/out-b")"
    if [ "$a" = "$b" ]; then
      echo "reproducible: $mod  $a"
    else
      echo "DIFFERS: $mod  $a != $b"
      rc=1
    fi
  done
  return "$rc"
}

if [ "${1:-}" = "--self-test" ]; then
  # one small program is enough to see the planted defect
  st=(backend/edge/dc-edge-agent)
  echo "== control: unmodified tree must reproduce"
  compare 0 "${st[@]}" || { echo "SELF-TEST FAIL: the clean tree did not reproduce" >&2; exit 1; }
  echo "== negative control: planted nondeterminism must be caught"
  # Exactly 1 (digests differ). A build failure is 2 and must not count as "caught".
  prc=0; compare 1 "${st[@]}" || prc=$?
  if [ "$prc" != 1 ]; then
    echo "SELF-TEST FAIL: planted nondeterministic build gave status $prc, want 1 (digests differ)" >&2
    exit 1
  fi
  echo "self-test ok"
  exit 0
fi

targets=("$@")
[ "${#targets[@]}" -gt 0 ] || targets=("${default_targets[@]}")
compare 0 "${targets[@]}"
