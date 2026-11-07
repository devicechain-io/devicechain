#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Turn govulncheck JSON reports into ONE OpenVEX document.
#
#   hack/vex-from-govulncheck.sh --commit <sha> --out vex.openvex.json \
#       [--allowlist hack/govulncheck-allow.txt] [--timestamp RFC3339] \
#       <module-dir>=<govulncheck.json> [<module-dir>=<govulncheck.json> ...]
#   hack/vex-from-govulncheck.sh --self-test
#
# Reports are produced by `govulncheck -format json ./...` (hack/govulncheck.sh keeps them when
# GOVULNCHECK_JSON_DIR is set). One statement is written per (module directory, advisory):
#
#   - the call graph reaches the vulnerable function  -> status "affected", with an
#     action_statement (the reviewed allowlist reason, or the fixed version, or "no fix yet");
#   - the advisory matches a module we require, but no call path reaches it
#                                                     -> status "not_affected", justification
#     "vulnerable_code_not_in_execute_path" (the status OpenVEX defines for exactly this).
#
# 🔴 A VEX statement is a CLAIM about reachability, so the failure that matters is a document
# that says "not_affected" because the scan never ran. In JSON mode govulncheck exits 0 whether
# or not it found anything, so a report with no "config" record (a scan that did not complete)
# is REFUSED here, exactly as hack/govulncheck.sh refuses it. An empty set of reports is refused
# too: a VEX document with no statements is indistinguishable from a clean bill of health.
#
# Needs: jq.
set -euo pipefail

SELF="$(basename "$0")"
die() { echo "$SELF: $*" >&2; exit 2; }

