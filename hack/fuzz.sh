#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Finds every native Go fuzz test in the workspace and fuzzes each one for a
# fixed time, judging each run by what it REPORTED rather than by its exit
# status alone.
#
#   hack/fuzz.sh                           # every fuzz test, FUZZTIME each
#   hack/fuzz.sh --list                    # "<pkg-dir> <FuzzName>" per target
#   hack/fuzz.sh --target <pkg-dir> <Name> # exactly one (pkg-dir relative to the repo root)
#   hack/fuzz.sh --self-test               # prove the classifier can fail; needs no Go toolchain
#
# Environment:
#   FUZZTIME       per-target fuzz time, whole seconds only (default 60s)
#   FUZZ_MINIMIZE  -fuzzminimizetime, whole seconds only (default 10s)
#   FUZZ_PARALLEL  worker count (default: 2 on a developer machine, GOMAXPROCS
#                  under GitHub Actions — parallel fuzz workers are memory-hungry)
#   FUZZ_GRACE     seconds added to FUZZTIME for the timeout budget: compile,
#                  baseline coverage and one minimisation (default 300)
#   FUZZ_LOG_DIR   where logs, failing inputs and summary.txt go (default: a new
#                  mktemp -d, printed at the end)
#
# ---------------------------------------------------------------------------
# WHY THIS EXISTS
#
# CI's `go test` runs a fuzz test's SEED inputs only; it never fuzzes. The
# differential fuzz tests in backend/core/graphql were written to find the next
# disagreement between our document reader and graphql-go, and nothing ran them.
# This is the runner, and fuzz-nightly.yml is what runs it.
#
# THE TRAPS IT CLOSES, each of which reads as a clean run
#
#   1. `go test -fuzz <pattern>` EXITS 0 when the pattern matches no fuzz test,
#      after printing only "testing: warning: no fuzz tests to fuzz"
#      ($GOROOT/src/testing/fuzz.go, fuzzing entry point). A renamed target, a
#      build tag, a typo — each is a green run that fuzzed nothing. So a run is
#      only PASS if the log shows the engine actually started fuzzing.
#   2. A pipe (`go test ... | tail`) reports the status of the LAST command and
#      throws the reason away. One boundary failure has already been lost that
#      way, with no log left to classify. Output goes to a file with `>`, never
#      through a pipe, and the whole log is kept.
#   3. A failing input is not always announced. The crash-minimisation path can
#      write an input to testdata/fuzz/<Name>/ without a "Failing input written
#      to" line, so the testdata directory is diffed as well as the text read.
#
# Each run also gets a private TMPDIR outside the log directory. The engine's
# worker shared-memory files live in $TMPDIR (internal/fuzz/mem.go), and a
# shared /tmp on a busy machine is one where something else can delete them; the
# `go` command's build work directory lands there too, and would otherwise end
# up in the uploaded artifact after a HANG.
#
# THE ONE EXIT THAT IS TOLERATED, AND WHY EXACTLY THAT ONE
#
# A run that fuzzes cleanly for the whole -fuzztime can still exit 1 with the
# single line `context deadline exceeded` and no input written. That is a race
# in Go's fuzz coordinator, not a finding (reported upstream as golang/go#75804;
# present in the go1.26 toolchain this repo builds with):
#
#   - internal/fuzz/fuzz.go wraps the -fuzztime deadline ctx in
#     `fuzzCtx, cancelWorkers := context.WithCancel(ctx)`, and at the deadline
#     calls `stop(ctx.Err())`. stop() suppresses the error only when
#     `err == fuzzCtx.Err()`.
#   - context/context.go's cancelCtx.cancel CLOSES the parent's Done channel
#     BEFORE it cancels the children. The coordinator can wake on the parent's
#     Done in between, read fuzzCtx.Err() == nil, and record DeadlineExceeded as
#     the run's error. testing then fails the target and prints it.
#
# It was observed, not inferred: 1 of 60 sequential 60 s reproduction runs of
# the graphql fuzzers ended this way (FuzzOperationType, elapsed 1m0s, no input),
# and a 20-line program that only does WithTimeout + WithCancel shows the parent
# erred while the child had not in a few of every 20 000 deadlines. The error
# value is only ever ctx.Err() of the deadline context, so the line cannot mean
# anything but "the time budget ran out".
#
# The tolerance is therefore as narrow as the cause: rc 1, fuzzing started, no
# input written or reported, the target's FAIL block holds EXACTLY that one
# line, and the run's last `fuzz: elapsed:` reached FUZZTIME (the coordinator's
# clock starts before the deadline is set, so a genuine instance always does).
# Anything else — another engine error, an extra line, an early end, an
# interrupt's `context canceled` — is FAILED. A TOLERATED run is reported as a
# warning, never silently.
#
# NOT tolerated, deliberately: an earlier boundary failure whose log was lost
# could also have been a failure to remove a worker's shared-memory file at
# cleanup (fuzz.go, the worker goroutine's `w.cleanup()` error), a worker killed
# in the last stats tick ("terminated by unexpected signal"), or a failed write
# to the fuzz cache. None of those was observed, so none is exempt; a
# recurrence turns the nightly red WITH ITS LOG.
# ---------------------------------------------------------------------------

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The self-test substitutes a fake `go` here, so it can drive the REAL per-target
# runner and loop without a toolchain. Nothing else should set it.
GO_BIN="${FUZZ_GO_BIN:-go}"

