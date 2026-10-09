#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses any non-test, non-migration Go source that runs raw SQL (`.Raw(` / `.Exec(`) over a
# tenant-scoped table, or whose SQL cannot be read statically, unless the enclosing function is
# in the allow-list in backend/tools/rdbguard/rawsql_allow.go with a stated reason.
#
# Defence in depth beside the tenant-scope callback. That callback injects its predicate into
# the statements it BUILDS; a Raw statement is finished SQL and an Exec runs on a processor with
# no callbacks at all. Whether such a statement is tenant-safe is a property of its text, so this
# is the checklist: every raw statement over a tenant-scoped table is either gone or sits in the
# allow-list beside the reason it is safe.
#
# 🔴 HOW "TENANT-SCOPED" IS DECIDED. Not from a list kept here: the table set is DERIVED on every
# run from the Go models under the scanned roots, with the same rule the callback applies at
# runtime (a TenantId or Tenant field, directly or through an embedded mixin) and gorm's own
# NamingStrategy / TableName() for the name. A test pins the field spellings to core/rdb.
#
# 🔴 FAIL CLOSED. SQL built from a variable, a call or another package's constant is flagged: the
# table it touches is unknown. Literals, `+`, fmt.Sprintf and same-package constants are followed.
#
# 🔴 AN ALLOW-LIST ENTRY MUST BE EXERCISED. An entry is (file, function, count). One whose
# function moved, whose site stopped matching, or that covers a different number of sites than
# it declares FAILS THE RUN (exit 2) — an exemption must not outlive its reason, and a second
# statement added to an exempted function must be read rather than inherited.
#
# ⚠️ WHAT IT CANNOT SEE, stated rather than left to be discovered:
#   - files named migration_*.go / baseline*.go are skipped (they run once, under the migration
#     system context, from frozen snapshots); a runtime query in a file with that name would not
#     be seen. _test.go, testdata, _legacy, vendor and .claude are skipped as for bare-table.
#   - backend/tools (maintainer-only, not shipped) and backend/k8s are not scanned.
#   - calls whose first argument is a context (`conn.Exec(ctx, sql)`) are pgx / graphql executors,
#     not gorm, and are skipped.
#   - only the SQL TEXT is read: a statement reaching a tenant table through a view, a function
#     or a join table with no Go model is out of reach.
#
#   hack/check-raw-sql-statements.sh
#   hack/check-raw-sql-statements.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The roots, each with the minimum number of non-test Go files it must yield (per root, not a
# total — see check-bare-table-statements.sh).
ROOTS=(backend/core=150 backend/services=500 backend/cli=50 deploy=1)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/rdbguard"

# Built from source every run, so the guard cannot drift from the analyzer it runs.
go build -o "$BIN" ./backend/tools/rdbguard/cmd/rdbguard

# Every fixture directory gets these models: one tenant-scoped table (widgets, through the
# embedded mixin) and one unscoped (gadgets). The table set is derived from the scanned roots,
# so a fixture must carry its own models.
MODELS='package model

type TenantScoped struct {
	TenantId string `gorm:"index"`
}

type Widget struct {
	TenantScoped
	Name string
}

type Gadget struct {
	ID uint
}
'

