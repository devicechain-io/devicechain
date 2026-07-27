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

# EVERY console invocation goes through tf_console, and it is time-bounded.
#
# `tofu console` READS STDIN. A harness that hands it a pipe and never closes that
# pipe does not produce an error — it blocks, forever, with no output. GitHub's
# opentofu/setup-opentofu does exactly that by default: it puts a Node wrapper on
# the PATH ahead of the real binary, and the wrapper does not forward the step's
# stdin to the child. Every other tofu command CI runs (fmt, init, validate) reads
# no stdin, so all of them passed and only this script hung — the very first
# assertion, before a single line of output. The observable symptom was not a red
# check but a check that never resolved, holding a runner until the workflow's own
# six-hour ceiling.
#
# The step now sets `tofu_wrapper: false`, which is the actual fix. This timeout is
# the backstop, and it is the part worth keeping: it converts any future
# stdin-blocking harness — a different action, a container, a shell that inherits a
# terminal — from an invisible six-hour stall into a named failure on the assertion
# that hung.
readonly console_timeout="${TF_CONSOLE_TIMEOUT:-60}"
tf_console_out=""
tf_console_status=0

# tf_console <stdin> [args...] — run `$TF console`, feeding <stdin> and CLOSING it.
tf_console() {
  local input="$1"
  shift
  tf_console_status=0
  tf_console_out="$(printf '%s' "$input" | timeout "$console_timeout" "$TF" console "$@" 2>&1)" ||
    tf_console_status=$?
  if ((tf_console_status == 124)); then
    tf_console_out="TIMED OUT after ${console_timeout}s with no output. \`$TF console\` reads stdin and something in the harness is not closing it; opentofu/setup-opentofu's Node wrapper has this exact behaviour, so check for \`tofu_wrapper: false\` on the setup step."
  fi
}

# timed_out — true if the last tf_console call hit the ceiling. Reported on its own
# rather than folded into the assertion's own failure text, because "the guard is
# wrong" and "the guard never ran" are different problems and only one of them is
# about the configuration.
timed_out() {
  local what="$1"
  ((tf_console_status == 124)) || return 1
  echo "FAIL  $what: $tf_console_out" >&2
  failures=$((failures + 1))
  return 0
}

# rejects <variable> <value> — the value must trip a validation block.
rejects() {
  local var="$1" value="$2" out
  # Empty stdin, closed immediately: a rejection is emitted while loading the
  # variables, before the console would read an expression.
  tf_console "" -var "$var=$value"
  timed_out "$var=$value" && return
  out="$tf_console_out"
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
  tf_console "var.$var" -var "$var=$value"
  timed_out "$var=$value" && return
  out="$tf_console_out"
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
  local out
  # tf_console captures the status rather than letting it escape. Under
  # `set -euo pipefail` a non-zero exit inside a bare $(...) aborts the whole script
  # from within the substitution: the run ends on a green "ok" line with no FAIL, no
  # summary, and every remaining assertion silently skipped. The exit code
  # survives, so CI does go red — but the diagnostics do not, which is the same
  # family as a pipe to `head` swallowing a failure.
  tf_console "$expr" "$@"
  timed_out "$expr  [$*]" && return
  if ((tf_console_status != 0)); then
    echo "FAIL  $expr did not evaluate (exit $tf_console_status)  [$*]" >&2
    sed 's/^/        /' <<<"$tf_console_out" >&2
    failures=$((failures + 1))
    return
  fi
  out="$(tail -1 <<<"$tf_console_out")"
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

# ROUTE TLS BEING ON AND ROUTE TLS WORKING ARE DIFFERENT FACTS, and the gap
# between them cost a working cluster. The assertion above passed — the
# configuration was right — while every route handshake failed, because the
# server certificate was issued only for the names CLIENTS dial. Servers reach
# each other by POD name through the headless Service (dc-nats-1.dc-nats-headless),
# which a load-balanced Service name cannot address, so with verification on the
# peers rejected each other and no cluster formed at all. Three isolated servers,
# no JetStream meta leader.
#
# Nothing saw it: not `tofu validate`, not helm lint, not the ha_topology output,
# and not any single-node install, which never opens a route. Only the live 3-node
# rig did. These lines are what move it back into a gate that runs on every PR.
evaluates true 'alltrue([for i in range(3) : contains(module.nats.ha_topology.server_dns_names, "dc-nats-${i}.dc-nats-headless")])' -var ha=true
evaluates true 'alltrue([for i in range(3) : contains(module.nats.ha_topology.server_dns_names, "dc-nats-${i}.dc-nats-headless.dc-system.svc.cluster.local")])' -var ha=true
# Scaled explicitly: a 5-server cluster needs five peers named, and a SAN list
# built for three would leave servers 3 and 4 unable to join.
evaluates true 'alltrue([for i in range(5) : contains(module.nats.ha_topology.server_dns_names, "dc-nats-${i}.dc-nats-headless")])' -var nats_cluster_replicas=5
# The counterweight: an unclustered broker opens no route, so it gets no route
# names. Without this the assertions above are satisfied by naming every possible
# pod unconditionally, which would be a certificate promising peers that do not
# exist.
evaluates false 'contains(module.nats.ha_topology.server_dns_names, "dc-nats-0.dc-nats-headless")' -var ha=false
# And the client names survive. A route-name change that dropped them would break
# every service connection instead — the same failure, pointed the other way.
evaluates true 'contains(module.nats.ha_topology.server_dns_names, "dc-nats.dc-system")' -var ha=true

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

# --- postgres_instances (ADR-020 A2.3) ---------------------------------------
#
# 0 derives from var.ha; 1 is the supported non-HA topology (decision D4); 3 is
# the smallest synchronous one.
#
# 🔴 2 IS THE POINT OF THIS BLOCK, and it is refused for a reason that is not
# obvious and is not the same as CloudNativePG's own rule. The operator's
# admission webhook rejects only `number >= instances` — it accepts two
# instances quite happily. But synchronous replication at two means one standby
# must confirm every commit and there is exactly one standby: losing it stalls
# every write, which is WORSE for availability than the single node it replaced.
# Nothing upstream refuses that, so if this validation stops working, the
# unsafe topology becomes reachable and looks like a reasonable middle setting.
for v in 0 1 3 5; do accepts postgres_instances "$v"; done
for v in 2 4 -1; do rejects postgres_instances "$v"; done

# --- the derived database topology, across both levers -----------------------
#
# `postgres_instances != 0 ? postgres_instances : (ha ? 3 : 1)` decides how many
# database instances exist, and the module then derives whether synchronous
# replication is enabled AT ALL from that count. Both derivations are pinned
# here because the failure they prevent is silent in the dangerous direction: a
# topology that asked for synchronous replication and did not get enough
# instances runs ASYNCHRONOUSLY, with healthy pods and a green apply.
#
# postgres_synchronous_enforced is therefore the database sibling of
# ha_topology.contradictory — it reports what is in force, not what was asked
# for, so an instance that quietly lost its durability guarantee is legible
# from the root's own outputs rather than only from the cluster.
evaluates 3 'var.postgres_instances != 0 ? var.postgres_instances : (var.ha ? 3 : 1)' -var ha=true
evaluates 1 'var.postgres_instances != 0 ? var.postgres_instances : (var.ha ? 3 : 1)' -var ha=false
evaluates 1 'var.postgres_instances != 0 ? var.postgres_instances : (var.ha ? 3 : 1)' -var ha=true -var postgres_instances=1

if ((failures > 0)); then
  echo >&2
  echo "$failures validation assertion(s) failed." >&2
  exit 1
fi
echo
echo "All OpenTofu variable validations behave as declared."
