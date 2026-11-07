#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Assemble a dated, hash-manifested, signable EVIDENCE PACK for a release candidate: the answers
# to the first page of a vendor security questionnaire, as files rather than prose.
#
#   hack/evidence-pack.sh record --rigs-dir D --rig ha|dr|x509|ota --log FILE --verdict pass|fail \
#                                --commit SHA [--finished RFC3339]
#   hack/evidence-pack.sh build  --rigs-dir D --commit SHA --out DIR --sbom DIR --vex FILE \
#                                [--attach FILE ...] [--max-age-days N] [--max-age RIG=N ...]
#                                [--now RFC3339]
#   hack/evidence-pack.sh check  --tarball F            # re-hash every member against the manifest
#   hack/evidence-pack.sh sign   --tarball F            # cosign attest-blob (keyless; needs OIDC)
#   hack/evidence-pack.sh verify --tarball F --identity-regexp RE [--issuer URL]
#   hack/evidence-pack.sh --self-test
#
# WHAT GOES IN
#   - one in-toto Statement (v1) per rig (ha, dr, x509, ota), whose subject is the rig's log by
#     SHA-256 and whose predicate carries the rig name, the commit it ran at, when it finished and
#     its verdict. The rigs are manual and need a real cluster, so a maintainer records each result
#     after a run (`record`); this script never runs a rig and never invents a result.
#   - a CycloneDX SBOM per image (--sbom DIR holding *.cdx.json), the OpenVEX document produced by
#     hack/vex-from-govulncheck.sh, and any --attach files (the release signature verifier's output,
#     the CodeQL and Scorecard state, ...), each hashed into manifest.json.
#
# 🔴 FRESHNESS IS THE POINT. A rig result is evidence only while it is recent. `build` REFUSES a
# result older than its window (default 30 days; --max-age-days / --max-age RIG=N), a result dated
# in the future, a verdict other than pass, a log whose hash no longer matches what was recorded,
# a missing rig, an SBOM with no components and an empty VEX document. Each refusal is a self-test
# case, and the self-test is run in CI first: a pack generator that cannot refuse is a rubber stamp.
#
# The pack is a claim about WHAT WAS RUN AND WHEN, not a certification of anything. `sign` attests
# the manifest over the tarball with cosign; `verify` checks that attestation (cosign
# verify-blob-attestation) AND re-hashes the members, because a valid signature over a manifest does
# not by itself show the members still match it.
#
# Needs: bash, jq, sha256sum, tar, gzip; cosign (>= 3) for sign / verify only.
set -euo pipefail

SELF="$(basename "$0")"
RIGS=(ha dr x509 ota)
DEFAULT_MAX_AGE_DAYS=30
PACK_PREDICATE_TYPE="https://devicechain.io/evidence/pack/v1"
RIG_PREDICATE_TYPE="https://devicechain.io/evidence/rig-result/v1"
ISSUER_DEFAULT="https://token.actions.githubusercontent.com"

# Git Bash on Windows rewrites POSIX-looking arguments for native programs, which mangles the identity
# regexps handed to cosign. It would equally break every PATH handed to jq, so it is switched off for
# cosign alone (see cosign_env), never script-wide.
cosign_env() {
  case "$(uname -s)" in
    MINGW* | MSYS*) MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*' "$@" ;;
    *) "$@" ;;
  esac
}

# jq.exe on Windows (Git Bash) writes CRLF, which turns every value read back with $(...) or `read`
# into "value\r" and breaks every comparison. Strip it; pipefail keeps jq -e's verdict.
jq() { command jq "$@" | tr -d '\r'; }

die() { echo "$SELF: $*" >&2; exit 2; }
refuse() { echo "$SELF: REFUSED: $*" >&2; exit 1; }

