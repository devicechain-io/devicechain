#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: every SERVED SCHEMA that declares an `update*` mutation has both partial-update
# guards wired in its own module.
#
# WHY THIS EXISTS, AND WHY IT CANNOT BE A GO TEST
#
# Two per-service guards protect the three-state update semantic — omitted leaves a field
# alone, an explicit null clears it, a value sets it:
#
#   AssertEveryUpdateTakesADedicatedRequest  (backend/core/rdb/partialupdatetest/guard.go)
#       reflects over one service's Api and requires every Update* method to take a
#       dedicated, registered, token-free, three-state request type.
#
#   AssertNoUpdateInputCarriesAnSDLDefault   (…/partialupdatetest/sdl_default.go)
#       walks one served SCHEMA and requires no field reachable from an update* mutation to
#       declare an SDL default. A default is packed as NON-NULL and seeded into the result
#       before packing, so an ABSENT field arrives with Set=true holding it, and every
#       update omitting the field WRITES it. That defect is only reachable ONCE AN INPUT IS
#       CONVERTED — a default against a pointer field fails schema construction outright —
#       so converting an input is what trades a compile-time refusal for a runtime one.
#
# 🔴 BOTH ARE INVISIBLE ON A SERVICE THAT WIRES NEITHER, AND THAT IS THE HOLE THIS CLOSES.
# A guard a module does not call reports nothing about that module. So a service added
# tomorrow with an `updateThing` mutation and no partial-update test at all has a green
# suite, a green `go test ./...`, and no coverage whatsoever — and nothing inside any
# module is positioned to notice, because the observation is about a module's ABSENCE.
# That is a question about the tree, so it is asked of the tree, once, here.
#
# WHAT IT DOES NOT DO. It does not check the guards' arguments — the anti-vacuity floors,
# the exemption maps, which Api a service reflects over. Those are per-service facts the
# per-service tests already state and fail on. This asks only the question they cannot:
# is the guard called at all, for this schema, in this module.
#
#   hack/check-partial-update-guards.sh              # check
#   hack/check-partial-update-guards.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 🔴 THE ANTI-VACUITY FLOOR, and it is the same floor the Go guards carry for the same
# reason: everything below loops over discovered schemas, and a loop over nothing reports
# success. A glob that stops matching — a directory renamed, a schema moved out of
# graphql/ — would otherwise turn this guard green on the day it stopped reading anything.
#
# It is set at the MEASURED count rather than safely under it. Seven served schemas declare
# an update* mutation today: device-management, notification-management,
# dashboard-management, outbound-connectors, ai-inference's admin plane, and both of
# user-management's (admin and tenant). A floor under the measurement cannot notice one
# disappearing, which is half of what it is for. Raise it when a schema gains an update
# surface; lower it only when one deliberately loses its last update mutation.
EXPECTED_UPDATE_SCHEMAS=7