FUZZTIME="${FUZZTIME:-60s}"
FUZZ_MINIMIZE="${FUZZ_MINIMIZE:-10s}"
FUZZ_GRACE="${FUZZ_GRACE:-300}"
if [ -z "${FUZZ_PARALLEL+x}" ]; then
  if [ "${GITHUB_ACTIONS:-}" = "true" ]; then FUZZ_PARALLEL=""; else FUZZ_PARALLEL=2; fi
fi

usage() {
  echo "usage: $0 [--list | --target <pkg-dir> <FuzzName> | --self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# validate_env: fail closed on a budget `timeout` cannot honour. An iteration
# count (`-fuzztime 1000x`) has no wall-clock bound, so it is refused rather than
# guessed at.
# ---------------------------------------------------------------------------
validate_env() {
  [[ "$FUZZTIME" =~ ^[1-9][0-9]*s$ ]] || { echo "FUZZTIME must be whole seconds like 60s (got '$FUZZTIME')" >&2; return 2; }
  [[ "$FUZZ_MINIMIZE" =~ ^[1-9][0-9]*s$ ]] || { echo "FUZZ_MINIMIZE must be whole seconds like 10s (got '$FUZZ_MINIMIZE')" >&2; return 2; }
  [[ "$FUZZ_GRACE" =~ ^[0-9]+$ ]] || { echo "FUZZ_GRACE must be a whole number of seconds (got '$FUZZ_GRACE')" >&2; return 2; }
  [[ -z "$FUZZ_PARALLEL" || "$FUZZ_PARALLEL" =~ ^[1-9][0-9]*$ ]] || { echo "FUZZ_PARALLEL must be a positive integer (got '$FUZZ_PARALLEL')" >&2; return 2; }
  return 0
}

# ---------------------------------------------------------------------------
# modules <root>: the workspace's module directories, from go.work via `go list`
# (the same enumeration as the CLAUDE.md sweep and CI's discover job).
# ---------------------------------------------------------------------------
modules() {
  (cd "$1" && "$GO_BIN" list -m -f '{{.Dir}}')
}

# module_of <pkg-abs-dir> <module-dirs...>: the longest module dir containing it.
module_of() {
  local pkg="$1" best="" m
  shift
  for m in "$@"; do
    if [ "$pkg" = "$m" ] || [[ "$pkg" == "$m"/* ]]; then
      [ "${#m}" -gt "${#best}" ] && best="$m"
    fi
  done
  printf '%s' "$best"
}

# ---------------------------------------------------------------------------
# discover <root>: prints "<pkg-dir-relative-to-root> <FuzzName>" per target.
#
# TRACKED files only (git ls-files), for the reason hack/shellcheck.sh gives: an
# untracked scratch probe must never be fuzzed, or fail the run. The match is
# anchored at `^func` and requires a *testing.F parameter, so a commented-out
# declaration and a FuzzHelper(t *testing.T) are not targets. A file outside
# every workspace module (_legacy/) is reported and skipped.
#
# The output is CAPTURED and tested for emptiness, never judged by grep's exit
# status: zero targets is a failure, because a runner that found nothing and
# fuzzed nothing must not read as a clean night.
# ---------------------------------------------------------------------------
discover() {
  local root="$1" mods files line file name dir mod
  mods="$(modules "$root")" || { echo "discover: could not list workspace modules" >&2; return 1; }
  local -a modarr
  mapfile -t modarr <<<"$mods"
  files="$(git -C "$root" ls-files -- '*_test.go' | grep -vE '(^|/)(testdata|vendor)/')"
  local found=""
  if [ -n "$files" ]; then
    found="$(cd "$root" && printf '%s\n' "$files" | xargs -r -d '\n' grep -HE '^func Fuzz[A-Za-z0-9_]*\([A-Za-z_][A-Za-z0-9_]* \*testing\.F\)')"
  fi
  local out=""
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    file="${line%%:*}"
    name="$(printf '%s' "${line#*:}" | sed -E 's/^func (Fuzz[A-Za-z0-9_]*)\(.*/\1/')"
    dir="$(dirname "$file")"
    mod="$(module_of "$root/$dir" "${modarr[@]}")"
    if [ -z "$mod" ]; then
      echo "discover: skipping $name in $dir (not in a workspace module)" >&2
      continue
    fi
    out+="$dir $name"$'\n'
  done <<<"$found"
  out="$(printf '%s' "$out" | LC_ALL=C sort -u)"
  if [ -z "$out" ]; then
    echo "discovered no fuzz targets" >&2
    return 1
  fi
  printf '%s\n' "$out"
}

# ---------------------------------------------------------------------------
# go_seconds <go-duration>: whole seconds in a duration as Go prints it in the
# fuzz stats line ("59s", "1m0s", "1h2m3s"). Prints nothing for anything else.
# ---------------------------------------------------------------------------
go_seconds() {
  local d="$1" total=0 re='^(([0-9]+)h)?(([0-9]+)m)?(([0-9]+)s)$'
  [[ "$d" =~ $re ]] || return 0
  total=$(( 10#${BASH_REMATCH[2]:-0} * 3600 + 10#${BASH_REMATCH[4]:-0} * 60 + 10#${BASH_REMATCH[6]} ))
  printf '%s' "$total"
}

# fail_detail <logfile> <FuzzName>: the lines strictly between the target's
# `--- FAIL: <Name> (` header and the next bare `FAIL` line.
fail_detail() {
  awk -v hdr="--- FAIL: $2 (" '
    index($0, hdr) == 1 { inblk = 1; next }
    inblk && $0 == "FAIL" { exit }
    inblk { print }
  ' "$1"
}

# ---------------------------------------------------------------------------
# classify <rc> <logfile> <new-inputs-count> <FuzzName> <fuzztime-seconds>:
# prints ONE verdict word on the first line, then (for anything but PASS) the
# lines that explain it.
#
# Order matters, and each rule is its own line on purpose:
#   HANG      rc 124: the timeout budget expired (scored by rc, never by text —
#             a hung run's log can look like anything).
#   KILLED    rc 137: SIGKILL — the budget's --kill-after, or the OOM killer.
#             Not called a hang, because it may not have been one.
#   FINDING   an input was written (testdata diff) or reported.
#   SEED-FAIL a committed seed input failed before fuzzing started.
#   NOT-RUN   the engine never started fuzzing: no match (rc 0!), more than one
#             match, a build failure.
#   PASS      rc 0, fuzzing started, and go test reported PASS and ok.
#   TOLERATED rc 1, and the ONLY thing wrong is the coordinator's deadline race
#             (see the header): the FAIL block is exactly `context deadline
#             exceeded`, and the run's clock reached FUZZTIME.
#   FAILED    everything else — including a run that fuzzed, exited 0, and was
#             truncated before PASS.
# ---------------------------------------------------------------------------
classify() {
  local rc="$1" log="$2" newinputs="$3" name="$4" fuzzsecs="$5"
  if [ "$rc" = 124 ]; then echo HANG; tail -n 5 "$log"; return 0; fi
  if [ "$rc" = 137 ]; then echo KILLED; tail -n 5 "$log"; return 0; fi
  if [ "$newinputs" -gt 0 ] || grep -qE '^[[:space:]]*Failing input written to ' "$log"; then
    echo FINDING; sed -n '/^--- FAIL/,$p' "$log" | head -n 20; return 0
  fi
  if grep -qE '^failure while testing seed corpus entry: ' "$log"; then
    echo SEED-FAIL; grep -E -A 10 '^failure while testing seed corpus entry: ' "$log" | head -n 20; return 0
  fi
  if ! grep -qE '^fuzz: elapsed: .*now fuzzing with [0-9]+ workers$' "$log"; then
    echo NOT-RUN; tail -n 10 "$log"; return 0
  fi
  if [ "$rc" = 0 ] && grep -qE '^PASS$' "$log" && grep -qE '^ok[[:space:]]' "$log"; then
    echo PASS; return 0
  fi
  if [ "$rc" = 1 ]; then
    local detail last elapsed
    detail="$(fail_detail "$log" "$name")"
    last="$(sed -nE 's/^fuzz: elapsed: ([0-9hms]+), execs: .*/\1/p' "$log" | tail -n 1)"
    elapsed="$(go_seconds "$last")"
    if [ "$detail" = "    context deadline exceeded" ] && [ -n "$elapsed" ] && [ "$elapsed" -ge "$fuzzsecs" ]; then
      echo TOLERATED; printf '%s (last stats at %s)\n' "$detail" "$last"; return 0
    fi
  fi
  echo FAILED
  if grep -qE '^--- FAIL' "$log"; then sed -n '/^--- FAIL/,$p' "$log" | head -n 20; else tail -n 10 "$log"; fi
}

# list_inputs <dir>: the file names in a testdata/fuzz/<Name> dir, sorted; empty
# when the directory does not exist.
list_inputs() {
  [ -d "$1" ] || return 0
  find "$1" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort
}

# ---------------------------------------------------------------------------
# run_one <root> <log-dir> <pkg-dir> <FuzzName>: fuzz one target, classify it,
# append a line to <log-dir>/summary.txt. Returns 0 iff PASS or TOLERATED.
# ---------------------------------------------------------------------------
run_one() {
  local root="$1" logdir="$2" pkg="$3" name="$4"
  local mods mod rel log tmp budget rc verdict tddir before after new=0 f
  mods="$(modules "$root")" || { echo "run_one: could not list workspace modules" >&2; return 1; }
  local -a modarr
  mapfile -t modarr <<<"$mods"
  mod="$(module_of "$root/$pkg" "${modarr[@]}")"
  if [ -z "$mod" ]; then
    echo "$name NOT-RUN - - $pkg is not in a workspace module" >>"$logdir/summary.txt"
    echo "$name: $pkg is not in a workspace module" >&2
    return 1
  fi
  rel="${root}/${pkg}"
  rel="./${rel#"$mod"}"
  rel="${rel/#.\/\//.\/}"
  [ "$rel" = "./" ] && rel="."
  log="$logdir/$name.log"
  tddir="$root/$pkg/testdata/fuzz/$name"
  before="$(list_inputs "$tddir")"

  # Private TMPDIR, deliberately OUTSIDE the log directory (see header).
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-fuzz-$name.XXXXXX")" || return 1
  budget=$(( ${FUZZTIME%s} + FUZZ_GRACE ))
  local -a cmd=(timeout --kill-after=30s "${budget}s" "$GO_BIN" test -run '^$' -fuzz "^${name}\$"
    -fuzztime "$FUZZTIME" -fuzzminimizetime "$FUZZ_MINIMIZE")
  [ -n "$FUZZ_PARALLEL" ] && cmd+=(-parallel "$FUZZ_PARALLEL")
  cmd+=("$rel")

  {
    echo "# hack/fuzz.sh: $name in $pkg (module $mod), $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# TMPDIR=$tmp"
    echo "# ${cmd[*]}"
  } >"$log"
  echo "fuzzing $pkg $name (FUZZTIME=$FUZZTIME, budget ${budget}s) ..."
  # No pipe: the redirect keeps both the full output and the real exit status.
  (cd "$mod" && TMPDIR="$tmp" "${cmd[@]}") >>"$log" 2>&1
  rc=$?
  rm -rf "$tmp"

  after="$(list_inputs "$tddir")"
  if [ "$before" != "$after" ]; then
    mkdir -p "$logdir/inputs/$name"
    while IFS= read -r f; do
      [ -n "$f" ] || continue
      cp -- "$tddir/$f" "$logdir/inputs/$name/" && new=$((new + 1))
    done < <(LC_ALL=C comm -13 <(printf '%s\n' "$before") <(printf '%s\n' "$after"))
    # A listing that changed but yielded nothing to copy is still a change
    # nobody can explain; never let it read as "no new input".
    [ "$new" -gt 0 ] || new=1
  fi

  local result execs
  result="$(classify "$rc" "$log" "$new" "$name" "${FUZZTIME%s}")"
  verdict="${result%%$'\n'*}"
  execs="$(grep -oE '^fuzz: elapsed: [^,]*, execs: [0-9]+' "$log" | tail -n 1 | sed -E 's/.*execs: //')"
  echo "$name $verdict rc=$rc execs=${execs:--} $log" >>"$logdir/summary.txt"
  echo "  $name: $verdict (rc=$rc)"
  case "$verdict" in
    PASS) return 0 ;;
    TOLERATED)
      printf '%s\n' "$result" | tail -n +2 | sed 's/^/    /'
      if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
        echo "::warning title=fuzz::$name: tolerated Go fuzz-coordinator deadline race (golang/go#75804): $(printf '%s' "$result" | sed -n 2p | sed 's/^ *//')"
      fi
      return 0
      ;;
  esac
  printf '%s\n' "$result" | tail -n +2 | sed 's/^/    /'
  if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
    echo "::error title=fuzz::$name: $verdict (rc=$rc), log in the fuzz-logs artifact"
  fi
  return 1
}

# ---------------------------------------------------------------------------
# run_all <root> <log-dir> <targets>: every target, rc RECORDED (never
# `|| echo`, whose status is the echo's — always 0), so one failure does not
# hide the targets after it and cannot be hidden by the ones after it.
# ---------------------------------------------------------------------------
run_all() {
  local root="$1" logdir="$2" targets="$3" rc=0 pkg name n=0
  : >"$logdir/summary.txt"
  while read -r pkg name; do
    [ -n "$pkg" ] || continue
    n=$((n + 1))
    run_one "$root" "$logdir" "$pkg" "$name" </dev/null || rc=1
  done <<<"$targets"
  if [ "$n" -eq 0 ]; then
    echo "ran no fuzz targets" >&2
    return 1
  fi
  echo
  echo "summary ($n targets):"
  sed 's/^/  /' "$logdir/summary.txt"
  echo "logs: $logdir"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    { echo "### fuzz ($n targets)"; echo '```'; cat "$logdir/summary.txt"; echo '```'; } >>"$GITHUB_STEP_SUMMARY"
  fi
  return "$rc"
}

# ===========================================================================
# Self-test. Needs bash, git, coreutils — no Go. The per-target runner and the
# loop run FOR REAL against a fake `go` that plays back fixture logs, so what is
# tested is the code that runs at night, not a copy of its command line.
# ===========================================================================
self_test() {
  local T fails=0 cases=0
  T="$(mktemp -d)"
  SELF_TEST_TMP="$T"
  trap 'rm -rf "$SELF_TEST_TMP"' EXIT

  expect() { # <case> <expected> <actual>
    cases=$((cases + 1))
    if [ "$2" != "$3" ]; then
      echo "self-test FAILED: $1: expected '$2', got '$3'" >&2
      fails=$((fails + 1))
    fi
  }

  # ---------------- classify, on fixture logs ----------------
  local L="$T/logs"
  mkdir -p "$L"
  # Trimmed from a real passing 60 s run (paths removed).
  cat >"$L/pass" <<'EOF'
fuzz: elapsed: 0s, gathering baseline coverage: 0/1257 completed
fuzz: elapsed: 1s, gathering baseline coverage: 1257/1257 completed, now fuzzing with 2 workers
fuzz: elapsed: 3s, execs: 23117 (7705/sec), new interesting: 0 (total: 1257)
fuzz: elapsed: 57s, execs: 427725 (8611/sec), new interesting: 7 (total: 1264)
fuzz: elapsed: 1m0s, execs: 453275 (8516/sec), new interesting: 7 (total: 1264)
fuzz: elapsed: 1m0s, execs: 453275 (0/sec), new interesting: 7 (total: 1264)
PASS
ok  	github.com/devicechain-io/dc-microservice/graphql	60.150s
EOF
  cat >"$L/nomatch" <<'EOF'
testing: warning: no fuzz tests to fuzz
PASS
ok  	github.com/devicechain-io/dc-microservice/graphql	0.012s
EOF
  cat >"$L/multi" <<'EOF'
testing: will not fuzz, -fuzz matches more than one fuzz test: [FuzzA FuzzAB]
FAIL
exit status 1
FAIL	github.com/devicechain-io/dc-microservice/graphql	0.010s
EOF
  cat >"$L/compile" <<'EOF'
# github.com/devicechain-io/dc-microservice/graphql [github.com/devicechain-io/dc-microservice/graphql.test]
graphql/root_fields_test.go:12:2: undefined: nope
FAIL	github.com/devicechain-io/dc-microservice/graphql [build failed]
EOF
  # Trimmed from a real failing run: a worker died while minimising.
  cat >"$L/finding" <<'EOF'
fuzz: elapsed: 2s, gathering baseline coverage: 357/357 completed, now fuzzing with 4 workers
fuzz: elapsed: 51s, execs: 508494 (47039/sec), new interesting: 57 (total: 414)
--- FAIL: FuzzRootFieldLimit (51.63s)
    fuzzing process hung or terminated unexpectedly while minimizing: EOF
    Failing input written to testdata/fuzz/FuzzRootFieldLimit/83fcc343b229fb2b
    To re-run:
    go test -run=FuzzRootFieldLimit/83fcc343b229fb2b
FAIL
exit status 1
FAIL	github.com/devicechain-io/dc-microservice/graphql	51.648s
EOF
  # The boundary shape: rc 1 at the deadline, nothing written. Not tolerated.
  cat >"$L/cleanup" <<'EOF'
fuzz: elapsed: 1s, gathering baseline coverage: 1257/1257 completed, now fuzzing with 2 workers
fuzz: elapsed: 1m0s, execs: 453275 (0/sec), new interesting: 7 (total: 1264)
--- FAIL: FuzzRootFieldLimit (60.10s)
    remove /tmp/fuzz-12: no such file or directory
FAIL
exit status 1
FAIL	github.com/devicechain-io/dc-microservice/graphql	60.128s
EOF
  # Observed in the reproduction runs (go1.26.6), trimmed: the coordinator's
  # deadline race. The ONE shape that is tolerated.
  cat >"$L/deadline" <<'EOF'
fuzz: elapsed: 0s, gathering baseline coverage: 0/968 completed
fuzz: elapsed: 7s, gathering baseline coverage: 968/968 completed, now fuzzing with 2 workers
fuzz: elapsed: 57s, execs: 354298 (8120/sec), new interesting: 9 (total: 977)
fuzz: elapsed: 1m0s, execs: 378610 (8103/sec), new interesting: 9 (total: 977)
fuzz: elapsed: 1m0s, execs: 378610 (0/sec), new interesting: 9 (total: 977)
--- FAIL: FuzzOperationType (60.13s)
    context deadline exceeded
FAIL
exit status 1
FAIL	github.com/devicechain-io/dc-microservice/graphql	60.150s
EOF
  sed 's/^    context deadline exceeded$/&\n    something else/' "$L/deadline" >"$L/deadline-extra"
  sed 's/^    context deadline exceeded$/    context canceled/' "$L/deadline" >"$L/deadline-canceled"
  sed 's/^    context deadline exceeded$/xx  context deadline exceeded/' "$L/deadline" >"$L/deadline-prefixed"
  sed 's/^    context deadline exceeded$/    context deadline exceeded (retrying)/' "$L/deadline" >"$L/deadline-suffixed"
  grep -v '^fuzz: elapsed: 1m0s' "$L/deadline" | sed 's/FuzzOperationType (60.13s)/FuzzOperationType (57.40s)/' >"$L/deadline-early"
  sed 's/^--- FAIL: FuzzOperationType /--- FAIL: FuzzOther /' "$L/deadline" >"$L/deadline-othertarget"
  cat >"$L/signal" <<'EOF'
fuzz: elapsed: 1s, gathering baseline coverage: 1257/1257 completed, now fuzzing with 2 workers
--- FAIL: FuzzRootFieldLimit (59.91s)
    fuzzing process terminated by unexpected signal; no crash will be recorded: signal: killed
FAIL
exit status 1
FAIL	github.com/devicechain-io/dc-microservice/graphql	60.001s
EOF
  cat >"$L/partial" <<'EOF'
fuzz: elapsed: 1s, gathering baseline coverage: 1257/1257 completed, now fuzzing with 2 workers
fuzz: elapsed: 3s, execs: 23117 (7705/sec), new interesting: 0 (total: 1257)
EOF
  cat >"$L/seed" <<'EOF'
fuzz: elapsed: 0s, gathering baseline coverage: 0/12 completed
failure while testing seed corpus entry: FuzzRootFieldLimit/seed#3
fuzz: elapsed: 0s, gathering baseline coverage: 3/12 completed
--- FAIL: FuzzRootFieldLimit (0.05s)
    --- FAIL: FuzzRootFieldLimit (0.00s)
        root_fields_test.go:260: limit bypassed
FAIL
exit status 1
FAIL	github.com/devicechain-io/dc-microservice/graphql	0.061s
EOF
  cat >"$L/failnoinput" <<'EOF'
fuzz: elapsed: 1s, gathering baseline coverage: 1257/1257 completed, now fuzzing with 2 workers
--- FAIL: FuzzRootFieldLimit (12.00s)
    some engine error
FAIL
EOF

  verdict_of() { classify "$1" "$L/$2" "$3" "${4:-FuzzRootFieldLimit}" "${5:-60}" | head -n 1; }
  expect "1 real passing run"                        PASS      "$(verdict_of 0 pass 0)"
  expect "2 no-match exits 0 (the trap)"             NOT-RUN   "$(verdict_of 0 nomatch 0)"
  expect "3 -fuzz matched two targets"               NOT-RUN   "$(verdict_of 1 multi 0)"
  expect "4 compile error"                           NOT-RUN   "$(verdict_of 1 compile 0)"
  expect "5 real minimise-EOF finding"               FINDING   "$(verdict_of 1 finding 1)"
  expect "6 boundary cleanup error is NOT tolerated" FAILED    "$(verdict_of 1 cleanup 0)"
  expect "7 worker killed by signal"                 FAILED    "$(verdict_of 1 signal 0)"
  expect "8 rc 124 is a hang, whatever the log"      HANG      "$(verdict_of 124 partial 0)"
  expect "9 rc 124 even over a PASS log"             HANG      "$(verdict_of 124 pass 0)"
  expect "10 rc 137 is KILLED"                       KILLED    "$(verdict_of 137 partial 0)"
  expect "11 new testdata input, no text line"       FINDING   "$(verdict_of 1 failnoinput 1)"
  expect "12 fuzzed, rc 0, truncated before PASS"    FAILED    "$(verdict_of 0 partial 0)"
  expect "13 a seed input failed"                    SEED-FAIL "$(verdict_of 1 seed 0)"
  expect "14 rc 1 with a PASS-shaped log"            FAILED    "$(verdict_of 1 pass 0)"
  expect "15 an input over a PASS log"               FINDING   "$(verdict_of 0 pass 1)"
  expect "t1 the observed deadline race"             TOLERATED "$(verdict_of 1 deadline 0 FuzzOperationType 60)"
  expect "t2 ...plus a second detail line"           FAILED    "$(verdict_of 1 deadline-extra 0 FuzzOperationType 60)"
  expect "t3 an interrupt's context canceled"        FAILED    "$(verdict_of 1 deadline-canceled 0 FuzzOperationType 60)"
  expect "t4 the line, not indented as go prints it" FAILED    "$(verdict_of 1 deadline-prefixed 0 FuzzOperationType 60)"
  expect "t5 the line with more after it"            FAILED    "$(verdict_of 1 deadline-suffixed 0 FuzzOperationType 60)"
  expect "t6 the clock never reached FUZZTIME"       FAILED    "$(verdict_of 1 deadline-early 0 FuzzOperationType 60)"
  expect "t7 FUZZTIME longer than the run reached"   FAILED    "$(verdict_of 1 deadline 0 FuzzOperationType 300)"
  expect "t8 the FAIL block is another target's"     FAILED    "$(verdict_of 1 deadline 0 FuzzRootFieldLimit 60)"
  expect "t9 the race line but an input was written" FINDING   "$(verdict_of 1 deadline 1 FuzzOperationType 60)"
  expect "t10 the race line under rc 2"              FAILED    "$(verdict_of 2 deadline 0 FuzzOperationType 60)"
  expect "t11 the race line under rc 124"            HANG      "$(verdict_of 124 deadline 0 FuzzOperationType 60)"
  expect "t12 go_seconds 1m0s" 60 "$(go_seconds 1m0s)"
  expect "t13 go_seconds 1h2m3s" 3723 "$(go_seconds 1h2m3s)"
  expect "t14 go_seconds 59s" 59 "$(go_seconds 59s)"
  expect "t15 go_seconds of garbage is empty" "" "$(go_seconds 1.5s)"

  # ---------------- the real runner, against a fake go ----------------
  local R="$T/repo" F="$T/fake"
  mkdir -p "$R/modA/pkg/testdata" "$R/modA/pkg/sub" "$R/modB" "$R/_legacy" "$F"
  cat >"$R/modA/pkg/a_test.go" <<'EOF'
package pkg
func FuzzX(f *testing.F) {}
func FuzzY(ff *testing.F) {}
// func FuzzZ(f *testing.F) {}
func FuzzHelper(t *testing.T) {}
EOF
  printf 'package testdata\nfunc FuzzT(f *testing.F) {}\n' >"$R/modA/pkg/testdata/x_test.go"
  printf 'package modb\nfunc FuzzW(f *testing.F) {}\n' >"$R/modB/b_test.go"
  printf 'package legacy\nfunc FuzzL(f *testing.F) {}\n' >"$R/_legacy/l_test.go"
  printf 'package sub\nfunc FuzzU(f *testing.F) {}\n' >"$R/modA/pkg/sub/u_test.go"
  (cd "$R" && git init -q && git add modA modB _legacy) || { echo "self-test: git setup failed" >&2; return 1; }
  # Untracked: a local scratch probe must never be discovered.
  printf 'package pkg\nfunc FuzzScratch(f *testing.F) {}\n' >"$R/modA/pkg/scratch_test.go"

  # The fake go. `list -m` prints the module dirs; `test` records what it was
  # run with, then plays back $F/<Name>.{log,rc,write,sleep}. A -fuzz argument
  # that is not exactly ^<Name>$ for a known fixture behaves like real go test
  # does on a pattern that matches nothing: a warning and exit 0.
  cat >"$F/go" <<EOF
#!/usr/bin/env bash
F='$F'
if [ "\$1" = list ]; then printf '%s\n' '$R/modA' '$R/modB'; exit 0; fi
name=""; prev=""
for a in "\$@"; do [ "\$prev" = -fuzz ] && name="\$a"; prev="\$a"; done
n="\${name#^}"; n="\${n%\\\$}"
if [ "\$name" != "^\$n\\\$" ] || [ ! -f "\$F/\$n.log" ]; then
  echo "testing: warning: no fuzz tests to fuzz"; echo PASS; echo "ok  	fake	0.01s"; exit 0
fi
{ echo "cwd=\$PWD"; echo "tmpdir=\$TMPDIR"; [ -d "\$TMPDIR" ] && echo "tmpdir-exists"; echo "argv=\$*"; } >"\$F/\$n.argv"
[ -f "\$F/\$n.sleep" ] && exec sleep "\$(cat "\$F/\$n.sleep")"
[ -f "\$F/\$n.write" ] && { mkdir -p "\$(cat "\$F/\$n.write")"; echo corpus >"\$(cat "\$F/\$n.write")/deadbeef"; }
cat "\$F/\$n.log"
exit "\$(cat "\$F/\$n.rc")"
EOF
  chmod +x "$F/go"
  local saved_go="$GO_BIN"
  GO_BIN="$F/go"

  local got
  got="$(discover "$R" 2>/dev/null | tr '\n' ' ')"
  expect "16 discovery: anchored, *testing.F only, tracked only, no testdata/_legacy" \
    "modA/pkg FuzzX modA/pkg FuzzY modA/pkg/sub FuzzU modB FuzzW " "$got"
  local E="$T/empty"
  mkdir -p "$E/modA" && (cd "$E" && git init -q)
  discover "$E" >/dev/null 2>&1
  expect "17 discovery over an empty tree fails" 1 "$?"

  # --- one passing and one failing target: the loop must fail, in either order.
  play() { cp "$L/$2" "$F/$1.log"; echo "$3" >"$F/$1.rc"; }
  local D1="$T/out1"
  mkdir -p "$D1"
  play FuzzX finding 1; echo "$R/modA/pkg/testdata/fuzz/FuzzX" >"$F/FuzzX.write"
  play FuzzY pass 0
  FUZZTIME=60s FUZZ_PARALLEL=2 run_all "$R" "$D1" $'modA/pkg FuzzX\nmodA/pkg FuzzY' >/dev/null 2>&1
  expect "18 loop: FINDING then PASS fails" 1 "$?"
  expect "19 the failing input is copied out" corpus "$(cat "$D1/inputs/FuzzX/deadbeef" 2>/dev/null)"
  expect "20 summary records both verdicts" "FuzzX FINDING|FuzzY PASS" \
    "$(cut -d' ' -f1,2 "$D1/summary.txt" | paste -sd'|')"
  rm -rf "$R/modA/pkg/testdata/fuzz" "$F/FuzzX.write"

  local D2="$T/out2"
  mkdir -p "$D2"
  play FuzzX pass 0; play FuzzY cleanup 1
  run_all "$R" "$D2" $'modA/pkg FuzzX\nmodA/pkg FuzzY' >/dev/null 2>&1
  expect "21 loop: PASS then FAILED fails" 1 "$?"

  local D2b="$T/out2b"
  mkdir -p "$D2b"
  cp "$L/deadline" "$F/FuzzY.log"; sed -i 's/FuzzOperationType/FuzzY/' "$F/FuzzY.log"; echo 1 >"$F/FuzzY.rc"
  play FuzzX pass 0
  FUZZTIME=60s run_all "$R" "$D2b" $'modA/pkg FuzzX\nmodA/pkg FuzzY' >/dev/null 2>&1
  expect "21b loop: PASS then TOLERATED succeeds" "0 FuzzX PASS|FuzzY TOLERATED" \
    "$? $(cut -d' ' -f1,2 "$D2b/summary.txt" | paste -sd'|')"
  local D2c="$T/out2c"
  mkdir -p "$D2c"
  play FuzzX cleanup 1
  FUZZTIME=60s run_all "$R" "$D2c" $'modA/pkg FuzzX\nmodA/pkg FuzzY' >/dev/null 2>&1
  expect "21c loop: FAILED then TOLERATED fails" 1 "$?"
  local D2d="$T/out2d"
  mkdir -p "$D2d"
  play FuzzX pass 0
  FUZZTIME=300s run_all "$R" "$D2d" $'modA/pkg FuzzX\nmodA/pkg FuzzY' >/dev/null 2>&1
  expect "21d loop: the race line under a longer FUZZTIME fails" 1 "$?"

  local D3="$T/out3"
  mkdir -p "$D3"
  play FuzzX pass 0; play FuzzY pass 0
  FUZZ_PARALLEL=3 FUZZTIME=45s FUZZ_MINIMIZE=7s run_all "$R" "$D3" $'modA/pkg FuzzX\nmodA/pkg FuzzY' >/dev/null 2>&1
  expect "22 loop: all PASS succeeds" 0 "$?"

  # --- what the runner actually executed, as seen by the child.
  expect "23 run from the owning module dir" "cwd=$R/modA" "$(grep '^cwd=' "$F/FuzzX.argv")"
  expect "24 exact argv: anchored -fuzz, -run ^\$, the budgets, the package" \
    'argv=test -run ^$ -fuzz ^FuzzX$ -fuzztime 45s -fuzzminimizetime 7s -parallel 3 ./pkg' \
    "$(grep '^argv=' "$F/FuzzX.argv")"
  local ctmp
  ctmp="$(sed -n 's/^tmpdir=//p' "$F/FuzzX.argv")"
  expect "25 child got a private TMPDIR that existed" "tmpdir-exists" "$(grep -x tmpdir-exists "$F/FuzzX.argv")"
  local private=no
  if [ -n "$ctmp" ] && [ "$ctmp" != "${TMPDIR:-/tmp}" ] && [[ "$ctmp" != "$D3"* ]] && [ ! -e "$ctmp" ]; then private=yes; fi
  expect "26 TMPDIR is private, outside the log dir, and removed afterwards" yes "$private"
  expect "27 the log is the full output plus a header" 8 "$(grep -cv '^#' "$D3/FuzzX.log")"

  # --- the module root is a package too ("." not "./").
  local D4="$T/out4"
  mkdir -p "$D4"
  play FuzzW pass 0
  FUZZ_PARALLEL=2 FUZZTIME=60s FUZZ_MINIMIZE=10s run_one "$R" "$D4" modB FuzzW >/dev/null 2>&1
  expect "28 a target at the module root runs as ." "argv=test -run ^\$ -fuzz ^FuzzW\$ -fuzztime 60s -fuzzminimizetime 10s -parallel 2 ." \
    "$(grep '^argv=' "$F/FuzzW.argv")"

  # --- a name that does not exist: go exits 0, the runner must not.
  local D5="$T/out5"
  mkdir -p "$D5"
  run_all "$R" "$D5" 'modA/pkg FuzzDoesNotExist' >/dev/null 2>&1
  expect "29 a target that does not exist is not a pass" "1 NOT-RUN" "$? $(cut -d' ' -f2 "$D5/summary.txt")"

  # --- a hang is caught by the budget, not by waiting for it.
  local D6="$T/out6"
  mkdir -p "$D6"
  play FuzzU pass 0; echo 30 >"$F/FuzzU.sleep"
  FUZZTIME=1s FUZZ_GRACE=1 run_all "$R" "$D6" 'modA/pkg/sub FuzzU' >/dev/null 2>&1
  expect "30 a run past its budget is a HANG" "1 HANG" "$? $(cut -d' ' -f2 "$D6/summary.txt")"

  # --- budgets that cannot be honoured are refused.
  (FUZZTIME=100x; validate_env 2>/dev/null)
  expect "31 FUZZTIME=100x is refused" 2 "$?"
  (FUZZ_MINIMIZE=5; validate_env 2>/dev/null)
  expect "32 FUZZ_MINIMIZE=5 is refused" 2 "$?"

  GO_BIN="$saved_go"
  if [ "$fails" -ne 0 ]; then
    echo "self-test: $fails of $cases cases FAILED" >&2
    return 1
  fi
  echo "self-test: $cases cases passed"
}

# ===========================================================================
main() {
  case "${1:-}" in
    --self-test)
      [ $# -eq 1 ] || usage
      self_test
      exit $?
      ;;
    --list)
      [ $# -eq 1 ] || usage
      discover "$ROOT"
      exit $?
      ;;
    --target)
      [ $# -eq 3 ] || usage
      validate_env || exit 2
      targets="$2 $3"
      ;;
    "")
      validate_env || exit 2
      targets="$(discover "$ROOT")" || exit 1
      echo "discovered $(printf '%s\n' "$targets" | wc -l | tr -d ' ') fuzz targets"
      ;;
    *) usage ;;
  esac
  if [ -z "${FUZZ_LOG_DIR:-}" ]; then
    FUZZ_LOG_DIR="$(mktemp -d)" || exit 1
  fi
  mkdir -p "$FUZZ_LOG_DIR" || exit 1
  run_all "$ROOT" "$FUZZ_LOG_DIR" "$targets"
  exit $?
}

main "$@"
