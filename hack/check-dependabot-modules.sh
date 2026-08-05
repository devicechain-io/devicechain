#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if the trees this repo actually contains and the trees Dependabot is told
# about have drifted apart in either direction. Two ecosystems are gated:
#
#   gomod — every module in go.work
#   npm   — every package-lock.json in the repo
#
# Each has a source of truth ON DISK, which is the only reason this gate is worth
# anything: it compares the config against the tree rather than against a second
# copy of the same list.
#
# GOMOD — a module Dependabot cannot see does not merely go stale, it BREAKS. Every
# module outside core carries `replace github.com/devicechain-io/dc-microservice =>
# ../../core`, and CI validates each module with GOWORK=off, so the module resolves
# against the local core on disk. When Dependabot moves core's requirements without
# moving a module that replaces it, that module's own go.mod/go.sum are stale
# relative to the core they point at and the build fails with `go: updates to go.mod
# needed`. The module that fails is never the one that was bumped; it is the one that
# was invisible.
#
# That is not hypothetical. The list omitted /backend/edge/*, /backend/sims/* and
# /backend/tools/*, so every weekly gomod PR broke the same four modules — and those
# four received no dependency or security updates at all for as long as it stood.
#
# NPM — the failure is quieter and therefore worse. Security updates come from the
# dependency graph and need no config entry, so an unlisted npm tree still raises
# alerts and still gets transitive CVE fixes; what it never gets is a VERSION update.
# The tree ages with nothing to say so, and the arriving security PRs make the queue
# look healthy. /docs sat in exactly that state — listed nowhere, five security PRs
# in flight, Docusaurus itself drifting.
#
# Note the shape those two paragraphs share. Both times the config asserted a
# completeness that nothing measured, and both times the ecosystem that broke was
# the one the previous fix had not thought to cover — which is why this gate reads
# the filesystem for BOTH rather than gating gomod and trusting npm.
#
# The reverse direction is checked too: a pattern matching NO tree is how a renamed
# or removed directory leaves a rule behind that silently protects nothing.
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

# Every npm tree in the repo, one per lockfile, normalised to a leading slash so the
# paths compare directly against Dependabot's. A lockfile is the right unit because
# it is exactly what Dependabot resolves against: frontend/ is one npm workspace with
# one lockfile covering apps/* and packages/*, and it wants ONE entry, not one per
# workspace member.
#
# The exclusions are NOT equally load-bearing, and saying so honestly is the point:
#
#   node_modules — real and tested. An installed dependency may ship its own lockfile,
#     and a tree that does would be reported as an uncovered npm tree the moment anyone
#     ran `npm ci`. Nothing here vendors one TODAY, which is exactly why the self-test
#     plants one rather than trusting the live tree; remove this line and that case
#     fails.
#   _legacy — defensive only. The archived pre-migration tree currently contains no npm
#     content at all, so this exclusion excludes nothing and no test covers it. It is
#     here so that restoring anything with a lockfile under _legacy does not start
#     demanding Dependabot coverage for a tree that is deliberately not built.
#
# 🔴 Blind spot, stated because a gate that looks thorough is worse than one whose
# limits are written down: only `package-lock.json` is discovered. A yarn or pnpm tree
# is an npm-ecosystem tree to Dependabot and is invisible here, so it would replay the
# /docs failure with nothing to say so. None exists today; add the lockfile name here
# on the day one does.
npm_trees() {
  local root="${1:-.}"
  find "$root" -name package-lock.json \
    -not -path '*/node_modules/*' \
    -not -path '*/_legacy/*' \
    -not -path '*/.git/*' \
    -printf '%h\n' 2>/dev/null |
    sed -e "s|^${root%/}||" -e 's|^\./||' -e 's|^\([^/]\)|/\1|' -e 's|^$|/|' |
    sort
}

# The directory entries of ONE ecosystem block. Walking the whole file would pick up
# the other ecosystems' paths and pass this gate for the wrong reason — a gomod module
# would read as covered because an npm entry happened to name the same path.
#
# Both spellings are accepted: the `directories:` list and the singular `directory:`,
# since Dependabot takes either and a config that switches between them must not
# silently stop being gated.
dependabot_dirs() {
  local eco="${1:?ecosystem}"
  awk -v eco="\"$eco\"" '
    /^[[:space:]]*-[[:space:]]*package-ecosystem:/ {
      inblock = (index($0, eco) > 0) ? 1 : 0
      indirs = 0
      next
    }
    !inblock { next }
    /^[[:space:]]*directories:[[:space:]]*$/ { indirs = 1; next }
    # The singular form carries its value on the same line, so it is matched and
    # printed here rather than accumulated. It sits above the end-of-list rule for
    # readability only — that rule sets a flag and does not `next`, so awk would run
    # this one either way. What matters is that this rule EXISTS, not where it sits.
    /^[[:space:]]*directory:[[:space:]]*"/ {
      indirs = 0
      match($0, /"[^"]*"/)
      print substr($0, RSTART + 1, RLENGTH - 2)
      next
    }
    # Any other key at the same level ends the list.
    /^[[:space:]]*[a-z][a-z-]*:/ { indirs = 0 }
    indirs && /^[[:space:]]*-[[:space:]]*"/ {
      match($0, /"[^"]*"/)
      print substr($0, RSTART + 1, RLENGTH - 2)
    }
  ' "${2:-.github/dependabot.yml}"
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