# mutation_block prints the body of a schema's `type Mutation { … }`, by brace depth rather
# than by looking for a closing brace in column one — a schema that indents its type
# declarations would defeat the latter silently.
#
# The `implements` clause is accepted (`type Mutation implements Node {`) because a root
# written that way would otherwise be INVISIBLE: the service would be skipped rather than
# reported, which is the certified-by-omission failure this whole guard exists to remove.
# It costs one alternation, and the self-test's new-service plant uses that form so the
# branch is exercised rather than merely written.
mutation_block() {
  awk '
    !inb && /type[[:space:]]+Mutation([[:space:]]+implements[^{]*)?[[:space:]]*\{/ { inb = 1; d = 1; next }
    inb {
      o = gsub(/\{/, "{"); c = gsub(/\}/, "}")
      d += o - c
      if (d <= 0) { inb = 0; next }
      print
    }
  ' "$1"
}

# update_fields prints the names of the update* mutations one schema declares. Comment
# lines begin with # and are excluded by the pattern rather than by stripping them.
update_fields() {
  mutation_block "$1" |
    grep -oE '^[[:space:]]+update[A-Za-z0-9_]*[[:space:]]*[(:]' |
    sed -E 's/[[:space:]]*[(:]$//; s/^[[:space:]]+//' | sort -u
}

# mutation_signatures prints one line per mutation field, with a multi-line argument list
# JOINED back onto its field. Every signature on this platform fits one line today, and
# that is exactly why the join is here rather than a note saying so: a check whose reach
# depends on how a schema happens to be FORMATTED overstates itself, and the one thing this
# PR is about is a guard that claims more than it does. Reformatting
#
#	updateDashboard(
#	    token: String!
#	    request: DashboardCreateRequest!
#	): Dashboard!
#
# must not become a way past it.
#
# `#` comments are stripped first, so a parenthesis in prose cannot unbalance the depth
# count and swallow the rest of the block.
mutation_signatures() {
  mutation_block "$1" | awk '
    { sub(/#.*/, "") }
    { joined = (joined == "" ? $0 : joined " " $0)
      o = gsub(/\(/, "("); c = gsub(/\)/, ")")
      d += o - c
      if (d < 0) { d = 0 }
      if (d == 0) { print joined; joined = "" }
    }
    END { if (joined != "") print joined }
  '
}

# create_request_updates prints any update* mutation still taking a *CreateRequest — the
# full-replace shape this arc converted away from. It reads whole signatures, so neither a
# line break nor a create mutation naming its own input changes the answer.
create_request_updates() {
  mutation_signatures "$1" | grep -E '^[[:space:]]*update[A-Za-z0-9_]*[[:space:]]*\(' |
    grep 'CreateRequest' || true
}

# embed_var names the Go variable a schema is embedded into, read from the //go:embed
# directive in the same directory. It is resolved rather than guessed because a service may
# serve several schemas (ai-inference and user-management both do), and "the service wires
# the guard" is not the same claim as "the service wires the guard FOR THIS SCHEMA".
embed_var() {
  local dir="$1" base="$2" f var
  for f in "$dir"/*.go; do
    [ -e "$f" ] || continue
    var="$(awk -v want="$base" '
      $0 ~ "go:embed[[:space:]]+" want "[[:space:]]*$" { found = 1; next }
      found && /^var[[:space:]]+[A-Za-z_]/ { print $2; exit }
    ' "$f")"
    if [ -n "$var" ]; then
      echo "$var"
      return 0
    fi
  done
  return 1
}

# 🔴 EVERY "IS IT WIRED" QUESTION BELOW IS ASKED OF CODE, NEVER OF A DOC COMMENT, AND THAT
# DISTINCTION IS NOT PEDANTRY — IT WAS A LIVE HOLE IN THE FIRST VERSION OF THIS SCRIPT.
#
# Both checks used to be a plain grep for the assertion's NAME. Every module that wires
# these guards also documents them, in a sentence of the form "The guard itself is core's
# putest.AssertEveryUpdateTakesADedicatedRequest — it enumerates…". So replacing only the
# CALL — `putest.AssertEveryUpdateTakesADedicatedRequest(` → `putest.assertRemoved(` — left
# the doc comment behind, the grep matched it, and this guard reported the surface as
# guarded WITH THE GUARD REMOVED. The same held for the SDL side: commenting out one
# `SDL: SchemaContent` row of a multi-schema wiring left the Go suite green (the other row
# still ran) and left the commented text for the grep to find.
#
# The self-test could not see either, because its plant was STRONGER than the defect: it
# used `s/…/g`, which deletes the doc comment along with the call, so it certified a
# property this script did not have. A control built out of the thing under test cannot
# notice that thing moving — plant the WEAKEST mutant that should still be caught.
#
# So `//` and everything after it is stripped before matching, and a call is required to
# look like one (a `(` after the name). Stripping is deliberately crude — it also blanks a
# `//` inside a string literal — because every way of being wrong here is a FALSE NEGATIVE
# on the wiring, i.e. this guard failing loudly on a wired module, never passing an
# unwired one.
code_of() {
  sed -E 's@//.*@@' "$1"
}

# sdl_guard_wired requires the SDL-default assertion to be CALLED in a test file that also
# names THIS schema's embed variable, both on non-comment lines. Two independent greps
# would accept a service that wires the guard for one of its schemas and not the other,
# which is exactly the half-wiring a multi-schema service produces.
sdl_guard_wired() {
  local svcdir="$1" var="$2" f code
  while read -r f; do
    [ -n "$f" ] || continue
    code="$(code_of "$f")"
    printf '%s\n' "$code" | grep -qE 'AssertNoUpdateInputCarriesAnSDLDefault[[:space:]]*\(' || continue
    if printf '%s\n' "$code" | grep -qE "SDL:[[:space:]]*$var([^A-Za-z0-9_]|\$)"; then
      return 0
    fi
  done < <(grep -rl 'AssertNoUpdateInputCarriesAnSDLDefault' "$svcdir" --include='*_test.go' 2>/dev/null || true)
  return 1
}

# surface_guard_wired requires the exhaustiveness guard to be CALLED somewhere in the
# module. It is a per-Api claim rather than a per-schema one — user-management wires it
# twice, for its admin Service and its identity Manager — so the module is the right
# granularity for a check that deliberately does not know which Api backs which schema.
surface_guard_wired() {
  local svcdir="$1" f
  while read -r f; do
    [ -n "$f" ] || continue
    if code_of "$f" | grep -qE 'AssertEveryUpdateTakesADedicatedRequest[[:space:]]*\('; then
      return 0
    fi
  done < <(grep -rl 'AssertEveryUpdateTakesADedicatedRequest' "$svcdir" --include='*_test.go' 2>/dev/null || true)
  return 1
}

check_tree() {
  local base="$1" found=0 failed=0
  local schema svcdir dir svc var

  for schema in "$base"/services/*/graphql/*.gql "$base"/services/*/graphql/*.graphql; do
    [ -e "$schema" ] || continue
    [ -n "$(update_fields "$schema")" ] || continue
    found=$((found + 1))

    dir="$(dirname "$schema")"
    svcdir="$(dirname "$dir")"
    svc="$(basename "$svcdir")"

    if ! var="$(embed_var "$dir" "$(basename "$schema")")"; then
      echo "  $svc/$(basename "$schema"): no //go:embed directive names it, so this guard" >&2
      echo "    cannot tell which variable a test would have to wire" >&2
      failed=1
      continue
    fi

    if ! sdl_guard_wired "$svcdir" "$var"; then
      echo "  $svc: $(basename "$schema") declares $(update_fields "$schema" | wc -l) update*" >&2
      echo "    mutation(s) and NOTHING calls putest.AssertNoUpdateInputCarriesAnSDLDefault" >&2
      echo "    with SDL: $var. An SDL default on any converted input would then make every" >&2
      echo "    update omitting that field WRITE the default, silently." >&2
      failed=1
    fi

    if ! surface_guard_wired "$svcdir"; then
      echo "  $svc: serves update* mutations and NOTHING in the module calls" >&2
      echo "    putest.AssertEveryUpdateTakesADedicatedRequest, so no test asks whether its" >&2
      echo "    updates take dedicated, token-free, three-state request types." >&2
      failed=1
    fi

    if [ -n "$(create_request_updates "$schema")" ]; then
      echo "  $svc: $(basename "$schema") has an update* mutation taking a *CreateRequest:" >&2
      create_request_updates "$schema" | sed 's/^/    /' >&2
      echo "    A shared create input can express only two of the three states, so every" >&2
      echo "    update through it is a full replace." >&2
      failed=1
    fi
  done

  if [ "$found" -lt "$EXPECTED_UPDATE_SCHEMAS" ]; then
    echo "  only $found served schema(s) with an update* mutation were found, and at least" >&2
    echo "  $EXPECTED_UPDATE_SCHEMAS are known to exist. This guard has stopped reading the" >&2
    echo "  surface it certifies — check the glob before lowering the floor." >&2
    failed=1
  fi

  return "$failed"
}

self_test() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # A clean tree must pass, or every failing case below proves nothing.
  cp -r backend "$tmp/clean"
  if ! check_tree "$tmp/clean" >/dev/null 2>&1; then
    echo "🔴 self-test: the real tree does not pass; every case below is meaningless" >&2
    check_tree "$tmp/clean" >&2 || true
    return 1
  fi
  echo "  ok   a clean tree passes"

  # Each defect is planted ALONE, and each is the WEAKEST mutant that should still be
  # caught. A plant stronger than the defect certifies a property the guard does not have:
  # case 4 below used to delete the doc comment along with the call, which is precisely how
  # this script shipped a version that a doc comment alone could satisfy.

  # 1. 🔴 THE HOLE THIS GUARD EXISTS FOR: a whole new service with an update mutation and
  #    no partial-update test of any kind. No per-service guard can see this, because the
  #    observation is that the module calls nothing.
  #
  #    The root is written `type Mutation implements …` deliberately: that form is what
  #    mutation_block's alternation exists for, and a plant using it means the branch is
  #    EXERCISED — if discovery could not see it, this service would be skipped, the check
  #    would pass, and this case would report the miss.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  mkdir -p "$tmp/t/services/widget-registry/graphql"
  cat >"$tmp/t/services/widget-registry/graphql/schema.graphql" <<'EOF'
type Mutation implements Node {
    createWidget(request: WidgetCreateRequest!): Widget!
    updateWidget(token: String!, request: WidgetUpdateRequest!): Widget!
}
EOF
  cat >"$tmp/t/services/widget-registry/graphql/schema.go" <<'EOF'
package graphql

//go:embed schema.graphql
var SchemaContent string
EOF
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a service wiring NEITHER guard was not caught" >&2
    return 1
  fi
  echo "  ok   a new service that wires neither guard is caught"

  # 2. The SDL-default guard deleted from a service that has one. This is the per-service
  #    regression, and it is the one a reviewer is least likely to notice: nothing fails.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  rm -f "$tmp/t/services/device-management/graphql/partial_update_sdl_default_test.go"
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a deleted SDL-default guard was not caught" >&2
    return 1
  fi
  echo "  ok   a deleted SDL-default guard is caught"

  # 3. HALF-WIRED, which is what a multi-schema service produces. The service still calls
  #    the assertion — for its OTHER schema — so a guard that grepped the module for the
  #    function name would pass here.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  sed -i 's/SDL: AdminSchemaContent/SDL: SchemaContent/' \
    "$tmp/t/services/user-management/graphql/partial_update_sdl_default_test.go"
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a schema wired under the wrong SDL variable was not caught" >&2
    return 1
  fi
  echo "  ok   a schema whose own SDL variable is not wired is caught"

  # 4. 🔴 THE CALL REMOVED AND THE DOC COMMENT LEFT BEHIND — the weakest mutant, and the
  #    one an earlier version of this script passed. Every module wiring these guards also
  #    documents them by name ("The guard itself is core's putest.Assert…"), so a plain
  #    grep for the name matches the prose after the call is gone. Only the `(` form is
  #    replaced here; line 14 of the target file still names the assertion in full.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  sed -i 's/putest\.AssertEveryUpdateTakesADedicatedRequest(/putest.assertRemoved(/' \
    "$tmp/t/services/dashboard-management/model/partial_update_guard_test.go"
  if ! grep -q 'AssertEveryUpdateTakesADedicatedRequest' \
    "$tmp/t/services/dashboard-management/model/partial_update_guard_test.go"; then
    echo "🔴 self-test: the plant removed the doc comment too, so it is stronger than the" >&2
    echo "   defect and proves nothing about a comment-only match" >&2
    return 1
  fi
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: an exhaustiveness guard whose CALL was removed, doc comment intact," >&2
    echo "   was not caught — a doc comment is satisfying this check" >&2
    return 1
  fi
  echo "  ok   a removed call is caught though the doc comment still names it"

  # 5. 🔴 THE SAME DEFECT ON THE SDL SIDE: one row of a multi-schema wiring COMMENTED OUT.
  #    The Go suite stays green — the admin row still runs — and the commented text is
  #    still there for a naive grep to find, while user-management's tenant plane and its
  #    updateProfile are guarded by nothing.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  sed -i 's@^\(\t*\)Name: "tenant", SDL: SchemaContent@\1// Name: "tenant", SDL: SchemaContent@' \
    "$tmp/t/services/user-management/graphql/partial_update_sdl_default_test.go"
  if ! grep -q 'SDL: SchemaContent' \
    "$tmp/t/services/user-management/graphql/partial_update_sdl_default_test.go"; then
    echo "🔴 self-test: the plant deleted the row rather than commenting it out, so it is" >&2
    echo "   stronger than the defect and proves nothing" >&2
    return 1
  fi
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a COMMENTED-OUT UpdateSchema row still counted as wiring" >&2
    return 1
  fi
  echo "  ok   a commented-out UpdateSchema row does not count as wiring"

  # 6. An update mutation reverted to a shared create input — the shape the whole arc
  #    converted away from, and the one that reintroduces full-replace semantics.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  sed -i 's/updateDashboard(token: String!, request: DashboardUpdateRequest!/updateDashboard(token: String!, request: DashboardCreateRequest!/' \
    "$tmp/t/services/dashboard-management/graphql/schema.graphql"
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: an update* taking a *CreateRequest was not caught" >&2
    return 1
  fi
  echo "  ok   an update* mutation taking a *CreateRequest is caught"

  # 7. THE SAME DEFECT SPREAD OVER SEVERAL LINES, which is what a reformat produces and
  #    what a line-shaped grep walks straight past. The check joins a signature before
  #    reading it precisely so its reach does not depend on formatting.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  python3 - "$tmp/t/services/dashboard-management/graphql/schema.graphql" <<'EOF'
import io, sys
p = sys.argv[1]
s = io.open(p, encoding="utf-8").read()
old = "    updateDashboard(token: String!, request: DashboardUpdateRequest!, expectedUpdatedAt: String): Dashboard!"
new = ("    updateDashboard(\n"
       "        token: String!\n"
       "        request: DashboardCreateRequest!\n"
       "        expectedUpdatedAt: String\n"
       "    ): Dashboard!")
assert old in s, "the self-test's reformat plant no longer matches the schema"
io.open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
EOF
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a *CreateRequest on its own line was not caught — the check is" >&2
    echo "   line-shaped and claims more than it does" >&2
    return 1
  fi
  echo "  ok   a *CreateRequest split across lines is caught"

  # 8. THE FLOOR ITSELF. Discovery that reads nothing must FAIL rather than report a clean
  #    sweep over zero schemas — the failure every loop-shaped guard has by default.
  rm -rf "$tmp/t"; mkdir -p "$tmp/t/services"
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a tree with no schemas at all passed" >&2
    return 1
  fi
  echo "  ok   discovery that finds nothing fails instead of passing"

  return 0
}

# A guard that cannot read its inputs must FAIL, not skip. "No findings" and "never ran"
# have to look different from each other.
if [ ! -d backend/services ]; then
  echo "🔴 backend/services is missing; this guard cannot report on a tree it cannot read" >&2
  exit 1
fi
if [ ! -f backend/core/rdb/partialupdatetest/sdl_default.go ]; then
  echo "🔴 backend/core/rdb/partialupdatetest/sdl_default.go is missing; the assertion this" >&2
  echo "   guard requires services to call does not exist, so every service would be" >&2
  echo "   reported as failing to call it — or, if it were renamed, none would." >&2
  exit 1
fi

if [ "${1:-}" = "--self-test" ]; then
  self_test
  echo "==> self-test passed: the guard fails on each defect, alone."
  exit 0
fi

echo "==> Checking every served schema with an update* mutation wires both partial-update guards."
if ! check_tree backend; then
  echo >&2
  echo "   See backend/core/rdb/partialupdatetest/{guard.go,sdl_default.go} for what each" >&2
  echo "   assertion is for, and backend/services/user-management/graphql/" >&2
  echo "   partial_update_sdl_default_test.go for a worked wiring." >&2
  exit 1
fi
echo "==> Every update* surface is guarded."
