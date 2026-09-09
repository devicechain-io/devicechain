#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses any non-test Go source that names its table as a string — `db.Table("widgets")`
# — because such a statement is not tenant-scoped.
#
# 🔴 WHY THIS IS A GATE RATHER THAN AN OBSERVATION. The tenant-scope callback classifies a
# statement from its parsed schema, and naming a table as a string produces one of two
# outcomes, neither of them a scoped read:
#
#   - No parseable destination at all. The statement names a table and carries no schema,
#     which the callback refuses outright. That refusal is the safety net, not the rule:
#     a refusal is an outage, and it is discovered in production.
#   - A destination that DOES parse, into a shape with no TenantId field — a projection
#     struct, or a `Model(&gadget{}).Table("widgets")` mismatch. That is not
#     unclassifiable; it is classified as NOT tenant-scoped. No predicate is injected,
#     nothing errors, and the read returns every tenant's rows.
#
# The second is why this is worth a gate. It has no failure mode at the call site at all:
# the error is nil and the rows look right. Both shapes have to start with the same call,
# so refusing the call covers them together, and does it before either can be written.
#
# It also replaces a snapshot with a check. The refusal above shipped with a safety
# argument that rested on an enumeration — `.Table(` had zero non-test occurrences, so
# nothing legitimate was being refused. Nothing kept that true, and the natural way to
# spell a bulk update is the way that reaches it.
#
# 🔴 WHY IT PARSES RATHER THAN GREPS. `core/rdb/tenant_scope.go` quotes gorm's own error
# text in a comment, and that text contains `db.Table(\"users\")`. A grep reports it, and
# is then narrowed with an exclusion until it is wrong quietly. An AST walk never sees a
# comment. The match is also on the METHOD rather than on a receiver spelling, so `db.`,
# `tx.`, a chained builder and a call split across lines are all one case.
#
# ⚠️ WHAT IT CANNOT SEE, stated rather than left to be discovered:
#
#   - A method VALUE: `f := db.Table`, called later through f. Closing it would mean
#     flagging every non-call `.Table` selector, and `stmt.Table` — gorm's own string
#     field — is READ in around seventy places across the migrations and core/rdb. An
#     allow-list that size is not read by anyone and goes stale on every new migration, at
#     which point the guard enforces whatever nobody got around to exempting. The narrow
#     true claim is preferred.
#   - Raw SQL. `db.Raw(...)` and `db.Exec(...)` name no table to the callback and never had
#     a schema; they are documented as out of reach where the callback is registered, and
#     they are a different rule.
#   - A helper OUTSIDE the roots that takes a table name and makes the call. The roots are
#     two broad directories for exactly that reason.
#   - A gorm handle opened with no callbacks registered at all, which is unscoped whatever
#     it calls. That is a property of the handle, not of the statement, and not a syntactic
#     question.
#   - _test.go files are SKIPPED, and they must be: core/rdb's own suite names tables on
#     purpose to prove the refusal fires, and tenantpurge counts fence rows that way.
#   - testdata directories, `.claude/` (where this repo's git worktrees live — without the
#     exclusion a maintainer scans a second, older copy of the tree and CI stays green),
#     `_legacy/`, vendor and node_modules.
#
# Recorded as NOT holes, so nobody re-tests them: the receiver's name, a chained builder,
# a call split across lines, a table name held in a variable, a build-tagged file, a
# generated file, and a //nolint comment. Each is a self-test case below.
#
# 🔴 THE ALLOW-LIST IS EMPTY TODAY, and that is a measurement, not an aspiration: no
# non-test file in this repository calls .Table. The mechanism exists in the analyzer
# because the legitimate users this will eventually need are real — code that opens a
# handle with no callbacks registered, or that runs under a deliberate system context —
# and the alternative to an allow-list is a pattern that tries to recognise them, which is
# a pattern anything else can be written to look like. An entry added there steps around
# the tenant predicate and the erasure fence, so it needs the reasoning written beside it.
#
#   hack/check-bare-table-statements.sh
#   hack/check-bare-table-statements.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The roots, each with the minimum number of non-test Go files it must yield. Per root
# rather than a single total: against ~900 files one floor of 500 is cleared by
# backend/core alone, so every service directory could vanish and the total still pass.
ROOTS=(backend=400 deploy=1)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/rdbguard"

# Built from source every run, so the guard cannot drift from the analyzer it runs.
go build -o "$BIN" ./backend/tools/rdbguard/cmd/rdbguard

