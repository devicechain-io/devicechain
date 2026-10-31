#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Builds the release manifest published to https://devicechain.io/releases.json.
#
#   hack/build-release-manifest.sh <tag> [out.json]
#   hack/build-release-manifest.sh --self-test
#
# It lists three products' downloads, each in its own table of identical rows: `dcctl` (the
# installer), `edgeAgent` (the dc-edge-agent static binaries for an edge host) and `simulator`
# (the dc-simulator reference runner that `dcctl sim run` drives).
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
# and docs can act on without a human noticing first. `theme` rides along with them: one
# phrase naming the release, written by hand in the same file, so the download menu can say
# what a version was about without a human copying it across.
#
# The checksums matter more than they look. They come from the goreleaser-produced
# checksum files (checksums.txt for dcctl, dc-edge-agent_checksums.txt for the agent), so an install script can verify what it downloaded against a file we
# published rather than trusting the transport. That is the half of "publish this data
# anyway" that is hard to add later.
set -euo pipefail

# --self-test: drive this script, end to end, against a stub `gh` on PATH that serves
# planted release assets. No network, no token. It runs the REAL entry point rather than a
# function inside it, because the bugs worth catching here live in the wiring: which file
# was downloaded, which checksums fed which table, whether a missing artifact stops the run.
if [ "${1:-}" = "--self-test" ]; then
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir "$tmp/bin" "$tmp/assets"
  # `gh release download <tag> --repo R --pattern P --dir D` copies the planted asset named
  # exactly P into D, and fails as the real one does when the Release has no such asset.
  cat >"$tmp/bin/gh" <<'STUB'
#!/usr/bin/env bash
[ "$1 $2" = "release download" ] || { echo "stub gh: unexpected call: $*" >&2; exit 2; }
shift 2
pattern="" dir="."
while [ $# -gt 0 ]; do
  case "$1" in
    --pattern) pattern="$2"; shift 2 ;;
    --dir) dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -f "$STUB_ASSETS/$pattern" ] || { echo "no assets match the file pattern" >&2; exit 1; }