# statements_for <module-dir> <report.json> <allow-reasons.tsv> <commit>
# Prints a JSON array of OpenVEX statements for one report.
statements_for() {
  local module="$1" report="$2" reasons="$3" commit="$4"
  jq -e -s 'any(.[]; .config)' "$report" >/dev/null 2>&1 \
    || die "$module: $report has no govulncheck config record -- the scan did not complete; refusing to state anything about it"
  local reasons_json
  reasons_json="$(jq -R -s 'split("\n") | map(select(length>0) | split("\t") | {(.[0]): .[1]}) | add // {}' "$reasons")"
  jq -s --arg mdir "$module" --arg commit "$commit" --argjson reasons "$reasons_json" '
    . as $all
    | ([$all[] | select(.osv) | .osv | {key: .id, value: .}] | from_entries) as $osvs
    | [$all[] | select(.finding)] as $findings
    | ($findings | map(.finding.osv) | unique) as $ids
    | [ $ids[] as $id
        | ($findings | map(.finding) | map(select(.osv == $id))) as $fs
        | ($fs | map(select(.trace[0].function != null))) as $reach
        | ($osvs[$id] // {}) as $o
        | {
            vulnerability: ({
              "@id": ("https://pkg.go.dev/vuln/" + $id),
              name: $id
            } + (if ($o.aliases // []) | length > 0 then {aliases: $o.aliases} else {} end)
              + (if ($o.summary // "") != "" then {description: $o.summary} else {} end)),
            products: [{
              "@id": ("pkg:github/devicechain-io/devicechain@" + $commit + "#" + $mdir),
              subcomponents: ([$fs[] | .trace[0] | select(.module != null)
                | {"@id": ("pkg:golang/" + .module + (if .version then "@" + .version else "" end))}] | unique)
            }]
          }
          + (if ($reach | length) > 0 then
              {
                status: "affected",
                action_statement:
                  (if $reasons[$id] then "Reviewed exception, no fixed version available: " + $reasons[$id]
                   elif ($reach[0].fixed_version // "") != "" then "Upgrade " + ($reach[0].trace[0].module // "the module") + " to " + $reach[0].fixed_version + " or later."
                   else "Reachable; no fixed version is published yet. Tracked until one is." end)
              }
            else
              {
                status: "not_affected",
                justification: "vulnerable_code_not_in_execute_path",
                impact_statement: "govulncheck call-graph analysis found no path from the code in this module directory to the vulnerable symbol."
              }
            end)
      ]' "$report"
}

# allow_reasons <module-dir> <allowlist> -> "OSV<TAB>reason" lines
allow_reasons() {
  [ -f "$2" ] || return 0
  awk -v m="$1" '
    /^[[:space:]]*(#|$)/ { next }
    $1 == m && $0 ~ /#/ { i = index($0, "#"); r = substr($0, i + 1); sub(/^[[:space:]]+/, "", r); printf("%s\t%s\n", $2, r) }' "$2"
}

build() {
  local commit="" out="" allow="hack/govulncheck-allow.txt" ts="" pairs=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --commit) commit="${2:?}"; shift 2 ;;
      --out) out="${2:?}"; shift 2 ;;
      --allowlist) allow="${2:?}"; shift 2 ;;
      --timestamp) ts="${2:?}"; shift 2 ;;
      -*) die "unknown option $1" ;;
      *=*) pairs+=("$1"); shift ;;
      *) die "expected <module-dir>=<report.json>, got '$1'" ;;
    esac
  done
  command -v jq >/dev/null || die "jq is required"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "--commit must be a full 40-hex commit"
  [ -n "$out" ] || die "--out is required"
  [ "${#pairs[@]}" -gt 0 ] || die "no reports given: a VEX document with no statements would read as a clean bill of health"
  [ -n "$ts" ] || ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  local work parts=() p mod rep n=0
  work="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand $work now
  trap "rm -rf '$work'" EXIT
  for p in "${pairs[@]}"; do
    mod="${p%%=*}"; rep="${p#*=}"
    [ -s "$rep" ] || die "$mod: report '$rep' is missing or empty"
    allow_reasons "$mod" "$allow" > "$work/reasons.tsv"
    statements_for "$mod" "$rep" "$work/reasons.tsv" "$commit" > "$work/s$n.json"
    parts+=("$work/s$n.json")
    n=$((n + 1))
  done
  jq -s --arg ts "$ts" --arg commit "$commit" '
    (add | sort_by(.vulnerability.name, .products[0]."@id")) as $st
    | {
        "@context": "https://openvex.dev/ns/v0.2.0",
        "@id": ("https://devicechain.io/vex/" + $commit + "/" + $ts),
        author: "DeviceChain release automation",
        role: "Document Creator",
        timestamp: $ts,
        version: 1,
        tooling: "hack/vex-from-govulncheck.sh (govulncheck call-graph analysis)",
        statements: $st
      }' "${parts[@]}" > "$out"
  echo "$SELF: wrote $out ($(jq '.statements|length' "$out") statements, $(jq '[.statements[]|select(.status=="affected")]|length' "$out") affected)"
}

self_test() {
  local tmp bad=0 sha
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT
  sha=0123456789abcdef0123456789abcdef01234567
  cat > "$tmp/r.json" <<'J'
{"config":{"scanner_name":"govulncheck"}}
{"osv":{"id":"GO-2026-5932","aliases":["CVE-2026-0001"],"summary":"openpgp is unmaintained"}}
{"osv":{"id":"GO-2026-9999","summary":"required but not called"}}
{"osv":{"id":"GO-2026-7777","summary":"reachable, fixable"}}
{"finding":{"osv":"GO-2026-5932","trace":[{"module":"golang.org/x/crypto","version":"v0.1.0","package":"golang.org/x/crypto/openpgp","function":"init"}]}}
{"finding":{"osv":"GO-2026-5932","trace":[{"module":"golang.org/x/crypto","version":"v0.1.0"}]}}
{"finding":{"osv":"GO-2026-9999","trace":[{"module":"example.com/lib","version":"v1.0.0"}]}}
{"finding":{"osv":"GO-2026-7777","fixed_version":"v2.0.1","trace":[{"module":"example.com/other","version":"v2.0.0","function":"Do"}]}}
J
  printf 'backend/cli GO-2026-5932 # helm v3 init chain; no fixed version exists\n' > "$tmp/allow.txt"
  "$0" --commit "$sha" --out "$tmp/v.json" --allowlist "$tmp/allow.txt" --timestamp 2026-11-07T00:00:00Z backend/cli="$tmp/r.json" >/dev/null
  chk() { # <description> <jq-filter-that-must-be-true>
    jq -e "$2" "$tmp/v.json" >/dev/null || { echo "FAIL: $1"; bad=1; }
  }
  chk "document is OpenVEX v0.2.0 with one statement per advisory" '."@context"=="https://openvex.dev/ns/v0.2.0" and (.statements|length)==3'
  chk "an unreachable advisory is not_affected with the execute-path justification" \
    '.statements[]|select(.vulnerability.name=="GO-2026-9999")|.status=="not_affected" and .justification=="vulnerable_code_not_in_execute_path"'
  chk "an advisory with a reachable row AND a module row is affected, never not_affected" \
    '.statements[]|select(.vulnerability.name=="GO-2026-5932")|.status=="affected"'
  chk "an allowlisted reachable advisory carries the reviewed reason" \
    '.statements[]|select(.vulnerability.name=="GO-2026-5932")|.action_statement|contains("helm v3 init chain")'
  chk "a fixable reachable advisory names the fixed version" \
    '.statements[]|select(.vulnerability.name=="GO-2026-7777")|.action_statement|contains("v2.0.1")'
  chk "every affected statement has an action_statement (OpenVEX requires it)" \
    'all(.statements[]|select(.status=="affected"); (.action_statement|length)>0)'
  chk "the product names the commit and module dir" \
    '.statements[0].products[0]."@id"|contains("0123456789abcdef0123456789abcdef01234567#backend/cli")'
  # Negative controls: the generator must refuse input that cannot support a claim, and say why.
  refuses() { # <what> <reason-regexp> <args...>
    local what="$1" want="$2" err; shift 2
    if err="$("$0" "$@" 2>&1 >/dev/null)"; then echo "FAIL: accepted $what"; bad=1
    elif ! grep -qE "$want" <<<"$err"; then echo "FAIL: refused $what for the wrong reason: $err"; bad=1; fi
  }
  : > "$tmp/empty.json"
  refuses "an empty report" "missing or empty" --commit "$sha" --out "$tmp/x.json" backend/cli="$tmp/empty.json"
  printf '{"finding":{"osv":"GO-1","trace":[{"module":"m"}]}}
' > "$tmp/noconfig.json"
  refuses "a report with no config record (scan never completed)" "no govulncheck config record" --commit "$sha" --out "$tmp/x.json" backend/cli="$tmp/noconfig.json"
  refuses "zero reports" "no reports given" --commit "$sha" --out "$tmp/x.json"
  refuses "a short commit" "40-hex" --commit abc --out "$tmp/x.json" backend/cli="$tmp/r.json"
  if [ "$bad" -eq 0 ]; then echo "$SELF self-test: ok"; else echo "$SELF self-test: FAILED"; return 1; fi
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
build "$@"
