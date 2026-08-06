#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if any GitHub Action is referenced by a MUTABLE ref (a tag or a branch)
# rather than by a full 40-character commit SHA.
#
# WHY THIS MATTERS MORE THAN IT LOOKS. `uses: some/action@v4` is not a version, it
# is a pointer the action's owner can move at any time — and several of the ones
# this repo depends on are exactly that. `actions/dependency-review-action@v4` is
# not even a tag; it is a BRANCH. Anyone who can push to that branch, or anyone who
# compromises that account, executes their code inside our runner with our
# GITHUB_TOKEN, on the next run, with no diff in this repo to review. That is the
# tj-actions/changed-files shape, and it is the single cheapest supply-chain
# exposure a project can close.
#
# A SHA is immutable, so the code that runs is the code that was reviewed when the
# pin landed. Dependabot still opens PRs to move these pins (see the
# `github-actions` ecosystem in .github/dependabot.yml) — the difference is that
# the change arrives as a reviewable diff rather than silently.
#
# Usage:
#   hack/check-action-pins.sh              # check .github/workflows
#   hack/check-action-pins.sh --self-test  # prove the check can fail
#
# ---------------------------------------------------------------------------
# TWO EXTRACTION TRAPS, both of which bit the first attempt at this. Read before
# "simplifying" the pattern below.
#
# 1. SUBSTRING MATCHES. Grepping for `uses:` finds `stat`+`uses: write` inside a
#    permissions block, and `ref`+`uses: "the ...` inside an English comment.
#    Neither is an action. A blind pin-everything rewrite corrupts both files.
#    Fixed by anchoring: a real reference is a YAML key at the start of its line
#    (after optional indentation and an optional "- " sequence marker), which no
#    substring of a longer word and no `#` comment can satisfy.
#
# 2. SUBPATH ACTIONS. `github/codeql-action/upload-sarif@v3` has TWO slashes. A
#    pattern shaped as `owner/repo@ref` silently skips it — the reference stays
#    unpinned and the check reports success. The character class below admits `/`
#    inside the path for exactly this reason.
# ---------------------------------------------------------------------------
set -euo pipefail

# A reference line: optional indent, optional "- ", then literally `uses:`.
# Anchoring at the start of the line is what defeats trap 1.
USES_RE='^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*'

# Scan a directory of workflow files and print "file:line:ref" for every action
# reference found. Local references (./path) are emitted too; the caller skips
# them, so that a change to what counts as "local" is visible in one place.
scan() {
  local root="$1"
  # -h would drop the filename; we need file:line to point a human at the fix.
  grep -rnE "$USES_RE" "$root" --include='*.yml' --include='*.yaml' 2>/dev/null |
    sed -E "s|($USES_RE)| |; s|[[:space:]]+| |g" |
    awk '{print $1, $2}' || true
}

