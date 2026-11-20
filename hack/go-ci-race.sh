#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Shared by standalone Go legs and small-module batches; keep one race/verdict command.
set -euo pipefail
MODULE="$1"
args_file="$2"
output_dir="$3"
mkdir -p "$output_dir"
cd "$GITHUB_WORKSPACE"
case "$DC_RACE_SCOPE" in
  all) mapfile -t args < "$args_file" ;;
  changed)
    # The plan reads packages from the source, tags and all, so a package whose every
    # file a build constraint excludes on this runner (an integration-only or
    # frontend-only directory) can be named, and `go test` refuses one outright. Those
    # are dropped. Any OTHER failure to resolve a package is doubt, and doubt races the
    # whole leg.
    read -r -a want <<<"$RACE_PKGS"
    if js="$(cd "$MODULE" && go list -e -json "${want[@]}")" \
       && [ "$(jq -s '[.[] | select(.Error and (.Error.Err | test("build constraints exclude all Go files") | not))] | length' <<<"$js")" = 0 ]; then
      mapfile -t args < <(jq -rs '.[] | select(.Error | not) | .ImportPath' <<<"$js")
      [ "${#args[@]}" -gt 0 ] || DC_RACE_SCOPE=none
    else
      echo "::warning::could not resolve the race packages ($RACE_PKGS); racing the whole leg"
      DC_RACE_SCOPE=all
      mapfile -t args < "$args_file"
    fi
    export DC_RACE_SCOPE
    ;;
  none) args=() ;;
  *) echo "::error::race_scope is '$DC_RACE_SCOPE', not all, changed or none"; exit 1 ;;
esac
hack/go-race.sh "$MODULE" "${args[@]}" 2>&1 | tee "$output_dir/go-race.log"
# Fixed strings, not a pattern: the module path is data, and a regex
# here would only be one metacharacter away from matching the wrong
# module or nothing at all.
if grep -qF "race: COVERED $MODULE " "$output_dir/go-race.log" \
   || grep -qF "race: NOT COVERED $MODULE " "$output_dir/go-race.log"; then
  :
elif [ "$GITHUB_EVENT_NAME" = pull_request ] \
   && { grep -qF "race: PARTIAL $MODULE " "$output_dir/go-race.log" || grep -qF "race: SKIPPED $MODULE " "$output_dir/go-race.log"; }; then
  :
else
  echo "::error::the race step reached the end without recording a verdict this $GITHUB_EVENT_NAME run may accept for $MODULE. Its green tick would mean nothing, so it fails instead."
  exit 1
fi