# ---------------------------------------------------------------------------
# self-test: prove the check can fail, once per shape.
# ---------------------------------------------------------------------------
# 🔴 ONE FIXTURE PER SHAPE. A combined fixture is satisfied by a checker that detects any
# single one of them, which is how a guard ends up enforcing a fraction of what it claims.
self_test() {
  local fx="$TMP/fx" rc=0

  plant() { # plant <name> <body-with-package-clause>
    mkdir -p "$fx/$1"
    printf '%s\n' "$2" >"$fx/$1/x.go"
  }

  # 1. the plain shape
  plant direct 'package model

func rows(db *gorm.DB) error { return db.Table("widgets").Find(&out).Error }'

  # 2. the PROJECTION shape — the one with no failure mode at the call site. The
  #    destination parses, has no TenantId, classifies as not tenant-scoped, and returns
  #    both tenants` rows with a nil error.
  plant projection 'package model

func rows(db *gorm.DB, ctx context.Context) error {
	var out []struct{ Name string }
	return db.WithContext(ctx).Table("widgets").Find(&out).Error
}'

  # 3. a Model()/Table() mismatch: gorm takes the table from Table, the schema from Model
  plant modelmismatch 'package model

func rows(db *gorm.DB) error { return db.Model(&gadget{}).Table("widgets").Find(&out).Error }'

  # 4. a chained builder, where the receiver of .Table is not a plain identifier
  plant chained 'package model

func rows(db *gorm.DB, ctx context.Context) error {
	return db.Session(&gorm.Session{}).WithContext(ctx).Table("widgets").Updates(patch).Error
}'

  # 5. a differently named receiver — a transaction handle, which is what a bulk update
  #    is usually written against
  plant receiver 'package model

func rows(tx *gorm.DB) error { return tx.Table("widgets").Where("x = ?", 1).Delete(nil).Error }'

  # 6. split across lines — gofmt PRODUCES this shape for a long chain, and a grep for
  #    `.Table(` on one line misses it
  plant linesplit 'package model

func rows(db *gorm.DB) error {
	return db.
		Table("widgets").
		Find(&out).Error
}'

  # 7. the table name in a variable, which defeats any check looking for a string literal
  plant variablename 'package model

func rows(db *gorm.DB, name string) error { return db.Table(name).Find(&out).Error }'

  # 8. a build-tagged file. Excluded from a build is not excluded from a query.
  mkdir -p "$fx/buildtag"
  printf '//go:build tools\n\npackage model\n\nfunc rows(db *gorm.DB) error { return db.Table("widgets").Find(&out).Error }\n' \
    >"$fx/buildtag/x.go"

  # 9. a generated file. "DO NOT EDIT" is not "do not check".
  plant generated '// Code generated by gen. DO NOT EDIT.

package model

func rows(db *gorm.DB) error { return db.Table("widgets").Find(&out).Error }'

  # 10. a //nolint-shaped escape, which this guard does not honour
  plant nolint 'package model

//nolint:all // deliberate
func rows(db *gorm.DB) error { return db.Table("widgets").Find(&out).Error }'

  for shape in direct projection modelmismatch chained receiver linesplit variablename \
               buildtag generated nolint; do
    local out status=0
    out="$("$BIN" -check=bare-table -strict-allowlist=false "$fx/$shape=1" 2>&1)" || status=$?
    if [ "$status" -ne 1 ]; then
      echo "self-test: shape '$shape' exited $status, want 1 — the guard does not catch it" >&2
      echo "$out" >&2
      rc=1
      continue
    fi
    if ! grep -q "$fx/$shape/x.go:[0-9]" <<<"$out"; then
      echo "self-test: shape '$shape' failed without naming a file and line:" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # --- the counterweights: the guard must NOT flag either of these ---

  # A. the REPLACEMENT shape. If this is flagged, the guard refuses the fix and every
  #    correct query in the repository reads as a violation.
  plant clean 'package model

func rows(db *gorm.DB, ctx context.Context) error {
	var out []Widget
	return db.WithContext(ctx).Model(&Widget{}).Find(&out).Error
}'

  # B. the COMMENT case, which is real source in this tree: core/rdb quotes gorm`s own
  #    error text, and that text contains db.Table("users"). A struct FIELD named Table,
  #    read rather than called, is here for the same reason — around seventy of those
  #    exist across the migrations.
  plant comments 'package model

// gorm reports: "Table not set, please set it like: db.Model(&user) or db.Table(\"users\")",
// so re-reporting it here would replace a precise message with a vaguer one.
func name(stmt *gorm.Statement) string { return stmt.Table }'

  local status
  for shape in clean comments; do
    status=0
    out="$("$BIN" -check=bare-table -strict-allowlist=false "$fx/$shape=1" 2>&1)" || status=$?
    if [ "$status" -ne 0 ]; then
      echo "self-test: the '$shape' fixture exited $status, want 0 — the guard flags something legitimate" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # C. 🔴 a scan that read nothing must FAIL, not report clean. This guard's allow-list is
  #    empty, so unlike its sibling it has no liveness probe of its own — the per-root file
  #    floor and the shapes above are the whole of its evidence, and this is the part that
  #    keeps a broken walker from reporting a clean tree.
  mkdir -p "$fx/empty"
  status=0
  "$BIN" -check=bare-table "$fx/empty=1" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an empty tree exited $status, want 2 — a scan that read nothing must not report clean" >&2
    rc=1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    exit 1
  fi
  echo "self-test passed: 10 shapes caught, the scoped form and the prose about the unscoped one allowed, an empty tree refused."
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

"$BIN" -check=bare-table "${ROOTS[@]}"
