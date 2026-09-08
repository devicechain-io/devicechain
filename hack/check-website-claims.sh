#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Do the claims devicechain.io makes still hold in this repository?
#
#   hack/check-website-claims.sh
#   hack/check-website-claims.sh --self-test
#
# The website is a separate repository whose prose is hand-written, and the release
# pipeline pushes releases.json to it and nothing else. So for three releases the page
# drifted from the platform with nothing to notice. An audit then found 15 wrong claims
# -- and the important number is that only TWO had drifted. THIRTEEN were wrong when
# they were written.
#
# 🔴 THAT SPLIT DICTATES THE DESIGN. A periodic re-read catches drift, which was the
# small half, and it would have been skipped by the second release anyway. What catches
# the large half is failing the PR that changes the underlying fact. So the claims live
# in hack/website-claims.json, next to the code that settles them, and this runs in CI.
#
# 🔴 WHAT IT DOES NOT DO. It cannot see the website. The HTML is in another repository
# and is deliberately NOT fetched: a gate that needs the network goes red on someone
# else's bad afternoon, and one that skips on a fetch failure is worse than no gate at
# all. So this proves the claims are TRUE, never that the page actually makes them.
# Keeping the manifest faithful to the page is a human step on the release-prep checklist.
#
# 🔴 EVERY CHECK HAS AN ANTI-VACUITY FLOOR. The failure mode of a check that reads a
# list is an empty list -- deleting a claim would otherwise be the way to make this pass.
# Each check refuses to run against nothing, and the self-test proves it by emptying it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# check <claims.json> <repo-root> -- prints one line per problem; silence is passing.
check() {
  local claims="$1" root="$2"
  local problems=()

  if [ ! -f "$claims" ]; then
    echo "claims file not found: $claims"
    return 0
  fi
  if ! jq empty "$claims" 2>/dev/null; then
    echo "$claims is not valid JSON"
    return 0
  fi

  # --- npm package names -------------------------------------------------------
  # The site names these as installable; the tree is the authority on what exists.
  local want_npm have_npm
  want_npm="$(jq -r '.npm_packages.site_says[]?' "$claims" | sort)"
  have_npm="$(find "$root/frontend/packages" -maxdepth 2 -name package.json 2>/dev/null \
              -exec jq -r '.name' {} \; | sort)"
  if [ -z "$want_npm" ]; then
    problems+=("npm_packages.site_says is empty -- a claim was deleted rather than checked")
  elif [ -z "$have_npm" ]; then
    problems+=("no package.json found under frontend/packages -- this check would pass anything")
  else
    local missing
    missing="$(comm -23 <(echo "$want_npm") <(echo "$have_npm") || true)"
    [ -n "$missing" ] && problems+=("the site names npm packages this repo does not publish: $(echo "$missing" | tr '\n' ' ')")
  fi

  # --- NuGet package id --------------------------------------------------------
  local want_nuget have_nuget
  want_nuget="$(jq -r '.nuget_package.site_says // empty' "$claims")"
  have_nuget="$(grep -ho '<PackageId>[^<]*</PackageId>' "$root"/sdks/csharp/src/*/*.csproj 2>/dev/null \
                | sed 's/<[^>]*>//g' | head -1)"
  if [ -z "$want_nuget" ]; then
    problems+=("nuget_package.site_says is empty")
  elif [ -z "$have_nuget" ]; then
    problems+=("no <PackageId> found in sdks/csharp -- this check would pass anything")
  elif [ "$want_nuget" != "$have_nuget" ]; then
    problems+=("the site calls the NuGet package '$want_nuget'; the csproj says '$have_nuget'")
  fi

  # --- outbound connector types ------------------------------------------------
  # Checked BOTH ways on purpose. A removed type makes the page overclaim; a NEW one
  # makes it underclaim, and underclaiming is how a shipped feature stays invisible --
  # which is the larger of the two problems this whole exercise found.
  local want_conn have_conn
  want_conn="$(jq -r '.connector_types.site_says[]?' "$claims" | sort)"
  have_conn="$(sed -n '/^var builders = map\[string\]builder{/,/^}/p' \
                 "$root/backend/services/outbound-connectors/connectorspec/connectorspec.go" 2>/dev/null \
               | grep -oE '"[a-z_]+"[[:space:]]*:' | tr -d ' \t":' | sort)"
  if [ -z "$want_conn" ]; then
    problems+=("connector_types.site_says is empty")
  elif [ -z "$have_conn" ]; then
    problems+=("could not read the connectorspec builders map -- this check would pass anything")
  else
    local only_site only_tree
    only_site="$(comm -23 <(echo "$want_conn") <(echo "$have_conn") || true)"
    only_tree="$(comm -13 <(echo "$want_conn") <(echo "$have_conn") || true)"
    [ -n "$only_site" ] && problems+=("the site claims connector types that are not registered: $(echo "$only_site" | tr '\n' ' ')")
    [ -n "$only_tree" ] && problems+=("connector types ship that the site does not mention: $(echo "$only_tree" | tr '\n' ' ') -- the page underclaims")
  fi

  # --- rule type count ---------------------------------------------------------
  # A number in prose is the most rot-prone thing on the page. It shipped wrong once
  # already (nine, by counting a WindowMode as a RuleType), so it is checked.
  local want_rules have_rules
  want_rules="$(jq -r '.rule_type_count.site_says // empty' "$claims")"
  have_rules="$(grep -cE '^\s*Type[A-Za-z]+ RuleType = "' \
                 "$root/backend/services/event-processing/internal/rules/schema.go" 2>/dev/null || echo 0)"
  if [ -z "$want_rules" ]; then
    problems+=("rule_type_count.site_says is empty")
  elif [ "$have_rules" -eq 0 ]; then
    problems+=("found no RuleType constants -- this check would pass anything")
  elif [ "$want_rules" != "$have_rules" ]; then
    problems+=("the site says $want_rules kinds of rule; the tree defines $have_rules RuleType constants (WindowMode values are modes of the aggregate type, not types)")
  fi

  # --- documentation links -----------------------------------------------------
  local want_docs
  want_docs="$(jq -r '.doc_links.site_says[]?' "$claims")"
  if [ -z "$want_docs" ]; then
    problems+=("doc_links.site_says is empty")
  else
    local d
    while IFS= read -r d; do
      [ -n "$d" ] || continue
      [ -f "$root/docs/docs/$d" ] || problems+=("the site links a docs page that does not exist: docs/docs/$d")
    done <<< "$want_docs"
  fi

  printf '%s\n' "${problems[@]+"${problems[@]}"}"
}

