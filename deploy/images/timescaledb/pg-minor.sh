#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Derive the PostgreSQL minor ("17.10") from a PG_IMAGE reference.
#
#   pg-minor.sh ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm@sha256:<hex>
#   pg-minor.sh --self-test
#
# The reference is `name:tag` or, once digest-pinned, `name:tag@sha256:<hex>`.
# The digest is stripped FIRST: a bare `${ref##*:}` returns the digest hex for a
# pinned reference, which would publish a tag like `sha256...-ts2.28.3`. Both
# the publish workflow and hack/check-tofu-validations.sh call this, so there is
# one formula to get wrong rather than two copies to keep in step.
#
# Prints the minor and exits 0; prints nothing and exits 1 if no `N.N` tag.

set -euo pipefail

pg_minor() {
  local ref="${1%%@*}" tag
  # A registry port ("host:5000/img") has a colon too; the tag is only what
  # follows the last colon when no slash comes after it.
  case "${ref##*:}" in */*) return 1 ;; esac
  [[ $ref == *:* ]] || return 1
  tag="${ref##*:}"
  tag="${tag%%-*}"
  [[ $tag =~ ^[0-9]+\.[0-9]+$ ]] || return 1
  printf '%s\n' "$tag"
}

self_test() {
  local fail=0 digest
  digest="sha256:$(printf 'a%.0s' {1..64})"
  expect() { # want ref
    local got
    got="$(pg_minor "$2" || true)"
    if [[ $got != "$1" ]]; then
      echo "FAIL: pg_minor '$2' = '$got', want '$1'" >&2
      fail=1
    fi
  }
  expect 17.10 "ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm"
  expect 17.10 "ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm@${digest}"
  expect 17.10 "registry.example:5000/pg:17.10-standard-bookworm@${digest}"
  expect 16.4 "pg:16.4"
  # Must refuse, not guess: no tag, digest only, a floating tag, a port-only ref.
  expect "" "ghcr.io/cloudnative-pg/postgresql"
  expect "" "ghcr.io/cloudnative-pg/postgresql@${digest}"
  expect "" "ghcr.io/cloudnative-pg/postgresql:latest@${digest}"
  expect "" "registry.example:5000/pg"
  [[ $fail -eq 0 ]] && echo "pg-minor self-test: ok"
  return "$fail"
}

if [[ ${1:-} == --self-test ]]; then
  self_test
else
  [[ $# -eq 1 ]] || { echo "usage: pg-minor.sh <PG_IMAGE> | --self-test" >&2; exit 2; }
  pg_minor "$1"
fi