cp "$STUB_ASSETS/$pattern" "$dir/"
STUB
  chmod +x "$tmp/bin/gh"
  tag=v9.9.9 ver=9.9.9
  # breaking:true so this fixture does not depend on whether the tree has declared breaks live.
  printf '{"version":"%s","theme":"a named release","breaking":true,"highlights":["one thing"]}\n' \
    "$tag" >"$tmp/hl.json"
  # A distinct digest per file, so a value landing on the wrong row is visible.
  sha_of() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }
  plant() { # rebuild a complete, well-formed release
    rm -f "$tmp/assets/"*
    local f
    for f in linux_amd64.tar.gz linux_arm64.tar.gz darwin_amd64.tar.gz darwin_arm64.tar.gz \
             windows_amd64.zip windows_arm64.zip; do
      echo "$(sha_of "dcctl_${ver}_$f")  dcctl_${ver}_$f" >>"$tmp/assets/checksums.txt"
    done
    for f in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
      echo "$(sha_of "dc-edge-agent_${ver}_$f.tar.gz")  dc-edge-agent_${ver}_$f.tar.gz" \
        >>"$tmp/assets/dc-edge-agent_checksums.txt"
    done
    for f in linux_amd64.tar.gz linux_arm64.tar.gz darwin_amd64.tar.gz darwin_arm64.tar.gz \
             windows_amd64.zip windows_arm64.zip; do
      echo "$(sha_of "dc-simulator_${ver}_$f")  dc-simulator_${ver}_$f" \
        >>"$tmp/assets/dc-simulator_checksums.txt"
    done
  }
  run() { # run -> rc; manifest in $tmp/out.json, stderr in $tmp/err
    rm -f "$tmp/out.json"
    local rc=0
    PATH="$tmp/bin:$PATH" STUB_ASSETS="$tmp/assets" GH_REPO=example/repo \
      HIGHLIGHTS_FILE="$tmp/hl.json" RELEASED_AT=2026-01-01T00:00:00Z \
      "$self" "$tag" "$tmp/out.json" >/dev/null 2>"$tmp/err" || rc=$?
    return "$rc"
  }
  fail() { echo "self-test FAIL: $*" >&2; [ -f "$tmp/err" ] && cat "$tmp/err" >&2; exit 1; }
  q() { jq -r "$1" "$tmp/out.json"; }

  # 1. A complete release: both tables, every row carrying ITS OWN file's digest.
  plant
  run || fail "a complete release was refused"
  [ "$(q .schemaVersion)" = 1 ] || fail "schemaVersion is $(q .schemaVersion), not 1"
  [ "$(q '.dcctl | length')" = 6 ] || fail "dcctl has $(q '.dcctl | length') rows, not 6"
  [ "$(q '[.dcctl[].filename | select(startswith("dcctl_") | not)] | length')" = 0 ] \
    || fail "a non-dcctl file reached the dcctl table"
  want="$(sha_of "dcctl_${ver}_windows_arm64.zip")"
  [ "$(q '.dcctl[] | select(.os=="windows" and .arch=="arm64") | .sha256')" = "$want" ] \
    || fail "dcctl windows/arm64 carries the wrong digest"
  [ "$(q '.edgeAgent | length')" = 4 ] || fail "edgeAgent has $(q '.edgeAgent | length') rows, not 4"
  for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
    os="${p%_*}" arch="${p#*_}" file="dc-edge-agent_${ver}_$p.tar.gz"
    row="$(q ".edgeAgent[] | select(.os==\"$os\" and .arch==\"$arch\")")"
    [ -n "$row" ] || fail "edgeAgent has no $os/$arch row"
    [ "$(jq -r .filename <<<"$row")" = "$file" ] || fail "edgeAgent $os/$arch filename: $(jq -r .filename <<<"$row")"
    [ "$(jq -r .sha256 <<<"$row")" = "$(sha_of "$file")" ] || fail "edgeAgent $os/$arch carries the wrong digest"
    [ "$(jq -r .url <<<"$row")" = "https://github.com/example/repo/releases/download/$tag/$file" ] \
      || fail "edgeAgent $os/$arch url: $(jq -r .url <<<"$row")"
  done
  [ "$(q '.simulator | length')" = 6 ] || fail "simulator has $(q '.simulator | length') rows, not 6"
  want="$(sha_of "dc-simulator_${ver}_windows_arm64.zip")"
  [ "$(q '.simulator[] | select(.os=="windows" and .arch=="arm64") | .sha256')" = "$want" ] \
    || fail "simulator windows/arm64 carries the wrong digest"
  [ "$(q '[.simulator[].filename | select(startswith("dc-simulator_") | not)] | length')" = 0 ] \
    || fail "a non-simulator file reached the simulator table"
  echo "self-test ok: a complete release lists 6 dcctl, 4 edge-agent and 6 simulator archives, each with its own digest" >&2

  # 2. The edge agent's checksum file is absent: the edge-agent job did not finish. A
  # manifest offering dcctl alone is the defect this table exists to end, so it is refused.
  plant; rm "$tmp/assets/dc-edge-agent_checksums.txt"
  run && fail "a release without dc-edge-agent_checksums.txt was published"
  grep -q "could not download dc-edge-agent_checksums.txt" "$tmp/err" \
    || fail "the refusal does not say which checksum file is missing"
  [ -f "$tmp/out.json" ] && fail "a refused run still left a manifest behind"
  echo "self-test ok: a missing edge-agent checksum file is refused, and writes nothing" >&2

  # 3. One edge archive missing: refused, naming the file, rather than a shorter table.
  plant; sed -i '/darwin_arm64/d' "$tmp/assets/dc-edge-agent_checksums.txt"
  run && fail "a release missing the darwin/arm64 edge archive was published"
  grep -q "dc-edge-agent_${ver}_darwin_arm64.tar.gz" "$tmp/err" || fail "the refusal does not name the missing archive"
  # This refusal comes from jq, while it is producing the manifest. Redirecting jq straight
  # into the output path would leave an empty file behind here, which is the case worth asking.
  [ -f "$tmp/out.json" ] && fail "a refused run still left a manifest behind"
  echo "self-test ok: a missing edge archive is refused by name, and writes nothing" >&2

  # 4. Each table reads ITS OWN checksum file. An edge digest found only in dcctl's
  # checksums.txt is not the edge job's attestation and must not be accepted as one.
  plant; grep linux_amd64 "$tmp/assets/dc-edge-agent_checksums.txt" >>"$tmp/assets/checksums.txt"
  sed -i '/linux_amd64/d' "$tmp/assets/dc-edge-agent_checksums.txt"
  run && fail "an edge digest taken from dcctl's checksums.txt was accepted"
  echo "self-test ok: an edge digest is read from the edge agent's own checksum file only" >&2

  # 5. THE COUNTERWEIGHT for the table that already existed: a missing dcctl archive is
  # still refused.
  plant; sed -i '/windows_amd64/d' "$tmp/assets/checksums.txt"
  run && fail "a release missing the windows/amd64 dcctl archive was published"
  grep -q "dcctl_${ver}_windows_amd64.zip" "$tmp/err" || fail "the refusal does not name the missing dcctl archive"
  echo "self-test ok: a missing dcctl archive is still refused by name" >&2

  # 6. The simulator's checksum file is absent, or one of its archives is: refused by name,
  # like the edge agent's, rather than published as a release with no simulator to run a scenario.
  plant; rm "$tmp/assets/dc-simulator_checksums.txt"
  run && fail "a release without dc-simulator_checksums.txt was published"
  grep -q "could not download dc-simulator_checksums.txt" "$tmp/err" \
    || fail "the refusal does not say which checksum file is missing"
  [ -f "$tmp/out.json" ] && fail "a refused run still left a manifest behind"
  plant; sed -i '/windows_arm64/d' "$tmp/assets/dc-simulator_checksums.txt"
  run && fail "a release missing the windows/arm64 simulator archive was published"
  grep -q "dc-simulator_${ver}_windows_arm64.zip" "$tmp/err" || fail "the refusal does not name the missing simulator archive"
  echo "self-test ok: a missing simulator checksum file or archive is refused by name" >&2

  echo "self-test passed" >&2
  exit 0
