#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if a workspace module's go.mod or go.sum is not what `go mod tidy` would leave
# them as, with the module resolved on its own (GOWORK=off) rather than through the
# workspace.
#
#   hack/check-go-mod-tidy.sh --module backend/core   # one module (the CI step)
#   hack/check-go-mod-tidy.sh                         # every module go.work lists
#   hack/check-go-mod-tidy.sh --self-test             # prove the check can fail
#
# Exit 0: tidy. Exit 1: drift (the diff and the command that fixes it are printed).
# Exit 2: the check could not run or could not decide; that is a refusal, never a pass.
#
# 🔴 WHY THE WORKSPACE HIDES IT. Every other Go step in CI builds, vets and tests
# through go.work, where a module's own requirements are not the ones that get built:
# the workspace's combined graph is. So a go.mod that is missing a requirement, still
# lists one the code no longer uses, or marks a direct import `// indirect` builds,
# vets and tests green everywhere, until something resolves the module alone — a
# GOWORK=off build, a release, anyone consuming the module outside this checkout.
# The cli and k8s modules drifted exactly that way from ordinary code changes and were
# tidied by hand.
#
# THE DEPENDABOT SHAPE. Every module outside core replaces core with the local
# ../../core. A bump that raises core's requirements leaves every module that replaces
# core behind core's graph, and a grouped Dependabot PR only tidies the directories it
# touched. That shape was already caught, but only incidentally: the graphql-go fork
# guard's `GOWORK=off go list -m` fails with `updates to go.mod needed` when graphql-go
# is absent from a module's graph, under a message about the fork. When graphql-go is
# present, or when the drift is an unused requirement, a wrong `// indirect` marker or
# a stale go.sum line, nothing noticed. This check catches all of them, by name, with
# the command that fixes them.
#
# 🔴 WHY THE EXIT STATUS ALONE IS NOT THE VERDICT. `go mod tidy -diff` exits 1 both for
# drift and for "could not resolve" (an unreachable proxy, an import nothing provides).
# Drift is exit 1 WITH a diff on stdout; a failure is exit 1 with nothing on stdout and
# the reason on stderr. Anything else is a refusal — including exit 0 with output,
# which no Go release produces today and which therefore must not be read as clean.
#
# GO.SUM IS CHECKED ONLY FOR WHAT TIDY WOULD CHANGE. Tidy keeps hashes it finds for
# modules in the graph (it is additive over an existing go.sum) and drops hashes for
# modules outside it. So a go.sum deliberately fuller than a from-scratch tidy — see
# backend/tools/promqlguard/go.mod — passes here; this check neither requires those
# extra hashes nor protects them (the fork guard is what needs them).
#
# GOWORK=off states the intent: the module's own files are the subject. As measured on
# the current toolchain `go mod tidy` ignores go.work anyway, so it is not load-bearing
# today; it holds if a future Go makes tidy workspace-aware.
#
# There is deliberately no --fix mode: a check that rewrites the files it checks is a
# way for CI to repair its own copy and pass. The failure prints the command to run.

set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# workspace_modules — go.work's modules, repo-relative without "./" (backend/core),
# via the toolchain-free reader CI's module discovery is cross-checked against.
workspace_modules() { "$ROOT/hack/list-workspace-modules.sh" "$ROOT/go.work" | sed 's|^\./||'; }

# check_module REL [MODS] — REL is repo-relative (backend/core); MODS is the workspace
# module list when the caller already has it. Returns 0 (tidy), 1 (drift), 2 (refused).
check_module() {
  local rel="${1%/}" mods="${2-}" out err rc=0
  rel="${rel#./}"
  if [ -z "$mods" ]; then
    mods="$(workspace_modules)" || { echo "check-go-mod-tidy: could not read go.work; refusing" >&2; return 2; }
  fi
  # Refuse a directory that is not a workspace member: a typo, or a renamed module,
  # must not report "tidy" for a path nobody builds. A here-string rather than a pipe
  # into grep -q, so an early-exiting grep cannot SIGPIPE the writer under pipefail
  # and turn a real member into a refusal.
  if ! grep -qxF -- "$rel" <<<"$mods"; then
    echo "check-go-mod-tidy: '$rel' is not a module listed in go.work; refusing" >&2
    return 2
  fi
  if [ ! -f "$ROOT/$rel/go.mod" ]; then
    echo "check-go-mod-tidy: '$rel/go.mod' does not exist; refusing" >&2
    return 2
  fi
  err="$(mktemp)" || return 2
  out="$(cd "$ROOT/$rel" && GOWORK=off go mod tidy -diff 2>"$err")" || rc=$?
  if [ "$rc" -eq 0 ] && [ -z "$out" ]; then
    rm -f "$err"
    echo "tidy: $rel"
    return 0
  fi
  if [ "$rc" -eq 1 ] && [[ "$out" == "diff "* ]]; then
    rm -f "$err"
    echo "::error::$rel: go.mod/go.sum are not tidy. Fix with: (cd $rel && GOWORK=off go mod tidy)"
    printf '%s\n' "$out"
    return 1
  fi
  echo "check-go-mod-tidy: could not decide whether $rel is tidy (go mod tidy -diff exited $rc); refusing" >&2
  if [ -n "$out" ]; then printf 'stdout:\n%s\n' "$out" >&2; fi
  sed 's/^/  /' "$err" >&2
  rm -f "$err"
  return 2
}

