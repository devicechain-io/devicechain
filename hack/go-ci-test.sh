#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Shared by standalone Go legs and small-module batches; keep one test/skip-gate command.
set -euo pipefail
module="$1"
args_file="$2"
output_dir="$3"
mkdir -p "$output_dir"
cd "$GITHUB_WORKSPACE/$module"
export DC_REQUIRE_TAGS=1
mapfile -t args < "$args_file" || exit 1
output="$output_dir/go-test.json"
set +e
go test -json -count=1 -timeout 20m "${args[@]}" | tee "$output" | jq -rj 'select(.Action == "output") | .Output // empty'
statuses=("${PIPESTATUS[@]}")
set -e
gate_status=0
bash "$GITHUB_WORKSPACE/hack/check-no-silent-skips.sh" "$output" || gate_status=$?
for status in "${statuses[@]}"; do
  if [ "$status" -ne 0 ]; then exit "$status"; fi
done
exit "$gate_status"
