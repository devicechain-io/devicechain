#!/usr/bin/env bash
# Print every OpenTofu ROOT in this repo, one per line, relative to the repo root.
#
# WHY THIS EXISTS
#
# Every OpenTofu gate in this repo hardcodes `deploy/opentofu`, because for the
# whole life of the tree there has been exactly one root. The moment a second one
# appears, each of those gates keeps passing while covering only the first:
#
#   ci.yml's opentofu job      `working-directory: deploy/opentofu` — the second
#                              root is never init'd and never validated at all
#   check-tofu-validations.sh  a single `cd`; validation blocks in the new root
#                              go untested, which is the one thing it exists for
#   check-chart-pins.sh        hardcodes main.tf AND variables.tf by filename, so
#                              pins that move to a new root leave it scanning an
#                              empty set and reporting success
#
# 🔴 That is this project's most expensive failure shape, and it has a name: a
# gate that keeps watching where the risk USED to be. It is worse here than
# usual, because the gates do not go red and get fixed — they go green over
# nothing, and the only signal is an install failing on somebody else's machine.
#
# So: gates ask this script what to iterate over, and gain coverage of a new root
# on the day it is added rather than on the day someone remembers to widen them.
#
# WHAT COUNTS AS A ROOT
#
# A root is a directory of .tf files that tofu is meant to init/plan/apply
# directly. A MODULE also holds .tf files and is NOT a root: it is reached only
# through a `source = "./modules/..."` reference, `tofu init` in one is
# meaningless, and `tofu validate` there reports errors about variables the
# caller supplies. Telling them apart is the whole job.
#
# The rule is structural, not a list: any directory under deploy/opentofu that
# holds at least one *.tf file and has no path segment named `modules`. That
# survives both shapes the split might take — a new sibling beside today's root,
# or a move to deploy/opentofu/{cluster,instance}/ with modules/ shared — without
# this file having to know which was chosen.
#
# Usage:
#   hack/tofu-roots.sh                 # print the roots
#   hack/tofu-roots.sh --tree <dir>    # discover under <dir> instead (tests)
#   hack/tofu-roots.sh --self-test     # prove discovery can fail
#
# 🔴 IT EXITS NON-ZERO WHEN IT FINDS NOTHING, and every caller must let that
# propagate. A discovery helper that can return an empty list quietly is a way to
# turn every gate downstream of it into a no-op at once — one bad refactor here
# would silently disarm all of them, which is a larger blast radius than any of
# the gates has on its own.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

discover() {
	local tree="$1"
	[ -d "$tree" ] || return 0
	# -print on the .tf file, then take its directory: a directory qualifies by
	# holding a .tf, not by being named anything in particular.
	find "$tree" -type f -name '*.tf' -print 2>/dev/null |
		while IFS= read -r f; do dirname "$f"; done |
		grep -v -E '(^|/)(modules|\.terraform)(/|$)' |
		sort -u
}

self_test() {
	local tmp
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" EXIT

	local rc=0

	# 1. An empty tree must FAIL, not return success with no roots. This is the
	#    property every caller depends on.
	if out="$(discover "$tmp/nothing-here")" && [ -z "$out" ]; then
		: # discover itself returns empty; the exit check below is what must fire
	fi
	if roots_or_die "$tmp/nothing-here" >/dev/null 2>&1; then
		echo "SELF-TEST FAIL: an empty tree was reported as success" >&2
		rc=1
	fi

	# 2. A module is not a root.
	mkdir -p "$tmp/tree/modules/nats"
	echo 'variable "x" {}' >"$tmp/tree/modules/nats/main.tf"
	if [ -n "$(discover "$tmp/tree")" ]; then
		echo "SELF-TEST FAIL: a directory under modules/ was reported as a root" >&2
		rc=1
	fi

	# 3. The real shape: one root today, two after the split, and modules/ still
	#    excluded in both.
	echo 'resource "null_resource" "a" {}' >"$tmp/tree/main.tf"
	if [ "$(discover "$tmp/tree" | wc -l)" -ne 1 ]; then
		echo "SELF-TEST FAIL: expected exactly 1 root, got: $(discover "$tmp/tree")" >&2
		rc=1
	fi
	mkdir -p "$tmp/tree/cluster"
	echo 'resource "null_resource" "b" {}' >"$tmp/tree/cluster/main.tf"
	if [ "$(discover "$tmp/tree" | wc -l)" -ne 2 ]; then
		echo "SELF-TEST FAIL: a second root was not discovered; got: $(discover "$tmp/tree")" >&2
		rc=1
	fi

	# 4. Nested modules under a new root are excluded too — the segment rule, not
	#    a top-level-only rule.
	mkdir -p "$tmp/tree/cluster/modules/thing"
	echo 'variable "y" {}' >"$tmp/tree/cluster/modules/thing/main.tf"
	if [ "$(discover "$tmp/tree" | wc -l)" -ne 2 ]; then
		echo "SELF-TEST FAIL: a module nested under a new root was counted as a root" >&2
		rc=1
	fi

	if [ "$rc" -eq 0 ]; then
		echo "SELF-TEST PASS: discovery finds roots, excludes modules, and fails on an empty tree"
	fi
	return "$rc"
}

roots_or_die() {
	local tree="$1"
	local out
	out="$(discover "$tree")"
	if [ -z "$out" ]; then
		echo "FAIL: no OpenTofu roots found under $tree." >&2
		echo "      Every gate that iterates over roots would now check NOTHING and report" >&2
		echo "      success, so this is an error rather than an empty list." >&2
		return 1
	fi
	printf '%s\n' "$out"
}

case "${1:-}" in
--self-test)
	self_test
	;;
--tree)
	[ $# -ge 2 ] || {
		echo "FAIL: --tree needs a directory" >&2
		exit 1
	}
	roots_or_die "$2"
	;;
"")
	roots_or_die "$repo_root/deploy/opentofu" | sed "s#^$repo_root/##"
	;;
*)
	echo "FAIL: unknown argument $1" >&2
	exit 1
	;;
esac