# ---------------------------------------------------------------------------
# self-test: prove the check can fail, once per shape.
# ---------------------------------------------------------------------------
# 🔴 ONE FIXTURE PER SHAPE. A combined fixture is satisfied by a checker that detects any
# single one of them, which is how a guard ends up enforcing a fraction of what it claims.
self_test() {
  local fx="$TMP/fx" rc=0 out status

  plant() { # plant <name> <body-with-package-clause>
    mkdir -p "$fx/$1"
    printf '%s\n' "$MODELS" >"$fx/$1/models.go"
    printf '%s\n' "$2" >"$fx/$1/x.go"
  }

  # --- shapes that MUST be flagged (exit 1, naming file and line) ---
  plant raw 'package model

func f(db *gorm.DB) { db.Raw("SELECT * FROM widgets WHERE name = ?", 1).Scan(nil) }'

  plant execupdate 'package model

func f(tx *gorm.DB) error { return tx.Exec("UPDATE widgets SET name = ?", "x").Error }'

  plant execdelete 'package model

func f(tx *gorm.DB) error { return tx.Exec("DELETE FROM \"area\".\"widgets\"").Error }'

  plant dynamicvar 'package model

func f(tx *gorm.DB, q string) error { return tx.Exec(q).Error }'

  plant dynamicsprintf 'package model

func f(tx *gorm.DB, t string) error { return tx.Exec(fmt.Sprintf("DELETE FROM %s", t)).Error }'

  plant constquery 'package model

const q = "SELECT * FROM widgets"

func f(tx *gorm.DB) { tx.Raw(q) }'

  plant linesplit 'package model

func f(db *gorm.DB) {
	db.
		WithContext(c).
		Raw("SELECT 1 FROM widgets").
		Scan(nil)
}'

  for shape in raw execupdate execdelete dynamicvar dynamicsprintf constquery linesplit; do
    status=0
    out="$("$BIN" -check=raw-sql -strict-allowlist=false "$fx/$shape=2" 2>&1)" || status=$?
    if [ "$status" -ne 1 ]; then
      echo "self-test: shape '$shape' exited $status, want 1 — the guard does not catch it" >&2
      echo "$out" >&2
      rc=1
      continue
    fi
    if ! grep -q "/$shape/x.go:[0-9]" <<<"$out"; then
      echo "self-test: shape '$shape' failed without naming a file and line:" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # --- counterweights: these must NOT be flagged ---
  plant cleanunscoped 'package model

func f(db *gorm.DB) { db.Raw("SELECT * FROM gadgets").Scan(nil) }'

  plant cleanlock 'package model

func f(tx *gorm.DB) error { return tx.Exec("SELECT pg_advisory_xact_lock(?)", 1).Error }'

  plant cleanmodel 'package model

func f(db *gorm.DB, ctx context.Context) error {
	return db.WithContext(ctx).Model(&Widget{}).Where("name = ?", "x").Find(&out).Error
}'

  plant cleanpgx 'package model

func f(ctx context.Context, q Q) { q.Exec(ctx, "DELETE FROM widgets") }'

  mkdir -p "$fx/cleanmigration"
  printf '%s\n' "$MODELS" >"$fx/cleanmigration/models.go"
  printf 'package model\n\nfunc f(tx *gorm.DB) { tx.Exec("UPDATE widgets SET a = 1") }\n' \
    >"$fx/cleanmigration/migration_x.go"

  for shape in cleanunscoped cleanlock cleanmodel cleanpgx cleanmigration; do
    status=0
    out="$("$BIN" -check=raw-sql -strict-allowlist=false "$fx/$shape=2" 2>&1)" || status=$?
    if [ "$status" -ne 0 ]; then
      echo "self-test: the '$shape' fixture exited $status, want 0 — the guard flags something legitimate" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # --- a stale allow-list must FAIL. A fixture tree contains none of the real entries, so with
  #     the liveness check ON (the default, and the only mode used on the repository) the run
  #     must be refused as a broken instrument (exit 2), even on a tree with no findings.
  status=0
  out="$("$BIN" -check=raw-sql "$fx/cleanunscoped=2" 2>&1)" || status=$?
  if [ "$status" -ne 2 ] || ! grep -q "matched nothing\|covers" <<<"$out"; then
    echo "self-test: stale allow-list entries exited $status, want 2 naming the entry" >&2
    echo "$out" >&2
    rc=1
  fi

  # --- a scan that read nothing must FAIL, not report clean ---
  mkdir -p "$fx/empty"
  status=0
  "$BIN" -check=raw-sql -strict-allowlist=false "$fx/empty=1" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an empty tree exited $status, want 2 — a scan that read nothing must not report clean" >&2
    rc=1
  fi

  # --- the allow-list mechanics (an allow-listed site absorbed, a stale entry reported, a grown
  #     function reported, exact-path matching) are asserted against injected lists ---
  if ! (cd backend/tools/rdbguard && go test -count=1 -run 'TestRawSQLAllowList|TestRawSQLFlagsShapes' ./ >/dev/null); then
    echo "self-test: the allow-list unit tests failed (run: cd backend/tools/rdbguard && go test -run TestRawSQL ./)" >&2
    rc=1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    exit 1
  fi
  echo "self-test passed: 7 shapes caught, 5 legitimate forms allowed, a stale allow-list and an empty tree refused."
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

"$BIN" -check=raw-sql "${ROOTS[@]}"
