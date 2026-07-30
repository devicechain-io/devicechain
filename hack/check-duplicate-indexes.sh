#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if any area's golden schema declares TWO indexes covering the same thing:
# the same table, the same column list, the same uniqueness, the same predicate.
# A second index on an identical key is pure cost — every insert, update and delete
# maintains both B-trees forever, and no read can ever prefer one over the other.
#
# 🔑 THIS CHECKS THE OUTCOME, NOT THE CODING PATTERN THAT PRODUCED IT, and that is
# the whole point. The duplicates it was written for came from a specific mistake —
# two snapshot structs describing one table, one of them pinning an explicit
# TableName so gorm derived a SECOND name for the same index — and a linter aimed at
# that pattern would have caught that instance and nothing else. Indexes can collide
# for reasons nobody has thought of yet: a hand-written CREATE INDEX duplicating a
# derived one, two migrations adding the same index under different names, a
# composite that happens to match an existing single-column key. Reading the emitted
# schema catches every one of those that appears as a CREATE INDEX.
#
# 🔴 ITS FIELD OF VIEW IS `CREATE INDEX`, WHICH IS NOT EVERY INDEX. The goldens also carry ~84
# `ALTER TABLE ... ADD CONSTRAINT ... PRIMARY KEY|UNIQUE` statements, and each of those creates a
# real index too — so a plain index duplicating a unique CONSTRAINT is not flagged here. No such
# pair exists today (checked), which is why this is a documented limit rather than a TODO: widening
# the parse to constraints means modelling which constraint kinds imply an index and in what column
# order, and getting that subtly wrong would produce false positives on a clean tree — worse than
# the narrow true negative it would close.
#
# It runs against the GOLDENS rather than a live database, so it needs no container
# and no Postgres: the goldens are the captured output of the real migration chain
# (see hack/migration-diff.sh), so anything true of them is true of a fresh install.
#
# Legitimately NOT duplicates, and deliberately not flagged:
#   - same columns, different predicate — a partial unique index over live rows
#     (ADR-042 P1: `WHERE deleted_at IS NULL`) alongside a plain index on the same
#     columns does real work the other cannot do.
#   - same columns, different uniqueness — a UNIQUE constraint and a plain index
#     express different things to the planner and to writers.
#   - different column ORDER — (a,b) and (b,a) serve different prefix lookups.
#
# Usage:
#   hack/check-duplicate-indexes.sh              # check the goldens
#   hack/check-duplicate-indexes.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GOLDEN_DIR="backend/tools/migrationdiff/golden"

scan() {
  # $1 = directory of *.sql goldens. Prints one line per duplicate group; prints
  # nothing and exits 0 when clean.
  python3 - "$1" <<'PY'
import collections, glob, os, re, sys

golden_dir = sys.argv[1]
files = sorted(glob.glob(os.path.join(golden_dir, "*.sql")))
if not files:
    print(f"no goldens found under {golden_dir}", file=sys.stderr)
    sys.exit(2)

# pg_dump emits one CREATE INDEX per line. Identifiers are quoted only when they
# need it (our area names contain a hyphen, index names usually do not), so both
# forms have to be accepted — matching only the quoted form would silently skip
# every unprefixed index, which is exactly the population this check exists for.
INDEX = re.compile(
    r'^CREATE\s+(?P<uniq>UNIQUE\s+)?INDEX\s+"?(?P<name>[^"\s]+)"?\s+'
    r'ON\s+"?(?P<schema>[^"\s.]+)"?\.(?P<table>\S+)\s+'
    r'USING\s+\w+\s+\((?P<cols>[^)]*)\)(?P<tail>.*?);?\s*$'
)

groups = collections.defaultdict(list)
for path in files:
    area = os.path.basename(path)[:-4]
    with open(path) as fh:
        for line in fh:
            m = INDEX.match(line.strip())
            if not m:
                continue
            # Normalise whitespace inside the column list but PRESERVE its order:
            # (a, b) and (b, a) are different indexes, so sorting here would
            # report a false duplicate.
            cols = ",".join(c.strip() for c in m.group("cols").split(","))
            key = (
                area,
                m.group("schema"),
                m.group("table"),
                cols,
                bool(m.group("uniq")),
                m.group("tail").strip(),   # the WHERE predicate, if any
            )
            groups[key].append(m.group("name"))

dupes = {k: v for k, v in groups.items() if len(v) > 1}
for (area, schema, table, cols, uniq, pred) in sorted(dupes):
    names = dupes[(area, schema, table, cols, uniq, pred)]
    detail = f"{schema}.{table} ({cols})"
    if uniq:
        detail += " UNIQUE"
    if pred:
        detail += f" {pred}"
    print(f"{area}: {detail}")
    for n in sorted(names):
        print(f"    {n}")
sys.exit(1 if dupes else 0)
PY
}

