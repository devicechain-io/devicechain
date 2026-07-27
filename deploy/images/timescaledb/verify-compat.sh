#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The compatibility gate for TimescaleDB version bumps (ADR-020 A2.6).
#
# THE DEFECT IT EXISTS TO PREVENT. `timescaledb.so` is a stub that loads the
# versioned library matching each database's catalog version. CloudNativePG
# rolling-updates replicas before the primary, which is the correct order — but
# only if the new image still ships the OLD versioned library. If it does not,
# the first replica onto the new image dies with
#
#     could not access file "$libdir/timescaledb-2.X.Y"
#
# mid-rollout, on a cluster that was healthy a minute earlier. This is not a
# hypothetical: Timescale's own official image did exactly this in December 2025
# (timescale#9072).
#
# WHAT IT ENFORCES — a SUPERSET rule, not a one-hop rule. The obvious check
# ("is the previous version carried?") passes on this sequence and still loses a
# library that clusters are running:
#
#     2.28.3, compat=()            -> published
#     2.29.0, compat=(2.28.3)      -> ok, ships both
#     2.30.0, compat=(2.29.0)      -> "previous is carried", and 2.28.3 is GONE
#
# A database whose catalog is still at 2.28.3 then kills its replica — and
# catalogs DO lag, because CNPG never runs `ALTER EXTENSION timescaledb UPDATE`
# for you, so "we upgraded" stays false until a human ran it per database. So
# the rule is: this commit's {target} ∪ {compat} must COVER the previous
# commit's {target} ∪ {compat}. Retiring a version is then a deliberate,
# reviewed act rather than a side effect of a bump — see DC_COMPAT_ALLOW_DROP.
#
# WHY IT READS GIT RATHER THAN TRUSTING A CHECKLIST. The rule is easy to state
# and easy to forget, and forgetting it produces an image that builds green,
# smokes green, and fails only against a live cluster during a rollout. So the
# previous state is not remembered by a human: it is read out of the committed
# history of versions.conf.
#
#   ./verify-compat.sh              # compare against the previous commit
#   ./verify-compat.sh --self-test  # prove the gate is capable of failing
#
# Environment:
#   DC_COMPAT_BASE_REF     what to compare against (default HEAD~1). CI passes
#                          the event's true base so a multi-commit push cannot
#                          hide a bad bump in an earlier commit.
#   DC_COMPAT_ALLOW_DROP   space-separated versions being deliberately retired.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
REL="deploy/images/timescaledb/versions.conf"
BASE_REF="${DC_COMPAT_BASE_REF:-HEAD~1}"
ALLOW_DROP="${DC_COMPAT_ALLOW_DROP:-}"

# ---------------------------------------------------------------------------
# Parsing. ONE parser, shared with the build.
#
# An earlier version read the file with sed while build.sh sourced it. The two
# disagreed — a trailing space made the gate believe the pin was `2.31.0 ` while
# the build used `2.31.0`, so the gate's own error message named a library path
# with a space in it. Sourcing in a subshell means the gate and the build cannot
# hold different beliefs about what the pin is.
# ---------------------------------------------------------------------------
read_vars_from_content() { # <content> -> echoes "<target>|<compat>"
  printf '%s\n' "$1" > "$TMPDIR_SELF/vars.env"
  (
    set -a
    # shellcheck source=/dev/null
    . "$TMPDIR_SELF/vars.env"
    set +a
    printf '%s|%s\n' "${TIMESCALEDB_VERSION:-}" "${TIMESCALEDB_COMPAT_VERSIONS:-}"
  )
}

# covers <have-list> <needle>
covers() {
  local have="$1" needle="$2" v
  set -f
  for v in $have; do [ "$v" = "$needle" ] && { set +f; return 0; }; done
  set +f
  return 1
}

# missing_from <have-list> <required-list> <allowed-drop-list> -> echoes what is not covered
missing_from() {
  local have="$1" required="$2" allowed="$3" v out=""
  set -f
  for v in $required; do
    covers "$have" "$v" && continue
    covers "$allowed" "$v" && continue
    out="${out}${out:+ }${v}"
  done
  set +f
  printf '%s' "$out"
}

