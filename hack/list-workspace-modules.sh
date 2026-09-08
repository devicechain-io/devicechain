#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Prints every module go.work declares, one per line, as go.work writes it
# (`./backend/core`). This is the TOOLCHAIN-FREE reader of the workspace, and that is
# its whole reason to exist: two callers need to know the module set without being
# able to (or wanting to) depend on the go command.
#
#   - .github/workflows/ci.yml cross-checks `go list -m` against it. A derived matrix
#     fails SILENTLY — a strategy.matrix over zero entries runs zero jobs, all of which
#     "succeed" by not existing — so the count is confirmed by a second reader that
#     shares no code with the first. That is only worth anything while the second
#     reader is genuinely independent, hence awk over the file rather than `go list`.
#   - hack/check-dependabot-modules.sh compares the same set against dependabot.yml,
#     from a job with no Go toolchain installed at all.
#
# 🔴 IT PARSES BOTH `use` FORMS, AND THE SINGLE-LINE ONE IS WHY THIS IS A FILE RATHER
# THAN A ONE-LINE awk COPIED INTO TWO PLACES. `use ./backend/tools/subconfirm` on its
# own line is valid go.work syntax that the block-only reader (the shape both callers
# had) counts as ZERO. In ci.yml that meant a VALID go.work failing the cross-check
# with "24 vs 23"; in the dependabot guard it meant a module silently dropping out of
# the coverage comparison, which is the direction that does not announce itself. The
# tree happens to use the parenthesised form today, so nothing was broken — it was a
# false positive (and a false negative) waiting for whoever next edits go.work by hand.
# --self-test pins both forms.

set -euo pipefail

list_workspace_modules() {
  awk '
    # A `use (` block opens; entries follow until the closing paren.
    /^[[:space:]]*use[[:space:]]*\(/ { inuse = 1; next }
    inuse && /^[[:space:]]*\)/       { inuse = 0; next }
    inuse {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "")
      if ($0 == "" || $0 ~ /^\/\//) next
      print $1
      next
    }
    # The single-line form: `use ./dir`. The character class rules out `use (`, which
    # the block rule above has already consumed anyway, and requires at least one
    # space so `use(` cannot be mistaken for a path either.
    /^[[:space:]]*use[[:space:]]+[^([:space:]]/ { print $2 }
  ' "${1:-go.work}"
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: both use forms, and the comments/blank lines between them"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  # Every shape at once: a block, a comment and a blank line inside it, a trailing
  # comment on an entry, and two single-line uses — one before the block and one after,
  # since a reader with a sticky in-block flag would get one of those wrong.
  cat > "$tmp/go.work" <<'EOF'
go 1.26.6

use ./backend/cli

use (
	// a comment inside the block

	./backend/core
	./backend/services/device-management // and one trailing an entry
)

use ./deploy
EOF
  got="$(list_workspace_modules "$tmp/go.work" | tr '\n' ' ')"
  want="./backend/cli ./backend/core ./backend/services/device-management ./deploy "
  if [ "$got" != "$want" ]; then
    echo "  FAIL: parsed [$got], want [$want]" >&2
    exit 1
  fi
  echo "  ok: single-line and block uses both parsed, comments and blanks skipped"

  # The negative control. A reader that counts every non-empty line would pass the
  # case above too, so prove the block delimiters are actually honoured: nothing
  # outside a `use` may be reported.
  cat > "$tmp/nouse.work" <<'EOF'
go 1.26.6

toolchain go1.26.6

// ./backend/core is named here in a comment and must not be counted
replace example.com/x => ./backend/core
EOF
  if [ -n "$(list_workspace_modules "$tmp/nouse.work")" ]; then
    echo "  FAIL: reported a module from a file with no use directive — the reader is" >&2
    echo "        matching paths anywhere rather than parsing use blocks" >&2
    exit 1
  fi
  echo "  ok: a go.work with no use directive reports nothing"
  exit 0
fi

cd "$(dirname "$0")/.."
list_workspace_modules "${1:-go.work}"
