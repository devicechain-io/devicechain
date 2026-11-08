#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The generated vocabulary tables must be exactly what the pinned inputs produce.
#
# backend/core/semantic/packs/*_gen.go, backend/core/semantic/contexts/ and
# contexts_gen.go are written by backend/tools/vocabgen from the files pinned in
# hack/vocab/sources.lock. This regenerates them in memory and fails if anything
# on disk differs, so a hand edit to a generated table, or a lock change that was
# not followed by a regeneration, cannot merge.
#
# It downloads the pinned inputs (about 5 MB) into a cache and verifies every
# one against its SHA-256 before use; an upstream file that changed under a
# pinned URL fails the run on the digest, never silently. It needs the network,
# so it is a manual gate (run it whenever the lock, vocabgen or a generated file
# changes), not part of the offline sweep.
#
#   hack/check-vocab-gen.sh              # verify
#   hack/check-vocab-gen.sh --regen      # rewrite the generated files

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cache="${VOCABGEN_CACHE:-${TMPDIR:-/tmp}/dc-vocabgen-cache}"
mode=-check
[ "${1:-}" = "--regen" ] && mode=

cd "$root/backend/tools/vocabgen"
# shellcheck disable=SC2086
go run . -root "$root" -cache "$cache" -fetch $mode
