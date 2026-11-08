#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Two red lines, enforced as static checks over the whole tree:
#
#   DECLARATIVE-ONLY. DeviceChain has no tenant code runtime. A script interpreter
#   (goja, otto, starlark, yaegi) or a WebAssembly runtime (wazero, wasmtime) anywhere in
#   a go.mod, go.sum, go.work or Go import is the first step toward running code a tenant
#   wrote, which turns every configuration surface into a code-execution surface. The
#   check reads every token of every module file (a replace target counts as much as a
#   require) and the imports of every Go file, build tags ignored. An exception is an
#   --allow dir=reason entry, admits only the files directly in that directory, must cite
#   an ADR-nnn, and fails the run if it matches nothing. The set below is empty.
#
#   CONSTANT REMEDIATION. The text an error gives an agent about what to do next is
#   written by a person at compile time. Text assembled from a device payload, a pack
#   description or a tenant-chosen name is data the agent would read as instruction. A
#   Remediation (backend/core/agenttext) built from anything but a string literal or a
#   constant fails. See backend/tools/redlines for the rules and what the check cannot see.
#
#   hack/check-redlines.sh
#   hack/check-redlines.sh --self-test   # prove the checks can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Allowed tenant-runtime references: "dir=reason (ADR-nnn)". Empty by decision; adding an
# entry is adding a tenant code runtime, which needs its own recorded decision first.
ALLOW=()

# A floor with room under it (about 4,400 Go files (1,765 outside tests) and 35 modules when this was written),
# so a scan that stopped reading the tree fails loudly instead of reporting clean.
DEPS_ROOT=".=3000"
REM_ROOT=".=1200"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/redlines"
# Built from source every run, so the guard cannot drift from the rule it enforces.
GOWORK=off go build -C backend/tools/redlines -o "$BIN" ./cmd/redlines

