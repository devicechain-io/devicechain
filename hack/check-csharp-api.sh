#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Does the public C# SDK still expose everything it exposed at the last release?
#
#   hack/check-csharp-api.sh                 # baseline: nearest stable tag reachable from HEAD
#   hack/check-csharp-api.sh v0.24.0         # baseline: the tag given
#   hack/check-csharp-api.sh --self-test     # prove the check can fail, through this script
#   hack/check-csharp-api.sh --promote       # release step: fold Unshipped into Shipped
#
# THE MECHANISM IS THE ANALYZER'S OWN. sdks/csharp/src/DeviceChain.Sdk carries
# Microsoft.CodeAnalysis.PublicApiAnalyzers and two files beside the project:
#
#   PublicAPI.Shipped.txt     the public API of the last release, frozen
#   PublicAPI.Unshipped.txt   what this release cycle adds, and removes
#
# A public symbol in neither file fails the build (RS0016); a symbol in Shipped that the code
# no longer has fails it too (RS0017). Declaring an addition is one line in Unshipped. Declaring
# a REMOVAL or a CHANGE is that line prefixed `*REMOVED*`, copied from Shipped, plus (for a
# change) the new signature as an ordinary line. That `*REMOVED*` line is the declared break:
# the release guard (hack/check-release-highlights.sh) refuses a release that has one unless
# its highlights say `"breaking": true`, and from 1.0.0 unless the release is a new major.
#
# WHAT THIS SCRIPT ADDS TO THE ANALYZER, because the analyzer trusts the files it is given:
#
#   1. SHIPPED IS FROZEN. Editing Shipped.txt is the one way to hide a removal from the
#      analyzer (delete the line, and RS0017 has nothing to say). So Shipped must equal the
#      API the baseline release actually had. A baseline that already carries the files is
#      read from them; the first baseline (v0.24.0, which predates the gate) is rebuilt: its
#      tree is extracted, the analyzer is injected, and the analyzer's own code fix writes the
#      file. Either way a hand-edited Shipped.txt fails, naming the lines that differ.
#   2. NO STALE DECLARATION. A `*REMOVED*` line whose symbol is not in Shipped declares a
#      removal of nothing, which over-covers the next real one.
#   3. THE REPORT. Every live `*REMOVED*` line is printed as a `declared` line so a reader of
#      the job summary sees the break without opening a file.
#
# 🔴 NOT YET PROMOTED IS NOT RED. A release tag cannot turn main red. Between the tag and the
# promote commit the working Shipped.txt is still the PREVIOUS release's, which equals the
# tag's own Shipped.txt; that state is accepted and printed as a note. `--promote` is the
# release-time step, run in the commit AFTER a release is tagged and never before it: until the
# tag exists the baseline is still the previous release, and a promoted Shipped.txt carries this
# cycle's additions, which that baseline did not ship (check 1 below fails on exactly that). It
# moves every Unshipped line into Shipped, dropping the `*REMOVED*` targets, and empties
# Unshipped.
#
# Exit 1: a break, a stale or invalid declaration, or a Shipped.txt that is not the baseline's.
# Anything that stops the comparison happening (no tag, no dotnet, a build that fails for a
# reason that is not an API diagnostic) is a different message, so a red check says which.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/api-baseline.sh
. "$ROOT/hack/api-baseline.sh"

SDK_REL=sdks/csharp/src/DeviceChain.Sdk
SDK="$ROOT/$SDK_REL"
SHIPPED_REL="$SDK_REL/PublicAPI.Shipped.txt"
UNSHIPPED_REL="$SDK_REL/PublicAPI.Unshipped.txt"
ANALYZER_PKG="Microsoft.CodeAnalysis.PublicApiAnalyzers"

# The analyzer is a build-time dependency: .NET must be on PATH. An SDK installed by
# dotnet-install.sh lands in ~/.dotnet, which is not.
# A machine-local install is the one global.json pins; prefer it when present.
if [ -x "$HOME/.dotnet/dotnet" ]; then
  export PATH="$HOME/.dotnet:$PATH"
fi

# normalize <file>: the API as a sorted set of lines, without the `#nullable` marker and
# line-ending noise. Everything below compares these sets.
normalize() {
  tr -d '\r' <"$1" | grep -v '^#nullable' | grep -v '^[[:space:]]*$' | LC_ALL=C sort -u || true
}