# Compares one ecosystem's configured directories against the paths that actually
# exist on disk, in both directions. Prints one line per problem; empty output is the
# passing case.
compare() {
  local noun="$1" eco="$2" cfgfile="$3" actual_list="$4" source="$5"
  local -a actual patterns
  mapfile -t actual < <(printf '%s' "$actual_list" | grep -v '^$' || true)
  mapfile -t patterns < <(dependabot_dirs "$eco" "$cfgfile")

  # A parse that silently yields nothing would make every comparison below vacuous
  # and the gate would pass loudest exactly when it understood least. Naming the
  # SOURCE matters here: the two ecosystems fail this way for unrelated reasons —
  # an unreadable go.work versus a `find` that returned nothing — and a message that
  # said only "on disk" would misdescribe the first and misdirect the reader.
  if [ ${#actual[@]} -eq 0 ]; then
    echo "unparseable: no ${noun}s found in $source"
    return 0
  fi
  if [ ${#patterns[@]} -eq 0 ]; then
    echo "unparseable: no $eco directories found in $cfgfile"
    return 0
  fi

  local a p rx matched
  for a in "${actual[@]}"; do
    matched=0
    for p in "${patterns[@]}"; do
      rx="$(pattern_to_regex "$p")"
      if [[ "$a" =~ $rx ]]; then matched=1; break; fi
    done
    [ "$matched" -eq 1 ] || echo "uncovered $noun: $a"
  done

  for p in "${patterns[@]}"; do
    rx="$(pattern_to_regex "$p")"
    matched=0
    for a in "${actual[@]}"; do
      if [[ "$a" =~ $rx ]]; then matched=1; break; fi
    done
    [ "$matched" -eq 1 ] || echo "pattern matches no $noun: $p"
  done
}

check_gomod() {
  local workfile="${1:-go.work}"
  compare "module" "gomod" "${2:-.github/dependabot.yml}" "$(workspace_modules "$workfile")" "$workfile"
}

check_npm() {
  local root="${1:-.}"
  compare "npm tree" "npm" "${2:-.github/dependabot.yml}" "$(npm_trees "$root")" "the tree under $root"
}

check() {
  check_gomod "${1:-go.work}" "${2:-.github/dependabot.yml}"
  check_npm "${3:-.}" "${2:-.github/dependabot.yml}"
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
  out="$(check_gomod "$tmp/go.work" "$tmp/db.yml")"
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
  out="$(check_gomod "$tmp/go.work" "$tmp/db.yml")"
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
  out="$(check_gomod "$tmp/go.work" "$tmp/db.yml")"
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
  out="$(check_gomod "$tmp/go.work" "$tmp/db.yml")"
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
  out="$(check_gomod "$tmp/go.work" "$tmp/db.yml")"
  case "$out" in
    *"uncovered module: /backend/tools/drdrill"*)
      echo "  ok: another ecosystem's directories do not count as coverage" ;;
    *)
      echo "  FAIL: a non-gomod ecosystem's paths were counted — the parse is too loose" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  # ---- npm ---------------------------------------------------------------
  # The npm half needs a tree on disk rather than a manifest to parse, so these
  # cases build one. /frontend gets a nested workspace member WITHOUT its own
  # lockfile, because that is the real shape: one entry must cover the workspace,
  # and a gate that counted package.json files instead would demand an entry per
  # member and be wrong in the opposite direction.
  mkdir -p "$tmp/tree/frontend/apps/console" "$tmp/tree/docs" "$tmp/tree/frontend/node_modules/left-pad"
  : > "$tmp/tree/frontend/package-lock.json"
  : > "$tmp/tree/docs/package-lock.json"
  : > "$tmp/tree/frontend/apps/console/package.json"
  : > "$tmp/tree/frontend/node_modules/left-pad/package-lock.json"

  # 6. A lockfile no entry covers — the /docs failure, which produced no alert of
  #    its own because security updates kept arriving from the dependency graph.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "npm"
    directory: "/frontend"
    schedule:
      interval: "weekly"
EOF
  out="$(check_npm "$tmp/tree" "$tmp/db.yml")"
  case "$out" in
    *"uncovered npm tree: /docs"*)
      echo "  ok: an npm tree with no entry is reported" ;;
    *)
      echo "  FAIL: an unlisted npm tree went unreported — /docs would drift again" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  # 7. The counterweight. Without it the guard could report drift unconditionally and
  #    case 6 would still pass. It also proves node_modules is not walked: the tree
  #    plants a lockfile under frontend/node_modules, and dropping that exclusion
  #    fails HERE rather than anywhere else.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "npm"
    directories:
      - "/frontend"
      - "/docs"
    schedule:
      interval: "weekly"
EOF
  out="$(check_npm "$tmp/tree" "$tmp/db.yml")"
  if [ -n "$out" ]; then
    echo "  FAIL: the guard reports drift on fully covered npm trees — it would fail every build" >&2
    echo "  got: $out" >&2; exit 1
  fi
  echo "  ok: fully covered npm trees produce no finding (and node_modules is not walked)"

  # 7b. The singular `directory:` spelling, COVERING EVERYTHING, which is the only
  #     shape that can tell a working parse from a corrupted one.
  #
  #     Case 6 uses the singular spelling too, but it cannot carry this claim: delete
  #     the singular rule entirely and case 6 still fails, just via the vacuity guard
  #     ("no npm directories found") instead of the finding it names. What survives
  #     case 6 is a parse that returns something WRONG rather than nothing — quotes
  #     left on the value, say — because a garbage pattern still fails to match /docs
  #     and still produces an `uncovered` line. Only asserting SILENCE over a tree the
  #     singular entry genuinely covers can distinguish the two, and that assertion
  #     has to be made against a config whose coverage is spelled the singular way.
  mkdir -p "$tmp/solo/docs"
  : > "$tmp/solo/docs/package-lock.json"
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "npm"
    directory: "/docs"
    schedule:
      interval: "weekly"
EOF
  out="$(check_npm "$tmp/solo" "$tmp/db.yml")"
  if [ -n "$out" ]; then
    echo "  FAIL: a singular 'directory:' entry did not register as coverage — the value is being parsed wrong" >&2
    echo "  got: $out" >&2; exit 1
  fi
  echo "  ok: the singular 'directory:' spelling is parsed, not merely tolerated"

  # 8. An entry pointing at a tree that no longer has a lockfile.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "npm"
    directories:
      - "/frontend"
      - "/docs"
      - "/website"
    schedule:
      interval: "weekly"
EOF
  out="$(check_npm "$tmp/tree" "$tmp/db.yml")"
  case "$out" in
    *"pattern matches no npm tree: /website"*)
      echo "  ok: an npm entry covering nothing is reported" ;;
    *)
      echo "  FAIL: a stale npm entry went unreported" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  # 9. The ecosystems must not borrow each other's paths. A gomod block naming
  #    /docs is not npm coverage — this is case 5 in the other direction, and the
  #    reason both checks read the ecosystem key rather than scanning the file.
  cat > "$tmp/db.yml" <<'EOF'
