#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Publish the release manifest to the website repository and confirm the site serves it.
#
#   hack/publish-release-manifest.sh <tag> <manifest.json>
#   hack/publish-release-manifest.sh --self-test
#
# Environment (all optional except where noted):
#   DEPLOY_KEY       private half of the website repo's write-enabled deploy key. When set, the
#                    script writes it to a 0600 temp file, pins GitHub's SSH host keys from
#                    api.github.com/meta (HTTPS, not trust-on-first-use) and removes both on exit.
#   WEBSITE_REMOTE   git remote to clone and push.  default git@github.com:devicechain-io/website.git
#   WEBSITE_BRANCH   branch to publish to.           default main
#   VERIFY_URL       URL that must report <tag>.     default https://devicechain.io/releases.json
#   BUILD_HOOK       Netlify build hook URL. Triggered ONCE if the manifest is already current
#                    in git but the site is not serving it. Unset = never triggered.
#   VERIFY_ATTEMPTS  polls, 15 s apart.              default 40 (10 minutes)
#   VERIFY_INTERVAL  seconds between polls.          default 15
#
# THE LAYOUT MOVE. The manifest's owner used to be a root-level releases.json, served because
# Netlify published the repo root. The Astro site publishes dist/ and owns the file at
# public/releases.json. The two layouts coexist for one window (this change merges before the
# website cutover), so the script LOOKS at the checkout it cloned instead of assuming one:
#
#   astro.config.mjs present  -> write public/releases.json            (new layout)
#   absent                    -> write releases.json at the repo root   (legacy layout)
#
# The legacy branch is exactly what the release job did before, so merging this change can never
# break a release that happens before the cutover, and a release that happens after it needs no
# second change. Delete the legacy branch once the cutover has shipped.
#
# NEVER REGRESS. Only a stable tag reaches this script, but a re-run of an OLD tag's job after a
# newer release would overwrite the live manifest with the older version. A tag lower than what
# the branch already carries is refused. The same tag again is the supported re-run: a no-op if
# the file is current.
#
# WHAT A RELEASE MAY TOUCH. The push is staged and then checked: only the manifest path for the
# layout in use may be in it. Anything else aborts before the commit.
set -euo pipefail

REMOTE="${WEBSITE_REMOTE:-git@github.com:devicechain-io/website.git}"
BRANCH="${WEBSITE_BRANCH:-main}"
VERIFY_URL="${VERIFY_URL:-https://devicechain.io/releases.json}"
ATTEMPTS="${VERIFY_ATTEMPTS:-40}"
INTERVAL="${VERIFY_INTERVAL:-15}"

# version_gt A B: is A strictly newer than B? (vX.Y.Z, stable only)
version_gt() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "${1#v}" "${2#v}" | sort -V | tail -n1)" = "${1#v}" ]
}

publish() {
  local tag="$1" manifest="$2" workdir
  [ -f "$manifest" ] || { echo "::error::manifest $manifest not found" >&2; return 1; }
  workdir="$(mktemp -d)"
  git clone --quiet --depth 1 --branch "$BRANCH" "$REMOTE" "$workdir/site"
  cd "$workdir/site"

  local target
  if [ -f astro.config.mjs ]; then target="public/releases.json"; else target="releases.json"; fi
  echo "website layout: $target"

  if [ -f "$target" ]; then
    local current
    current="$(jq -r '.version // empty' "$target" 2>/dev/null || true)"
    if [ -n "$current" ] && version_gt "$current" "$tag"; then
      echo "::error::$target already carries $current, which is newer than $tag. Refusing to regress the live manifest." >&2
      return 1
    fi
  fi

  mkdir -p "$(dirname "$target")"
  cp "$manifest" "$target"

  git config user.name "devicechain-release"
  git config user.email "noreply@devicechain.io"
  git add -- "$target"
  local touched
  touched="$(git diff --cached --name-only)"
  if [ -z "$touched" ]; then
    echo "$target already current for $tag - nothing to commit."
    echo "current" > "$workdir/state"
  else
    if [ "$touched" != "$target" ]; then
      echo "::error::staged paths other than $target: $touched" >&2
      return 1
    fi
    git commit --quiet -m "Publish release manifest for $tag"
    git push --quiet origin "HEAD:$BRANCH"
    echo "pushed $tag to $BRANCH."
    echo "pushed" > "$workdir/state"
  fi
  PUBLISH_STATE="$(cat "$workdir/state")"
  cd - >/dev/null
  rm -rf "$workdir"
}