# check_all — every workspace module. The exit status is the WORST verdict (2 > 1 > 0),
# recorded as the loop goes, never the last module's: `... || echo FAILED` makes a loop
# report the status of its last echo, which is the trap the local sweep documents.
check_all() {
  local mods worst=0 rc m n=0
  mods="$(workspace_modules)" || { echo "check-go-mod-tidy: could not read go.work; refusing" >&2; return 2; }
  while IFS= read -r m; do
    [ -n "$m" ] || continue
    n=$((n + 1))
    rc=0
    check_module "$m" "$mods" || rc=$?
    if [ "$rc" -gt "$worst" ]; then worst=$rc; fi
  done <<<"$mods"
  if [ "$n" -eq 0 ]; then
    echo "check-go-mod-tidy: go.work lists no modules; refusing" >&2
    return 2
  fi
  return "$worst"
}

self_test() (
  # A subshell with an EXIT trap, so an early abort cleans up as surely as a pass.
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  repo="$tmp/repo"
  fails=0
  mkdir -p "$repo/hack" "$repo/x" "$repo/core" "$repo/svc" "$repo/other"
  # The script under test and its module reader, run the way CI runs them: by path, as
  # their own process. Calling the functions in-process would test neither the root
  # resolution nor the argument handling nor the behaviour under `set -e`.
  cp "${BASH_SOURCE[0]}" "$repo/hack/check-go-mod-tidy.sh"
  cp "$ROOT/hack/list-workspace-modules.sh" "$repo/hack/list-workspace-modules.sh"
  printf 'go 1.23\n\nuse (\n\t./x\n\t./core\n\t./svc\n\t./other\n)\n' >"$repo/go.work"

  # 🔴 EVERY FIXTURE go.mod IS WRITTEN IN TIDY'S CANONICAL FORM — direct and indirect
  # requirements in separate blocks — or the fixture is itself drift and the negative
  # control below fails. The `go` line is deliberately low: a line above the running
  # toolchain forces a toolchain switch that GOPROXY=off cannot download, and every
  # case would then refuse for a reason that has nothing to do with tidiness.
  # printf rather than heredocs, so no editor or linter can rewrite the tabs.
  write_fixtures() {
    rm -f "$repo"/*/go.sum
    printf 'module example.com/x\n\ngo 1.23\n' >"$repo/x/go.mod"
    printf 'package x\n\nconst V = 1\n' >"$repo/x/x.go"
    printf 'module example.com/core\n\ngo 1.23\n\nrequire example.com/x v1.2.0\n\nreplace example.com/x => ../x\n' >"$repo/core/go.mod"
    printf 'package core\n\nimport "example.com/x"\n\nconst V = x.V\n' >"$repo/core/c.go"
    printf 'module example.com/svc\n\ngo 1.23\n\nrequire example.com/core v0.0.0\n\nrequire example.com/x v1.2.0 // indirect\n\nreplace (\n\texample.com/core => ../core\n\texample.com/x => ../x\n)\n' >"$repo/svc/go.mod"
    printf 'package svc\n\nimport "example.com/core"\n\nconst V = core.V\n' >"$repo/svc/s.go"
    printf 'module example.com/other\n\ngo 1.23\n' >"$repo/other/go.mod"
    printf 'package other\n' >"$repo/other/o.go"
  }
  write_fixtures

  expect() {
    local want="$1" name="$2" got=0
    shift 2
    LAST_OUT="$(cd "$repo" && GOPROXY=off GOFLAGS='' GOTOOLCHAIN=local hack/check-go-mod-tidy.sh "$@" 2>&1)" || got=$?
    if [ "$got" -ne "$want" ]; then
      echo "self-test FAILED: $name — exit $got, want $want" >&2
      printf '%s\n' "$LAST_OUT" >&2
      fails=$((fails + 1))
    fi
  }
  mentions() {
    case "$LAST_OUT" in *"$1"*) return 0 ;; esac
    return 1
  }
  bad() { echo "self-test FAILED: $1" >&2; fails=$((fails + 1)); }
  # snapshot DIR / unchanged DIR NAME — a drift case must leave the files byte-identical,
  # which is what separates `tidy -diff` from a tidy that repairs CI's copy and passes.
  snapshot() {
    rm -rf "$tmp/snap"
    mkdir -p "$tmp/snap"
    cp "$repo/$1/go.mod" "$tmp/snap/go.mod"
    if [ -f "$repo/$1/go.sum" ]; then cp "$repo/$1/go.sum" "$tmp/snap/go.sum"; fi
  }
  unchanged() {
    cmp -s "$tmp/snap/go.mod" "$repo/$1/go.mod" || bad "$2: go.mod was rewritten by the check"
    if [ -f "$tmp/snap/go.sum" ]; then
      cmp -s "$tmp/snap/go.sum" "$repo/$1/go.sum" || bad "$2: go.sum was rewritten by the check"
    fi
  }
  gobuild() { (cd "$repo/$1" && GOWORK=off GOPROXY=off GOFLAGS='' GOTOOLCHAIN=local go build ./... 2>&1); }

  # 1. Negative control: the tree as built is tidy, per module and in aggregate — and
  # the module really builds on its own, so "tidy" is about a working module.
  expect 0 "a tidy module passes" --module svc
  mentions "tidy: svc" || bad "a tidy module is not reported as tidy by name"
  expect 0 "a tidy workspace passes with no arguments"
  for m in x core svc other; do mentions "tidy: $m" || bad "the aggregate did not check $m"; done
  gobuild svc >/dev/null || bad "premise: the tidy svc fixture does not build with GOWORK=off"

  # 2. A direct import missing from go.mod.
  printf 'package other\n\nimport "example.com/x"\n\nconst V = x.V\n' >"$repo/other/o.go"
  printf 'module example.com/other\n\ngo 1.23\n\nreplace example.com/x => ../x\n' >"$repo/other/go.mod"
  snapshot other
  expect 1 "a direct import missing from go.mod is drift" --module other
  mentions "+require example.com/x" || bad "the missing requirement is not in the printed diff"
  mentions "(cd other && GOWORK=off go mod tidy)" || bad "the drift does not print the command that fixes it"
  unchanged other "missing requirement"
  write_fixtures

  # 3. An unused requirement.
  printf 'module example.com/other\n\ngo 1.23\n\nrequire example.com/x v1.2.0\n\nreplace example.com/x => ../x\n' >"$repo/other/go.mod"
  snapshot other
  expect 1 "an unused requirement is drift" --module other
  mentions "-require example.com/x v1.2.0" || bad "the unused requirement is not in the printed diff"
  unchanged other "unused requirement"

  # 9. The aggregate names the drifted module and only it, and exits 1.
  expect 1 "the aggregate fails on one drifted module"
  mentions "other: go.mod/go.sum are not tidy" || bad "the aggregate does not name the drifted module"
  mentions "svc: go.mod/go.sum are not tidy" && bad "the aggregate names a tidy module as drifted"
  mentions "tidy: svc" || bad "the aggregate stopped checking after the drifted module"
  write_fixtures

  # 4. THE DEPENDABOT SHAPE: core's requirement was raised, svc was left behind.
  printf 'module example.com/svc\n\ngo 1.23\n\nrequire example.com/core v0.0.0\n\nrequire example.com/x v1.1.0 // indirect\n\nreplace (\n\texample.com/core => ../core\n\texample.com/x => ../x\n)\n' >"$repo/svc/go.mod"
  snapshot svc
  expect 1 "a module left behind by a core bump is drift" --module svc
  mentions "+require example.com/x v1.2.0 // indirect" || bad "the left-behind requirement is not in the printed diff"
  unchanged svc "left behind by core"
  # Premise: this fixture is the real failure, not a stand-in for it.
  out="$(gobuild svc)" && bad "premise: the left-behind svc fixture still builds with GOWORK=off"
  case "$out" in *"updates to go.mod needed"*) ;; *) bad "premise: the left-behind svc fixture fails for another reason: $out" ;; esac
  write_fixtures

  # 5. A direct import marked `// indirect`.
  printf 'module example.com/svc\n\ngo 1.23\n\nrequire (\n\texample.com/core v0.0.0 // indirect\n\texample.com/x v1.2.0 // indirect\n)\n\nreplace (\n\texample.com/core => ../core\n\texample.com/x => ../x\n)\n' >"$repo/svc/go.mod"
  snapshot svc
  expect 1 "a direct import marked indirect is drift" --module svc
  mentions "+require example.com/core v0.0.0" || bad "the wrongly-indirect requirement is not in the printed diff"
  unchanged svc "direct marked indirect"
  write_fixtures

  # 6. A stale go.sum line, for a module outside the graph.
  printf 'example.com/zzz v1.0.0/go.mod h1:%s=\n' "$(printf 'A%.0s' $(seq 43))" >"$repo/svc/go.sum"
  snapshot svc
  expect 1 "a stale go.sum line is drift" --module svc
  mentions "diff current/go.sum" || bad "the go.sum drift does not show a go.sum diff"
  mentions "-example.com/zzz" || bad "the stale go.sum line is not in the printed diff"
  unchanged svc "stale go.sum"
  write_fixtures

  # 7. 🔴 An error is a refusal, not drift: tidy exits 1 here too, with no diff.
  printf 'package other\n\nimport "example.invalid/nope"\n\nconst V = nope.V\n' >"$repo/other/o.go"
  expect 2 "a module that cannot be resolved is refused, not reported as drift" --module other
  mentions "refusing" || bad "the refusal does not say it refused"
  mentions "cannot find module providing package" || bad "the refusal does not show go's reason"

  # 9. The aggregate exit is the WORST verdict: a refusal in one module outranks drift
  # in a module checked AFTER it (so neither "the last verdict" nor "the last failure"
  # passes), and the drift is still reported.
  printf 'package svc\n\nimport "example.invalid/nope"\n\nconst V = nope.V\n' >"$repo/svc/s.go"
  printf 'module example.com/other\n\ngo 1.23\n\nrequire example.com/x v1.2.0\n\nreplace example.com/x => ../x\n' >"$repo/other/go.mod"
  printf 'package other\n' >"$repo/other/o.go"
  expect 2 "the aggregate reports a refusal over later drift"
  mentions "other: go.mod/go.sum are not tidy" || bad "the aggregate did not report the drift after the refusal"
  write_fixtures

  # 10. Drift in the FIRST module go.work lists, the rest clean: a loop that kept only
  # the last verdict would pass this.
  printf 'module example.com/x\n\ngo 1.23\n\nrequire example.com/core v0.0.0\n\nreplace example.com/core => ../core\n' >"$repo/x/go.mod"
  expect 1 "drift in the first module fails the aggregate"
  mentions "x: go.mod/go.sum are not tidy" || bad "the aggregate does not name the first module"
  write_fixtures

  # 8. Not a workspace module.
  expect 2 "a path that does not exist is refused" --module nosuch
  expect 2 "a path that only resolves to one is refused" --module x/..
  mkdir -p "$repo/stray"
  printf 'module example.com/stray\n\ngo 1.23\n' >"$repo/stray/go.mod"
  expect 2 "a module go.work does not list is refused" --module stray
  mentions "not a module listed in go.work" || bad "an unlisted module is not refused for being unlisted"
  rm -rf "$repo/stray"
  mv "$repo/svc/go.mod" "$tmp/svc.go.mod"
  expect 2 "a listed module without a go.mod is refused" --module svc
  mv "$tmp/svc.go.mod" "$repo/svc/go.mod"

  # 11. An empty workspace is a refusal, not a vacuous pass.
  cp "$repo/go.work" "$tmp/go.work"
  printf 'go 1.23\n\nuse ()\n' >"$repo/go.work"
  expect 2 "a go.work listing no modules is refused"
  cp "$tmp/go.work" "$repo/go.work"

  # 12. Argument handling.
  expect 2 "an unknown argument is refused" --bogus
  expect 2 "--module without a directory is refused" --module
  expect 0 "premise: the fixtures are restored" --module svc

  if [ "$fails" -ne 0 ]; then
    echo "check-go-mod-tidy self-test: $fails failure(s)" >&2
    exit 1
  fi
  echo "check-go-mod-tidy self-test passed: the script, run by path, passes a tidy module that builds on its own and a tidy workspace; fails, with the diff and the command that fixes it and without rewriting either file, a direct import missing from go.mod, an unused requirement, a module left behind by a core bump (which really does break a GOWORK=off build), a direct import marked indirect and a stale go.sum line; refuses a module that cannot be resolved rather than calling it drift; reports the worst verdict across the workspace, wherever the drifted module sits; and refuses unlisted modules, a missing go.mod, an empty workspace and bad arguments."
)

case "${1:-}" in
  --self-test)
    self_test
    ;;
  --module)
    [ $# -eq 2 ] || { echo "usage: $0 --module DIR" >&2; exit 2; }
    go version >&2 || true
    rc=0
    check_module "$2" || rc=$?
    exit "$rc"
    ;;
  "")
    go version >&2 || true
    rc=0
    check_all || rc=$?
    exit "$rc"
    ;;
  *)
    echo "usage: $0 [--module DIR | --self-test]" >&2
    exit 2
    ;;
esac