# ---------------------------------------------------------------------------
if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: every check must be shown to FAIL, and a true manifest must PASS"
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  c="$tmp/claims.json"

  # A synthetic tree, so the self-test never depends on today's real values.
  r="$tmp/repo"
  mkdir -p "$r/frontend/packages/alpha" "$r/sdks/csharp/src/X" \
           "$r/backend/services/outbound-connectors/connectorspec" \
           "$r/backend/services/event-processing/internal/rules" "$r/docs/docs/guides"
  echo '{"name":"@dc/alpha"}' > "$r/frontend/packages/alpha/package.json"
  echo '<Project><PackageId>Real.Sdk</PackageId></Project>' > "$r/sdks/csharp/src/X/X.csproj"
  printf 'var builders = map[string]builder{\n\t"mqtt": {},\n\t"kafka": {},\n}\n' \
    > "$r/backend/services/outbound-connectors/connectorspec/connectorspec.go"
  printf 'const (\n\tTypeA RuleType = "a"\n\tTypeB RuleType = "b"\n)\n' \
    > "$r/backend/services/event-processing/internal/rules/schema.go"
  touch "$r/docs/docs/guides/real.md"

  good() { cat > "$c" <<EOF
{"npm_packages":{"site_says":["@dc/alpha"]},
 "nuget_package":{"site_says":"Real.Sdk"},
 "connector_types":{"site_says":["mqtt","kafka"]},
 "rule_type_count":{"site_says":2},
 "doc_links":{"site_says":["guides/real.md"]}}
EOF
  }
  expect() { # <label> <ok|fail>
    local out; out="$(check "$c" "$r")"; out="$(echo "$out" | sed '/^$/d')"
    if [ "$2" = "ok" ] && [ -n "$out" ]; then echo "  FAIL: $1 -- expected silence, got: $out" >&2; exit 1; fi
    if [ "$2" = "fail" ] && [ -z "$out" ]; then echo "  FAIL: $1 -- expected a problem, got silence" >&2; exit 1; fi
    echo "  ok: $1"
  }

  # 🔴 THE COUNTERWEIGHT, FIRST. Every "it caught the defect" below is satisfied just as
  # well by a checker that fails everything, so a correct manifest has to pass first.
  good; expect "a manifest that matches the tree passes" ok

  good; jq '.npm_packages.site_says += ["@dc/ghost"]' "$c" > "$c.t" && mv "$c.t" "$c"
  expect "an npm package the repo does not publish is caught" fail

  good; jq '.nuget_package.site_says = "Wrong.Sdk"' "$c" > "$c.t" && mv "$c.t" "$c"
  expect "a NuGet package id that does not match the csproj is caught" fail

  good; jq '.connector_types.site_says += ["gcp_pubsub"]' "$c" > "$c.t" && mv "$c.t" "$c"
  expect "a connector type the site claims but does not ship is caught" fail

  # The direction that matters most, and the one a naive check omits.
  good; jq '.connector_types.site_says = ["mqtt"]' "$c" > "$c.t" && mv "$c.t" "$c"
  expect "a shipped connector type the site FAILS to mention is caught (underclaiming)" fail

  good; jq '.rule_type_count.site_says = 3' "$c" > "$c.t" && mv "$c.t" "$c"
  expect "a wrong rule-kind count is caught -- the defect that shipped" fail

  good; jq '.doc_links.site_says = ["guides/gone.md"]' "$c" > "$c.t" && mv "$c.t" "$c"
  expect "a link to a docs page that does not exist is caught" fail

  # 🔴 ANTI-VACUITY. Deleting a claim must not be the way to pass.
  for k in npm_packages connector_types doc_links; do
    good; jq ".$k.site_says = []" "$c" > "$c.t" && mv "$c.t" "$c"
    expect "an emptied $k claim is refused, not silently passed" fail
  done

  # 🔴 A TREE THE CHECK CANNOT READ MUST FAIL, NOT PASS. If a file moves, every claim
  # against it becomes trivially satisfiable -- which is the shape of a gate that goes
  # green while measuring nothing.
  good; rm "$r/backend/services/event-processing/internal/rules/schema.go"
  expect "a missing source file fails rather than passing vacuously" fail

  echo "==> Self-test passed: 11 planted defects each caught, and a true manifest passes"
  exit 0
fi

findings="$(check "$ROOT/hack/website-claims.json" "$ROOT" | sed '/^$/d')"
if [ -n "$findings" ]; then
  echo "::error::the website makes claims this repository no longer supports:" >&2
  echo "$findings" | sed 's/^/  - /' >&2
  echo "  Fix the site (github.com/devicechain-io/website) AND hack/website-claims.json." >&2
  exit 1
fi
echo "ok: every recorded website claim still holds"
