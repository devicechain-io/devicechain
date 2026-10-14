#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Prefetch the Go module graph with a bounded retry, so a transient failure of the module
# proxy is absorbed here instead of failing whichever build step happened to trigger the
# download. CI fans the same modules out across ~25 matrix jobs; at that fan-out a rare
# proxy hiccup (an HTTP/2 stream reset such as `stream error: ... INTERNAL_ERROR`) becomes
# a regular red job unrelated to the diff.
#
# usage: hack/go-mod-download.sh [DIR ...]     prefetch the modules of each DIR (default: .)
#        hack/go-mod-download.sh --workspace   every module go.work declares
#        hack/go-mod-download.sh --retry -- CMD...   run CMD (a `go install` / `go run` of a
#                                              tool, which resolves outside any DIR) with the
#                                              same retry and fallback shape
#        hack/go-mod-download.sh --self-test
#
# Environment:
#   DC_GOMOD_ATTEMPTS  total attempts per directory, default 3 (the last one is the fallback)
#   DC_GOMOD_BACKOFF   seconds to sleep before attempt N+1, multiplied by N; default 5
#   DC_GOMOD_EXTRA_GOOS  space-separated GOOS values to prefetch IN ADDITION to the host's
#                      (e.g. "windows" for a job that vets GOOS=windows: files behind a
#                      windows build constraint import modules a linux `go list` never
#                      loads, so their zips would otherwise be fetched by the vet step)
#   DC_GOMOD_TIDY      "1" to also prefetch what `GOWORK=off go mod tidy -diff` fetches, for a
#                      job that runs hack/check-go-mod-tidy.sh (see TIDY FETCHES MORE below)
#
# 🔴 TIDY FETCHES MORE, AND FROM A DIFFERENT GRAPH. The tidy check resolves the module ON
# ITS OWN (GOWORK=off), so it wants the module's own versions rather than the workspace's,
# and tidy loads every build tag, every GOOS and the TESTS OF DEPENDENCIES -- `X tested by
# X.test imports Y`. None of that is in the `go list -deps -test ./...` set, so before
# DC_GOMOD_TIDY existed the tidy step was the one that downloaded those zips bare, and it is
# where both observed proxy failures landed: modernc.org/sqlite@v1.23.1 via
# `glebarez/go-sqlite.test` (device-state) and filippo.io/edwards25519@v1.2.0 via
# `jmoiron/sqlx.test` (cli). No `go list` pattern reproduces that set (tidy ignores
# constraints), so the prefetch runs the same `GOWORK=off go mod tidy -diff` the check runs,
# which writes nothing. Its VERDICT is not this script's business: drift (exit 1 with a diff
# on stdout) counts as a completed fetch and is left for the check to report; only a failure
# to resolve (exit 1 with no diff, or any other status) is retried.
#
# THE SHAPE, AND WHY. Attempts before the last use the configured GOPROXY unchanged. The
# LAST attempt rewrites that list so it falls through on any error: every `,` separator
# becomes `|` and `direct` is added at the end. The separator is the whole point. Per
# `go help goproxy` and cmd/go/internal/modfetch/proxy.go (`fallBackOnError = goproxy[i] == '|'`),
# a `,` moves to the next entry only on a 404 or 410 -- which is exactly what the default
# `https://proxy.golang.org,direct` does, and why it does nothing for a stream reset or a
# 5xx -- while a `|` moves on after ANY error. `direct` fetches from the origin version
# control host: different infrastructure from the proxy, so the fallback is not a retry of
# the thing that just failed.
#
# 🔴 CHECKSUM VERIFICATION STAYS ON. Nothing here touches GOSUMDB, GONOSUMDB, GONOSUMCHECK,
# GOFLAGS or GOINSECURE, and a module fetched direct is still verified against go.sum and
# the checksum database. The fallback changes WHERE bytes come from, never whether they
# are checked.
#
# 🔴 IT PREFETCHES IN WORKSPACE MODE, WITH `go list -deps -test`, AND BOTH ARE DELIBERATE.
# CI builds through go.work, and the workspace resolves HIGHER versions than a module does
# on its own (for command-delivery, golang.org/x/text v0.42.0 against v0.41.0 from its own
# go.mod). A per-module `GOWORK=off go mod download` therefore fills the cache with the
# wrong versions and leaves the very zips the build wants to be fetched by the build. And
# workspace-mode `go mod download` is no alternative: it appends the hashes of the whole
# build graph to go.work.sum, which hack/check-go-work-sum.sh reads as an incomplete
# committed file. `go list -deps -test ./...` resolves exactly what build, vet and test
# resolve and fetches the zips of the packages they compile (the same fetch path, so the
# same proxy faults), without recording anything the build would not.
#
# 🔴 A PREFETCH MUST NOT WRITE TO THE TREE, AND THAT IS ENFORCED. `go mod download` quietly
# rewrites go.mod and re-adds missing go.sum lines, which would hide exactly what the later
# `go.mod and go.sum are tidy` and workspace-sum steps exist to catch. This script hashes
# DIR/go.mod, DIR/go.sum, go.work and go.work.sum around the download and fails loudly if
# any of them moved.
#
# 🔴 A FAILURE STAYS A FAILURE. Retries print a ::warning:: so a flaky proxy is visible in
# the run summary instead of being absorbed silently; if every attempt fails the script
# exits 1 with the last attempt's output already on the log. There is no path that exits 0
# after a failed download.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
attempts="${DC_GOMOD_ATTEMPTS:-3}"
backoff="${DC_GOMOD_BACKOFF:-5}"

