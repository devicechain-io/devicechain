#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Source rules for a Unity package whose .cs files are ALSO compiled by a dotnet
# sidecar (e.g. demos/shared/io.devicechain.sim-traffic), that neither compiler
# enforces on its own:
#
#   1. No conditional compilation (#if / #elif) in Runtime/, Testing/ or Tests/.
#      One set of files must mean one behaviour on every runtime; a directive
#      makes the dotnet build test a different program from the one Unity runs.
#   2. No async tests in Tests/: no `async Task` / `async void` method and no
#      method returning Task. They are outside the NUnit subset the package's
#      tests keep to so that both runners execute them alike, and the old-NUnit
#      compile cannot catch one, since a Task-returning method compiles there too.
#
#   hack/check-unity-shared-sources.sh <package-dir>
#   hack/check-unity-shared-sources.sh --self-test     # prove each rule can fail
#
# grep's exit status is checked EXACTLY: 1 is "no match", 0 is a match, and 2 is
# a bad path or a read error. 2 must fail too, or a renamed folder would turn
# this into a check of nothing that reports clean.

set -euo pipefail

# Matches an #if or #elif directive, indented or not, with or without space after '#'.
COND_RE='^[[:space:]]*#[[:space:]]*(el)?if\b'
# Matches an async method, or a method declared to return Task / Task<T>. A comment
# that merely mentions "async tests" does not match.
ASYNC_RE='\basync[[:space:]]+(Task|void)\b|\bTask(<[^>]*>)?[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\('

usage() {
  echo "usage: $0 <package-dir> | --self-test" >&2
  exit 2
}

# scan <description> <regex> <path>...: 0 when clean, 1 when a match or an error
scan() {
  local what="$1" re="$2" rc
  shift 2
  set +e
  grep -rnE --include='*.cs' "$re" "$@"
  rc=$?
  set -e
  case "$rc" in
    1) return 0 ;;
    0) echo "::error::$what (matches above)" >&2; return 1 ;;
    *) echo "::error::grep rc=$rc while checking for $what in $*: bad path or read error, nothing was checked" >&2; return 1 ;;
  esac
}

check() {
  local pkg="${1%/}" bad=0
  scan "conditional compilation in the shared sources" "$COND_RE" \
    "$pkg/Runtime" "$pkg/Testing" "$pkg/Tests" || bad=1
  scan "an async test (async method, or a method returning Task)" "$ASYNC_RE" \
    "$pkg/Tests" || bad=1
  [ "$bad" -eq 0 ] || return 1
  echo "no #if/#elif in $pkg/{Runtime,Testing,Tests}, and no async test in $pkg/Tests"
}

self_test() {
  local tmp failures=0
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  fixture() { # fixture <name>: a well-formed package
    local p="$tmp/$1"
    mkdir -p "$p/Runtime" "$p/Testing" "$p/Tests/Runtime"
    printf 'namespace N { public struct V { public double X; } }\n' >"$p/Runtime/V.cs"
    printf '// Test kit: no NUnit.\nnamespace N { static class Kit { } }\n' >"$p/Testing/Kit.cs"
    printf '%s\n' \
      '// No async tests, no [Timeout]. #if is not allowed here either.' \
      'namespace N { public class VTests {' \
      '  [Test] public void Works() { var tasks = 0; Assert.That(tasks, Is.EqualTo(0)); }' \
      '} }' >"$p/Tests/Runtime/VTests.cs"
    echo "$p"
  }

  expect() { # expect pass|fail <description> <package>
    local want="$1" what="$2" p="$3" got
    if check "$p" >/dev/null 2>&1; then got=pass; else got=fail; fi
    if [ "$got" = "$want" ]; then
      echo "  ok: $what ($got)"
    else
      echo "FAIL: $what: expected $want, got $got" >&2
      failures=$((failures + 1))
    fi
  }

  local p
  p="$(fixture clean)"
  expect pass "well-formed sources (comments may NAME the rules)" "$p"

  p="$(fixture if-runtime)"
  printf '#if UNITY_EDITOR\n#endif\n' >>"$p/Runtime/V.cs"
  expect fail "#if in Runtime/" "$p"

  p="$(fixture elif-indented-testing)"
  printf '  #  elif X\n' >>"$p/Testing/Kit.cs"
  expect fail "an indented, spaced #elif in Testing/" "$p"

  p="$(fixture if-tests)"
  printf '\t#if NET8_0\n' >>"$p/Tests/Runtime/VTests.cs"
  expect fail "#if in Tests/" "$p"

  p="$(fixture async-task)"
  printf 'class A { [Test] public async Task Probe() { await Task.Yield(); } }\n' >>"$p/Tests/Runtime/VTests.cs"
  expect fail "an async Task test" "$p"

  p="$(fixture async-void)"
  printf 'class A { [Test] public async void Probe() { } }\n' >>"$p/Tests/Runtime/VTests.cs"
  expect fail "an async void test" "$p"

  p="$(fixture task-returning)"
  printf 'class A { [Test] public Task<int> Probe() => Task.FromResult(1); }\n' >>"$p/Tests/Runtime/VTests.cs"
  expect fail "a test returning Task<T> without async" "$p"

  p="$(fixture missing-folder)"
  rm -r "$p/Testing"
  expect fail "a missing source folder (grep rc=2)" "$p"

  if [ "$failures" -ne 0 ]; then
    echo "self-test FAILED ($failures case(s))" >&2
    return 1
  fi
  echo "self-test passed"
}

[ "$#" -eq 1 ] || usage
case "$1" in
  --self-test) self_test ;;
  -*) usage ;;
  *) check "$1" ;;
esac
