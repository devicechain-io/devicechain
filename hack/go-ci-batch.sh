#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Small-module executor. ci-go-batch.py passes ci-select's ORIGINAL leg payloads unchanged.
# Shared go-ci-test/race helpers are also used by standalone legs in ci.yml.
set -euo pipefail
cd "$GITHUB_WORKSPACE"
root="$PWD"
entries="${BATCH_ENTRIES:?BATCH_ENTRIES is required}"
jq -e 'type == "array" and all(.[]; (.module | type == "string") and
  (.static | type == "boolean") and .shard == "" and .exclude == "" and
  (.tests // "") == "" and (.skip_tests // "") == "" and
  (.race_scope == "all" or .race_scope == "changed" or .race_scope == "none"))' <<<"$entries" >/dev/null
if [ "$(jq length <<<"$entries")" -eq 0 ]; then
  echo "batch: no selected modules; skipped"
  exit 0
fi

rc=0
index=0
while IFS= read -r entry; do
  index=$((index + 1))
  module="$(jq -r .module <<<"$entry")"
  static="$(jq -r .static <<<"$entry")"
  dir="$RUNNER_TEMP/go-batch-$index"
  mkdir -p "$dir"
  failed=()
  echo "::group::$module"
  # Every check is attributed, and one member's failure never prevents later members from
  # running. Tidy still runs after failures, as !cancelled() does in the standalone job.
  check() {
    local label="$1" status=0
    shift
    echo "==> $module: $label"
    "$@" || status=$?
    if [ "$status" -ne 0 ]; then
      echo "::error::$module: $label failed (exit $status)"
      failed+=("$label")
      rc=1
    fi
  }
  export LEG_PKGS LEG_EXCLUDE LEG_SHARD DC_GOMOD_TIDY DC_GOMOD_EXTRA_GOOS
  LEG_PKGS="$(jq -r .pkgs <<<"$entry")"
  LEG_EXCLUDE=""
  LEG_SHARD=""
  DC_GOMOD_EXTRA_GOOS=""
  DC_GOMOD_TIDY=""
  if [ "$static" = true ]; then DC_GOMOD_TIDY=1; fi
  check "go mod download" bash "$root/hack/go-mod-download.sh" "$module"
  check "resolve packages" bash "$root/hack/go-leg.sh" "$module" "$dir/leg-args"
  cd "$root/$module"
  if [ "$static" = true ]; then
    check gofmt bash -euo pipefail -c 'out="$(gofmt -l .)"; if [ -n "$out" ]; then echo "These files are not gofmt-clean:"; echo "$out"; exit 1; fi'
    check "go build" go build ./...
    check "go vet" go vet ./...
    check "workspace sum" bash "$root/hack/check-go-work-sum.sh" --module "$module"
    check "go vet (build-tagged suites)" go vet -tags "integration interop" ./...
    check govulncheck bash "$root/hack/govulncheck.sh" "$module"
  fi
  if [ -s "$dir/leg-args" ]; then
    check "go test / silent-skip gate" bash "$root/hack/go-ci-test.sh" "$module" "$dir/leg-args" "$dir"
    export DC_RACE_SCOPE RACE_PKGS
    DC_RACE_SCOPE="$(jq -r .race_scope <<<"$entry")"
    RACE_PKGS="$(jq -r .race_pkgs <<<"$entry")"
    check "race detector" bash "$root/hack/go-ci-race.sh" "$module" "$dir/leg-args" "$dir"
  else
    echo "::error::$module: no resolved test arguments; refusing to pass untested"
    failed+=("test arguments")
    rc=1
  fi
  if [ "$static" = true ]; then
    check tidy bash "$root/hack/check-go-mod-tidy.sh" --module "$module"
  fi
  result=PASS
  if [ "${#failed[@]}" -gt 0 ]; then result="FAIL: ${failed[*]}"; fi
  echo "batch: $module: $result"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf -- '- **%s**: %s\n' "$module" "$result" >> "$GITHUB_STEP_SUMMARY"
  fi
  echo "::endgroup::"
  cd "$root"
done < <(jq -c '.[]' <<<"$entries")
exit "$rc"
