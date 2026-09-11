#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Refuses a `helm_release` that does not bound its retained revision history.
#
# 🔴 WHY THIS IS A GATE AT ALL. The Helm provider documents `max_history` as
# "Defaults to 0 (no limit)", where the `helm` command passes 10. A release that
# leaves it unset therefore keeps every revision it has ever had — not as a
# configured choice, but because nobody wrote a number down. Each revision holds the
# values it was rendered with, so the record grows without limit and a value that
# changes is not retracted from the ones already written.
#
# 🔴 WHY A GATE RATHER THAN "WE SET IT EVERYWHERE". Because we did not: the bound
# was first written for the one release dcctl drives in-process, and the seven here
# were unset. A count of releases is also not stable — this tree has gained and lost
# them — and the enumeration that found these seven was done by hand, which is
# exactly the kind of list that is right once. The next `helm_release` is added by
# someone who has not read this file.
#
# ⚠️ WHAT IT CANNOT SEE, stated rather than left for a reviewer to find:
#
#   - It is a text scan, not an HCL parse. A `max_history` inside a comment, or in a
#     different resource block that happens to fall inside the window, counts. The
#     window is deliberately generous for that reason: a false PASS here is a missing
#     bound, so the scan errs toward accepting, and the real protection against a
#     wrong value is that `local.helm_max_history` is declared once per module.
#   - It says nothing about the VALUE. `max_history = 0` is spelled the same as
#     unlimited and would pass. Nothing in this tree writes one, and refusing a
#     literal zero would also refuse a legitimate variable reference.
#   - It covers `deploy/opentofu` only. A Helm release created from Go (the chart
#     dcctl installs) is bounded in code and pinned by its own test.
#
#   hack/check-helm-release-history.sh
#   hack/check-helm-release-history.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCAN_DIR="${ROOT}/deploy/opentofu"

# scan_tree walks every .tf file under $1 and prints "file:line" for each
# helm_release block that does not set max_history before the block ends.
scan_tree() {
  local dir="$1" rc=0
  local file
  while IFS= read -r file; do
    awk -v F="$file" '
      /^resource[[:space:]]+"helm_release"/ { inblock=1; depth=0; found=0; start=NR; name=$0 }
      inblock {
        n = gsub(/{/, "{"); depth += n
        n = gsub(/}/, "}"); depth -= n
        if ($0 ~ /max_history[[:space:]]*=/) found=1
        if (depth <= 0) {
          if (!found) { printf "%s:%d: %s\n", F, start, name; rc=1 }
          inblock=0
        }
      }
      END { exit rc }
    ' "$file" || rc=1
  done < <(find "$dir" -name '*.tf' -not -path '*/.terraform/*' | sort)
  return "$rc"
}

if [[ "${1:-}" == "--self-test" ]]; then
  # 🔑 The gate is worth nothing until it has been shown to fail. Build a tree
  # holding one bounded release and one unbounded one, and require that the scan
  # accepts the first and refuses the second — a check that only ever passes and a
  # check that only ever fails are the same useless instrument.
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  mkdir -p "$tmp/good" "$tmp/bad"
  cat > "$tmp/good/main.tf" <<'EOF'
resource "helm_release" "fine" {
  name        = "x"
  max_history = local.helm_max_history
}
EOF
  cat > "$tmp/bad/main.tf" <<'EOF'
resource "helm_release" "unbounded" {
  name = "x"
  set {
    name = "nested.block"
  }
}
EOF

  if ! scan_tree "$tmp/good" >/dev/null; then
    echo "SELF-TEST FAILED: the check refused a release that sets max_history." >&2
    exit 1
  fi
  if scan_tree "$tmp/bad" >/dev/null; then
    echo "SELF-TEST FAILED: the check accepted a release with no max_history, so a" >&2
    echo "green run from it would mean nothing." >&2
    exit 1
  fi
  echo "==> Self-test passed: the check accepts a bounded release and refuses an unbounded one."
  exit 0
fi

if ! out="$(scan_tree "$SCAN_DIR")"; then
  echo "These helm_release blocks do not bound their retained history:" >&2
  echo "$out" >&2
  echo >&2
  echo "The provider's default is 0, which means keep every revision forever. Set" >&2
  echo "  max_history = local.helm_max_history" >&2
  echo "declaring the local in the module if it does not have one." >&2
  exit 1
fi

echo "==> Every helm_release bounds its retained history."
