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
# Three ways to strip a Referer, and the guard covers all of them: the response
# header; the `<meta name="referrer">` tag in an app's index.html — which is on
# the same checklists, does the same thing, and lives in a file nobody thinks of
# as deployment config; and the same tag (or a `referrerPolicy`) set at runtime
# from the frontend source.
#
# ONE deliberate exemption, and it is narrow on purpose. The single sign-on
# return pages (/login/sso/complete for the console, /dash/sso/complete for the
# standalone viewer) arrive with a one-time code in their address, so they
# forbid the Referer — nginx sends the header from an EXACT-MATCH location for
# each path, and each app's SSO module adds the meta tag at runtime. That is
# safe because such a document never renders a map: it redeems the code and
# leaves by a FULL navigation (window.location.replace), so the policy dies with
# it instead of following the person into the app. Both halves of that are
# pinned below, so the exemption cannot quietly grow:
#   - nginx: only a `location = <path>` block for exactly those two paths, in
#     frontend/deploy/nginx.conf. A prefix (`location /login/`), a regex, any
#     other exact path, a nested block, or the same block in any other file
#     still fails.
#   - runtime: only the two SSO modules named in META_EXEMPT_FILES, and only
#     while each still leaves its page with window.location.replace. The same
#     code in any other file — including a file of the same name elsewhere —
#     still fails.
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

# The frontend source the runtime route is looked for in. Required for the same
# reason as the header targets.
SOURCE_TARGETS=(frontend/apps frontend/packages)

# Case-insensitive and separator-optional on purpose: `REFERRER-POLICY` is a
# valid nginx header name, and `referrerPolicy` is what a Traefik headers
# middleware calls the same field.
HEADER_PATTERN='referrer[-_]?policy'

# The meta-tag route, in the HTML entry point of each frontend app.
META_GLOB='frontend/apps/*/index.html'
META_PATTERN='<meta[^>]+name=["'"'"']?referrer'

# The runtime route, in frontend source: the bare string 'referrer' (a meta
# tag's name, however it is set — `meta.name = 'referrer'`, setAttribute, JSX
# name="referrer"), or a referrerPolicy (on a fetch, an element, a header). A
# quote must sit directly before `referrer`, so `rel="noreferrer"` and the
# VALUE 'no-referrer' do not match on their own.
RUNTIME_PATTERN="[\"'\`]referrer[\"'\`]|$HEADER_PATTERN"

# The nginx file the exempt locations may live in, and the locations: exact
# matches (`=`) for the two SSO return paths, nothing else. Compared after
# whitespace is collapsed, so `location  =  /x` is the same block and
# `location =/x` (a different spelling nginx also accepts) is NOT exempt — the
# guard fails closed on it rather than parsing nginx's grammar in full.
NGINX_CONF='frontend/deploy/nginx.conf'
EXEMPT_LOCATIONS=('location = /login/sso/complete' 'location = /dash/sso/complete')

# The modules that add the meta tag to an SSO return page, and the call each
# must still contain for its exemption to hold: the full navigation that throws
# the no-Referer document away. Lose the call and the meta tag fails again.
META_EXEMPT_FILES=(frontend/apps/console/src/lib/sso.ts frontend/apps/dashboard/src/sso.ts)
META_EXEMPT_REQUIRES='window.location.replace('

