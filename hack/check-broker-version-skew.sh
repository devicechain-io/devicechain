#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The broker DeviceChain ships is built on ONE nats-server version, and every other place
# this repository names a nats-server version must name the same one.
#
# WHY
#
# The broker image (backend/broker/dc-nats-server) is upstream nats-server's main on the
# upstream library, at the version its go.mod requires. Everything around it was verified
# against a version too, and nothing ties those to the library:
#
#   - the nats Helm CHART (deploy/opentofu: the module's chart_version default and the
#     instance root's nats_chart_version). The module is sized against chart internals — the
#     config checksum template, the reloader's watch list, the lame duck defaults — and chart
#     2.15.1 is the chart for nats-server 2.15.1. A chart bumped without the library, or the
#     library without the chart, runs a server under templates verified against another one.
#   - every other workspace module's nats-server requirement. The services' tests embed the
#     server to test against; a test suite on 2.16 proves nothing about a broker on 2.15.
#   - the documented upstream ROLLBACK image (the module's comment and the root variable's
#     description) and any image a script runs as "the upstream broker": a rollback to
#     another version is a rollback to an untested server.
#
# Dependabot moves the go.mod requirements together (one grouped PR), and nothing else: it
# does not read the chart pins or the image tags. So the bump that matters most — the library
# under the broker — is the one most likely to arrive alone. This makes that a failure that
# names every pin to move with it.
#
# EXCEPTIONS are listed below with a reason, and an exception whose file no longer names a
# nats-server version FAILS, so the list can only shrink.
#
#   hack/check-broker-version-skew.sh              # check
#   hack/check-broker-version-skew.sh --self-test  # prove each defect is caught, alone

set -euo pipefail

case "${1:-}" in
  "" | --self-test) ;;
  *)
    echo "usage: $(basename "$0") [--self-test]" >&2
    exit 2
    ;;
esac

# Files that name a nats-server version which is deliberately NOT the broker's, with why.
# Matched as whole paths relative to the repository root.
EXCEPTIONS=(
  # A maintainer's measurement of the event store's purge cost. The broker there is a
  # fixture the measurement runs beside, on the version the recorded baselines were taken
  # with; moving it would change what the script compares against, not what ships.
  "hack/purge-measure.sh"
)

is_exception() {
  local f="$1" e
  for e in "${EXCEPTIONS[@]}"; do
    [ "$f" = "$e" ] && return 0
  done
  return 1
}

