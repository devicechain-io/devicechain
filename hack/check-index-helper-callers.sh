#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses any non-test caller of core/rdb's partial-unique-index helpers —
# CreatePartialUniqueIndex, CreateTenantTokenIndex, CreateTenantExternalIdIndex — beyond
# the single file allowed to call one.
#
# 🔴 WHY THIS IS A GATE. An index NAME and its `WHERE` predicate are SCHEMA. A migration
# that sources either from another module is silently rewritten whenever that module
# changes: fresh installs start building a different index while every existing database
# keeps the shape it was given, from a diff that touched neither the migration nor the
# service nor even their module — and both report a clean migration and hold different
# schemas. That is precisely the divergence the "a migration declares its OWN structs"
# rule exists to prevent.
#
# Two of the three helpers are documented in their own source as TEST FIXTURES with no
# non-test callers; the third has exactly one, core's own secrets migration, which passes
# a locally-declared snapshot struct and a literal index name and whose emitted statement
# is pinned by frozen-SQL tests on both sides. Until now that whole arrangement was
# comment-only: nothing stopped a service migration calling a helper with a LIVE model.
#
# 🔴 THE RULE IS "NO NON-TEST CALLER", NOT "NO CALLER FROM A MIGRATION". Deciding whether
# a file is a migration is a classification, and the classification then becomes the thing
# to evade — rename the file, lift the gormigrate step into a helper beside it, assemble
# the chain from a slice built elsewhere. There is nothing legitimate a non-test caller
# does anyway, so the guard asks the question with no gradient in it.
#
# 🔴 WHY IT PARSES RATHER THAN GREPS, and this one is not a matter of taste. The forbidden
# names are already written down in this tree, in prose: four service migrations carry a
# comment saying "THIS IS A DELIBERATE COPY OF rdb.CreateTenantTokenIndex, NOT AN
# OVERSIGHT", and core/rdb/model.go cites two of the helpers to explain what they are for.
# A text scan reports all of those on its first run and is then narrowed with exclusions
# until it is wrong quietly. An AST walk never sees a comment. The usual reasons apply too:
# an AST has no line breaks, and the match is on the SYMBOL's own name rather than on the
# local name bound to core/rdb, so an import alias and a dot-import are the same match.
#
# ⚠️ WHAT IT CANNOT SEE, stated here rather than left for a reviewer to find — a narrow
# true claim beats a broad false one:
#
#   - A migration that RE-IMPLEMENTS the statement instead of calling a helper. That is not
#     an evasion, it is the sanctioned pattern; six areas do it today. What keeps those
#     copies honest is the migration-diff golden schema, a different instrument.
#   - A wrapper defined INSIDE the one allow-listed core/rdb file. It could export a new
#     name that a service migration then calls while naming nothing watched. The allow-list
#     is scoped to a FILE rather than a package for this reason — a wrapper in any other
#     file is reported where it is DEFINED — which keeps the exempt surface to two files a
#     reviewer can actually read.
#   - The symbol reached without ever being named in source: reflection over a string built
#     at run time, or //go:linkname. Recorded as impractical rather than unnoticed —
#     reflection cannot obtain a package-level func value here at all, and a //go:linkname
#     to a non-runtime symbol is refused by the linker in a real build.
#   - _test.go files are SKIPPED. The helpers exist for them.
#   - testdata directories, skipped as go build skips them: a Go tool that parses Go is
#     entitled to deliberately invalid fixtures, and a parse error is fatal here.
#   - Anything under `.claude/`, where this repo's git worktrees live. Without it a
#     maintainer with a worktree scans a second, older copy of the whole tree and gets
#     findings at paths they are not editing, while CI (which has no worktrees) stays green.
#   - Anything outside the roots below. They are two broad directories, not an enumeration
#     of modules, so a service added tomorrow is covered the day it lands.
#
# Recorded as NOT holes, so nobody re-tests them: an import alias, a dot-import, a call
# split across lines, a method value never called, an indirect call through a variable, a
# build-tagged file, a generated file, and a //nolint comment. Each is a self-test case
# below. A build-tagged or generated file is parsed like any other because this reads
# SYNTAX and not a build configuration, so it over-reports rather than under-reports.
#
#   hack/check-index-helper-callers.sh
#   hack/check-index-helper-callers.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The roots, each with the minimum number of non-test Go files it must yield.
#
# 🔴 THE MINIMUM IS PER ROOT, AND A SINGLE TOTAL WOULD NOT DO. A scan that read nothing
# must fail LOUDLY rather than report clean — a wrong path, a renamed directory or a
# filter that stopped matching all land there. Against ~900 files a single floor of 500 is
# cleared by backend/core alone, so every service directory could vanish and the total
# would still pass. Each is a floor with room under it, not a tracking count.
ROOTS=(backend=400 deploy=1)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/rdbguard"

