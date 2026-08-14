#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if any Go source hands gorm a condition as a bare Go VALUE rather than as
# a spelled-out predicate — `db.Find(&out, ids)`, `db.Where(ids)` — instead of
# going through rdb.FindByIds.
#
# 🔴 What this is protecting against, and why a "defensive" guard it is not.
#
# gorm's inline-condition form DROPS an empty slice rather than rendering a
# predicate that matches nothing, so `Find(&out, []uint{})` is an unfiltered,
# unpaginated SELECT: a request for no rows answered with every row in scope. It
# is reachable straight from the public API, because `xById(ids: [])` is a legal
# GraphQL document and nothing upstream rejects an empty list. Twenty-one lookups
# across two services had it at once, and the reason it spread is worth stating:
# the SIBLING form, `Find(&out, "token in ?", tokens)`, renders a match-nothing
# predicate and is completely safe. The two sit next to each other in every
# api_*.go and behave OPPOSITELY on the same input, so reading either one teaches
# the wrong rule about the other. Nothing at a call site makes the difference
# visible — which is exactly what a source guard is for.
#
# `Where` is here for the same reason, measured the same way: `db.Where([]uint{})
# .Find(&out)` also returns the whole table. A zero-valued struct condition,
# `Where(Thing{})`, matches everything too — so a bare identifier passed to Where
# is worth a second look whatever its type.
#
# 🔴 WHAT THIS DOES NOT SEE. Stated in full, because "the guard is green" is only
# worth what its coverage is, and every shape below was MEASURED against gorm
# v1.31.2 rather than recalled:
#
#   - A call split across lines. None exist today and gofmt does not create them.
#   - `First(&x, ids)` / `Take(&x, ids)` with an empty slice: gorm returns THE
#     TABLE'S FIRST ROW with a nil error — a wrong entity reported as found, which
#     is worse than a wrong count. Deliberately NOT matched: the legitimate scalar
#     form `First(&x, id)` is textually identical, and a guard with false
#     positives is a guard people learn to suppress. The one such call in the tree
#     (command-delivery loadCommand) takes a scalar id, which cannot be empty.
#   - `Delete(&T{}, ids)` is NOT a hazard: gorm refuses a delete with no WHERE
#     clause outright (ErrMissingWhereClause, 0 rows affected). Measured — the
#     assumption that it would silently delete the table was wrong.
#
# A grep is the right instrument here, unlike hack/check-subscribe-confirmed.sh,
# which needs a type checker: the discriminator is syntactic, not type-dependent.
#
#   hack/check-inline-id-conditions.sh
#   hack/check-inline-id-conditions.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

# Two shapes:
#   .Find(&x, y     where y does not begin with a double quote
#   .Where(x)       a lone bare value, no predicate string
# The destination allows dots so a field target — `Find(&s.cache, ids)` — cannot
# slip past; that was a real hole in the first version of this pattern.
PATTERN='\.Find\(&?[A-Za-z_][A-Za-z0-9_.]*, [^"]|\.Where\([A-Za-z_][A-Za-z0-9_.]*\)'

