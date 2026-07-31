#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if the workspace (go.work) and Dependabot's gomod `directories` list have
# drifted apart in either direction.
#
# This exists because a module Dependabot cannot see does not merely go stale — it
# BREAKS. Every module outside core carries
# `replace github.com/devicechain-io/dc-microservice => ../../core`, and CI validates
# each module with GOWORK=off, so the module resolves against the local core on disk.
# When Dependabot moves core's requirements without moving a module that replaces it,
# that module's own go.mod/go.sum are stale relative to the core they point at and the
# build fails with `go: updates to go.mod needed`. The module that fails is never the
# one that was bumped; it is the one that was invisible.
#
# That is not hypothetical. The list omitted /backend/edge/*, /backend/sims/* and
# /backend/tools/*, so every weekly gomod PR broke the same four modules — and those
# four received no dependency or security updates at all for as long as it stood.
#
# The reverse direction is checked too: a pattern matching NO module is how a renamed
# or removed tree leaves a rule behind that silently protects nothing.
#
#   hack/check-dependabot-modules.sh              # check
#   hack/check-dependabot-modules.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Modules as go.work itself declares them, normalised from `./backend/core` to
# `/backend/core` so they compare directly against Dependabot's leading-slash paths.
# go.work is parsed rather than `go list -m` so the gate needs no toolchain and can
# be wired into any job.
workspace_modules() {
  awk '
    /^use[[:space:]]*\(/ { inuse = 1; next }
    inuse && /^\)/       { inuse = 0; next }
    inuse {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "")
      if ($0 == "" || $0 ~ /^\/\//) next
      sub(/^\./, "")
      print
    }
  ' "${1:-go.work}"
}

# The `directories` entries of the gomod ecosystem block ONLY. Walking the whole file
# would pick up the npm/docker/actions paths and pass this gate for the wrong reason.
dependabot_gomod_dirs() {
  awk '
    /^[[:space:]]*-[[:space:]]*package-ecosystem:/ {
      ingomod = ($0 ~ /"gomod"/) ? 1 : 0
      indirs = 0
      next
    }
    !ingomod { next }
    /^[[:space:]]*directories:[[:space:]]*$/ { indirs = 1; next }
    # Any other key at the same level ends the list.
    /^[[:space:]]*[a-z][a-z-]*:/ { indirs = 0 }
    indirs && /^[[:space:]]*-[[:space:]]*"/ {
      match($0, /"[^"]*"/)
      print substr($0, RSTART + 1, RLENGTH - 2)
    }
  ' "${1:-.github/dependabot.yml}"
}

