#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The packed-artifact proof for the widget SDK's developer path.
#
# WHAT THIS GATES
#
# The claim that somebody who is not us can write a widget package against the PUBLISHED
# artifacts: it packs @devicechain/client, @devicechain/dashboards and @devicechain/widget-sdk
# with `npm pack`, copies examples/widget-tank-level into a temporary directory OUTSIDE this
# repository, installs the tarballs there, builds it, and checks the result
# (hack/widget-package-proof/check.mjs):
#
#   - no monorepo path, workspace/file/link dependency, tsconfig `paths`, or import of a
#     private package (`@devicechain/widgets`, the console) anywhere in the example;
#   - the SDK and the runtime resolve from the copy's own node_modules (the tarballs), and
#     exactly one copy of the SDK is installed, since its React contexts are identified by
#     the object that created them;
#   - the DOM-only entry (dist/card.js) imports only react, the SDK root and the runtime;
#   - a host bundle of the DOM-only entry contains no echarts, zrender, maplibre-gl or
#     built-in widget code, and a bundle of the chart entry DOES contain echarts (the
#     reach control: an absence claim is worth nothing from a detector that cannot see).
#
# NEGATIVE CONTROL. `negative` plants an import of the SDK's `/echarts` entry in the DOM-only
# entry of the built copy, rebuilds it, and requires check.mjs to FAIL on exactly that, then
# restores and rebuilds the copy. A check that has never been seen to fail proves nothing.
#
# WHY OUTSIDE THE REPOSITORY. Node resolves modules by walking parents; a copy under the repo
# would also see frontend/node_modules, and a dependency the tarball forgot to declare would
# resolve anyway -- the rig would prove the opposite of what it claims. The work directory
# is refused if it lies inside the repository.
#
# WINDOWS. Run it with Git Bash, by path: `& 'C:\Program Files\Git\bin\bash.exe'
# hack/widget-package-proof.sh all` from PowerShell, where a bare `bash` is WSL. Nothing here
# calls tar (Git Bash tar breaks on C: paths; npm pack does not use it). WORK defaults to a
# directory under the system temp dir.
#
#   hack/widget-package-proof.sh pack      build the packages and `npm pack` them
#   hack/widget-package-proof.sh build     copy + install + build the example from the tarballs
#   hack/widget-package-proof.sh check     run the assertions on the built copy
#   hack/widget-package-proof.sh negative  plant a chart import in the DOM-only entry; check must fail
#   hack/widget-package-proof.sh all       pack + build + check + negative
#   hack/widget-package-proof.sh clean     remove the work directory
#
# Requires node, npm and an installed frontend (`cd frontend && npm ci`). Downloads npm packages.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
FRONTEND="$REPO_ROOT/frontend"
EXAMPLE="$REPO_ROOT/examples/widget-tank-level"
RIG="$REPO_ROOT/hack/widget-package-proof"
WORK="${DC_WIDGET_PROOF_WORK:-${TMPDIR:-/tmp}/dc-widget-package-proof}"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31m==> FAILED: %s\033[0m\n\n' "$*" >&2; exit 1; }

preflight() {
  command -v node >/dev/null || fail "node is not on PATH"
  command -v npm >/dev/null || fail "npm is not on PATH"
  [ -d "$FRONTEND/node_modules" ] || fail "frontend is not installed. Run: (cd frontend && npm ci)"
  mkdir -p "$WORK"
  local repo work
  repo="$(cd "$REPO_ROOT" && pwd -P)"
  work="$(cd "$WORK" && pwd -P)"
  case "$work/" in
    "$repo/"*) fail "the work directory $WORK is inside the repository; set DC_WIDGET_PROOF_WORK outside it" ;;
  esac
}

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
    --workspace @devicechain/widget-sdk \
    --pack-destination "$WORK/tarballs" >/dev/null)
  local count
  count="$(find "$WORK/tarballs" -name '*.tgz' -size +8k | wc -l)"
  [ "$count" -eq 3 ] || fail "expected 3 non-trivial tarballs, found $count in $WORK/tarballs"
}

