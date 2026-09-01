#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: no ADR references in published documentation.
#
# WHY. ADRs live in `.agent-os/`, a gitignored symlink to the private strategy
# repo. They are the right citation in SOURCE — a maintainer reading a comment
# can look one up, and CLAUDE.md establishes that convention deliberately. They
# are the wrong citation in the DOCS SITE, where the reader is a user who has no
# access to them: "(ADR-025)" is a dead reference dressed as a source, and it
# quietly advertises that a private repo exists.
#
# The line this draws:
#
#   Rendered documentation   docs/docs, docs/i18n, docs/blog, docs/src  -> NO ADR refs
#   Draft feature docs       docs-drafts/**/*.md, body only            -> NO ADR refs
#   npm registry metadata    frontend/packages/*/package.json          -> NO ADR refs
#   Helm listing prose       deploy/helm/*/{Chart.yaml,README.md}      -> NO ADR refs
#   NuGet registry metadata  sdks/**/*.csproj <Description>/<PackageTags> -> NO ADR refs
#   NuGet package readme     the file a csproj names as its <PackageReadmeFile>,
#                            scanned whole -> NO ADR refs
#   Published GraphQL SDL    the schemas served at /schema/  -> NO ADR refs, CHECKED ELSEWHERE
#   Source and maintainer    everything else, incl. docusaurus.config.ts -> ADR refs fine
#
# The distinguishing question is not "is this file public?" — the whole repo is
# public. It is "does a READER of the published site see this?". Each row above
# is a place where a stranger is SERVED this prose by some other host — the docs
# site, npmjs.com, Artifact Hub, nuget.org — with no way to follow an ADR
# citation.
#
# 🔴 ONE COVERED SURFACE IS NOT CHECKED IN THIS FILE, deliberately. The GraphQL
# schemas under backend/services/*/graphql/ are published verbatim at
# docs.devicechain.io/schema/, which makes them a served surface by exactly the
# test above. But WHICH of them are published is decided by
# docs/scripts/schemas.manifest.mjs — one of the fourteen carries `publish:
# false` and is never served — and bash cannot read that inventory without a
# second, hand-maintained copy of it. So the check lives beside the manifest, in
# docs/scripts/schema-publish.test.mjs, driven by the same authority that decides
# what gets published; the docs CI job runs it alongside this guard, as its
# "Schema publishing self-tests" step. It is named here rather than left
# implicit because two guards that each look complete, while one has a hole, is
# precisely how a citation gets back in.
#
#   hack/check-docs-adr-refs.sh              # check
#   hack/check-docs-adr-refs.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Rendered content only. docs/build is generated output and is not checked —
# it is a copy of what these sources already say.
TARGETS=(docs/docs docs/i18n docs/blog docs/src)
PATTERN='ADR-[0-9]{3}'

