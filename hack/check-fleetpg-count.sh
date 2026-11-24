#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Reads `go test -json` output on stdin and requires that at least MIN tests whose name starts
# with the prefix (default TestFleetPG) EXECUTED AND PASSED, and that none failed or was skipped.
#
# WHY. backend/fleet's real-Postgres tests live behind the build tag `fleetpg`. Without the tag
# they are not compiled, so a mistyped tag, a renamed prefix or a package that stopped matching
# produces `ok ... [no tests to run]` and a green build over a suite that never ran. Exit status
# alone cannot tell that from success, so the COUNT of passed tests is asserted instead.
#
# Usage:
#   go test -tags fleetpg -count=1 -json ./... | hack/check-fleetpg-count.sh [PREFIX [MIN]]
#   hack/check-fleetpg-count.sh --self-test
set -euo pipefail

check() {
	local prefix="${1:-TestFleetPG}" min="${2:-1}" exact_top="${3:-}" input passed failed skipped top
	input="$(cat)"
	# Every line must parse: truncated or non-JSON output (a build failure prints plain text)
	# is not evidence that tests passed.
	if ! printf '%s\n' "$input" | jq -e -s 'length > 0' >/dev/null 2>&1; then
		echo "FAIL: no parsable go test -json output on stdin" >&2
		return 1
	fi
	passed="$(printf '%s\n' "$input" | jq -s --arg p "$prefix" '[.[] | select(.Action == "pass" and (.Test // "" | startswith($p)))] | length')"
	failed="$(printf '%s\n' "$input" | jq -s '[.[] | select(.Action == "fail")] | length')"
	skipped="$(printf '%s\n' "$input" | jq -s --arg p "$prefix" '[.[] | select(.Action == "skip" and (.Test // "" | startswith($p)))] | length')"
	if [ "$failed" -ne 0 ]; then
		echo "FAIL: $failed failing test or package event(s) in the go test output" >&2
		return 1
	fi
	if [ "$skipped" -ne 0 ]; then
		echo "FAIL: $skipped $prefix* test(s) SKIPPED; the real-Postgres suite must never skip" >&2
		return 1
	fi
	# EXACT_TOP (third argument): the number of TOP-LEVEL tests (no subtest) that passed must be
	# exactly this. A floor lets a test be deleted and another added with the count unchanged, or a
	# test go missing while its subtests keep the total up; an exact top-level count notices both.
	if [ -n "$exact_top" ]; then
		top="$(printf '%s\n' "$input" | jq -s --arg p "$prefix" '[.[] | select(.Action == "pass" and (.Test // "" | startswith($p)) and ((.Test | contains("/")) | not))] | length')"
		if [ "$top" -ne "$exact_top" ]; then
			echo "FAIL: $top top-level $prefix* test(s) passed, want exactly $exact_top: a test was added, removed or renamed without updating the count" >&2
			return 1
		fi
	fi
	if [ "$passed" -lt "$min" ]; then
		echo "FAIL: only $passed $prefix* test(s) passed, want at least $min: the tagged suite did not run (a mistyped build tag?)" >&2
		return 1
	fi
	echo "ok: $passed $prefix* test(s) executed and passed"
}

self_test() {
	local rc=0 good bad
	good='{"Action":"run","Test":"TestFleetPGOne"}
{"Action":"pass","Test":"TestFleetPGOne"}
{"Action":"pass","Package":"x"}'
	# A normal run passes.
	printf '%s\n' "$good" | check >/dev/null || { echo "SELF-TEST FAIL: a good run was refused" >&2; rc=1; }
	# NEGATIVE CONTROL 1: a mistyped tag compiles nothing: only a package-level pass is reported.
	bad='{"Action":"output","Output":"ok  \tx\t0.01s [no tests to run]\n"}
{"Action":"pass","Package":"x"}'
	if printf '%s\n' "$bad" | check >/dev/null 2>&1; then echo "SELF-TEST FAIL: zero executed tests was accepted" >&2; rc=1; fi
	# NEGATIVE CONTROL 2: a different prefix's passing tests do not count.
	if printf '%s\n' '{"Action":"pass","Test":"TestOther"}' | check >/dev/null 2>&1; then echo "SELF-TEST FAIL: tests outside the prefix were counted" >&2; rc=1; fi
	# NEGATIVE CONTROL 3: a skip is a failure.
	if printf '%s\n%s\n' "$good" '{"Action":"skip","Test":"TestFleetPGTwo"}' | check >/dev/null 2>&1; then echo "SELF-TEST FAIL: a skipped test was accepted" >&2; rc=1; fi
	# NEGATIVE CONTROL 4: a failure anywhere is a failure, even with passes beside it.
	if printf '%s\n%s\n' "$good" '{"Action":"fail","Test":"TestFleetPGThree"}' | check >/dev/null 2>&1; then echo "SELF-TEST FAIL: a failing test was accepted" >&2; rc=1; fi
	# NEGATIVE CONTROL 5: empty and non-JSON input.
	if printf '' | check >/dev/null 2>&1; then echo "SELF-TEST FAIL: empty input was accepted" >&2; rc=1; fi
	if printf 'build failed\n' | check >/dev/null 2>&1; then echo "SELF-TEST FAIL: non-JSON input was accepted" >&2; rc=1; fi
	# MIN is honoured.
	if printf '%s\n' "$good" | check TestFleetPG 2 >/dev/null 2>&1; then echo "SELF-TEST FAIL: min=2 accepted one test" >&2; rc=1; fi
	# EXACT_TOP: a subtest is not a top-level test; too many and too few both fail.
	sub='{"Action":"pass","Test":"TestFleetPGOne/sub"}'
	if ! printf '%s\n%s\n' "$good" "$sub" | check TestFleetPG 1 1 >/dev/null 2>&1; then echo "SELF-TEST FAIL: a subtest was counted as a top-level test" >&2; rc=1; fi
	if printf '%s\n' "$good" | check TestFleetPG 1 2 >/dev/null 2>&1; then echo "SELF-TEST FAIL: exact=2 accepted one top-level test" >&2; rc=1; fi
	if printf '%s\n%s\n' "$good" '{"Action":"pass","Test":"TestFleetPGTwo"}' | check TestFleetPG 1 1 >/dev/null 2>&1; then echo "SELF-TEST FAIL: exact=1 accepted two top-level tests" >&2; rc=1; fi
	[ "$rc" -eq 0 ] && echo "SELF-TEST PASS: the count check refuses zero, skipped, failed, foreign-prefix and non-JSON runs"
	return "$rc"
}

case "${1:-}" in
--self-test) self_test ;;
*) check "$@" ;;
esac