# Built from source every run. A checked-in binary is a guard that stops tracking the rule
# it enforces the moment somebody edits the analyzer.
go build -o "$BIN" ./backend/tools/rdbguard/cmd/rdbguard

# ---------------------------------------------------------------------------
# self-test: prove the check can fail, once per evasion shape.
# ---------------------------------------------------------------------------
# 🔴 ONE FIXTURE PER SHAPE, NOT ONE FIXTURE WITH EVERYTHING IN IT. A combined fixture is
# satisfied by a checker that detects any single shape, which is how a guard ends up
# enforcing a fraction of what it claims while every run is green.
#
# The two PASSING fixtures matter as much as the failing ones. A guard that also flags the
# sanctioned pattern — a migration writing its own CREATE INDEX — or that flags the
# COMMENTS four migrations carry about these helpers is a guard people learn to work
# around, and the comment case is the exact thing the grep version got wrong.
self_test() {
  local fx="$TMP/fx" rc=0

  plant() { # plant <name> <body-with-package-clause>
    mkdir -p "$fx/$1"
    printf '%s\n' "$2" >"$fx/$1/x.go"
  }

  # 1. the plain shape: a service migration calling the fixture with a LIVE model
  plant direct 'package schema

import "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error { return rdb.CreateTenantTokenIndex(tx, &Device{}) }'

  # 2. an import alias, which defeats anything hardcoding the name "rdb"
  plant importalias 'package schema

import r "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error { return r.CreateTenantExternalIdIndex(tx, &Device{}) }'

  # 3. a dot-import: no package qualifier survives at the call site at all
  plant dotimport 'package schema

import . "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error { return CreateTenantTokenIndex(tx, &Device{}) }'

  # 4. a method value. NOTHING is called here; the call happens through install, later,
  #    possibly in another file, where nothing watched is named.
  plant methodvalue 'package schema

import "github.com/devicechain-io/dc-microservice/rdb"

var install = rdb.CreateTenantExternalIdIndex'

  # 5. the indirect call, which is what shape 4 buys you
  plant indirect 'package schema

import "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error {
	fn := rdb.CreateTenantTokenIndex
	return fn(tx, &Device{})
}'

  # 6. split across lines — gofmt keeps this, and a grep for `rdb.CreateTenant` misses it
  plant linesplit 'package schema

import "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error {
	return rdb.
		CreateTenantTokenIndex(tx, &Device{})
}'

  # 7. a same-named wrapper in ANOTHER package. Callers of it name nothing watched, so the
  #    only place left to catch it is its DEFINITION — which is why the allow-list is
  #    scoped to a file rather than to core/rdb as a package.
  plant wrapper 'package rdbx

func CreateTenantTokenIndex(tx anyDB, m any) error { return nil }'

  # 8. a build-tagged file. Excluded from a build is not excluded from a schema.
  mkdir -p "$fx/buildtag"
  printf '//go:build tools\n\npackage schema\n\nimport "github.com/devicechain-io/dc-microservice/rdb"\n\nfunc migrate(tx anyDB) error { return rdb.CreateTenantTokenIndex(tx, &Device{}) }\n' \
    >"$fx/buildtag/x.go"

  # 9. a generated file. "DO NOT EDIT" is not "do not check".
  plant generated '// Code generated by gen. DO NOT EDIT.

package schema

import "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error { return rdb.CreatePartialUniqueIndex(tx, &Device{}, "uix_x", "tenant_id") }'

  # 10. a //nolint-shaped escape, which this guard does not honour
  plant nolint 'package schema

import "github.com/devicechain-io/dc-microservice/rdb"