TMPDIR_SELF="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_SELF"' EXIT

# ---------------------------------------------------------------------------
# Self-test.
#
# Two levels, because the first level alone was misleading. Testing only the
# decision function proved that `covers()` can say no — while a regression in
# the UNTESTED half (reading the current file instead of the git one) left the
# real gate passing a known-bad bump with every self-test line still green.
# So level 2 drives the whole script against a throwaway repository.
# ---------------------------------------------------------------------------
if [ "${1:-}" = "--self-test" ]; then
  fails=0

  echo "==> Level 1: the decision function"
  check() { # <desc> <expect: pass|fail> <have> <required> <allowed>
    local desc="$1" expect="$2" got missing
    missing="$(missing_from "$3" "$4" "$5")"
    if [ -z "$missing" ]; then got=pass; else got=fail; fi
    if [ "$got" = "$expect" ]; then
      echo "  ok   ${desc} (${got})"
    else
      echo "  FAIL ${desc}: expected ${expect}, got ${got} (missing: ${missing:-none})" >&2
      fails=$((fails + 1))
    fi
  }
  check "unchanged pin"                       pass "2.28.3"               "2.28.3"        ""
  check "bump carrying the old version"       pass "2.29.0 2.28.3"        "2.28.3"        ""
  check "bump carrying two old versions"      pass "2.30.0 2.29.0 2.28.3" "2.29.0 2.28.3" ""
  check "bump dropping the old version"       fail "2.29.0"               "2.28.3"        ""
  check "bump carrying an unrelated version"  fail "2.29.0 2.20.0"        "2.28.3"        ""
  # 🔴 The one a one-hop gate misses.
  check "second bump silently dropping 2.28.3" fail "2.30.0 2.29.0"       "2.29.0 2.28.3" ""
  check "the same drop, explicitly retired"   pass "2.30.0 2.29.0"        "2.29.0 2.28.3" "2.28.3"

  echo "==> Level 2: the whole gate, against a real repository"
  repo="$TMPDIR_SELF/repo"
  mkdir -p "$repo/deploy/images/timescaledb"
  git -c init.defaultBranch=main init -q "$repo"
  git -C "$repo" config user.email t@t; git -C "$repo" config user.name t
  cp "$HERE/verify-compat.sh" "$repo/deploy/images/timescaledb/verify-compat.sh"
  chmod +x "$repo/deploy/images/timescaledb/verify-compat.sh"

  write_env() { printf 'TIMESCALEDB_VERSION=%s\nTIMESCALEDB_COMPAT_VERSIONS=%s\n' "$1" "$2" \
                  > "$repo/deploy/images/timescaledb/versions.conf"; }

  write_env 2.28.3 ""
  git -C "$repo" add -A >/dev/null; git -C "$repo" commit -qm base

  run_in_repo() { ( cd "$repo" && ./deploy/images/timescaledb/verify-compat.sh >/dev/null 2>&1 ); }

  e2e() { # <desc> <expect-rc> <target> <compat> [allow-drop]
    local desc="$1" want="$2" rc
    write_env "$3" "$4"
    # --allow-empty: the "unchanged pin" case writes an identical file, and a
    # plain commit would fail with "nothing to commit" and take the whole
    # self-test down with set -e.
    git -C "$repo" add -A >/dev/null; git -C "$repo" commit -q --allow-empty -m "$desc" >/dev/null
    if DC_COMPAT_ALLOW_DROP="${5:-}" run_in_repo; then rc=0; else rc=$?; fi
    if [ "$rc" = "$want" ]; then
      echo "  ok   ${desc} (rc=${rc})"
    else
      echo "  FAIL ${desc}: expected rc=${want}, got rc=${rc}" >&2
      fails=$((fails + 1))
    fi
    git -C "$repo" reset -q --hard HEAD~1
  }

  e2e "good bump carries the old version" 0 2.29.0 "2.28.3"
  e2e "bad bump drops the old version"    1 2.29.0 ""
  e2e "unchanged pin"                     0 2.28.3 ""
  e2e "bad bump, but drop is authorised"  0 2.29.0 "" "2.28.3"

  if [ "$fails" -ne 0 ]; then
    echo "==> Self-test FAILED: the gate does not behave as specified" >&2
    exit 1
  fi
  echo "==> Self-test passed (decision function AND end-to-end)"
  exit 0
