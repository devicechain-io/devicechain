#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Exercise the OpenTofu root's variable `validation` blocks against real values.
#
# WHY THIS EXISTS
#
# `tofu validate` does NOT run validation blocks. It type-checks the configuration
# and evaluates variables at their DEFAULTS, so a validation block can be
# malformed, inverted, or reference the wrong variable and still pass every gate
# this repo runs. The condition is only evaluated when a non-default value is
# supplied — i.e. exactly when a human is deviating from the shipped topology,
# which is precisely when the guard is supposed to speak up and the worst time to
# discover it never worked.
#
# This matters most for nats_cluster_replicas (ADR-020 A0). It is the escape hatch
# for representing a topology `var.ha` cannot, so the values it accepts and refuses
# ARE the supported set — an even count that slipped through would provision a
# cluster whose RAFT majority tolerates no more failures than the odd size below
# it, at the cost of an extra server and a wider quorum on every write, and nothing
# downstream would notice.
#
# `tofu console` is the cheapest way to force variable evaluation without a
# cluster, a backend, or configured providers. Note it exits 0 even when a
# validation fails, so the check is on the emitted diagnostic, not the exit code.
#
# WHAT IT DOES AND DOES NOT DISTINGUISH — established by mutation, not assumed.
# nats_cluster_replicas is guarded TWICE: once on the root variable and again on
# modules/nats's own, so a direct consumer of the module is covered too. This
# script asserts what the ROOT ENTRY POINT does with a value, and does not care
# which block refuses it — gutting the root's condition alone leaves every
# assertion here passing, because the module's then fires (checked: the diagnostic
# moves from variables.tf to modules/nats/main.tf). Gutting BOTH fails it.
#
# That is deliberate rather than a gap: what an operator experiences is whether
# `tofu apply -var nats_cluster_replicas=4` is refused, not which file refused it.
# But do not read a pass here as evidence that a particular block is correct. If
# you change one of the two, change it knowing this script cannot tell you that
# you broke it — only that you broke both.
#
# Usage:
#   hack/check-tofu-validations.sh
#
# Requires tofu (or terraform) on PATH and a completed `tofu init -backend=false`
# in deploy/opentofu; it runs the init itself if the working directory is cold.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tofu_dir="$repo_root/deploy/opentofu"

TF="${TF:-}"
if [[ -z "$TF" ]]; then
  if command -v tofu >/dev/null 2>&1; then
    TF=tofu
  elif command -v terraform >/dev/null 2>&1; then
    TF=terraform
  else
    echo "FAIL: neither tofu nor terraform is on PATH" >&2
    exit 1
  fi
fi

cd "$tofu_dir"
if [[ ! -d .terraform ]]; then
  "$TF" init -backend=false >/dev/null
fi

failures=0

# rejects <variable> <value> — the value must trip a validation block.
rejects() {
  local var="$1" value="$2" out
  out="$("$TF" console -var "$var=$value" </dev/null 2>&1 || true)"
  if grep -q "Invalid value for variable" <<<"$out"; then
    echo "ok    $var=$value rejected"
  else
    echo "FAIL  $var=$value was ACCEPTED; no validation block on this path covers it" >&2
    failures=$((failures + 1))
  fi
}

# accepts <variable> <value> — the counterweight. A validation that rejects
# everything is not a guard, it is an outage, and it would pass every "rejects"
# assertion above on its own.
accepts() {
  local var="$1" value="$2" out
  out="$(echo "var.$var" | "$TF" console -var "$var=$value" 2>&1 || true)"
  if grep -q "Invalid value for variable" <<<"$out"; then
    echo "FAIL  $var=$value was REJECTED; it is a supported topology" >&2
    failures=$((failures + 1))
    return
  fi
  # Absence of that one string is NOT evidence of acceptance. A syntax error, an
  # unloadable module, a missing provider or the wrong $TF binary all produce a
  # different diagnostic — and the earlier version of this function read every one
  # of them as "supported topology accepted", printing green while the
  # configuration could not be parsed at all. Demand a real evaluated value.
  if grep -q "^Error:" <<<"$out" || [[ -z "$(tail -1 <<<"$out")" ]]; then
    echo "FAIL  $var=$value produced no value; the configuration did not evaluate:" >&2
    sed 's/^/        /' <<<"$out" >&2
    failures=$((failures + 1))
    return
  fi
  echo "ok    $var=$value accepted"
}

# --- nats_cluster_replicas (ADR-020 A0) --------------------------------------
# 0 derives from var.ha; 1/3/5 are the supported odd counts. Even counts buy no
# extra fault tolerance, and JetStream refuses more than 5 replicas per stream, so
# a 7-server cluster cannot host a stream replicated across it.
for v in 0 1 3 5; do accepts nats_cluster_replicas "$v"; done
for v in 2 4 6 7 -1; do rejects nats_cluster_replicas "$v"; done

