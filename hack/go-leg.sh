#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Resolves one leg of the `go` job into the arguments its `go test` and race steps pass.
#
#   hack/go-leg.sh <module> <out-file>   # reads LEG_PKGS, LEG_EXCLUDE, LEG_SHARD; writes one arg per line
#   hack/go-leg.sh --self-test           # the shard partition and the catch-all, against cases that must fail
#
# The leg comes from hack/ci-select.py's plan, one of three shapes:
#
#   LEG_PKGS=""        LEG_EXCLUDE="./messaging/"  -> the catch-all: `go list ./...` MINUS the split packages
#   LEG_PKGS="./x/ ./y/"                           -> exactly those packages (a reader-selected leg)
#   LEG_PKGS="./messaging/" LEG_SHARD="2/3"        -> shard 2 of 3 of one package's top-level tests
#
# 🔴 THE CATCH-ALL IS A SUBTRACTION, NOT A LIST. It is computed here, in the leg, from the
# package loader's own `go list ./...`, so a package added tomorrow lands in it with no edit
# anywhere. Every excluded package must resolve to exactly one package of that list -- a stale
# split (a package renamed or moved) fails here rather than silently running nowhere -- and
# the leg prints the arithmetic: |all| = |run here| + |split out|.
#
# 🔴 A SHARD IS A ROUND-ROBIN OVER THE SORTED `go test -list` OUTPUT. Every shard of a package
# runs in its own job on the same commit, so each derives the same list and takes its own
# residue class: the shards are disjoint and their union is the list by construction (the
# self-test pins both). A shard that would run nothing fails, since `-run` matching nothing
# exits 0 with "no tests to run", indistinguishable from a pass.
set -euo pipefail

# shard_names <i> <n>: stdin is the test names, one per line; prints those in shard i of n.
shard_names() {
  LC_ALL=C sort -u | awk -v i="$1" -v n="$2" 'NF { if ((k++ % n) == i - 1) print }'
}

# test_names <pkg>: the top-level tests, examples and fuzz targets `go test -run` can select.
test_names() {
  go test -list '.*' "$1" | tr -d '\r' | grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' || true
}

self_test() {
  local list n i got union fails=0
  list="$(printf 'Test%03d\n' $(seq 1 47))"
  for n in 1 2 3 4; do
    union=""
    for i in $(seq 1 "$n"); do
      got="$(shard_names "$i" "$n" <<<"$list")"
      [ -n "$got" ] || { echo "  FAIL: shard $i/$n is empty"; fails=1; }
      union+="$got"$'\n'
    done
    if [ "$(sed '/^$/d' <<<"$union" | LC_ALL=C sort)" != "$(LC_ALL=C sort <<<"$list")" ]; then
      echo "  FAIL: the $n shards are not a partition of the list (a test lost or run twice)"; fails=1
    else
      echo "  ok: $n shards partition 47 tests"
    fi
  done
  # The counterweight: a partition check that cannot fail proves nothing. Drop the last shard.
  union=""
  for i in 1 2; do union+="$(shard_names "$i" 3 <<<"$list")"$'\n'; done
  if [ "$(sed '/^$/d' <<<"$union" | LC_ALL=C sort)" = "$(LC_ALL=C sort <<<"$list")" ]; then
    echo "  FAIL: two of three shards compared equal to the whole list"; fails=1
  else
    echo "  ok: two of three shards are not the whole list"
  fi
  # Order-independence: every leg must agree whatever order `go test -list` printed.
  if [ "$(shard_names 2 3 <<<"$list")" != "$(shard_names 2 3 < <(sort -r <<<"$list"))" ]; then
    echo "  FAIL: the shard depends on input order"; fails=1
  else
    echo "  ok: input order does not move a test between shards"
  fi
  [ "$fails" = 0 ] || { echo "go-leg self-test FAILED"; exit 1; }
  echo "go-leg self-test passed"
}

if [ "${1:-}" = --self-test ]; then self_test; exit 0; fi
[ $# -eq 2 ] || { echo "usage: $0 <module> <out-file> | --self-test" >&2; exit 2; }
module="$1"
out="$2"
cd "$(dirname "${BASH_SOURCE[0]}")/../$module"
pkgs="${LEG_PKGS:-}"
exclude="${LEG_EXCLUDE:-}"
shard="${LEG_SHARD:-}"
args=()

if [ -n "$shard" ]; then
  i="${shard%/*}"
  n="${shard#*/}"
  read -r -a p <<<"$pkgs"
  [ "${#p[@]}" -eq 1 ] || { echo "::error::a shard names exactly one package, got: $pkgs" >&2; exit 1; }
  all="$(test_names "${p[0]}")"
  [ -n "$all" ] || { echo "::error::${p[0]} lists no tests; a shard of it would run nothing" >&2; exit 1; }
  mine="$(shard_names "$i" "$n" <<<"$all")"
  [ -n "$mine" ] || { echo "::error::shard $shard of ${p[0]} is empty" >&2; exit 1; }
  echo "leg: $module ${p[0]} shard $shard runs $(grep -c . <<<"$mine") of $(grep -c . <<<"$all") top-level tests"
  args=(-run "^($(paste -sd'|' <<<"$mine"))\$" "${p[0]}")
elif [ -n "$pkgs" ]; then
  read -r -a args <<<"$pkgs"
  echo "leg: $module packages ${args[*]}"
else
  full="$(go list ./... | tr -d '\r' | LC_ALL=C sort -u)"
  [ -n "$full" ] || { echo "::error::go list ./... returned nothing in $module" >&2; exit 1; }
  split=""
  for e in $exclude; do
    ip="$(go list "$e" | tr -d '\r')"
    grep -qxF -- "$ip" <<<"$full" || { echo "::error::split package $e ($ip) is not in $module's go list ./...; fix SPLITS in hack/ci-select.py" >&2; exit 1; }
    split+="$ip"$'\n'
  done
  split="$(sed '/^$/d' <<<"$split" | LC_ALL=C sort -u)"
  if [ -n "$split" ]; then
    rest="$(LC_ALL=C comm -23 <(echo "$full") <(echo "$split"))"
  else
    rest="$full"
  fi
  [ -n "$rest" ] || { echo "::error::the catch-all of $module is empty" >&2; exit 1; }
  nf="$(grep -c . <<<"$full")"; nr="$(grep -c . <<<"$rest")"; ns="$(grep -c . <<<"$split" || true)"
  if [ "$nf" -ne $((nr + ns)) ]; then
    echo "::error::$module: $nf packages != $nr in the catch-all + $ns split out" >&2
    exit 1
  fi
  echo "leg: $module catch-all runs $nr of $nf packages; split out to their own legs: ${split//$'\n'/ }"
  if [ -z "$split" ]; then
    args=(./...)
  else
    mapfile -t args <<<"$rest"
  fi
fi
printf '%s\n' "${args[@]}" > "$out"