check() {
  local root="${1:-.github/workflows}"
  local bad=0 total=0 pinned=0

  # Read the raw grep output rather than scan()'s trimmed form, because we need
  # both the ref AND whatever follows it on the line (the `# v7` comment).
  while IFS= read -r line; do
    local loc rest ref
    loc="${line%%:*}"                       # file
    rest="${line#*:}"; loc="$loc:${rest%%:*}"  # file:line
    rest="${rest#*:}"                       # the source line itself

    # Strip everything up to and including `uses:` plus surrounding whitespace.
    ref="$(printf '%s' "$rest" | sed -E "s|$USES_RE||")"
    local after="${ref#* }"                 # trailing text, if the line had a space
    ref="${ref%% *}"                        # the reference token itself
    [ "$after" = "$ref" ] && after=""       # no trailing text at all

    # A local reusable workflow or composite action takes a PATH, not a ref.
    # There is nothing to pin and nothing an outsider can move.
    case "$ref" in
      ./*|'') continue ;;
    esac

    total=$((total + 1))

    case "$ref" in
      *@[0-9a-f]*)
        # Must be a full 40-hex SHA — a short SHA is ambiguous and GitHub
        # rejects it, but "starts with hex" would also accept the tag `v0`
        # on a repo whose tags happen to look hexish, so match the length.
        local sha="${ref##*@}"
        if [ "${#sha}" -eq 40 ] && [ -z "${sha//[0-9a-f]/}" ]; then
          # A bare SHA is unreadable. Require the `# vX.Y.Z` comment so a
          # reviewer can tell a routine version bump from a substitution.
          case "$after" in
            \#*) pinned=$((pinned + 1)) ;;
            *)
              echo "  $loc: pinned but unlabelled: $ref"
              echo "      add a trailing comment naming the version, e.g. '# v7'"
              bad=$((bad + 1)) ;;
          esac
          continue
        fi
        ;;
    esac

    echo "  $loc: not pinned to a commit SHA: $ref"
    bad=$((bad + 1))
  done < <(grep -rnE "$USES_RE" "$root" --include='*.yml' --include='*.yaml' 2>/dev/null || true)

  if [ "$bad" -gt 0 ]; then
    echo "::error::$bad action reference(s) are not pinned to a full commit SHA."
    echo "Resolve a tag to its COMMIT sha with:"
    echo "  gh api repos/<owner>/<repo>/commits/<tag> --jq .sha"
    echo "and write it as:  uses: <owner>/<repo>@<sha> # <tag>"
    echo
    echo "Do NOT use 'gh api repos/<o>/<r>/git/ref/tags/<tag>' — for an ANNOTATED"
    echo "tag that returns the tag object's sha, which is not a commit and does"
    echo "not work in Actions. Two of this repo's pins are annotated tags."
    return 1
  fi

  echo "OK: $pinned/$total action reference(s) pinned to a commit SHA."
  return 0
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report an unpinned action, and must not cry wolf"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/wf"

  # 1. A plain unpinned tag — the everyday regression.
  cat > "$tmp/wf/a.yml" <<'EOF'
jobs:
  x:
    steps:
      - uses: actions/checkout@v7
EOF
  if out="$(check "$tmp/wf" 2>&1)"; then
    echo "  FAIL: an unpinned tag was accepted — the guard is not checking anything" >&2
    echo "  got: $out" >&2; exit 1
  fi
  case "$out" in
    *"not pinned to a commit SHA: actions/checkout@v7"*)
      echo "  ok: an unpinned tag is reported" ;;
    *) echo "  FAIL: wrong message for an unpinned tag" >&2; echo "  got: $out" >&2; exit 1 ;;
  esac

  # 2. A SUBPATH action, unpinned. This is trap 2: the obvious owner/repo@ref
  #    pattern skips it silently, so the guard would pass while the reference
  #    stayed mutable. Without this case that bug is invisible.
  cat > "$tmp/wf/a.yml" <<'EOF'
jobs:
  x:
    steps:
      - uses: github/codeql-action/upload-sarif@v3
EOF
  if out="$(check "$tmp/wf" 2>&1)"; then
    echo "  FAIL: an unpinned SUBPATH action was accepted (two slashes, skipped by a naive pattern)" >&2
    echo "  got: $out" >&2; exit 1
  fi
  echo "  ok: an unpinned subpath action is reported"

  # 3. A SHA with no version comment.
  cat > "$tmp/wf/a.yml" <<'EOF'
jobs:
  x:
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
EOF
  if check "$tmp/wf" >/dev/null 2>&1; then
    echo "  FAIL: a pinned-but-unlabelled reference was accepted" >&2; exit 1
  fi
  echo "  ok: a pin with no version comment is reported"

  # 4. THE COUNTERWEIGHT. Without it, a guard that failed unconditionally would
  #    pass every case above. This file also carries both substring traps
  #    (`statuses: write`, `refuses: "the`) and a local workflow reference — all
  #    three must be ignored, not reported.
  cat > "$tmp/wf/a.yml" <<'EOF'
permissions:
  statuses: write
jobs:
  x:
    # golangci-lint is omitted because it refuses: "the Go language version ..."
    uses: ./.github/workflows/other.yml
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7
      - uses: github/codeql-action/upload-sarif@c4dd10e44af883a891fe31ced449bcb4a6728b9b # v3
EOF
  if ! out="$(check "$tmp/wf" 2>&1)"; then
    echo "  FAIL: correctly pinned references were rejected — the guard cries wolf" >&2
    echo "  got: $out" >&2; exit 1
  fi
  case "$out" in
    *"2/2 action reference(s) pinned"*)
      echo "  ok: correct pins pass, and the substring/local traps are not counted" ;;
    *)
      echo "  FAIL: expected exactly 2 counted references (the substring traps and the" >&2
      echo "        local ./ reference must not be counted)" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  echo "==> Self-test passed"
  exit 0
fi

check "${1:-.github/workflows}"