fi

cd "$ROOT"

current="$(cat "$REL")"
IFS='|' read -r target compat <<<"$(read_vars_from_content "$current")"

if [ -z "$target" ]; then
  echo "TIMESCALEDB_VERSION is empty or unset in ${REL}" >&2
  exit 1
fi
echo "==> current:  TIMESCALEDB_VERSION=${target} TIMESCALEDB_COMPAT_VERSIONS='${compat}'"

# Is the history actually available? A shallow clone (CI's default is depth 1)
# cannot resolve HEAD~1, and the wrong response is to shrug and pass — that is
# precisely how this gate would become decorative.
if ! git rev-parse --verify "${BASE_REF}" >/dev/null 2>&1; then
  echo "cannot resolve ${BASE_REF} — the checkout has no history." >&2
  echo "This gate compares against the previously committed pin, so it needs one." >&2
  echo "In CI, set: actions/checkout with fetch-depth: 2 (or more)." >&2
  exit 1
fi

base_path="$REL"
if ! git cat-file -e "${BASE_REF}:${REL}" 2>/dev/null; then
  # Not necessarily a new file — the directory may have been renamed, and a
  # rename must not be a way to disarm the gate on the very commit that renames.
  moved="$(git ls-tree -r --name-only "${BASE_REF}" \
            | grep -E '^deploy/images/[^/]+/versions\.env$' | head -n1 || true)"
  if [ -n "$moved" ]; then
    echo "==> ${REL} is new at ${BASE_REF}, but found ${moved} — treating it as a rename."
    base_path="$moved"
  else
    echo "==> No operand versions.conf exists at ${BASE_REF} — first commit of the image."
    echo "==> Nothing has been published from an older pin, so no compatibility library is owed."
    exit 0
  fi
fi

previous_content="$(git show "${BASE_REF}:${base_path}")"
IFS='|' read -r prev_target prev_compat <<<"$(read_vars_from_content "$previous_content")"
echo "==> previous: TIMESCALEDB_VERSION=${prev_target} TIMESCALEDB_COMPAT_VERSIONS='${prev_compat}' (${BASE_REF}:${base_path})"

if [ -z "$prev_target" ]; then
  echo "could not read TIMESCALEDB_VERSION from ${BASE_REF}:${base_path}" >&2
  exit 1
fi

have="${target} ${compat}"
required="${prev_target} ${prev_compat}"
missing="$(missing_from "$have" "$required" "$ALLOW_DROP")"

if [ -z "$missing" ]; then
  if [ "$prev_target" = "$target" ]; then
    echo "==> OK: the pin is unchanged, and every previously shipped library is still carried."
  else
    echo "==> OK: bumped ${prev_target} -> ${target}, carrying: ${compat:-<none>}"
  fi
  [ -n "$ALLOW_DROP" ] && echo "==> NOTE: deliberately retired this commit: ${ALLOW_DROP}"
  exit 0
fi

cat >&2 <<EOF

==> COMPATIBILITY GATE FAILED

This image would stop shipping TimescaleDB libraries that the previous one
carried:

    missing: ${missing}

    previously shipped: ${required}
    this commit ships:  ${have}

CloudNativePG updates replicas before the primary. A replica restarting onto
this image, whose database catalog is still at one of the versions above, would
look for a \$libdir/timescaledb-<version> that this image does not contain — so
it would fail to start, mid-rollout, on a cluster that was healthy.

Fix, in ${REL}:

    TIMESCALEDB_COMPAT_VERSIONS=${missing}${compat:+ ${compat}}

If a version is being retired ON PURPOSE, say so explicitly rather than letting
a bump drop it silently:

    DC_COMPAT_ALLOW_DROP="${missing}" ${0##*/}

Retire a version only once no cluster can still be running it. Note that CNPG
never runs \`ALTER EXTENSION timescaledb UPDATE\` for you, so a catalog stays on
the old version until a human has run it per database.

EOF
exit 1
