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
#   SC2015  "A && B || C is not if-then-else" — the reporting idiom these guards
#           are built on is `[ cond ] && pass "..." || warn "..."` (see
#           hack/check-ko-base-pin.sh), and `pass` is a bare printf that cannot
#           fail, so C never runs when A is true. A genuine footgun in general,
#           not reachable here.
#
#           🔴 This used to cite deploy/local/preflight.sh, which has since been
#           withdrawn -- dcctl preflight covers it. Re-measured on removal rather
#           than assumed: dropping SC2015 still reports ten hits across two live
#           scripts, so the exclusion stays and now names one of them.
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
# The early-closing-pipe rule — a second pass over the SAME scripts().
#
# 🔴 A READER THAT CAN STOP EARLY MUST NEVER READ FROM A PIPE WHOSE STATUS
# ANYTHING CONSUMES. `printf '%s\n' "$list" | grep -qxF -- "$m"` looks like a
# membership test, but grep -q exits at its first match, the writer's next write
# then fails, and under pipefail that failure is the pipeline's status: "found,
# and grep stopped reading" reads as "not found". hack/go-race.sh failed the go
# job of a module nobody had touched exactly that way. shellcheck has no rule for
# it, so this pass refuses the shape in every tracked script, with no exemption
# list: capture the output first and test the captured text (grep -q... <<<"$x",
# which is written in full before grep starts and is not a pipeline at all), and
# truncate with sed -n '1,Np', which reads to the end, rather than head.
#
# The matcher is hack/lib/early-close-pipes.awk; its header lists what it
# recognises and what it does not.
# ---------------------------------------------------------------------------
early_close_pipes() { awk -f hack/lib/early-close-pipes.awk "$@"; }

run_pipe_rule() {
  local -a files
  local found
  mapfile -t files < <(scripts)
  if [ "${#files[@]}" -eq 0 ]; then
    echo "::error::found no tracked *.sh files to check — the enumeration is broken, not the tree" >&2
    return 1
  fi
  found="$(early_close_pipes "${files[@]}")"
  if [ -n "$found" ]; then
    echo "::error::a reader that can stop early reads from a pipe; under pipefail the writer's broken pipe becomes the answer:" >&2
    printf '%s\n' "$found" >&2
    echo "  Capture the output first and test it: grep -q... <<<\"\$(cmd)\"; truncate with sed -n '1,Np', not head." >&2
    return 1
  fi
  echo "no early-closing pipes in ${#files[@]} tracked scripts"
}