# removed_targets <unshipped-file>: the symbol each `*REMOVED*` line declares gone.
removed_targets() {
  tr -d '\r' <"$1" | grep '^\*REMOVED\*' | sed 's/^\*REMOVED\*//' | LC_ALL=C sort -u || true
}

# promote_to <shipped> <unshipped> <out>: the Shipped file the next release carries.
promote_to() {
  local s="$1" u="$2" out="$3" rm
  rm="$(mktemp)"
  removed_targets "$u" >"$rm"
  {
    normalize "$s" | grep -vxF -f "$rm" || true
    normalize "$u" | grep -v '^\*REMOVED\*' || true
  } | LC_ALL=C sort -u >"$out"
  rm -f "$rm"
}

# frozen_diff <working-shipped> <expected-set-file>: lines that differ, as `+`/`-` lines
# (+ = in the working file but not in the baseline; - = in the baseline, missing from the file).
frozen_diff() {
  local w e
  w="$(mktemp)"; e="$(mktemp)"
  normalize "$1" >"$w"
  normalize "$2" >"$e"
  diff "$e" "$w" | grep '^[<>]' | sed 's/^< /- /; s/^> /+ /' || true
  rm -f "$w" "$e"
}

# stale_declarations <shipped> <unshipped>: a `*REMOVED*` whose target is not in Shipped.
stale_declarations() {
  local s u
  s="$(mktemp)"; u="$(mktemp)"
  normalize "$1" >"$s"
  removed_targets "$2" >"$u"
  grep -vxF -f "$s" "$u" || true
  rm -f "$s" "$u"
}

# snapshot_api <src-root> <out>: the public API of the SDK source under <src-root> (a tree
# laid out like this repository), written by the analyzer's own code fix, so the format is
# the analyzer's and not this script's guess at it.
snapshot_api() {
  local root="$1" out="$2" proj
  proj="$root/$SDK_REL/DeviceChain.Sdk.csproj"
  [ -f "$proj" ] || { echo "snapshot: $proj does not exist" >&2; return 1; }
  rm -f "$root/$SDK_REL/packages.lock.json"
  if ! grep -q "$ANALYZER_PKG" "$proj"; then
    sed -i "0,/<PackageReference Include=\"MQTTnet\"/s##<PackageReference Include=\"$ANALYZER_PKG\" Version=\"3.3.4\" PrivateAssets=\"all\" />\n    &#" "$proj"
    grep -q "$ANALYZER_PKG" "$proj" || { echo "snapshot: could not inject the analyzer into $proj" >&2; return 1; }
  fi
  printf '#nullable enable\n' >"$root/$SHIPPED_REL"
  printf '#nullable enable\n' >"$root/$UNSHIPPED_REL"
  # A Windows-native dotnet cannot always follow an MSYS /tmp path; the mixed form works everywhere.
  proj="$(cygpath -m "$proj" 2>/dev/null || echo "$proj")"
  dotnet restore "$proj" >"$root/restore.log" 2>&1 || { cat "$root/restore.log" >&2; return 1; }
  # 🔴 dotnet format only touches files UNDER THE WORKING DIRECTORY: run from elsewhere it
  # reports "Formatted 0 of 0 files" and exits 0, which looks like a project with no API.
  (cd "$root/$SDK_REL" && dotnet format analyzers DeviceChain.Sdk.csproj --diagnostics RS0016 --severity info --no-restore) \
    >"$root/format.log" 2>&1 || { cat "$root/format.log" >&2; return 1; }
  normalize "$root/$UNSHIPPED_REL" >"$out"
  # An SDK with no public API is not a baseline, it is a failed run: refuse a vacuous snapshot.
  [ -s "$out" ] || { echo "snapshot: the analyzer's code fix wrote no API for $proj" >&2; cat "$root/format.log" >&2; return 1; }
}

# baseline_shipped <tag> <out> <note-var>: the Shipped set the baseline release implies, and
# whether the working Shipped.txt may instead be that release's own (not yet promoted).
# Prints the state to stdout as one word: promoted-set | rebuilt.
baseline_shipped() {
  local tag="$1" out="$2" tmp
  if git cat-file -e "${tag}:${SHIPPED_REL}" 2>/dev/null; then
    tmp="$(mktemp -d)"
    git show "${tag}:${SHIPPED_REL}" >"$tmp/s.txt"
    if git cat-file -e "${tag}:${UNSHIPPED_REL}" 2>/dev/null; then
      git show "${tag}:${UNSHIPPED_REL}" >"$tmp/u.txt"
    else
      : >"$tmp/u.txt"
    fi
    promote_to "$tmp/s.txt" "$tmp/u.txt" "$out"
    cp "$tmp/s.txt" "$out.own"
    rm -rf "$tmp"
    echo files
    return 0
  fi
  # The baseline predates the gate: rebuild its API.
  tmp="$(mktemp -d)"
  git archive --format=tar "$tag" sdks/csharp/Directory.Build.props "$SDK_REL" global.json branding/logos \
    | tar -x -C "$tmp" || { rm -rf "$tmp"; return 1; }
  mkdir -p "$tmp/sdks/csharp"
  snapshot_api "$tmp" "$out" || { rm -rf "$tmp"; return 1; }
  rm -rf "$tmp"
  echo rebuilt
}

