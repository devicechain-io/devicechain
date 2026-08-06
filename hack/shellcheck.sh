#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs shellcheck over every shell script tracked in this repository.
#
# WHY THIS EXISTS. actionlint already shellchecks the `run:` blocks inside the
# workflows, so CI's inline shell was covered — but the standalone scripts were
# not, and that is where the substantial shell in this repo lives: the two
# validation rigs, the CI guards, the local bring-up, the operand-image build.
#
# 🔴 WHY THIS GATES AT `info` AND EXCLUDES BY CODE, NOT BY SEVERITY BAND.
#
# The obvious design is `--severity=warning`, and it is WRONG here. It was tried
# first and the self-test below caught it: **SC2086 — an unquoted expansion, the
# single most common real bug in shell — is classified `info`, not `warning`.**
# So a warning-level gate would have sailed straight past the two genuine
# defects found in this repo's workflows the week this landed (an unquoted
# `$(...)` in ci.yml, an unquoted `${REGISTRY}` in release.yml). A severity band
# does not mean "is this a real bug"; it is a coarse proxy that happens to put
# the worst class on the wrong side of the line.
#
# So the threshold is `info` — which gates SC2086, SC2046 and their family — and
# the noise is dropped one CODE at a time, each with a reason a reviewer can
# check. Measured over the tracked tree when this landed: 1 warning, 20 info,
# 9 style. The four codes excluded below account for all 20 info findings; the
# one warning was a real (if trivial) defect and was fixed rather than excluded.
#
# `style` stays out via the threshold: it is all SC2001 ("use ${var//x/y}
# instead of sed"), and rewriting nine working sed pipelines risks behaviour for
# no correctness gain.
#
# This is scoping the gate down, which is allowed. It is not making it advisory
# — there is no `continue-on-error` here and there must never be one. A gate
# that cannot fail is the failure mode this repository has shipped before.
#
# ADDING A CODE HERE NARROWS THE GATE. Do it only with a reason written down,
# and prefer fixing the finding.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

SEVERITY="${SHELLCHECK_SEVERITY:-info}"

# Excluded codes, and why each is not a defect in THIS tree:
#
#   SC2016  "expressions don't expand in single quotes" — fires on the awk, jq
#           and Go-template programs these guards are built out of, where the
#           single quotes are the entire point.
#   SC1091  "not following sourced file" — shellcheck checks each script in
#           isolation and cannot resolve a path computed at runtime. Nothing is
#           wrong with the source line; the checker simply cannot see the target.
#   SC2015  "A && B || C is not if-then-else" — the reporting idiom in
#           deploy/local/preflight.sh is `[ cond ] && pass "..." || warn "..."`,
#           and `pass` is a bare printf that cannot fail, so C never runs when A
#           is true. A genuine footgun in general, not reachable here.
#   SC1003  "want to escape a single quote?" — a literal backslash in the case
#           pattern of a glob-to-regex escaper, which is exactly what is meant.
EXCLUDE="${SHELLCHECK_EXCLUDE:-SC2016,SC1091,SC2015,SC1003}"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# scripts: every tracked *.sh, derived from git rather than declared.
#
# 🔴 THIS IS THE ONE THAT MATTERS. `git ls-files` is used instead of a `find`
# or a hand-written list for two reasons, and the second is the load-bearing
# one: it tracks the tree automatically as scripts are added, AND it can never
# reach into an untracked scratch directory and fail the build on somebody's
# throwaway file. `_legacy/` carries no shell today, but it is tracked, so if
# that ever changes this is where to exclude it.
# ---------------------------------------------------------------------------
scripts() {
  git ls-files '*.sh'
}

run_shellcheck() {
  local -a files
  mapfile -t files < <(scripts)

  # An empty enumeration — wrong working directory, a renamed extension, git
  # unavailable — is refused here rather than passed downstream.
  #
  # 🔑 HONESTY NOTE, because the first version of this comment was wrong and the
  # self-test caught it. This was written believing shellcheck exits 0 when
  # given no files, which would have made an empty list a silent pass — the
  # cannot-fail shape. **It does not**: shellcheck exits 3 with "No files
  # specified.", so CI would have failed either way. Measured, not assumed
  # (case 3 of the self-test pins it, and will fail here if that ever changes).
  #
  # The check therefore earns its place as DIAGNOSTICS, not as a correctness
  # gate: it says "the enumeration is broken, not the tree", where shellcheck
  # would only dump its usage text and leave you looking at the scripts.
  if [ "${#files[@]}" -eq 0 ]; then
    echo "::error::found no tracked *.sh files to check — the enumeration is broken, not the tree" >&2
    return 1
  fi

  echo "shellcheck --severity=${SEVERITY} --exclude=${EXCLUDE} over ${#files[@]} tracked scripts"
  shellcheck --severity="${SEVERITY}" --exclude="${EXCLUDE}" -f gcc "${files[@]}"
}