//nolint:all // deliberate
func migrate(tx anyDB) error { return rdb.CreateTenantTokenIndex(tx, &Device{}) }'

  # 11. the third helper, called directly with a live model and a literal name — a shape
  #     that LOOKS correct because the name is literal, and is not, because the predicate
  #     and the table still come from another module.
  plant partialunique 'package schema

import "github.com/devicechain-io/dc-microservice/rdb"

func migrate(tx anyDB) error {
	return rdb.CreatePartialUniqueIndex(tx, &Device{}, "uix_devices_tenant_token", "tenant_id", "token")
}'

  for shape in direct importalias dotimport methodvalue indirect linesplit wrapper \
               buildtag generated nolint partialunique; do
    local out status=0
    out="$("$BIN" -check=index-helpers -strict-allowlist=false "$fx/$shape=1" 2>&1)" || status=$?
    if [ "$status" -ne 1 ]; then
      echo "self-test: shape '$shape' exited $status, want 1 — the guard does not catch it" >&2
      echo "$out" >&2
      rc=1
      continue
    fi
    # Naming the file and line is the difference between a gate and an alarm.
    if ! grep -q "$fx/$shape/x.go:[0-9]" <<<"$out"; then
      echo "self-test: shape '$shape' failed without naming a file and line:" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # --- the counterweights: the guard must NOT flag either of these ---

  # A. the sanctioned pattern — a migration writing its own statement against its own
  #    snapshot struct. If this is flagged, the guard is refusing the fix.
  plant clean 'package schema

import "fmt"

type device struct{ Token string }

func migrate(tx anyDB) error {
	return tx.Exec(fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (tenant_id, token) WHERE deleted_at IS NULL",
		"uix_devices_tenant_token", "devices")).Error
}'

  # B. the COMMENT case, which is real code in this tree and is what defeats a grep. Four
  #    migrations explain in prose that their copy of the helper is deliberate.
  plant comments 'package schema

// 🔴 THIS IS A DELIBERATE COPY OF rdb.CreateTenantTokenIndex, NOT AN OVERSIGHT. Calling
// rdb.CreatePartialUniqueIndex here would put this migration under another module.
// See also rdb.CreateTenantExternalIdIndex.
const note = "see rdb.CreateTenantTokenIndex"

func migrate(tx anyDB) error { return nil }'

  local status
  for shape in clean comments; do
    status=0
    out="$("$BIN" -check=index-helpers -strict-allowlist=false "$fx/$shape=1" 2>&1)" || status=$?
    if [ "$status" -ne 0 ]; then
      echo "self-test: the '$shape' fixture exited $status, want 0 — the guard flags something legitimate" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # C. a scan that read nothing must FAIL, not report clean.
  mkdir -p "$fx/empty"
  status=0
  "$BIN" -check=index-helpers -strict-allowlist=false "$fx/empty=1" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an empty tree exited $status, want 2 — a scan that read nothing must not report clean" >&2
    rc=1
  fi

  # D. 🔴 THE LIVENESS CHECK, which is the half a green tick cannot otherwise distinguish
  #    from a broken instrument. Zero findings is what a clean tree reports AND what a
  #    matcher that has gone blind reports. A working scan also lands on every allow-list
  #    entry, because each names source known to be present. Here the clean fixture
  #    contains none of them, so with the check ARMED the run must exit 2 rather than 0.
  status=0
  out="$("$BIN" -check=index-helpers "$fx/clean=1" 2>&1)" || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: a tree with no allow-listed file exited $status, want 2 — a stale allow-list is not a pass" >&2
    echo "$out" >&2
    rc=1
  elif ! grep -q "matched nothing" <<<"$out"; then
    echo "self-test: the stale allow-list failure did not say what was stale:" >&2
    echo "$out" >&2
    rc=1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    exit 1
  fi
  echo "self-test passed: 11 evasion shapes caught, the sanctioned pattern and the prose about it allowed, an empty tree and a stale allow-list refused."
}

case "${1-}" in
  --self-test)
    self_test
    exit 0
    ;;
  "") ;;
  *)
    echo "usage: $0 [--self-test]" >&2
    exit 2
    ;;
esac

"$BIN" -check=index-helpers "${ROOTS[@]}"
