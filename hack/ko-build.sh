#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The ONE way DeviceChain runs `ko build`: ko with the repo's .ko.yaml, unconditionally.
#
# ko finds .ko.yaml in its WORKING DIRECTORY only, and every build runs from a module
# directory, so a bare `ko build` there silently ignores the pinned base image and builds
# on whatever its tag resolves to that day. Routing every invocation through this wrapper
# makes "forgot KO_CONFIG_PATH" impossible rather than something to remember; the lint in
# hack/check-ko-base-used.sh refuses a bare ko anywhere else. (dcctl does the same in Go,
# backend/cli/bootstrap/steps.go.)
#
# Usage: ko-build.sh <ko build args...>   (run it from the module directory to build)

set -euo pipefail

root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
KO_CONFIG_PATH="$root/.ko.yaml"
[ -f "$KO_CONFIG_PATH" ] || { echo "::error::$KO_CONFIG_PATH not found" >&2; exit 1; }
export KO_CONFIG_PATH
exec ko build "$@"