# Paths (relative to the scanned root) that are allowed to carry the raw form.
# Deliberately short, and every entry is a place where the unsafe call IS the
# subject rather than an oversight:
#
#   rdb/find_by_ids.go       the one sanctioned call — the helper itself
#   rdb/find_by_ids_test.go  makes the unsafe call on purpose, to pin the gorm
#                            behaviour the helper exists for
#   _legacy/                 archived pre-migration tree, not in the workspace
#
# If a legitimate new case appears — a subquery passed to Where, say — spell the
# condition out or add an entry here WITH ITS REASON. Do not widen the pattern.
is_exempt() {
  case "$1" in
    _legacy/*) return 0 ;;
    backend/core/rdb/find_by_ids.go | backend/core/rdb/find_by_ids_test.go) return 0 ;;
    *) return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# scan <dir>: report every offending line under dir. Returns 1 if any were found.
# ---------------------------------------------------------------------------
# Taking the root as an argument is what lets the self-test run the REAL scanner
# over a planted tree instead of a copy of its logic, and it means nothing is ever
# written into the repository to test the check — no chance of a deliberately
# broken file being left behind in a working tree shared by several sessions.
scan() {
  local root="$1" rc=0 hit path line
  local -a hits=()

  # `|| true` because grep exits 1 for "no matches", which is the PASSING case
  # here and must not trip set -e. 🔴 That swallows a BROKEN PATTERN too — grep
  # exits 2 — which would report a clean tree it never really read. The self-test
  # is the fence: CI runs it in the same job, immediately before the real scan,
  # and it requires a planted call to be reported.
  while IFS= read -r hit; do
    path="${hit%%:*}"
    path="${path#./}"
    is_exempt "$path" && continue
    # Skip whole-line comments: prose ABOUT the pattern (this file's own header,
    # the doc comment on FindByIds) is not a call.
    line="${hit#*:}"
    line="${line#*:}"
    case "${line#"${line%%[![:space:]]*}"}" in
      //*) continue ;;
    esac
    hits+=("$hit")
    rc=1
  done < <(cd "$root" && grep -rnE --include='*.go' "$PATTERN" . 2>/dev/null || true)

  if [ "$rc" -ne 0 ]; then
    printf '%s\n' "${hits[@]}" >&2
  fi
  return "$rc"
}

check() {
  if ! scan "$ROOT"; then
    echo >&2
    echo "FAILED: the calls above hand gorm a condition as a bare Go value. With an EMPTY" >&2
    echo "        slice gorm drops the condition entirely and returns every row in scope;" >&2
    echo "        a zero-valued struct condition matches everything for the same reason." >&2
    echo >&2
    echo "        For a by-id lookup, use rdb.FindByIds:" >&2
    echo >&2
    echo "            return rdb.FindByIds[Device](api.RDB.DB(ctx).Preload(\"DeviceType\"), ids)" >&2
    echo >&2
    echo "        Otherwise spell the predicate out: .Where(\"id in ?\", ids)" >&2
    echo >&2
    return 1
  fi
  echo "==> No bare-value gorm conditions outside rdb.FindByIds."
}

# ---------------------------------------------------------------------------
# Self-test.
# ---------------------------------------------------------------------------
# 🔴 BOTH directions, and the exemption too. "The check reports a bad call" is
# satisfied by a check that reports everything, and an exemption list is the part
# most likely to quietly swallow the whole tree — an over-broad entry would make
# every later run pass while looking identical to a clean one.
self_test() {
  local tmp status=0
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064  # expand tmp now: the trap must not depend on scope
  trap "rm -rf '$tmp'" RETURN

  mkdir -p "$tmp/backend/services/x/model" "$tmp/backend/core/rdb" "$tmp/_legacy/old"

  # Each planted shape is checked on its own, so a pattern that lost one branch
  # cannot hide behind another branch still firing.
  echo "==> Self-test: every unsafe shape must be reported, one at a time"
  local shape
  for shape in \
      'result := api.RDB.DB(ctx).Find(&found, ids)' \
      'result := api.RDB.DB(ctx).Find(&api.cache.rows, ids)' \
      'result := api.RDB.DB(ctx).Where(ids).Find(&found)' \
      ; do
    cat > "$tmp/backend/services/x/model/api_things.go" <<EOF
package model

func (api *Api) ThingsById(ctx context.Context, ids []uint) ([]*Thing, error) {
	found := make([]*Thing, 0)
	$shape
	return found, result.Error
}
EOF
    status=0
    scan "$tmp" 2>/dev/null || status=$?
    [ "$status" -eq 1 ] || {
      echo "SELF-TEST FAILED: not reported: $shape" >&2
      return 1
    }
    echo "    reported: $shape"
  done

  echo "==> Self-test: the safe forms and the exempt paths must be reported by nothing"
  cat > "$tmp/backend/services/x/model/api_things.go" <<'EOF'
package model

// A doc comment mentioning db.Find(&out, ids) is prose, not a call.
func (api *Api) ThingsById(ctx context.Context, ids []uint) ([]*Thing, error) {
	return rdb.FindByIds[Thing](api.RDB.DB(ctx), ids)
}

func (api *Api) ThingsByToken(ctx context.Context, tokens []string) ([]*Thing, error) {
	found := make([]*Thing, 0)
	result := api.RDB.DB(ctx).Find(&found, "token in ?", tokens)
	return found, result.Error
}

func (api *Api) AllThings(ctx context.Context) ([]*Thing, error) {
	found := make([]*Thing, 0)
	return found, api.RDB.DB(ctx).Where("live = ?", true).Find(&found).Error
}

func (api *Api) SeedThing(ctx context.Context) error {
	return api.RDB.DB(ctx).Where(Thing{Token: "seed"}).FirstOrCreate(&Thing{}).Error
}
EOF
  # The exempt paths, carrying the very shape the check rejects everywhere else.
  echo 'func f() { db.Find(&found, ids) }' > "$tmp/backend/core/rdb/find_by_ids.go"
  echo 'func f() { db.Find(&found, ids) }' > "$tmp/backend/core/rdb/find_by_ids_test.go"
  echo 'func f() { db.Find(&found, ids) }' > "$tmp/_legacy/old/archived.go"
  status=0
  scan "$tmp" || status=$?
  [ "$status" -eq 0 ] || { echo "SELF-TEST FAILED: a safe or exempt file was reported." >&2; return 1; }
  echo "    clean"

  echo "==> Self-test: the exemption must not extend past those files"
  # A neighbour in the same package, and a path that merely CONTAINS the exempt
  # name — the two ways a path test gets written too loosely.
  echo 'func f() { db.Find(&found, ids) }' > "$tmp/backend/core/rdb/rdb.go"
  status=0
  scan "$tmp" 2>/dev/null || status=$?
  [ "$status" -eq 1 ] || { echo "SELF-TEST FAILED: the rdb exemption swallowed its whole package." >&2; return 1; }
  rm "$tmp/backend/core/rdb/rdb.go"

  mkdir -p "$tmp/backend/services/y/find_by_ids.go.d"
  echo 'func f() { db.Find(&found, ids) }' > "$tmp/backend/services/y/find_by_ids.go.d/x.go"
  status=0
  scan "$tmp" 2>/dev/null || status=$?
  [ "$status" -eq 1 ] || { echo "SELF-TEST FAILED: the exemption matched on a substring of the path." >&2; return 1; }
  echo "    scoped"

  echo "==> Self-test passed."
}

case "$#:${1:-}" in
  0:)            check ;;
  1:--self-test) self_test ;;
  *)             usage ;;
esac