# Reject anything unrecognised. A mistyped `--selftest` used to fall through to the
# real check and exit 0, so a CI step spelled that way would report a passing
# self-test that never ran — the check-that-cannot-fail shape, one level up.
case "${1:-}" in
  ""|--self-test) ;;
  *) echo "usage: $(basename "$0") [--self-test]" >&2; exit 2 ;;
esac

# ── self-test: plant a duplicate and require the check to catch it ────────────
# A check nobody has watched fail is a check nobody has watched work. This copies
# the real goldens to a temp dir, appends a second index over a key that already
# has one, and requires a non-zero exit. It never writes to the real goldens.
if [ "${1:-}" = "--self-test" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  cp "$GOLDEN_DIR"/*.sql "$tmp/"

  # Duplicate whatever the first CREATE INDEX in this golden is, under a new name,
  # rather than hardcoding a table — so the self-test cannot rot when the schema
  # moves on. It must reproduce the ORIGINAL line's columns/uniqueness/predicate
  # exactly, or it would not be a duplicate at all and the test would pass
  # vacuously.
  victim="$tmp/device-management.sql"
  line="$(grep -m1 -E '^CREATE (UNIQUE )?INDEX ' "$victim")"
  [ -n "$line" ] || { echo "self-test: found no CREATE INDEX to duplicate" >&2; exit 2; }
  # Rewrite only the index NAME, leaving everything after "ON" untouched.
  echo "$line" | sed -E 's/^CREATE( UNIQUE)? INDEX "?[^" ]+"? ON /CREATE\1 INDEX dc_selftest_planted_dupe ON /' >> "$victim"

  echo "==> self-test: planted a duplicate index; the check must now FAIL"
  if out="$(scan "$tmp" 2>&1)"; then
    echo "$out"
    echo "SELF-TEST FAILED: a planted duplicate index was NOT detected." >&2
    echo "The check is not able to fail, so a green run from it means nothing." >&2
    exit 1
  fi
  echo "$out" | sed 's/^/    /'
  echo "==> self-test passed: the planted duplicate was detected."

  # And the counterweight — the unmodified goldens must still pass, or the check
  # could be "detecting" by failing on everything.
  echo "==> self-test: the unmodified goldens must still PASS"
  rm -rf "$tmp" && mkdir -p "$tmp" && cp "$GOLDEN_DIR"/*.sql "$tmp/"
  if ! scan "$tmp" >/dev/null 2>&1; then
    echo "SELF-TEST FAILED: clean goldens were reported as duplicated." >&2
    exit 1
  fi
  echo "==> self-test passed: clean goldens pass."
  exit 0
fi

# ── the real check ────────────────────────────────────────────────────────────
if out="$(scan "$GOLDEN_DIR")"; then
  echo "==> No duplicate indexes in any golden schema."
  exit 0
fi

cat >&2 <<EOF
==> DUPLICATE INDEXES FOUND

Each group below is two or more indexes covering an identical key. Both are
maintained on every write and no read can prefer one, so one of them is pure
cost. Drop all but one.

$out

The usual cause is a table described by MORE THAN ONE snapshot struct in the same
migration, where one pins an explicit TableName and the other does not: gorm
applies the area TablePrefix only to names it DERIVES, so the two shapes produce
two names for the same index and nothing errors. One table, one shape.
EOF
exit 1
