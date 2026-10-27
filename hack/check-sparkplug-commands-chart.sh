#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The chart's derivation of command-delivery's Sparkplug capability flag, both ways.
#
# WHY. command-delivery decides what to do with a Sparkplug command from one mounted flag,
# `sparkplugCommands`. Nobody authors it: the chart sets it to true exactly when some Sparkplug source
# sets `commands.enabled`. The two failures are opposite and both are silent at render time:
#
#   - the flag left OFF while a source sends commands: every Sparkplug command is answered "this
#     transport has no downlink" while the adapter sits ready to send it, and the cause is a missing
#     line in a ConfigMap nobody reads;
#   - the flag ON with no source sending: commands are accepted, held in SENT and never answered,
#     where today they fail within seconds with an honest reason.
#
# So the chart test asserts the flag's value for the states that decide it, and that the combinations
# which cannot work (commands with no command-delivery, no way to reach it, a hand-set flag, a value
# the service would refuse at start) fail the RENDER. `helm lint` cannot see any of it: it reports a
# template `fail` as an INFO line and exits 0.
#
#   hack/check-sparkplug-commands-chart.sh              # check the chart
#   hack/check-sparkplug-commands-chart.sh --self-test  # prove the check can fail
#
# CHART=<dir> points the check at another copy of the chart (the self-test uses this).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${CHART:-$ROOT/deploy/helm/devicechain}"

say() { printf '==> %s\n' "$*"; }
bad() {
  printf '::error::%s\n' "$*" >&2
  exit 1
}

# render <values-file|-> [helm args...] prints the rendered manifest, or the helm error.
render() {
  local vf="$1"
  shift
  local args=(--set "instance.config.infrastructure.secrets.rootKey=$KEY")
  [ "$vf" = "-" ] || args+=(-f "$vf")
  helm template dc "$CHART" "${args[@]}" "$@" 2>&1
}

# cd_config prints command-delivery's rendered config document from a manifest on stdin.
cd_config() { grep -E '^  command-delivery: ' || true; }

KEY=""
TMP=""

# values <file> <commands-block-for-source-1> [<commands-block-for-source-2>]: a values file with a
# Sparkplug source per block, on an explicit area list that includes command-delivery.
values() {
  local f="$1"
  shift
  {
    echo 'enabledFunctionalAreas: [user-management, device-management, command-delivery, sparkplug-ingest]'
    echo 'functionalAreas:'
    echo '  sparkplug-ingest:'
    echo '    config:'
    echo '      sources:'
    local i=0 block
    for block in "$@"; do
      i=$((i + 1))
      echo "        - tenant: t$i"
      echo "          broker: {url: \"tcp://broker-$i:1883\"}"
      printf '%s\n' "$block"
    done
  } >"$f"
}

run_checks() {
  KEY="$(openssl rand -base64 32)"
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT

  local on='          commands: {enabled: true, hostRole: primary}'
  local off='          commands: {enabled: false}'
  local flag='sparkplugCommands'
  local out

  expect_flag() { # <label> <manifest>
    # The exact pair, not "the flag somewhere before a true": `"sparkplugCommands":false` beside any
    # other true value would satisfy that.
    case "$(cd_config <<<"$2")" in
      *'\"sparkplugCommands\":true'*) printf '  ok: %s\n' "$1" ;;
      *) bad "$1: command-delivery's config does not carry $flag=true: $(cd_config <<<"$2")" ;;
    esac
  }
  expect_no_flag() { # <label> <manifest>
    local cfg
    cfg="$(cd_config <<<"$2")"
    case "$cfg" in
      '') bad "$1: the manifest has no command-delivery config at all, so nothing was checked: ${2:0:300}" ;;
      *"$flag"*) bad "$1: command-delivery's config carries $flag when no source sends commands: $cfg" ;;
      *) printf '  ok: %s\n' "$1" ;;
    esac
  }
  expect_refused() { # <label> <fragment> <manifest-or-error> <rc>
    if [ "$4" -eq 0 ]; then bad "$1: rendered cleanly; the refusal did not fire"; fi
    case "$3" in
      *"$2"*) printf '  ok: %s\n' "$1" ;;
      *) bad "$1: refused for another reason: $3" ;;
    esac
  }

  say "the flag follows the sources"

  # The default profile has no Sparkplug area at all.
  out="$(render -)" || bad "the default chart did not render: $out"
  expect_no_flag "no Sparkplug area" "$out"

  values "$TMP/off.yaml" "$off"
  out="$(render "$TMP/off.yaml")" || bad "a source with commands off did not render: $out"
  expect_no_flag "a source with commands.enabled false" "$out"

  values "$TMP/none.yaml" '          autoRegister: false'
  out="$(render "$TMP/none.yaml")" || bad "a source with no commands block did not render: $out"
  expect_no_flag "a source with no commands block" "$out"

  values "$TMP/on.yaml" "$on"
  out="$(render "$TMP/on.yaml")" || bad "a commands-enabled source did not render: $out"
  expect_flag "a source with commands.enabled true" "$out"
  case "$out" in
    *'\"commands\":{\"enabled\":true,\"hostRole\":\"primary\"}'*) printf '  ok: the source keeps its commands block\n' ;;
    *) bad "the sparkplug-ingest config does not carry the source's commands block" ;;
  esac

  values "$TMP/second.yaml" "$off" "$on"
  out="$(render "$TMP/second.yaml")" || bad "two sources did not render: $out"
  expect_flag "only the second of two sources enables commands" "$out"

  # The service binds keys without regard to case, so a block spelled this way IS enabled there.
  values "$TMP/upper.yaml" '          Commands: {Enabled: true, HostRole: primary}'
  out="$(render "$TMP/upper.yaml")" || bad "a differently spelled block did not render: $out"
  expect_flag "Commands/Enabled spelled with capitals" "$out"

  # ...and the top-level key too: a capitalised Sources is the same key to the service.
  cat >"$TMP/capsources.yaml" <<'YAML'
