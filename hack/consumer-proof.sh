#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The out-of-tree consumer proof for the published npm packages.
#
# WHAT THIS GATES
#
# Not the merge. What it gates is the CLAIM that @devicechain/client,
# @devicechain/dashboards and @devicechain/widgets are usable by somebody who is
# not us — from a real `npm pack` tarball, in an application outside this
# repository, built by a bundler this repository does not otherwise use.
#
# WHY IT HAS TO EXIST AT ALL. The map widget's failure mode is that it builds,
# type-checks, passes every unit test, answers 200 for every file it asks for,
# logs nothing — and renders nothing. Everything cheaper than this rig has been
# measured against a deliberately broken build and found unable to see it:
#
#   the package build      exit 0, with the defect in the artifact
#   the consumer's build   exit 0, twice, for two DIFFERENT real defects
#   the browser console    zero errors on one of them
#   the DOM                two canvases, eight markers, no fallback, no notice
#
# So the rig gates on PIXELS, and on what its own server was asked for. Both of
# the real defects it has caught so far were invisible to everything else.
#
# 🔴 It has already earned its keep once: it found the webpack recipe published
# in the widgets README and in map-runtime-context.tsx to be BROKEN — a verbatim
# copy of MapLibre's worker leaves that file's `import "./maplibre-gl-shared.mjs"`
# dangling, and the map then draws ocean and no land, quietly.
#
#   hack/consumer-proof.sh pack      build the packages and `npm pack` them
#   hack/consumer-proof.sh arms      materialise + install + build the three arms
#   hack/consumer-proof.sh verify    drive a real browser at each arm and assert
#   hack/consumer-proof.sh control   THE NEGATIVE CONTROLS: three planted defects,
#                                    each required to fail the named assertion, each
#                                    then restored and required to pass again
#   hack/consumer-proof.sh all       pack + arms + verify + control
#   hack/consumer-proof.sh clean     remove the work directory
#
# THREE ARMS, and the third is not padding:
#
#   vite     the ready-made runtime the package ships (`@devicechain/widgets/vite`)
#   webpack  the bundler-native recipe: the worker as a second webpack entry
#   copy     the bundler-agnostic recipe the README publishes: the worker COPIED
#            into the served output, next to its sibling
#
# A Vite-only proof would certify the one bundler that already worked, and the
# copy arm exists because a published recipe that nothing exercises is precisely
# how the broken one got there.
#
# 🔴 A green run means these three arms, on this box, with ESM + React 19 + this
# Chrome. It says nothing about Next.js/RSC, CJS consumers, or `node16`/`nodenext`
# module resolution — those are documented limits, not tested ones.
#
# `all` is the one worth running. `verify` alone reports on instruments whose
# ability to fail has not been demonstrated in this session.
#
# 🔴 If a CONTROL fails partway, it will have restored the git tree (there is a
# trap for that) but the work directory may still hold a deliberately broken
# tarball installed into one arm. Re-run `pack` and `arms` before believing a
# later `verify`.
#
# Requires: node, npm, a Chrome or Chromium on PATH (or CHROME_PATH), and an
# installed frontend (`cd frontend && npm ci`). Downloads npm packages: the arms
# install a real toolchain each.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
FRONTEND="$REPO_ROOT/frontend"
RIG="$REPO_ROOT/hack/consumer-proof"
# 🔴 OUTSIDE THE REPOSITORY, DELIBERATELY. Node resolves modules by walking parents,
# so an arm placed anywhere under the repo would also see `frontend/node_modules` —
# and a dependency the tarball forgot to declare would resolve anyway. The rig would
# then prove the opposite of what it claims.
WORK="${DC_CONSUMER_PROOF_WORK:-${TMPDIR:-/tmp}/dc-consumer-proof}"
ARMS=(vite webpack copy)

