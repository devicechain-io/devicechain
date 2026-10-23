#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Does the dcctl command tree still accept everything it accepted at the last release?
#
#   hack/check-cli-compat.sh            # baseline: nearest stable tag reachable from HEAD
#   hack/check-cli-compat.sh v0.24.0    # baseline: the tag given
#   hack/check-cli-compat.sh --self-test # prove the check can fail, through this script
#
# Builds the baseline release's own dcctl command tree (git archive of the tag, then a
# test injected through `go test -overlay` walks cobra's tree and writes every command,
# alias and flag - name, shorthand, type, default, hidden, deprecated) and the working
# tree's, and fails on a removed command, alias or flag, a changed flag type, or a changed
# or dropped shorthand, unless declared in backend/tools/docsgql/apicompat/clitree/breaks.txt.
# A changed default, a newly hidden or deprecated command or flag are reported, never failing.
#
# Exit 1 is a break (or a stale or invalid declaration); anything that stops the
# comparison from happening - no tag, no baseline, a build that fails - is a different
# message, so a red check says which of the two it is.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/api-baseline.sh
. "$ROOT/hack/api-baseline.sh"

BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT
build_apicompat "$BIN_DIR"
APICOMPAT="$BIN_DIR/apicompat"

self_test() {
  local tmp out rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN

  # The baseline is a COPY OF THE WORKING TREE, so the only difference from it is what is
  # planted here. The plant runs last in package init (a file name sorting after every
  # other), when every command is registered: a flag and a command the working tree does
  # not have (so they are REMOVED relative to it), and a changed default.
  mkdir -p "$tmp/base"
  copy_workspace "$tmp/base"
  cat > "$tmp/base/backend/cli/cmd/zz_selftest_probe.go" <<'GO'
package cmd

import "github.com/spf13/cobra"

func init() {
	c, _, err := rootCmd.Find([]string{"bootstrap"})
	if err != nil || c == rootCmd {
		panic("self-test: the bootstrap command is gone; pick another command to plant the probe on")
	}
	c.Flags().String("zz-selftest-flag", "", "probe")
	f := c.Flags().Lookup("cluster")
	if f == nil {
		panic("self-test: bootstrap --cluster is gone; pick another flag to change the default of")
	}
	f.DefValue = "zz-selftest-default"
	// The working tree requires `docs --out`; the planted baseline did not, so the
	// working tree newly requires it - an invocation the baseline accepted is refused.
	d, _, err := rootCmd.Find([]string{"docs"})
	if err != nil || d == rootCmd || d.Flags().Lookup("out") == nil {
		panic("self-test: docs --out is gone; pick another required flag to unrequire")
	}
	if _, ok := d.Flags().Lookup("out").Annotations[cobra.BashCompOneRequiredFlag]; !ok {
		panic("self-test: docs --out is no longer required; pick another required flag")
	}
	delete(d.Flags().Lookup("out").Annotations, cobra.BashCompOneRequiredFlag)
	// A persistent flag is accepted by every command beneath the one that declares it, so
	// removing it must fail on the CHILD too: that line is only there if the export lists
	// inherited flags.
	l, _, err := rootCmd.Find([]string{"dead-letters", "list"})
	if err != nil || l.Name() != "list" || l.Parent().Name() != "dead-letters" {
		panic("self-test: dead-letters list is gone; pick another parent with a child")
	}
	l.Parent().PersistentFlags().String("zz-selftest-inherited", "", "probe")
	rootCmd.AddCommand(&cobra.Command{Use: "zz-selftest-command", Run: func(*cobra.Command, []string) {}})
}
GO
  echo "self-test: exporting the planted baseline and the working tree ..."
  "$APICOMPAT" export-cli -tree "$tmp/base" -out "$tmp/base.json" || return 1
  "$APICOMPAT" export-cli -tree "$ROOT" -out "$tmp/head.json" || return 1
  grep -q '"path": "bootstrap"' "$tmp/head.json" || { echo "self-test: the export holds no bootstrap command; it walked nothing" >&2; return 1; }
  : > "$tmp/none.txt"

  # 1. The working tree against itself: no change at all, so the check must pass and report
  #    0 breaking. This is the case that proves the check does not cry wolf on today's tree.
  rc=0; out="$("$APICOMPAT" cli -baseline v0.0.0 -base-json "$tmp/head.json" -head-json "$tmp/head.json" -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "identical trees" 0 "$rc" "$out" || return 1
  check_has "identical trees" ' 0 breaking (0 declared), 0 dangerous, 0 safe; 0 failed' "$out" || return 1

  # 2. The baseline had a flag and a command the working tree does not: removed. The check
  #    must FAIL and name each. The changed default is only reported.
  rc=0; out="$("$APICOMPAT" cli -baseline v0.0.0 -base-json "$tmp/base.json" -head-json "$tmp/head.json" -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "a removed flag and command" 1 "$rc" "$out" || return 1
  check_has "removed flag" 'bootstrap:--zz-selftest-flag flag-removed' "$out" || return 1
  check_has "removed command" 'zz-selftest-command command-removed' "$out" || return 1
  check_has "changed default" 'dangerous dcctl: bootstrap:--cluster flag-default-changed' "$out" || return 1
  check_has "newly required flag" 'docs:--out flag-required' "$out" || return 1
  check_has "removed persistent flag, on the child" 'dead-letters.list:--zz-selftest-inherited flag-removed' "$out" || return 1

  # 3. The same removals, declared against this baseline: passes, and counts them declared.
  cat > "$tmp/declared.txt" <<'TXT'
v0.0.0 dcctl bootstrap:--zz-selftest-flag flag-removed self-test probe
v0.0.0 dcctl zz-selftest-command command-removed self-test probe
v0.0.0 dcctl docs:--out flag-required self-test probe
v0.0.0 dcctl dead-letters:--zz-selftest-inherited flag-removed self-test probe
v0.0.0 dcctl dead-letters.list:--zz-selftest-inherited flag-removed self-test probe
TXT
  rc=0; out="$("$APICOMPAT" cli -baseline v0.0.0 -base-json "$tmp/base.json" -head-json "$tmp/head.json" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "declared removals" 0 "$rc" "$out" || return 1
  check_has "declared removals" '5 breaking (5 declared)' "$out" || return 1

  # 4. The declarations against a DIFFERENT baseline are expired: they cover nothing, so the
  #    removals fail again.
  rc=0; out="$("$APICOMPAT" cli -baseline v0.0.1 -base-json "$tmp/base.json" -head-json "$tmp/head.json" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "expired declarations" 1 "$rc" "$out" || return 1
  check_has "expired declarations" 'expired' "$out" || return 1

  # 5. A declaration with no change behind it is stale and fails.
  rc=0; out="$("$APICOMPAT" cli -baseline v0.0.0 -base-json "$tmp/head.json" -head-json "$tmp/head.json" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "stale declarations" 1 "$rc" "$out" || return 1
  check_has "stale declarations" 'but that change is not in the dcctl command tree' "$out" || return 1

  # 6. The wrapper's own reading of the exit status, which every real run goes through.
  verdict_self_test || return 1

  echo "cli-compat self-test: identical trees pass; a removed flag (inherited ones on every child), a removed command and a newly required flag fail naming themselves; a changed default is reported; declared passes; expired and stale fail; exit 1 and exit 2 both fail the wrapper"
}

case "${1:-}" in
  --self-test)
    [ $# -eq 1 ] || { echo "usage: $0 --self-test" >&2; exit 2; }
    self_test
    exit
    ;;
esac

if [ $# -gt 1 ]; then
  echo "usage: $0 [baseline-tag | --self-test]" >&2
  exit 2
fi

baseline="$(resolve_baseline "${1:-}")" || exit 1
echo "dcctl compatibility baseline: $baseline"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp" "$BIN_DIR"' EXIT
extract_baseline "$baseline" "$tmp" || exit 1

rc=0
"$APICOMPAT" cli -head "$ROOT" -baseline "$baseline" -base "$tmp" >"$tmp/cli.log" 2>&1 || rc=$?
cat "$tmp/cli.log"
summarize "dcctl command tree against $baseline" "$tmp/cli.log"
verdict "dcctl command tree" "$baseline" "$rc"