enabledFunctionalAreas: [user-management, device-management, command-delivery, sparkplug-ingest]
functionalAreas:
  sparkplug-ingest:
    config:
      Sources:
        - tenant: t1
          broker: {url: "tcp://broker-1:1883"}
          commands: {enabled: true, hostRole: primary}
YAML
  out="$(render "$TMP/capsources.yaml")" || bad "a capitalised Sources key did not render: $out"
  expect_flag "Sources spelled with a capital" "$out"

  # Sources configured under an area that is not deployed are never read.
  values "$TMP/inert.yaml" "$on"
  out="$(render "$TMP/inert.yaml" --set 'enabledFunctionalAreas={user-management,device-management,command-delivery}')" ||
    bad "commands configured on an area that is not enabled did not render: $out"
  expect_no_flag "commands configured while sparkplug-ingest is not enabled" "$out"

  say "what cannot work fails the render"
  local rc

  rc=0
  out="$(render "$TMP/on.yaml" --set 'enabledFunctionalAreas={user-management,device-management,sparkplug-ingest}')" || rc=$?
  expect_refused "commands enabled with no command-delivery" "which needs command-delivery" "$out" "$rc"

  rc=0
  out="$(render "$TMP/on.yaml" --set instance.config.infrastructure.commandDelivery.hostname=)" || rc=$?
  expect_refused "commands enabled with no way to reach command-delivery" "could not reach command-delivery" "$out" "$rc"

  rc=0
  out="$(render "$TMP/on.yaml" --set instance.config.infrastructure.commandDelivery.port=0)" || rc=$?
  expect_refused "commands enabled with command-delivery's port unset" "could not reach command-delivery" "$out" "$rc"

  # The same key twice under different capitalisation: the service keeps one of them and the chart
  # cannot tell which, so it refuses instead of OR-ing them.
  cat >"$TMP/dup-sources.yaml" <<'YAML'
enabledFunctionalAreas: [user-management, device-management, command-delivery, sparkplug-ingest]
functionalAreas:
  sparkplug-ingest:
    config:
      sources:
        - tenant: t1
          broker: {url: "tcp://broker-1:1883"}
          commands: {enabled: false}
      Sources:
        - tenant: t1
          broker: {url: "tcp://broker-1:1883"}
          commands: {enabled: true, hostRole: primary}
YAML
  rc=0
  out="$(render "$TMP/dup-sources.yaml")" || rc=$?
  expect_refused "sources and Sources together" "more than once" "$out" "$rc"

  values "$TMP/dup-commands.yaml" '          commands: {enabled: false}
          Commands: {enabled: true, hostRole: primary}'
  rc=0
  out="$(render "$TMP/dup-commands.yaml")" || rc=$?
  expect_refused "commands and Commands together" "more than once" "$out" "$rc"

  values "$TMP/dup-enabled.yaml" '          commands: {enabled: false, Enabled: true, hostRole: primary}'
  rc=0
  out="$(render "$TMP/dup-enabled.yaml")" || rc=$?
  expect_refused "enabled and Enabled together" "more than once" "$out" "$rc"

  rc=0
  out="$(render "$TMP/on.yaml" --set-string 'functionalAreas.sparkplug-ingest.config.sources[0].commands.enabled=true')" || rc=$?
  expect_refused "commands.enabled as a string" "must be true or false" "$out" "$rc"

  rc=0
  out="$(render "$TMP/off.yaml" --set functionalAreas.command-delivery.config.sparkplugCommands=true)" || rc=$?
  expect_refused "a hand-set flag with no source sending" "cannot be set by hand" "$out" "$rc"

  rc=0
  out="$(render "$TMP/on.yaml" --set functionalAreas.command-delivery.config.SparkplugCommands=false)" || rc=$?
  expect_refused "a hand-set flag, in another spelling, beside a source that sends" "cannot be set by hand" "$out" "$rc"

  say "sparkplug-ingest commands chart check passed"
}

