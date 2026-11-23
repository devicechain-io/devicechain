#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Prints the newest STABLE release tag (vX.Y.Z, no prerelease suffix) that does not point at HEAD,
# for the fleet-operation foundation gate (backend/services/device-management/model/
# fleet_operation_gate_test.go). That test asks "does the release this build upgrades from carry the
# device attribute writer protocol?" once the database guard is in the schema, and it normally finds
# the baseline with `git tag --merged HEAD`. A shallow CI clone fetches every tag but none of their
# history, so that finds nothing; this script reads the tag names instead.
#
# A tag that points at HEAD is the release under test and must never witness its own foundation, so
# it is skipped here exactly as the test skips it.
#
# Used as:  echo "DC_BASELINE_TAG=$(hack/fleet-baseline-tag.sh)" >> "$GITHUB_ENV"
# Prints nothing and exits 1 when there is no such tag, which the gate then reports by name.
set -euo pipefail

self="$(git tag --points-at HEAD || true)"
tag="$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname |
  grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' |
  grep -vxF -f <(printf '%s\n' "$self") |
  head -n 1 || true)"
if [ -z "$tag" ]; then
  echo "no stable release tag is visible; fetch tags (actions/checkout fetch-tags: true)" >&2
  exit 1
fi
printf '%s\n' "$tag"
