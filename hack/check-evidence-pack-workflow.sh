#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Holds two properties of .github/workflows/evidence-pack.yml that fail OPEN when lost:
#
#  1. Every step that pipes into tee or jq runs under `shell: bash`. A step with no `shell:`
#     runs as `bash -e {0}`, without pipefail, so `verifier | tee file` reports tee's status:
#     a failed signature verification, or a failed API call feeding jq (which would record
#     zero open CodeQL alerts as `[]`), still yields a signed, uploaded pack.
#  2. The govulncheck module list comes from go.work (hack/workspace-modules.sh), not a glob
#     that omits the broker, its administrator, deploy and the OTA reference device.
#
#   hack/check-evidence-pack-workflow.sh [FILE]   # default: the real workflow
#   hack/check-evidence-pack-workflow.sh --self-test
#
# Exit 0: both hold. Exit 1: one does not. Exit 2: the check could not run.
set -euo pipefail

WF_DEFAULT=".github/workflows/evidence-pack.yml"

check() {
  local wf="$1" bad=0
  [ -s "$wf" ] || { echo "check-evidence-pack-workflow: cannot read $wf" >&2; return 2; }
  # Split into steps at each "      - " (6-space list item) and judge each.
  local report
  report="$(awk '
    function flush() {
      if (inStep && piped && !shell) printf "step \"%s\" pipes into tee/jq with no `shell: bash`\n", name
    }
    /^      - / { flush(); inStep=1; name="(unnamed)"; piped=0; shell=0 }
    /^      - name: / { name=$0; sub(/^      - name: /, "", name) }
    /^ +name: / && name=="" { n=$0; sub(/^ +name: /, "", n); name=n }
    /^        shell: bash[[:space:]]*$/ { shell=1 }
    /\|[[:space:]]*(tee|jq)([[:space:]]|$)/ { piped=1 }
    END { flush() }
  ' "$wf")"
  if [ -n "$report" ]; then echo "$report" >&2; bad=1; fi
  if ! grep -Eq '^[^#]*mapfile -t modules < <\(hack/workspace-modules\.sh\)' "$wf"; then
    echo "the govulncheck module list is not derived from go.work (hack/workspace-modules.sh)" >&2; bad=1
  fi
  if grep -Eq 'backend/services/\*/|backend/tools/\*/' "$wf"; then
    echo "a hardcoded module glob is back in the workflow" >&2; bad=1
  fi
  return "$bad"
}

# The shell GitHub runs for `shell: bash` and for no shell at all, respectively.
with_shell()    { bash --noprofile --norc -eo pipefail -c "$1"; }
without_shell() { bash --noprofile --norc -e -c "$1"; }

self_test() {
  local d rc
  d="$(mktemp -d)"; trap 'rm -rf "$d"' RETURN
  # Control: a failing verifier piped into tee must fail the step under the shell the
  # workflow now asks for, and must NOT under the default one -- else the control proves nothing.
  if with_shell 'false 2>&1 | tee /dev/null'; then echo "self-test: failing verifier passed under pipefail" >&2; return 1; fi
  without_shell 'false 2>&1 | tee /dev/null' || { echo "self-test: control is vacuous (default shell already fails)" >&2; return 1; }
  # The real verifier, failing, through the same pipe.
  if with_shell "$(printf '%q' "$PWD/hack/verify-release-signatures.sh") --no-such-flag-xyz 2>&1 | tee /dev/null"; then
    echo "self-test: the real verifier failed open" >&2; return 1; fi
  # The real workflow passes.
  check "$WF_DEFAULT" || { echo "self-test: the real workflow was rejected" >&2; return 1; }
  # Mutants: each must be rejected.
  sed '/^        shell: bash$/d' "$WF_DEFAULT" > "$d/noshell.yml"
  if check "$d/noshell.yml" 2>/dev/null; then echo "self-test: missing shell: bash not caught" >&2; return 1; fi
  sed 's#^\( *\)mapfile -t modules.*#modules=(backend/core) \# hack/workspace-modules.sh#' "$WF_DEFAULT" > "$d/nolist.yml"
  if check "$d/nolist.yml" 2>/dev/null; then echo "self-test: go.work derivation removal not caught" >&2; return 1; fi
  sed 's#mapfile -t modules.*#for d in backend/services/*/; do#' "$WF_DEFAULT" > "$d/glob.yml"
  if check "$d/glob.yml" 2>/dev/null; then echo "self-test: hardcoded glob not caught" >&2; return 1; fi
  : > "$d/empty.yml"
  rc=0; check "$d/empty.yml" 2>/dev/null || rc=$?
  [ "$rc" = 2 ] || { echo "self-test: empty file did not yield exit 2" >&2; return 1; }
  echo "OK: evidence-pack workflow check can fail"
}

case "${1:-}" in
  --self-test) self_test ;;
  *) check "${1:-$WF_DEFAULT}" && echo "OK: evidence-pack workflow holds" ;;
esac
