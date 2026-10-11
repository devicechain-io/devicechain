#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: every alert the chart ships is named on a published docs page, in every
# locale.
#
# WHY. An alert is a page at 3 a.m., and the person it wakes has one question: what
# does this mean and what do I do? The answer lives on a docs page or nowhere. The
# chart's PrometheusRule templates and the docs are edited by different people at
# different times, and nothing tied them together: a quarter of the shipped alerts
# were found with no mention anywhere in the docs. This script makes "I added an
# alert" and "I wrote its runbook entry" one change.
#
# WHAT IT CHECKS. The names come from `alert:` lines in
# deploy/helm/devicechain/templates/prometheusrule*.yaml. Each must appear as a whole
# word in at least one page under docs/docs, docs/i18n/es/.../current and
# docs/i18n/zh-CN/.../current. Alert names are identifiers and are never translated,
# so a missing name in a locale means the row was not mirrored.
#
# EXCEPTIONS live in hack/docs-alert-allowlist.txt, one name per line with a
# `#` reason above it. An entry that names no real alert, or one that is now
# documented, FAILS: the list may only shrink.
#
# 🔴 SCOPE. It proves the NAME appears, not that the text around it is a good
# runbook entry. A name in a code block satisfies it. Read a green run as "no alert
# is undocumented", never as "every alert is explained well".
#
# 🔴 A SCANNER FAILS BY REPORTING CLEAN, so it refuses to run on an empty alert
# list, and --self-test plants a gap in a copy of the tree and requires the check to
# name it.
#
#   hack/check-docs-alert-coverage.sh              # check
#   hack/check-docs-alert-coverage.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# check <repo-root> — prints problems, returns 1 if any.
check() {
  local repo="$1" rules allow names n problems=0 tree loc
  rules="$repo/deploy/helm/devicechain/templates"
  allow="$repo/hack/docs-alert-allowlist.txt"

  names="$(grep -hoE '^[[:space:]]*-?[[:space:]]*alert:[[:space:]]*[A-Za-z0-9_]+' "$rules"/prometheusrule*.yaml |
    sed -E 's/.*alert:[[:space:]]*//' | LC_ALL=C sort -u)"
  if [ -z "$names" ]; then
    echo "::error::found no alert names under $rules — the extraction is broken, not the chart" >&2
    return 1
  fi

  local trees=("$repo/docs/docs")
  for loc in "$repo"/docs/i18n/*/docusaurus-plugin-content-docs/current; do
    [ -d "$loc" ] && trees+=("$loc")
  done

  local allowed=""
  if [ -f "$allow" ]; then
    allowed="$(grep -vE '^[[:space:]]*(#|$)' "$allow" | sed -E 's/[[:space:]]+$//' | LC_ALL=C sort -u || true)"
  fi

  for n in $allowed; do
    if ! printf '%s\n' "$names" | grep -qx "$n"; then
      echo "  allowlist entry $n is not an alert the chart ships; delete it"
      problems=$((problems + 1))
    fi
  done

  for n in $names; do
    local listed=0
    printf '%s\n' "$allowed" | grep -qx "$n" && listed=1
    local missing=()
    for tree in "${trees[@]}"; do
      if ! grep -rqwF --include='*.md' --include='*.mdx' -- "$n" "$tree"; then
        missing+=("${tree#"$repo"/}")
      fi
    done
    if [ "$listed" = 1 ]; then
      if [ "${#missing[@]}" = 0 ]; then
        echo "  allowlist entry $n is documented everywhere now; delete it"
        problems=$((problems + 1))
      fi
      continue
    fi
    if [ "${#missing[@]}" -gt 0 ]; then
      echo "  alert $n is not named under: ${missing[*]}"
      problems=$((problems + 1))
    fi
  done

  echo "checked $(printf '%s\n' "$names" | wc -l | tr -d ' ') alerts against ${#trees[@]} docs trees"
  [ "$problems" = 0 ]
}

self_test() {
  local tmp rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  mkdir -p "$tmp/deploy/helm/devicechain/templates" "$tmp/hack" \
    "$tmp/docs/docs" "$tmp/docs/i18n/es/docusaurus-plugin-content-docs/current" \
    "$tmp/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current"
  cat >"$tmp/deploy/helm/devicechain/templates/prometheusrule-x.yaml" <<'EOF'
        - alert: DocumentedAlert
          expr: up == 0
        - alert: PlantedGapAlert
          expr: up == 0
EOF
  local d
  for d in docs/docs docs/i18n/es/docusaurus-plugin-content-docs/current docs/i18n/zh-CN/docusaurus-plugin-content-docs/current; do
    echo 'The `DocumentedAlert` alert.' >"$tmp/$d/page.md"
  done

  # 1. the planted gap must be named.
  if out="$(check "$tmp" 2>&1)"; then
    echo "SELF-TEST FAILED: a shipped alert with no docs mention passed" >&2
    return 1
  fi
  printf '%s\n' "$out" | grep -q 'alert PlantedGapAlert is not named' ||
    { echo "SELF-TEST FAILED: the gap was not named: $out" >&2; return 1; }

  # 2. documented in English only: the other locales must be reported.
  echo 'The `PlantedGapAlert` alert.' >"$tmp/docs/docs/page2.md"
  out="$(check "$tmp" 2>&1 || true)"
  printf '%s\n' "$out" | grep -q 'PlantedGapAlert is not named under: docs/i18n/es' ||
    { echo "SELF-TEST FAILED: a missing translation was not named: $out" >&2; return 1; }

  # 3. documented everywhere: passes.
  for d in docs/i18n/es/docusaurus-plugin-content-docs/current docs/i18n/zh-CN/docusaurus-plugin-content-docs/current; do
    echo 'The `PlantedGapAlert` alert.' >"$tmp/$d/page2.md"
  done
  check "$tmp" >/dev/null 2>&1 || { echo "SELF-TEST FAILED: a fully documented tree was rejected" >&2; return 1; }

  # 4. a stale allowlist entry fails.
  printf '# reason\nPlantedGapAlert\nNoSuchAlert\n' >"$tmp/hack/docs-alert-allowlist.txt"
  out="$(check "$tmp" 2>&1 || true)"
  printf '%s\n' "$out" | grep -q 'NoSuchAlert is not an alert' &&
    printf '%s\n' "$out" | grep -q 'PlantedGapAlert is documented everywhere' ||
    { echo "SELF-TEST FAILED: a stale allowlist was accepted: $out" >&2; return 1; }

  # 5. an allowlisted, undocumented alert passes.
  rm "$tmp"/docs/docs/page2.md "$tmp"/docs/i18n/*/docusaurus-plugin-content-docs/current/page2.md
  printf '# reason\nPlantedGapAlert\n' >"$tmp/hack/docs-alert-allowlist.txt"
  check "$tmp" >/dev/null 2>&1 || { echo "SELF-TEST FAILED: an allowlisted gap was rejected" >&2; return 1; }

  echo "self-test OK: a gap, a missing translation and a stale allowlist each fail; a documented tree passes"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") check "$ROOT" && echo "Every shipped alert is documented." ;;
  *) echo "usage: $0 [--self-test]" >&2; exit 2 ;;
esac