# check <root> — prints every disagreement, returns 1 if there is any (2 if it cannot run).
check() {
  local root="$1" ours bad=0 n=0 f v line
  local broker_mod="$root/backend/broker/dc-nats-server/go.mod"
  [ -f "$broker_mod" ] || { echo "FAIL: $broker_mod not found; nothing to compare against" >&2; return 2; }
  ours="$(awk '$1 == "github.com/nats-io/nats-server/v2" { print $2 } $1 == "require" && $2 == "github.com/nats-io/nats-server/v2" { print $3 }' "$broker_mod" | sort -u)"
  if [ "$(printf '%s\n' "$ours" | grep -c .)" -ne 1 ]; then
    echo "FAIL: the broker's go.mod must require exactly one nats-server version, found: '${ours//$'\n'/ }'" >&2
    return 2
  fi
  ours="${ours#v}"
  echo "the broker is built on nats-server $ours"

  # 1. Every workspace module's nats-server requirement (direct or indirect).
  while read -r mod; do
    f="$root/${mod#./}/go.mod"
    [ -f "$f" ] || { echo "FAIL: go.work names $mod, which has no go.mod" >&2; bad=1; continue; }
    v="$(awk '$1 == "github.com/nats-io/nats-server/v2" { print $2 } $1 == "require" && $2 == "github.com/nats-io/nats-server/v2" { print $3 }' "$f" | sort -u)"
    [ -n "$v" ] || continue
    n=$((n + 1))
    if [ "$v" != "v$ours" ]; then
      echo "SKEW: ${mod#./}/go.mod requires nats-server $v" >&2
      bad=1
    fi
  done < <(cd "$root" && bash hack/list-workspace-modules.sh)

  # 2. The chart pins: the module's default and the root's.
  local chart_pins=(
    "deploy/opentofu/modules/nats/main.tf:chart_version"
    "deploy/opentofu/instance/variables.tf:nats_chart_version"
  )
  local spec file var
  for spec in "${chart_pins[@]}"; do
    file="${spec%%:*}"
    var="${spec#*:}"
    [ -f "$root/$file" ] || { echo "FAIL: $file not found" >&2; bad=1; continue; }
    v="$(awk -v want="$var" '
      $1 == "variable" && $2 == "\"" want "\"" { inv = 1; next }
      inv && /^}/ { inv = 0 }
      inv && $1 == "default" { gsub(/"/, "", $3); print $3; exit }' "$root/$file")"
    if [ -z "$v" ]; then
      echo "FAIL: found no default for $var in $file; the walk is blind to that pin" >&2
      bad=1
      continue
    fi
    n=$((n + 1))
    if [ "${v#v}" != "$ours" ]; then
      echo "SKEW: $file $var defaults to $v" >&2
      bad=1
    fi
  done

  # 3. Every upstream nats-server image a tracked file names: `nats:<ver>` references and
  #    the rollback tag "<ver>-alpine" in the nats module and the instance root. An image
  #    reference needs a dotted version and no name character before `nats`, so
  #    `dc-nats:4222` (a host and a port) is not one; `${X:-nats:2.12}` is. This file is
  #    skipped: its self-test plants literals of exactly that shape.
  local hits
  hits="$(cd "$root" && git ls-files -- backend deploy hack .github |
    grep -v -e '_test\.go$' -e '/testdata/' -e '\.sum$' -e '^hack/check-broker-version-skew\.sh$' |
    xargs grep -HnoE '((^|[^A-Za-z0-9._-]|:-)nats:[0-9]+\.[0-9]+[0-9.]*|"[0-9]+\.[0-9]+\.[0-9]+-alpine"|\\"[0-9]+\.[0-9]+\.[0-9]+-alpine\\")' 2>/dev/null || true)"
  local seen_exceptions=()
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    f="${line%%:*}"
    v="${line##*:}"
    v="${v//\\/}"
    v="${v//\"/}"
    v="${v#nats:}"
    v="${v%-alpine}"
    if is_exception "$f"; then
      seen_exceptions+=("$f")
      continue
    fi
    # Only the two places the rollback tag is documented carry a bare "<ver>-alpine";
    # anywhere else that shape is not about this broker.
    case "$line" in
      *-alpine*) case "$f" in
        deploy/opentofu/modules/nats/main.tf | deploy/opentofu/instance/variables.tf | hack/check-tofu-validations.sh) ;;
        *) case "$line" in *nats:*) ;; *) continue ;; esac ;;
        esac ;;
    esac
    n=$((n + 1))
    if [ "$v" != "$ours" ]; then
      echo "SKEW: $f names upstream nats-server $v ($line)" >&2
      bad=1
    fi
  done <<<"$hits"

  local e found
  for e in "${EXCEPTIONS[@]}"; do
    found=0
    for f in "${seen_exceptions[@]+"${seen_exceptions[@]}"}"; do
      [ "$f" = "$e" ] && found=1
    done
    if [ "$found" -eq 0 ]; then
      echo "STALE EXCEPTION: $e names no nats-server version any more; remove it from EXCEPTIONS" >&2
      bad=1
    fi
  done

  # A walk that found almost nothing is a walk that went blind, not a clean tree: the
  # broker's own go.mod is not counted, so the floor is the chart pins plus at least the
  # rollback documentation and one other module.
  if [ "$n" -lt 5 ]; then
    echo "FAIL: compared only $n pin(s) against the broker's version; the walk is not reading the tree" >&2
    return 2
  fi
  if [ "$bad" -ne 0 ]; then
    cat >&2 <<EOF

The broker is built on nats-server $ours, and the pins above name another version. Move them
together: the library under the broker (backend/broker/dc-nats-server/go.mod) and in every
workspace module, the nats chart (chart_version in deploy/opentofu/modules/nats and
nats_chart_version in deploy/opentofu/instance), and the rollback image tag documented beside
them. Re-check the chart's templates before raising it (see chart_version's description).
EOF
    return 1
  fi
  echo "OK: $n nats-server pin(s) agree with the broker's $ours"
}

