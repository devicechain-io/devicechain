#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Stamp the Artifact Hub prerelease flag before packaging; helm cannot set annotations.
#
#   hack/stamp-chart-prerelease.sh <Chart.yaml path> <chart version>
#   hack/stamp-chart-prerelease.sh deploy/helm/devicechain/Chart.yaml 1.4.0-rc.1
#   hack/stamp-chart-prerelease.sh --self-test
set -euo pipefail

stamp() (
  local file="$1" version="$2" expected=false tmp line newline in_annotations=false
  local found=false
  # Same version shape as the release guard, with its leading v already stripped.
  if [[ "$version" == v* ]]; then
    echo "error: chart version must not have a leading v: $version" >&2
    exit 1
  fi
  if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "error: invalid chart version '$version' (expected MAJOR.MINOR.PATCH[-prerelease])" >&2
    exit 1
  fi
  [[ "$version" != *-* ]] || expected=true
  if ! grep -qx 'annotations:' "$file"; then
    echo "error: $file requires a top-level annotations: line" >&2
    exit 1
  fi

  tmp="$(mktemp "${file}.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  # Look only inside the annotations mapping when deciding whether to insert.
  if awk '
    /^annotations:$/ { inside = 1; next }
    /^[^[:space:]#]/ { inside = 0 }
    inside && /^[[:space:]]+artifacthub[.]io\/prerelease:/ { found = 1 }
    END { exit !found }
  ' "$file"; then
    found=true
  fi
  # read/printf retain each original line, including CR bytes and a missing final LF.
  while :; do
    newline=true
    IFS= read -r line || { newline=false; [ -n "$line" ] || break; }
    if [ "$line" = 'annotations:' ]; then
      in_annotations=true
      printf '%s\n' "$line"
      if [ "$found" = false ]; then
        printf '  artifacthub.io/prerelease: "%s"\n' "$expected"
      fi
    else
      if [[ "$line" =~ ^[^[:space:]#] ]]; then in_annotations=false; fi
      if [ "$in_annotations" = true ] && [[ "$line" =~ ^[[:space:]]+artifacthub\.io/prerelease: ]]; then
        printf '  artifacthub.io/prerelease: "%s"' "$expected"
      else
        printf '%s' "$line"
      fi
      [ "$newline" = false ] || printf '\n'
    fi
    [ "$newline" = true ] || break
  done < "$file" > "$tmp"
  mv "$tmp" "$file"
  if [ "$(grep -cFx "  artifacthub.io/prerelease: \"$expected\"" "$file" || true)" != 1 ] ||
     [ "$(awk '!/^[[:space:]]*#/ && /artifacthub[.]io\/prerelease/ { n++ } END { print n+0 }' "$file")" != 1 ]; then
    echo "error: $file must contain exactly one artifacthub.io/prerelease line with value \"$expected\"" >&2
    exit 1
  fi
  echo "stamped artifacthub.io/prerelease: \"$expected\" ($version) into $file"
)

self_test() (
  local root work original delta
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  original="$root/deploy/helm/devicechain/Chart.yaml"
  fail() { echo "self-test: FAIL ($1)" >&2; exit 1; }
  check_flag() {
    [ "$(grep -cFx "  artifacthub.io/prerelease: \"$2\"" "$1" || true)" = 1 ] &&
      [ "$(grep -cF 'artifacthub.io/prerelease:' "$1" || true)" = 1 ]
  }

  cp "$original" "$work/rc.yaml" || fail 'a: copy'
  stamp "$work/rc.yaml" 1.4.0-rc.1 || fail 'a: stamp rc'
  check_flag "$work/rc.yaml" true || fail 'a: rc flag'
  cp "$original" "$work/final.yaml" || fail 'b: copy'
  stamp "$work/final.yaml" 1.4.0 || fail 'b: stamp final'
  check_flag "$work/final.yaml" false || fail 'b: final flag'
  stamp "$work/rc.yaml" 1.4.0 || fail 'c: re-stamp'
  check_flag "$work/rc.yaml" false || fail 'c: exactly one false after re-stamp'

  # Exactly one addition and no deletions; removing that addition restores every byte.
  delta="$(diff -u "$original" "$work/final.yaml" || [ "$?" = 1 ])" || fail 'd: diff'
  [ "$(printf '%s\n' "$delta" | grep -c '^+[^+]' || true)" = 1 ] || fail 'd: added line count'
  [ "$(printf '%s\n' "$delta" | grep -c '^-[^-]' || true)" = 0 ] || fail 'd: deleted line count'
  sed '/^  artifacthub\.io\/prerelease: "false"$/d' "$work/final.yaml" > "$work/restored.yaml"
  cmp -s "$original" "$work/restored.yaml" || fail 'd: unchanged bytes'

  if stamp "$work/final.yaml" v1.4.0 > /dev/null 2>&1; then fail 'e: leading v accepted'; fi
  if stamp "$work/final.yaml" 1.4 > /dev/null 2>&1; then fail 'f: garbage version accepted'; fi
  sed '/^annotations:$/d' "$original" > "$work/missing.yaml"
  if stamp "$work/missing.yaml" 1.4.0 > /dev/null 2>&1; then fail 'g: missing annotations accepted'; fi

  if command -v helm > /dev/null 2>&1; then
    # Keep Helm's configuration and caches inside the disposable test directory too.
    export HELM_CONFIG_HOME="$work/helm/config" HELM_CACHE_HOME="$work/helm/cache"
    export HELM_DATA_HOME="$work/helm/data" HELM_REGISTRY_CONFIG="$work/helm/registry.json"
    export DOCKER_CONFIG="$work/docker"
    cp -R "$root/deploy/helm/devicechain" "$work/chart" || fail 'h: copy chart'
    stamp "$work/chart/Chart.yaml" 1.4.0-rc.1 || fail 'h: stamp chart'
    helm package "$work/chart" --version 1.4.0-rc.1 --destination "$work" > /dev/null || fail 'h: package'
    helm show chart "$work/devicechain-1.4.0-rc.1.tgz" > "$work/shown.yaml" || fail 'h: show chart'
    grep -qF 'artifacthub.io/prerelease: "true"' "$work/shown.yaml" || fail 'h: packaged annotation'
  else
    echo 'self-test: skip (h: helm is not on PATH)'
  fi
  echo 'self-test: OK'
)

if [ "$#" = 1 ] && [ "$1" = --self-test ]; then
  self_test
elif [ "$#" = 2 ]; then
  stamp "$1" "$2"
else
  echo "usage: $0 <Chart.yaml path> <chart version> | --self-test" >&2
  exit 1
fi