# fallback_proxy turns the configured proxy list into one that falls through on any error.
fallback_proxy() {
  local p="$1"
  case "$p" in
    off | direct) printf '%s' "$p" ;; # nothing to fall back from / to
    *)
      p="${p//,/|}"
      case "|$p|" in
        *"|direct|"*) printf '%s' "$p" ;;
        *) printf '%s|direct' "$p" ;;
      esac
      ;;
  esac
}

# tree_state prints a digest of every file a prefetch must leave alone.
tree_state() {
  local dir="$1" f
  for f in "$dir/go.mod" "$dir/go.sum" "$root/go.work" "$root/go.work.sum"; do
    if [ -f "$f" ]; then sha256sum "$f"; else echo "absent $f"; fi
  done
}

# prefetch resolves and fetches everything build, vet and test compile in DIR, in workspace
# mode, without touching the tree. The integration and interop tags cover the tagged vet step.
prefetch() {
  local os
  (cd "$1" && go list -deps -test -tags "integration interop" ./... >/dev/null) || return $?
  for os in ${DC_GOMOD_EXTRA_GOOS:-}; do
    (cd "$1" && GOOS="$os" go list -deps -test -tags "integration interop" ./... >/dev/null) || return $?
  done
  if [ "${DC_GOMOD_TIDY:-}" = 1 ]; then
    prefetch_tidy "$1" || return $?
  fi
}

# prefetch_tidy fetches what hack/check-go-mod-tidy.sh will fetch, by running the same command.
# Tidy and drift are both a completed fetch; anything else is a failure to retry.
prefetch_tidy() {
  local out rc=0
  out="$(cd "$1" && GOWORK=off go mod tidy -diff)" || rc=$?
  if [ "$rc" -eq 0 ] || { [ "$rc" -eq 1 ] && [[ "$out" == "diff "* ]]; }; then
    return 0
  fi
  return "$rc"
}

download_dir() {
  local dir="$1" n rc fb before
  before="$(tree_state "$dir")"
  for ((n = 1; n <= attempts; n++)); do
    rc=0
    if [ "$n" -eq "$attempts" ] && [ "$attempts" -gt 1 ]; then
      fb="$(fallback_proxy "$(cd "$dir" && go env GOPROXY)")"
      echo "module prefetch in $dir: attempt $n/$attempts with GOPROXY=$fb"
      GOPROXY="$fb" prefetch "$dir" || rc=$?
    else
      echo "module prefetch in $dir: attempt $n/$attempts"
      prefetch "$dir" || rc=$?
    fi
    if [ "$(tree_state "$dir")" != "$before" ]; then
      echo "::error::module prefetch in $dir modified go.mod, go.sum, go.work or go.work.sum; a prefetch must not write to the tree"
      return 1
    fi
    if [ "$rc" -eq 0 ]; then
      return 0
    fi
    if [ "$n" -lt "$attempts" ]; then
      echo "::warning::module prefetch in $dir failed (exit $rc) on attempt $n/$attempts; retrying in $((backoff * n))s"
      sleep "$((backoff * n))"
    fi
  done
  echo "::error::module prefetch in $dir failed on all $attempts attempts"
  return 1
}

