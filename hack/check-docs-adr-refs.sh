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
#   Source and maintainer    everything else, incl. docusaurus.config.ts -> ADR refs fine
#
# The distinguishing question is not "is this file public?" — the whole repo is
# public. It is "does a READER of the published site see this?".
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

  echo "==> Self-test passed"
  exit 0
fi

hits="$(scan "${TARGETS[@]}")"
pkg_hits="$(scan_package_descriptions)"
if [ -n "$pkg_hits" ]; then
  hits="${hits:+$hits
}${pkg_hits}"
fi

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

ADR citations remain correct in source comments and maintainer-facing files —
this guard covers ${TARGETS[*]} only.

EOF
  exit 1
fi

echo "==> No ADR references in published documentation."