# nginx_hits prints every Referrer-Policy line in nginx file $1 (shown as $2)
# whose innermost enclosing block is not an exempt location. A hit on the same
# line as a `{` is judged by the block AROUND it, so a one-line exempt block is
# flagged — fail closed rather than clever.
nginx_hits() {
  local exempt
  exempt="$(IFS='|'; echo "${EXEMPT_LOCATIONS[*]}")"
  awk -v pat="$HEADER_PATTERN" -v shown="$2" -v exempt="$exempt" '
    BEGIN {
      n = split(exempt, e, "|")
      for (i = 1; i <= n; i++) ok[e[i]] = 1
      depth = 0
    }
    {
      if (tolower($0) ~ pat) {
        h = depth > 0 ? hdr[depth] : ""
        if (!(h in ok)) print shown ":" FNR ":" $0
      }
      code = $0
      sub(/#.*/, "", code)
      for (i = 1; i <= length(code); i++) {
        c = substr(code, i, 1)
        if (c == "{") {
          s = seg
          gsub(/[ \t]+/, " ", s); sub(/^ /, "", s); sub(/ $/, "", s)
          hdr[++depth] = s; seg = ""
        } else if (c == "}") {
          if (depth > 0) depth--
          seg = ""
        } else if (c == ";") {
          seg = ""
        } else {
          seg = seg c
        }
      }
      seg = seg " "
    }
  ' "$1"
}

# scan prints every hit under $1 (a tree root), and FAILS if a required target
# is missing rather than quietly narrowing what it looked at.
scan() {
  local root="$1" dirs=() srcs=() d missing=()
  for d in "${HEADER_TARGETS[@]}"; do
    if [ -d "$root/$d" ]; then dirs+=("$root/$d"); else missing+=("$d"); fi
  done
  for d in "${SOURCE_TARGETS[@]}"; do
    if [ -d "$root/$d" ]; then srcs+=("$root/$d"); else missing+=("$d"); fi
  done
  if [ ${#missing[@]} -gt 0 ]; then
    echo "MISSING TARGET: ${missing[*]} — this guard cannot see what it claims to check" >&2
    return 2
  fi
  # grep exits 1 on no matches, which is the PASSING case, so `|| true` is
  # load-bearing under `set -e` rather than sloppiness. The one nginx file that
  # may hold the exempt locations is read by nginx_hits instead.
  grep -rniE "$HEADER_PATTERN" "${dirs[@]}" 2>/dev/null | grep -vF "$root/$NGINX_CONF:" || true
  if [ -f "$root/$NGINX_CONF" ]; then
    nginx_hits "$root/$NGINX_CONF" "$root/$NGINX_CONF"
  fi

  # The meta tag. A glob that matches nothing is also a silent narrowing, so an
  # app with no index.html is worth knowing about — but it is not fatal here,
  # because an app can legitimately be removed.
  local f
  for f in "$root"/$META_GLOB; do
    [ -f "$f" ] || continue
    grep -niE "$META_PATTERN" "$f" 2>/dev/null | sed "s|^|$f:|" || true
  done

  # The runtime route. Tests are skipped — they query for the tag to prove it is
  # there, and they do not ship — as are build output and dependencies.
  local hits line file rel exempt_ok
  hits="$(grep -rniE --include='*.ts' --include='*.tsx' --include='*.js' --include='*.jsx' \
    --include='*.mjs' --include='*.cjs' --include='*.html' \
    --exclude='*.test.*' --exclude='*.spec.*' \
    --exclude-dir=node_modules --exclude-dir=dist \
    "$RUNTIME_PATTERN" "${srcs[@]}" 2>/dev/null || true)"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    file="${line%%:*}"
    rel="${file#"$root"/}"
    exempt_ok=0
    for f in "${META_EXEMPT_FILES[@]}"; do
      if [ "$rel" = "$f" ] && grep -qF "$META_EXEMPT_REQUIRES" "$file"; then exempt_ok=1; fi
    done
    [ "$exempt_ok" = 1 ] || echo "$line"
  done <<<"$hits"
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
  mkdir -p "$tmp/frontend/deploy" "$tmp/deploy/helm" "$tmp/frontend/apps/console/src/lib" \
    "$tmp/frontend/apps/console/src/routes" "$tmp/frontend/apps/dashboard/src" "$tmp/frontend/packages/widgets/src"

  fail() { echo "SELF-TEST FAILED: $1" >&2; exit 1; }

  # nginx_conf writes a server block holding $1 as the nginx config under test.
  nginx_conf() {
    # shellcheck disable=SC2016 # $uri is nginx's variable, written literally
    printf 'server {\n  listen 80;\n%s\n  location / {\n    try_files $uri /index.html;\n  }\n}\n' "$1" \
      >"$tmp/frontend/deploy/nginx.conf"
  }
  HDR='    add_header Referrer-Policy "no-referrer" always;'
  # An SSO module as the exemption expects it: the meta tag AND the full navigation.
  SSO_MODULE="const meta = document.createElement('meta');
meta.name = 'referrer';
meta.content = 'no-referrer';
export function leave(dest: string) { window.location.replace(dest); }"

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

  # 4. The exemption, granted: both SSO return pages as exact-match locations,
  #    and both SSO modules with their full navigation. The whole real shape
  #    must come back clean, or the exemption is not the one documented above.
  nginx_conf "  location = /login/sso/complete {
$HDR
    try_files /index.html =404;
  }
  location  =  /dash/sso/complete {
$HDR
    try_files /dash/index.html =404;
  }"
  printf '%s\n' "$SSO_MODULE" >"$tmp/frontend/apps/console/src/lib/sso.ts"
  printf '%s\n' "$SSO_MODULE" >"$tmp/frontend/apps/dashboard/src/sso.ts"
  [ -z "$(scan "$tmp")" ] || fail "flagged the exempt SSO return locations or modules: $(scan "$tmp")"

  # 5. The exemption cannot be WIDENED. Each of these is one edit away from the
  #    real config and must fail on its own. A map route — the app's own pages,
  #    where tiles load — given the header:
  for loc in 'location = /alarms {' 'location = /dash/ {' 'location /dash/ {' \
    'location /login/ {' 'location /login/sso/complete {' 'location ~ ^/login/sso/complete {' \
    'location = /login/sso/complete/ {' 'location =/login/sso/complete {'; do
    nginx_conf "  $loc
$HDR
    try_files \$uri /index.html;
  }"
    [ -n "$(scan "$tmp")" ] || fail "did not flag the header in: $loc"
  done
  #    ...the header on the whole server, outside any location:
  nginx_conf "$HDR"
  [ -n "$(scan "$tmp")" ] || fail "did not flag the header at server level"
  #    ...a map route nested INSIDE an exempt location (the innermost block decides):
  nginx_conf "  location = /login/sso/complete {
    try_files /index.html =404;
    if (\$arg_map) {
$HDR
    }
  }"
  [ -n "$(scan "$tmp")" ] || fail "did not flag the header in a block nested in an exempt location"
  #    ...and the exempt location copied into a file that is not the nginx config:
  nginx_conf ""
  printf 'location = /login/sso/complete {\n%s\n}\n' "$HDR" >"$tmp/frontend/deploy/extra.conf"
  [ -n "$(scan "$tmp")" ] || fail "did not flag the exempt location in another file"
  rm -f "$tmp/frontend/deploy/extra.conf"
  [ -z "$(scan "$tmp")" ] || fail "the tree was not clean between cases"

  # 6. The runtime meta tag, anywhere but the two SSO modules: in other app
  #    source, in a package (the map widgets live there), and in a file with the
  #    SAME NAME as an exempt module in another directory.
  for f in frontend/apps/console/src/routes/Map.tsx frontend/packages/widgets/src/map.ts \
    frontend/apps/console/src/routes/sso.ts; do
    printf '%s\n' "$SSO_MODULE" >"$tmp/$f"
    [ -n "$(scan "$tmp")" ] || fail "did not flag a runtime referrer meta in $f"
    rm -f "$tmp/$f"
  done
  #    ...a referrerPolicy on a request, which needs no meta tag at all:
  printf "fetch(url, { referrerPolicy: 'no-referrer' });\n" >"$tmp/frontend/packages/widgets/src/tiles.ts"
  [ -n "$(scan "$tmp")" ] || fail "did not flag a referrerPolicy in frontend source"
  rm -f "$tmp/frontend/packages/widgets/src/tiles.ts"
  #    ...and an exempt module that stopped leaving by a full navigation (a
  #    router hop would carry its policy into the app's maps):
  printf "const meta = document.createElement('meta');\nmeta.name = 'referrer';\nnavigate(dest);\n" \
    >"$tmp/frontend/apps/console/src/lib/sso.ts"
  [ -n "$(scan "$tmp")" ] || fail "kept exempting an SSO module that no longer leaves by a full navigation"
  printf '%s\n' "$SSO_MODULE" >"$tmp/frontend/apps/console/src/lib/sso.ts"
  [ -z "$(scan "$tmp")" ] || fail "the tree was not clean after the runtime cases"

  # 7. A missing target must be an ERROR, not a quiet pass. This is the case
  #    that separates "the check is clean" from "the check looked nowhere".
  for d in deploy/helm frontend/packages; do
    mv "$tmp/$d" "$tmp/$d.gone"
    if scan "$tmp" >/dev/null 2>&1; then
      fail "reported success with required target $d missing"
    fi
    mv "$tmp/$d.gone" "$tmp/$d"
  done

  echo "self-test passed: fires on a header, a meta tag and a runtime referrer; exempts exactly the SSO return locations and modules, and no widening of them; quiet on a clean tree; errors on a missing target"
  exit 0
fi

report "$(scan "$ROOT")"
echo "no Referrer-Policy header or referrer meta tag is stripping the Referer"
