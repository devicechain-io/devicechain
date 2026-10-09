#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Lists deployable microservices (single source of truth for CI matrices).
set -euo pipefail
cd "$(dirname "$0")/.."
for d in backend/services/*/; do
  if [ -f "${d}main.go" ]; then basename "$d"; fi
done | jq -R . | jq -s -c .
