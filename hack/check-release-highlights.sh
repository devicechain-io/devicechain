#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Is .github/release-highlights.json valid for the tag being released?
#
#   hack/check-release-highlights.sh <tag> [highlights.json]
#   hack/check-release-highlights.sh --self-test
#
# The highlights are the one part of the published release manifest a machine cannot
# derive — generated from commit subjects they would read like `fix(events): give
# payload rows and anchors identities of their own`, which is a developer's sentence,
# not a feature bullet. So a human writes them, and the failure mode of a human-written
# file in a release pipeline is that it is FORGOTTEN, publishing the previous release's
# highlights against this release's version. Confidently wrong is worse than absent.
#
# 🔴 WHY THIS IS A SCRIPT RATHER THAN TWO INLINE CHECKS. It had been written twice — once
# in the release workflow's `guard` job and once in build-release-manifest.sh — with a
# comment on the second saying the repetition was deliberate, so that running the script
# by hand could not quietly produce a mismatched manifest. That reasoning was right and
# the duplication still cost a release run: relaxing the rule to accept a release
# CANDIDATE was applied to the workflow copy only, so `v0.11.0-rc.1` passed the first job,
# built every image, passed the load gate, and then died in publish-manifest against the
# copy nobody had changed. Two guards that must agree are one guard with two callers.
#
# THE RULE. A release candidate carries the same highlights as the release it is a
# candidate for, so `v0.11.0-rc.1` is satisfied by highlights written for `v0.11.0`.
# Demanding an exact match would force the file to be edited to the rc version and then
# edited back, and the failure mode of that dance is shipping the STABLE release with
# `-rc.N` still in the manifest — the precise outcome this guard exists to prevent. The
# BASE version must still match exactly, so a forgotten update is caught on the rc, which
# is the earliest anyone could catch it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Prints one line per problem; empty output is the passing case.
check() {
  local tag="$1" hl="$2"
  local base="${tag%%-*}"

  if [ ! -f "$hl" ]; then
    echo "highlights file not found: $hl"
    return 0
  fi

  local version
  version="$(jq -r '.version // empty' "$hl" 2>/dev/null || true)"
  if [ -z "$version" ]; then
    echo "$hl has no .version"
    return 0
  fi
  if [ "$version" != "$tag" ] && [ "$version" != "$base" ]; then
    echo "$hl is for '$version' but this release is '$tag'"
    return 0
  fi

  # A file carrying the right version and no content is the same forgotten-update
  # failure wearing a different hat.
  local count
  count="$(jq -r '.highlights | length' "$hl" 2>/dev/null || echo 0)"
  if [ "$count" -eq 0 ]; then
    echo "$hl carries no highlights for $tag"
  fi
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must accept a candidate and still reject a stale file"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  hl="$tmp/hl.json"

  write() { printf '{"version":"%s","breaking":false,"highlights":%s}\n' "$1" "${2:-[\"a\"]}" > "$hl"; }
  expect() { # <label> <tag> <want: ok|fail>
    local out; out="$(check "$2" "$hl")"
    if [ "$3" = "ok" ] && [ -n "$out" ]; then
      echo "  FAIL: $1 — expected acceptance, got: $out" >&2; exit 1
    fi
    if [ "$3" = "fail" ] && [ -z "$out" ]; then
      echo "  FAIL: $1 — expected rejection, got silence" >&2; exit 1
    fi
    echo "  ok: $1"
  }

  write v0.11.0
  expect "a stable tag matching its highlights"            v0.11.0      ok
  expect "an rc satisfied by the release's highlights"     v0.11.0-rc.1 ok
  expect "a DIFFERENT release is rejected"                 v0.12.0      fail
  # A patch inheriting the previous minor's highlights is the forgotten update, and the
  # base-version rule must not launder it: v0.11.1's base is v0.11.1, not v0.11.0.
  expect "a patch cannot inherit stale highlights"         v0.11.1      fail

  write v0.11.0-rc.1
  expect "an rc file matching its own rc tag"              v0.11.0-rc.1 ok
  # 🔴 The dangerous direction, and the whole reason the rule is base-aware rather than
  # prefix-loose: the STABLE release must not ship a manifest that still says -rc.N.
  expect "a stable tag REJECTS leftover rc highlights"     v0.11.0      fail

  write v0.11.0 '[]'
  expect "the right version with no highlights"            v0.11.0      fail

  rm -f "$hl"
  expect "a missing file"                                  v0.11.0      fail

  echo "==> Self-test passed"
  exit 0
fi

TAG="${1:?usage: check-release-highlights.sh <tag> [highlights.json]}"
HL="${2:-$ROOT/.github/release-highlights.json}"

findings="$(check "$TAG" "$HL")"
if [ -n "$findings" ]; then
  echo "::error::$findings. Update .github/release-highlights.json (highlights + breaking) before tagging." >&2
  exit 1
fi

echo "ok: release highlights are valid for $TAG"
