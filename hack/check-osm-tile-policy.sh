#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: the console must keep sending a Referer when it fetches map tiles.
#
# WHY. DeviceChain ships the OpenStreetMap standard tile layer as its default
# basemap (settings.DefaultBasemapJSON). The OSMF tile usage policy asks a
# browser client for a valid HTTP Referer identifying the site, tells you not to
# set a restrictive Referrer-Policy that removes it, and reserves the right to
# block access without prior notice where usage degrades the service.
#
# 🔴 THE FAILURE THIS EXISTS FOR IS SILENT AND REMOTE. Adding
# `Referrer-Policy: no-referrer` is a one-line, obviously-correct-looking
# security hardening — it is on every header-hardening checklist, and nothing
# in this repo would fail because of it. Every test stays green, the console
# still builds, tiles still render in dev. The consequence lands weeks later,
# on somebody else's install, as "the map went blank", with no local signal
# connecting it to the commit. The whole point of a gate here is to put the
# argument in front of the person writing that line, at the moment they write
# it, rather than leaving it in a comment nobody greps for.
#
# It is NOT a ban on the header. It is a ban on setting it without deciding
# what happens to the default basemap. A deployment that has moved to a keyed
# provider may want it; the OSM default and this header cannot both be right,
# and that is a choice to make deliberately.
#
# Two ways to strip a Referer, and the guard covers both: the response header,
# and the `<meta name="referrer">` tag — which is on the same checklists, does
# the same thing, and lives in a file nobody thinks of as deployment config.
#
# The policy's other standing obligation — never pre-seed, bulk-fetch or
# archive tiles — is not mechanically checkable and is deliberately not
# attempted here. There is nothing to grep for; the trigger is a FEATURE
# ("download this area for offline use") whose reviewer needs to know the rule
# rather than a string a script can find. It lives in the comment on
# settings.KeyBasemapDefault, next to the default it constrains.
#
#   hack/check-osm-tile-policy.sh              # check
#   hack/check-osm-tile-policy.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Everything that can put a response header in front of the console: the nginx
# config the frontend image serves, and the chart that fronts it (an Ingress
# annotation is just as capable of stripping the Referer).
#
# 🔴 These are REQUIRED to exist. A guard that silently skips a missing target
# is the shape that reports "clean" about a tree it never read — rename
# frontend/deploy in a build refactor and this would pass forever, including on
# the commit that adds the header to the moved file.
HEADER_TARGETS=(frontend/deploy deploy/helm)

# Case-insensitive and separator-optional on purpose: `REFERRER-POLICY` is a
# valid nginx header name, and `referrerPolicy` is what a Traefik headers
# middleware calls the same field.
HEADER_PATTERN='referrer[-_]?policy'

# The meta-tag route, in the HTML entry point of each frontend app.
META_GLOB='frontend/apps/*/index.html'
META_PATTERN='<meta[^>]+name=["'"'"']?referrer'

# scan prints every hit under $1 (a tree root), and FAILS if a required target
# is missing rather than quietly narrowing what it looked at.
scan() {
  local root="$1" dirs=() d missing=()
  for d in "${HEADER_TARGETS[@]}"; do
    if [ -d "$root/$d" ]; then dirs+=("$root/$d"); else missing+=("$d"); fi
  done
  if [ ${#missing[@]} -gt 0 ]; then
    echo "MISSING TARGET: ${missing[*]} — this guard cannot see what it claims to check" >&2
    return 2
  fi
  # grep exits 1 on no matches, which is the PASSING case, so `|| true` is
  # load-bearing under `set -e` rather than sloppiness.
  grep -rniE "$HEADER_PATTERN" "${dirs[@]}" 2>/dev/null || true

  # The meta tag. A glob that matches nothing is also a silent narrowing, so an
  # app with no index.html is worth knowing about — but it is not fatal here,
  # because an app can legitimately be removed.
  local f
  for f in "$root"/$META_GLOB; do
    [ -f "$f" ] || continue
    grep -niE "$META_PATTERN" "$f" 2>/dev/null | sed "s|^|$f:|" || true
  done
}

report() {
  local hits="$1"
  [ -z "$hits" ] && return 0
  echo "The Referer the default basemap depends on is being stripped:" >&2
  echo >&2
  echo "$hits" >&2
  cat >&2 <<'EOF'

The default basemap is the OpenStreetMap standard tile layer, and the OSMF tile
usage policy asks browser clients for a valid Referer. Removing it — with a
Referrer-Policy header or a <meta name="referrer"> tag — can get an instance
blocked with no notice, and the map goes blank on installs you cannot see, with
nothing here to connect it to this change.

If you need this, change the default basemap in the same commit:
  backend/services/user-management/settings/model.go (DefaultBasemapJSON)

`strict-origin-when-cross-origin` still sends an origin-only Referer and is the
usual middle ground, but it is a decision to make, not a default to inherit.
EOF
  return 1
}

# --self-test: the negative control. A guard that has never been shown to FAIL
# is indistinguishable from a guard that cannot fail.
#
# 🔴 It runs against a temp tree, which means it proves the PATTERN fires — not
# that the real invocation looks anywhere real. That second half is what the
# missing-target check above is for, and the third case below exercises it.
if [ "${1:-}" = "--self-test" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/frontend/deploy" "$tmp/deploy/helm" "$tmp/frontend/apps/console"

  fail() { echo "SELF-TEST FAILED: $1" >&2; exit 1; }

  # 1. The response header, in the casing a linter would not flag.
  printf 'server {\n  add_header REFERRER-POLICY "no-referrer" always;\n}\n' \
    >"$tmp/frontend/deploy/nginx.conf"
  [ -n "$(scan "$tmp")" ] || fail "did not flag a planted Referrer-Policy header"
  : >"$tmp/frontend/deploy/nginx.conf"

  # 2. The meta tag, which no amount of header checking would catch.
  printf '<html><head><meta name="referrer" content="no-referrer"></head></html>\n' \
    >"$tmp/frontend/apps/console/index.html"
  [ -n "$(scan "$tmp")" ] || fail "did not flag a planted <meta name=referrer> tag"
  : >"$tmp/frontend/apps/console/index.html"

  # 3. The counterweight: a clean tree must come back clean, or "always fails"
  #    would satisfy both cases above just as well.
  printf 'server {\n  add_header Cache-Control "no-cache";\n}\n' \
    >"$tmp/frontend/deploy/nginx.conf"
  [ -z "$(scan "$tmp")" ] || fail "flagged a tree with neither the header nor the tag"

  # 4. A missing target must be an ERROR, not a quiet pass. This is the case
  #    that separates "the check is clean" from "the check looked nowhere".
  rm -rf "$tmp/deploy/helm"
  if scan "$tmp" >/dev/null 2>&1; then
    fail "reported success with a required target directory missing"
  fi

  echo "self-test passed: fires on a header, fires on a meta tag, quiet on a clean tree, errors on a missing target"
  exit 0
fi

report "$(scan "$ROOT")"
echo "no Referrer-Policy header or referrer meta tag is stripping the Referer"
