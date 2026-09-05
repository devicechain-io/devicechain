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
mutation_block() {
  awk '
    !inb && /type[[:space:]]+Mutation[[:space:]]*\{/ { inb = 1; d = 1; next }
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

# create_request_updates prints any update* mutation still taking a *CreateRequest — the
# full-replace shape this arc converted away from. It reads the mutation block, so a create
# mutation naming its own input is not a match.
create_request_updates() {
  mutation_block "$1" | grep -E '^[[:space:]]+update[A-Za-z0-9_]*[[:space:]]*\(' |
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

# sdl_guard_wired requires the SDL-default assertion to be called in a test file that also
# names THIS schema's embed variable. Two separate greps would accept a service that wires
# the guard for one of its schemas and not the other, which is exactly the half-wiring a
# multi-schema service produces.
sdl_guard_wired() {
  local svcdir="$1" var="$2" f
  while read -r f; do
    [ -n "$f" ] || continue
    if grep -qE "SDL:[[:space:]]*$var([^A-Za-z0-9_]|\$)" "$f"; then
      return 0
    fi
  done < <(grep -rl 'AssertNoUpdateInputCarriesAnSDLDefault' "$svcdir" --include='*_test.go' 2>/dev/null || true)
  return 1
}

# surface_guard_wired requires the exhaustiveness guard somewhere in the module. It is a
# per-Api claim rather than a per-schema one — user-management wires it twice, for its
# admin Service and its identity Manager — so the module is the right granularity for a
# check that deliberately does not know which Api backs which schema.
surface_guard_wired() {
  grep -rq 'AssertEveryUpdateTakesADedicatedRequest' "$1" --include='*_test.go' 2>/dev/null
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

  # Each defect is planted ALONE. A self-test that plants several at once is satisfied by
  # a checker that finds any one of them.

  # 1. 🔴 THE HOLE THIS GUARD EXISTS FOR: a whole new service with an update mutation and
  #    no partial-update test of any kind. No per-service guard can see this, because the
  #    observation is that the module calls nothing.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  mkdir -p "$tmp/t/services/widget-registry/graphql"
  cat >"$tmp/t/services/widget-registry/graphql/schema.graphql" <<'EOF'
type Mutation {
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

  # 4. The exhaustiveness guard deleted from a service that serves updates.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  sed -i 's/AssertEveryUpdateTakesADedicatedRequest/assertRemoved/g' \
    "$tmp/t/services/dashboard-management/model/partial_update_guard_test.go"
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: a deleted exhaustiveness guard was not caught" >&2
    return 1
  fi
  echo "  ok   a deleted exhaustiveness guard is caught"

  # 5. An update mutation reverted to a shared create input — the shape the whole arc
  #    converted away from, and the one that reintroduces full-replace semantics.
  rm -rf "$tmp/t"; cp -r backend "$tmp/t"
  sed -i 's/updateDashboard(token: String!, request: DashboardUpdateRequest!/updateDashboard(token: String!, request: DashboardCreateRequest!/' \
    "$tmp/t/services/dashboard-management/graphql/schema.graphql"
  if check_tree "$tmp/t" >/dev/null 2>&1; then
    echo "🔴 self-test: an update* taking a *CreateRequest was not caught" >&2
    return 1
  fi
  echo "  ok   an update* mutation taking a *CreateRequest is caught"

  # 6. THE FLOOR ITSELF. Discovery that reads nothing must FAIL rather than report a clean
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