# run_all is the whole gate: both passes, every time, so one failing does not
# hide the other. It is a function rather than inline in the dispatch so the
# self-test can run exactly what CI runs, and see it fail.
run_all() {
  local rc=0
  run_shellcheck || rc=1
  run_pipe_rule || rc=1
  return "$rc"
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

  # Case 4 — the early-closing-pipe rule finds every shape it claims to, each
  # at its own line. Asserted as the exact list of line numbers, not a count: a
  # count survives one shape dropping out while another is reported twice.
  # Lines 10-11 and 12-13 are each one pipeline split across lines, reported
  # where it starts.
  cat >"$tmp/early.sh" <<'EARLY'
a | grep -q x
a | grep -qxF -- "$m"
a | grep -Eq '^v'
a | grep -E -q y
a | grep -m1 z
a | grep -l z
a | head -1
a | head -n 20
x="$(a | head)"
a |
  grep -q x
a \
  | head -1
a | awk '/x/ { print; exit }'
a | sed 1q
a | sed -n '/x/{p;q}'
a | read -r first
a | LC_ALL=C grep -q x
a | command grep -q x
a | { grep -q x; }
a | grep --quiet x
a | grep --max-count=1 x
a | grep --files-with-matches x
a | timeout 5 grep -q x
a | awk '$1=="x" || $2=="y" { print; exit }'
a | sed -n '/x/{p;Q}'
a | env FOO=1 grep -q x
a | grep --silent x
a | grep --files-without-match x
a | grep -e x -q
EARLY
  local want got
  want="1 2 3 4 5 6 7 8 9 10 12 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 "
  got="$(early_close_pipes "$tmp/early.sh" | cut -d: -f2 | tr '\n' ' ')"
  if [ "$got" != "$want" ]; then
    echo "FAIL: the early-closing-pipe rule reported lines [$got], want [$want]" >&2
    return 1
  fi
  echo "  ok: every early-closing reader is reported, at its own line"

  # Case 5 — the counterweight: readers that drain their input, a here-string,
  # an ||, a comment and heredoc bodies are not reported. The inner heredocs use
  # their own tags so they cannot end this one early. This fixture sitting in a
  # heredoc of THIS file is also a standing proof: the real run scans this file
  # and must not report what is written here.
  cat >"$tmp/fine.sh" <<'FINE'
grep -q x <<<"$v"
a || grep -q x file
grep -q x file
a | grep -c x
a | grep -o x
a | sed -n 1p
a | sed -n '1,10s/^/  /p'
a | tail -1
a | awk '{ n++ } END { print n }'
# a | grep -q x
cat <<'XX'
a | grep -q x
XX
cat <<-YY
	a | head -1
	YY
y=$(( 1 << 2 ))
a | sed 's/q/Q/'
a | awk '{ print $1 }' | sort -u
[ "$(a | grep -c .)" -lt 2 ]
a | while read -r l; do echo "$l"; done
FINE
  got="$(early_close_pipes "$tmp/fine.sh")"
  if [ -n "$got" ]; then
    echo "FAIL: the early-closing-pipe rule reported pipelines that drain their input:" >&2
    printf '%s\n' "$got" >&2
    return 1
  fi
  echo "  ok: readers that drain their input, here-strings and heredoc bodies are not reported"

  # Case 6 — 🔴 a heredoc the matcher cannot see close FAILS, rather than
  # silently skipping the rest of the file. `<<` inside a string or a shift
  # opens one that never ends; the pipeline after it must not go unreported.
  # (Heredoc fixtures again: written inline in this file, these lines would
  # trip the real run of the rule, which is how that was found.)
  cat >"$tmp/unclosed-string.sh" <<'UNCLOSED_STRING'
echo "usage: cat <<EOF"
a | grep -q x
UNCLOSED_STRING
  cat >"$tmp/unclosed-shift.sh" <<'UNCLOSED_SHIFT'
z=$(( a << b ))
a | grep -q x
UNCLOSED_SHIFT
  local probe_file tag
  for probe_file in string shift; do
    if [ "$probe_file" = string ]; then tag=EOF; else tag=b; fi
    want="$tmp/unclosed-$probe_file.sh:1:heredoc <<$tag opened here never closes; the rest of the file was not scanned"
    got="$(early_close_pipes "$tmp/unclosed-$probe_file.sh")"
    if [ "$got" != "$want" ]; then
      echo "FAIL: an unclosed heredoc ($probe_file) was reported as [$got], want [$want]" >&2
      return 1
    fi
  done
  # ...and the unclosed heredoc in one file does not hide the next file.
  got="$(early_close_pipes "$tmp/unclosed-string.sh" "$tmp/early.sh" | sed -n 2p | cut -d: -f1,2)"
  if [ "$got" != "$tmp/early.sh:1" ]; then
    echo "FAIL: a heredoc left open in one file hid the next file's first finding (got [$got])" >&2
    return 1
  fi
  echo "  ok: a heredoc that never closes is reported, and ends at its own file"

  rc=0
  ( scripts() { :; }; run_pipe_rule ) >/dev/null 2>&1 || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "  ok: the early-closing-pipe rule refuses an empty enumeration"
  else
    echo "FAIL: the early-closing-pipe rule passed an empty enumeration — it cannot fail" >&2
    return 1
  fi

  # Case 7 — 🔴 THE GATE, NOT JUST ITS MATCHER. Cases 4-6 prove
  # early_close_pipes finds the shape; they say nothing about whether a finding
  # FAILS the run. A run_pipe_rule that printed its findings and returned 0, or
  # a dispatch that ran it and ignored its status, passes all of them while CI
  # goes green over any number of early-closing pipes. So the same fixture is
  # run through run_pipe_rule and through run_all, the dispatch CI calls, and
  # each must fail and name the fixture's line. The fixture is shellcheck-clean,
  # so run_all's failure can only be the pipe rule's; the clean twin is the
  # counterweight that a gate failing everything would not pass.
  cat >"$tmp/gate-early.sh" <<'GATE_EARLY'
#!/usr/bin/env bash
set -euo pipefail
true | grep -q x
GATE_EARLY
  cat >"$tmp/gate-clean.sh" <<'GATE_CLEAN'
#!/usr/bin/env bash
set -euo pipefail
grep -q x <<<"$(true)" || true
GATE_CLEAN
  # gate_on runs one gate with the enumeration replaced by a single fixture, in
  # a subshell so the override cannot leak into the rest of the self-test.
  gate_on() (
    gate_fixture="$1"
    scripts() { printf '%s\n' "$gate_fixture"; }
    case "$2" in
      run_pipe_rule) run_pipe_rule ;;
      run_all) run_all ;;
      *) echo "gate_on: unknown gate $2" >&2; return 2 ;;
    esac
  )
  local gate out
  for gate in run_pipe_rule run_all; do
    rc=0
    out="$(gate_on "$tmp/gate-early.sh" "$gate" 2>&1)" || rc=$?
    if [ "$rc" -eq 0 ]; then
      echo "FAIL: $gate passed a script with an early-closing pipe — the gate cannot fail:" >&2
      printf '%s\n' "$out" >&2
      return 1
    fi
    if ! grep -qF -- "$tmp/gate-early.sh:3:" <<<"$out"; then
      echo "FAIL: $gate failed (rc=$rc) but did not name $tmp/gate-early.sh:3:" >&2
      printf '%s\n' "$out" >&2
      return 1
    fi
    rc=0
    out="$(gate_on "$tmp/gate-clean.sh" "$gate" 2>&1)" || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "FAIL: $gate failed a clean script (rc=$rc):" >&2
      printf '%s\n' "$out" >&2
      return 1
    fi
  done
  echo "  ok: an early-closing pipe fails run_pipe_rule and the full gate, at its line; a clean script passes both"
  # ...and run_all fails on shellcheck's finding too, so neither pass's status
  # can be dropped from it. dirty.sh is Case 2's unquoted expansion.
  rc=0
  gate_on "$tmp/dirty.sh" run_all >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: run_all passed a script shellcheck rejects — it drops run_shellcheck's status" >&2
    return 1
  fi
  echo "  ok: a shellcheck finding fails the full gate"

  echo "==> Self-test passed"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") run_all ;;
  *) usage ;;
esac