build() {
  preflight
  [ -d "$WORK/tarballs" ] || fail "no tarballs yet. Run: $0 pack"
  say "copying the example outside the repository"
  rm -rf "${WORK:?}/example"
  mkdir -p "$WORK/example"
  cp -R "$EXAMPLE/package.json" "$EXAMPLE/tsconfig.json" "$EXAMPLE/tsconfig.build.json" \
    "$EXAMPLE/build.mjs" "$EXAMPLE/src" "$WORK/example/"
  say "installing from the tarballs"
  # The tarballs go in together so the exact-pinned internal peers (`0.0.0-dev`) resolve to
  # each other and not to the registry. react, echarts and graphql are the peers a host
  # provides; echarts is only needed because the chart entry imports it.
  (cd "$WORK/example" && npm install --no-audit --no-fund --no-save \
    "$WORK"/tarballs/*.tgz react@^19.3.0 react-dom@^19.3.0 echarts@^6.0.0 graphql@^16.0.0)
  say "building the example"
  (cd "$WORK/example" && npm run typecheck && npm run build)
}

check() {
  [ -d "$WORK/example/dist" ] || fail "no built example. Run: $0 build"
  say "checking the built example"
  # Run from the work directory so esbuild resolves the INSTALLED tarballs, not the monorepo.
  (cd "$WORK/example" && node "$RIG/check.mjs" "$WORK/example" "$EXAMPLE") \
    || fail "the example did not pass the packed-artifact checks"
  say "PASSED"
}

# negative plants a chart import in the DOM-only entry of the built copy and requires the
# check to fail on it, for the right reason, then puts the copy back. If it is interrupted
# the copy may stay planted; `build` re-copies the example from the repository.
negative() {
  [ -d "$WORK/example/dist" ] || fail "no built example. Run: $0 build"
  local entry="$WORK/example/src/card.ts" saved="$WORK/card.ts.orig" out rc=0
  cp "$entry" "$saved"
  printf "export { EChart } from '@devicechain/widget-sdk/echarts';\n" >>"$entry"
  say "negative control: a chart import planted in the DOM-only entry must fail the check"
  (cd "$WORK/example" && node build.mjs >/dev/null 2>&1) || { mv "$saved" "$entry"; fail "the planted copy did not build"; }
  out="$(cd "$WORK/example" && node "$RIG/check.mjs" "$WORK/example" "$EXAMPLE" 2>&1)" || rc=$?
  mv "$saved" "$entry"
  say "restoring and rebuilding the copy"
  (cd "$WORK/example" && npm run build >/dev/null)
  [ "$rc" -ne 0 ] || fail "the check PASSED with an echarts import in the DOM-only entry"
  local want
  for want in \
    'FAIL dist/card.js imports only the root-level peers (@devicechain/widget-sdk/echarts)' \
    'FAIL DOM-only host bundle contains no module matching /node_modules[\\/]echarts[\\/]/'; do
    grep -qF "$want" <<<"$out" || { printf '%s\n' "$out" >&2; fail "the check failed, but not with: $want"; }
  done
  say "negative control PASSED: the planted import was caught"
}

case "${1:-}" in
  pack) pack ;;
  build) build ;;
  check) check ;;
  negative) negative ;;
  all) pack; build; check; negative ;;
  clean) rm -rf "${WORK:?}"; say "removed $WORK" ;;
  *)
    cat >&2 <<'USAGE'
hack/widget-package-proof.sh -- build the example widget package against packed SDK tarballs.

  pack      build the packages and `npm pack` them
  build     copy the example out of the repository, install the tarballs, build it
  check     assert: no private imports, DOM-only bundle is chart/map/built-in free
  negative  plant a chart import in the DOM-only entry; the check must fail on it
  all       pack + build + check + negative
  clean     remove the work directory
USAGE
    exit 1
    ;;
esac