# retry_cmd runs a command that fetches modules outside any directory's graph (a `go install
# tool@version`, a `go run tool@latest`), with the same attempts, backoff and last-attempt
# fallback as download_dir. There is no tree to guard here. Every attempt streams the command's
# own output, so the final failure's error is on the log exactly as the command printed it; the
# exit status is the command's own on the last attempt.
retry_cmd() {
  local n rc fb
  for ((n = 1; n <= attempts; n++)); do
    rc=0
    if [ "$n" -eq "$attempts" ] && [ "$attempts" -gt 1 ]; then
      fb="$(fallback_proxy "$(go env GOPROXY)")"
      echo "retry '$*': attempt $n/$attempts with GOPROXY=$fb"
      GOPROXY="$fb" "$@" || rc=$?
    else
      echo "retry '$*': attempt $n/$attempts"
      "$@" || rc=$?
    fi
    if [ "$rc" -eq 0 ]; then
      return 0
    fi
    if [ "$n" -lt "$attempts" ]; then
      echo "::warning::'$*' failed (exit $rc) on attempt $n/$attempts; retrying in $((backoff * n))s"
      sleep "$((backoff * n))"
    fi
  done
  echo "::error::'$*' failed on all $attempts attempts"
  return 1
}

self_test() {
  local tmp bin fail=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  mkdir "$tmp/bin" "$tmp/mod" "$tmp/mod2"
  bin="$tmp/bin"
  for d in mod mod2; do
    echo "module example.com/$d" >"$tmp/$d/go.mod"
    : >"$tmp/$d/go.sum"
  done
  # A fake `go`: `env GOPROXY` prints $FAKE_GOPROXY; any other call appends one line to
  # $FAKE_LOG recording the GOPROXY and the settings that must never change, and succeeds
  # only on the call numbers in $FAKE_OK_ON, or -- when that contains "fallback" -- only
  # when the proxy list ends in a pipe fallback. $FAKE_FAIL_DIR makes every call in a
  # directory of that name fail; $FAKE_TOUCH names a file in the cwd to modify.
  cat >"$bin/go" <<'FAKE'
#!/usr/bin/env bash
if [ "$1" = env ]; then printf '%s\n' "$FAKE_GOPROXY"; exit 0; fi
echo "GOPROXY=${GOPROXY-<unset>} GOWORK=${GOWORK-<unset>} GONOSUMDB=${GONOSUMDB-<unset>} GOSUMDB=${GOSUMDB-<unset>} GOFLAGS=${GOFLAGS-<unset>} GOINSECURE=${GOINSECURE-<unset>} GONOSUMCHECK=${GONOSUMCHECK-<unset>} DIR=$(basename "$PWD") ARGS=$*" >>"$FAKE_LOG"
n="$(wc -l <"$FAKE_LOG")"
[ -z "${FAKE_TOUCH-}" ] || echo "# touched" >>"$FAKE_TOUCH"
if [ "$*" = "mod tidy -diff" ] && [ -n "${FAKE_TIDY_DRIFT-}" ]; then
  printf 'diff current/go.mod proposed/go.mod\n+require example.com/x v1.0.0\n'
  exit 1
fi
[ -z "${FAKE_FAIL_DIR-}" ] || [ "$(basename "$PWD")" != "$FAKE_FAIL_DIR" ] || { echo "stream error: INTERNAL_ERROR" >&2; exit 1; }
case " $FAKE_OK_ON " in
  *" $n "*) exit 0 ;;
  *" all "*) exit 0 ;;
  *" fallback "*) case "${GOPROXY-}" in *"|direct") exit 0 ;; esac ;;
