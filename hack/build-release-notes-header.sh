#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Build the header of the GitHub Release body for a tag.
#
#   hack/build-release-notes-header.sh <tag> [highlights.json] > header.md
#   hack/build-release-notes-header.sh --self-test
#
# goreleaser generates the CHANGELOG (one line per commit, from the subjects).
# That answers "what changed" and is useless for "should I upgrade" -- a reader
# gets `fix(events): give payload rows and anchors identities of their own` and
# has to reverse-engineer the release from it. This emits the part a machine
# cannot derive: the same hand-written highlights the website already shows,
# above the generated list.
#
# 🔴 ONE SOURCE, TWO CONSUMERS. The highlights live in exactly one file and feed
# both the website manifest (hack/build-release-manifest.sh) and this header.
# The alternative -- a bulleted list pasted into the release notes by hand -- is
# a second copy of a thing that is already hard to remember to update, and the
# copy nobody reads until it is wrong. The same reasoning retired a duplicated
# highlights check: two guards that must agree are one guard with two callers.
#
# THE HEADING CARRIES THE THEME. `theme` is the one phrase naming the release, and it is
# joined to the version in the `What's in` heading -- the same `vX.Y.Z — phrase` shape the
# published upgrade guide's own section headings use, so a reader arriving from one lands
# on a title they have already seen. It is NOT re-validated here: the gate above already
# refuses an absent, blank or over-long theme, and this file's whole argument is that two
# guards which must agree are one guard with two callers.
#
# 🔴 THE PRE-1.0 WARNING LIVES HERE, NOT IN .goreleaser.yaml. It used to sit in
# the config's `release.header`, which `--release-header` OVERRIDES -- so leaving
# it there would have left a paragraph that looks load-bearing, is never emitted,
# and drifts. It is emitted unconditionally below, so it cannot be forgotten on
# the one release that needed it most. Remove it at v1.0.0.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The published upgrade guide's anchor for a version: v0.15.0 -> v0150-upgrade.
# A release candidate points at its own release's section (v0.15.0-rc.1 is
# documented as v0.15.0), which is the same base-version rule the highlights
# guard uses.
anchor_for() {
  local base="${1%%-*}"
  echo "${base//./}-upgrade"
}

emit() { # <tag> <highlights.json> <docs-file-or-empty>
  local tag="$1" hl="$2" docs="$3"
  local base="${tag%%-*}"
  local anchor; anchor="$(anchor_for "$tag")"
  local theme; theme="$(jq -r '(.theme // "") | gsub("^\\s+|\\s+$";"")' "$hl")"
  local url="https://docs.devicechain.io/deployment/releases-and-upgrades#${anchor}"

  # An anchor that does not exist silently lands the reader at the top of a long
  # page, which is indistinguishable from a working link until someone follows
  # it. If we are pointing at a section, the section has to be there.
  if [ -n "$docs" ]; then
    if ! grep -q "{#${anchor}}" "$docs"; then
      echo "::error::the upgrade guide has no {#${anchor}} section for ${tag}. Add it to $(basename "$docs") (and its es mirror) before tagging." >&2
      return 1
    fi
  fi

  cat <<EOF
> [!WARNING]
> **Pre-1.0 release.** Until v1.0.0, any release — including a patch — may change
> APIs, schemas, or behavior without a compatibility shim.
> See [Pre-1.0 stability](https://docs.devicechain.io/deployment/releases-and-upgrades#pre-10-stability).

## What's in ${base} — ${theme}

EOF

  if [ "$(jq -r '.breaking' "$hl")" = "true" ]; then
    cat <<EOF
> [!IMPORTANT]
> **This release contains breaking changes.** They are described in the first
> bullets below and in full in the [upgrade notes](${url}).

EOF
  fi

  jq -r '.highlights[] | "- " + .' "$hl"

  cat <<EOF

**[Full upgrade notes for ${base} →](${url})**
EOF
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the header must carry the warning, the highlights, and the breaking callout only when breaking"
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  hl="$tmp/hl.json"; docs="$tmp/docs.md"
  printf '### v0.9.0 — x {#v090-upgrade}\n' > "$docs"
  write() {
    printf '{"version":"%s","breaking":%s,"theme":"a named release","highlights":["first thing","second thing"]}\n' \
      "$1" "$2" > "$hl"
  }

  write v0.9.0 false
  out="$(emit v0.9.0 "$hl" "$docs")"
  grep -q 'Pre-1.0 release' <<<"$out" || { echo "  FAIL: no pre-1.0 warning" >&2; exit 1; }
  grep -q -- '- first thing' <<<"$out" || { echo "  FAIL: highlight missing" >&2; exit 1; }
  grep -q -- '- second thing' <<<"$out" || { echo "  FAIL: highlight missing" >&2; exit 1; }
  grep -q 'v090-upgrade' <<<"$out" || { echo "  FAIL: wrong or missing anchor" >&2; exit 1; }
  # The theme leads, joined to the version. A heading of 'What's in v0.9.0 — ' with nothing
  # after the dash is what an unchecked empty theme looks like, so assert the whole line.
  grep -q "^## What's in v0.9.0 — a named release$" <<<"$out" \
    || { echo "  FAIL: the heading does not lead with the theme" >&2; exit 1; }
  # THE COUNTERWEIGHT: a non-breaking release must NOT get the callout, or the
  # callout means nothing on the release that has one.
  grep -q 'IMPORTANT' <<<"$out" && { echo "  FAIL: breaking callout on a non-breaking release" >&2; exit 1; }
  echo "  ok: a non-breaking release gets the warning and the highlights, and NO breaking callout"

  write v0.9.0 true
  out="$(emit v0.9.0 "$hl" "$docs")"
  grep -q 'IMPORTANT' <<<"$out" || { echo "  FAIL: no breaking callout on a breaking release" >&2; exit 1; }
  grep -q 'contains breaking changes' <<<"$out" || { echo "  FAIL: callout text missing" >&2; exit 1; }
  echo "  ok: a breaking release gets the callout"

  # An rc inherits its release's section, the same base-version rule the
  # highlights guard uses -- v0.9.0-rc.1 must not look for a v091-rc1 anchor.
  out="$(emit v0.9.0-rc.1 "$hl" "$docs")"
  grep -q 'v090-upgrade' <<<"$out" || { echo "  FAIL: an rc did not resolve to its release's anchor" >&2; exit 1; }
  grep -q "What's in v0.9.0 — a named release" <<<"$out" || { echo "  FAIL: an rc did not title itself with the base version and theme" >&2; exit 1; }
  echo "  ok: a release candidate points at its release's section"

  # 🔴 THE ONE THAT MATTERS MOST: a missing anchor must FAIL, not silently link
  # to the top of the page. Without this the check is decorative.
  printf 'no anchors here\n' > "$docs"
  if emit v0.9.0 "$hl" "$docs" >/dev/null 2>&1; then
    echo "  FAIL: a missing upgrade-guide anchor was accepted" >&2; exit 1
  fi
  echo "  ok: a missing upgrade-guide section is refused"

  echo "==> Self-test passed"
  exit 0
fi

TAG="${1:?usage: build-release-notes-header.sh <tag> [highlights.json]}"
HL="${2:-$ROOT/.github/release-highlights.json}"
DOCS="$ROOT/docs/docs/deployment/releases-and-upgrades.md"

# One guard, two callers: the version/emptiness rules live in that script.
"$ROOT/hack/check-release-highlights.sh" "$TAG" "$HL" >/dev/null

emit "$TAG" "$HL" "$DOCS"
