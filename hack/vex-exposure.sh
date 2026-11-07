#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# vex-exposure.sh — given release SBOMs and an advisory, say which artifacts are exposed and
# draft the VEX statement for them.
#
# INPUTS
#   --advisory FILE   {"id":"CVE-…","package":"<name as in the SBOM>","affected":["1.2.3",…]}
#                     "affected" is a list of exact versions; "*" means every version.
#   --sbom FILE       an SPDX 2.x JSON SBOM (the format the release attaches to each image).
#                     Repeatable. The artifact name is the SBOM's own "name".
#   --status S        VEX status for exposed artifacts (default: under_investigation).
#                     not_affected needs --justification (an OpenVEX justification label) and
#                     --impact (the statement a reader can check).
#   --vex-out FILE    where to write the OpenVEX document (default: stdout is the exposure list only).
#
# OUTPUT
#   stdout: one line per exposed artifact, "EXPOSED <artifact> <package>@<version>", then a
#   summary. Exit 0 when nothing is exposed, 1 when something is, 2 on bad input.
#
# 🔴 FAIL-CLOSED. An SBOM with no packages at all, or with no package list, is a bad input (exit
# 2), never "nothing exposed": an empty answer from an empty file is how an exposure is missed.
#
#   hack/vex-exposure.sh --advisory a.json --sbom img1.spdx.json --sbom img2.spdx.json --vex-out out.vex.json
#   hack/vex-exposure.sh --self-test

set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required" >&2; exit 2; }

advisory="" vexout="" status="under_investigation" justification="" impact=""
sboms=()

die() { echo "FAIL: $*" >&2; exit 2; }

run() {
  [ -f "$advisory" ] || die "--advisory file is required"
  [ "${#sboms[@]}" -gt 0 ] || die "at least one --sbom is required"
  case "$status" in
    under_investigation|affected|fixed) ;;
    not_affected) [ -n "$justification" ] && [ -n "$impact" ] || die "not_affected needs --justification and --impact" ;;
    *) die "unknown --status $status" ;;
  esac
  jq -e '(.id|type=="string") and (.package|type=="string") and (.affected|type=="array" and length>0)' "$advisory" >/dev/null \
    || die "advisory must carry id, package and a non-empty affected list"
  local id pkg
  id="$(jq -r .id "$advisory")"; pkg="$(jq -r .package "$advisory")"

  local exposed=() f name hits
  for f in "${sboms[@]}"; do
    [ -f "$f" ] || die "no such SBOM: $f"
    jq -e '(.packages|type=="array" and length>0)' "$f" >/dev/null \
      || die "$f has no package list; refusing to read that as 'not exposed'"
    name="$(jq -r '.name // empty' "$f")"; [ -n "$name" ] || die "$f has no name"
    hits="$(jq -r --slurpfile a "$advisory" '
        ($a[0]) as $adv
        | .packages[] | select(.name == $adv.package) | . as $p
        | select(($adv.affected | index("*")) or ($p.versionInfo == null) or ($p.versionInfo == "")
                 or ($adv.affected | index($p.versionInfo)))
        | "\(.name)@\(.versionInfo // "unknown-version")"' "$f" | sort -u)"
    if [ -n "$hits" ]; then
      while IFS= read -r h; do
        echo "EXPOSED $name $h"; exposed+=("$name")
      done <<<"$hits"
    fi
  done

  if [ -n "$vexout" ]; then
    local uniq=()
    if [ "${#exposed[@]}" -gt 0 ]; then
      while IFS= read -r f; do uniq+=("$f"); done < <(printf '%s
' "${exposed[@]}" | sort -u)
    fi
    jq -n --arg id "$id" --arg status "$status" --arg just "$justification" --arg imp "$impact" '
      $ARGS.positional as $arts
      | {"@context":"https://openvex.dev/ns/v0.2.0",
         "@id":("https://devicechain.io/vex/" + $id),
         author:"DeviceChain maintainers", version:1,
         timestamp:(now | strftime("%Y-%m-%dT%H:%M:%SZ")),
         statements:([ { vulnerability:{name:$id},
                         products: ($arts | map({"@id": ("pkg:oci/" + .)})),
                         status:$status }
                       + (if $just != "" then {justification:$just, impact_statement:$imp} else {} end) ]
                     | map(select(.products|length>0))) }' --args "${uniq[@]+"${uniq[@]}"}" > "$vexout"
  fi
  echo "$id: ${#exposed[@]} exposed artifact(s) of ${#sboms[@]} checked ($pkg)"
  [ "${#exposed[@]}" -eq 0 ]
}