scan() {
  local dirs=() d
  for d in "$@"; do [ -d "$d" ] && dirs+=("$d"); done
  [ ${#dirs[@]} -eq 0 ] && return 0
  # grep exits 1 when there are no matches, which is the PASSING case here, so
  # the `|| true` is load-bearing under `set -e` rather than sloppiness.
  grep -rnE "$PATTERN" "${dirs[@]}" 2>/dev/null || true
}

# docs-drafts/ holds feature documents for arcs that have reached their
# GA-claimable state. They are drafts of published pages, so they are written to
# the docs site's rules from the first sentence rather than converted later — a
# draft written in the strategy repo's house style cites ADRs freely, and moving
# it into docs/ is then a rewrite rather than a `git mv`.
#
# THE FRONTMATTER IS A DELIBERATE EXCEPTION and the only one. A draft's ADR
# pointers are worth keeping with it during the holding period; putting them in
# one machine-strippable block keeps the BODY publishable as-is. So the body is
# checked exactly as a published page is, and the leading `---` … `---` block is
# blanked before the scan — blanked rather than deleted, so the line numbers in a
# failure still address the file as the author sees it.
scan_drafts() {
  local base="${1:-docs-drafts}" f
  [ -d "$base" ] || return 0
  while IFS= read -r -d '' f; do
    awk 'NR==1 && $0=="---" { fm=1; print ""; next }
         fm==1 && $0=="---" { fm=0; print ""; next }
         fm==1              { print ""; next }
                            { print }' "$f" |
      grep -nE "$PATTERN" | sed "s|^|${f}:|" || true
  done < <(find "$base" -type f -name '*.md' -print0)
}

# The other published surface, and a much easier one to miss: the `description`
# of an npm package renders on npmjs.com. frontend/packages/* are slated for
# publication to the public registry, so their descriptions are read by people
# who have never seen this repository, let alone the private one.
# Takes the packages root so the self-test can point it somewhere harmless
# rather than at the real tree.
scan_package_descriptions() {
  local base="${1:-frontend/packages}" f
  for f in "$base"/*/package.json; do
    [ -f "$f" ] || continue
    grep -nE "\"(description|keywords)\".*${PATTERN}" "$f" 2>/dev/null | sed "s|^|${f}:|" || true
  done
}

# The third published surface: the Helm chart's LISTING on Artifact Hub, which
# ingests oci://ghcr.io/devicechain-io/charts (pushed by the release workflow).
# Artifact Hub renders the packaged chart's `README.md` as the package page and
# the `Chart.yaml` metadata as its header — so both are read by people who have
# never seen this repository, exactly like an npm description.
#
# 🔴 SCOPE, stated so the guard's existence is not mistaken for a broader claim:
# this covers the chart's PROSE only. `values.yaml`, `values.schema.json` and
# `templates/` are also packaged, and Artifact Hub does expose them under its
# Values and Templates tabs — but those are chart SOURCE, browsed the same way
# this public repo is browsed, and CLAUDE.md's table deliberately keeps ADR
# citations in `deploy/`. They carry ~90 references today and are NOT checked.
# If that line ever moves, it moves as a decision, not by widening this glob.
scan_chart_listing() {
  local base="${1:-deploy/helm}" d f
  for d in "$base"/*/; do
    d="${d%/}"
    [ -f "$d/Chart.yaml" ] || continue
    for f in "$d/Chart.yaml" "$d/README.md"; do
      [ -f "$f" ] || continue
      grep -nE "$PATTERN" "$f" 2>/dev/null | sed "s|^|${f}:|" || true
    done
  done
}

# The fourth published surface, and the one this guard was missing: a NuGet
# package's <Description> and <PackageTags> render on nuget.org exactly as an
# npm `description` renders on npmjs.com. Same shape, same reader — someone who
# has never seen this repository — so the same rule applies.
#
# It was found the way these usually are: the C# SDK's own Description carried
# "(ADR-035)" while every npm description was clean, because npm had a guard and
# NuGet did not. The packages are not published yet, which is exactly why this is
# worth closing now rather than after a prefix reservation turns it into a public
# page.
#
# 🔴 SCOPE, drawn on the same line as the chart rule: this covers the PACKAGE
# METADATA a registry renders, not the project file as a whole. A comment on a
# <PackageReference> explaining why a dependency is pinned is source, is browsed
# the way this repo is browsed, and stays free to cite an ADR.
scan_nuget_metadata() {
  local base="${1:-sdks}" f
  [ -d "$base" ] || return 0
  while IFS= read -r -d '' f; do
    grep -nE "<(Description|PackageTags)>[^<]*${PATTERN}" "$f" 2>/dev/null | sed "s|^|${f}:|" || true
    scan_nuget_readme "$f"
  done < <(find "$base" -type f -name '*.csproj' -print0)
}

# A <PackageReadmeFile> is the LARGEST published surface a csproj has: nuget.org
# renders it as the package page's body, above the fold, where the <Description>
# is one line in the sidebar. It reaches the same stranger under the same rule.
#
# Scanned WHOLE, unlike the csproj that names it. A csproj is mostly source and
# only two of its elements are registry-rendered; a readme is registry-rendered
# prose from the first line to the last. That includes its HTML comments, which
# a renderer drops — the stricter line is deliberate, because "which parts of my
# readme does the reader see" is not a question worth being clever about.
#
# 🔴 This resolves the property LITERALLY, against the csproj's own directory. A
# project that packs some other file under this name (`<None ... PackagePath>`)
# would send it looking for something that is not there — so a missing file is
# reported by check_nuget_readmes_exist below, never passed over as clean.
scan_nuget_readme() {
  local csproj="$1" readme path
  readme="$(sed -n 's|.*<PackageReadmeFile>[[:space:]]*\([^<]*[^<[:space:]]\)[[:space:]]*</PackageReadmeFile>.*|\1|p' "$csproj" | head -1)"
  [ -n "$readme" ] || return 0
  path="$(dirname "$csproj")/$readme"
  [ -f "$path" ] || return 0   # reported as its own failure, not as an ADR hit
  grep -nE "$PATTERN" "$path" 2>/dev/null | sed "s|^|${path}:|" || true
}

# The counterweight to scanning a path taken from a file: if the path is wrong,
# the scan above reads exactly like a clean one. Nothing else in CI opens this
# property — the csharp job restores, builds and tests but never packs — so a
# dangling PackageReadmeFile would otherwise survive until a release run tried
# to publish and failed there.
check_nuget_readmes_exist() {
  local base="${1:-sdks}" f readme path
  [ -d "$base" ] || return 0
  while IFS= read -r -d '' f; do
    readme="$(sed -n 's|.*<PackageReadmeFile>[[:space:]]*\([^<]*[^<[:space:]]\)[[:space:]]*</PackageReadmeFile>.*|\1|p' "$f" | head -1)"
    [ -n "$readme" ] || continue
    path="$(dirname "$f")/$readme"
    [ -f "$path" ] || echo "${f}: <PackageReadmeFile> names '${readme}', but ${path} does not exist"
  done < <(find "$base" -type f -name '*.csproj' -print0)
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report a planted reference"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/docs"
  printf 'Secured at the broker (ADR-025).\n' > "$tmp/docs/planted.md"

  planted="$(scan "$tmp/docs")"
  if [ -z "$planted" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference — it is not checking anything" >&2
    exit 1
  fi
  echo "  ok: planted reference detected"

  printf 'Secured at the broker.\n' > "$tmp/docs/planted.md"
  if [ -n "$(scan "$tmp/docs")" ]; then
    echo "  FAIL: the guard reports a match in clean content — it would fail every build" >&2
    exit 1
  fi
  echo "  ok: clean content produces no match"

  mkdir -p "$tmp/packages/example"
  printf '{ "description": "A thing (ADR-039)." }\n' > "$tmp/packages/example/package.json"
  if [ -z "$(scan_package_descriptions "$tmp/packages")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in an npm description" >&2
    exit 1
  fi
  echo "  ok: planted npm description detected"

  printf '{ "description": "A thing." }\n' > "$tmp/packages/example/package.json"
  if [ -n "$(scan_package_descriptions "$tmp/packages")" ]; then
    echo "  FAIL: the guard reports a match in a clean npm description" >&2
    exit 1
  fi
  echo "  ok: clean npm description produces no match"

  # The draft exemption is the one rule here that makes the guard WEAKER, so it
  # gets both controls: the body must still be caught, and the frontmatter must
  # still be let through. Checking only the second would pass with the whole
  # file exempted.
  mkdir -p "$tmp/drafts"
  printf -- '---\nadrs: [ADR-077]\n---\n\nThe token is released on completion.\n' \
    > "$tmp/drafts/example.md"
  if [ -n "$(scan_drafts "$tmp/drafts")" ]; then
    echo "  FAIL: the guard reports a match in a draft's frontmatter, which is the one" >&2
    echo "        place a draft is allowed to name an ADR" >&2
    exit 1
  fi
  echo "  ok: a draft's frontmatter ADR list is exempt"

  printf -- '---\nadrs: [ADR-077]\n---\n\nThe token is released (ADR-077).\n' \
    > "$tmp/drafts/example.md"
  planted="$(scan_drafts "$tmp/drafts")"
  if [ -z "$planted" ]; then
    echo "  FAIL: the guard did not see an ADR reference in a draft's BODY — the" >&2
    echo "        frontmatter exemption is swallowing the whole file" >&2
    exit 1
  fi
  case "$planted" in
    *:5:*) echo "  ok: planted draft body reference detected, at its real line number" ;;
    *)     echo "  FAIL: draft body reference reported at the wrong line: $planted" >&2; exit 1 ;;
  esac

  # The chart listing is TWO files, and the failure that matters is covering one
  # and not the other — a guard that only ever sees README.md would pass a
  # Chart.yaml description full of ADR refs, which is the string Artifact Hub
  # puts on the search-results card. So each file gets its own planted case,
  # with the other held clean, rather than one case planting in both.
  mkdir -p "$tmp/helm/example"
  clean_chart() {
    printf 'apiVersion: v2\nname: example\ndescription: A thing.\n' > "$tmp/helm/example/Chart.yaml"
    printf '# Example\n\nRenders a thing.\n' > "$tmp/helm/example/README.md"
  }

  clean_chart
  if [ -n "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard reports a match in a clean chart listing" >&2
    exit 1
  fi
  echo "  ok: clean chart listing produces no match"

  clean_chart
  printf 'apiVersion: v2\nname: example\ndescription: A thing (ADR-022).\n' \
    > "$tmp/helm/example/Chart.yaml"
  if [ -z "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in a chart description" >&2
    exit 1
  fi
  echo "  ok: planted chart description detected"

  clean_chart
  printf '# Example\n\nRenders a thing (ADR-022).\n' > "$tmp/helm/example/README.md"
  if [ -z "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in a chart README —" >&2
    echo "        that file IS the Artifact Hub package page" >&2
    exit 1
  fi
  echo "  ok: planted chart README reference detected"

  # A directory with a README but no Chart.yaml is not a chart, and scanning it
  # would quietly widen the guard past the surface it documents.
  mkdir -p "$tmp/helm/notachart"
  printf '# Notes\n\nSee ADR-022.\n' > "$tmp/helm/notachart/README.md"
  clean_chart
  if [ -n "$(scan_chart_listing "$tmp/helm")" ]; then
    echo "  FAIL: the guard scanned a directory that has no Chart.yaml" >&2
    exit 1
  fi
  echo "  ok: a non-chart directory is not scanned"

  # NuGet metadata gets both directions, and one extra case that matters more
  # here than elsewhere: a csproj is mostly SOURCE, so a guard that scanned the
  # whole file would flag the pin comments this repo deliberately writes. The
  # third case holds an ADR reference in a comment and requires it to pass.
  mkdir -p "$tmp/sdks/example"
  printf '<Project><PropertyGroup><Description>A thing.</Description></PropertyGroup></Project>\n' \
    > "$tmp/sdks/example/Example.csproj"
  if [ -n "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the guard reports a match in a clean NuGet description" >&2
    exit 1
  fi
  echo "  ok: clean NuGet description produces no match"

  printf '<Project><PropertyGroup><Description>A thing (ADR-035).</Description></PropertyGroup></Project>\n' \
    > "$tmp/sdks/example/Example.csproj"
  if [ -z "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the guard did not see a planted ADR reference in a NuGet description —" >&2
    echo "        that string is what nuget.org puts on the package page" >&2
    exit 1
  fi
  echo "  ok: planted NuGet description detected"

  printf '<Project>\n  <!-- Pinned deliberately (ADR-035). -->\n  <PropertyGroup><Description>A thing.</Description></PropertyGroup>\n</Project>\n' \
    > "$tmp/sdks/example/Example.csproj"
  if [ -n "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the guard flagged an ADR reference in a csproj COMMENT — that is source," >&2
    echo "        not registry metadata, and CLAUDE.md keeps such citations deliberately" >&2
    exit 1
  fi
  echo "  ok: an ADR reference in a csproj comment is left alone"

  # The package readme. Both directions, then the case that makes the other two
  # mean anything: a property pointing at a file that is not there must FAIL, not
  # read as clean. Without that third case the first two would still pass with the
  # resolution logic broken, because "no file" and "clean file" produce the same
  # empty output.
  printf '<Project><PropertyGroup><PackageReadmeFile>PACKAGE.md</PackageReadmeFile></PropertyGroup></Project>\n' \
    > "$tmp/sdks/example/Example.csproj"
  printf 'The DeviceChain .NET client SDK.\n' > "$tmp/sdks/example/PACKAGE.md"
  if [ -n "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the guard reports a match in a clean package readme" >&2
    exit 1
  fi
  echo "  ok: clean package readme produces no match"

  printf 'The SDK speaks the documented wire seams (ADR-035).\n' > "$tmp/sdks/example/PACKAGE.md"
  if [ -z "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the guard did not follow <PackageReadmeFile> to the readme — that file" >&2
    echo "        is the BODY of the package page on nuget.org, the largest surface here" >&2
    exit 1
  fi
  echo "  ok: planted reference in a package readme detected"

  # An HTML comment is dropped by the renderer, and is scanned anyway. Pinning it
  # so the stricter-than-necessary line is a decision someone can find, rather
  # than something a later "fix" quietly relaxes.
  printf -- '<!-- Internal note (ADR-035). -->\n\nThe DeviceChain .NET client SDK.\n' \
    > "$tmp/sdks/example/PACKAGE.md"
  if [ -z "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the guard skipped an HTML comment in a package readme — a readme is" >&2
    echo "        scanned whole, deliberately, unlike the csproj that names it" >&2
    exit 1
  fi
  echo "  ok: a package readme is scanned whole, comments included"

  if [ -n "$(check_nuget_readmes_exist "$tmp/sdks")" ]; then
    echo "  FAIL: the existence check fired on a readme that is present" >&2
    exit 1
  fi
  echo "  ok: a readme that exists is not reported as dangling"

  rm -f "$tmp/sdks/example/PACKAGE.md"
  if [ -n "$(scan_nuget_metadata "$tmp/sdks")" ]; then
    echo "  FAIL: the ADR scan reported a hit for a readme that does not exist" >&2
    exit 1
  fi
  if [ -z "$(check_nuget_readmes_exist "$tmp/sdks")" ]; then
    echo "  FAIL: a <PackageReadmeFile> naming a missing file was not reported. The ADR" >&2
    echo "        scan over it is empty, which is indistinguishable from clean — this is" >&2
    echo "        the check that keeps the two apart" >&2
    exit 1
  fi
  echo "  ok: a <PackageReadmeFile> naming a missing file is reported"

  echo "==> Self-test passed"
  exit 0
fi

# Checked BEFORE the ADR sweep, and reported separately: a dangling readme path
# is not an ADR reference, and folding it into that block would explain it with
# advice about citations that has nothing to do with the problem.
dangling="$(check_nuget_readmes_exist)"
if [ -n "$dangling" ]; then
  cat >&2 <<EOF

==> A csproj names a package readme that is not there

$dangling

nuget.org renders this file as the package page. \`dotnet pack\` fails on a
missing one (NU5039), so this would surface at RELEASE time — and until then the
ADR sweep over that file reads as clean, because there is nothing to read.

Point <PackageReadmeFile> at a file that exists, relative to the csproj. If the
project packs it under a different name via <None ... PackagePath>, drop the
rename: this guard resolves the property literally, on purpose.

EOF
  exit 1
fi

hits="$(scan "${TARGETS[@]}")"
for extra in "$(scan_package_descriptions)" "$(scan_drafts)" "$(scan_chart_listing)" "$(scan_nuget_metadata)"; do
  [ -n "$extra" ] || continue
  hits="${hits:+$hits
}${extra}"
done

if [ -n "$hits" ]; then
  cat >&2 <<EOF

==> ADR references found in published documentation

$hits

ADRs live in the private strategy repo (.agent-os/), so a reader of the docs
site cannot follow these. Rewrite each one to say the thing itself, or link to
a public page that explains it:

  "secured at the broker (ADR-025)"  ->  "secured at the broker"
  "[ADR-026](../concepts/architecture.md)"
                                     ->  "[data lifecycle](../concepts/architecture.md)"

ADR citations remain correct in source comments and maintainer-facing files.
This guard covers only the surfaces a stranger is SERVED by another host:
${TARGETS[*]}, docs-drafts (body), frontend/packages/*/package.json
(description/keywords), deploy/helm/*/{Chart.yaml,README.md} — the prose
Artifact Hub renders as a chart's listing — and sdks/**/*.csproj
<Description>/<PackageTags> plus the whole of any file a csproj names as its
<PackageReadmeFile>, all of which nuget.org renders the same way. Chart
values/templates, and csproj comments, are source and are NOT covered.

One further served surface, the published GraphQL schemas, is covered by
docs/scripts/schema-publish.test.mjs instead of by this script — see the header
for why.

EOF
  exit 1
fi

echo "==> No ADR references in published documentation."