root="$(git rev-parse --show-toplevel)"

if [ "${1:-}" != "--self-test" ]; then
  check "$root"
  exit $?
fi

# ---------------------------------------------------------------------------
# SELF-TEST: a copy of the tracked tree, each defect planted ALONE, each grep-asserted to
# have applied before the verdict is believed.
# ---------------------------------------------------------------------------
st="$(mktemp -d)"
trap 'rm -rf "$st"' EXIT
fresh() {
  rm -rf "$st/t"
  mkdir -p "$st/t"
  (cd "$root" && git ls-files -z -- go.work backend/*/*/go.mod backend/*/go.mod deploy/go.mod examples/*/go.mod deploy/opentofu hack .github |
    xargs -0 -I{} cp --parents {} "$st/t/")
  (cd "$st/t" && git init -q && git add -A >/dev/null 2>&1)
}
expect_fail() {
  local label="$1"
  if check "$st/t" >/dev/null 2>&1; then
    echo "SELF-TEST FAILED: $label was not caught" >&2
    exit 1
  fi
  echo "  ok: $label is caught"
}

fresh
check "$st/t" >/dev/null || { echo "SELF-TEST FAILED: the clean copy does not pass" >&2; exit 1; }
echo "  ok: the clean tree passes"

fresh
sed -i -E 's#(github.com/nats-io/nats-server/v2) v[0-9.]+#\1 v9.9.9#' "$st/t/backend/core/go.mod"
grep -q 'nats-server/v2 v9.9.9' "$st/t/backend/core/go.mod" || { echo "mutation 1 did not apply" >&2; exit 1; }
(cd "$st/t" && git add -A)
expect_fail "a workspace module on another nats-server"

fresh
sed -i -E '/^variable "nats_chart_version"/,/^}/ s/default( *)= "[^"]+"/default\1= "2.99.0"/' "$st/t/deploy/opentofu/instance/variables.tf"
grep -q '"2.99.0"' "$st/t/deploy/opentofu/instance/variables.tf" || { echo "mutation 2 did not apply" >&2; exit 1; }
(cd "$st/t" && git add -A)
expect_fail "the root's chart pin moved alone"

fresh
sed -i -E '/^variable "chart_version"/,/^}/ s/default( *)= "[^"]+"/default\1= "2.99.0"/' "$st/t/deploy/opentofu/modules/nats/main.tf"
grep -q '"2.99.0"' "$st/t/deploy/opentofu/modules/nats/main.tf" || { echo "mutation 3 did not apply" >&2; exit 1; }
(cd "$st/t" && git add -A)
expect_fail "the module's chart pin moved alone"

fresh
sed -i -E 's/server_image_tag        = "[0-9.]+-alpine"/server_image_tag        = "2.99.0-alpine"/' "$st/t/deploy/opentofu/modules/nats/main.tf"
grep -q '"2.99.0-alpine"' "$st/t/deploy/opentofu/modules/nats/main.tf" || { echo "mutation 4 did not apply" >&2; exit 1; }
(cd "$st/t" && git add -A)
expect_fail "the documented rollback image on another version"

fresh
printf 'IMAGE="nats:2.99.0-alpine@sha256:%064d"\n' 0 >"$st/t/hack/planted.sh"
(cd "$st/t" && git add -A)
expect_fail "a script running another upstream nats image"

fresh
sed -i -E 's/nats:2\.12-alpine/nats-gone/' "$st/t/hack/purge-measure.sh"
grep -q 'nats:2\.12' "$st/t/hack/purge-measure.sh" && { echo "mutation 6 did not apply" >&2; exit 1; }
(cd "$st/t" && git add -A)
expect_fail "an exception that no longer names a version (stale)"

fresh
rm "$st/t/deploy/opentofu/instance/variables.tf"
(cd "$st/t" && git add -A)
expect_fail "a chart pin file the walk can no longer read"

echo "self-test passed"
