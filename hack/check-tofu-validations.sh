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
#
# A refusal is only evidence about the value under test if it is ATTRIBUTED to it.
# The first version of `rejects` asked only whether the string "Invalid value for
# variable" appeared anywhere in the diagnostic, and that is a check that cannot
# fail the moment any SHIPPED DEFAULT stops satisfying its own validation: the run
# is then refused before the supplied value matters, so every rejects line here goes
# green having evaluated nothing. Hence the baseline control below — the defaults
# must load clean — and an attribution check: the diagnostic must name the variable
# under test.
#
# What attribution does NOT have to catch, measured rather than assumed: a condition
# that tests some OTHER variable instead of its own is refused by OpenTofu outright
# ("the condition for variable X must refer to var.X in order to test incoming
# values"), so the pure cross-wire cannot be written. The reachable version is a
# block that tests its own value against the WRONG neighbour — restore_tsdb_target_time
# checking restore_rdb_from — and that one is caught by the ACCEPTS counterweight,
# not by attribution: it refuses the real recovery it is supposed to permit.
#
# The cost of naming it: nats_cluster_replicas is guarded TWICE, once on the root
# variable and again on modules/nats's own, and this now pins the ROOT block
# specifically — gutting it fails here even though the module's block still refuses
# the apply. That is the right trade (an operator's experience is unchanged, but a
# maintainer editing one of two guards now hears about it), and it leaves the
# module's own block covered by nothing. Testing that one needs a root pointed at
# the module; it is a real gap, named rather than papered over.
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

# Which variables this run actually exercised, in each direction. Recorded as the
# assertions run rather than listed by hand, so the coverage control at the bottom
# cannot drift away from what the script does. See it for why both halves matter.
rejected_vars=()
accepted_vars=()

# THE CONTROL EVERY `rejects` BELOW STANDS ON: the root must LOAD CLEAN at its
# shipped defaults.
#
# `rejects` reads a validation diagnostic as evidence that the value it supplied was
# refused. If a shipped default ever stops satisfying its own validation, that
# diagnostic is emitted on every invocation regardless of the value — and every
# assertion in this file goes green while testing nothing at all. It is also a real
# failure in its own right: a default that fails validation makes a bare `tofu apply`
# impossible.
tf_console "1"
if timed_out "the defaults baseline"; then
  :
elif grep -q "Invalid value for variable" <<<"$tf_console_out"; then
  echo "FAIL  the root does not load at its own defaults — a shipped default fails its" \
    "validation. Until that is fixed every 'rejected' line below is vacuous: the" \
    "diagnostic they match on is emitted whatever value is supplied." >&2
  sed 's/^/        /' <<<"$tf_console_out" >&2
  failures=$((failures + 1))
else
  echo "ok    the root loads clean at its shipped defaults"
fi

# rejects <variable> <value> [--by <name>] [extra -var args...] — the value must trip
# a validation block, and the diagnostic must name <variable> as the one that refused
# it.
#
# --by names a DIFFERENT variable when the guard lives downstream: a root variable
# passed straight into a module whose own variable carries the validation. Writing
# it out is the point — it is the difference between "the root declares this
# variable's grammar" and "the root declares no grammar and the module happens to",
# which is otherwise invisible, and the coverage control at the bottom only counts
# blocks in the root.
rejects() {
  local var="$1" value="$2" by="$1" plain want
  shift 2
  if [[ "${1:-}" == "--by" ]]; then
    by="$2"
    shift 2
  fi
  # Empty stdin, closed immediately: a rejection is emitted while loading the
  # variables, before the console would read an expression.
  tf_console "" -var "$var=$value" "$@"
  timed_out "$var=$value" && return
  rejected_vars+=("$var")
  plain="$(sed 's/\x1b\[[0-9;]*m//g' <<<"$tf_console_out")"
  if ! grep -q "Invalid value for variable" <<<"$plain"; then
    echo "FAIL  $var=$value was ACCEPTED; no validation block on this path covers it  [$*]" >&2
    failures=$((failures + 1))
    return
  fi
  # 🔴 REFUSED BY *THIS* VARIABLE'S BLOCK. A diagnostic quoting some other variable
  # means the guard under test never ran — its neighbour's did, on input meant for
  # this one — so the value here is unguarded while the line reads green.
  # 🔴 THE HEADER LINE, NOT THE VALUE CONTEXT. A diagnostic quotes the block it
  # came from as `variable "x" {`, and separately lists EVERY variable that
  # block's condition referenced under the `├──` rule — so `restore_rdb_target_time`'s
  # refusal prints `var.restore_rdb_from is ""` too. Matching on that context would
  # attribute a neighbour's refusal to whichever variable it happened to mention,
  # which is the precise failure this check exists to catch. Only the header is
  # evidence about WHICH guard fired.
  #
  # The value form is kept for --by alone, where the guard lives in a module and
  # the diagnostic is headed by the module CALL rather than by a variable block.
  # That form is weaker, and it is confined to the one case that needs it.
  want="variable \"$by\" {"
  [[ "$by" == "$var" ]] || want="var.$by is "
  if ! grep -qF -- "$want" <<<"$plain"; then
    echo "FAIL  $var=$value was refused, but not by $by's validation block:" >&2
    sed 's/^/        /' <<<"$plain" >&2
    failures=$((failures + 1))
    return
  fi
  echo "ok    $var=$value rejected  [$*]"
}

