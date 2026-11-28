#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Resolves one leg of the `go` job into the arguments its `go test` and race steps pass.
#
#   hack/go-leg.sh <module> <out-file>   # reads LEG_PKGS, LEG_EXCLUDE, LEG_SHARD, LEG_TESTS, LEG_SKIP_TESTS;
#                                        # writes one arg per line
#   hack/go-leg.sh --self-test           # the shard partition and the catch-all, against cases that must fail
#
# The leg comes from hack/ci-select.py's plan, one of four shapes:
#
#   LEG_PKGS=""        LEG_EXCLUDE="./messaging/"  -> the catch-all: `go list ./...` MINUS the split packages
#   LEG_PKGS="./x/ ./y/"                           -> exactly those packages (a reader-selected leg)
#   LEG_PKGS="./messaging/" LEG_SHARD="2/3"        -> shard 2 of 3 of one package's top-level tests,
#                         LEG_SKIP_TESTS="TestX"   ->   dealt from the list WITHOUT TestX (ci-select ALONE)
#   LEG_PKGS="./messaging/" LEG_TESTS="TestX"      -> exactly TestX of that package, in a leg of its own
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
#
# 🔴 AN ALONE TEST IS SUBTRACTED BEFORE THE DEAL, AND RUN BY NAME. Every name in LEG_TESTS and
# LEG_SKIP_TESTS must be in the package's `go test -list` (a renamed test fails here, rather than
# its own leg running nothing while the shards deal it anyway), and a shard prints the arithmetic
# |all| = |dealt| + |alone|. The self-test pins that the shards plus the alone tests are the list.
set -euo pipefail

# shard_names <i> <n>: stdin is the test names, one per line; prints those in shard i of n.
shard_names() {
  LC_ALL=C sort -u | awk -v i="$1" -v n="$2" 'NF { if ((k++ % n) == i - 1) print }'
}

# without <names>: stdin is the test names, one per line; prints those not in the space-separated names.
without() {
  awk -v skip="$1" 'BEGIN { n = split(skip, s, " "); for (j = 1; j <= n; j++) drop[s[j]] = 1 } NF && !($0 in drop)'
}

# require_listed <all> <names> <what>: every name must be a listed test; a stale ALONE entry fails.
require_listed() {
  local t
  for t in $2; do
    grep -qxF -- "$t" <<<"$1" || { echo "::error::$3 $t is not among the package's tests (renamed or moved?); fix ALONE in hack/ci-select.py" >&2; return 1; }
  done
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
  # ALONE: the shards of the list without the alone tests, plus the alone tests, are the list once.
  local alone="Test007 Test031"
  union="$(tr ' ' '\n' <<<"$alone")"$'\n'
  for i in 1 2 3; do union+="$(without "$alone" <<<"$list" | shard_names "$i" 3)"$'\n'; done
  if [ "$(sed '/^$/d' <<<"$union" | LC_ALL=C sort)" != "$(LC_ALL=C sort <<<"$list")" ]; then
    echo "  FAIL: three shards without the alone tests, plus the alone tests, are not a partition of the list"; fails=1
  else
    echo "  ok: three shards without 2 alone tests, plus those 2, partition 47 tests"
  fi
  # The counterweight: dealing the shards from the WHOLE list runs each alone test twice.
  union="$(tr ' ' '\n' <<<"$alone")"$'\n'
  for i in 1 2 3; do union+="$(shard_names "$i" 3 <<<"$list")"$'\n'; done
  if [ "$(sed '/^$/d' <<<"$union" | LC_ALL=C sort)" = "$(LC_ALL=C sort <<<"$list")" ]; then
    echo "  FAIL: shards that kept the alone tests compared equal to the list"; fails=1
  else
    echo "  ok: shards that keep the alone tests run them twice, and the check sees it"
  fi
  if require_listed "$list" "Test007 TestGone" "self-test:" 2>/dev/null; then
    echo "  FAIL: an alone test the list does not print was accepted"; fails=1
  else
    echo "  ok: an alone test the list does not print is refused"
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
tests="${LEG_TESTS:-}"
skip="${LEG_SKIP_TESTS:-}"
args=()

if [ -n "$tests" ]; then
  if [ -n "$shard" ] || [ -n "$skip" ]; then
    echo "::error::an alone leg (LEG_TESTS) is no shard and skips nothing" >&2; exit 1
  fi
  read -r -a p <<<"$pkgs"
  [ "${#p[@]}" -eq 1 ] || { echo "::error::an alone leg names exactly one package, got: $pkgs" >&2; exit 1; }
  all="$(test_names "${p[0]}")"
  require_listed "$all" "$tests" "alone test"
  mine="$(tr ' ' '\n' <<<"$tests" | sed '/^$/d' | LC_ALL=C sort -u)"
  echo "leg: $module ${p[0]} alone runs $(grep -c . <<<"$mine") of $(grep -c . <<<"$all") top-level tests: ${tests}"
  args=(-run "^($(paste -sd'|' <<<"$mine"))\$" "${p[0]}")
elif [ -n "$shard" ]; then
  i="${shard%/*}"
  n="${shard#*/}"
  read -r -a p <<<"$pkgs"
  [ "${#p[@]}" -eq 1 ] || { echo "::error::a shard names exactly one package, got: $pkgs" >&2; exit 1; }
  all="$(test_names "${p[0]}")"
  [ -n "$all" ] || { echo "::error::${p[0]} lists no tests; a shard of it would run nothing" >&2; exit 1; }
  require_listed "$all" "$skip" "the shards skip"
  dealt="$(without "$skip" <<<"$all")"
  na="$(grep -c . <<<"$all")"; nd="$(grep -c . <<<"$dealt" || true)"; ns="$(wc -w <<<"$skip" | tr -d ' ')"
  [ "$na" -eq $((nd + ns)) ] || { echo "::error::${p[0]}: $na tests != $nd dealt + $ns alone" >&2; exit 1; }
  mine="$(shard_names "$i" "$n" <<<"$dealt")"
  [ -n "$mine" ] || { echo "::error::shard $shard of ${p[0]} is empty" >&2; exit 1; }
  echo "leg: $module ${p[0]} shard $shard runs $(grep -c . <<<"$mine") of the $nd top-level tests dealt ($na listed, $ns in legs of their own${skip:+: $skip})"
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
