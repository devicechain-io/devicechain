#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Run a command with bounded retries and backoff: ci-retry.sh <command> [args...]
#
# For CI steps whose only failure mode worth retrying is a download from a host that
# is having an outage (a GitHub release-asset 504 behind `tofu init` or setup-envtest).
# Tries CI_RETRY_ATTEMPTS times (default 4), sleeping 5s, 15s, 45s, capped at 60s.
# The final failure is the command's own exit status, so a real error still fails the step.
set -u
attempts="${CI_RETRY_ATTEMPTS:-4}"
delay=5
n=1
while :; do
  "$@" && exit 0
  rc=$?
  if [ "$n" -ge "$attempts" ]; then
    echo "::error::'$*' failed after $n attempts (last status $rc)" >&2
    exit "$rc"
  fi
  echo "::warning::attempt $n/$attempts of '$*' failed (status $rc); retrying in ${delay}s" >&2
  sleep "$delay"
  n=$((n + 1))
  delay=$((delay * 3))
  [ "$delay" -gt 60 ] && delay=60
done