esac
echo "stream error: INTERNAL_ERROR" >&2
exit 1
FAKE
  chmod +x "$bin/go"

  # exec_script ARGS... runs this script against the stand-in go, capped at 60s so a loop that
  # never ends is a FAILED case rather than a hung job. A hang is not a kill.
  exec_script() {
    env -u GONOSUMDB -u GOSUMDB -u GOFLAGS -u GOINSECURE -u GONOSUMCHECK -u GOWORK \
      PATH="$bin:$PATH" FAKE_LOG="$tmp/log" FAKE_OK_ON="${FAKE_OK_ON-}" FAKE_GOPROXY="${FAKE_GOPROXY-https://proxy.golang.org,direct}" \
      DC_GOMOD_ATTEMPTS="${ATT:-3}" DC_GOMOD_BACKOFF=0 timeout 60 bash "$0" "$@" >"$tmp/out" 2>&1
  }

  # run NAME WANT_RC WANT_CALLS OK_ON [ATTEMPTS] -- DIRS...
  run() {
    local name="$1" want_rc="$2" want_calls="$3" ok="$4" att="$5" rc=0 calls
    shift 5
    : >"$tmp/log"
    FAKE_OK_ON="$ok" ATT="$att" exec_script "$@" || rc=$?
    calls="$(wc -l <"$tmp/log")"
    if [ "$rc" -ne "$want_rc" ] || [ "$calls" -ne "$want_calls" ]; then
      echo "FAIL $name: rc=$rc (want $want_rc) calls=$calls (want $want_calls)"
      cat "$tmp/out"
      fail=1
    else
      echo "ok   $name"
    fi
  }

  run "first attempt succeeds, no retry" 0 1 "1" 3 "$tmp/mod"
  run "a transient failure is retried" 0 2 "2" 3 "$tmp/mod"
  run "the last attempt falls back through a pipe" 0 3 "fallback" 3 "$tmp/mod"
  run "every attempt failing is a failure" 1 3 "" 3 "$tmp/mod"
  run "a single attempt does not use the fallback" 1 1 "fallback" 1 "$tmp/mod"
  run "every directory is prefetched" 0 2 "all" 3 "$tmp/mod" "$tmp/mod2"
  FAKE_FAIL_DIR=mod2 run "a failure in a later directory fails the run" 1 4 "all" 3 "$tmp/mod" "$tmp/mod2"

  # --retry: same shape for an arbitrary command.
  run "retry mode: a transient failure is retried" 0 2 "2" 3 --retry -- go install example.com/tool@v1
  run "retry mode: the last attempt falls back through a pipe" 0 3 "fallback" 3 --retry -- go install example.com/tool@v1
  run "retry mode: every attempt failing is a failure" 1 3 "" 3 --retry -- go install example.com/tool@v1
  run "retry mode: a missing command is rejected" 2 0 "all" 3 --retry

  # DC_GOMOD_EXTRA_GOOS adds one list per extra OS, and a failure in it fails the run.
  : >"$tmp/log"
  DC_GOMOD_EXTRA_GOOS="windows" FAKE_OK_ON=all exec_script "$tmp/mod" && rc=0 || rc=$?
  if [ "$rc" -ne 0 ] || [ "$(wc -l <"$tmp/log")" -ne 2 ]; then
    echo "FAIL extra GOOS prefetch"
    cat "$tmp/log" "$tmp/out"
    fail=1
  else
    echo "ok   an extra GOOS is prefetched too"
  fi

  # DC_GOMOD_TIDY: the tidy check's own command, resolved GOWORK=off, after the list. Drift is
  # a completed fetch (the check reports it); a tidy that could not resolve is retried.
  DC_GOMOD_TIDY=1 run "tidy mode: list then tidy" 0 2 "all" 3 "$tmp/mod"
  if [ "$(tail -n1 "$tmp/log" | sed 's/.* ARGS=//')" != "mod tidy -diff" ] ||
    [ "$(tail -n1 "$tmp/log" | awk '{print $2}')" != "GOWORK=off" ] ||
    [ "$(head -n1 "$tmp/log" | awk '{print $2}')" != "GOWORK=<unset>" ]; then
    echo "FAIL tidy mode runs GOWORK=off go mod tidy -diff after a workspace-mode list"
    cat "$tmp/log"
    fail=1
  else
    echo "ok   tidy mode runs GOWORK=off go mod tidy -diff after a workspace-mode list"
  fi
  DC_GOMOD_TIDY=1 FAKE_TIDY_DRIFT=1 run "tidy mode: drift is a completed fetch, not a retry" 0 2 "1" 3 "$tmp/mod"
  DC_GOMOD_TIDY=1 run "tidy mode: a tidy that fails to fetch is retried" 0 4 "1 3 4" 3 "$tmp/mod"
  DC_GOMOD_TIDY=1 run "tidy mode: a tidy failing on every attempt is a failure" 1 6 "1 3 5" 3 "$tmp/mod"
  run "without tidy mode no tidy runs" 0 1 "all" 3 "$tmp/mod"

  # Anything that moves go.mod or go.sum must fail the run, not be absorbed.
  : >"$tmp/log"
  FAKE_TOUCH=go.sum FAKE_OK_ON=all exec_script "$tmp/mod" && rc=0 || rc=$?
  if [ "$rc" -ne 1 ] || ! grep -q '^::error::.*must not write to the tree' "$tmp/out"; then
    echo "FAIL a prefetch that rewrites go.sum must fail"
    cat "$tmp/out"
    fail=1
  else
    echo "ok   a prefetch that rewrites go.sum fails"
  fi
  : >"$tmp/mod/go.sum"

  # The exact environment of each attempt, which the cases above only imply.
  : >"$tmp/log"
  FAKE_OK_ON="" FAKE_GOPROXY="https://p.example,direct" exec_script "$tmp/mod" || true
  if [ "$(tail -n1 "$tmp/log" | awk '{print $1}')" != "GOPROXY=https://p.example|direct" ] ||
    [ "$(head -n1 "$tmp/log" | awk '{print $1}')" != "GOPROXY=<unset>" ] ||
    [ "$(grep -c '^::warning::' "$tmp/out")" -ne 2 ] || ! grep -q '^::error::' "$tmp/out"; then
    echo "FAIL fallback shape"
    cat "$tmp/log" "$tmp/out"
    fail=1
  else
    echo "ok   fallback shape (pipe on the last attempt only, a warning per retry, an error at the end)"
  fi
  # Checksum verification stays on and the workspace stays in use on every attempt.
  if [ "$(awk '{print $2,$3,$4,$5,$6,$7}' "$tmp/log" | sort -u)" != "GOWORK=<unset> GONOSUMDB=<unset> GOSUMDB=<unset> GOFLAGS=<unset> GOINSECURE=<unset> GONOSUMCHECK=<unset>" ]; then
    echo "FAIL a prefetch changed GOWORK or a checksum-verification setting"
    cat "$tmp/log"
    fail=1
  else
    echo "ok   GOWORK and the checksum settings are untouched on every attempt"
  fi

  if [ "$(fallback_proxy off)" = off ] && [ "$(fallback_proxy direct)" = direct ] &&
    [ "$(fallback_proxy 'https://a,https://b,direct')" = 'https://a|https://b|direct' ] &&
    [ "$(fallback_proxy 'https://a')" = 'https://a|direct' ]; then
    echo "ok   fallback_proxy rewrites"
  else
    echo "FAIL fallback_proxy rewrites"
    fail=1
  fi

  if [ "$fail" -eq 0 ]; then
    echo "self-test passed"
  fi
  return "$fail"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi

if [ "${1:-}" = "--retry" ]; then
  shift
  [ "${1:-}" = "--" ] && shift
  if [ "$#" -eq 0 ]; then
    echo "::error::--retry needs a command: hack/go-mod-download.sh --retry -- CMD..." >&2
    exit 2
  fi
  retry_cmd "$@"
  exit $?
fi

if [ "${1:-}" = "--workspace" ]; then
  cd "$root"
  mapfile -t mods < <(hack/list-workspace-modules.sh)
  [ "${#mods[@]}" -gt 0 ] || { echo "::error::no workspace modules found in go.work" >&2; exit 2; }
  set -- "${mods[@]}"
fi
[ "$#" -gt 0 ] || set -- .
for d in "$@"; do
  download_dir "$d"
done