is_sha() { [[ "$1" =~ ^[0-9a-f]{40}$ ]]; }
is_ts() { [[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; }
epoch() { jq -n -r --arg t "$1" '$t | fromdateiso8601'; }
now_ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
sha256_of() { sha256sum "$1" | cut -d' ' -f1; }

cmd_record() {
  local dir="" rig="" log="" verdict="" commit="" finished=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --rigs-dir) dir="${2:?}"; shift 2 ;;
      --rig) rig="${2:?}"; shift 2 ;;
      --log) log="${2:?}"; shift 2 ;;
      --verdict) verdict="${2:?}"; shift 2 ;;
      --commit) commit="${2:?}"; shift 2 ;;
      --finished) finished="${2:?}"; shift 2 ;;
      *) die "record: unknown argument $1" ;;
    esac
  done
  [ -n "$dir" ] && [ -n "$rig" ] && [ -n "$log" ] || die "record: --rigs-dir, --rig and --log are required"
  [[ " ${RIGS[*]} " == *" $rig "* ]] || die "record: --rig must be one of: ${RIGS[*]}"
  [ -s "$log" ] || die "record: log '$log' is missing or empty"
  case "$verdict" in pass | fail) ;; *) die "record: --verdict must be pass or fail" ;; esac
  is_sha "$commit" || die "record: --commit must be a full 40-hex commit"
  [ -n "$finished" ] || finished="$(now_ts)"
  is_ts "$finished" || die "record: --finished must be RFC3339 UTC, e.g. 2026-11-07T12:00:00Z"
  mkdir -p "$dir"
  cp "$log" "$dir/$rig.log"
  jq -n --arg rig "$rig" --arg commit "$commit" --arg finished "$finished" --arg verdict "$verdict" \
    --arg sha "$(sha256_of "$dir/$rig.log")" \
    '{rig:$rig, commit:$commit, finishedAt:$finished, verdict:$verdict, log:($rig+".log"), logSha256:$sha}' \
    > "$dir/$rig.json"
  echo "$SELF: recorded $rig ($verdict @ ${commit:0:12}, $finished) in $dir"
}