# The files the controls edit. Restored with `git checkout --`, which is why every
# control refuses to start unless they are clean.
CONTROLLED_SOURCES=(
  "frontend/packages/widgets/src/index.ts"
  "frontend/packages/widgets/src/widgets/map.tsx"
)

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31m==> FAILED: %s\033[0m\n\n' "$*" >&2; exit 1; }

# ---- preflight --------------------------------------------------------------

preflight() {
  command -v node >/dev/null || fail "node is not on PATH"
  command -v npm >/dev/null || fail "npm is not on PATH"
  if [ -z "${CHROME_PATH:-}" ]; then
    command -v google-chrome >/dev/null \
      || command -v google-chrome-stable >/dev/null \
      || command -v chromium >/dev/null \
      || command -v chromium-browser >/dev/null \
      || fail "no Chrome/Chromium on PATH. Install one, or set CHROME_PATH."
  fi
  [ -d "$FRONTEND/node_modules" ] || fail "frontend is not installed. Run: (cd frontend && npm ci)"
}

# Every control edits tracked sources and restores them with git. If they are
# already dirty, "restore" would destroy work that is not the rig's — so the
# controls refuse to run rather than risk it.
require_clean_controlled_sources() {
  local dirty
  dirty="$(cd "$REPO_ROOT" && git status --porcelain -- "${CONTROLLED_SOURCES[@]}")"
  [ -z "$dirty" ] || fail "the sources the controls edit are already modified:
$dirty
Commit or stash them first — a control restores by checking them out."
}

# ---- pack -------------------------------------------------------------------

pack() {
  preflight
  say "building the publishable packages"
  (cd "$FRONTEND" && npm run build:packages)

  say "packing them"
  mkdir -p "$WORK/tarballs"
  rm -f "$WORK"/tarballs/*.tgz
  (cd "$FRONTEND" && npm pack \
    --workspace @devicechain/client \
    --workspace @devicechain/dashboards \
    --workspace @devicechain/widgets \
    --pack-destination "$WORK/tarballs" >/dev/null)

  # Asserted, not assumed: `npm pack` writing nothing, or writing an empty
  # tarball, would otherwise be discovered as a confusing install failure later.
  local count
  count="$(find "$WORK/tarballs" -name '*.tgz' -size +8k | wc -l)"
  [ "$count" -eq 3 ] || fail "expected 3 non-trivial tarballs, found $count in $WORK/tarballs"
  say "packed: $(cd "$WORK/tarballs" && echo ./*.tgz)"
}

repack_widgets() {
  (cd "$FRONTEND" && npm run build:packages >/dev/null)
  (cd "$FRONTEND" && npm pack --workspace @devicechain/widgets \
    --pack-destination "$WORK/tarballs" >/dev/null)
}

# ---- the arms ---------------------------------------------------------------

# Copy the shared application in, then the arm's own runtime beside it. The app is
# byte-identical everywhere; `runtime.ts` is the contract each bundler must satisfy,
# and is the ONLY source difference between the arms.
materialize() {
  local arm="$1"
  rm -rf "${WORK:?}/$arm"
  mkdir -p "$WORK/$arm/src"
  cp -R "$RIG/$arm"/* "$WORK/$arm/"
  mv "$WORK/$arm/runtime.ts" "$WORK/$arm/src/runtime.ts"
  cp "$RIG/app/app.tsx" "$RIG/app/board.ts" "$WORK/$arm/src/"
  cp "$RIG/app/index.html" "$WORK/$arm/index.html"
}

install_arm() {
  local arm="$1"
  say "$arm: installing from the tarballs"
  # The three tarballs are installed together so the exact-pinned internal peers
  # (`0.0.0-dev`) resolve to each other rather than to the registry.
  (cd "$WORK/$arm" && npm install --no-audit --no-fund "$WORK"/tarballs/*.tgz)
}

build_arm() {
  local arm="$1"
  say "$arm: building"
  rm -rf "${WORK:?}/$arm/dist"
  (cd "$WORK/$arm" && npm run build)
}

# 🔴 A tarball older than the sources is the false-green this whole arc is about: the
# arms install, build and pass, against code nobody has shipped. Cheap to check, and the
# alternative is a rig that reports on yesterday.
require_fresh_tarballs() {
  local tgz newer found=0
  for tgz in "$WORK"/tarballs/*.tgz; do
    [ -e "$tgz" ] || continue
    found=1
    # `-print -quit` stops at the first source newer than this tarball: no filename
    # parsing, and no second pass over a tree that has already answered the question.
    newer="$(find "$FRONTEND/packages" -path '*/node_modules' -prune -o \
      \( -path '*/src/*' -o -name 'package.json' \) -type f -newer "$tgz" -print -quit)"
    [ -z "$newer" ] || fail "$(basename "$tgz") is older than $newer
Run: $0 pack"
  done
  # A vacuous loop would otherwise report freshness for a directory with nothing in it.
  [ "$found" -eq 1 ] || fail "no tarballs in $WORK/tarballs. Run: $0 pack"
}

arms() {
  preflight
  [ -d "$WORK/tarballs" ] || fail "no tarballs yet. Run: $0 pack"
  require_fresh_tarballs
  say "installing the harness"
  mkdir -p "$WORK/harness"
  cp "$RIG/harness"/* "$WORK/harness/"
  (cd "$WORK/harness" && npm install --no-audit --no-fund)
  for arm in "${ARMS[@]}"; do
    materialize "$arm"
    install_arm "$arm"
    build_arm "$arm"
  done
}

drive() {
  local arm="$1"; shift
  node "$WORK/harness/drive.mjs" --dist "$WORK/$arm/dist" --arm "$arm" "$@"
}

verify() {
  preflight
  require_fresh_tarballs
  for arm in "${ARMS[@]}"; do
    drive "$arm" || fail "$arm did not render the map"
  done
  say "ALL THREE ARMS PASSED"
}

# ---- the negative controls --------------------------------------------------

# Each control plants ONE defect, requires the assertion aimed at it to be the one
# that fails, then restores and requires a pass through the SAME pipeline.
#
# 🔴 The restore-and-repass half is not ceremony. Without it a control that went red
# for an unrelated reason — a stale install, a network blip, a rig this edit broke —
# reads as a held control, and the rig quietly stops testing anything.

restore_sources() {
  (cd "$REPO_ROOT" && git checkout -- "${CONTROLLED_SOURCES[@]}")
}

# Assert an edit actually landed. A `sed` whose pattern matches nothing exits 0 and
# changes nothing, which would leave a control planting no defect and then reporting
# that the defect was not detected.
assert_planted() {
  local file="$1" needle="$2"
  grep -q -- "$needle" "$file" || fail "the control did not plant into $file (looked for: $needle)"
}

control_deleted_export() {
  say "CONTROL 1 — an export deleted from @devicechain/widgets"
  local index="$FRONTEND/packages/widgets/src/index.ts"
  sed -i 's/export { MapRuntimeProvider, useMapRuntime/export { useMapRuntime/' "$index"
  assert_planted "$index" 'export { useMapRuntime'
  # Written as an `if` rather than `grep … && fail`: under `set -e` an AND-list whose
  # first command fails takes the whole script down, so the readable form would abort
  # the rig on the successful path.
  if grep -q 'MapRuntimeProvider,' "$index"; then
    fail "MapRuntimeProvider is still exported; the plant did nothing"
  fi

  repack_widgets
  install_arm vite
  local out rc
  set +e
  out="$(build_arm vite 2>&1)"; rc=$?
  set -e
  restore_sources

  [ "$rc" -ne 0 ] || fail "CONTROL 1 DID NOT HOLD: the consumer built successfully with a deleted export"
  # 🔴 The failure must NAME the export. "It failed" is satisfied by a typo in this
  # script, a corrupt tarball, or a full disk.
  grep -q 'MapRuntimeProvider' <<<"$out" \
    || fail "CONTROL 1 DID NOT HOLD: the build failed, but never named MapRuntimeProvider. Output:
$out"
  say "CONTROL 1 HELD — the build failed and named MapRuntimeProvider"

  say "CONTROL 1 — restoring and re-proving through the same pipeline"
  repack_widgets
  install_arm vite
  build_arm vite
  drive vite || fail "CONTROL 1 restore did not pass: the pipeline is now broken for another reason"
  say "CONTROL 1 RESTORED — vite passes again"
}

control_reintroduced_dialect() {
  say "CONTROL 2 — a bundler dialect planted back into @devicechain/widgets"
  local map="$FRONTEND/packages/widgets/src/widgets/map.tsx"
  sed -i "s|^function loadMapLibre|import plantedWorkerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?worker\&url';\n\nfunction loadMapLibre|" "$map"
  sed -i 's|mod.setWorkerUrl(runtime.workerUrl);|mod.setWorkerUrl(plantedWorkerUrl);|' "$map"
  assert_planted "$map" "maplibre-gl-worker.mjs?worker&url"
  assert_planted "$map" 'setWorkerUrl(plantedWorkerUrl)'

  repack_widgets
  install_arm webpack
  # 🔴 Measured, and the reason this control drives a browser instead of reading a
  # build log: webpack does NOT reject the dialect. It strips the query, resolves the
  # module, bundles it, and exits 0. The specifier's VALUE is then a module namespace
  # object where a URL string was expected.
  build_arm webpack || fail "CONTROL 2 could not run: the webpack build failed for another reason"
  drive webpack --expect fail --require "drew LAND" \
    || fail "CONTROL 2 DID NOT HOLD"
  restore_sources
  say "CONTROL 2 HELD — a green webpack build, and no land on the map"

  say "CONTROL 2 — restoring and re-proving through the same pipeline"
  repack_widgets
  install_arm webpack
  build_arm webpack
  drive webpack || fail "CONTROL 2 restore did not pass"
  say "CONTROL 2 RESTORED — webpack passes again"
}

control_unwired_host() {
  say "CONTROL 3 — a host that installs no MapRuntimeProvider"
  local app="$WORK/vite/src/app.tsx"
  sed -i 's|<MapRuntimeProvider runtime={mapRuntime}>|<>|' "$app"
  sed -i 's|</MapRuntimeProvider>|</>|' "$app"
  assert_planted "$app" '<>'
  assert_planted "$app" '</>'

  build_arm vite
  # Asserted POSITIVELY, per the widget's own contract: an unwired host must be TOLD
  # what is missing. "No map appeared" is equally true of a page that failed to load.
  drive vite --expect notice || fail "CONTROL 3 DID NOT HOLD: an unwired host did not show the notice"
  say "CONTROL 3 HELD — the widget refused, loudly"

  say "CONTROL 3 — restoring and re-proving"
  cp "$RIG/app/app.tsx" "$app"
  build_arm vite
  drive vite || fail "CONTROL 3 restore did not pass"
  say "CONTROL 3 RESTORED — vite passes again"
}

control() {
  preflight
  require_clean_controlled_sources
  [ -d "$WORK/vite/node_modules" ] || fail "the arms are not built yet. Run: $0 arms"
  # Any exit path restores the tree — a control that dies half-planted must not leave
  # a bundler dialect sitting in the package source.
  trap 'restore_sources' EXIT
  control_deleted_export
  control_reintroduced_dialect
  control_unwired_host
  trap - EXIT
  restore_sources
  say "ALL THREE CONTROLS HELD AND RESTORED"
}

# ---- entry point ------------------------------------------------------------

case "${1:-}" in
  pack) pack ;;
  arms) arms ;;
  verify) verify ;;
  control) control ;;
  all) pack; arms; verify; control ;;
  clean) rm -rf "${WORK:?}"; say "removed $WORK" ;;
  *)
    cat >&2 <<'USAGE'
hack/consumer-proof.sh — the out-of-tree consumer proof for the npm packages.

  pack      build the packages and `npm pack` them
  arms      materialise + install + build the three arms (vite, webpack, copy)
  verify    drive a real browser at each arm and assert what the map did
  control   the three negative controls, each restored and re-proved
  all       pack + arms + verify + control   <- the one worth running
  clean     remove the work directory

The header of this script explains what a green run does and does not mean.
USAGE
    exit 1
    ;;
esac