fi

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

# Each product's goreleaser writes its OWN checksum file onto the one Release: dcctl's is the
# default checksums.txt, the edge agent's is dc-edge-agent_checksums.txt (named apart so the
# edge job's `gh release upload --clobber` cannot replace dcctl's). Each table below is built
# from its own product's file only. A digest is that job's statement about the archives it
# built; reading an edge archive's digest out of dcctl's file would be publishing a claim
# nobody made.
#
# Either file being absent means its job did not finish, and the manifest is refused rather
# than published short. A manifest that silently offers dcctl alone is exactly the defect the
# edge table was added to end: the site cannot tell a missing product from one never shipped.
sums() { # <checksum-asset> <filename-prefix> <out.json>  ->  {filename: sha}
  gh release download "$TAG" --repo "$REPO" --pattern "$1" --dir "$WORK" \
    || { echo "could not download $1 for $TAG — did the job that publishes it finish?" >&2; exit 1; }
  # `sha  filename` -> {filename: sha}, keeping only this product's archives. A line that
  # names one and does not parse fails the run (capture errors) rather than being skipped.
  jq -Rn --arg prefix "$2" '
    [ inputs
      | select(contains($prefix))
      | capture("^(?<sha>[0-9a-f]{64})\\s+\\*?(?<file>\\S+)$")
    ] | map({key: .file, value: .sha}) | from_entries
  ' "$WORK/$1" > "$3"
}
sums checksums.txt dcctl_ "$WORK/dcctl.json"
sums dc-edge-agent_checksums.txt dc-edge-agent_ "$WORK/edge.json"
sums dc-simulator_checksums.txt dc-simulator_ "$WORK/sim.json"

VERSION_NO_V="${TAG#v}"
BASE="https://github.com/$REPO/releases/download/$TAG"

