#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: the narrowed public claims (Substrate Constitution draft, CON-12).
#
# WHY. A smart-city platform is judged on what it says about surveillance, AI and
# safety, and the first over-claim found by a hostile reader costs more than every
# accurate page earns. The Constitution draft fixes a short list of claims the
# project must NEVER make on a published surface, each with the honest wording to
# use instead, once the evidence for it exists:
#
#   Never say                                Say instead
#   "unable to become a surveillance ..."    "nothing can be done silently"
#   "byte-identical replay of AI decisions"  "deterministic replay of everything
#                                             downstream of a recorded inference"
#   "operator-blind"                         "operator access is tenant-approved,
#                                             tenant-visible and tamper-evident"
#   "device-verified life-safety actuation"  "supervisory only; the local
#                                             controller keeps the interlock"
#   "agents run the city"                    "agents assist operators, inside
#                                             budgets a human set"
#   "purpose-bound sharing"                  "purpose recorded (not enforced)"
#
# This catches the left column. It cannot judge whether the right column's
# evidence exists yet; that stays a review question.
#
# Surfaces: everything a stranger is served — the docs site (living and frozen,
# every locale), docs-drafts, the repository README, the npm package readmes, the
# Helm chart README, the published release highlights, and the console and
# dashboard UI strings.
#
# LIMIT, stated so the check is not read as more than it is: the patterns are
# English plus the Spanish form of "operator-blind". A translated over-claim in
# another wording is not caught; an untranslated English phrase in any locale is.
#
#   hack/check-docs-claims.sh              # check
#   hack/check-docs-claims.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGETS=(
  docs/docs docs/versioned_docs docs/versioned_sidebars docs/i18n docs/blog docs/src
  docs-drafts
  README.md
  deploy/helm/devicechain/README.md
  .github/release-highlights.json
  frontend/apps/console/src/i18n/locales
  frontend/apps/dashboard/src/i18n/locales
)
for readme in frontend/packages/*/README.md; do TARGETS+=("$readme"); done

# Each entry: a short label, a TAB, a case-insensitive extended regex. Narrow on
# purpose: a pattern that also matches the honest wording would fail every build
# and teach people to ignore it, so the self-test checks the honest wording too.
CLAIMS=(
  "operator-blind	operator[- ]blind|cieg[oa]s? (al|para el|frente al) operador"
  "unable to surveil	(unable|impossible|cannot|can ?not|can't|(can|could|will) never|never able) (to )?(ever )?(become|be turned into|be used as|serve as) an? (mass[- ])?surveillance"
  "replay of AI decisions	(byte[- ]identical|bit[- ]exact|bit[- ]for[- ]bit|exact|deterministic) replay of (the |an? )?(AI|LLM|model|agent)s?([' ]|$)"
  "life-safety actuation	(device[- ]verified|certified|guaranteed) life[- ]safety (actuation|control)"
  "agents run the city	(AI|agents?|the agents?) (runs?|operates?|controls?) (the|your|a|every) (city|cities)"
  "purpose-bound	purpose[- ]bound"
)

# files PATH... lists the text files under each existing PATH (a file or a
# directory), NUL-separated. Images and other binaries are not prose.
files() {
  local p
  for p in "$@"; do
    [ -e "$p" ] || continue
    find "$p" -name node_modules -prune -o -type f \
      \( -name '*.md' -o -name '*.mdx' -o -name '*.json' -o -name '*.ts' -o -name '*.tsx' \
         -o -name '*.js' -o -name '*.jsx' -o -name '*.yml' -o -name '*.yaml' -o -name '*.txt' \) -print0
  done
}

# scan PATH... prints "file:line: [label] text" for every hit, nothing when clean,
# and exits 2 when a file cannot be read.
#
# It matches PARAGRAPHS, not lines: consecutive non-blank lines are joined with
# single spaces first, so a claim that Markdown wrapping splits over two lines
# ("Agents run the" / "city.") is still one claim. The line reported is the line
# the paragraph starts on.
scan() {
  local list=() f
  while IFS= read -r -d '' f; do list+=("$f"); done < <(files "$@")
  [ ${#list[@]} -eq 0 ] && return 0
  CLAIMS_TSV="$(printf '%s\n' "${CLAIMS[@]}")" awk '
    BEGIN {
      n = split(ENVIRON["CLAIMS_TSV"], rows, "\n")
      for (i = 1; i <= n; i++) {
        if (rows[i] == "") continue
        t = index(rows[i], "\t")
        k++; label[k] = substr(rows[i], 1, t - 1); re[k] = tolower(substr(rows[i], t + 1))
      }
    }
    function flush(   i, p) {
      if (para != "") {
        p = tolower(para)
        for (i = 1; i <= k; i++)
          if (p ~ re[i]) print file ":" start ": [" label[i] "] " substr(para, 1, 200)
      }
      para = ""
    }
    FNR == 1 { flush(); file = FILENAME; prose = (FILENAME ~ /\.(md|mdx|txt)$/) }
    /^[ \t\r]*$/ { flush(); next }
    {
      line = $0; sub(/\r$/, "", line); gsub(/[ \t]+/, " ", line); sub(/^ /, "", line)
      if (para == "") { para = line; start = FNR; lines = 1 } else { para = para " " line; lines++ }
      # Structured files (JSON, code, YAML) hold one string per line, not wrapped
      # prose, so they are matched line by line; a long prose paragraph is cut at 40
      # lines so a pathological file cannot make the join quadratic.
      if (!prose || lines >= 40) flush()
    }
    END { flush() }
  ' "${list[@]}" || { echo "check-docs-claims: could not read the scanned files" >&2; return 2; }
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must report each planted claim, alone"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/docs"
  fails=0

  # Each forbidden phrasing is planted ALONE: a self-test that plants all of them at
  # once is satisfied by a checker that finds any one.
  forbidden=(
    "The platform is operator-blind."
    "Una plataforma ciega al operador."
    "DeviceChain is unable to become a surveillance machine."
    "It can never be used as a mass surveillance tool."
    "Byte-identical replay of AI decisions."
    "We offer deterministic replay of the model's output."
    "Device-verified life-safety actuation."
    "Agents run the city."
    "Your AI operates every city district."
    "Purpose-bound sharing between agencies."
    "Agents run the
city."
    "We promise byte-identical replay
of AI decisions."
  )
  for line in "${forbidden[@]}"; do
    printf 'Intro.\n\n%s\n' "$line" > "$tmp/docs/planted.md"
    hit="$(scan "$tmp/docs")"
    case "$hit" in
      *planted.md:3:*) echo "  ok: detected: $line" ;;
      *) echo "  FAIL: not detected (or wrong line): $line" >&2; fails=1 ;;
    esac
  done

  # The honest wording must pass, or the guard fails every build that adopts it.
  allowed=(
    "Nothing can be done silently: every read and every operator entry is tamper-evident."
    "Deterministic replay of everything downstream of a recorded inference."
    "Operator access is tenant-approved, tenant-visible and tamper-evident."
    "Supervisory only. The local controller keeps the interlock."
    "Agents assist operators, inside budgets a human set."
    "Purpose recorded (not enforced)."
    "Exact replay of the event stream rebuilds the alarm state."
    "The operator must not be blind to a stalled consumer."
  )
  for line in "${allowed[@]}"; do
    printf '%s\n' "$line" > "$tmp/docs/planted.md"
    if [ -n "$(scan "$tmp/docs")" ]; then
      echo "  FAIL: honest wording reported: $line" >&2; fails=1
    else
      echo "  ok: allowed: $line"
    fi
  done

  # An unreadable file is an error (exit 2), never a clean pass.
  printf 'Agents assist operators.
' > "$tmp/docs/unreadable.md"
  chmod 000 "$tmp/docs/unreadable.md"
  if [ -r "$tmp/docs/unreadable.md" ]; then
    echo "  skip: cannot make a file unreadable here (running as root?)"
  else
    set +e; ( scan "$tmp/docs" ) >/dev/null 2>&1; st=$?; set -e
    if [ "$st" -eq 2 ]; then echo "  ok: an unreadable file exits 2"
    else echo "  FAIL: an unreadable file gave status $st, not 2" >&2; fails=1; fi
  fi
  chmod 644 "$tmp/docs/unreadable.md"; rm -f "$tmp/docs/unreadable.md"

  # A target that is a single file (README.md) is scanned, not only directories.
  printf 'Agents run the city.\n' > "$tmp/README.md"
  if [ -z "$(scan "$tmp/README.md")" ]; then
    echo "  FAIL: a single-file target was not scanned" >&2; fails=1
  else
    echo "  ok: a single-file target is scanned"
  fi

  [ "$fails" -eq 0 ] || exit 1
  echo "==> Self-test passed."
  exit 0
fi

# Floor: a moved or renamed docs tree must not pass by scanning nothing.
count=0
while IFS= read -r -d '' _; do count=$((count + 1)); done < <(files "${TARGETS[@]}")
if [ "$count" -lt 200 ]; then
  echo "check-docs-claims: only $count files to scan (floor 200); the targets moved, fix TARGETS" >&2
  exit 2
fi

hits="$(scan "${TARGETS[@]}")"
if [ -n "$hits" ]; then
  cat >&2 <<EOF

==> Forbidden public claims found

$hits

These are claims DeviceChain must not make on a published surface. Use the
honest wording instead (see the header of hack/check-docs-claims.sh), and only
once the feature that backs it has shipped.

EOF
  exit 1
fi

echo "==> No forbidden public claims in $count published files."
