#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses any registration on net/http's package-global http.DefaultServeMux.
#
# 🔴 WHY THIS IS A GATE AT ALL. Every HTTP server in this repository serves an
# EXPLICIT Handler — the mux its Microservice owns — which makes the default mux a
# mux NOTHING SERVES. A registration on it compiles, runs and returns; the route
# simply is not there, with no error and no log line, and the endpoint answers 404
# to whatever asked for it. In this tree that is a chart probe, the ingress, or a
# peer service fetching /auth/jwks, whose loss takes every pod out of its Service
# endpoints while presenting as an authentication fault.
#
# 🔴 WHY IT PARSES RATHER THAN GREPS. A grep was written first and defeated in
# minutes, three ways that all compile and all survive gofmt: an alias through a
# variable (`var reg = http.Handle`), a call split across lines (`http.` then
# `HandleFunc(`), and a helper placed outside whatever subtree the grep walked. The
# analyzer answers the first two by construction — an AST has no line breaks, and
# the selector is evidence wherever the function VALUE is named, called or not — and
# the third by taking its roots from this script rather than guessing.
#
# 🔴 THE PARSING VERSION WAS ATTACKED TOO, AND LOST TWICE. Both were real
# registrations, not theoretical ones: a SECOND import of net/http under another
# name (the import loop watched only the last), and a blank import whose init
# registers — `import _ "net/http/pprof"` mounts /debug/pprof/ and answered 200.
# The second is the one a developer writes by ACCIDENT, reaching for a profiler.
# Both are closed; the analyzer's doc comment carries the full list of what is not.
#
# ⚠️ WHAT IT STILL CANNOT SEE, stated here rather than left for a reviewer to find.
# The list is long deliberately — a narrow true claim beats a broad false one:
#
#   - `Handler: h` where h is a nil-valued variable. Telling that from `Handler: mux`
#     needs type and flow analysis, and guessing from syntax would flag the CORRECT
#     pattern, which two servers here use.
#   - A server never built as a composite literal — `new(http.Server)`,
#     `var s http.Server` — with Handler simply never assigned. Flagging those would
#     refuse the legitimate build-then-assign pattern.
#   - A literal that sets Handler and a later statement that clears it.
#   - A type alias for http.Server, or a struct EMBEDDING it: the literal then names
#     neither http nor Server.
#   - Reflection, and any mux obtained at run time from beyond the roots. Reflection
#     still has to NAME http.Handle or http.DefaultServeMux to get a value, so the
#     common shapes are caught.
#   - _test.go files are SKIPPED. Tests here deliberately register on the default mux
#     to prove a server does not serve it. A production route mounted from a test file
#     would not be found.
#   - testdata directories are skipped, as go build skips them — a Go tool that parses
#     Go is entitled to malformed fixtures, and a parse error is fatal here.
#   - A dot-import of net/http would erase the selector this depends on, so the
#     analyzer REFUSES one rather than reporting a file it cannot check.
#   - Only the roots below are walked. Adding a Go tree outside them silently puts
#     it out of scope — which is why they are two broad directories and not a list
#     of services.
#
# Two shapes are recorded as NOT evasions, so nobody re-tries them: //go:linkname is
# rejected by the linker in a real binary, and reflection must name a watched symbol.
#
#   hack/check-default-servemux.sh
#   hack/check-default-servemux.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The roots, each with the minimum number of Go files it must yield.
#
# Whole trees rather than an enumeration of services: the third evasion the grep
# version fell to was a helper one directory over.
#
# 🔴 THE MINIMUM IS PER ROOT, AND A SINGLE TOTAL WOULD NOT DO. A scan that read
# nothing must fail LOUDLY rather than report clean — a wrong path, a renamed
# directory or a filter that stopped matching all land there. But against ~890 files
# a single floor of 500 is cleared by backend/core alone, so every service directory
# could vanish and the total would still pass. A per-root floor fails the root that
# went quiet.
#
# Each is a floor with room under it, not a tracking count: high enough that a broken
# root cannot slip past, low enough that deleting a service never trips it. backend
# parsed ~890 files and deploy 1 when this was written; deploy is a single-file module
# (the chart embedder), so its floor is simply "it is still there".
ROOTS=(backend=400 deploy=1)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/muxguard"

# Built from source every run. A checked-in or pinned binary is a guard that stops
# tracking the rule it enforces the moment somebody edits the analyzer.
go build -o "$BIN" ./backend/tools/muxguard/cmd/muxguard

# ---------------------------------------------------------------------------
# self-test: prove the check can fail, once per evasion shape.
# ---------------------------------------------------------------------------
# 🔴 SEVERAL DISTINCT SHAPES, NOT ONE. A single planted registration proves only the
# happy path; the shapes below are the ones that have actually defeated a scan of
# this rule, so each gets its own fixture and its own assertion. A clean fixture and
# an empty directory are here too — a guard that flags everything, or one that
# cannot tell an empty read from a clean tree, is not a guard.
self_test() {
  local fixtures="$TMP/fixtures" rc=0

  plant() { # plant <name> <body>
    mkdir -p "$fixtures/$1"
    printf 'package p\n\nimport "net/http"\n\nvar _ = http.StatusOK\n\n%s\n' "$2" \
      >"$fixtures/$1/x.go"
  }

  # 1. the plain shape
  plant direct 'func f() { http.Handle("/x", nil) }'
  # 2. split across lines — gofmt keeps this, and a grep for `http.Handle(` misses it
  plant linesplit 'func f() {
	http.
		HandleFunc("/x", nil)
}'
  # 3. an alias through a variable: the function is never called here at all
  plant alias 'var reg = http.Handle'
  # 4. an import alias, which defeats anything hardcoding the name "http"
  mkdir -p "$fixtures/importalias"
  printf 'package p\n\nimport nh "net/http"\n\nfunc f() { nh.Handle("/x", nil) }\n' \
    >"$fixtures/importalias/x.go"
  # 5. handing the global to a registrar that takes a *http.ServeMux — the indirect
  #    shape that hid two of user-management's ten sites from the first enumeration
  plant indirect 'func reg(mux *http.ServeMux) {}
