#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Builds the release manifest published to https://devicechain.io/releases.json.
#
#   hack/build-release-manifest.sh <tag> [out.json]
#
# The manifest is a PUBLIC CONTRACT the moment anything curls it — an install script, a
# docs version banner, the website's download menu. That is why it carries `schemaVersion`
# from the first byte: consumers get something to branch on when the shape grows, instead
# of parsing by hope. Add fields freely; never repurpose or remove one without bumping it.
#
# 🔴 WHY THIS EXISTS RATHER THAN THE WEBSITE CALLING THE GITHUB API. The site's CSP is
# `default-src 'self'` with no connect-src, so a browser fetch to api.github.com is refused
# outright — and loosening that for a download button trades a deliberate, documented
# security posture for a convenience. Serving our own JSON from our own origin needs no CSP
# change, no rate limit, and no third party being up. It also carries things the GitHub API
# cannot: per-artifact checksums, a curated highlight list, and a `breaking` flag the site
# and docs can act on without a human noticing first.
#
# The checksums matter more than they look. They come from the goreleaser-produced
# checksums.txt, so an install script can verify what it downloaded against a file we
# published rather than trusting the transport. That is the half of "publish this data
# anyway" that is hard to add later.
set -euo pipefail

TAG="${1:?usage: build-release-manifest.sh <tag> [out.json]}"
OUT="${2:-releases.json}"
REPO="${GH_REPO:-devicechain-io/devicechain}"
HIGHLIGHTS="${HIGHLIGHTS_FILE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.github/release-highlights.json}"

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
command -v gh >/dev/null || { echo "gh is required" >&2; exit 1; }

[ -f "$HIGHLIGHTS" ] || { echo "highlights file not found: $HIGHLIGHTS" >&2; exit 1; }

# The same guard runs in the release workflow's `guard` job, where it fails before any
# image is pushed. It runs again here so that invoking this script by hand cannot quietly
# produce a manifest whose highlights belong to a different release.
#
# 🔴 It is the SAME SCRIPT in both places, not the same rule written twice. It was written
# twice, and the copies drifted the moment the rule changed: relaxing it to accept a
# release candidate was applied to the workflow copy only, so v0.11.0-rc.1 cleared the
# first job, built every image, passed the load gate, and then died here.
"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-release-highlights.sh" "$TAG" "$HIGHLIGHTS" >/dev/null

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# checksums.txt is goreleaser's, covering the dcctl archives. Its absence means the cli job
# did not finish, and a manifest without checksums is not one worth publishing.
gh release download "$TAG" --repo "$REPO" --pattern 'checksums.txt' --dir "$WORK" \
  || { echo "could not download checksums.txt for $TAG — did the cli job publish?" >&2; exit 1; }

# `sha  filename` -> {filename: sha}. Ignores anything that is not a dcctl archive.
jq -Rn '
  [ inputs
    | select(test("dcctl_"))
    | capture("^(?<sha>[0-9a-f]{64})\\s+\\*?(?<file>\\S+)$")
  ] | map({key: .file, value: .sha}) | from_entries
' "$WORK/checksums.txt" > "$WORK/sums.json"

VERSION_NO_V="${TAG#v}"
BASE="https://github.com/$REPO/releases/download/$TAG"

# The build matrix is goreleaser's (backend/cli/.goreleaser.yaml): linux/darwin/windows x
# amd64/arm64, tar.gz except a zip for windows. Listed explicitly rather than derived from
# the asset list so a MISSING artifact is an error here instead of a silently shorter menu
# on the website.
jq -n \
  --arg tag "$TAG" \
  --arg ver "$VERSION_NO_V" \
  --arg base "$BASE" \
  --arg notes "https://github.com/$REPO/releases/tag/$TAG" \
  --arg released "${RELEASED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" \
  --slurpfile sums "$WORK/sums.json" \
  --slurpfile hl "$HIGHLIGHTS" '
  ($sums[0]) as $s
  | ($hl[0]) as $h
  | [ {os:"linux",   arch:"amd64", ext:"tar.gz"},
      {os:"linux",   arch:"arm64", ext:"tar.gz"},
      {os:"darwin",  arch:"amd64", ext:"tar.gz"},
      {os:"darwin",  arch:"arm64", ext:"tar.gz"},
      {os:"windows", arch:"amd64", ext:"zip"},
      {os:"windows", arch:"arm64", ext:"zip"} ]
    | map(
        ("dcctl_" + $ver + "_" + .os + "_" + .arch + "." + .ext) as $file
        | if ($s[$file] // null) == null
          then error("no checksum for " + $file + " — artifact missing from the release")
          else {os: .os, arch: .arch, filename: $file, url: ($base + "/" + $file), sha256: $s[$file]}
          end
      ) as $dcctl
  | {
      schemaVersion: 1,
      version: $tag,
      released: $released,
      breaking: ($h.breaking // false),
      notes: $notes,
      docs: "https://docs.devicechain.io",
      highlights: ($h.highlights // []),
      dcctl: $dcctl
    }
' > "$OUT"

echo "wrote $OUT ($(jq -r '.dcctl | length' "$OUT") dcctl artifacts, $(jq -r '.highlights | length' "$OUT") highlights)"
