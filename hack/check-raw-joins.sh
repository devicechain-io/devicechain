#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses any non-test Go source that joins a tenant-scoped table (`.Joins("JOIN t ON ...")`)
# without a tenant-column equality in the ON clause, unless the enclosing function is in the
# allow-list in backend/tools/rdbguard/rawsql_allow.go with a stated reason.
#
# Defence in depth beside the tenant-scope callback. The callback filters the OUTER table of a
# statement; a joined table is added by hand-written SQL and nothing filters it. Today every
# join in the tree is safe by PROVENANCE of its foreign key (the key was written under a
# tenant-scoped write). An ON clause that also equates the tenant columns makes it safe by QUERY:
# the property can be read in one place and does not depend on every writer of the key.
#
# What counts as the equality: `<joined table or alias>.tenant_id = <x>.tenant_id` (either order,
# either column spelling, quoted or not) or `<joined>.tenant_id = ?`. The other side is not
# required to be the outer table — that is a reviewer's call.
#
# 🔴 HOW "TENANT-SCOPED" IS DECIDED. Not from a list kept here: the table set is DERIVED on every
# run from the Go models under the scanned roots, with the same rule the callback applies at
# runtime (see check-raw-sql-statements.sh).
#
# 🔴 FAIL CLOSED. An association join (`Joins("Device")`) names no table at the call, a dynamic
# join string cannot be read, and a subquery join cannot be parsed: each is flagged.
#
# 🔴 AN ALLOW-LIST ENTRY MUST BE EXERCISED. An entry is (file, function, count); one that matches
# nothing, or a different number of sites than it declares, FAILS THE RUN (exit 2).
#
# ⚠️ WHAT IT CANNOT SEE: a join expressed other than through gorm's Joins (a raw statement is the
# raw-sql guard's), a join written through a Scopes helper that builds its string elsewhere (it
# would be flagged as dynamic), and a table with a tenant column but no Go model.
#
#   hack/check-raw-joins.sh
#   hack/check-raw-joins.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

ROOTS=(backend/core=150 backend/services=500 backend/cli=50 deploy=1)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/rdbguard"

# Built from source every run, so the guard cannot drift from the analyzer it runs.
go build -o "$BIN" ./backend/tools/rdbguard/cmd/rdbguard

# Every fixture directory carries its own models: the table set is derived from the roots.
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
# 🔴 ONE FIXTURE PER SHAPE, and the counterweights matter as much as the shapes: a guard that
# flags the joined table WITH its tenant equality refuses the fix.
self_test() {
  local fx="$TMP/fx" rc=0 out status

  plant() { # plant <name> <body-with-package-clause>
    mkdir -p "$fx/$1"
    printf '%s\n' "$MODELS" >"$fx/$1/models.go"
    printf '%s\n' "$2" >"$fx/$1/x.go"
  }

  # --- shapes that MUST be flagged (exit 1, naming file and line) ---
  plant noequality 'package model

func f(db *gorm.DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id") }'

  plant leftjoin 'package model

func f(db *gorm.DB) {
	db.Joins("LEFT JOIN widgets ON widgets.id = gadgets.widget_id AND widgets.deleted_at IS NULL")
}'

  plant wrongside 'package model

func f(db *gorm.DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND g.tenant_id = ?") }'

  plant association 'package model

func f(db *gorm.DB) { db.Joins("Widget") }'

  plant dynamic 'package model

func f(db *gorm.DB, j string) { db.Joins(j) }'

  plant secondjoin 'package model

func f(db *gorm.DB) {
	db.Joins("JOIN widgets w ON w.id = g.a AND w.tenant_id = g.tenant_id JOIN widgets x ON x.id = g.b")
}'

  plant linesplit 'package model

func f(db *gorm.DB) {
	db.
		Model(&Gadget{}).
		Joins("JOIN widgets w ON w.id = gadgets.widget_id")
}'

  for shape in noequality leftjoin wrongside association dynamic secondjoin linesplit; do
    status=0
    out="$("$BIN" -check=raw-join -strict-allowlist=false "$fx/$shape=2" 2>&1)" || status=$?
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
  plant cleanequality 'package model

func f(db *gorm.DB) { db.Joins("JOIN widgets w ON w.id = g.widget_id AND w.tenant_id = g.tenant_id") }'

  plant cleanreversed 'package model

func f(db *gorm.DB) { db.Joins("JOIN widgets w ON g.tenant_id = w.tenant_id AND w.id = g.widget_id") }'

  plant cleanbind 'package model

func f(db *gorm.DB) { db.Joins("JOIN widgets ON widgets.id = g.widget_id AND widgets.tenant_id = ?", 1) }'

  plant cleanunscoped 'package model

func f(db *gorm.DB) { db.Joins("JOIN gadgets g2 ON g2.id = widgets.gadget_id") }'

  for shape in cleanequality cleanreversed cleanbind cleanunscoped; do
    status=0
    out="$("$BIN" -check=raw-join -strict-allowlist=false "$fx/$shape=2" 2>&1)" || status=$?
    if [ "$status" -ne 0 ]; then
      echo "self-test: the '$shape' fixture exited $status, want 0 — the guard flags something legitimate" >&2
      echo "$out" >&2
      rc=1
    fi
  done

  # --- a stale allow-list must FAIL. A fixture tree contains none of the real entries, so with
  #     the liveness check ON (the only mode used on the repository) the run is refused as a
  #     broken instrument (exit 2), even on a tree with no findings.
  status=0
  out="$("$BIN" -check=raw-join "$fx/cleanequality=2" 2>&1)" || status=$?
  if [ "$status" -ne 2 ] || ! grep -q "matched nothing\|covers" <<<"$out"; then
    echo "self-test: stale allow-list entries exited $status, want 2 naming the entry" >&2
    echo "$out" >&2
    rc=1
  fi

  # --- a scan that read nothing must FAIL, not report clean ---
  mkdir -p "$fx/empty"
  status=0
  "$BIN" -check=raw-join -strict-allowlist=false "$fx/empty=1" >/dev/null 2>&1 || status=$?
  if [ "$status" -ne 2 ]; then
    echo "self-test: an empty tree exited $status, want 2 — a scan that read nothing must not report clean" >&2
    rc=1
  fi

  # --- the allow-list mechanics (an allow-listed site absorbed, a stale entry reported, an
  #     entry whose join gained the equality reported) are asserted against injected lists ---
  if ! (cd backend/tools/rdbguard && go test -count=1 -run 'TestRawJoinAllowList|TestRawJoinShapes' ./ >/dev/null); then
    echo "self-test: the allow-list unit tests failed (run: cd backend/tools/rdbguard && go test -run TestRawJoin ./)" >&2
    rc=1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "self-test FAILED" >&2
    exit 1
  fi
  echo "self-test passed: 7 shapes caught, 4 legitimate forms allowed, a stale allow-list and an empty tree refused."
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

"$BIN" -check=raw-join "${ROOTS[@]}"