# ---------------------------------------------------------------------------
# --self-test: the check against copies of the chart with one defect planted each. Each must make
# run_checks fail, or the check proves nothing about the derivation it names.
# ---------------------------------------------------------------------------
self_test() {
  local n=0
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT

  plant() { # <label> <sed-expression> <file under the chart>
    local label="$1" expr="$2" file="$3" copy="$work/chart-$n"
    n=$((n + 1))
    cp -r "$ROOT/deploy/helm/devicechain" "$copy"
    sed -i "$expr" "$copy/$file"
    if cmp -s "$ROOT/deploy/helm/devicechain/$file" "$copy/$file"; then
      echo "SELF-TEST BROKEN: the planted defect '$label' changed nothing" >&2
      exit 1
    fi
    local seen
    if seen="$(CHART="$copy" "$0" 2>&1)"; then
      echo "SELF-TEST FAILED: the check passed with this defect planted: $label" >&2
      exit 1
    fi
    # Say WHAT it failed on, so a defect that merely broke the template's syntax is visible as such.
    echo "  ok: caught: $label  [$(grep -m1 '^::error::' <<<"$seen" | cut -c1-110)]"
  }

  say "the clean chart passes"
  "$0" >/dev/null || {
    echo "SELF-TEST FAILED: the clean chart does not pass" >&2
    exit 1
  }

  say "each defect planted alone"
  local h=templates/_helpers.tpl
  plant "the flag is never derived" 's/{{- if include "devicechain.sparkplugCommandsEnabled" . -}}/{{- if false -}}/' "$h"
  plant "the flag is always derived" 's/{{- if include "devicechain.sparkplugCommandsEnabled" . -}}/{{- if true -}}/' "$h"
  plant "the source flag is read inverted" 's/{{- if \$cv -}}{{- \$on = true -}}{{- end -}}/{{- if not $cv -}}{{- $on = true -}}{{- end -}}/' "$h"
  plant "key case is not folded" 's/{{- if eq (lower \$ck) "enabled" -}}/{{- if eq $ck "enabled" -}}/' "$h"
  plant "only the first source is read" 's/{{- range \$src := (\$sources | default list) -}}/{{- range $src := (slice ($sources | default list) 0 1) -}}/' "$h"
  plant "a hand-set flag is accepted" 's/{{- if eq (lower \$k) "sparkplugcommands" -}}/{{- if eq (lower $k) "never-matches" -}}/' "$h"
  plant "commands without command-delivery render" 's/{{- if not (has "command-delivery" \$areas) -}}/{{- if false -}}/' "$h"
  plant "an unreachable command-delivery renders" 's/{{- if or (not \$cd.hostname) (not \$cd.port) -}}/{{- if false -}}/' "$h"
  plant "top-level sources is matched case-sensitively" 's/{{- if eq (lower \$k) "sources" -}}/{{- if eq $k "sources" -}}/' "$h"
  plant "an unset command-delivery port renders" 's/{{- if or (not \$cd.hostname) (not \$cd.port) -}}/{{- if not $cd.hostname -}}/' "$h"
  plant "an unset command-delivery hostname renders" 's/{{- if or (not \$cd.hostname) (not \$cd.port) -}}/{{- if not $cd.port -}}/' "$h"
  plant "case-folded duplicate keys are accepted" 's/{{- if gt \$n 1 -}}/{{- if false -}}/' "$h"
  plant "a non-boolean enabled renders" 's/{{- if not (kindIs "bool" \$cv) -}}/{{- if false -}}/' "$h"
  plant "the refusals are not evaluated without command-delivery" '/^{{- \$_ := include "devicechain.sparkplugCommandsEnabled" \$root -}}$/d' "$h"

  echo "self-test passed: $n defects, each planted alone, each caught; the clean chart passes"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") run_checks ;;
  *)
    echo "usage: $0 [--self-test]" >&2
    exit 2
    ;;
esac