cmd_build() {
  local rigs_dir="" commit="" out="" sbom="" vex="" now="" drift=0 attach=() max_default="$DEFAULT_MAX_AGE_DAYS"
  declare -A max_age=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --rigs-dir) rigs_dir="${2:?}"; shift 2 ;;
      --commit) commit="${2:?}"; shift 2 ;;
      --out) out="${2:?}"; shift 2 ;;
      --sbom) sbom="${2:?}"; shift 2 ;;
      --vex) vex="${2:?}"; shift 2 ;;
      --attach) attach+=("${2:?}"); shift 2 ;;
      --max-age-days) max_default="${2:?}"; shift 2 ;;
      --max-age) max_age["${2%%=*}"]="${2#*=}"; shift 2 ;;
      --now) now="${2:?}"; shift 2 ;;
      --allow-rig-commit-drift) drift=1; shift ;;
      *) die "build: unknown argument $1" ;;
    esac
  done
  [ -n "$rigs_dir" ] && [ -n "$out" ] && [ -n "$sbom" ] && [ -n "$vex" ] || die "build: --rigs-dir, --out, --sbom and --vex are required"
  is_sha "$commit" || die "build: --commit must be a full 40-hex commit"
  [ -n "$now" ] || now="$(now_ts)"
  is_ts "$now" || die "build: --now must be RFC3339 UTC"
  local now_e; now_e="$(epoch "$now")"
  [[ "$max_default" =~ ^([1-9][0-9]{0,4}|0)$ ]] || die "build: --max-age-days must be a whole number of days (no leading zeros, at most 99999)"

  # --- rig results: validate ALL of them before writing anything ---------------------------------
  local rig meta finished verdict rcommit logsha age_days window f_e
  for rig in "${RIGS[@]}"; do
    meta="$rigs_dir/$rig.json"
    [ -s "$meta" ] || refuse "no recorded result for the $rig rig ($meta). Run hack/${rig}-rig.sh and 'record' its result; the pack does not ship without all of: ${RIGS[*]}"
    # One jq call: every field the checks need, and the finish time as an epoch ("bad" if unparseable).
    # (Process starts are the cost that dominates on a Windows maintainer box.)
    IFS=$'\t' read -r finished verdict rcommit logsha f_e < <(jq -r --arg rig "$rig" '
      if (.rig == $rig and (.commit|type=="string") and (.finishedAt|type=="string") and (.verdict|type=="string") and (.logSha256|type=="string"))
      then [.finishedAt, .verdict, .commit, .logSha256, (try (.finishedAt|fromdateiso8601|tostring) catch "bad")] | @tsv
      else empty end' "$meta") || true
    f_e="${f_e//$'\r'/}"
    [ -n "${finished:-}" ] || refuse "$meta is not a result record for the $rig rig"
    is_ts "$finished" || refuse "$rig: finishedAt '$finished' is not RFC3339 UTC"
    is_sha "$rcommit" || refuse "$rig: commit '$rcommit' is not a full 40-hex commit"
    if [ "$rcommit" != "$commit" ] && [ "$drift" -ne 1 ]; then
      refuse "$rig ran at ${rcommit:0:12}, not at the pack commit ${commit:0:12}. Re-run the rig at this commit, or pass --allow-rig-commit-drift."
    fi
    [ "$verdict" = "pass" ] || refuse "$rig rig verdict is '$verdict', not pass"
    [ -s "$rigs_dir/$rig.log" ] || refuse "$rig: the recorded log $rigs_dir/$rig.log is missing"
    [ "$(sha256_of "$rigs_dir/$rig.log")" = "$logsha" ] || refuse "$rig: the log no longer matches the hash recorded with the result (edited after the run?)"
    [ "$f_e" != "bad" ] || refuse "$rig: finishedAt '$finished' does not parse"
    [ "$f_e" -le "$now_e" ] || refuse "$rig: finishedAt $finished is in the future relative to $now"
    window="${max_age[$rig]:-$max_default}"
    [[ "$window" =~ ^([1-9][0-9]{0,4}|0)$ ]] || die "build: --max-age $rig=$window must be a whole number of days (no leading zeros, at most 99999)"
    age_days=$(((now_e - f_e) / 86400))
    [ $((now_e - f_e)) -le $((window * 86400)) ] || refuse "$rig rig result is STALE: finished $finished ($age_days days before $now), freshness window is $window days. Re-run the rig."
  done

  # --- SBOMs, VEX, attachments -------------------------------------------------------------------
  local sboms=() s
  while IFS= read -r s; do sboms+=("$s"); done < <(find "$sbom" -maxdepth 1 -type f -name '*.cdx.json' | sort)
  [ "${#sboms[@]}" -gt 0 ] || refuse "no *.cdx.json SBOMs in $sbom"
  for s in "${sboms[@]}"; do
    jq -e -s 'length == 1 and (.[0] | .bomFormat == "CycloneDX" and ((.components // []) | length) > 0 and all(.components[]; type == "object" and (.name|type == "string")))' "$s" >/dev/null 2>&1 \
      || refuse "$s is not a CycloneDX SBOM with at least one component (an empty SBOM reads like a complete one)"
  done
  [ -s "$vex" ] || refuse "VEX document '$vex' missing"
  # A scan that found nothing legitimately yields ZERO statements, so emptiness is not the test; PROVENANCE
  # is: the document must come from the generator, which itself refuses an unfinished scan and an empty
  # set of reports. Every statement present must be well-formed.
  jq -e -s 'length == 1 and (.[0] | (."@context"|type == "string" and startswith("https://openvex.dev/ns/"))
      and (.tooling|type == "string" and startswith("hack/vex-from-govulncheck.sh"))
      and (.statements|type == "array")
      and all(.statements[]; type == "object" and (.status|IN("affected","not_affected")) and (.vulnerability.name|type == "string") and (.products|length > 0)))' "$vex" >/dev/null 2>&1 || refuse "$vex is not an OpenVEX document produced by hack/vex-from-govulncheck.sh with well-formed statements"
  local a
  for a in "${attach[@]+"${attach[@]}"}"; do [ -s "$a" ] || refuse "attachment '$a' is missing or empty"; done

  # --- write the tree ----------------------------------------------------------------------------
  local date short stage name
  date="${now%%T*}"; short="${commit:0:12}"; name="devicechain-evidence-$date-$short"
  stage="$out/$name"
  [ ! -e "$stage" ] || die "build: $stage already exists"
  mkdir -p "$stage/statements" "$stage/rigs" "$stage/sbom" "$stage/vex" "$stage/attachments"
  for rig in "${RIGS[@]}"; do
    cp "$rigs_dir/$rig.log" "$stage/rigs/$rig.log"
    jq -n --arg rig "$rig" --arg pack "$commit" --arg now "$now" --slurpfile m "$rigs_dir/$rig.json" \
      --arg ptype "$RIG_PREDICATE_TYPE" --argjson win "${max_age[$rig]:-$max_default}" '
      $m[0] as $r
      | {"_type":"https://in-toto.io/Statement/v1",
         subject:[{name:("rigs/"+$rig+".log"), digest:{sha256:$r.logSha256}}],
         predicateType:$ptype,
         predicate:{rig:$rig, commit:$r.commit, finishedAt:$r.finishedAt, verdict:$r.verdict,
                    freshnessWindowDays:$win, packCommit:$pack, packBuiltAt:$now}}' \
      > "$stage/statements/rig-$rig.intoto.json"
  done
  for s in "${sboms[@]}"; do cp "$s" "$stage/sbom/$(basename "$s")"; done
  cp "$vex" "$stage/vex/$(basename "$vex")"
  for a in "${attach[@]+"${attach[@]}"}"; do cp "$a" "$stage/attachments/$(basename "$a")"; done

  ( cd "$stage"
    find . -type f ! -path ./manifest.json | LC_ALL=C sort | sed 's#^\./##' | xargs -d '
' sha256sum       | jq -R -s --arg commit "$commit" --arg now "$now" --arg ptype "$PACK_PREDICATE_TYPE" '
          {schema:"devicechain-evidence-pack/v1", commit:$commit, builtAt:$now, predicateType:$ptype,
           files:(split("
") | map(select(length>0) | capture("^(?<sha256>[0-9a-f]{64}) [ *](?<path>.+)$")) | map({path, sha256}))}'       > manifest.json )

  local tarball="$out/$name.tar.gz"
  tar -C "$out" --sort=name --mtime="$date 00:00:00 UTC" --owner=0 --group=0 --numeric-owner -cf - "$name" | gzip -n > "$tarball"
  ( cd "$out" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256" )
  rm -rf "$stage"
  echo "$SELF: wrote $tarball"
}

cmd_check() {
  local tarball=""
  while [ $# -gt 0 ]; do case "$1" in --tarball) tarball="${2:?}"; shift 2 ;; *) die "check: unknown argument $1" ;; esac; done
  [ -s "$tarball" ] || die "check: --tarball required"
  local work root
  work="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$work'" EXIT
  extract_safe "$tarball" "$work"
  root="$PACK_ROOT"
  [ -s "$root/manifest.json" ] || refuse "no manifest.json in the tarball"
  local n listed
  n="$(jq '.files | length' "$root/manifest.json")"
  [ "$n" -gt 0 ] || refuse "manifest lists no files"
  jq -r '.files[] | .sha256 + "  " + .path' "$root/manifest.json" > "$work/sums.txt"
  ( cd "$root" && sha256sum --check --strict --quiet "$work/sums.txt" >"$work/check.out" 2>&1 )     || refuse "a member is missing or does not match its manifest hash (tampered or corrupted): $(head -3 "$work/check.out" | tr '
' ' ')"
  # Nothing may ride along unlisted.
  listed="$(jq -r '.files[].path' "$root/manifest.json" | LC_ALL=C sort)"
  [ "$(cd "$root" && find . -type f ! -path ./manifest.json | sed 's#^\./##' | LC_ALL=C sort)" = "$listed" ]     || refuse "the tarball holds files the manifest does not list"
  echo "$SELF: $n members match the manifest"
}

# Extract a pack tarball into <dir>, leaving the single pack root in PACK_ROOT. A tarball is refused before
# extraction when it names an absolute or ".." path, holds anything but regular files and directories (a
# symlink or hard link would let a member hash something outside the pack), or has more than one top-level
# entry.
extract_safe() {
  local bad tops
  bad="$(tar -tzf "$1" | grep -E '(^/|(^|/)\.\.(/|$))' || true)"
  [ -z "$bad" ] || refuse "tarball has unsafe member paths: $bad"
  bad="$(tar -tvzf "$1" | grep -vE '^[-d]' || true)"
  [ -z "$bad" ] || refuse "tarball holds members that are neither regular files nor directories: $bad"
  tops="$(tar -tzf "$1" | sed -e 's#^\./##' -e 's#/.*$##' | sort -u | grep -v '^$' || true)"
  [ "$(printf '%s\n' "$tops" | grep -c .)" -eq 1 ] || refuse "tarball must hold exactly one top-level directory, found: $(echo "$tops" | tr '\n' ' ')"
  tar -xzf "$1" -C "$2"
  PACK_ROOT="$2/$tops"
  [ -d "$PACK_ROOT" ] || refuse "the tarball's top-level entry is not a directory"
}

pack_manifest_to() { # <tarball> <dest-file>
  local work root
  work="$(mktemp -d)"
  extract_safe "$1" "$work"
  root="$PACK_ROOT"
  cp "$root/manifest.json" "$2"
  rm -rf "$work"
}

cmd_sign() {
  local tarball=""
  while [ $# -gt 0 ]; do case "$1" in --tarball) tarball="${2:?}"; shift 2 ;; *) die "sign: unknown argument $1" ;; esac; done
  [ -s "$tarball" ] || die "sign: --tarball required"
  command -v cosign >/dev/null || die "cosign is required"
  cmd_check --tarball "$tarball"
  local pred; pred="$(mktemp)"
  pack_manifest_to "$tarball" "$pred"
  cosign_env cosign attest-blob --yes --predicate "$pred" --type "$PACK_PREDICATE_TYPE" --bundle "$tarball.bundle" "$tarball"
  rm -f "$pred"
  echo "$SELF: wrote $tarball.bundle"
}

cmd_verify() {
  local tarball="" re="" issuer="$ISSUER_DEFAULT"
  while [ $# -gt 0 ]; do
    case "$1" in
      --tarball) tarball="${2:?}"; shift 2 ;;
      --identity-regexp) re="${2:?}"; shift 2 ;;
      --issuer) issuer="${2:?}"; shift 2 ;;
      *) die "verify: unknown argument $1" ;;
    esac
  done
  [ -s "$tarball" ] && [ -s "$tarball.bundle" ] && [ -n "$re" ] || die "verify: --tarball (with its .bundle next to it) and --identity-regexp are required"
  command -v cosign >/dev/null || die "cosign is required"
  cosign_env cosign verify-blob-attestation --bundle "$tarball.bundle" --type "$PACK_PREDICATE_TYPE" \
    --certificate-identity-regexp "$re" --certificate-oidc-issuer "$issuer" "$tarball"
  cmd_check --tarball "$tarball"
}

# ------------------------------------------------------------------------------------------------
self_test() {
  local tmp bad=0 sha now
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  sha=0123456789abcdef0123456789abcdef01234567
  now=2026-11-07T12:00:00Z
  local me="$0"

  mkdir -p "$tmp/sbom" "$tmp/rigs" "$tmp/out"
  printf '{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"name":"x"}]}\n' > "$tmp/sbom/operator.cdx.json"
  printf '{"@context":"https://openvex.dev/ns/v0.2.0","tooling":"hack/vex-from-govulncheck.sh (test)","statements":[{"status":"not_affected","vulnerability":{"name":"GO-1"},"products":[{"@id":"pkg:x"}]}]}\n' > "$tmp/vex.json"
  echo "rig output" > "$tmp/log.txt"
  record_all() { # <finished>
    local r
    for r in ha dr x509 ota; do "$me" record --rigs-dir "$tmp/rigs" --rig "$r" --log "$tmp/log.txt" --verdict pass --commit "$sha" --finished "$1" >/dev/null; done
  }
  build() { "$me" build --rigs-dir "$tmp/rigs" --commit "$sha" --out "$tmp/out" --sbom "$tmp/sbom" --vex "$tmp/vex.json" --now "$now" "$@"; }
  # must_fail <what> <expected-reason-regexp> <build args...>: the build must fail AND say why. Asserting
  # the reason matters: a refusal for the wrong reason (a crash downstream) would let the check under
  # test be deleted without this test noticing.
  must_fail() {
    local what="$1" want="$2" err; shift 2
    rm -rf "$tmp/out"; mkdir -p "$tmp/out"
    if err="$(build "$@" 2>&1 >/dev/null)"; then echo "FAIL: accepted $what"; bad=1
    elif grep -qE "$want" <<<"$err"; then echo "  ok: refuses $what"
    else echo "FAIL: refused $what, but not for the right reason: $err"; bad=1; fi
  }

  record_all 2026-11-05T12:00:00Z
  rm -rf "$tmp/out"; mkdir -p "$tmp/out"
  if build >/dev/null 2>&1; then echo "  ok: fresh results build a pack"; else echo "FAIL: a valid pack was refused"; build; bad=1; fi
  local tb; tb="$(find "$tmp/out" -name '*.tar.gz' | head -1)"
  [ -n "$tb" ] || { echo "FAIL: no tarball"; bad=1; tb=/nonexistent; }

  if [ -f "$tb" ]; then
    "$me" check --tarball "$tb" >/dev/null 2>&1 && echo "  ok: check passes an untouched pack" || { echo "FAIL: check rejects an untouched pack"; bad=1; }
    # in-toto shape
    local w; w="$(mktemp -d)"; tar -xzf "$tb" -C "$w"
    jq -e '._type=="https://in-toto.io/Statement/v1" and .predicateType=="https://devicechain.io/evidence/rig-result/v1" and (.subject[0].digest.sha256|length==64) and .predicate.rig=="ha"' \
      "$w"/*/statements/rig-ha.intoto.json >/dev/null || { echo "FAIL: rig statement is not a well-formed in-toto Statement"; bad=1; }
    # Tamper: change a member, repack, check must fail.
    echo tampered >> "$w"/*/rigs/ha.log
    ( cd "$w" && tar -czf "$tmp/tampered.tar.gz" ./* )
    if "$me" check --tarball "$tmp/tampered.tar.gz" >/dev/null 2>&1; then echo "FAIL: check accepted a tampered member"; bad=1; else echo "  ok: check refuses a tampered member"; fi
    # Smuggled file: listed nowhere.
    w2="$(mktemp -d)"; tar -xzf "$tb" -C "$w2"; echo x > "$(echo "$w2"/*)/extra.txt"
    ( cd "$w2" && tar -czf "$tmp/extra.tar.gz" ./* )
    if "$me" check --tarball "$tmp/extra.tar.gz" >/dev/null 2>&1; then echo "FAIL: check accepted an unlisted file"; bad=1; else echo "  ok: check refuses an unlisted file"; fi
    rm -rf "$w" "$w2"
  fi

  # NEGATIVE CONTROL (the one the lane is judged on): a stale result must stop the generator.
  record_all 2026-09-01T12:00:00Z
  must_fail "a STALE rig result (67 days old, window 30)" "STALE"
  # ...and the window really is the knob: the same results pass with a wide enough window.
  rm -rf "$tmp/out"; mkdir -p "$tmp/out"
  if build --max-age-days 90 >/dev/null 2>&1; then echo "  ok: the same results pass under a 90-day window (the refusal was about age)"; else echo "FAIL: age window not honoured"; bad=1; fi
  # One stale rig among fresh ones, with a per-rig window.
  record_all 2026-11-05T12:00:00Z
  "$me" record --rigs-dir "$tmp/rigs" --rig dr --log "$tmp/log.txt" --verdict pass --commit "$sha" --finished 2026-09-01T12:00:00Z >/dev/null
  must_fail "one stale rig among fresh ones" "dr rig result is STALE"
  rm -rf "$tmp/out"; mkdir -p "$tmp/out"
  build --max-age dr=90 >/dev/null 2>&1 && echo "  ok: --max-age RIG=N widens one rig only" || { echo "FAIL: per-rig window"; bad=1; }

  record_all 2026-11-09T12:00:00Z
  must_fail "a result dated in the future" "in the future"
  record_all 2026-11-05T12:00:00Z
  "$me" record --rigs-dir "$tmp/rigs" --rig x509 --log "$tmp/log.txt" --verdict fail --commit "$sha" --finished 2026-11-05T12:00:00Z >/dev/null
  must_fail "a failing rig verdict" "x509 rig verdict is .fail."
  record_all 2026-11-05T12:00:00Z
  rm "$tmp/rigs/ota.json"
  must_fail "a missing rig (ota)" "no recorded result for the ota rig"
  record_all 2026-11-05T12:00:00Z
  echo edited >> "$tmp/rigs/ha.log"
  must_fail "a rig log edited after it was recorded" "no longer matches the hash"
  record_all 2026-11-05T12:00:00Z
  printf '{"bomFormat":"CycloneDX","components":[]}\n' > "$tmp/sbom/empty.cdx.json"
  must_fail "an SBOM with no components" "at least one component"
  rm "$tmp/sbom/empty.cdx.json"
  # A clean scan legitimately has NO statements: that must build. A document the generator did not
  # produce (no tooling marker) or with a malformed statement must not.
  printf '{"@context":"https://openvex.dev/ns/v0.2.0","tooling":"hack/vex-from-govulncheck.sh (test)","statements":[]}\n' > "$tmp/vex-clean.json"
  rm -rf "$tmp/out"; mkdir -p "$tmp/out"
  if "$me" build --rigs-dir "$tmp/rigs" --commit "$sha" --out "$tmp/out" --sbom "$tmp/sbom" --vex "$tmp/vex-clean.json" --now "$now" >/dev/null 2>&1; then echo "  ok: a clean scan (zero VEX statements) builds"; else echo "FAIL: a clean scan was refused"; bad=1; fi
  vex_must_fail() { # <what> <file>
    rm -rf "$tmp/out"; mkdir -p "$tmp/out"
    if err="$("$me" build --rigs-dir "$tmp/rigs" --commit "$sha" --out "$tmp/out" --sbom "$tmp/sbom" --vex "$2" --now "$now" 2>&1 >/dev/null)"; then echo "FAIL: accepted $1"; bad=1
    elif grep -q "not an OpenVEX document produced by" <<<"$err"; then echo "  ok: refuses $1"; else echo "FAIL: $1 refused for the wrong reason: $err"; bad=1; fi
  }
  printf '{"@context":"https://openvex.dev/ns/v0.2.0","statements":[]}\n' > "$tmp/vex-hand.json"
  vex_must_fail "a VEX document the generator did not produce" "$tmp/vex-hand.json"
  printf '{"@context":"https://openvex.dev/ns/v0.2.0","tooling":"hack/vex-from-govulncheck.sh","statements":[null]}\n' > "$tmp/vex-null.json"
  vex_must_fail "a VEX document with a null statement" "$tmp/vex-null.json"
  printf '{"@context":"https://openvex.dev/ns/v0.2.0","tooling":"hack/vex-from-govulncheck.sh","statements":[]}\n{"x":1}\n' > "$tmp/vex-two.json"
  vex_must_fail "two concatenated VEX documents" "$tmp/vex-two.json"

  # SBOM schema: a null component, and two concatenated documents (jq -e judges only the LAST output).
  printf '{"bomFormat":"CycloneDX","components":[null]}\n' > "$tmp/sbom/null.cdx.json"
  must_fail "an SBOM whose component is null" "at least one component"
  printf '{"bomFormat":"CycloneDX","components":[]}\n{"bomFormat":"CycloneDX","components":[{"name":"x"}]}\n' > "$tmp/sbom/null.cdx.json"
  must_fail "an empty SBOM followed by a valid one in the same file" "at least one component"
  rm "$tmp/sbom/null.cdx.json"

  # A rig that ran at a different commit than the pack is not evidence for this pack.
  for r in ha dr x509 ota; do "$me" record --rigs-dir "$tmp/rigs" --rig "$r" --log "$tmp/log.txt" --verdict pass --commit fedcba9876543210fedcba9876543210fedcba98 --finished 2026-11-05T12:00:00Z >/dev/null; done
  must_fail "rig results from a different commit" "not at the pack commit"
  rm -rf "$tmp/out"; mkdir -p "$tmp/out"
  if build --allow-rig-commit-drift >/dev/null 2>&1; then echo "  ok: --allow-rig-commit-drift is the explicit override"; else echo "FAIL: drift override not honoured"; bad=1; fi
  record_all 2026-11-05T12:00:00Z

  # Freshness is judged in seconds, not whole days: 30 days 23 hours old is NOT within a 30-day window.
  record_all 2026-10-07T13:00:00Z
  must_fail "a result 30 days 23 hours old under a 30-day window" "STALE"
  must_fail "a leading-zero window" "whole number of days" --max-age-days 030
  record_all 2026-11-05T12:00:00Z

  # Archive structure: links, extra top-level entries and unlisted nested manifests are all refused.
  local tbl; tbl="$(find "$tmp/out" -name '*.tar.gz' | head -1)"
  rm -rf "$tmp/out"; mkdir -p "$tmp/out"; build >/dev/null 2>&1
  tbl="$(find "$tmp/out" -name '*.tar.gz' | head -1)"
  archive_must_fail() { # <what> <reason-regexp> <mutator-fn>
    local wk; wk="$(mktemp -d)"; tar -xzf "$tbl" -C "$wk"
    "$3" "$(echo "$wk"/*)" "$wk"
    ( cd "$wk" && tar -czf "$tmp/mut.tar.gz" ./* )
    if err="$("$me" check --tarball "$tmp/mut.tar.gz" 2>&1 >/dev/null)"; then echo "FAIL: check accepted $1"; bad=1
    elif grep -qE "$2" <<<"$err"; then echo "  ok: check refuses $1"; else echo "FAIL: $1 refused for the wrong reason: $err"; bad=1; fi
    rm -rf "$wk"
  }
  mut_symlink() { ln -sf /etc/hostname "$1/extra-link"; }
  archive_must_fail "a symlink member" "neither regular files nor directories" mut_symlink
  mut_second_top() { mkdir -p "$2/other"; echo x > "$2/other/f"; }
  archive_must_fail "a second top-level directory" "exactly one top-level" mut_second_top
  mut_nested_manifest() { echo '{}' > "$1/rigs/manifest.json"; }
  archive_must_fail "an unlisted nested manifest.json" "does not list" mut_nested_manifest

  if "$me" record --rigs-dir "$tmp/rigs" --rig bogus --log "$tmp/log.txt" --verdict pass --commit "$sha" >/dev/null 2>&1; then echo "FAIL: recorded an unknown rig"; bad=1; else echo "  ok: refuses to record an unknown rig"; fi

  if [ "$bad" -eq 0 ]; then echo "$SELF self-test: ok"; else echo "$SELF self-test: FAILED"; return 1; fi
}

main() {
  command -v jq >/dev/null || die "jq is required"
  local sub="${1:-}"
  [ $# -gt 0 ] && shift
  case "$sub" in
    --self-test) self_test ;;
    record) cmd_record "$@" ;;
    build) cmd_build "$@" ;;
    check) cmd_check "$@" ;;
    sign) cmd_sign "$@" ;;
    verify) cmd_verify "$@" ;;
    *) sed -n '5,14p' "$0" >&2; exit 2 ;;
  esac
}
main "$@"
