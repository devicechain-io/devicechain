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
#        hack/go-mod-download.sh --self-test
#
# Environment:
#   DC_GOMOD_ATTEMPTS  total attempts per directory, default 3 (the last one is the fallback)
#   DC_GOMOD_BACKOFF   seconds to sleep before attempt N+1, multiplied by N; default 5
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
  (cd "$1" && go list -deps -test -tags "integration interop" ./... >/dev/null)
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
echo "GOPROXY=${GOPROXY-<unset>} GOWORK=${GOWORK-<unset>} GONOSUMDB=${GONOSUMDB-<unset>} GOSUMDB=${GOSUMDB-<unset>} GOFLAGS=${GOFLAGS-<unset>} GOINSECURE=${GOINSECURE-<unset>} GONOSUMCHECK=${GONOSUMCHECK-<unset>} DIR=$(basename "$PWD")" >>"$FAKE_LOG"
n="$(wc -l <"$FAKE_LOG")"
[ -z "${FAKE_TOUCH-}" ] || echo "# touched" >>"$FAKE_TOUCH"
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