updates:
  - package-ecosystem: "gomod"
    directories:
      - "/docs"
  - package-ecosystem: "npm"
    directories:
      - "/frontend"
    schedule:
      interval: "weekly"
EOF
  out="$(check_npm "$tmp/tree" "$tmp/db.yml")"
  case "$out" in
    *"uncovered npm tree: /docs"*)
      echo "  ok: a gomod entry does not count as npm coverage" ;;
    *)
      echo "  FAIL: another ecosystem's paths were counted as npm coverage" >&2
      echo "  got: $out" >&2; exit 1 ;;
  esac

  echo "==> Self-test passed"
  exit 0
fi

findings="$(check)"
if [ -n "$findings" ]; then
  echo "::error::this repository and .github/dependabot.yml have drifted apart."
  echo "$findings"
  echo
  echo "Every workspace module must appear in the gomod ecosystem's 'directories',"
  echo "and every package-lock.json in the npm ecosystem's. Neither omission is a"
  echo "mere skip:"
  echo
  echo "  gomod — the module carries a local replace onto core, so the next core"
  echo "          bump leaves its go.mod stale and CI fails there with"
  echo "          'go: updates to go.mod needed'."
  echo "  npm   — the tree keeps receiving SECURITY updates (those come from the"
  echo "          dependency graph, not from this file) and silently stops"
  echo "          receiving version updates, so it ages with nothing to say so."
  exit 1
fi

echo "ok: $(workspace_modules | wc -l) workspace modules and $(npm_trees | wc -l) npm trees are covered by Dependabot"