# analyzer_build <sdk-project-dir> <log>: build with the analyzer; 0 = clean, 1 = API
# diagnostics, 2 = the build failed for another reason.
analyzer_build() {
  local dir="$1" log="$2" rc=0
  dotnet restore "$dir/DeviceChain.Sdk.csproj" --locked-mode >"$log" 2>&1 || { cat "$log" >&2; return 2; }
  dotnet build "$dir/DeviceChain.Sdk.csproj" --no-restore -c Release >>"$log" 2>&1 || rc=$?
  [ "$rc" -eq 0 ] && return 0
  if grep -qE 'error RS00[0-9]{2}' "$log"; then
    return 1
  fi
  return 2
}

# report_diagnostics <log>: the API diagnostics, one per distinct symbol, as FAIL lines.
report_diagnostics() {
  grep -E 'error RS00[0-9]{2}' "$1" \
    | sed -E 's/^.*error (RS00[0-9]{2}): (.*) \(https:.*$/FAIL \1 \2/; s/\[[A-Za-z]:[^]]*\]//' \
    | LC_ALL=C sort -u
}

self_test() {
  local tmp out rc log
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  log="$tmp/build.log"

  # A copy of the SDK in a repository-shaped tree: the csproj reaches ../../../../branding.
  mkdir -p "$tmp/w/sdks/csharp/src" "$tmp/w/branding/logos"
  cp -R "$SDK" "$tmp/w/sdks/csharp/src/"
  cp "$ROOT/sdks/csharp/Directory.Build.props" "$tmp/w/sdks/csharp/"
  cp "$ROOT/global.json" "$tmp/w/"
  cp "$ROOT/branding/logos/avatar-400.png" "$tmp/w/branding/logos/"
  rm -rf "$tmp/w/$SDK_REL/bin" "$tmp/w/$SDK_REL/obj"
  local w="$tmp/w/$SDK_REL"

  # 1. The tree as committed builds clean: the check does not cry wolf on today's API.
  rc=0; analyzer_build "$w" "$log" || rc=$?
  check_rc "the committed API" 0 "$rc" "$(tail -20 "$log")" || return 1

  # 2. A public type the API files do not declare fails, naming it (RS0016).
  cat >"$w/ZzSelfTestProbe.cs" <<'CS'
namespace DeviceChain.Sdk
{
    public sealed class ZzSelfTestProbe { }
}
CS
  rc=0; analyzer_build "$w" "$log" || rc=$?
  check_rc "an undeclared public type" 1 "$rc" "$(tail -20 "$log")" || return 1
  out="$(report_diagnostics "$log")"
  check_has "an undeclared public type" "RS0016 Symbol 'ZzSelfTestProbe'" "$out" || return 1

  # 3. Declared in Unshipped, it passes: an addition costs one line.
  echo 'DeviceChain.Sdk.ZzSelfTestProbe' >>"$w/PublicAPI.Unshipped.txt"
  echo 'DeviceChain.Sdk.ZzSelfTestProbe.ZzSelfTestProbe() -> void' >>"$w/PublicAPI.Unshipped.txt"
  rc=0; analyzer_build "$w" "$log" || rc=$?
  check_rc "a declared addition" 0 "$rc" "$(tail -20 "$log")" || return 1
  rm "$w/ZzSelfTestProbe.cs"
  # Back to the COMMITTED Unshipped file, not an empty one: this cycle's real additions live
  # there, and dropping them would fail every case below on RS0016 for symbols nobody planted.
  cp "$SDK/PublicAPI.Unshipped.txt" "$w/PublicAPI.Unshipped.txt"

  # 4. A symbol Shipped promises and the code lacks fails (RS0017): the removed member.
  echo 'DeviceChain.Sdk.ZzSelfTestRemoved' >>"$w/PublicAPI.Shipped.txt"
  rc=0; analyzer_build "$w" "$log" || rc=$?
  check_rc "a removed member" 1 "$rc" "$(tail -20 "$log")" || return 1
  out="$(report_diagnostics "$log")"
  check_has "a removed member" "RS0017 Symbol 'DeviceChain.Sdk.ZzSelfTestRemoved'" "$out" || return 1

  # 5. Declared with *REMOVED* in Unshipped, it passes - and the declaration is reported.
  echo '*REMOVED*DeviceChain.Sdk.ZzSelfTestRemoved' >>"$w/PublicAPI.Unshipped.txt"
  rc=0; analyzer_build "$w" "$log" || rc=$?
  check_rc "a declared removal" 0 "$rc" "$(tail -20 "$log")" || return 1
  out="$(removed_targets "$w/PublicAPI.Unshipped.txt")"
  check_has "a declared removal" "DeviceChain.Sdk.ZzSelfTestRemoved" "$out" || return 1
  [ -z "$(stale_declarations "$w/PublicAPI.Shipped.txt" "$w/PublicAPI.Unshipped.txt")" ] \
    || { echo "self-test: a declaration with its target in Shipped was called stale" >&2; return 1; }

  # 6. A declaration whose target Shipped does not have is stale.
  echo '*REMOVED*DeviceChain.Sdk.ZzNeverShipped' >>"$w/PublicAPI.Unshipped.txt"
  out="$(stale_declarations "$w/PublicAPI.Shipped.txt" "$w/PublicAPI.Unshipped.txt")"
  [ "$out" = "DeviceChain.Sdk.ZzNeverShipped" ] \
    || { echo "$out"; echo "self-test: a stale declaration was not found" >&2; return 1; }

  # 7. Shipped is frozen: a hand-edited Shipped.txt differs from the baseline's, in both
  #    directions (a deleted line is the hidden removal; an added one is a hidden promise).
  cp "$SDK/PublicAPI.Shipped.txt" "$tmp/base.txt"
  cp "$tmp/base.txt" "$tmp/edited.txt"
  [ -z "$(frozen_diff "$tmp/edited.txt" "$tmp/base.txt")" ] \
    || { echo "self-test: identical Shipped files differ" >&2; return 1; }
  local victim
  victim="$(grep -v '^#nullable' "$tmp/base.txt" | sed -n '5p')"
  [ -n "$victim" ] || { echo "self-test: Shipped.txt has fewer than five symbols" >&2; return 1; }
  grep -vxF -- "$victim" "$tmp/base.txt" >"$tmp/edited.txt"
  out="$(frozen_diff "$tmp/edited.txt" "$tmp/base.txt")"
  [ "$out" = "- $victim" ] || { echo "$out"; echo "self-test: a deleted Shipped line was not found" >&2; return 1; }
  echo "DeviceChain.Sdk.ZzHidden" >>"$tmp/base.txt"
  out="$(frozen_diff "$SDK/PublicAPI.Shipped.txt" "$tmp/base.txt")"
  [ "$out" = "- DeviceChain.Sdk.ZzHidden" ] || { echo "$out"; echo "self-test: a missing baseline line was not found" >&2; return 1; }

  # 8. Promotion: additions fold into Shipped, *REMOVED* targets leave it, Unshipped empties.
  printf '#nullable enable\nA.B\nA.C\nA.D\n' >"$tmp/s.txt"
  printf '#nullable enable\n*REMOVED*A.C\nA.E\n' >"$tmp/u.txt"
  promote_to "$tmp/s.txt" "$tmp/u.txt" "$tmp/p.txt"
  [ "$(paste -sd, "$tmp/p.txt")" = "A.B,A.D,A.E" ] \
    || { cat "$tmp/p.txt"; echo "self-test: promotion produced the wrong set" >&2; return 1; }

  echo "csharp-api self-test: the committed API is clean; an undeclared type and a removed member fail naming the symbol; declared ones pass; stale and hand-edited Shipped lines are found; promotion folds correctly"
}