# verify TAG: poll VERIFY_URL until it reports TAG. If the file was already current in git and the
# site is not serving it, the previous build must have failed or never started: trigger the
# Netlify build hook once and keep waiting.
verify() {
  local tag="$1" hooked=0 live i
  for i in $(seq 1 "$ATTEMPTS"); do
    live="$(curl -fsS "$VERIFY_URL" | jq -r '.version' 2>/dev/null || true)"
    if [ "$live" = "$tag" ]; then
      echo "$VERIFY_URL is serving $tag."
      return 0
    fi
    echo "attempt $i: serving '${live:-<none>}', waiting for $tag"
    if [ "$PUBLISH_STATE" = "current" ] && [ "$hooked" = 0 ] && [ -n "${BUILD_HOOK:-}" ]; then
      echo "manifest is current in git but not live: triggering the Netlify build hook once."
      curl -fsS -X POST -d '{}' "$BUILD_HOOK" >/dev/null || echo "::warning::build hook call failed"
      hooked=1
    fi
    sleep "$INTERVAL"
  done
  echo "::error::$VERIFY_URL did not report $tag within $((ATTEMPTS * INTERVAL)) seconds. The commit landed; check the Netlify deploy." >&2
  return 1
}

self_test() {
  local root fails=0
  root="$(mktemp -d)"
  trap 'rm -rf "$root"' RETURN
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
  mk() { # mk <name> <legacy|astro> <version>
    git init -q --bare "$root/$1.git"
    git clone -q "$root/$1.git" "$root/$1-seed" 2>/dev/null
    ( cd "$root/$1-seed" && git checkout -q -b main
      if [ "$2" = astro ]; then mkdir -p public && echo "{\"version\":\"$3\"}" > public/releases.json && : > astro.config.mjs
      else echo "{\"version\":\"$3\"}" > releases.json; fi
      echo x > other.txt
      git add -A && git commit -q -m seed && git push -q origin main ) 2>/dev/null
  }
  expect() { # expect <label> <want-rc> <cmd...>
    local label="$1" want="$2"; shift 2
    local rc=0; ( "$@" ) >"$root/out" 2>&1 || rc=$?
    if { [ "$want" = nz ] && [ "$rc" = 0 ]; } || { [ "$want" != nz ] && [ "$rc" != "$want" ]; }; then echo "FAIL: $label (rc=$rc want=$want)"; sed 's/^/    /' "$root/out"; fails=$((fails+1)); else echo "ok: $label"; fi
  }
  newmanifest() { echo "{\"version\":\"$1\"}" > "$root/m-$1.json"; echo "$root/m-$1.json"; }
  furl() { if command -v cygpath >/dev/null 2>&1; then echo "file:///$(cygpath -m "$root/m-$1.json")"; else echo "file://$root/m-$1.json"; fi; }
  for v in v1.1.0 v1.1.10 v1.2.0 v1.0.5 v1.0.9 v1.3.0; do newmanifest "$v" >/dev/null; done
  field() { git -C "$root/$1.git" show "main:$2" 2>/dev/null | jq -r .version; }

  mk legacy legacy v1.0.0
  mk astro astro v1.0.0
  export WEBSITE_BRANCH=main PUBLISH_STATE=
  run() { WEBSITE_REMOTE="$root/$1.git" "$0" "$2" "$(newmanifest "$2")"; }

  expect "legacy layout publishes to the repo root" 0 env VERIFY_ATTEMPTS=1 VERIFY_URL="$(furl v1.1.0)" WEBSITE_REMOTE="$root/legacy.git" "$0" v1.1.0 "$(newmanifest v1.1.0)"
  [ "$(field legacy releases.json)" = v1.1.0 ] || { echo "FAIL: legacy root file not updated"; fails=$((fails+1)); }
  git -C "$root/legacy.git" cat-file -e main:public/releases.json 2>/dev/null && { echo "FAIL: legacy repo gained public/releases.json"; fails=$((fails+1)); }

  expect "astro layout publishes to public/" 0 env VERIFY_ATTEMPTS=1 VERIFY_URL="$(furl v1.1.0)" WEBSITE_REMOTE="$root/astro.git" "$0" v1.1.0 "$(newmanifest v1.1.0)"
  [ "$(field astro public/releases.json)" = v1.1.0 ] || { echo "FAIL: public/releases.json not updated"; fails=$((fails+1)); }
  git -C "$root/astro.git" cat-file -e main:releases.json 2>/dev/null && { echo "FAIL: astro repo gained a root releases.json"; fails=$((fails+1)); }

  expect "same tag again is a no-op that still verifies" 0 env VERIFY_ATTEMPTS=1 VERIFY_URL="$(furl v1.1.0)" WEBSITE_REMOTE="$root/astro.git" "$0" v1.1.0 "$(newmanifest v1.1.0)"
  expect "an older tag is refused (negative control)" 1 env VERIFY_ATTEMPTS=1 VERIFY_URL="$(furl v1.1.0)" WEBSITE_REMOTE="$root/astro.git" "$0" v1.0.5 "$(newmanifest v1.0.5)"
  [ "$(field astro public/releases.json)" = v1.1.0 ] || { echo "FAIL: older tag overwrote the manifest"; fails=$((fails+1)); }
  expect "an older tag is refused on the legacy layout too" 1 env VERIFY_ATTEMPTS=1 VERIFY_URL="$(furl v1.1.0)" WEBSITE_REMOTE="$root/legacy.git" "$0" v1.0.9 "$(newmanifest v1.0.9)"
  expect "10 sorts above 9 (not a string compare)" 0 env VERIFY_ATTEMPTS=1 VERIFY_URL="$(furl v1.1.10)" WEBSITE_REMOTE="$root/astro.git" "$0" v1.1.10 "$(newmanifest v1.1.10)"
  expect "verification fails when the site serves another version (negative control)" 1 env VERIFY_ATTEMPTS=1 VERIFY_INTERVAL=0 VERIFY_URL="$(furl v1.1.0)" WEBSITE_REMOTE="$root/astro.git" "$0" v1.2.0 "$(newmanifest v1.2.0)"
  expect "a missing branch fails loudly" nz env WEBSITE_BRANCH=nope WEBSITE_REMOTE="$root/astro.git" "$0" v1.3.0 "$(newmanifest v1.3.0)"

  if [ "$fails" -ne 0 ]; then echo "self-test: $fails failure(s)"; return 1; fi
  echo "self-test: all checks passed"
}

setup_ssh() {
  [ -n "${DEPLOY_KEY:-}" ] || return 0
  keydir="$(mktemp -d)"
  trap 'rm -rf "$keydir"' EXIT
  printf '%s
' "$DEPLOY_KEY" > "$keydir/key"
  chmod 600 "$keydir/key"
  curl -fsSL https://api.github.com/meta | jq -r '.ssh_keys[] | "github.com " + .' > "$keydir/known_hosts"
  [ -s "$keydir/known_hosts" ] || { echo "::error::could not fetch GitHub's SSH host keys" >&2; exit 1; }
  export GIT_SSH_COMMAND="ssh -i $keydir/key -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$keydir/known_hosts"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") echo "usage: $0 <tag> <manifest.json> | --self-test" >&2; exit 2 ;;
  *)
    [ $# -eq 2 ] || { echo "usage: $0 <tag> <manifest.json>" >&2; exit 2; }
    PUBLISH_STATE=""
    setup_ssh
    publish "$1" "$2"
    verify "$1"
    ;;
esac