# Dependabot globs are path-segment scoped: `*` stands for one segment, not for an
# arbitrary run of characters. Bash `case` would let `*` cross a `/` and report a
# module as covered when it is not — a false PASS in exactly the direction this gate
# exists to catch — so the pattern is compiled to an anchored regex instead.
pattern_to_regex() {
  local p="$1" out=""
  local i ch
  for ((i = 0; i < ${#p}; i++)); do
    ch="${p:i:1}"
    case "$ch" in
      '*') out+='[^/]*' ;;
      '.' | '+' | '?' | '(' | ')' | '[' | ']' | '{' | '}' | '^' | '$' | '|' | '\') out+="\\$ch" ;;
      *) out+="$ch" ;;
    esac
  done
  printf '^%s$' "$out"
}

# Prints one line per problem; empty output is the passing case.
check() {
  local workfile="${1:-go.work}" cfgfile="${2:-.github/dependabot.yml}"
  local -a modules patterns
  mapfile -t modules < <(workspace_modules "$workfile")
  mapfile -t patterns < <(dependabot_gomod_dirs "$cfgfile")

  # A parse that silently yields nothing would make every comparison below vacuous
  # and the gate would pass loudest exactly when it understood least.
  if [ ${#modules[@]} -eq 0 ]; then
    echo "unparseable: no modules found in $workfile"
    return 0
  fi
  if [ ${#patterns[@]} -eq 0 ]; then
    echo "unparseable: no gomod directories found in $cfgfile"
    return 0
  fi

  local m p rx matched
  for m in "${modules[@]}"; do
    matched=0
    for p in "${patterns[@]}"; do
      rx="$(pattern_to_regex "$p")"
      if [[ "$m" =~ $rx ]]; then matched=1; break; fi
    done
    [ "$matched" -eq 1 ] || echo "uncovered module: $m"
  done

  for p in "${patterns[@]}"; do
    rx="$(pattern_to_regex "$p")"
    matched=0
    for m in "${modules[@]}"; do
      if [[ "$m" =~ $rx ]]; then matched=1; break; fi
    done
    [ "$matched" -eq 1 ] || echo "pattern matches no module: $p"
  done
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report drift in both directions"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  cat > "$tmp/go.work" <<'EOF'
go 1.26.5

use (
	./backend/core
	./backend/services/device-management
	./backend/tools/drdrill
)
EOF

  # 1. A module no pattern covers — the failure that actually happened.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "gomod"
    directories:
      - "/backend/core"
      - "/backend/services/*"
    schedule:
      interval: "weekly"
EOF
  out="$(check "$tmp/go.work" "$tmp/db.yml")"
  case "$out" in
    *"uncovered module: /backend/tools/drdrill"*)
      echo "  ok: an uncovered module is reported" ;;
    *)
      echo "  FAIL: the guard did not notice an uncovered module — it is not checking anything" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  # 2. The counterweight. Without it the guard could report drift unconditionally
  #    and case 1 would still pass.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "gomod"
    directories:
      - "/backend/core"
      - "/backend/services/*"
      - "/backend/tools/*"
    schedule:
      interval: "weekly"
EOF
  out="$(check "$tmp/go.work" "$tmp/db.yml")"
  if [ -n "$out" ]; then
    echo "  FAIL: the guard reports drift on a fully covered workspace — it would fail every build" >&2
    echo "  got: $out" >&2; exit 1
  fi
  echo "  ok: a fully covered workspace produces no finding"

  # 3. A pattern left behind by a renamed tree.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "gomod"
    directories:
      - "/backend/core"
      - "/backend/services/*"
      - "/backend/tools/*"
      - "/backend/gone/*"
    schedule:
      interval: "weekly"
EOF
  out="$(check "$tmp/go.work" "$tmp/db.yml")"
  case "$out" in
    *"pattern matches no module: /backend/gone/*"*)
      echo "  ok: a pattern covering nothing is reported" ;;
    *)
      echo "  FAIL: a stale pattern went unreported" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  # 4. `*` must not cross a path separator. `/backend/*` looks like it covers
  #    /backend/tools/drdrill under shell glob rules, and does not under
  #    Dependabot's — the false PASS this gate would otherwise hand out.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "gomod"
    directories:
      - "/backend/*"
    schedule:
      interval: "weekly"
EOF
  out="$(check "$tmp/go.work" "$tmp/db.yml")"
  case "$out" in
    *"uncovered module: /backend/tools/drdrill"*)
      echo "  ok: a single-segment glob does not silently cover a nested module" ;;
    *)
      echo "  FAIL: '*' crossed a path separator — nested modules would read as covered" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  # 5. Only the gomod block counts. An npm entry listing /frontend must not be
  #    borrowed to satisfy a Go module.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "npm"
    directories:
      - "/backend/tools/*"
  - package-ecosystem: "gomod"
    directories:
      - "/backend/core"
      - "/backend/services/*"
    schedule:
      interval: "weekly"
EOF
  out="$(check "$tmp/go.work" "$tmp/db.yml")"
  case "$out" in
    *"uncovered module: /backend/tools/drdrill"*)
      echo "  ok: another ecosystem's directories do not count as coverage" ;;
    *)
      echo "  FAIL: a non-gomod ecosystem's paths were counted — the parse is too loose" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  echo "==> Self-test passed"
  exit 0
fi

findings="$(check)"
if [ -n "$findings" ]; then
  echo "::error::go.work and .github/dependabot.yml have drifted apart."
  echo "$findings"
  echo
  echo "Every workspace module must appear in the gomod ecosystem's 'directories'."
  echo "A module Dependabot cannot see is not merely skipped: it carries a local"
  echo "replace onto core, so the next core bump leaves its go.mod stale and CI"
  echo "fails there with 'go: updates to go.mod needed'."
  exit 1
fi

echo "ok: all $(workspace_modules | wc -l) workspace modules are covered by Dependabot's gomod directories"