promote() {
  local tmp
  tmp="$(mktemp)"
  promote_to "$SDK/PublicAPI.Shipped.txt" "$SDK/PublicAPI.Unshipped.txt" "$tmp"
  { printf '#nullable enable\n'; cat "$tmp"; } >"$SDK/PublicAPI.Shipped.txt"
  printf '#nullable enable\n' >"$SDK/PublicAPI.Unshipped.txt"
  rm -f "$tmp"
  echo "promoted: $(grep -vc '^#nullable' "$SDK/PublicAPI.Shipped.txt") shipped symbols; Unshipped is empty. Commit both files."
}

case "${1:-}" in
  --self-test)
    [ $# -eq 1 ] || { echo "usage: $0 --self-test" >&2; exit 2; }
    command -v dotnet >/dev/null 2>&1 || { echo "::error::dotnet is not available; the C# API check cannot run." >&2; exit 1; }
    self_test
    exit
    ;;
  --promote)
    [ $# -eq 1 ] || { echo "usage: $0 --promote" >&2; exit 2; }
    promote
    exit
    ;;
esac

[ $# -le 1 ] || { echo "usage: $0 [baseline-tag | --self-test | --promote]" >&2; exit 2; }
command -v dotnet >/dev/null 2>&1 || { echo "::error::dotnet is not available; the C# API check cannot run." >&2; exit 1; }

# The check proper, in a function so its output can be teed into the job summary.
main_check() {
  baseline="$(resolve_baseline "${1:-}")" || exit 1
  echo "C# API baseline: $baseline"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  rc=0

  # 1. Shipped is the baseline's API.
  if ! state="$(baseline_shipped "$baseline" "$tmp/expected.txt")"; then
    echo "::error::could not establish the C# API of $baseline; nothing was compared. See the error above." >&2
    exit 1
  fi
  diff_lines="$(frozen_diff "$SDK/PublicAPI.Shipped.txt" "$tmp/expected.txt")"
  if [ -n "$diff_lines" ] && [ "$state" = files ]; then
    # Not yet promoted: the working file is the baseline's own Shipped.txt, which is the
    # previous release's API. Accepted, said out loud.
    if [ -z "$(frozen_diff "$SDK/PublicAPI.Shipped.txt" "$tmp/expected.txt.own")" ]; then
      echo "note: $SHIPPED_REL is still the API before $baseline; run hack/check-csharp-api.sh --promote and commit it."
      diff_lines=""
    fi
  fi
  if [ -n "$diff_lines" ]; then
    echo "FAIL $SHIPPED_REL is not the API $baseline shipped (- = missing from the file, + = not in $baseline):"
    echo "$diff_lines" | sed 's/^/FAIL   /'
    rc=1
  fi

  # 2. No stale declaration.
  stale="$(stale_declarations "$SDK/PublicAPI.Shipped.txt" "$SDK/PublicAPI.Unshipped.txt")"
  if [ -n "$stale" ]; then
    while IFS= read -r s; do
      echo "FAIL *REMOVED*$s in PublicAPI.Unshipped.txt declares the removal of a symbol $baseline did not ship; delete the line."
    done <<<"$stale"
    rc=1
  fi

  # 3. The declared breaks, for the summary and for the release guard.
  declared="$(removed_targets "$SDK/PublicAPI.Unshipped.txt")"
  if [ -n "$declared" ]; then
    while IFS= read -r d; do
      echo "declared $baseline csharp $d member-removed-or-changed"
    done <<<"$declared"
  fi

  # 4. The analyzer, as the build.
  brc=0
  analyzer_build "$SDK" "$tmp/build.log" || brc=$?
  case "$brc" in
    0) ;;
    1)
      report_diagnostics "$tmp/build.log"
      echo "To declare a removal or a change, copy its line from PublicAPI.Shipped.txt into PublicAPI.Unshipped.txt prefixed *REMOVED* (a change also adds the new signature as an ordinary line). An addition is one ordinary line."
      rc=1
      ;;
    *)
      tail -30 "$tmp/build.log" >&2
      echo "::error::the C# SDK did not build for a reason that is not an API diagnostic (exit $brc); nothing was compared." >&2
      exit 1
      ;;
  esac

  n_shipped="$(normalize "$SDK/PublicAPI.Shipped.txt" | wc -l | tr -d ' ')"
  echo "csharp-api: $n_shipped shipped symbols, $(printf '%s' "$declared" | grep -c . || true) declared break(s) against $baseline"
  if [ "$rc" -ne 0 ]; then
    echo "::error::the public C# SDK API differs from $baseline without a declaration, or a declaration is invalid. The FAIL lines above name each symbol." >&2
    exit 1
  fi
}

log="$(mktemp)"
rc=0
main_check "$@" 2>&1 | tee "$log" || rc=${PIPESTATUS[0]}
summarize "C# API (see the log for the baseline)" "$log"
rm -f "$log"
exit "$rc"