# evaluates <expected> <expr> [-var k=v ...] — the expression must evaluate to
# exactly <expected> under those variables.
evaluates() {
  local expected="$1" expr="$2"
  shift 2
  local out status
  # `|| true` and an explicit status, NOT a bare command substitution. Under
  # `set -euo pipefail` a non-zero exit inside $(...) aborts the whole script from
  # within the substitution: the run ends on a green "ok" line with no FAIL, no
  # summary, and every remaining assertion silently skipped. The exit code
  # survives, so CI does go red — but the diagnostics do not, which is the same
  # family as a pipe to `head` swallowing a failure.
  out="$(echo "$expr" | "$TF" console "$@" 2>&1 | tail -1 || true)"
  status=$?
  if ((status != 0)); then
    echo "FAIL  $expr did not evaluate (exit $status)  [$*]" >&2
    failures=$((failures + 1))
    return
  fi
  if [[ "$out" == "$expected" ]]; then
    echo "ok    $expr == $expected  [$*]"
  else
    echo "FAIL  $expr == $out, want $expected  [$*]" >&2
    failures=$((failures + 1))
  fi
}

# --- the ADR-020 A0 server count, across both levers -------------------------
#
# The derivation `cluster_replicas > 0 ? cluster_replicas : (ha ? 3 : 1)` decides
# how many NATS servers exist, and therefore the CEILING on how widely any stream
# can be replicated. It is worth pinning at this level because it is the one place
# where two levers resolve to one number, and getting it backwards — the toggle
# silently winning over an explicit count — would produce a cluster the operator
# did not ask for while every artifact still read as if they had.
#
# This is the cheap half of the "render the HA topology in CI" check: no cluster,
# no provider credentials, no network. `tofu console` computes a module output as
# long as nothing in it depends on a resource attribute, which local-only
# arithmetic does not. The other half — that a 3-server cluster actually places one
# server per node — needs a real multi-node cluster and is the live A0 validation.
evaluates 1 module.nats.cluster_replicas -var ha=false
evaluates 3 module.nats.cluster_replicas -var ha=true
evaluates 5 module.nats.cluster_replicas -var nats_cluster_replicas=5
# An explicit count OVERRIDES the shorthand rather than being overridden by it.
evaluates 5 module.nats.cluster_replicas -var ha=true -var nats_cluster_replicas=5
# ha=false with an explicit cluster is NOT a contradiction: the cluster is enabled
# on the count, not on the flag, so this is the explicit-topology path.
evaluates 3 module.nats.cluster_replicas -var ha=false -var nats_cluster_replicas=3

# --- the values that decide whether HA is REAL -------------------------------
#
# Each of these can be broken in a way that leaves an instance looking highly
# available and surviving nothing, and none of them is reachable from a variable
# validation block. A resource `precondition` would not help either — those run
# during a PLAN, which no per-PR gate performs. So they are asserted here, off the
# module's ha_topology output, which `tofu console` can compute because it depends
# on no resource attribute.
#
# whenUnsatisfiable is the sharpest of them: flipping it to ScheduleAnyway is a
# one-word change that lets the scheduler put all three NATS servers on one node,
# which passes every other check A0 adds (three peers, Replicas:3, all current)
# and survives zero node losses.
evaluates '"DoNotSchedule"' 'module.nats.ha_topology.spread_constraints["kubernetes.io/hostname"].whenUnsatisfiable' -var ha=true
evaluates 1 'module.nats.ha_topology.spread_constraints["kubernetes.io/hostname"].maxSkew' -var ha=true
evaluates '{}' 'module.nats.ha_topology.spread_constraints' -var ha=false
# The MQTT gateway's own streams hold persistent sessions and inflight QoS 1
# messages; at 1 on a clustered broker, losing their node drops every session.
evaluates 3 module.nats.ha_topology.mqtt_stream_replicas -var ha=true
evaluates 1 module.nats.ha_topology.mqtt_stream_replicas -var ha=false
evaluates 3 module.nats.ha_topology.mqtt_stream_replicas -var nats_cluster_replicas=5
# Clustering opens route port 6222, which carries every replicated write. Without
# mutual verification any pod that can reach it joins the cluster as a peer and
# reads every account, bypassing the auth callout.
evaluates true module.nats.ha_topology.route_tls_verified -var ha=true
evaluates true module.nats.ha_topology.clustered -var ha=true
evaluates false module.nats.ha_topology.clustered -var ha=false

# The ha=true + cluster_replicas=1 contradiction. The REFUSAL lives in a
# helm_release precondition, which only runs during a plan and which nothing in CI
# performs — so what is asserted here is that the module still DETECTS the
# contradiction, not that it refuses it. Worth having anyway: if this flips, the
# precondition is guarding a condition that can no longer occur, and an operator
# asking for HA would quietly get one server.
evaluates true module.nats.ha_topology.contradictory -var ha=true -var nats_cluster_replicas=1
evaluates false module.nats.ha_topology.contradictory -var ha=true
evaluates false module.nats.ha_topology.contradictory -var ha=false -var nats_cluster_replicas=3

# --- nats_mqtt_node_port -----------------------------------------------------
# Pre-existing guard, included so this script covers the root's validation blocks
# rather than only the newest one.
for v in 0 30000 31883 32767; do accepts nats_mqtt_node_port "$v"; done
for v in 1883 29999 32768; do rejects nats_mqtt_node_port "$v"; done

if ((failures > 0)); then
  echo >&2
  echo "$failures validation assertion(s) failed." >&2
  exit 1
fi
echo
echo "All OpenTofu variable validations behave as declared."