self_test() {
  local fx="$TMP/fixtures" rc=0

  write() { # write <relative path> <body>
    mkdir -p "$(dirname "$fx/$1")"
    printf '%s\n' "$2" >"$fx/$1"
  }
  # expect <name> <want exit> <want text> <args...>; scored on content as well as status,
  # so a run that exits 1 for the wrong reason does not read as a catch.
  expect() {
    local name="$1" want="$2" text="$3" out status=0
    shift 3
    out="$(cd "$fx" && "$BIN" "$@" 2>&1)" || status=$?
    if [ "$status" -ne "$want" ]; then
      echo "self-test: '$name' exited $status, want $want" >&2
      echo "$out" >&2
      rc=1
      return
    fi
    if [ -n "$text" ] && ! grep -qF -- "$text" <<<"$out"; then
      echo "self-test: '$name' exited $want but did not report: $text" >&2
      echo "$out" >&2
      rc=1
    fi
  }

  local MOD=$'module example.com/m\n\ngo 1.26'

  # --- declarative-only: each planted reference must exit 1 and name its file and line ---
  write goja-import/go.mod "$MOD"
  write goja-import/x.go 'package p

import _ "github.com/dop251/goja"'
  expect goja-import 1 "x.go:3: github.com/dop251/goja is a tenant code runtime" deps goja-import=1

  write goja-mod/go.mod "$MOD

require github.com/dop251/goja v0.0.0-20240101000000-abcdef123456"
  write goja-mod/x.go 'package p'
  expect goja-mod 1 "go.mod:5: github.com/dop251/goja" deps goja-mod=1

  write goja-replace/go.mod "$MOD

replace github.com/some/lib => github.com/dop251/goja v1.0.0"
  write goja-replace/x.go 'package p'
  expect goja-replace-target 1 "go.mod:5:" deps goja-replace=1

  write goja-sum/go.mod "$MOD"
  write goja-sum/go.sum 'github.com/dop251/goja v1.0.0 h1:abc='
  write goja-sum/x.go 'package p'
  expect goja-sum 1 "go.sum:1:" deps goja-sum=1

  write wasm/go.mod "$MOD

require github.com/tetratelabs/wazero v1.7.0"
  write wasm/x.go 'package p'
  expect wazero 1 "github.com/tetratelabs/wazero" deps wasm=1

  write starlark/go.mod "$MOD"
  write starlark/x.go 'package p

import (
	sl "go.starlark.net/starlark"
)

var _ = sl.None'
  expect starlark-aliased-import 1 "x.go:4:" deps starlark=1

  write yaegi/go.mod "$MOD"
  write yaegi/x.go 'package p

import _ "github.com/traefik/yaegi/interp"'
  expect yaegi 1 "github.com/traefik/yaegi" deps yaegi=1

  write otto/go.mod "$MOD

require github.com/robertkrimen/otto v0.2.1"
  write otto/x.go 'package p'
  expect otto 1 "github.com/robertkrimen/otto" deps otto=1

  write wasmtime/go.mod "$MOD

require github.com/bytecodealliance/wasmtime-go/v14 v14.0.0"
  write wasmtime/x.go 'package p'
  expect wasmtime 1 "wasmtime-go" deps wasmtime=1

  write gowork/go.mod "$MOD"
  write gowork/go.work 'go 1.26

replace github.com/dop251/goja => ./fork'
  write gowork/x.go 'package p'
  expect go.work 1 "go.work:3:" deps gowork=1

  # An allowance admits ONE directory, needs an ADR citation, and is stale if unused.
  write allowed/go.mod "$MOD"
  write allowed/sandbox/x.go 'package s

import _ "github.com/tetratelabs/wazero"'
  expect allowed 0 "allowed allowed/sandbox (1 reference(s))" deps -allow "allowed/sandbox=typed decode ABI (ADR-901)" allowed=1
  write allowed/sandbox/sub/y.go 'package sub

import _ "github.com/tetratelabs/wazero"'
  expect allowance-is-not-recursive 1 "sandbox/sub/y.go:3:" deps -allow "allowed/sandbox=typed decode ABI (ADR-901)" allowed=1
  expect stale-allowance 1 "allowance nowhere matched no reference" deps -allow "nowhere=typed decode ABI (ADR-901)" allowed=1

  # --- negative controls: each must exit 0 ---
  write clean/go.mod "$MOD

require github.com/google/cel-go v0.20.0"
  write clean/x.go 'package p

import _ "github.com/google/cel-go/cel"'
  expect clean 0 "0 finding(s)" deps clean=1

  # --- the instrument: inputs it cannot use must exit 2, not report clean ---
  mkdir -p "$fx/empty"
  expect empty-tree 2 "the root is not being read" deps empty=1
  write short/go.mod "$MOD"
  write short/x.go 'package p'
  expect below-floor 2 "the root is not being read" deps short=5
  expect allowance-without-adr 2 "cites no decision" deps -allow "short/sandbox=because we want it" short=1
  expect duplicate-allowance 2 "duplicate allowance" deps -allow "a=x ADR-1" -allow "a=y ADR-2" short=1
  write broken/go.mod "$MOD"
  write broken/x.go 'package p
import ('
  expect unparseable-go 2 "cannot read imports" deps broken=1

  # --- constant remediation ---
  local REMHDR='package p

import (
	"fmt"

	"example.com/m/agenttext"
)

var _ = fmt.Sprint
'
  write rem-sprintf/go.mod "$MOD"
  write rem-sprintf/agenttext/a.go 'package agenttext

type Remediation string'
  write rem-sprintf/x.go "${REMHDR}
func f(id string) agenttext.Remediation {
	return agenttext.Remediation(fmt.Sprintf(\"check %s\", id))
}"
  expect remediation-sprintf 1 "x.go:12: argument of Remediation is not a compile-time constant: a call to Sprintf" remediation rem-sprintf=1

  write rem-var/go.mod "$MOD"
  write rem-var/agenttext/a.go 'package agenttext

type Remediation string'
  write rem-var/x.go 'package p

import "example.com/m/agenttext"

type Err struct{ Remediation string }

func f(e *Err, device string) {
	e.Remediation = "check " + device
}'
  expect remediation-concat 1 "x.go:8: assignment to .Remediation is not a compile-time constant" remediation rem-var=1

  write rem-field/go.mod "$MOD"
  write rem-field/agenttext/a.go 'package agenttext

type Remediation string'
  write rem-field/x.go 'package p

import "example.com/m/agenttext"

type Err struct{ Remediation agenttext.Remediation }

func f(msg string) Err {
	return Err{Remediation: agenttext.Remediation(msg)}
}'
  expect remediation-field 1 "x.go:8:" remediation rem-field=1

  write rem-ok/go.mod "$MOD"
  write rem-ok/agenttext/a.go 'package agenttext

type Remediation string'
  write rem-ok/x.go 'package p

import "example.com/m/agenttext"

const retry = "Retry after the freeze lifts."

type Err struct{ Remediation agenttext.Remediation }

var E = Err{Remediation: agenttext.Remediation(retry + " Then re-run.")}'
  expect remediation-clean 0 "0 finding(s)" remediation rem-ok=1
  expect remediation-empty 2 "the root is not being read" remediation empty=1

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    return 1
  fi
  echo "self-test: all planted references and shapes behaved as required"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi

args=()
for a in "${ALLOW[@]+"${ALLOW[@]}"}"; do args+=(-allow "$a"); done
status=0
"$BIN" deps "${args[@]+"${args[@]}"}" "$DEPS_ROOT" || status=1
"$BIN" remediation "$REM_ROOT" || status=1
exit "$status"