# ---------------------------------------------------------------------------
# Self-test. A guard is worth nothing until it has been shown to FAIL, so this
# proves both directions on throwaway fixtures: a clean script passes, a script
# with a genuine warning-level defect is caught, and — the case that motivated
# the assertion above — an empty file list is refused rather than passed.
# ---------------------------------------------------------------------------
self_test() {
  command -v shellcheck >/dev/null 2>&1 || {
    echo "FAIL: shellcheck is not on PATH, so the self-test would prove nothing" >&2
    return 1
  }

  local tmp rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # Case 1 — a clean script must pass. This is the counterweight: without it,
  # "the mutation was caught" is satisfied by a checker that fails everything.
  cat >"$tmp/clean.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
greeting="hello"
printf '%s\n' "$greeting"
EOF
  if shellcheck --severity="$SEVERITY" --exclude="$EXCLUDE" -f gcc "$tmp/clean.sh"; then
    echo "  ok: a clean script passes"
  else
    echo "FAIL: a clean script was reported as broken" >&2
    return 1
  fi

  # Case 2 — 🔴 THE CASE THAT CHANGED THE DESIGN. SC2086, an unquoted expansion,
  # is the most common real bug in shell and is classified `info` — so this
  # fixture PASSES at --severity=warning. That is how the first version of this
  # gate was caught being too coarse. Keep this case: it is what stops anyone
  # (including a future me) from "tidying" the threshold back up to warning.
  cat >"$tmp/dirty.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
target="/tmp/some path"
ls $target
EOF
  rc=0
  shellcheck --severity="$SEVERITY" --exclude="$EXCLUDE" -f gcc "$tmp/dirty.sh" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "  ok: an unquoted expansion is caught at --severity=${SEVERITY}"
  else
    echo "FAIL: an unquoted expansion passed — the threshold is too coarse." >&2
    echo "      SC2086 is 'info', so --severity=warning does NOT catch it." >&2
    return 1
  fi

  # Case 2b — the exclusion list must not have swallowed the bug classes it is
  # allowed to hide noise from. An exclusion list is the easiest way to turn
  # this gate back into one that cannot fail, so the codes that matter most are
  # asserted to be absent from it by name.
  local code
  for code in SC2086 SC2046 SC2034; do
    case ",${EXCLUDE}," in
      *",${code},"*)
        echo "FAIL: ${code} is excluded — that is a real bug class, not noise" >&2
        return 1
        ;;
    esac
  done
  echo "  ok: SC2086 / SC2046 / SC2034 are not excluded"

  # Case 3 — pins what shellcheck ACTUALLY does with no files, because this
  # script's empty-list check was originally justified by the opposite belief.
  # It exits 3 ("No files specified."), so an empty enumeration was
  # never a silent pass. If a future shellcheck changes that to 0, this case
  # fails and the comment in run_shellcheck stops being true — which is the
  # point of asserting a premise instead of writing it down.
  rc=0
  shellcheck --severity="$SEVERITY" --exclude="$EXCLUDE" -f gcc >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: shellcheck with no files now exits 0 — an empty list would be a SILENT pass." >&2
    echo "      The empty-list check in run_shellcheck is now load-bearing, not diagnostics;" >&2
    echo "      update its comment before relaxing anything." >&2
    return 1
  fi
  echo "  ok: shellcheck refuses an empty file list (exit ${rc}) rather than passing it"

  rc=0
  ( scripts() { :; }; run_shellcheck ) >/dev/null 2>&1 || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "  ok: an empty enumeration is refused rather than passed"
  else
    echo "FAIL: an empty enumeration passed — the gate cannot fail" >&2
    return 1
  fi

  echo "==> Self-test passed"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") run_shellcheck ;;
  *) usage ;;
esac
