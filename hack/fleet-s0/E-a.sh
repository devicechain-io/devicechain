#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ ${1:-} == --self-test ]]; then exec bash "$here/self-test.sh" E-a; fi
exec bash "$here/run.sh" E-a "$@"