selftest() {
  local d="$here/testdata/vex" t out rc
  t="$(mktemp -d)"
  echo "== a CVE injected into a test SBOM yields an exposure and a VEX entry"
  rc=0; out="$("$0" --advisory "$d/advisory-affected.json" --sbom "$d/image-a.spdx.json" --sbom "$d/image-b.spdx.json" --vex-out "$t/v.json")" || rc=$?
  echo "$out"
  [ "$rc" = 1 ] || { echo "SELF-TEST FAIL: exposure not reported (rc=$rc)" >&2; return 1; }
  echo "$out" | grep -q '^EXPOSED image-a ' || { echo "SELF-TEST FAIL: image-a not listed" >&2; return 1; }
  ! echo "$out" | grep -q '^EXPOSED image-b ' || { echo "SELF-TEST FAIL: unexposed image-b listed" >&2; return 1; }
  jq -e '.statements[0].vulnerability.name=="CVE-2099-0001" and (.statements[0].products|length==1) and .statements[0].status=="under_investigation" and (.timestamp|length==20)' "$t/v.json" >/dev/null \
    || { echo "SELF-TEST FAIL: VEX entry wrong" >&2; return 1; }
  echo "== control: a fixed version must not be reported"
  rc=0; "$0" --advisory "$d/advisory-fixed-only.json" --sbom "$d/image-a.spdx.json" >/dev/null || rc=$?
  [ "$rc" = 0 ] || { echo "SELF-TEST FAIL: fixed version reported as exposed (rc=$rc)" >&2; return 1; }
  echo "== a wildcard advisory exposes every version"
  rc=0; out="$("$0" --advisory "$d/advisory-wildcard.json" --sbom "$d/image-b.spdx.json")" || rc=$?
  [ "$rc" = 1 ] && echo "$out" | grep -q '^EXPOSED image-b ' || { echo "SELF-TEST FAIL: wildcard advisory missed image-b (rc=$rc)" >&2; return 1; }
  echo "== a matching package with no version is exposure unknown, never clean"
  rc=0; out="$("$0" --advisory "$d/advisory-fixed-only.json" --sbom "$d/image-c.spdx.json")" || rc=$?
  [ "$rc" = 1 ] && echo "$out" | grep -q '^EXPOSED image-c ' || { echo "SELF-TEST FAIL: versionless package reported clean (rc=$rc)" >&2; return 1; }
  echo "== negative control: an SBOM with no packages is an error, not 'clean'"
  rc=0; "$0" --advisory "$d/advisory-affected.json" --sbom "$d/empty.spdx.json" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || { echo "SELF-TEST FAIL: empty SBOM accepted (rc=$rc)" >&2; return 1; }
  rm -rf "$t"
  echo "self-test ok"
}

if [ "${1:-}" = "--self-test" ]; then selftest; exit $?; fi
while [ $# -gt 0 ]; do
  case "$1" in
    --advisory) advisory="${2:?}"; shift 2 ;;
    --sbom) sboms+=("${2:?}"); shift 2 ;;
    --status) status="${2:?}"; shift 2 ;;
    --justification) justification="${2:?}"; shift 2 ;;
    --impact) impact="${2:?}"; shift 2 ;;
    --vex-out) vexout="${2:?}"; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done
run