# The build matrices are goreleaser's, listed explicitly rather than derived from the asset
# list so a MISSING artifact is an error here instead of a silently shorter menu on the
# website:
#   dcctl      (backend/cli/.goreleaser.yaml): linux/darwin/windows x amd64/arm64, tar.gz
#              except a zip for windows.
#   edgeAgent  (backend/edge/dc-edge-agent/.goreleaser.yaml): linux/darwin x amd64/arm64,
#              tar.gz. No windows build: the agent reads free disk with syscall.Statfs.
#   simulator  (backend/sims/dc-simulator/.goreleaser.yaml): the same matrix as dcctl.
#
# `edgeAgent` is a NEW top-level key with the same row shape as `dcctl`, so this stays
# schemaVersion 1: every existing field keeps its name, type and meaning, and a reader that
# knows only `dcctl` reads exactly what it read before.
#
# Written to a temporary file and moved into place, so a refused run leaves no manifest
# behind — not even an empty one a later step could mistake for output.
jq -n \
  --arg tag "$TAG" \
  --arg ver "$VERSION_NO_V" \
  --arg base "$BASE" \
  --arg notes "https://github.com/$REPO/releases/tag/$TAG" \
  --arg released "${RELEASED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" \
  --slurpfile dcctl "$WORK/dcctl.json" \
  --slurpfile edge "$WORK/edge.json" \
  --slurpfile sim "$WORK/sim.json" \
  --slurpfile hl "$HIGHLIGHTS" '
  def table($product; $sums; $platforms):
    $platforms
    | map(
        ($product + "_" + $ver + "_" + .os + "_" + .arch + "." + .ext) as $file
        | if ($sums[$file] // null) == null
          then error("no checksum for " + $file + " — artifact missing from the release")
          else {os: .os, arch: .arch, filename: $file, url: ($base + "/" + $file), sha256: $sums[$file]}
          end
      );
  ($hl[0]) as $h
  | table("dcctl"; $dcctl[0];
      [ {os:"linux",   arch:"amd64", ext:"tar.gz"},
        {os:"linux",   arch:"arm64", ext:"tar.gz"},
        {os:"darwin",  arch:"amd64", ext:"tar.gz"},
        {os:"darwin",  arch:"arm64", ext:"tar.gz"},
        {os:"windows", arch:"amd64", ext:"zip"},
        {os:"windows", arch:"arm64", ext:"zip"} ]) as $dcctlRows
  | table("dc-edge-agent"; $edge[0];
      [ {os:"linux",  arch:"amd64", ext:"tar.gz"},
        {os:"linux",  arch:"arm64", ext:"tar.gz"},
        {os:"darwin", arch:"amd64", ext:"tar.gz"},
        {os:"darwin", arch:"arm64", ext:"tar.gz"} ]) as $edgeRows
  | table("dc-simulator"; $sim[0];
      [ {os:"linux",   arch:"amd64", ext:"tar.gz"},
        {os:"linux",   arch:"arm64", ext:"tar.gz"},
        {os:"darwin",  arch:"amd64", ext:"tar.gz"},
        {os:"darwin",  arch:"arm64", ext:"tar.gz"},
        {os:"windows", arch:"amd64", ext:"zip"},
        {os:"windows", arch:"arm64", ext:"zip"} ]) as $simRows
  | {
      schemaVersion: 1,
      version: $tag,
      theme: ($h.theme // error("the highlights file has no .theme — the phrase that names the release")),
      released: $released,
      breaking: ($h.breaking // false),
      notes: $notes,
      docs: "https://docs.devicechain.io",
      highlights: ($h.highlights // []),
      dcctl: $dcctlRows,
      edgeAgent: $edgeRows,
      simulator: $simRows
    }
' > "$WORK/manifest.json"
mv "$WORK/manifest.json" "$OUT"

echo "wrote $OUT ($(jq -r '.dcctl | length' "$OUT") dcctl artifacts, $(jq -r '.edgeAgent | length' "$OUT") edge-agent artifacts, $(jq -r '.simulator | length' "$OUT") simulator artifacts, $(jq -r '.highlights | length' "$OUT") highlights)"
