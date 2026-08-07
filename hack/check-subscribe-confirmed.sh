#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs the subconfirm analyzer (backend/tools/subconfirm) over every workspace
# module: a subscription the broker has never confirmed, or an MQTT SUBACK whose
# result is never read.
#
# 🔴 Why this is an analyzer behind a script, and not a grep in this file.
#
# `.Subscribe(` matches at least four unrelated APIs in this repo — nats.Conn,
# paho's mqtt.Client, nats.JetStreamContext, and our own GraphQL-over-WebSocket
# client — and only two of them have the problem. The first attempt to inventory
# the sites textually got the count wrong in BOTH directions: it counted a
# JetStream subscribe (confirmed by construction) and a line inside a comment as
# defects, and it had no way to see that the receiver's type was the only thing
# that mattered. A grep narrowed with `nc.` or `conn.` would just be a guess about
# variable names. Only a type checker can tell these apart, so the guard is a
# go/analysis pass and this script is the thing that runs it everywhere.
#
# Suppressing a report is deliberate and requires a reason — see the package
# comment in backend/tools/subconfirm/analyzer for the directive and when it is
# the right answer.
#
#   hack/check-subscribe-confirmed.sh
#   hack/check-subscribe-confirmed.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/subconfirm"

# Built from source every run rather than installed. A checked-in binary, or one
# pinned to a released version, is a guard that stops tracking the rules it is
# supposed to enforce the moment somebody edits the analyzer.
go build -o "$BIN" ./backend/tools/subconfirm/cmd/subconfirm

# ---------------------------------------------------------------------------
# check: run the analyzer over every workspace module.
# ---------------------------------------------------------------------------
# The tool's own exit status is the part that matters: singlechecker exits 3 for
# diagnostics and 1 for a package it could not load. Collapsing those two would
# let a module that stopped building report as clean — the exact "gate that cannot
# fail" shape this repo has been bitten by before.
#
# 🔴 `|| status=$?` is the only spelling of this that works, and the two that do
# not both shipped here first:
#
#   - a `set +e` / `set -e` pair around a helper that re-enabled -e itself, which
#     aborted the loop at the first module WITH a finding. Every later module went
#     unchecked. It still exited non-zero, so reading only the exit code would
#     have called it working.
#   - `if ! out=$(...); then status=$?; fi`, which looks like the careful version
#     and is strictly worse: inside the branch `$?` is the status of `! cmd`, so
#     it is always 0. Every finding was printed and the script exited clean — a
#     gate that cannot fail, found only by re-running the negative control after
#     changing the instrument.
check() {
  local rc=0 dir out status
  for dir in $(go list -m -f '{{.Dir}}'); do
    status=0
    out="$(cd "$dir" && "$BIN" ./... 2>&1)" || status=$?
    if [ -n "$out" ]; then
      printf '%s\n' "$out"
    fi
    case "$status" in
      0) ;;
      3) rc=1 ;;
      *)
        echo "ERROR: the analyzer could not load ${dir#"$ROOT"/} (exit $status). That is not a" >&2
        echo "       pass — nothing in that module was checked." >&2
        rc=1
        ;;
    esac
  done
  if [ "$rc" -ne 0 ]; then
    echo >&2
    echo "FAILED: see backend/tools/subconfirm/analyzer for what satisfies this check." >&2
  fi
  return "$rc"
}

# ---------------------------------------------------------------------------
# Self-test.
# ---------------------------------------------------------------------------
# It plants a package inside backend/core rather than in a scratch module, and
# that is the whole point: the analyzer matches on the DECLARING PACKAGE PATH of
# the receiver's type, so it is only really exercised against the real nats.go and
# the real paho. A hand-written stub would prove the analyzer agrees with the
# stub. The unit tests in the analyzer package use stubs deliberately and cover
# the discrimination cases; this covers the one thing they cannot.
#
# 🔴 It checks BOTH directions. "The checker reports a bad subscribe" is satisfied
# by a checker that reports everything, which is why the second half — the same
# file written correctly, reported by nothing — is not optional.
self_test() {
  local pkg
  pkg="$(mktemp -d "$ROOT/backend/core/subconfirm_selftest_XXXXXX")"
  trap 'rm -rf "$TMP" "$pkg"' EXIT

  echo "==> Self-test: an unconfirmed subscribe must be reported"
  cat > "$pkg/planted.go" <<'EOF'
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package planted

import (
	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
)

func natsBare(nc *nats.Conn, h nats.MsgHandler) {
	nc.Subscribe("selftest", h)
}

func mqttBare(c mqtt.Client, h mqtt.MessageHandler) {
	token := c.Subscribe("selftest", 1, h)
	token.Wait()
}
EOF

  local out status=0
  out="$(cd "$ROOT/backend/core" && "$BIN" "./$(basename "$pkg")/" 2>&1)" || status=$?
  if [ "$status" -ne 3 ]; then
    echo "SELF-TEST FAILED: the analyzer exited $status (want 3) on a planted bare subscribe." >&2
    echo "$out" >&2
    return 1
  fi
  # Both rules, not just whichever one fires first: they match different packages
  # and a regression in one is invisible behind the other.
  local n
  n="$(printf '%s\n' "$out" | grep -c 'core NATS DROPS a publish')"
  [ "$n" -eq 1 ] || { echo "SELF-TEST FAILED: NATS rule fired $n times, want 1" >&2; echo "$out" >&2; return 1; }
  n="$(printf '%s\n' "$out" | grep -c 'paho never reports a REFUSAL')"
  [ "$n" -eq 1 ] || { echo "SELF-TEST FAILED: MQTT rule fired $n times, want 1" >&2; echo "$out" >&2; return 1; }
  echo "    both rules fired"

  echo "==> Self-test: the same code written correctly must be reported by nothing"
  cat > "$pkg/planted.go" <<'EOF'
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package planted

import (
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
)

func natsConfirmed(nc *nats.Conn, h nats.MsgHandler) error {
	_, err := messaging.SubscribeSynced(nc, "selftest", h)
	return err
}

func mqttConfirmed(c mqtt.Client, h mqtt.MessageHandler) error {
	return messaging.SubscribeMqttConfirmed(c, "selftest", 1, h, 5*time.Second)
}
EOF

  status=0
  out="$(cd "$ROOT/backend/core" && "$BIN" "./$(basename "$pkg")/" 2>&1)" || status=$?
  if [ "$status" -ne 0 ]; then
    echo "SELF-TEST FAILED: the analyzer exited $status (want 0) on correctly written code." >&2
    echo "$out" >&2
    return 1
  fi
  echo "    clean"

  rm -rf "$pkg"
  trap 'rm -rf "$TMP"' EXIT
  echo "==> Self-test passed."
}

case "${1:-}" in
  "")           check ;;
  --self-test)  self_test ;;
  *)            usage ;;
esac