# accepts <variable> <value> [extra -var args...] — the counterweight. A validation
# that rejects everything is not a guard, it is an outage, and it would pass every
# "rejects" assertion above on its own.
accepts() {
  local var="$1" value="$2" out
  shift 2
  tf_console "var.$var" -var "$var=$value" "$@"
  timed_out "$var=$value" && return
  accepted_vars+=("$var")
  out="$tf_console_out"
  if grep -q "Invalid value for variable" <<<"$out"; then
    echo "FAIL  $var=$value was REJECTED; it is a supported topology" >&2
    failures=$((failures + 1))
    return
  fi
  # Absence of that one string is NOT evidence of acceptance. A syntax error, an
  # unloadable module, a missing provider or the wrong $TF binary all produce a
  # different diagnostic — and the FIRST version of this function read every one of
  # them as "supported topology accepted", printing green while the configuration
  # could not be parsed at all. Demand a real evaluated value.
  #
  # 🔴 THE SECOND VERSION WAS ALSO DEAD, AND FOR A DUMBER REASON: it grepped
  # `^Error:` and tested for an empty last line, and neither can ever match.
  # Terraform/OpenTofu box-draw their diagnostics, so the line reads
  # `│ Error: ...` behind an ANSI colour prefix — `^Error:` matches nothing — and
  # the last line of a failed run is `\e[0m\e[0m`, which is not empty. Measured:
  # against a root that does not parse, `grep -c '^Error:'` is 0 and the guard
  # never fired, so 19 green `accepted` lines printed for a configuration
  # Terraform could not load. Never assert on a diagnostic's shape without having
  # watched the assertion fail on a real one.
  #
  # Two signals now, because neither covers the other — both measured against the
  # real binary with an expression on stdin:
  #
  #   situation                          exit   last line   "Error:" (ANSI-stripped)
  #   valid config, value accepted        0     the value   no
  #   valid config, value rejected        0     the value   yes   (caught above)
  #   root does not parse                 1     ANSI reset  yes
  #   undeclared variable                 1     ANSI reset  yes
  #   validation condition won't compile  0     the value   yes
  #
  # The last row is why the exit status alone is not enough, and it is the
  # dangerous one: a validation block whose condition does not compile is
  # SILENTLY IGNORED — console exits 0 and happily prints the value — so the
  # variable has no guard at all while this function reports it accepted.
  local plain
  plain="$(sed 's/\x1b\[[0-9;]*m//g' <<<"$out")"
  if ((tf_console_status != 0)) || grep -q "Error:" <<<"$plain"; then
    echo "FAIL  $var=$value did not evaluate (exit $tf_console_status); the configuration" \
      "did not load, or its validation block does not compile:" >&2
    sed 's/^/        /' <<<"$out" >&2
    failures=$((failures + 1))
    return
  fi
  # The value itself, on the stripped output — a bare ANSI reset is not a value.
  if [[ -z "$(tail -1 <<<"$plain")" ]]; then
    echo "FAIL  $var=$value produced no value:" >&2
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
# Both counterweights, because a single `true` is satisfied by an output hard-wired
# to true. An unclustered broker opens NO route, so "route TLS verified" must be
# false there rather than vacuously true — and with TLS off it must be false on a
# clustered one, which is the state that actually leaves 6222 in the clear.
evaluates false module.nats.ha_topology.route_tls_verified -var ha=false
evaluates false module.nats.ha_topology.route_tls_verified -var ha=true -var nats_enable_tls=false
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
# --by: this is the one variable in the root that declares NO grammar of its own —
# the refusal comes from modules/nats's mqtt_node_port block, which the root feeds
# directly. Worth naming rather than hiding: an operator reading variables.tf sees
# no constraint on nats_mqtt_node_port, and the day the module stops taking it
# straight through, this line is what says the root never guarded it.
for v in 1883 29999 32768; do rejects nats_mqtt_node_port "$v" --by mqtt_node_port; done

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

# The SECOND derivation — whether synchronous replication is actually IN FORCE.
#
# This one is asymmetric, which is why it needs its own assertions rather than
# riding on the instance count above. Break the derivation toward ON and the
# chart's `fail` catches it loudly (the CI helm step covers exactly that). Break
# it toward OFF and a `--ha` install runs ASYNCHRONOUSLY behind three healthy
# pods and a green apply — the false-HA shape — with nothing in CI to notice.
#
# `synchronous_enforced` reports what is in force rather than what was asked
# for, which is what makes it the database sibling of ha_topology.contradictory.
# These three lines are what stop it from being decorative.
evaluates true 'module.cnpg_rdb.synchronous_enforced' -var ha=true
evaluates false 'module.cnpg_rdb.synchronous_enforced' -var ha=false
evaluates false 'module.cnpg_rdb.synchronous_enforced' -var ha=true -var postgres_instances=1

# --- timescale_instances (ADR-020 A2.4) --------------------------------------
#
# The event store's half of the same two-lever derivation. Same 1-or-3-never-2
# rule, and it needs its own assertions rather than being assumed to follow the
# relational store: the two are separate variables feeding separate modules, so
# a derivation that breaks on one is invisible from the other.
for v in 0 1 3 5; do accepts timescale_instances "$v"; done
for v in 2 4 -1; do rejects timescale_instances "$v"; done

evaluates 3 'var.timescale_instances != 0 ? var.timescale_instances : (var.ha ? 3 : 1)' -var ha=true
evaluates 1 'var.timescale_instances != 0 ? var.timescale_instances : (var.ha ? 3 : 1)' -var ha=false
evaluates 1 'var.timescale_instances != 0 ? var.timescale_instances : (var.ha ? 3 : 1)' -var ha=true -var timescale_instances=1

evaluates true 'module.cnpg_tsdb.synchronous_enforced' -var ha=true
evaluates false 'module.cnpg_tsdb.synchronous_enforced' -var ha=false
evaluates false 'module.cnpg_tsdb.synchronous_enforced' -var ha=true -var timescale_instances=1

# 🔴 The two stores must not share a lever by accident. If a future edit points
# the event store's module at postgres_instances, every assertion above still
# passes (both derive 3 under ha=true), and the only visible symptom is that
# pinning one store silently moves the other. These two lines are what make that
# substitution fail: they set the levers to DIFFERENT values and require each
# store to follow its own.
evaluates 3 'module.cnpg_tsdb.synchronous_enforced ? 3 : 1' -var ha=true -var postgres_instances=1
evaluates 1 'module.cnpg_rdb.synchronous_enforced ? 3 : 1' -var ha=true -var postgres_instances=1

# --- the node-loss eviction fuse (ADR-020 A1.4) ------------------------------
#
# 🔴 THE FAILURE THIS PINS IS A DELETION, NOT A WRONG VALUE, and that is why it
# is asserted at all when the module hard-codes a default. Drop
# `nodeLossTolerationSeconds` from base_values in a refactor and nothing breaks:
# the chart's own default is null, null renders no toleration, and Kubernetes'
# DefaultTolerationSeconds admission plugin then injects 300 onto every instance
# pod. Three healthy pods, a green apply, synchronous replication genuinely on —
# and a dead primary's pod lingering five minutes instead of thirty seconds,
# which lengthens the tail of every failover by the difference.
#
# There is no symptom until a node dies. So the assertion is that the value
# REACHES the chart, on both stores, in both postures.
#
# The expected value is a STRING because the output is tostring()'d: the "unset"
# case has to be distinguishable from a number, and an untyped null compares
# badly against a typed one.
#
# Measured, by deleting the wiring and running the mutation: the unset case
# prints `tostring(null)` here, NOT `<nil>` — an earlier version of this comment
# said `<nil>` and was simply wrong. That string is what "Kubernetes' 300s
# default is in force" looks like from the root, and it is the state these four
# lines exist to keep from appearing silently.
evaluates '"30"' 'module.cnpg_rdb.node_loss_toleration_seconds' -var ha=true
evaluates '"30"' 'module.cnpg_rdb.node_loss_toleration_seconds' -var ha=false
evaluates '"30"' 'module.cnpg_tsdb.node_loss_toleration_seconds' -var ha=true
evaluates '"30"' 'module.cnpg_tsdb.node_loss_toleration_seconds' -var ha=false

# --- the CNPG control plane's own availability (ADR-020 A1.5) ----------------
#
# 🔴 THE OPERATOR IS IN THE DATABASE FAILOVER PATH. CloudNativePG cannot
# reconcile a Cluster whose plugins it cannot reach, and a Cluster that does not
# reconcile does not promote a standby. Measured on one cluster, same fault
# minutes apart: 10m50s to fail over when the control plane shared the dead node,
# 1m51s when it did not.
#
# What these pin is a DELETION, and there are two distinct ones. Drop `ha =
# var.ha` at the module call and the module's own default (false) silently takes
# over — one replica, no spread, green apply. Drop `topologySpreadConstraints`
# from the values document and two replicas cheerfully share a node, which is the
# same false-HA shape as three database instances on one host: it costs twice as
# much and protects against nothing, and every replica count agrees it is fine.
#
# `operator_spread_enforced` reports the SELECTOR, not merely the constraint,
# because a topologySpreadConstraint whose labelSelector matches no pods renders
# fine and spreads nothing.
evaluates 2 'module.cnpg[0].operator_replicas' -var ha=true
evaluates 1 'module.cnpg[0].operator_replicas' -var ha=false
evaluates true 'module.cnpg[0].operator_spread_enforced' -var ha=true
evaluates false 'module.cnpg[0].operator_spread_enforced' -var ha=false

# The plugin is deliberately NOT replicated — its CNPG-I gRPC server is
# leader-election-gated and its chart's readinessProbe is a hard-coded tcpSocket
# on that port, so a second replica never becomes Ready and `helm wait` fails the
# apply. This asserts the fuse it DOES get, on both postures, since that is the
# whole of its node-loss story.
evaluates '"30"' 'module.cnpg[0].control_plane_toleration_seconds' -var ha=true
evaluates '"30"' 'module.cnpg[0].control_plane_toleration_seconds' -var ha=false

# --- database backups (ADR-028, ADR-020 A2.5) --------------------------------
#
# The destination is two-valued by construction. There is deliberately no third
# value meaning "backups on, destination none", because that is the exact state
# this slice exists to remove: the plugin installed, the flag reading true, and
# nothing archived anywhere. An empty string is the value a half-finished edit
# produces, so it has to be refused rather than treated as "no destination".
for v in in-cluster external; do accepts backup_destination "$v"; done
for v in "" none s3 In-Cluster local; do rejects backup_destination "$v"; done

# 🔴 BACKUPS REQUIRE THE OPERATOR, and the conjunction is the thing being pinned.
# `--no-cnpg` already sets enable_database_backups=false at the dcctl layer, so
# dropping `&& var.enable_cnpg` from the derivation looks harmless and passes
# every dcctl-side test. What it produces is an object store, a 20Gi volume and
# two ObjectStore resources standing ready for a plugin that was never installed
# — no error anywhere, and no backups.
evaluates true 'local.backups_on'
evaluates false 'local.backups_on' -var enable_database_backups=false
evaluates false 'local.backups_on' -var enable_cnpg=false
evaluates false 'local.backups_on' -var enable_cnpg=false -var enable_database_backups=true

# What the flag actually BUYS, read back through the modules rather than from the
# flag itself. `database_backups_enabled` used to mean only that the plugin was
# installed; these assert that each store resolves a real destination path, which
# is the claim that was previously unsupported.
evaluates '"s3://devicechain-rdb/dc-rdb"' 'module.cnpg_rdb.backup_destination'
evaluates '"s3://devicechain-tsdb/dc-tsdb"' 'module.cnpg_tsdb.backup_destination'
evaluates 'tostring(null)' 'module.cnpg_rdb.backup_destination' -var enable_database_backups=false
evaluates 'tostring(null)' 'module.cnpg_tsdb.backup_destination' -var enable_database_backups=false

# 🔴 THE TWO STORES MUST NOT SHARE A BUCKET, and this is the substitution guard
# for it — the exact sibling of the instance-count one above, for the exact same
# reason. Point the event store's module at var.backup_bucket_rdb by accident and
# every assertion in this file still passes on the defaults, because the two
# buckets are only ever compared here. What it would silently cost is the whole
# core-data / event-data split: one bucket, one retention policy, and "restore
# the event database without touching the control plane" stops being a thing that
# can be done.
#
# Setting the two levers to DIFFERENT values is what makes a substitution fail
# rather than coincide.
evaluates '"s3://core-only/dc-rdb"' 'module.cnpg_rdb.backup_destination' -var backup_bucket_rdb=core-only
evaluates '"s3://devicechain-tsdb/dc-tsdb"' 'module.cnpg_tsdb.backup_destination' -var backup_bucket_rdb=core-only
evaluates '"s3://events-only/dc-tsdb"' 'module.cnpg_tsdb.backup_destination' -var backup_bucket_tsdb=events-only
evaluates '"s3://devicechain-rdb/dc-rdb"' 'module.cnpg_rdb.backup_destination' -var backup_bucket_tsdb=events-only

# The object store is provisioned only where it is used. An external destination
# must not stand one up — that would be a MinIO pod and a volume nobody writes to,
# on the configuration whose whole point is that storage lives elsewhere.
evaluates 1 'length(module.object_store)'
evaluates 0 'length(module.object_store)' -var enable_database_backups=false
evaluates 0 'length(module.object_store)' -var backup_destination=external -var backup_endpoint_url=https://s3.example.com

# ...and the credentials come from the matching source. The in-cluster
# destination REUSES the object store's own Secret rather than writing a second
# copy of the same credential — MinIO's root credentials and the credentials the
# archiver presents are the same credentials, and two Secrets holding one value
# is two things to rotate and one of them to forget.
#
# Asserted on the KEY NAMES rather than on a resource count, and not by choice:
# `length(kubernetes_secret_v1.backup_credentials)` is `(known after apply)` in
# the console, so it cannot discriminate anything. The key names can, and they
# are a sharper probe anyway — they differ between the two branches, so this
# fails if the conditional ever selects the wrong source while both branches
# still produce a Secret.
evaluates '"MINIO_ROOT_USER"' 'local.backup_credentials.access_key'
evaluates '"ACCESS_KEY_ID"' 'local.backup_credentials.access_key' -var backup_destination=external -var backup_endpoint_url=https://s3.example.com
evaluates 'tostring(null)' 'local.backup_credentials == null ? null : "set"' -var enable_database_backups=false

# --- restore (ADR-028, ADR-020 A2.5c) ----------------------------------------
#
# A restore is a rebuild-time lever, and the whole reason it needs a gate here is
# that its failure modes are QUIET. CloudNativePG reads `spec.bootstrap` only
# when it CREATES a Cluster, so most ways of getting this wrong produce a green
# apply and a cluster that either came up empty or is wedged part-way through
# coming up — neither of which is an apply error anybody sees.
#
# 🔴 WHAT THIS CANNOT DO, stated plainly for the same reason the ha=true +
# cluster_replicas=1 block above states it: the REFUSALS live in
# terraform_data.restore_guard preconditions, which run during a PLAN, and no
# per-PR gate performs one. NONE of the four is checked here or anywhere else.
#
# An earlier version of this header said the section asserts "the root still
# DETECTS each condition — the exact expressions the preconditions evaluate." That
# was false, and provably so: deleting terraform_data.restore_guard in full left
# every assertion below green. Restating a precondition's expression and asking
# `tofu console` to evaluate it does not reach the precondition; it re-runs the
# operators inside it. Those five assertions are gone — see the note further down.
#
# What is left is what this tool can honestly establish without a plan: that the
# variable validations refuse what they claim to, and that the LOCALS the module
# blocks are wired from take the shapes the chart expects. The refusals stay
# unverified until hack/dr-rig.sh drives one against a live cluster and is turned
# away.

# A target with nothing to restore is a variable validation, so it IS refused in
# CI. It matters because it is silent otherwise: recovery would replay the whole
# archive and the operator would believe they had stopped before the bad delete.
rejects restore_rdb_target_time "2026-07-28 03:00:00+00"
rejects restore_tsdb_target_time "2026-07-28 03:00:00+00"
# 🔴 THE COUNTERWEIGHT, AND IT HAS TO SET A SOURCE. `accepts <target> ""` — the only
# acceptance these two blocks had — passes against a condition hard-wired to reject
# every non-empty target, because the empty string IS the default and the guard is
# then never asked anything. What has to keep working is the real recovery: a target
# WITH the source it names. The event store's block had no acceptance at all.
accepts restore_rdb_target_time "2026-07-28 03:00:00+00" -var restore_rdb_from=dc-rdb
accepts restore_tsdb_target_time "2026-07-26 01:02:03+00" -var restore_tsdb_from=dc-tsdb

# The per-store objects the modules actually receive. Null on a normal install —
# and this is the assertion that fails if a future edit makes "restore" a flag
# rather than an absent object, which would hand the chart a half-populated
# restore block on every ordinary bootstrap.
evaluates 'tostring(null)' 'local.rdb_restore == null ? null : "set"'
evaluates 'tostring(null)' 'local.tsdb_restore == null ? null : "set"'
evaluates '"dc-rdb"' 'local.rdb_restore.source_server_name' -var restore_rdb_from=dc-rdb
evaluates 'tostring(null)' 'local.tsdb_restore == null ? null : "set"' -var restore_rdb_from=dc-rdb
evaluates '"dc-tsdb"' 'local.tsdb_restore.source_server_name' -var restore_tsdb_from=dc-tsdb

# 🔴 ONE STORE AT A TIME IS THE NORMAL CASE, and the pair above is what proves
# the two levers are independent. A restore wired to a single shared switch
# would rewind the control plane every time somebody recovered the event store,
# discarding every tenant, device and rule created since — a data-loss bug
# committed while restoring from data loss.

# The point-in-time target reaches the module as CNPG's own camelCase field.
# Spelled wrongly it is pruned by the API server and the recovery silently
# replays everything: a PITR that restores the damage it was run to undo.
evaluates '"2026-07-28 03:00:00+00"' 'local.rdb_restore.recovery_target["targetTime"]' \
  -var restore_rdb_from=dc-rdb -var 'restore_rdb_target_time=2026-07-28 03:00:00+00'
evaluates 'tomap({})' 'local.rdb_restore.recovery_target' -var restore_rdb_from=dc-rdb

# AND THE EVENT STORE'S TWIN, which had none of this. The rdb block above is
# where a spelling mistake would be caught; the tsdb block is a hand-written copy
# of it, and until now nothing read it at all — so `targetTime` misspelled there
# was silent, and it is the store an operator is MORE likely to rewind, since
# telemetry is where a bad ingest lands. The counterweight matters for the same
# reason it does above: without it a `recovery_target` hard-wired to a constant
# map satisfies the positive case.
#
# A DIFFERENT instant on purpose. With one shared literal, a tsdb block wired to
# var.restore_rdb_target_time by copy-paste would read the right value whenever
# both were set, and the assertion would confirm the bug.
evaluates '"2026-07-26 01:02:03+00"' 'local.tsdb_restore.recovery_target["targetTime"]' \
  -var restore_tsdb_from=dc-tsdb -var 'restore_tsdb_target_time=2026-07-26 01:02:03+00'
evaluates 'tomap({})' 'local.tsdb_restore.recovery_target' -var restore_tsdb_from=dc-tsdb
# The cross-wire, named directly: the event store's target must not follow the
# relational store's variable. Both set, to different moments -- the only shape
# in which a transposition produces a value rather than an empty map.
evaluates '"2026-07-26 01:02:03+00"' 'local.tsdb_restore.recovery_target["targetTime"]' \
  -var restore_rdb_from=dc-rdb -var 'restore_rdb_target_time=2026-07-28 03:00:00+00' \
  -var restore_tsdb_from=dc-tsdb -var 'restore_tsdb_target_time=2026-07-26 01:02:03+00'
evaluates '"2026-07-28 03:00:00+00"' 'local.rdb_restore.recovery_target["targetTime"]' \
  -var restore_rdb_from=dc-rdb -var 'restore_rdb_target_time=2026-07-28 03:00:00+00' \
  -var restore_tsdb_from=dc-tsdb -var 'restore_tsdb_target_time=2026-07-26 01:02:03+00'

# 🔴 THE WEDGE CONDITION IS NOT CHECKED HERE, AND FIVE ASSERTIONS THAT CLAIMED TO
# CHECK IT HAVE BEEN DELETED.
#
# They restated the precondition's own expression and asked `tofu console` to
# evaluate it:
#
#   evaluates false 'var.backup_server_name_rdb != "" && \
#     var.backup_server_name_rdb != var.restore_rdb_from' -var restore_rdb_from=dc-rdb
#
# That references nothing this root declares beyond two variables, so it tests
# that OpenTofu's `!=` operator works. Measured, not argued: deleting
# `terraform_data.restore_guard` in full -- all four preconditions -- left the
# whole script printing "All OpenTofu variable validations behave as declared."
# The header above once claimed this section asserts "the root still DETECTS each
# condition." It did not, and a check that passes against the feature's absence is
# worse than no check, because it is counted.
#
# A precondition is only evaluated during a PLAN, and nothing in CI plans this
# root (it needs credentials and a cluster). So the four preconditions at
# main.tf's restore_guard are UNVERIFIED until hack/dr-rig.sh runs one live and
# is shown to be refused. What survives below is what `tofu console` can honestly
# reach: variable validations, and the locals the module blocks are wired from.

# Restoring with backups off: there is no ObjectStore to read from, and the
# cluster would come up EMPTY rather than failing — which during a rebuild looks
# exactly like a restore that found nothing to bring back.
evaluates false 'local.backups_on' -var restore_rdb_from=dc-rdb -var enable_database_backups=false

# NOT ASSERTED HERE: the `database_restored_from` output — which, to be accurate
# about what it is, NOTHING currently reads. dcctl consumes
# database_backups_enabled and database_backup_survives_cluster_loss; this one was
# written for the "what did the infrastructure do vs what did I ask for" question
# and never wired to a consumer, so dcctl's own "recovering from archive %q" line
# is printed from argv — the very thing the output exists to stop.
# `tofu console` cannot address `output.*` at all — outputs exist in state, after
# an apply — and the only way to check one from here is to restate its expression,
# which is a second copy that stops matching the day the first one changes. The
# local it projects is asserted above instead, which is the thing that could
# actually be wrong; the projection is a null check on it.

# --- the operand image tag is a SECOND COPY, and nothing links it to its source
#
# deploy/images/timescaledb/versions.conf is the single source of truth for the
# image we build. The workflow computes the published tag from it as
# `<pg_minor>-ts<timescaledb_version>-r<revision>`; variables.tf then carries
# that tag as a hand-written string, because a Terraform default cannot read a
# shell file.
#
# So the deployed event store's image version is defined in two places that have
# no mechanical relationship. Bump versions.conf, rebuild, publish — and the
# platform keeps deploying the OLD tag, successfully, with the new image sitting
# unused in the registry. Nothing fails; the fix simply does not take effect.
# This recomputes the tag and requires the default to match it.
versions_conf="$repo_root/deploy/images/timescaledb/versions.conf"
if [[ -f $versions_conf ]]; then
  # shellcheck disable=SC1090
  source "$versions_conf"
  pg_minor=${PG_IMAGE##*:}
  pg_minor=${pg_minor%%-*}
  # Guarded, because the workflow this formula is copied from guards it too. A
  # digest-pinned PG_IMAGE leaves hex here, and the assertion would then fail
  # pointing at variables.tf when the problem is PG_IMAGE.
  if ! grep -qE '^[0-9]+\.[0-9]+$' <<<"$pg_minor"; then
    echo >&2 "MISSING: could not parse a PostgreSQL minor version from PG_IMAGE=$PG_IMAGE (got '$pg_minor')."
    failures=$((failures + 1))
    pg_minor=""
  fi
  want_image="ghcr.io/devicechain-io/postgresql-timescaledb:${pg_minor}-ts${TIMESCALEDB_VERSION}-r${IMAGE_REVISION}"
  # `tofu console` renders a string result WITH its quotes, so the expected value
  # carries them too. Comparing the raw tag would fail on every correct run.
  evaluates "\"$want_image\"" 'var.timescale_image'
else
  echo >&2 "MISSING: $versions_conf — cannot check the operand image tag against its source of truth."
  # NOT `((failures++))`: with failures at 0 that arithmetic command evaluates to 0,
  # exits 1, and `set -e` kills the script right here — losing the summary below.
  failures=$((failures + 1))
fi

# --- coverage: every validation block the root declares must be exercised -------
#
# 🔴 THE ASSERTION THAT KEEPS THIS FILE HONEST AS THE ROOT GROWS. `tofu validate`
# never runs a validation block, so a new one is dead configuration until something
# supplies a non-default value — and this script is the only thing that does. Five
# of the six blocks in variables.tf had never been executed by anything when this
# control was added: they could have been inverted, or referencing the wrong
# variable, through every gate the repo runs.
#
# BOTH directions are required per variable, because each catches a block the other
# cannot see. With no `rejects`, a condition hard-wired to `true` is a guard that
# guards nothing. With no `accepts`, a condition hard-wired to `false` refuses every
# apply — including the shipped topology.
#
# Read EVERY root .tf file, not just variables.tf, and cross-check the count. A
# parser is the wrong place to be lenient: anything it fails to match is silently
# EXEMPTED from the requirement, which is the same green-for-nothing shape one
# level up. So the blocks it attributes to a variable are counted against the raw
# number of `validation {` blocks in the same files, and a mismatch is a failure
# naming the parser rather than a quietly smaller coverage set.
guarded="$(awk '/^variable "/ { v = $2; gsub(/"/, "", v) } /^[[:space:]]*validation[[:space:]]*\{/ { if (v != "") print v }' \
  "$tofu_dir"/*.tf | sort -u)"
attributed="$(awk '/^variable "/ { v = $2 } /^[[:space:]]*validation[[:space:]]*\{/ { if (v != "") n++ } END { print n + 0 }' \
  "$tofu_dir"/*.tf)"
declared="$(grep -hcE '^[[:space:]]*validation[[:space:]]*\{' "$tofu_dir"/*.tf | paste -sd+ | bc)"
if [[ -z "$guarded" ]]; then
  # The control's own control: a parser that matches nothing reports full coverage.
  echo "FAIL  found no validation blocks in the root .tf files — the coverage check below" \
    "is reading nothing, so it cannot fail. Fix the parser." >&2
  failures=$((failures + 1))
elif [[ "$attributed" != "$declared" ]]; then
  echo "FAIL  the root declares $declared validation block(s) but only $attributed could be" \
    "attributed to a variable. The rest are exempt from the coverage requirement below" \
    "without anything saying so — fix the parser rather than the count." >&2
  failures=$((failures + 1))
fi
covered=0
while read -r v; do
  [[ -n "$v" ]] || continue
  r=""; a=""
  printf '%s\n' "${rejected_vars[@]}" | grep -qx -- "$v" || r="no rejects"
  printf '%s\n' "${accepted_vars[@]}" | grep -qx -- "$v" || a="no accepts"
  if [[ -n "$r" || -n "$a" ]]; then
    echo "FAIL  variable \"$v\" declares a validation block that this run never exercised" \
      "(${r:-ok}, ${a:-ok}). Nothing else in this repo evaluates it: \`tofu validate\`" \
      "reads variables at their defaults, so the block could be inverted or reference" \
      "the wrong variable and every gate would stay green." >&2
    failures=$((failures + 1))
    continue
  fi
  covered=$((covered + 1))
done <<<"$guarded"
echo "ok    all $covered validated variable(s) exercised in both directions"

if ((failures > 0)); then
  echo >&2
  echo "$failures validation assertion(s) failed." >&2
  exit 1
fi
echo
echo "All OpenTofu variable validations behave as declared."