func f() { reg(http.DefaultServeMux) }'
  # 6. a server literal with no Handler, which serves the default mux by omission
  plant nohandler 'func f() *http.Server { return &http.Server{Addr: ":8080"} }'
  # 7. a dot-import, which the analyzer refuses rather than silently failing to see
  mkdir -p "$fixtures/dotimport"
  printf 'package p\n\nimport . "net/http"\n\nfunc f() { Handle("/x", nil) }\n' \
    >"$fixtures/dotimport/x.go"

  # --- shapes that defeated the FIRST parsing version, each proven to register ---

  # 8. net/http imported TWICE. A loop that kept one local name watched only the
  #    last, and the other name registered freely.
  mkdir -p "$fixtures/twoimports"
  printf 'package p\n\nimport (\n\ta "net/http"\n\tb "net/http"\n)\n\nvar _ = b.StatusOK\n\nfunc f() { a.Handle("/x", nil) }\n' \
    >"$fixtures/twoimports/x.go"

  # 9. a blank import whose init registers. NOTHING is called here; the import IS the
  #    registration. This is the shape a developer writes by ACCIDENT, reaching for a
  #    profiler — and then the profiling endpoint 404s.
  mkdir -p "$fixtures/pprof"
  printf 'package p\n\nimport _ "net/http/pprof"\n' >"$fixtures/pprof/x.go"

  # 10. expvar, same mechanism, different door.
  mkdir -p "$fixtures/expvar"
  printf 'package p\n\nimport _ "expvar"\n' >"$fixtures/expvar/x.go"

  # 11. Handler present but explicitly nil. A check that asked only whether the KEY
  #     appeared accepted this, and it serves the default mux exactly as omission does.
  plant nilhandler 'func f() *http.Server { return &http.Server{Addr: ":8080", Handler: nil} }'

  # 12. the argument family: none of these names Handle, HandleFunc or
  #     DefaultServeMux, and a nil in the last position means the default mux.
  plant listenandserve 'func f() error { return http.ListenAndServe(":8080", nil) }'
  mkdir -p "$fixtures/serve"
  printf 'package p\n\nimport (\n\t"net"\n\t"net/http"\n)\n\nfunc f(l net.Listener) error { return http.Serve(l, nil) }\n' \
    >"$fixtures/serve/x.go"

  # 13. an httptest server with a nil handler serves the default mux, which is how a
  #     test can pass against routes production does not serve.
  mkdir -p "$fixtures/httptestnil"
  printf 'package p\n\nimport "net/http/httptest"\n\nfunc f() { _ = httptest.NewServer(nil) }\n' \
    >"$fixtures/httptestnil/x.go"

  for shape in direct linesplit alias importalias indirect nohandler dotimport \
               twoimports pprof expvar nilhandler listenandserve serve httptestnil; do
    local out status=0
    out="$("$BIN" "$fixtures/$shape=1" 2>&1)" || status=$?
    if [ "$status" -ne 1 ]; then
      echo "self-test: shape '$shape' exited $status, want 1 — the guard does not catch it" >&2
      echo "$out" >&2
      rc=1
      continue
    fi
    # Naming the file and line is the difference between a gate and an alarm.
    if ! grep -q "$fixtures/$shape/x.go:[0-9]" <<<"$out"; then
      echo "self-test: shape '$shape' failed without naming a file and line:" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # The counterweight: the REPLACEMENT shape must pass, or the guard cannot tell the
  # fix from the defect and every correct server reads as a violation.
  mkdir -p "$fixtures/clean"
  printf 'package p\n\nimport "net/http"\n\nfunc f() *http.Server {\n\tmux := http.NewServeMux()\n\tmux.Handle("/x", nil)\n\tmux.HandleFunc("/y", nil)\n\treturn &http.Server{Addr: ":8080", Handler: mux}\n}\n' \
    >"$fixtures/clean/x.go"
  local status=0
  "$BIN" "$fixtures/clean=1" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne 0 ]; then
    echo "self-test: the clean fixture exited $status, want 0 — the guard flags the fix as the defect" >&2
    rc=1
  fi

  # 🔴 And the vacuity check: a scan that finds no files must FAIL, not report clean.
  mkdir -p "$fixtures/empty"
  status=0
  "$BIN" "$fixtures/empty=1" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an empty tree exited $status, want 2 — a scan that read nothing must not report clean" >&2
    rc=1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    exit 1
  fi
  echo "self-test passed: 14 evasion shapes caught, the fix shape allowed, an empty tree refused."
}

case "${1-}" in
  --self-test)
    self_test
    exit 0
    ;;
  "")
    ;;
  *)
    echo "usage: $0 [--self-test]" >&2
    exit 2
    ;;
esac

"$BIN" "${ROOTS[@]}"
