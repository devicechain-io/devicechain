#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Prints the repo-relative directory of every module in go.work, one per line. This is the
# module list for anything that must cover the whole workspace: it is read from go.work
# itself, so a module added to the workspace is covered without anyone editing a list, and
# the broker, the broker administrator, deploy and the OTA reference device are not
# silently outside it (a hardcoded glob of backend/services/*/ and friends omits all four).
#
# Fails, rather than printing nothing, when go.work yields no module: an empty list would let
# a loop over it scan zero modules and report green.
set -euo pipefail
out="$(go work edit -json | jq -r '.Use[].DiskPath' | sed -e 's#^\./##' -e 's#/$##')"
[ -n "$out" ] || { echo "workspace-modules: go.work lists no modules" >&2; exit 1; }
printf '%s\n' "$out"
